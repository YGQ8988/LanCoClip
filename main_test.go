package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"lanoclip/internal/lancoclip"
	"lanoclip/internal/notify"
	"lanoclip/internal/store"
)

// newTestStore builds a Store backed by a throwaway directory.
func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return st
}

func TestCanClear(t *testing.T) {
	s := &server{localIPs: map[string]bool{"192.168.1.10": true}}

	cases := []struct {
		name   string
		remote string
		ua     string
		want   bool
	}{
		{"loopback", "127.0.0.1:5000", "Mozilla/5.0 (Macintosh)", true},
		{"ipv6 loopback", "[::1]:5000", "Mozilla/5.0 (Macintosh)", true},
		{"own LAN address", "192.168.1.10:5000", "Mozilla/5.0 (Macintosh)", true},
		{"other device on LAN", "192.168.1.99:5000", "Mozilla/5.0 (Macintosh)", false},
		{"other network", "10.0.0.5:5000", "Mozilla/5.0 (Macintosh)", false},
		{"mobile even from loopback", "127.0.0.1:5000", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) Mobile/15E148 Safari/604.1", false},
		{"mobile via local proxy", "192.168.1.10:5000", "Mozilla/5.0 (Linux; Android 14) Mobile Safari", false},
	}
	for _, c := range cases {
		r := &http.Request{RemoteAddr: c.remote, Header: http.Header{"User-Agent": []string{c.ua}}}
		if got := s.canClear(r); got != c.want {
			t.Errorf("%s: canClear(%s, %q) = %v, want %v", c.name, c.remote, c.ua, got, c.want)
		}
	}
}

func TestIsMobileUA(t *testing.T) {
	mobile := []string{
		"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 Mobile/15E148",
		"Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 Chrome/120 Mobile Safari/537.36",
		"Mozilla/5.0 (iPad; CPU OS 17_0 like Mac OS X) Mobile/15E148",
		"Mozilla/5.0 (Linux; Android 13; wv) MicroMessenger/8.0",
	}
	desktop := []string{
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 Safari/605.1.15",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/120.0.0.0 Safari/537.36",
		"curl/8.4.0",
	}
	for _, ua := range mobile {
		if !isMobileUA(ua) {
			t.Errorf("expected mobile: %s", ua)
		}
	}
	for _, ua := range desktop {
		if isMobileUA(ua) {
			t.Errorf("expected desktop: %s", ua)
		}
	}
}

func TestHandleRecordsPagination(t *testing.T) {
	s := &server{
		store:    newTestStore(t),
		localIPs: map[string]bool{},
		shareURL: "http://192.168.1.10:8899",
	}
	for i := 1; i <= 25; i++ {
		s.store.AddText(fmt.Sprintf("item-%d", i))
	}
	for i := 1; i <= 3; i++ {
		s.store.AddImage([]byte{byte(i), 'P', 'N', 'G'})
	}

	get := func(query string) map[string]any {
		req := httptest.NewRequest(http.MethodGet, "/api/records?"+query, nil)
		req.RemoteAddr = "192.168.1.99:5000"
		rec := httptest.NewRecorder()
		s.handleRecords(rec, req)
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("bad json: %v", err)
		}
		return out
	}

	// 默认文本、每页 10 条
	def := get("")
	if def["size"] != float64(10) || def["total"] != float64(25) || def["totalPages"] != float64(3) {
		t.Fatalf("defaults wrong: %v", def)
	}
	if def["type"] != store.KindText || def["hasMore"] != true {
		t.Fatalf("page 1 meta wrong: %v", def)
	}

	// 图片 tab：总数独立
	img := get("type=image")
	if img["type"] != store.KindImage || img["total"] != float64(3) || img["totalPages"] != float64(1) {
		t.Fatalf("image page wrong: %v", img)
	}
	if img["hasMore"] != false {
		t.Fatalf("image page should have no more: %v", img)
	}

	// 两个 tab 的数量
	counts := def["counts"].(map[string]any)
	if counts["text"] != float64(25) || counts["image"] != float64(3) {
		t.Fatalf("counts wrong: %v", counts)
	}

	// 最后一页没有更多
	last := get("page=3&size=10")
	if last["hasMore"] != false || len(last["records"].([]any)) != 5 {
		t.Fatalf("last page wrong: hasMore=%v n=%d", last["hasMore"], len(last["records"].([]any)))
	}

	// size 超上限时被夹住
	big := get("size=9999")
	if big["size"] != float64(500) {
		t.Fatalf("size should be capped at 500, got %v", big["size"])
	}

	// 未知 type 归为文本
	if odd := get("type=bogus"); odd["type"] != store.KindText {
		t.Fatalf("unknown type should fall back to text, got %v", odd["type"])
	}
}

