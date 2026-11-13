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
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
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
	// page 没有业务理由超过这个值；夹住它也能防止 page*size 溢出。
	maxPage = 1 << 20
	// QR 容量有限，正常 URL 远小于此；挡住超长 text 造成的无意义编码开销。
	maxQRTextBytes = 1024
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

// Static page assets (CSS / JS) split out of index.html so the CSP can drop
// 'unsafe-inline'. They are read from the same embedded FS as the page.
func (s *Server) handleAppJS(w http.ResponseWriter, r *http.Request) {
	s.serveWebAsset(w, r, "web/app.js", "application/javascript; charset=utf-8")
}

func (s *Server) handleThemeJS(w http.ResponseWriter, r *http.Request) {
	s.serveWebAsset(w, r, "web/theme.js", "application/javascript; charset=utf-8")
}

func (s *Server) handleAppCSS(w http.ResponseWriter, r *http.Request) {
	s.serveWebAsset(w, r, "web/app.css", "text/css; charset=utf-8")
}

// serveWebAsset writes one embedded text asset with caching disabled, so a
// rebuilt binary never serves stale JS/CSS from the browser cache.
func (s *Server) serveWebAsset(w http.ResponseWriter, r *http.Request, name, contentType string) {
	data, err := fs.ReadFile(s.webFS, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

func (s *Server) handleRecords(w http.ResponseWriter, r *http.Request) {
	kind := parseKind(r.URL.Query().Get("type"))
	page := queryInt(r, "page", 1)
	if page > maxPage {
		page = maxPage
	}
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
		"counts": map[string]int{
			"text":     textCount,
			"image":    imageCount,
			"favorite": s.store.FavoriteCount(),
		},
		"settings":     map[string]int{"maxText": s.store.MaxText(), "maxImage": s.store.MaxImage()},
		"canClear":     s.canClear(r),
		"shareUrl":     s.currentShareURL(),
		"version":      Version,
		"buildTime":    BuildTime,
		"paused":       s.store.Paused(),
		"storageError": s.store.SaveError(),
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
	switch v {
	case store.KindImage:
		return store.KindImage
	case store.KindFavorite:
		return store.KindFavorite
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

// allowMethods rejects every method the endpoint does not accept, so handlers
// stay focused on the request itself. All /api/* 405s share this body and the
// standard Allow header instead of repeating the check in each handler.
func allowMethods(next http.HandlerFunc, methods ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		for _, m := range methods {
			if r.Method == m {
				next(w, r)
				return
			}
		}
		w.Header().Set("Allow", strings.Join(methods, ", "))
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

func (s *Server) handleClear(w http.ResponseWriter, r *http.Request) {
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
	if err := s.store.Save(); err != nil {
		log.Printf("save after clear: %v", err)
		writeStorageError(w)
		return
	}
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
			"paused":             s.store.Paused(),
			"storageError":       s.store.SaveError(),
		}
		writeJSON(w, http.StatusOK, resp)
		return
	case http.MethodPost:
		// 整个 POST 事务串行化：并发请求若各自读到同一份旧值，后失败的一方
		// 回滚时会把先成功一方已经生效的设置抹掉（A/B 读到同一旧值）。
		s.settingsMu.Lock()
		defer s.settingsMu.Unlock()

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
			Paused        *bool   `json:"paused"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad json"})
			return
		}
		// autostart / 快捷键 / 保留条数均可分开提交；全部缺失才视为无效请求。
		// 保留条数允许只提交 maxText 或只提交 maxImage，另一项取当前生效值。
		if body.Autostart == nil && body.HotkeyQR == nil && body.HotkeyHistory == nil &&
			body.Paused == nil && body.MaxText == nil && body.MaxImage == nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "请提供 maxText/maxImage 或其它设置项"})
			return
		}

		// ── 校验阶段：只做检查，绝不改动任何状态 ────────────────────────
		// 这样「先校验、后统一应用」可以杜绝「返回 500 但 paused 已落盘」
		// 这类半生效；真正应用时任一步失败都会逆序回滚已生效的改动。
		var newQR, newH string
		hotkeyChange := body.HotkeyQR != nil || body.HotkeyHistory != nil
		if hotkeyChange {
			// 两个字段一起提交（或都不提交）；未提交的取本次请求前的 effective 值。
			newQR = s.store.HotkeyQR()
			newH = s.store.HotkeyHistory()
			if body.HotkeyQR != nil {
				newQR = strings.ToLower(strings.TrimSpace(*body.HotkeyQR))
			}
			if body.HotkeyHistory != nil {
				newH = strings.ToLower(strings.TrimSpace(*body.HotkeyHistory))
			}
			if err := validateHotkeyPair(newQR, newH); err != nil {
				log.Printf("validate hotkey pair: %v", err)
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
				return
			}
		}
		if body.Autostart != nil && !autostart.Supported() {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "当前平台不支持开机自启"})
			return
		}
		// 数值范围：任何被提交的保留条数都必须为正；更细的上下限由
		// store.SetLimits 夹取。缺省的一项在下面用当前 effective 值补齐。
		if (body.MaxText != nil && *body.MaxText <= 0) ||
			(body.MaxImage != nil && *body.MaxImage <= 0) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "保留条数必须为正数"})
			return
		}

		// ── 应用阶段：统一落盘，维护逆序回滚列表 ────────────────────────
		// 旧值一律取「本次请求前的 effective 值」，hotkey 回滚据此重注册。
		oldPaused := s.store.Paused()
		oldQR, oldH := s.store.HotkeyQR(), s.store.HotkeyHistory()
		oldMaxText, oldMaxImage := s.store.MaxText(), s.store.MaxImage()
		oldAutostart := autostart.Enabled()

		// 单字段 limit：缺的那个取当前 effective 值补齐。
		limitsChange := body.MaxText != nil || body.MaxImage != nil
		newMaxText, newMaxImage := oldMaxText, oldMaxImage
		if body.MaxText != nil {
			newMaxText = *body.MaxText
		}
		if body.MaxImage != nil {
			newMaxImage = *body.MaxImage
		}

		var rollback []func()
		rollbackAll := func() {
			for i := len(rollback) - 1; i >= 0; i-- {
				rollback[i]()
			}
		}
		// fail 用具体中文文案回 500，详细错误只写日志，避免泄漏内部路径等。
		fail := func(msg string, err error) {
			log.Printf("settings apply failed: %v", err)
			rollbackAll()
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": msg})
		}
		// 落盘失败沿用统一的 storageError 响应（error + storageError 两个字段）。
		failStorage := func(err error) {
			log.Printf("settings save failed: %v", err)
			rollbackAll()
			writeStorageError(w)
		}

		resp := map[string]any{"ok": true}
		if body.Paused != nil {
			// 先登记回滚：SetPaused 会先改内存值再落盘，失败时也要还原。
			rollback = append(rollback, func() { _ = s.store.SetPaused(oldPaused) })
			if err := s.store.SetPaused(*body.Paused); err != nil {
				failStorage(err)
				return
			}
			resp["paused"] = s.store.Paused()
		}
		if hotkeyChange {
			// 先重注册（托盘模式下）；失败（如组合键被占用）则回滚前面的改动。
			// hotkey.Update 自身的失败回滚由 hotkey 包负责。
			if err := hotkey.Update(newQR, newH); err != nil {
				fail("快捷键注册失败，组合键可能被其他应用占用", err)
				return
			}
			rollback = append(rollback, func() {
				if err := hotkey.Update(oldQR, oldH); err != nil {
					// 二次注册失败时，配置仍会写回旧值，但实际生效的仍是新组合：
					// 这条日志是排查「快捷键实际生效值与配置不一致」的唯一线索。
					log.Printf("hotkey rollback failed: %v; 快捷键实际生效值与配置不一致（生效 %q/%q，配置写回 %q/%q）",
						err, newQR, newH, oldQR, oldH)
				}
				if err := s.store.SetHotkeys(oldQR, oldH); err != nil {
					log.Printf("restore hotkey settings: %v", err)
				}
			})
			if err := s.store.SetHotkeys(newQR, newH); err != nil {
				fail("快捷键已生效但配置保存失败，请重试", err)
				return
			}
			resp["hotkeyQR"] = s.store.HotkeyQR()
			resp["hotkeyHistory"] = s.store.HotkeyHistory()
		}
		if body.Autostart != nil {
			if err := autostart.Set(*body.Autostart); err != nil {
				fail("设置开机自启失败，请检查系统权限", err)
				return
			}
			rollback = append(rollback, func() { _ = autostart.Set(oldAutostart) })
			resp["autostart"] = autostart.Enabled()
		}
		if limitsChange {
			trimmedText, trimmedImage := s.store.SetLimits(newMaxText, newMaxImage)
			// Save 成功前不删图片文件：失败时先 RestoreTrim 把记录放回内存，
			// 再把上限还原成旧值。
			rollback = append(rollback, func() {
				s.store.RestoreTrim()
				s.store.SetLimits(oldMaxText, oldMaxImage)
			})
			if err := s.store.Save(); err != nil {
				failStorage(err)
				return
			}
			s.store.CommitTrim()
			resp["maxText"] = s.store.MaxText()
			resp["maxImage"] = s.store.MaxImage()
			resp["trimmedText"] = trimmedText
			resp["trimmedImage"] = trimmedImage
		}
		writeJSON(w, http.StatusOK, resp)
	}
	// 其它方法由 Routes 上的 allowMethods 统一拦成 405。
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
	effQR := hotkeyOrDefault(qr, store.DefaultHotkeyQR)
	effH := hotkeyOrDefault(hist, store.DefaultHotkeyHistory)
	cQR, err := hotkey.Parse(effQR)
	if err != nil {
		return fmt.Errorf("二维码快捷键无效: %w", err)
	}
	cH, err := hotkey.Parse(effH)
	if err != nil {
		return fmt.Errorf("历史快捷键无效: %w", err)
	}
	if hotkey.Same(cQR, cH) {
		return errors.New("两个快捷键不能相同")
	}
	// 系统保留组合：注册多半会失败或劫持系统功能，直接拒绝并给出可操作文案。
	if msg := reservedHotkeyMessage(effQR); msg != "" {
		return errors.New(msg)
	}
	if msg := reservedHotkeyMessage(effH); msg != "" {
		return errors.New(msg)
	}
	return nil
}

// reservedHotkeyMessage reports the user-facing error for a combo that the OS
// reserves, or "" when the combo is free to use. It works on the canonical
// string form ("meta+shift+q") instead of hotkey.Combo so it does not depend on
// the hotkey package's internal layout.
//
// 判定要求「该修饰键是唯一修饰键」：⌘Q / Ctrl+C 这类系统组合都是不带 Shift
// 的裸组合，而 ⌘⇧Q（默认二维码快捷键）并不被系统占用，不能误伤。
func reservedHotkeyMessage(combo string) string {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(combo)), "+")
	if len(parts) < 2 {
		return ""
	}
	key := strings.TrimSpace(parts[len(parts)-1])
	mods := make(map[string]bool, 3)
	for _, p := range parts[:len(parts)-1] {
		switch strings.TrimSpace(p) {
		case "meta", "cmd", "command", "win":
			mods["meta"] = true
		case "ctrl", "control":
			mods["ctrl"] = true
		case "alt", "option", "opt":
			mods["alt"] = true
		case "shift":
			mods["shift"] = true
		default:
			return "" // 未知修饰键：交给 hotkey.Parse 报错，不在这里猜
		}
	}
	only := func(mod string) bool { return len(mods) == 1 && mods[mod] }

	switch runtime.GOOS {
	case "darwin":
		if only("meta") {
			switch key {
			case "q", "w", "tab", "space", "m", "h":
				return "该组合为系统保留（如 ⌘Q 退出、⌘Space 聚焦搜索），请换一个组合"
			}
		}
	case "windows":
		if only("ctrl") {
			switch key {
			case "c", "v", "x", "z", "a":
				return "该组合为系统保留（如 Ctrl+C 复制、Ctrl+V 粘贴），请换一个组合"
			}
		}
		if only("alt") && key == "f4" {
			return "该组合为系统保留（如 Alt+F4 关闭窗口），请换一个组合"
		}
		if only("meta") && key == "l" {
			return "该组合为系统保留（如 Win+L 锁定屏幕），请换一个组合"
		}
	}
	return ""
}

// imageNameRE 只放行 store 实际会生成的文件名：<id>.png / <id>-<nano>.png。
// 它是 /api/image 的第一道白名单，挡住 ../ 之类的构造路径。
var imageNameRE = regexp.MustCompile(`^\d+(-\d+)?\.png$`)

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
	name := filepath.Base(path)
	if !imageNameRE.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	// 最终路径必须仍在 images 目录内：解析目录级软链后再做包含校验，
	// 防止 images 目录或其父目录被换成指向别处的软链。
	if !resolvedInsideImageDir(path) {
		http.NotFound(w, r)
		return
	}
	// Lstat 不跟随软链：目标本身是符号链接就拒绝（否则可指向 ~/.ssh/id_rsa）。
	// 全平台可用，不用 syscall.O_NOFOLLOW。
	info, err := os.Lstat(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// 显式 ServeContent（而非 ServeFile 的路径协商 + 跟随软链行为）。
	http.ServeContent(w, r, name, info.ModTime(), f)
}

// resolvedInsideImageDir reports whether path, once every symlink in it is
// resolved, still lives directly inside filepath.Dir(path) (the images dir).
// It is a containment check only; the actual symlink rejection is the Lstat in
// handleImage. All calls here are cross-platform (no O_NOFOLLOW / syscall).
func resolvedInsideImageDir(path string) bool {
	dir := filepath.Dir(path)
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(realDir, realPath)
	if err != nil {
		return false
	}
	return rel != ".." &&
		!strings.HasPrefix(rel, ".."+string(filepath.Separator)) &&
		!filepath.IsAbs(rel)
}

// handleSuppress marks a payload as "copied from the service page" so the
// lancoclip lancoclipWatcher will not record it again (which would duplicate an entry).
// Only the local machine's own page is allowed to do this: copies made on a
// phone never touch this machine's lancoclip.
func (s *Server) handleSuppress(w http.ResponseWriter, r *http.Request) {
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

	if err := s.store.Save(); err != nil {
		log.Printf("save after suppress: %v", err)
		writeStorageError(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleFavorite marks / unmarks a text record as a favourite. It is open to
// the LAN like /api/add-text: marking is additive (a flag on an existing
// record), never destructive, and a phone browsing the history is exactly
// where you want to keep something. Rate limiting / CSRF still apply through
// guardWrites.
func (s *Server) handleFavorite(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID       int64 `json:"id"`
		Favorite *bool `json:"favorite"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad json"})
		return
	}
	if body.ID <= 0 || body.Favorite == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "缺少 id 或 favorite"})
		return
	}
	found, changed := s.store.SetFavorite(body.ID, *body.Favorite)
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "记录不存在"})
		return
	}
	if changed {
		if err := s.store.Save(); err != nil {
			log.Printf("save after favorite: %v", err)
			writeStorageError(w)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"id":        body.ID,
		"favorite":  *body.Favorite,
		"favorites": s.store.FavoriteCount(),
	})
}

