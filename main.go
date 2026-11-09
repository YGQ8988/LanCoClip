package main

import (
	"context"
	"embed"
	"errors"
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
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"

	"lanoclip/internal/instancelock"
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
	closeLog := func() {}
	if inBundle || runtime.GOOS == "windows" {
		closeLog = setupFileLogging(*dataDir)
	}
	defer closeLog()

	// 数据目录的跨进程锁必须在 NewStore 之前拿：NewStore 启动时会扫描
	// images/ 回收无主文件，第二个进程若先跑到那一步，可能删掉第一个进程
	// 刚写入、还没 flush 到 images.json 的图片。端口探测只保护监听端口，
	// 两个进程同时启动会各绑相邻端口，挡不住这条路径。
	lock, lockErr := instancelock.Acquire(store.ResolveDir(*dataDir))
	if errors.Is(lockErr, instancelock.ErrAlreadyRunning) {
		log.Printf("另一个 LanCoClip 实例正在使用同一数据目录，本进程退出")
		// 顺带让已在运行的实例弹「已在运行」通知（探测本身即触发）。
		if url, ok := server.RunningInstanceURL(*port, server.PortScanRange); ok {
			log.Printf("LanCoClip 已在运行（%s）", url)
		}
		return
	}
	if lockErr != nil {
		// 文件系统不支持锁时不能把用户锁死在门外，记日志后继续启动。
		log.Printf("instance lock unavailable (%v); continuing anyway", lockErr)
	}
	if lock != nil {
		defer func() { _ = lock.Release() }()
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

	// 连续复制时不必每条都全量 Marshal 500 条文本 + 图片元数据再写盘：
	// 500ms 内的捕获合并成一次写；下面的 3s ticker 仍作为兜底，保证持续
	// 捕获时也不会一直推迟落盘。
	var saveMu sync.Mutex
	var saveTimer *time.Timer
	requestSave := func() {
		saveMu.Lock()
		defer saveMu.Unlock()
		if saveTimer != nil {
			saveTimer.Stop()
		}
		saveTimer = time.AfterFunc(500*time.Millisecond, func() {
			if err := st.Save(); err != nil {
				log.Printf("save: %v", err)
			}
		})
	}
	defer func() {
		saveMu.Lock()
		if saveTimer != nil {
			saveTimer.Stop()
		}
		saveMu.Unlock()
	}()

	onCaptured := func(what string) {
		log.Printf("captured %s", what)
		requestSave()
		tray.UpdateHistory(srv.Recent(tray.HistoryCount))
	}
	safeGo("clipboard watch", func() {
		lancoclip.Watch(ctx, srv.Suppressor(),
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
	})

	// Periodic flush to disk.
	safeGo("save ticker", func() {
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
	})

	httpSrv := &http.Server{
		Handler: srv.Routes(),
		// 上传/下载图片可能到几十 MB，WriteTimeout 放宽到 60s 避免慢一点的
		// 局域网被误断；IdleTimeout 负责回收长期占着的空闲连接。
		ReadHeaderTimeout: 10 * time.Second,
		// 只声明超大 Content-Length 却不发 body 的慢速请求会一直占住
		// goroutine 与连接（原来只有 ReadHeaderTimeout，管不到 body）。
		// 120s 足够慢速 Wi-Fi 传完 32MiB 图片，同时给这类连接一个上限。
		ReadTimeout:    120 * time.Second,
		WriteTimeout:   60 * time.Second,
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}

	safeGo("http serve", func() {
		log.Printf("lancoclip service listening on %s", srv.URL())
		log.Printf("open this URL on your phone (same Wi-Fi): %s", srv.URL())
		if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("serve: %v", err)
		}
	})

	// Windows 上程序就是托盘应用；macOS 上双击 .app 即进入菜单栏模式
	useTray := tray.Supported() && (*trayMode || inBundle || runtime.GOOS == "windows")
	if useTray {
		// SIGINT / SIGTERM should also stop the menu-bar loop.
		safeGo("tray quit", func() { <-ctx.Done(); tray.Quit() })
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

// safeGo is the uniform wrapper for long-lived background goroutines: a panic
// no longer kills the whole process (in tray / GUI mode the user would just see
// the tray icon vanish with nothing in app.log — on Windows -H windowsgui
// fd 2 is not even connected). The panic and its stack go to the log instead,
// so the failure is at least diagnosable. HTTP handlers are covered by
// net/http's own recover and do not go through this.
func safeGo(tag string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("panic in %s: %v\n%s", tag, r, debug.Stack())
			}
		}()
		fn()
	}()
}