func TestHandleSuppress(t *testing.T) {
	newServer := func() *server {
		return &server{
			store:      newTestStore(t),
			suppressor: lancoclip.NewSuppressor(),
			localIPs:   map[string]bool{},
			shareURL:   "http://192.168.1.10:6116",
		}
	}
	desktopUA := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Safari/605.1.15"

	post := func(s *server, remote, ua, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/suppress", strings.NewReader(body))
		req.RemoteAddr = remote
		req.Header.Set("User-Agent", ua)
		rec := httptest.NewRecorder()
		s.handleSuppress(rec, req)
		return rec
	}

	// 本机：登记抑制；唯一的那条记录必须保留（否则就是删了原始记录）
	s := newServer()
	s.store.AddText("from-page")
	rec := post(s, "127.0.0.1:5000", desktopUA, `{"kind":"text","content":"from-page"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("local suppress = %d, want 200", rec.Code)
	}
	if tc, _ := s.store.Counts(); tc != 1 {
		t.Fatalf("unique record must survive, got %d texts", tc)
	}
	if !s.suppressor.TakeText("from-page") {
		t.Fatal("text should be registered for suppression")
	}

	// 本机：真·重复（存在更早的同内容记录）才撤回最新那条
	sdup := newServer()
	sdup.store.AddText("dup")
	sdup.store.AddText("other")
	sdup.store.AddText("dup") // [dup, other, dup]
	post(sdup, "127.0.0.1:5000", desktopUA, `{"kind":"text","content":"dup"}`)
	if tc, _ := sdup.store.Counts(); tc != 2 {
		t.Fatalf("duplicate should be retracted, got %d texts", tc)
	}

	// 手机：不允许（手机上复制不经过本机剪贴板）
	s2 := newServer()
	rec2 := post(s2, "127.0.0.1:5000", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) Mobile/15E148", `{"kind":"text","content":"x"}`)
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("mobile suppress = %d, want 403", rec2.Code)
	}

	// 未知图片 / 空内容
	s3 := newServer()
	if rec := post(s3, "127.0.0.1:5000", desktopUA, `{"kind":"image","id":999}`); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown image = %d, want 404", rec.Code)
	}
	if rec := post(s3, "127.0.0.1:5000", desktopUA, `{"kind":"text","content":""}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty content = %d, want 400", rec.Code)
	}
	// 非 POST
	req := httptest.NewRequest(http.MethodGet, "/api/suppress", nil)
	req.RemoteAddr = "127.0.0.1:5000"
	recGET := httptest.NewRecorder()
	s3.handleSuppress(recGET, req)
	if recGET.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET = %d, want 405", recGET.Code)
	}
}