// handleUnfavoriteAll clears every favourite marker in one shot. Unlike the
// single /api/favorite toggle this is a bulk destructive action, so it uses
// the same machine-only permission as /api/clear; a phone can still unmark
// records one by one through /api/favorite.
// The button is labelled "清空全部收藏" to match the text/image clear buttons,
// but it only drops flags — unlike /api/clear it never deletes a record.
func (s *Server) handleUnfavoriteAll(w http.ResponseWriter, r *http.Request) {
	if !s.canClear(r) {
		writeForbidden(w, "批量取消收藏")
		return
	}
	ids := s.store.UnfavoriteAll()
	if len(ids) > 0 {
		if err := s.store.Save(); err != nil {
			// 内存先改再落盘：写失败必须把标记放回去，否则页面显示已清空、
			// 磁盘还是旧数据，重启后收藏「复活」。
			s.store.RestoreFavorites(ids)
			log.Printf("save after unfavorite-all: %v", err)
			writeStorageError(w)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"cleared":   len(ids),
		"favorites": s.store.FavoriteCount(),
	})
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
			writeStorageError(w)
			return
		}
		tray.UpdateHistory(s.Recent(tray.HistoryCount))
	}
	// added=false：与最新一条重复，store 未变化（前端据此提示，不算失败）
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "added": added, "paused": s.store.Paused()})
}

