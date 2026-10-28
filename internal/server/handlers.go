package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"  // 注册 GIF 解码器：手机相册选出来的图也可能是 GIF
	_ "image/jpeg" // 注册 JPEG 解码器：iOS 相册上传的多为 JPEG
	"image/png"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"lanoclip/internal/autostart"
	"lanoclip/internal/hotkey"
	"lanoclip/internal/notify"
	"lanoclip/internal/store"
	"lanoclip/internal/tray"
)

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(s.webFS, "web/index.html")
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
func (s *Server) handleFavicon(w http.ResponseWriter, r *http.Request) {
	s.serveWebPNG(w, r, "web/favicon.png")
}

// handleAppIcon serves the high-resolution app icon used by the page header.
// The tab icon is only 64px, which turns to mush at 2x/3x on the hero slot.
func (s *Server) handleAppIcon(w http.ResponseWriter, r *http.Request) {
	s.serveWebPNG(w, r, "web/appicon.png")
}

// serveWebPNG writes an embedded PNG with caching disabled, so a rebuilt
// binary never serves a stale icon from the browser cache.
func (s *Server) serveWebPNG(w http.ResponseWriter, r *http.Request, name string) {
	data, err := fs.ReadFile(s.webFS, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

func (s *Server) handleRecords(w http.ResponseWriter, r *http.Request) {
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
		"version":    Version,
		"buildTime":  BuildTime,
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
func (s *Server) handleRecent(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) handleClear(w http.ResponseWriter, r *http.Request) {
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
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) handleImage(w http.ResponseWriter, r *http.Request) {
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
func (s *Server) handleSuppress(w http.ResponseWriter, r *http.Request) {
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
func (s *Server) handleAddText(w http.ResponseWriter, r *http.Request) {
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
func (s *Server) handleAddImage(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) handleQR(w http.ResponseWriter, r *http.Request) {
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
func (s *Server) handleActivate(w http.ResponseWriter, r *http.Request) {
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
		Open(url)
	}); err != nil {
		// 通知不可用（例如终端里跑的 macOS 裸二进制）时直接打开页面，
		// 否则用户第二次双击会以为程序毫无反应。
		log.Printf("already-running notification: %v", err)
		Open(url)
	}
}

// canClear reports whether the request comes from the machine running the
// service (loopback or one of its own network addresses). Mobile browsers are
// never allowed to clear, even when they reach the service through a local
// proxy (in which case the source IP looks local).
func (s *Server) canClear(r *http.Request) bool {
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

// recentWindow is how old a record may be for a late suppression report to
// still retract it.
const recentWindow = 10 * time.Second

// Routes wires every HTTP endpoint. The page and static assets are served first;
// the /api/* block is the machine-local + LAN API.
func (s *Server) Routes() http.Handler {
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