func TestIsMobileRequest(t *testing.T) {
	// Client Hint wins even when the User-Agent looks like a desktop one
	// (e.g. a proxy that rewrites the UA).
	req := httptest.NewRequest(http.MethodGet, "/api/records", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Safari/605.1.15")
	req.Header.Set("Sec-CH-UA-Mobile", "?1")
	if !isMobileRequest(req) {
		t.Error("Sec-CH-UA-Mobile: ?1 should be detected as mobile")
	}

	req2 := httptest.NewRequest(http.MethodGet, "/api/records", nil)
	req2.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Safari/605.1.15")
	if isMobileRequest(req2) {
		t.Error("desktop request should not be detected as mobile")
	}
}

func TestVirtualInterfaceDetection(t *testing.T) {
	virtual := []string{"utun3", "bridge0", "vmnet1", "docker0", "awdl0", "llw0", "anpi0", "ap1", "lo0", "gpd0"}
	physical := []string{"en0", "en1", "eth0", "wlan0"}
	for _, n := range virtual {
		if !isVirtualInterface(n) {
			t.Errorf("%s should be virtual", n)
		}
	}
	for _, n := range physical {
		if isVirtualInterface(n) {
			t.Errorf("%s should not be virtual", n)
		}
	}
}

func TestBestLocalIPPrefersPhysical(t *testing.T) {
	ip := bestLocalIP()
	if ip == "" || net.ParseIP(ip) == nil {
		t.Fatalf("bestLocalIP returned %q", ip)
	}
	// A virtual NIC such as utun (VPN) must not win over a physical NIC.
	ifs, err := net.Interfaces()
	if err != nil {
		return
	}
	for _, ifi := range ifs {
		if isVirtualInterface(ifi.Name) {
			continue
		}
		if v := firstIPv4(ifi); v != "" && ip == v {
			return // matched a physical interface, ok
		}
	}
	t.Logf("bestLocalIP=%s (no physical NIC found, fell back)", ip)
}

func TestHandleRecordsShape(t *testing.T) {
	s := &server{
		store:    newTestStore(t),
		localIPs: map[string]bool{},
		shareURL: "http://192.168.1.10:8899",
	}
	req := httptest.NewRequest(http.MethodGet, "/api/records", nil)
	req.RemoteAddr = "192.168.1.99:5000"
	rec := httptest.NewRecorder()

	s.handleRecords(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if body := rec.Body.String(); body == "" || body[0] != '{' {
		t.Fatalf("unexpected body: %s", body)
	}
}

// /api/settings：GET 任何人可看；POST 仅服务本机，保存立即生效。
func TestHandleSettings(t *testing.T) {
	newServer := func() *server {
		return &server{
			store:      newTestStore(t),
			suppressor: lancoclip.NewSuppressor(),
			localIPs:   map[string]bool{},
			shareURL:   "http://192.168.1.10:6116",
		}
	}
	desktopUA := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Safari/605.1.15"
	mobileUA := "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) Mobile/15E148"

	get := func(s *server) map[string]any {
		req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
		req.RemoteAddr = "192.168.1.99:5000"
		rec := httptest.NewRecorder()
		s.handleSettings(rec, req)
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("bad json: %v", err)
		}
		return out
	}

	post := func(s *server, remote, ua, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/settings", strings.NewReader(body))
		req.RemoteAddr = remote
		req.Header.Set("User-Agent", ua)
		rec := httptest.NewRecorder()
		s.handleSettings(rec, req)
		return rec
	}

	// GET：返回当前配置（默认值）
	s := newServer()
	cur := get(s)
	if cur["maxText"] != float64(store.DefaultMaxTextRecords) || cur["maxImage"] != float64(store.DefaultMaxImageRecords) {
		t.Fatalf("GET settings wrong: %v", cur)
	}

	// POST：本机桌面可以修改，并立即修剪
	for i := 1; i <= 12; i++ {
		s.store.AddText(fmt.Sprintf("keep-%d", i))
	}
	rec := post(s, "127.0.0.1:5000", desktopUA, `{"maxText":10,"maxImage":200}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("local POST = %d, want 200", rec.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if out["trimmedText"] != float64(2) {
		t.Fatalf("trimmedText = %v, want 2", out["trimmedText"])
	}
	if tc, _ := s.store.Counts(); tc != 10 {
		t.Fatalf("texts after POST = %d, want 10", tc)
	}

	// POST：手机 / 其他设备不允许
	s2 := newServer()
	if rec := post(s2, "127.0.0.1:5000", mobileUA, `{"maxText":10,"maxImage":10}`); rec.Code != http.StatusForbidden {
		t.Fatalf("mobile POST = %d, want 403", rec.Code)
	}
	if rec := post(s2, "192.168.1.99:5000", desktopUA, `{"maxText":10,"maxImage":10}`); rec.Code != http.StatusForbidden {
		t.Fatalf("remote POST = %d, want 403", rec.Code)
	}

	// POST：坏请求
	s3 := newServer()
	if rec := post(s3, "127.0.0.1:5000", desktopUA, `not-json`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad json POST = %d, want 400", rec.Code)
	}
	if rec := post(s3, "127.0.0.1:5000", desktopUA, `{"maxText":10}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing field POST = %d, want 400", rec.Code)
	}

	// 其他方法
	req := httptest.NewRequest(http.MethodPut, "/api/settings", nil)
	req.RemoteAddr = "127.0.0.1:5000"
	recPUT := httptest.NewRecorder()
	s3.handleSettings(recPUT, req)
	if recPUT.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT = %d, want 405", recPUT.Code)
	}
}