// handleAddImage stores an image picked on a phone (photo library or files)
// as a new record. Uploads are normalised to PNG — the only format the store
// keeps, since /api/image serves everything back as image/png.
func (s *Server) handleAddImage(w http.ResponseWriter, r *http.Request) {
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
			writeStorageError(w)
			return
		}
		tray.UpdateHistory(s.Recent(tray.HistoryCount))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "added": added, "paused": s.store.Paused()})
}

// toPNGBytes normalises an uploaded image to PNG. PNG input is passed through
// untouched so a screenshot stays byte-identical; anything else (JPEG from a
// phone's photo library, GIF…) is decoded and re-encoded.
func toPNGBytes(raw []byte) ([]byte, error) {
	if bytes.HasPrefix(raw, pngMagic) {
		cfg, err := png.DecodeConfig(bytes.NewReader(raw))
		if err == nil {
			// 透传之前也要挡尺寸：解码成 RGBA 的内存开销与编码格式无关，
			// 不能因为「PNG 不重新编码」就放过超高像素的图。
			if err := checkImagePixels(cfg.Width, cfg.Height); err != nil {
				return nil, err
			}
			return raw, nil
		}
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	if err := checkImagePixels(cfg.Width, cfg.Height); err != nil {
		return nil, err
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

// checkImagePixels 挡住极端尺寸（解码成 RGBA 会吃掉几百 MB 内存）。
func checkImagePixels(w, h int) error {
	if int64(w)*int64(h) > maxImagePixels {
		return errors.New("image too large")
	}
	return nil
}

func (s *Server) handleQR(w http.ResponseWriter, r *http.Request) {
	// 剔除 C0/C1 控制字符：它们既无助于 URL，又会污染编码结果。
	text := stripC0C1(r.URL.Query().Get("text"))
	if text == "" {
		text = s.currentShareURL()
	} else {
		if len(text) > maxQRTextBytes {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "二维码内容过长"})
			return
		}
		// 只允许生成本服务地址的二维码：否则页面就成了「给任意文本/外链
		// 生成二维码」的工具，也能被用来伪装成本服务的入口。
		if !s.qrTextAllowed(text, r) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "仅支持生成本服务地址的二维码"})
			return
		}
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

