package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	_ "image/gif"  // 注册 GIF 解码器：手机相册选出来的图也可能是 GIF
	_ "image/jpeg" // 注册 JPEG 解码器：iOS 相册上传的多为 JPEG
	"image/png"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"lanoclip/internal/autostart"
	"lanoclip/internal/hotkey"
	"lanoclip/internal/lancoclip"
	"lanoclip/internal/notify"
	"lanoclip/internal/store"
	"lanoclip/internal/tray"
	"lanoclip/internal/winhide"
)

//go:embed web
var webFS embed.FS

// version is the build version, injected by build-mac.sh / build-windows.sh
// (-ldflags "-X main.version=..."). It is surfaced on the page so it is obvious
// which build is actually running.
var version = "dev"

// buildTime is the UTC build timestamp, injected the same way
// (-ldflags "-X main.buildTime=..."). Shown next to the version in the
// settings dialog so stale builds are easy to spot.
var buildTime = "dev"

// server bundles the shared state used by the HTTP handlers.
//
// localIPs / shareURL 是「最近一次探测」的缓存：切换网络（换 Wi-Fi、DHCP
// 续租换 IP）后旧地址会失效，所以生产环境按 TTL 定期重算（见 refreshNet）。
// port == 0 表示测试直构的 server，保持预设值不动。
type server struct {
	store      *store.Store
	suppressor *lancoclip.Suppressor
	port       int
	localIPs   map[string]bool
	shareURL   string

	netMu      sync.Mutex
	netChecked time.Time // 上次重算 localIPs/shareURL 的时间
}

// recentWindow is how old a record may be for a late suppression report to
// still retract it.
const recentWindow = 10 * time.Second

func main() {
	port := flag.Int("port", 6116, "HTTP listen port")
	interval := flag.Duration("interval", 500*time.Millisecond, "lancoclip polling interval used as the fallback where change notifications are unavailable (macOS/Windows detect changes natively)")
	trayMode := flag.Bool("tray", false, "run as a macOS menu bar app (auto-enabled inside a .app bundle)")
	dataDir := flag.String("data", "", "data directory (default: user config dir, or $LANOCLIP_DIR)")
	flag.Parse()

	// A double-clicked .app (macOS) or a GUI-subsystem .exe (Windows) has no
	// terminal, so keep logs next to the data.
	inBundle := runningInAppBundle()
	if inBundle || runtime.GOOS == "windows" {
		setupFileLogging()
	}

	store, err := store.NewStore(*dataDir)
	if err != nil {
		log.Fatalf("init store: %v", err)
	}

	srv := &server{
		store:      store,
		suppressor: lancoclip.NewSuppressor(),
		port:       *port,
		localIPs:   localIPSet(),
	}

	// 单实例：程序已经在跑时（再次双击 .app / exe）不再起第二个监听，
	// 而是让已在运行的那个实例弹「已在运行」通知，本进程随即退出。
	if url := runningInstanceURL(*port, portScanRange); url != "" {
		log.Printf("LanCoClip 已在运行（%s）：已通知该实例，本进程退出", url)
		return
	}

	// Bind first, so a busy port can fall back to the next free one and the
	// share URL always reflects the port actually in use.
	ln, err := listenFirstFree(*port, portScanRange)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	srv.port = ln.Addr().(*net.TCPAddr).Port
	srv.shareURL = fmt.Sprintf("http://%s:%d", bestLocalIP(), srv.port)
	srv.netChecked = time.Now() // 启动探测算一次；之后由 refreshNet 按 TTL 刷新

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// LanCoClip lancoclipWatcher: write straight through so a record survives even if the
	// app is killed right after a copy (the periodic flush below is a safety net).
	onCaptured := func(what string) {
		log.Printf("captured %s", what)
		if err := store.Save(); err != nil {
			log.Printf("save: %v", err)
		}
		tray.UpdateHistory(srv.Recent(tray.HistoryCount))
	}
	go lancoclip.Watch(ctx, *interval, srv.suppressor,
		func(content string) {
			if store.AddText(content) {
				onCaptured(fmt.Sprintf("%d chars of text", len([]rune(content))))
			}
		},
		func(png []byte) {
			added, err := store.AddImage(png)
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
				if err := store.Save(); err != nil {
					log.Printf("save: %v", err)
				}
			}
		}
	}()

	httpSrv := &http.Server{
		Handler:           srv.routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("lancoclip service listening on %s", srv.shareURL)
		log.Printf("open this URL on your phone (same Wi-Fi): %s", srv.shareURL)
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
			HotkeyQR:      srv.store.HotkeyQR(),
			HotkeyHistory: srv.store.HotkeyHistory(),
		}) // blocks on the main thread until the user quits
		stop()
	} else {
		<-ctx.Done()
	}

	log.Println("shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	_ = store.Save()
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/favicon.ico", s.handleFavicon)
	mux.HandleFunc("/favicon.png", s.handleFavicon)
	mux.HandleFunc("/appicon.png", s.handleAppIcon)
	mux.HandleFunc("/api/records", s.handleRecords)
	mux.HandleFunc("/api/recent", s.handleRecent)
	mux.HandleFunc("/api/clear", s.handleClear)
	mux.HandleFunc("/api/suppress", s.handleSuppress)
	mux.HandleFunc("/api/image", s.handleImage)
	mux.HandleFunc("/api/add-text", s.handleAddText)
	mux.HandleFunc("/api/add-image", s.handleAddImage)
	mux.HandleFunc("/api/qr", s.handleQR)
	mux.HandleFunc("/api/settings", s.handleSettings)
	mux.HandleFunc("/api/activate", s.handleActivate)
	return mux
}

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := webFS.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, "page not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Never cache the page: a stale copy (browser or an intermediate proxy such
	// as Whistle) would keep serving old JS and old markup.
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	_, _ = w.Write(data)
}