// trayItemLabel: 截断、换行压平、图片占位、时间戳格式。
func TestTrayItemLabel(t *testing.T) {
	now := time.Now()
	long := strings.Repeat("字", 40)
	got := trayItemLabel(store.Record{Kind: store.KindText, Content: long, CreatedAt: now.UnixMilli()})
	// 截断到 36 字 + 省略号 + 时间戳前缀 "HH:MM  "
	if !strings.HasSuffix(got, "…") || !strings.Contains(got, strings.Repeat("字", 36)) {
		t.Fatalf("label not truncated: %q", got)
	}
	if strings.Contains(got, strings.Repeat("字", 37)) {
		t.Fatal("label keeps more than 36 runes")
	}
	if len(got) < 6 || got[2] != ':' {
		t.Fatalf("label missing HH:MM stamp: %q", got)
	}

	got = trayItemLabel(store.Record{Kind: store.KindText, Content: "a\nb", CreatedAt: now.UnixMilli()})
	if !strings.Contains(got, "a b") {
		t.Fatalf("newline not flattened: %q", got)
	}

	got = trayItemLabel(store.Record{Kind: store.KindImage, CreatedAt: now.UnixMilli()})
	if !strings.Contains(got, "[图片]") {
		t.Fatalf("image placeholder missing: %q", got)
	}

	// 跨天记录带日期前缀
	old := now.AddDate(0, 0, -3)
	got = trayItemLabel(store.Record{Kind: store.KindText, Content: "hi", CreatedAt: old.UnixMilli()})
	wantDate := old.Format("01-02")
	if !strings.HasPrefix(got, wantDate) {
		t.Fatalf("old record should start with %q, got %q", wantDate, got)
	}

	// 空文本兜底
	got = trayItemLabel(store.Record{Kind: store.KindText, Content: "  \n ", CreatedAt: now.UnixMilli()})
	if !strings.Contains(got, "（空白文本）") {
		t.Fatalf("blank text fallback missing: %q", got)
	}
}

// server.ClearAll（tray.App）: 文本与图片一起清空，图片文件同步删除。
func TestServerClearAllViaTray(t *testing.T) {
	s := &server{store: newTestStore(t), localIPs: map[string]bool{}}
	s.store.AddText("keep-me-no-more")
	if added, err := s.store.AddImage([]byte{9, 'P', 'N', 'G'}); err != nil || !added {
		t.Fatalf("AddImage: added=%v err=%v", added, err)
	}
	imgs, _ := s.store.Page(store.KindImage, 1, 1)
	if len(imgs) == 0 {
		t.Fatal("no image record after AddImage")
	}
	id := imgs[0].ID
	path, ok := s.store.ImagePath(id)
	if !ok {
		t.Fatal("ImagePath should find the image")
	}

	if err := s.ClearAll(); err != nil {
		t.Fatalf("ClearAll: %v", err)
	}
	tc, ic := s.store.Counts()
	if tc != 0 || ic != 0 {
		t.Fatalf("counts after ClearAll = %d/%d, want 0/0", tc, ic)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("image file should be deleted")
	}
}