// setupFileLogging also mirrors the standard logger to a file in the data
// directory, so menu-bar / tray launches (which have no terminal) still leave a
// trail while a terminal run still prints to stderr. The returned closer must
// be called on shutdown.
func setupFileLogging(dataDir string) func() {
	noop := func() {}
	// 日志和数据必须同目录：指定 -data / $LANOCLIP_DIR 时若还写默认目录，
	// 权限收敛与 1 GiB 磁盘水位都覆盖不到它。
	dir := store.ResolveDir(dataDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return noop
	}
	logPath := filepath.Join(dir, "app.log")
	rotateLogIfNeeded(logPath)
	rl, err := newRotatingLog(logPath)
	if err != nil {
		return noop
	}
	log.SetOutput(io.MultiWriter(os.Stderr, rl))
	return rl.Close
}

const (
	// 常驻后台的程序不能让 app.log 无限增长：超过 2 MiB 就换一份，
	// 只保留上一轮备份，占用上限约 4 MiB。
	maxLogBytes = 2 << 20
)

// rotatingLog rolls the log by bytes written at runtime. The old code checked
// the size only once at startup, so a tray app left running for days grew
// app.log without bound and eventually ate the whole 1 GiB data budget.
type rotatingLog struct {
	mu   sync.Mutex
	path string
	f    *os.File
	size int64
	// rotate 失败（Windows 上 app.log 被占用时 Rename 常见失败）后的退避：
	// 不加退避时每条日志都会重试一遍 Close+Rename+Open；open 再失败则
	// f=nil，此后 Write 静默丢日志且永不自愈。lastAttempt 让两条路径都
	// 最多每分钟试一次。
	lastAttempt time.Time
}

func newRotatingLog(path string) (*rotatingLog, error) {
	rl := &rotatingLog{path: path}
	if err := rl.open(); err != nil {
		return nil, err
	}
	return rl, nil
}

func (rl *rotatingLog) open() error {
	f, err := os.OpenFile(rl.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	rl.f, rl.size = f, info.Size()
	return nil
}

func (rl *rotatingLog) Write(p []byte) (int, error) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if rl.f == nil {
		// 上一次 open 失败后进入静默丢弃态：每分钟重试一次重新打开，
		// 恢复后继续落盘，而不是从此永久丢日志。
		if time.Since(rl.lastAttempt) < time.Minute {
			return len(p), nil
		}
		rl.lastAttempt = time.Now()
		if err := rl.open(); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "reopen log: %v\n", err)
			return len(p), nil
		}
	}
	if rl.size+int64(len(p)) > maxLogBytes && time.Since(rl.lastAttempt) >= time.Minute {
		rl.lastAttempt = time.Now()
		rl.rotate()
	}
	if rl.f == nil {
		return len(p), nil // 已关闭：丢弃，避免 MultiWriter 报 ErrShortWrite
	}
	n, err := rl.f.Write(p)
	rl.size += int64(n)
	return n, err
}

// rotate must be called with mu held. Never log through the standard logger
// here — its output points back at this writer and would deadlock.
func (rl *rotatingLog) rotate() {
	_ = rl.f.Close()
	rl.f = nil
	backup := rl.path + ".1"
	_ = os.Remove(backup) // Windows 上 Rename 不覆盖已存在的目标
	if err := os.Rename(rl.path, backup); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "rotate log: %v\n", err)
	}
	if err := rl.open(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "reopen log: %v\n", err)
	}
}

func (rl *rotatingLog) Close() {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if rl.f != nil {
		_ = rl.f.Close()
		rl.f = nil
	}
}

// rotateLogIfNeeded moves an oversized log aside at startup. Best-effort: if
// the rename fails we keep appending rather than losing the trail. Runtime
// rotation is handled by rotatingLog.Write.
func rotateLogIfNeeded(path string) {
	info, err := os.Stat(path)
	if err != nil || info.Size() < maxLogBytes {
		return
	}
	backup := path + ".1"
	_ = os.Remove(backup) // Windows 上 Rename 不覆盖已存在的目标
	if err := os.Rename(path, backup); err != nil {
		log.Printf("rotate log: %v", err)
	}
}