// defaultPageSize is the page size used by the H5 list and the PC pager.
const (
	defaultPageSize = 10
	maxPageSize     = 500
)

// handleFavicon serves the browser-tab icon. /favicon.ico is handled too
// (browsers request it by default) — a PNG body is fine there.
func (s *server) handleFavicon(w http.ResponseWriter, r *http.Request) {
	serveWebPNG(w, r, "web/favicon.png")
}

// handleAppIcon serves the high-resolution app icon used by the page header.
// The tab icon is only 64px, which turns to mush at 2x/3x on the hero slot.
func (s *server) handleAppIcon(w http.ResponseWriter, r *http.Request) {
	serveWebPNG(w, r, "web/appicon.png")
}

// serveWebPNG writes an embedded PNG with caching disabled, so a rebuilt
// binary never serves a stale icon from the browser cache.
func serveWebPNG(w http.ResponseWriter, r *http.Request, name string) {
	data, err := webFS.ReadFile(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

func (s *server) handleRecords(w http.ResponseWriter, r *http.Request) {
	kind := parseKind(r.URL.Query().Get("type"))
	page := queryInt(r, "page", 1)
	size := queryInt(r, "size", defaultPageSize)
	if size > maxPageSize {
		size = maxPageSize
	}

	// 文本与图片各自分页、各自计数
	records, total := s.store.Page(kind, page, size)
	totalPages := (total + size - 1) / size
	if totalPages < 1 {
		totalPages = 1
	}
	textCount, imageCount := s.store.Counts()

	writeJSON(w, http.StatusOK, map[string]any{
		"records":    records,
		"type":       kind,
		"page":       page,
		"size":       size,
		"total":      total,
		"totalPages": totalPages,
		"hasMore":    page*size < total,
		"counts":     map[string]int{"text": textCount, "image": imageCount},
		"settings":   map[string]int{"maxText": s.store.MaxText(), "maxImage": s.store.MaxImage()},
		"canClear":   s.canClear(r),
		"shareUrl":   s.currentShareURL(),
		"version":    version,
		"buildTime":  buildTime,
	})
}

// queryInt reads a positive integer query parameter, falling back to def.
func queryInt(r *http.Request, key string, def int) int {
	if v, err := strconv.Atoi(r.URL.Query().Get(key)); err == nil && v > 0 {
		return v
	}
	return def
}

// parseKind normalises the ?type= parameter; anything unknown means text.
func parseKind(v string) string {
	if v == store.KindImage {
		return store.KindImage
	}
	return store.KindText
}

// handleRecent returns the newest records across both kinds (text + image
// merged, newest first) for the history popup (?history=1). Images carry only
// metadata; the page renders thumbnails via /api/image?id=.
func (s *server) handleRecent(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", tray.HistoryCount)
	if limit > 100 {
		limit = 100
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"records":  s.store.RecentMixed(limit),
		"shareUrl": s.currentShareURL(),
		"canClear": s.canClear(r),
	})
}

// writeForbidden rejects a machine-only action coming from a phone browser.
func writeForbidden(w http.ResponseWriter, action string) {
	writeJSON(w, http.StatusForbidden, map[string]any{"error": "仅服务本机可" + action})
}

func (s *server) handleClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	if !s.canClear(r) {
		writeForbidden(w, "清空记录")
		return
	}
	// 按 tab 清空：只清当前类型（不带 type 时清全部）
	kind := r.URL.Query().Get("type")
	if kind != store.KindText && kind != store.KindImage {
		kind = ""
	}
	s.store.Clear(kind)
	_ = s.store.Save()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "type": kind})
}