// /api/recent：文本+图片混合、最新在前，limit 夹取生效，meta 字段齐全。
func TestHandleRecent(t *testing.T) {
	s := &server{store: newTestStore(t), localIPs: map[string]bool{}, shareURL: "http://192.168.1.10:8899"}
	for i := 1; i <= 5; i++ {
		s.store.AddText(fmt.Sprintf("text-%d", i))
		time.Sleep(2 * time.Millisecond) // 保证 CreatedAt 可区分顺序
	}
	s.store.AddImage([]byte{1, 'P', 'N', 'G'})
	time.Sleep(2 * time.Millisecond)
	s.store.AddText("newest")

	get := func(query string) map[string]any {
		req := httptest.NewRequest(http.MethodGet, "/api/recent"+query, nil)
		req.RemoteAddr = "127.0.0.1:5000"
		rec := httptest.NewRecorder()
		s.handleRecent(rec, req)
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("bad json: %v", err)
		}
		return out
	}

	out := get("?limit=30")
	recs := out["records"].([]any)
	if len(recs) != 7 {
		t.Fatalf("want 7 records, got %d", len(recs))
	}
	first := recs[0].(map[string]any)
	if first["kind"] != store.KindText || first["content"] != "newest" {
		t.Fatalf("newest record wrong: %v", first)
	}
	// 第二新的是图片，content 为空、只带元数据（omitempty 序列化后键不存在）
	second := recs[1].(map[string]any)
	if second["kind"] != store.KindImage {
		t.Fatalf("second record should be image, got %v", second)
	}
	if v, ok := second["content"]; ok && v != "" {
		t.Fatalf("image record should not carry content: %v", second)
	}
	if out["shareUrl"] != s.shareURL {
		t.Fatalf("shareUrl wrong: %v", out["shareUrl"])
	}

	// limit 截断
	small := get("?limit=2")
	if got := len(small["records"].([]any)); got != 2 {
		t.Fatalf("limit=2 want 2 records, got %d", got)
	}
	// 非法 limit 回退到默认（HistoryCount=30）
	if got := len(get("?limit=-5")["records"].([]any)); got != 7 {
		t.Fatalf("bad limit should fall back, got %d", got)
	}
}

// /api/settings 的 autostart 扩展：GET 带字段，POST 可只提交 autostart。
// 注意：不能在测试里真的启用自启（mac 会写 LaunchAgent），只测关闭方向。
func TestHandleSettingsAutostart(t *testing.T) {
	s := &server{store: newTestStore(t), localIPs: map[string]bool{}, shareURL: "http://x:1"}

	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	rec := httptest.NewRecorder()
	s.handleSettings(rec, req)
	var cur map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &cur); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if _, ok := cur["autostart"]; !ok {
		t.Fatalf("GET settings missing autostart: %v", cur)
	}
	if _, ok := cur["autostartSupported"]; !ok {
		t.Fatalf("GET settings missing autostartSupported: %v", cur)
	}

	// 只提交 autostart=false 也应成功（旧逻辑强制要求 maxText/maxImage）
	body := `{"autostart": false}`
	req = httptest.NewRequest(http.MethodPost, "/api/settings", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:5000"
	rec = httptest.NewRecorder()
	s.handleSettings(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST autostart=false: code=%d body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if out["autostart"] != false {
		t.Fatalf("autostart should stay false: %v", out)
	}

	// 手机端（无权限）POST 被拒
	req = httptest.NewRequest(http.MethodPost, "/api/settings", strings.NewReader(body))
	req.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X)")
	rec = httptest.NewRecorder()
	s.handleSettings(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("mobile POST should be 403, got %d", rec.Code)
	}
}