// stripC0C1 removes C0 (0x00-0x1F, 0x7F) and C1 (0x80-0x9F) control runes.
func stripC0C1(v string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return -1
		}
		return r
	}, v)
}

// qrTextAllowed reports whether raw is an http(s) URL pointing back at this
// service: a loopback host, one of this machine's current LAN addresses, or the
// host the request itself was addressed to.
func (s *Server) qrTextAllowed(raw string, r *http.Request) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if !strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return true
	}
	if host == strings.ToLower(hostOnly(r.Host)) {
		return true
	}
	s.refreshNet() // 本机地址集随网络切换动态更新
	s.netMu.Lock()
	ok := s.localIPs[host]
	s.netMu.Unlock()
	return ok
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
// service (loopback or one of its own network addresses).
//
// 安全边界只看来源 IP，不看 User-Agent / Sec-CH-UA-Mobile 这类可伪造的头。
// 本机任意进程仍可调用特权接口，这是产品取舍（用户明确不做鉴权）；UA 之类
// 只用于 UI 分流，不是安全边界，因此这里不再读取它。
func (s *Server) canClear(r *http.Request) bool {
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

// writeStorageError 统一写接口落盘失败的响应：只回固定泛化文案，详细错误
// 由调用方 log.Printf，避免把路径等内部信息泄漏给客户端；storageError 字段
// 供前端显示，与 GET 接口里的 storageError 同为字符串。
func writeStorageError(w http.ResponseWriter) {
	writeJSON(w, http.StatusInternalServerError, map[string]any{
		"error":        "保存失败，请稍后重试",
		"storageError": "保存失败，请稍后重试",
	})
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
	mux.HandleFunc("/app.js", s.handleAppJS)
	mux.HandleFunc("/theme.js", s.handleThemeJS)
	mux.HandleFunc("/app.css", s.handleAppCSS)
	mux.HandleFunc("/api/records", s.handleRecords)
	mux.HandleFunc("/api/recent", s.handleRecent)
	mux.HandleFunc("/api/clear", allowMethods(s.handleClear, http.MethodPost))
	mux.HandleFunc("/api/suppress", allowMethods(s.handleSuppress, http.MethodPost))
	mux.HandleFunc("/api/favorite", allowMethods(s.handleFavorite, http.MethodPost))
	mux.HandleFunc("/api/unfavorite-all", allowMethods(s.handleUnfavoriteAll, http.MethodPost))
	mux.HandleFunc("/api/image", s.handleImage)
	mux.HandleFunc("/api/add-text", allowMethods(s.handleAddText, http.MethodPost))
	mux.HandleFunc("/api/add-image", allowMethods(s.handleAddImage, http.MethodPost))
	mux.HandleFunc("/api/qr", s.handleQR)
	mux.HandleFunc("/api/settings", allowMethods(s.handleSettings, http.MethodGet, http.MethodPost))
	mux.HandleFunc("/api/activate", allowMethods(s.handleActivate, http.MethodPost))
	// 由外到内：安全响应头 → Host 白名单（防 DNS rebinding）→ 读请求同源校验
	// → 写请求三道防线。guardReads 只作用于 /api/* 非写方法，页面与静态资源、
	// 以及非浏览器客户端的无 Sec-Fetch-Site 请求都不受影响。
	return securityHeaders(s.guardHost(s.guardReads(s.guardWrites(mux))))
}

// securityHeaders hardens the page and the API responses. JS/CSS live in
// separate same-origin files (web/app.js, web/theme.js, web/app.css), so the
// CSP no longer needs 'unsafe-inline'; everything stays locked to same-origin.
func securityHeaders(next http.Handler) http.Handler {
	const csp = "default-src 'self'; " +
		"script-src 'self'; " +
		"style-src 'self'; " +
		"img-src 'self' data: blob:; " +
		"connect-src 'self'; object-src 'none'; base-uri 'none'; " +
		"form-action 'self'; frame-ancestors 'self'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "SAMEORIGIN")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", csp)
		// 所有响应（页面、静态资源、API）都禁缓存：剪贴板正文经中间代理或
		// 磁盘缓存留存下来，就违背了「本机/局域网内即用即走」的预期。
		h.Set("Cache-Control", "no-store")
		h.Set("Pragma", "no-cache")
		h.Set("Expires", "0")
		// 只阻止跨站加载本资源；同源页面（含手机直连 LAN IP）正常。
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Vary", "Origin")
		next.ServeHTTP(w, r)
	})
}