// handleSettings serves the retention configuration: GET returns the current
// caps (open to any viewer), POST updates them (service machine only, like
// /api/clear). Saving takes effect immediately — records beyond the new caps
// are trimmed right away, oldest first.
func (s *server) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		resp := map[string]any{
			"maxText":            s.store.MaxText(),
			"maxImage":           s.store.MaxImage(),
			"autostart":          autostart.Enabled(),
			"autostartSupported": autostart.Supported(),
			"hotkeyQR":           s.store.HotkeyQR(),
			"hotkeyHistory":      s.store.HotkeyHistory(),
		}
		writeJSON(w, http.StatusOK, resp)
		return
	case http.MethodPost:
		if !s.canClear(r) {
			writeForbidden(w, "修改设置")
			return
		}
		var body struct {
			MaxText       *int    `json:"maxText"`
			MaxImage      *int    `json:"maxImage"`
			Autostart     *bool   `json:"autostart"`
			HotkeyQR      *string `json:"hotkeyQR"`
			HotkeyHistory *string `json:"hotkeyHistory"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad json"})
			return
		}
		resp := map[string]any{"ok": true}
		// autostart / 快捷键 / 保留条数均可分开提交；全部缺失才视为无效请求
		if body.Autostart == nil && body.HotkeyQR == nil && body.HotkeyHistory == nil &&
			(body.MaxText == nil || body.MaxImage == nil) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing maxText/maxImage"})
			return
		}
		// 快捷键：两个字段一起提交（或都不提交）。空串表示恢复平台默认。
		if body.HotkeyQR != nil || body.HotkeyHistory != nil {
			newQR := s.store.HotkeyQR()
			newH := s.store.HotkeyHistory()
			if body.HotkeyQR != nil {
				newQR = strings.ToLower(strings.TrimSpace(*body.HotkeyQR))
			}
			if body.HotkeyHistory != nil {
				newH = strings.ToLower(strings.TrimSpace(*body.HotkeyHistory))
			}
			if err := validateHotkeyPair(newQR, newH); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
				return
			}
			// 先重注册（托盘模式下）；失败（如组合键被占用）则不落盘
			if err := hotkey.Update(newQR, newH); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			s.store.SetHotkeys(newQR, newH)
			resp["hotkeyQR"] = s.store.HotkeyQR()
			resp["hotkeyHistory"] = s.store.HotkeyHistory()
		}
		if body.Autostart != nil {
			if !autostart.Supported() {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "当前平台不支持开机自启"})
				return
			}
			if err := autostart.Set(*body.Autostart); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "设置开机自启失败: " + err.Error()})
				return
			}
			resp["autostart"] = autostart.Enabled()
		}
		if body.MaxText != nil && body.MaxImage != nil {
			trimmedText, trimmedImage := s.store.SetLimits(*body.MaxText, *body.MaxImage)
			if err := s.store.Save(); err != nil {
				log.Printf("save after settings: %v", err)
			}
			resp["maxText"] = s.store.MaxText()
			resp["maxImage"] = s.store.MaxImage()
			resp["trimmedText"] = trimmedText
			resp["trimmedImage"] = trimmedImage
		}
		writeJSON(w, http.StatusOK, resp)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

// hotkeyOrDefault turns an empty combo (meaning "reset to default") into the
// platform default for validation/display purposes.
func hotkeyOrDefault(s string, def func() string) string {
	if s == "" {
		return def()
	}
	return s
}

// validateHotkeyPair parses both combos ("" = platform default) and rejects a
// pair that presses the same keys. Empty combos are validated against the
// platform defaults so a reset request is still checked.
func validateHotkeyPair(qr, hist string) error {
	cQR, err := hotkey.Parse(hotkeyOrDefault(qr, store.DefaultHotkeyQR))
	if err != nil {
		return fmt.Errorf("二维码快捷键无效: %w", err)
	}
	cH, err := hotkey.Parse(hotkeyOrDefault(hist, store.DefaultHotkeyHistory))
	if err != nil {
		return fmt.Errorf("历史快捷键无效: %w", err)
	}
	if hotkey.Same(cQR, cH) {
		return errors.New("两个快捷键不能相同")
	}
	return nil
}

func (s *server) handleImage(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	path, ok := s.store.ImagePath(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "image/png")
	http.ServeFile(w, r, path)
}

// handleSuppress marks a payload as "copied from the service page" so the
// lancoclip lancoclipWatcher will not record it again (which would duplicate an entry).
// Only the local machine's own page is allowed to do this: copies made on a
// phone never touch this machine's lancoclip.
func (s *server) handleSuppress(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	if !s.canClear(r) {
		writeForbidden(w, "忽略记录")
		return
	}

	var body struct {
		Kind    string `json:"kind"`
		Content string `json:"content"`
		ID      int64  `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad json"})
		return
	}

	if body.Kind == store.KindImage {
		sum, ok := s.store.ImageSum(body.ID)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "unknown image"})
			return
		}
		s.suppressor.AddImageSum(sum)
		// 兜住竞态：剪贴板先变、上报后到，把刚记下的重复项撤回
		s.store.DropRecentImage(sum, recentWindow)
	} else {
		if body.Content == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "empty content"})
			return
		}
		s.suppressor.AddText(body.Content)
		s.store.DropRecentText(body.Content, recentWindow)
	}

	_ = s.store.Save()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ── 手机端推送：把手机上的文本 / 图片直接写进服务本机的记录 ───────────────