// /api/settings 的快捷键扩展：GET 回显当前组合，POST 校验（非法 400、相同 400），
// 合法提交后 GET 返回新值。测试环境托盘未运行，hotkey.Update 是 no-op。
func TestHandleSettingsHotkeys(t *testing.T) {
	s := &server{store: newTestStore(t), localIPs: map[string]bool{}, shareURL: "http://x:1"}

	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/settings", strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:5000"
		rec := httptest.NewRecorder()
		s.handleSettings(rec, req)
		return rec
	}

	// 非法组合 → 400
	if rec := post(`{"hotkeyQR":"q","hotkeyHistory":"meta+shift+v"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("modifier-less combo should 400, got %d", rec.Code)
	}
	// 两个相同 → 400
	if rec := post(`{"hotkeyQR":"meta+shift+q","hotkeyHistory":"cmd+shift+q"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("identical combos should 400, got %d", rec.Code)
	}
	// 合法提交 → 200，GET 回显新值
	rec := post(`{"hotkeyQR":"meta+alt+q","hotkeyHistory":"meta+shift+v"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid combo should 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	grec := httptest.NewRecorder()
	s.handleSettings(grec, req)
	var out map[string]any
	if err := json.Unmarshal(grec.Body.Bytes(), &out); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if out["hotkeyQR"] != "meta+alt+q" {
		t.Fatalf("hotkeyQR should persist, got %v", out["hotkeyQR"])
	}
	// 空串重置 → 恢复平台默认
	if rec := post(`{"hotkeyQR":"","hotkeyHistory":""}`); rec.Code != http.StatusOK {
		t.Fatalf("empty reset should 200, got %d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	grec = httptest.NewRecorder()
	s.handleSettings(grec, req)
	_ = json.Unmarshal(grec.Body.Bytes(), &out)
	if out["hotkeyQR"] == "" {
		t.Fatalf("empty combo should fall back to default, got %v", out["hotkeyQR"])
	}
}

// 切网适配：启动后网络变了（旧 IP 失效），托盘/快捷键打开的 URL、
// canClear 的本机地址集都必须跟着刷新，而不是沿用启动时缓存的旧值。
func TestRefreshNetAfterNetworkSwitch(t *testing.T) {
	// port != 0 表示「生产模式」：缓存过期后 refreshNet 必须重新探测
	s := &server{
		port:     6116,
		localIPs: map[string]bool{"203.0.113.99": true}, // 假设的旧 IP
	}
	s.shareURL = "http://203.0.113.99:6116"
	s.netChecked = time.Now().Add(-time.Minute) // 缓存已过期

	// URL 必须重算成当前机器的真实地址，而不是旧缓存
	want := fmt.Sprintf("http://%s:6116", bestLocalIP())
	if got := s.URL(); got != want {
		t.Fatalf("URL() = %q, want %q (stale cache must be refreshed)", got, want)
	}
	// 本机地址集同样刷新：旧 IP 被清掉
	if s.localIPs["203.0.113.99"] {
		t.Fatal("stale local IP should have been dropped from the set")
	}

	// TTL 内不重探：即使把缓存改回旧值，短时间内 URL 仍保持刚算出的结果
	s.netChecked = time.Now()
	s.shareURL = "http://203.0.113.99:6116"
	if got := s.URL(); got != "http://203.0.113.99:6116" {
		t.Fatalf("URL() = %q, want stale value within TTL", got)
	}
}

// 重复启动：已在运行的实例收到 /api/activate 后自报家门并弹通知。
func TestHandleActivate(t *testing.T) {
	orig := openBrowser
	var opened []string
	openBrowser = func(u string) { opened = append(opened, u) }
	defer func() { openBrowser = orig }()

	s := &server{port: 6116, store: newTestStore(t), localIPs: map[string]bool{}, shareURL: "http://192.168.1.10:6116"}
	post := func(remote string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/activate", strings.NewReader("{}"))
		req.RemoteAddr = remote
		rec := httptest.NewRecorder()
		s.handleActivate(rec, req)
		return rec
	}

	rec := post("127.0.0.1:54321")
	if rec.Code != http.StatusOK {
		t.Fatalf("activate from loopback = %d, want 200", rec.Code)
	}
	var body struct {
		OK  bool   `json:"ok"`
		App string `json:"app"`
		URL string `json:"url"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode activate response: %v", err)
	}
	if !body.OK || body.App != "lanoclip" {
		t.Fatalf("activate body = %+v, want ok + app=lanoclip", body)
	}
	// 本机页面地址走 loopback，不依赖局域网 IP（没网时也要能打开）
	if body.URL != "http://127.0.0.1:6116" {
		t.Fatalf("activate url = %q, want http://127.0.0.1:6116", body.URL)
	}
	// 通知发不出去时（测试进程没有 .app bundle）必须兜底直接开页面，
	// 否则用户第二次双击会毫无反应。
	if !notify.Supported() {
		if len(opened) != 1 || opened[0] != body.URL {
			t.Fatalf("fallback open = %v, want [%s]", opened, body.URL)
		}
	}

	// 手机 / 非本机的来源不能触发桌面通知
	if rec := post("203.0.113.99:5000"); rec.Code != http.StatusForbidden {
		t.Fatalf("activate from remote = %d, want 403", rec.Code)
	}

	// 探测只走 POST
	req := httptest.NewRequest(http.MethodGet, "/api/activate", nil)
	req.RemoteAddr = "127.0.0.1:1"
	recGET := httptest.NewRecorder()
	s.handleActivate(recGET, req)
	if recGET.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET activate = %d, want 405", recGET.Code)
	}
}

