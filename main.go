package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"lanoclip/internal/lancoclip"
	"lanoclip/internal/server"
	"lanoclip/internal/store"
	"lanoclip/internal/tray"
)

//go:embed web
var webFS embed.FS

func main() {
	port := flag.Int("port", 6116, "HTTP listen port")
	trayMode := flag.Bool("tray", false, "run as a macOS menu bar app (auto-enabled inside a .app bundle)")
	dataDir := flag.String("data", "", "data directory (default: user config dir, or $LANOCLIP_DIR)")
	flag.Parse()

	// A double-clicked .app (macOS) or a GUI-subsystem .exe (Windows) has no
	// terminal, so keep logs next to the data.
	inBundle := runningInAppBundle()
	if inBundle || runtime.GOOS == "windows" {
		setupFileLogging()
	}

	st, err := store.NewStore(*dataDir)
	if err != nil {
		log.Fatalf("init store: %v", err)
	}

	srv := server.New(st, webFS)

	// 单实例：程序已经在跑时（再次双击 .app / exe）不再起第二个监听，
	// 而是让已在运行的那个实例弹「已在运行」通知，本进程随即退出。
	if url, ok := server.RunningInstanceURL(*port, server.PortScanRange); ok {
		log.Printf("LanCoClip 已在运行（%s）：已通知该实例，本进程退出", url)
		return
	}

	// Bind first, so a busy port can fall back to the next free one and the
	// share URL always reflects the port actually in use.
	ln, err := server.ListenFirstFree(*port, server.PortScanRange)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	srv.SetBoundPort(ln.Addr().(*net.TCPAddr).Port)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	onCaptured := func(what string) {
		log.Printf("captured %s", what)
		if err := st.Save(); err != nil {
			log.Printf("save: %v", err)
		}
		tray.UpdateHistory(srv.Recent(tray.HistoryCount))
	}
	go lancoclip.Watch(ctx, srv.Suppressor(),
		func(content string) {
			if st.AddText(content) {
				onCaptured(fmt.Sprintf("%d chars of text", len([]rune(content))))
			}
		},
		func(png []byte) {
			added, err := st.AddImage(png)
			if err != nil {
				log.Printf("save image: %v", err)
				return
			}
			if added {
				onCaptured(fmt.Sprintf("image (%d KB)", len(png)/1024))
			}
		},
	)

	// Periodic flush to disk.
	go func() {
		t := time.NewTicker(3 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := st.Save(); err != nil {
					log.Printf("save: %v", err)
				}
			}
		}
	}()

	httpSrv := &http.Server{
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("lancoclip service listening on %s", srv.URL())
		log.Printf("open this URL on your phone (same Wi-Fi): %s", srv.URL())
		if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("serve: %v", err)
		}
	}()

	// Windows 上程序就是托盘应用；macOS 上双击 .app 即进入菜单栏模式
	useTray := tray.Supported() && (*trayMode || inBundle || runtime.GOOS == "windows")
	if useTray {
		// SIGINT / SIGTERM should also stop the menu-bar loop.
		go func() { <-ctx.Done(); tray.Quit() }()
		log.Println("menu bar mode: use the menu bar icon to open the page or quit")
		tray.Run(srv, tray.Options{
			HotkeyQR:      st.HotkeyQR(),
			HotkeyHistory: st.HotkeyHistory(),
		}) // blocks on the main thread until the user quits
		stop()
	} else {
		<-ctx.Done()
	}

	log.Println("shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	_ = st.Save()
}

// runningInAppBundle reports whether the executable lives inside a macOS .app
// bundle (i.e. it was launched by double-clicking from Finder).
func runningInAppBundle() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return runtime.GOOS == "darwin" &&
		strings.Contains(exe, ".app/Contents/MacOS/")
}

// setupFileLogging also mirrors the standard logger to a file in the user config
// directory, so menu-bar / tray launches (which have no terminal) still leave a
// trail while a terminal run still prints to stderr.
func setupFileLogging() {
	dir, err := store.DefaultDir()
	if err != nil {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "app.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	log.SetOutput(io.MultiWriter(os.Stderr, f))
}