// 与剪贴板捕获共用同一个 store，但不碰系统剪贴板，所以不需要 suppressor
// 抑制：本机 lancoclipWatcher 不会因为这条记录再多记一次。
const (
	// maxAddTextBytes 限制单条推送文本，避免一次请求塞爆 store。
	maxAddTextBytes = 512 << 10
	// maxUploadBytes 限制上传图片的原始字节数。
	maxUploadBytes = 32 << 20
	// maxImagePixels 挡住极端尺寸（解码成 RGBA 会吃掉几百 MB 内存）；
	// 4000×3000 的照片才 12MP，离这个上限还很远。
	maxImagePixels = 40_000_000
)

// pngMagic is the fixed 8-byte signature every PNG starts with.
var pngMagic = []byte("\x89PNG\r\n\x1a\n")

// handleAddText records text pushed from any client — typically a phone.
// Unlike /api/clear it is deliberately open to the LAN: sending something to
// the PC is the whole point, and the PC's own lancoclip is untouched.
func (s *server) handleAddText(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAddTextBytes)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad json"})
		return
	}
	if strings.TrimSpace(body.Content) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "内容为空"})
		return
	}

	added := s.store.AddText(body.Content)
	if added {
		log.Printf("added %d chars of text pushed from %s", len([]rune(body.Content)), r.RemoteAddr)
		if err := s.store.Save(); err != nil {
			log.Printf("save: %v", err)
		}
		tray.UpdateHistory(s.Recent(tray.HistoryCount))
	}
	// added=false：与最新一条重复，store 未变化（前端据此提示，不算失败）
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "added": added})
}