// 单实例探测：只认自报 lanoclip 的端口，别的服务占着也不能误判。
func TestRunningInstanceURL(t *testing.T) {
	mine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/activate" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "app": "lanoclip", "url": "http://127.0.0.1:6116"})
	}))
	defer mine.Close()

	if got := runningInstanceURL(portOf(t, mine.URL), 1); got != "http://127.0.0.1:6116" {
		t.Fatalf("runningInstanceURL = %q, want the running instance's URL", got)
	}

	// 别人的服务（返回 HTML）占了端口：认不出来就当没实例，程序照常启动
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>not lanoclip</html>"))
	}))
	defer foreign.Close()
	if got := runningInstanceURL(portOf(t, foreign.URL), 1); got != "" {
		t.Fatalf("runningInstanceURL = %q, want empty for a foreign service", got)
	}

	// 没人监听的端口同样返回空
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	free := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	if got := runningInstanceURL(free, 1); got != "" {
		t.Fatalf("runningInstanceURL = %q, want empty for a closed port", got)
	}
}

// portOf extracts the port from an httptest server URL (http://127.0.0.1:PORT).
func portOf(t *testing.T, rawURL string) int {
	t.Helper()
	_, p, err := net.SplitHostPort(strings.TrimPrefix(rawURL, "http://"))
	if err != nil {
		t.Fatalf("split %q: %v", rawURL, err)
	}
	port, err := strconv.Atoi(p)
	if err != nil {
		t.Fatalf("port %q: %v", p, err)
	}
	return port
}

// canClear 在切网后同样要认得「新的本机 IP」：请求来自当前真实网卡地址时放行。
func TestCanClearAfterNetworkSwitch(t *testing.T) {
	cur := bestLocalIP()
	if cur == "127.0.0.1" {
		t.Skip("no LAN IP on this machine")
	}
	s := &server{port: 6116, localIPs: map[string]bool{"203.0.113.99": true}}
	s.shareURL = "http://203.0.113.99:6116"
	s.netChecked = time.Now().Add(-time.Minute)

	r := &http.Request{
		RemoteAddr: cur + ":5000",
		Header:     http.Header{"User-Agent": []string{"Mozilla/5.0 (Macintosh)"}},
	}
	if !s.canClear(r) {
		t.Fatalf("canClear from current LAN IP %s = false, want true", cur)
	}
}