// handleAddImage stores an image picked on a phone (photo library or files)
// as a new record. Uploads are normalised to PNG — the only format the store
// keeps, since /api/image serves everything back as image/png.
func (s *server) handleAddImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "上传失败或图片过大"})
		return
	}
	file, _, err := r.FormFile("image")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "缺少图片文件"})
		return
	}
	defer file.Close()

	raw, err := io.ReadAll(io.LimitReader(file, maxUploadBytes+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "读取图片失败"})
		return
	}
	if len(raw) > maxUploadBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": "图片超过 32MB"})
		return
	}

	data, err := toPNGBytes(raw)
	if err != nil {
		log.Printf("decode uploaded image: %v", err)
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]any{"error": "不支持的图片格式"})
		return
	}

	added, err := s.store.AddImage(data)
	if err != nil {
		log.Printf("add uploaded image: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "保存图片失败"})
		return
	}
	if added {
		log.Printf("added image (%d KB) uploaded from %s", len(data)/1024, r.RemoteAddr)
		if err := s.store.Save(); err != nil {
			log.Printf("save: %v", err)
		}
		tray.UpdateHistory(s.Recent(tray.HistoryCount))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "added": added})
}

// toPNGBytes normalises an uploaded image to PNG. PNG input is passed through
// untouched so a screenshot stays byte-identical; anything else (JPEG from a
// phone's photo library, GIF…) is decoded and re-encoded.
func toPNGBytes(raw []byte) ([]byte, error) {
	if bytes.HasPrefix(raw, pngMagic) {
		if _, err := png.DecodeConfig(bytes.NewReader(raw)); err == nil {
			return raw, nil
		}
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	if int64(cfg.Width)*int64(cfg.Height) > maxImagePixels {
		return nil, errors.New("image too large")
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (s *server) handleQR(w http.ResponseWriter, r *http.Request) {
	text := r.URL.Query().Get("text")
	if text == "" {
		text = s.currentShareURL()
	}
	png, err := qrcode.Encode(text, qrcode.Medium, 512)
	if err != nil {
		http.Error(w, "qr encode failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

// handleActivate is the "already running" ping.
//
// A second launch (double-clicking the .app / exe again) finds this instance
// before it binds its own port and asks it to surface the notification; the
// caller then exits instead of starting a second watcher. Showing it from the
// running instance is deliberate: it owns the tray icon and the run loop the
// notification click needs, while the second process is about to die.
//
// Machine-local only — a phone on the LAN must not be able to trigger a
// desktop notification.
func (s *server) handleActivate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	if !s.canClear(r) {
		writeForbidden(w, "重复启动提示")
		return
	}
	url := s.localURL()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "app": "lanoclip", "url": url})

	log.Printf("another launch detected; notifying on this instance (%s)", url)
	if err := notify.Notify("LanCoClip已在运行", "点击即可打开剪贴板记录页面", func() {
		openBrowser(url)
	}); err != nil {
		// 通知不可用（例如终端里跑的 macOS 裸二进制）时直接打开页面，
		// 否则用户第二次双击会以为程序毫无反应。
		log.Printf("already-running notification: %v", err)
		openBrowser(url)
	}
}

// canClear reports whether the request comes from the machine running the
// service (loopback or one of its own network addresses). Mobile browsers are
// never allowed to clear, even when they reach the service through a local
// proxy (in which case the source IP looks local).
func (s *server) canClear(r *http.Request) bool {
	if isMobileRequest(r) {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	s.refreshNet() // 本机地址集随网络切换动态更新
	s.netMu.Lock()
	ips := s.localIPs
	s.netMu.Unlock()
	return ips[ip.String()]
}

// ── tray.App: the tray menu's view of the server ─────────────────────────────

// netTTL is how long a resolved LAN address / local-IP set stays trusted.
// Short enough that a network switch is picked up on the next tray click or
// page poll, long enough that the per-request cost stays negligible.
const netTTL = 3 * time.Second

// refreshNet re-resolves the machine's addresses when the cached ones are
// stale. Without this, switching networks leaves the tray/hotkey URL pointing
// at an IP that no longer exists and canClear fails for the new local IP.
// It is a no-op for test-built servers (port == 0 keeps preset values).
func (s *server) refreshNet() {
	if s.port == 0 {
		return
	}
	s.netMu.Lock()
	defer s.netMu.Unlock()
	if !s.netChecked.IsZero() && time.Since(s.netChecked) < netTTL {
		return
	}
	s.netChecked = time.Now()
	s.localIPs = localIPSet()
	if ip := bestLocalIP(); ip != "" {
		s.shareURL = fmt.Sprintf("http://%s:%d", ip, s.port)
	}
}

// currentShareURL returns the up-to-date access URL (refreshNet-aware).
func (s *server) currentShareURL() string {
	s.refreshNet()
	s.netMu.Lock()
	defer s.netMu.Unlock()
	return s.shareURL
}

// URL implements tray.App: the LAN address a phone has to use.
func (s *server) URL() string { return s.currentShareURL() }

// LocalURL implements tray.App: the loopback address the tray menu opens.
// Only the browser's own address changes — the QR rendered inside the page
// still carries the LAN share URL — so the page becomes a secure context and
// navigator.clipboard (needed to copy images) stops being undefined.
func (s *server) LocalURL() string { return s.localURL() }

// localURL is the page address on this machine itself. Unlike URL() it does not
// depend on the LAN address, so it keeps working with no network at all — which
// is exactly the case for "you already have this running, here is its page".
func (s *server) localURL() string { return fmt.Sprintf("http://127.0.0.1:%d", s.port) }

// Recent implements tray.App: the newest records across both kinds, formatted
// for the tray menu.
func (s *server) Recent(n int) []tray.Item {
	recs := s.store.RecentMixed(n)
	items := make([]tray.Item, 0, len(recs))
	for _, r := range recs {
		items = append(items, tray.Item{
			Kind:  r.Kind,
			ID:    r.ID,
			Label: trayItemLabel(r),
			Text:  r.Content,
		})
	}
	return items
}

// CopyItem implements tray.App: put a history entry back onto the lancoclip.
// The lancoclipWatcher will pick it up and float it back to the top of the history,
// which is the expected behaviour of a lancoclip manager.
func (s *server) CopyItem(it tray.Item) error {
	if it.Kind == store.KindImage {
		path, ok := s.store.ImagePath(it.ID)
		if !ok {
			return fmt.Errorf("image #%d not found", it.ID)
		}
		png, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return lancoclip.WriteImage(png)
	}
	return lancoclip.WriteText(it.Text)
}

// ClearAll implements tray.App: drop every text and image record (image files
// are deleted too) and persist right away.
func (s *server) ClearAll() error {
	s.store.Clear("")
	if err := s.store.Save(); err != nil {
		return err
	}
	log.Println("cleared all history from the tray")
	return nil
}

// trayItemLabel formats one record as a tray menu title:
// "14:05 会议纪要要点…" / "01-02 14:05 [图片]".
func trayItemLabel(r store.Record) string {
	t := time.UnixMilli(r.CreatedAt)
	now := time.Now()
	stamp := t.Format("15:04")
	if t.Day() != now.Day() || t.Month() != now.Month() {
		stamp = t.Format("01-02 15:04")
	}
	if r.Kind == store.KindImage {
		return stamp + "  [图片]"
	}
	content := strings.TrimSpace(strings.ReplaceAll(r.Content, "\n", " "))
	if runes := []rune(content); len(runes) > 36 {
		content = string(runes[:36]) + "…"
	}
	if content == "" {
		content = "（空白文本）"
	}
	return stamp + "  " + content
}

// isMobileUA reports whether the request looks like it comes from a mobile
// browser. It checks both the classic User-Agent keywords and the Client Hints
// header sent by Chromium browsers.
func isMobileUA(ua string) bool {
	if strings.TrimSpace(ua) == "" {
		return false
	}
	ua = strings.ToLower(ua)
	for _, kw := range []string{
		"android", "iphone", "ipod", "ipad", "mobile", "windows phone",
		"micromessenger", "harmonyos", "okhttp", "webview",
	} {
		if strings.Contains(ua, kw) {
			return true
		}
	}
	return false
}

// isMobileRequest combines the User-Agent and the Sec-CH-UA-Mobile client hint.
func isMobileRequest(r *http.Request) bool {
	if strings.TrimSpace(r.Header.Get("Sec-CH-UA-Mobile")) == "?1" {
		return true
	}
	return isMobileUA(r.UserAgent())
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// localIPSet returns every non-loopback IPv4 address of this machine.
func localIPSet() map[string]bool {
	set := map[string]bool{}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return set
	}
	for _, addr := range addrs {
		ipnet, ok := addr.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() {
			continue
		}
		if v4 := ipnet.IP.To4(); v4 != nil {
			set[v4.String()] = true
		}
	}
	return set
}

// virtualInterfacePrefixes lists interface name prefixes that are NOT the
// physical LAN NIC: VPN tunnels, bridges, VM/container networks, AirDrop etc.
// Advertising one of these as the access address is useless for phones.
var virtualInterfacePrefixes = []string{
	"utun", "ipsec", "ppp", "tun", "tap", "awdl", "llw", "anpi", "ap",
	"bridge", "vmnet", "vboxnet", "docker", "veth", "gpd", "lo",
}

func isVirtualInterface(name string) bool {
	n := strings.ToLower(name)
	for _, p := range virtualInterfacePrefixes {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}

// firstIPv4 returns the first usable (non-link-local) IPv4 address of an interface.
func firstIPv4(ifi net.Interface) string {
	addrs, err := ifi.Addrs()
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		ipnet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		v4 := ipnet.IP.To4()
		if v4 == nil || v4.IsLinkLocalUnicast() {
			continue
		}
		return v4.String()
	}
	return ""
}

// bestLocalIP picks the address phones can reach.
//
// Priority: en0 (网卡1, the primary physical NIC) → any other physical NIC →
// any address at all, including virtual NICs (last resort, e.g. VPN-only).
func bestLocalIP() string {
	ifs, err := net.Interfaces()
	if err != nil {
		return "127.0.0.1"
	}

	var fallback string
	for _, ifi := range ifs {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		if isVirtualInterface(ifi.Name) {
			continue
		}
		ip := firstIPv4(ifi)
		if ip == "" {
			continue
		}
		if ifi.Name == "en0" {
			return ip
		}
		if fallback == "" {
			fallback = ip
		}
	}
	if fallback != "" {
		return fallback
	}

	// Last resort: accept virtual interfaces too.
	for _, ifi := range ifs {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		if ip := firstIPv4(ifi); ip != "" {
			return ip
		}
	}
	return "127.0.0.1"
}

// portScanRange is how many consecutive ports a launch considers: both when
// binding (listenFirstFree) and when probing for an instance already running
// (runningInstanceURL). The two must agree, or a second launch could miss the
// running instance and start a duplicate watcher on the next free port.
const portScanRange = 10

// runningInstanceURL probes the ports this launch would bind, looking for an
// instance that is already serving. It returns that instance's local page URL,
// or "" when nothing answers.
//
// The probe is a POST to /api/activate and the answer must identify itself as
// lanoclip: a foreign service sitting on one of the ports (a dev server, say)
// must not be mistaken for our own instance, or every launch would refuse to
// start. Each attempt is short-lived — a closed port fails instantly, and the
// only real cost is the HTTP timeout on a port that is open but slow.
func runningInstanceURL(start, tries int) string {
	client := &http.Client{Timeout: 300 * time.Millisecond}
	for i := 0; i < tries; i++ {
		port := start + i
		req, err := http.NewRequest(http.MethodPost,
			fmt.Sprintf("http://127.0.0.1:%d/api/activate", port), strings.NewReader("{}"))
		if err != nil {
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		var body struct {
			App string `json:"app"`
			URL string `json:"url"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<12)).Decode(&body)
		resp.Body.Close()
		if body.App != "lanoclip" {
			continue
		}
		if body.URL != "" {
			return body.URL
		}
		return fmt.Sprintf("http://127.0.0.1:%d", port)
	}
	return ""
}

// listenFirstFree binds the first free TCP port in [start, start+tries).
func listenFirstFree(start, tries int) (net.Listener, error) {
	var lastErr error
	for i := 0; i < tries; i++ {
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", start+i))
		if err == nil {
			if i > 0 {
				log.Printf("port %d is busy, using %d instead", start, start+i)
			}
			return ln, nil
		}
		lastErr = err
	}
	return nil, lastErr
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
	return strings.Contains(exe, ".app/Contents/MacOS/")
}

// openBrowser opens url in the default browser, the same way the tray menu
// does. It is a variable so tests can capture the URL instead of really
// launching a browser.
var openBrowser = func(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		// rundll32（而非 cmd /c start）避免 GUI 程序弹出控制台窗口。
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	winhide.Hide(cmd)
	_ = cmd.Start()
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
