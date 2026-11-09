package server

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"image"
	"image/jpeg"
	"image/png"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"lanoclip/internal/lanaddr"
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

// newTestServer builds a Server with a throwaway store, ready for handler tests.
func newTestServer(t *testing.T) *Server {
	t.Helper()
	return &Server{
		store:      newTestStore(t),
		suppressor: lancoclip.NewSuppressor(),
		localIPs:   map[string]bool{},
		shareURL:   "http://192.168.1.10:6116",
	}
}

// serve runs a request through the real router (routes + handlers) and returns
// the recorder. It centralises the boilerplate every handler test used to repeat
// inline: building the request, setting RemoteAddr / User-Agent, and dispatching.
func serve(s *Server, method, target, remote, ua, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	// Routes 最外层有 Host 白名单；测试统一模拟从服务本机打开页面。
	req.Host = "127.0.0.1:6116"
	req.RemoteAddr = remote
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)
	return rec
}

// decodeJSON unmarshals a recorder body into a map[string]any, failing the test
// on malformed JSON.
func decodeJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("bad json (%s): %v", rec.Body.String(), err)
	}
	return out
}

func TestCanClear(t *testing.T) {
	// canClear 只按来源 IP 判定，User-Agent / Sec-CH-UA-Mobile 一律不参与：
	// 本机任意进程可调用特权接口是产品取舍（用户明确不做鉴权）。
	// ua 列仍保留，正是为了证明同一 IP 换任何 UA 结果都不变。
	s := &Server{localIPs: map[string]bool{"192.168.1.10": true}}

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
		{"mobile UA from loopback is local", "127.0.0.1:5000", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) Mobile/15E148 Safari/604.1", true},
		{"mobile UA via own LAN IP is local", "192.168.1.10:5000", "Mozilla/5.0 (Linux; Android 14) Mobile Safari", true},
	}
	for _, c := range cases {
		r := &http.Request{RemoteAddr: c.remote, Header: http.Header{"User-Agent": []string{c.ua}}}
		if got := s.canClear(r); got != c.want {
			t.Errorf("%s: canClear(%s, %q) = %v, want %v", c.name, c.remote, c.ua, got, c.want)
		}
	}
}

func TestHandleRecordsPagination(t *testing.T) {
	s := &Server{
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
		return decodeJSON(t, serve(s, http.MethodGet, "/api/records?"+query, "192.168.1.99:5000", "", ""))
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
	desktopUA := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Safari/605.1.15"

	post := func(sv *Server, remote, ua, body string) *httptest.ResponseRecorder {
		return serve(sv, http.MethodPost, "/api/suppress", remote, ua, body)
	}

	// 本机：登记抑制；唯一的那条记录必须保留（否则就是删了原始记录）
	s := newTestServer(t)
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
	sdup := newTestServer(t)
	sdup.store.AddText("dup")
	sdup.store.AddText("other")
	sdup.store.AddText("dup") // [dup, other, dup]
	post(sdup, "127.0.0.1:5000", desktopUA, `{"kind":"text","content":"dup"}`)
	if tc, _ := sdup.store.Counts(); tc != 2 {
		t.Fatalf("duplicate should be retracted, got %d texts", tc)
	}

	// 非本机来源（手机经 LAN 访问）→ 403；只按 RemoteAddr 判定，与 UA 无关。
	s2 := newTestServer(t)
	rec2 := post(s2, "192.168.1.99:5000", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) Mobile/15E148", `{"kind":"text","content":"x"}`)
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("non-local suppress = %d, want 403", rec2.Code)
	}
	// 反过来：本机回环即便带手机 UA 也放行（UA 不是安全边界）。
	if rec := post(s2, "127.0.0.1:5000", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) Mobile/15E148", `{"kind":"text","content":"y"}`); rec.Code != http.StatusOK {
		t.Fatalf("local suppress with mobile UA = %d, want 200", rec.Code)
	}

	// 未知图片 / 空内容
	s3 := newTestServer(t)
	if rec := post(s3, "127.0.0.1:5000", desktopUA, `{"kind":"image","id":999}`); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown image = %d, want 404", rec.Code)
	}
	if rec := post(s3, "127.0.0.1:5000", desktopUA, `{"kind":"text","content":""}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty content = %d, want 400", rec.Code)
	}
	// 非 POST
	recGET := serve(s3, http.MethodGet, "/api/suppress", "127.0.0.1:5000", "", "")
	if recGET.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET = %d, want 405", recGET.Code)
	}
}

func TestVirtualInterfaceDetection(t *testing.T) {
	virtual := []string{"utun3", "bridge0", "vmnet1", "docker0", "awdl0", "llw0", "anpi0", "ap1", "lo0", "gpd0"}
	physical := []string{"en0", "en1", "eth0", "wlan0"}
	for _, n := range virtual {
		if !lanaddr.IsVirtualInterface(n) {
			t.Errorf("%s should be virtual", n)
		}
	}
	for _, n := range physical {
		if lanaddr.IsVirtualInterface(n) {
			t.Errorf("%s should not be virtual", n)
		}
	}
}

func TestBestLocalIPPrefersPhysical(t *testing.T) {
	ip := lanaddr.BestLocalIP()
	if ip == "" || net.ParseIP(ip) == nil {
		t.Fatalf("bestLocalIP returned %q", ip)
	}
	// A virtual NIC such as utun (VPN) must not win over a physical NIC.
	ifs, err := net.Interfaces()
	if err != nil {
		return
	}
	for _, ifi := range ifs {
		if lanaddr.IsVirtualInterface(ifi.Name) {
			continue
		}
		if v := lanaddr.FirstIPv4(ifi); v != "" && ip == v {
			return // matched a physical interface, ok
		}
	}
	t.Logf("bestLocalIP=%s (no physical NIC found, fell back)", ip)
}

func TestHandleRecordsShape(t *testing.T) {
	s := &Server{
		store:    newTestStore(t),
		localIPs: map[string]bool{},
		shareURL: "http://192.168.1.10:8899",
	}
	rec := serve(s, http.MethodGet, "/api/records", "192.168.1.99:5000", "", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if body := rec.Body.String(); body == "" || body[0] != '{' {
		t.Fatalf("unexpected body: %s", body)
	}
}

// /api/settings：GET 任何人可看；POST 仅服务本机，保存立即生效。
func TestHandleSettings(t *testing.T) {
	desktopUA := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Safari/605.1.15"
	mobileUA := "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) Mobile/15E148"

	get := func(sv *Server) map[string]any {
		return decodeJSON(t, serve(sv, http.MethodGet, "/api/settings", "192.168.1.99:5000", "", ""))
	}
	post := func(sv *Server, remote, ua, body string) *httptest.ResponseRecorder {
		return serve(sv, http.MethodPost, "/api/settings", remote, ua, body)
	}

	// GET：返回当前配置（默认值）
	s := newTestServer(t)
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
	out := decodeJSON(t, rec)
	if out["trimmedText"] != float64(2) {
		t.Fatalf("trimmedText = %v, want 2", out["trimmedText"])
	}
	if tc, _ := s.store.Counts(); tc != 10 {
		t.Fatalf("texts after POST = %d, want 10", tc)
	}

	// POST：本机回环（哪怕带手机 UA）允许，非本机 IP 拒绝 —— 只看来源 IP。
	s2 := newTestServer(t)
	if rec := post(s2, "127.0.0.1:5000", mobileUA, `{"maxText":10,"maxImage":10}`); rec.Code != http.StatusOK {
		t.Fatalf("local POST with mobile UA = %d, want 200", rec.Code)
	}
	if rec := post(s2, "192.168.1.99:5000", desktopUA, `{"maxText":10,"maxImage":10}`); rec.Code != http.StatusForbidden {
		t.Fatalf("remote POST = %d, want 403", rec.Code)
	}

	// POST：坏请求
	s3 := newTestServer(t)
	if rec := post(s3, "127.0.0.1:5000", desktopUA, `not-json`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad json POST = %d, want 400", rec.Code)
	}
	// 只提交 maxText：另一个上限取当前 effective 值补齐，不再静默忽略/报错。
	before := s3.store.MaxImage()
	if rec := post(s3, "127.0.0.1:5000", desktopUA, `{"maxText":10}`); rec.Code != http.StatusOK {
		t.Fatalf("single-field POST = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if s3.store.MaxText() != 10 || s3.store.MaxImage() != before {
		t.Fatalf("single-field POST limits = %d/%d, want 10/%d",
			s3.store.MaxText(), s3.store.MaxImage(), before)
	}

	// 其他方法
	recPUT := serve(s3, http.MethodPut, "/api/settings", "127.0.0.1:5000", "", "")
	if recPUT.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT = %d, want 405", recPUT.Code)
	}
}

// trayItemLabel: 截断、换行压平、图片占位、时间戳格式。
func TestTrayItemLabel(t *testing.T) {
	now := time.Now()
	long := strings.Repeat("字", 40)
	got := TrayItemLabel(store.Record{Kind: store.KindText, Content: long, CreatedAt: now.UnixMilli()})
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

	got = TrayItemLabel(store.Record{Kind: store.KindText, Content: "a\nb", CreatedAt: now.UnixMilli()})
	if !strings.Contains(got, "a b") {
		t.Fatalf("newline not flattened: %q", got)
	}

	got = TrayItemLabel(store.Record{Kind: store.KindImage, CreatedAt: now.UnixMilli()})
	if !strings.Contains(got, "[图片]") {
		t.Fatalf("image placeholder missing: %q", got)
	}

	// 跨天记录带日期前缀
	old := now.AddDate(0, 0, -3)
	got = TrayItemLabel(store.Record{Kind: store.KindText, Content: "hi", CreatedAt: old.UnixMilli()})
	wantDate := old.Format("01-02")
	if !strings.HasPrefix(got, wantDate) {
		t.Fatalf("old record should start with %q, got %q", wantDate, got)
	}

	// 空文本兜底
	got = TrayItemLabel(store.Record{Kind: store.KindText, Content: "  \n ", CreatedAt: now.UnixMilli()})
	if !strings.Contains(got, "（空白文本）") {
		t.Fatalf("blank text fallback missing: %q", got)
	}
}

// server.ClearAll（tray.App）: 文本与图片一起清空，图片文件同步删除。
func TestServerClearAllViaTray(t *testing.T) {
	s := &Server{store: newTestStore(t), localIPs: map[string]bool{}}
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
	s := &Server{store: newTestStore(t), localIPs: map[string]bool{}, shareURL: "http://192.168.1.10:8899"}
	for i := 1; i <= 5; i++ {
		s.store.AddText(fmt.Sprintf("text-%d", i))
		time.Sleep(2 * time.Millisecond) // 保证 CreatedAt 可区分顺序
	}
	s.store.AddImage([]byte{1, 'P', 'N', 'G'})
	time.Sleep(2 * time.Millisecond)
	s.store.AddText("newest")

	get := func(query string) map[string]any {
		return decodeJSON(t, serve(s, http.MethodGet, "/api/recent"+query, "127.0.0.1:5000", "", ""))
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
	s := &Server{store: newTestStore(t), localIPs: map[string]bool{}, shareURL: "http://x:1"}

	cur := decodeJSON(t, serve(s, http.MethodGet, "/api/settings", "", "", ""))
	if _, ok := cur["autostart"]; !ok {
		t.Fatalf("GET settings missing autostart: %v", cur)
	}
	if _, ok := cur["autostartSupported"]; !ok {
		t.Fatalf("GET settings missing autostartSupported: %v", cur)
	}

	// 只提交 autostart=false 也应成功（旧逻辑强制要求 maxText/maxImage）
	rec := serve(s, http.MethodPost, "/api/settings", "127.0.0.1:5000", "", `{"autostart": false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST autostart=false: code=%d body=%s", rec.Code, rec.Body.String())
	}
	out := decodeJSON(t, rec)
	if out["autostart"] != false {
		t.Fatalf("autostart should stay false: %v", out)
	}

	// 非本机来源（手机经 LAN 访问）POST 被拒；按 RemoteAddr 判定，与 UA 无关。
	rec = serve(s, http.MethodPost, "/api/settings", "192.168.1.99:5000", "", `{"autostart": false}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-local POST should be 403, got %d", rec.Code)
	}
}

// /api/settings 的快捷键扩展：GET 回显当前组合，POST 校验（非法 400、相同 400），
// 合法提交后 GET 返回新值。测试环境托盘未运行，hotkey.Update 是 no-op。
func TestHandleSettingsHotkeys(t *testing.T) {
	s := &Server{store: newTestStore(t), localIPs: map[string]bool{}, shareURL: "http://x:1"}

	post := func(body string) *httptest.ResponseRecorder {
		return serve(s, http.MethodPost, "/api/settings", "127.0.0.1:5000", "", body)
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
	out := decodeJSON(t, serve(s, http.MethodGet, "/api/settings", "", "", ""))
	if out["hotkeyQR"] != "meta+alt+q" {
		t.Fatalf("hotkeyQR should persist, got %v", out["hotkeyQR"])
	}
	// 空串重置 → 恢复平台默认
	if rec := post(`{"hotkeyQR":"","hotkeyHistory":""}`); rec.Code != http.StatusOK {
		t.Fatalf("empty reset should 200, got %d", rec.Code)
	}
	out = decodeJSON(t, serve(s, http.MethodGet, "/api/settings", "", "", ""))
	if out["hotkeyQR"] == "" {
		t.Fatalf("empty combo should fall back to default, got %v", out["hotkeyQR"])
	}
}

// 单字段 limit：只提交 maxText 或 maxImage 时，另一项保持当前 effective 值。
func TestSettingsSingleLimitField(t *testing.T) {
	s := newTestServer(t)
	post := func(body string) *httptest.ResponseRecorder {
		return serve(s, http.MethodPost, "/api/settings", "127.0.0.1:5000", "", body)
	}
	get := func() map[string]any {
		return decodeJSON(t, serve(s, http.MethodGet, "/api/settings", "", "", ""))
	}

	origImage := s.store.MaxImage()
	if rec := post(`{"maxText":100}`); rec.Code != http.StatusOK {
		t.Fatalf("maxText only = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	out := get()
	if out["maxText"] != float64(100) || out["maxImage"] != float64(origImage) {
		t.Fatalf("after maxText only: %v (want 100/%d)", out, origImage)
	}

	origText := s.store.MaxText()
	if rec := post(`{"maxImage":50}`); rec.Code != http.StatusOK {
		t.Fatalf("maxImage only = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	out = get()
	if out["maxText"] != float64(origText) || out["maxImage"] != float64(50) {
		t.Fatalf("after maxImage only: %v (want %d/50)", out, origText)
	}

	// 提交的数值仍必须为正。
	if rec := post(`{"maxText":0}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("maxText=0 = %d, want 400", rec.Code)
	}
}

// 设置落盘失败时 limits 事务必须回滚：被裁掉的记录要放回内存，且后续
// 成功落盘的内容不能少（RestoreTrim 生效）。
func TestSettingsSaveFailureRestoresTrimmedRecords(t *testing.T) {
	dir := t.TempDir()
	st, err := store.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	s := &Server{
		store:      st,
		suppressor: lancoclip.NewSuppressor(),
		localIPs:   map[string]bool{},
		shareURL:   "http://127.0.0.1:6116",
	}
	for i := 0; i < 20; i++ {
		st.AddText(fmt.Sprintf("keep-%02d", i))
	}

	// texts.json.tmp 预置成目录 → Save() 的 stageFile 必失败（跨平台）。
	if err := os.Mkdir(filepath.Join(dir, "texts.json.tmp"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	rec := serve(s, http.MethodPost, "/api/settings", "127.0.0.1:5000", "", `{"maxText":10,"maxImage":5}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("settings save failure = %d, want 500 (%s)", rec.Code, rec.Body.String())
	}
	// RestoreTrim 生效：20 条仍在内存里。
	out := decodeJSON(t, serve(s, http.MethodGet, "/api/records", "127.0.0.1:5000", "", ""))
	if out["total"] != float64(20) {
		t.Fatalf("after failed settings total = %v, want 20", out["total"])
	}
	// 上限也必须回滚，否则后续 21 条会被新上限裁掉。
	if st.MaxText() != store.DefaultMaxTextRecords || st.MaxImage() != store.DefaultMaxImageRecords {
		t.Fatalf("limits after rollback = %d/%d, want defaults", st.MaxText(), st.MaxImage())
	}

	// 解除故障后再落盘：20 + 1 条都要写入 texts.json。
	if err := os.Remove(filepath.Join(dir, "texts.json.tmp")); err != nil {
		t.Fatalf("remove blocker: %v", err)
	}
	if rec := serve(s, http.MethodPost, "/api/add-text", "127.0.0.1:5000", "", `{"content":"after-restore"}`); rec.Code != http.StatusOK {
		t.Fatalf("add after restore = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	raw, err := os.ReadFile(filepath.Join(dir, "texts.json"))
	if err != nil {
		t.Fatalf("read texts.json: %v", err)
	}
	var saved []store.Record
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatalf("decode texts.json: %v", err)
	}
	if len(saved) != 21 {
		t.Fatalf("texts.json has %d records, want 21", len(saved))
	}
}

// 系统保留组合必须被拒；带 Shift 的默认组合（⌘⇧Q）不受影响。
func TestReservedHotkeyRejected(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("no system-reserved hotkey table for this platform")
	}
	s := newTestServer(t)
	post := func(body string) *httptest.ResponseRecorder {
		return serve(s, http.MethodPost, "/api/settings", "127.0.0.1:5000", "", body)
	}

	var reserved, allowed string
	if runtime.GOOS == "darwin" {
		reserved = `{"hotkeyQR":"meta+q","hotkeyHistory":"meta+shift+v"}`
		allowed = `{"hotkeyQR":"meta+shift+q","hotkeyHistory":"meta+shift+v"}`
	} else {
		reserved = `{"hotkeyQR":"ctrl+c","hotkeyHistory":"ctrl+shift+v"}`
		allowed = `{"hotkeyQR":"ctrl+shift+q","hotkeyHistory":"ctrl+shift+v"}`
	}

	rec := post(reserved)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("reserved combo = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "系统保留") {
		t.Fatalf("reserved combo body = %s", rec.Body.String())
	}
	if rec := post(allowed); rec.Code != http.StatusOK {
		t.Fatalf("allowed combo = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
}

// 切网适配：启动后网络变了（旧 IP 失效），托盘/快捷键打开的 URL、
// canClear 的本机地址集都必须跟着刷新，而不是沿用启动时缓存的旧值。
func TestRefreshNetAfterNetworkSwitch(t *testing.T) {
	// port != 0 表示「生产模式」：缓存过期后 refreshNet 必须重新探测
	s := &Server{
		port:     6116,
		localIPs: map[string]bool{"203.0.113.99": true}, // 假设的旧 IP
	}
	s.shareURL = "http://203.0.113.99:6116"
	s.netChecked = time.Now().Add(-time.Minute) // 缓存已过期

	// URL 必须重算成当前机器的真实地址，而不是旧缓存
	want := fmt.Sprintf("http://%s:6116", lanaddr.BestLocalIP())
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
	orig := Open
	var opened []string
	Open = func(u string) { opened = append(opened, u) }
	defer func() { Open = orig }()

	s := &Server{port: 6116, store: newTestStore(t), localIPs: map[string]bool{}, shareURL: "http://192.168.1.10:6116"}
	post := func(remote string) *httptest.ResponseRecorder {
		return serve(s, http.MethodPost, "/api/activate", remote, "", "{}")
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
	recGET := serve(s, http.MethodGet, "/api/activate", "127.0.0.1:1", "", "")
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
	cur := lanaddr.BestLocalIP()
	if cur == "127.0.0.1" {
		t.Skip("no LAN IP on this machine")
	}
	s := &Server{port: 6116, localIPs: map[string]bool{"203.0.113.99": true}}
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

// 手机端推送：/api/add-text 与 /api/add-image。
// 这两个接口故意对局域网开放（不像 /api/clear 只认本机）——手机能往电脑上
// 写记录正是它的用途，且全程不碰系统剪贴板，不会让本机 watcher 重复记录。
func TestHandleAddFromPhone(t *testing.T) {
	s := &Server{
		store:      newTestStore(t),
		suppressor: lancoclip.NewSuppressor(),
		localIPs:   map[string]bool{},
		shareURL:   "http://192.168.1.10:6116",
	}
	phoneUA := "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) Mobile/15E148"

	postText := func(body string) *httptest.ResponseRecorder {
		return serve(s, http.MethodPost, "/api/add-text", "192.168.1.99:5000", phoneUA, body)
	}
	decode := func(rec *httptest.ResponseRecorder) map[string]any {
		return decodeJSON(t, rec)
	}

	// 手机也能写：200 且确实入库
	rec := postText(`{"content":"来自手机的文本"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("phone add text = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if decode(rec)["added"] != true {
		t.Fatalf("first add should report added=true: %s", rec.Body.String())
	}
	// 相邻重复：added=false，但不是错误
	if out := decode(postText(`{"content":"来自手机的文本"}`)); out["added"] != false {
		t.Fatalf("duplicate add should report added=false: %v", out)
	}
	if postText(`{"content":"   "}`).Code != http.StatusBadRequest {
		t.Fatal("blank content should be 400")
	}
	getRec := serve(s, http.MethodGet, "/api/add-text", "", "", "")
	if getRec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /api/add-text = %d, want 405", getRec.Code)
	}

	// ── 图片上传 ──
	upload := func(name string, data []byte) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, err := mw.CreateFormFile("image", name)
		if err != nil {
			t.Fatalf("CreateFormFile: %v", err)
		}
		if _, err := fw.Write(data); err != nil {
			t.Fatalf("write part: %v", err)
		}
		if err := mw.Close(); err != nil {
			t.Fatalf("close multipart: %v", err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/add-image", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.RemoteAddr = "192.168.1.99:5000"
		req.Header.Set("User-Agent", phoneUA)
		rec := httptest.NewRecorder()
		s.handleAddImage(rec, req)
		return rec
	}

	// 相册里挑出来的 JPEG：必须转成 PNG 入库（/api/image 统一按 image/png 输出）
	var jpg bytes.Buffer
	if err := jpeg.Encode(&jpg, image.NewRGBA(image.Rect(0, 0, 4, 4)), nil); err != nil {
		t.Fatalf("encode test jpeg: %v", err)
	}
	rec = upload("photo.jpg", jpg.Bytes())
	if rec.Code != http.StatusOK {
		t.Fatalf("jpeg upload = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if decode(rec)["added"] != true {
		t.Fatalf("jpeg upload should add: %s", rec.Body.String())
	}
	// 同一张再传 → added=false
	if out := decode(upload("photo.jpg", jpg.Bytes())); out["added"] != false {
		t.Fatalf("duplicate image should report added=false: %v", out)
	}
	// 非图片 → 415
	if rec := upload("notes.txt", []byte("definitely not an image")); rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("non-image upload = %d, want 415", rec.Code)
	}
	// 表单里没有 image 字段 → 400
	empty := httptest.NewRequest(http.MethodPost, "/api/add-image", strings.NewReader(""))
	empty.Header.Set("Content-Type", "multipart/form-data; boundary=zzz")
	emptyRec := httptest.NewRecorder()
	s.handleAddImage(emptyRec, empty)
	if emptyRec.Code != http.StatusBadRequest {
		t.Fatalf("form without image = %d, want 400", emptyRec.Code)
	}

	// 落盘的必须是合法 PNG
	recs, _ := s.store.Page(store.KindImage, 1, 10)
	if len(recs) != 1 {
		t.Fatalf("image records = %d, want 1", len(recs))
	}
	path, ok := s.store.ImagePath(recs[0].ID)
	if !ok {
		t.Fatal("ImagePath missing for the uploaded record")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read stored image: %v", err)
	}
	if _, err := png.Decode(bytes.NewReader(raw)); err != nil {
		t.Fatalf("stored image is not a valid PNG: %v", err)
	}
}

// 去年同月同日不能再显示成「今天 HH:MM」。
func TestTrayItemLabelLastYear(t *testing.T) {
	now := time.Now()
	lastYear := time.Date(now.Year()-1, now.Month(), now.Day(), 12, 30, 0, 0, now.Location())
	got := TrayItemLabel(store.Record{Kind: store.KindText, Content: "hi", CreatedAt: lastYear.UnixMilli()})
	wantStamp := lastYear.Format("01-02 15:04")
	if !strings.HasPrefix(got, wantStamp) {
		t.Fatalf("去年同月同日应带日期前缀 %q, got %q", wantStamp, got)
	}
}

// PNG 走透传分支，也必须挡住超高像素（否则前端解码会吃掉几百 MB）。
func TestToPNGBytesHugePNG(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatalf("encode: %v", err)
	}
	small := buf.Bytes()
	if got, err := toPNGBytes(small); err != nil || !bytes.Equal(got, small) {
		t.Fatalf("small PNG should pass through unchanged: err=%v", err)
	}

	// 只改 IHDR 里的宽高并重算 CRC：DecodeConfig 读头部就能拿到尺寸，
	// 不需要真的分配 100000×100000 的像素。
	huge := bytes.Clone(small)
	binary.BigEndian.PutUint32(huge[16:20], 100000)
	binary.BigEndian.PutUint32(huge[20:24], 100000)
	binary.BigEndian.PutUint32(huge[29:33], crc32.ChecksumIEEE(huge[12:29]))
	if _, err := toPNGBytes(huge); err == nil {
		t.Fatal("huge PNG should be rejected by the pixel limit")
	}
}

// method 校验收到 Routes 的包装器里后，405 仍统一且带 Allow 头。
func TestAllowMethods(t *testing.T) {
	s := newTestServer(t)
	postOnly := serve(s, http.MethodPut, "/api/clear", "127.0.0.1:5000", "", "")
	if postOnly.Code != http.StatusMethodNotAllowed || postOnly.Header().Get("Allow") != "POST" {
		t.Fatalf("POST-only 405 = %d, Allow = %q", postOnly.Code, postOnly.Header().Get("Allow"))
	}
	settings := serve(s, http.MethodPut, "/api/settings", "127.0.0.1:5000", "", "")
	if settings.Code != http.StatusMethodNotAllowed || settings.Header().Get("Allow") != "GET, POST" {
		t.Fatalf("settings 405 = %d, Allow = %q", settings.Code, settings.Header().Get("Allow"))
	}
}

// 暂停开关：可单独提交、状态跨接口可见、暂停期间推送不入库。
func TestSettingsPausedAndStatus(t *testing.T) {
	s := newTestServer(t)
	remote := "127.0.0.1:5000"
	desktopUA := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Safari/605.1.15"

	get := serve(s, http.MethodGet, "/api/settings", remote, desktopUA, "")
	if get.Code != http.StatusOK {
		t.Fatalf("GET settings = %d", get.Code)
	}
	if out := decodeJSON(t, get); out["paused"] != false {
		t.Fatalf("default paused = %v, want false", out["paused"])
	}

	// 单独提交 paused（不带 maxText/maxImage）也应通过校验
	post := serve(s, http.MethodPost, "/api/settings", remote, desktopUA, `{"paused":true}`)
	if post.Code != http.StatusOK {
		t.Fatalf("POST paused = %d: %s", post.Code, post.Body.String())
	}
	if out := decodeJSON(t, post); out["paused"] != true {
		t.Fatalf("POST paused response = %v", out["paused"])
	}
	if !s.store.Paused() {
		t.Fatal("store should be paused")
	}

	recs := serve(s, http.MethodGet, "/api/records", remote, desktopUA, "")
	out := decodeJSON(t, recs)
	if out["paused"] != true {
		t.Fatalf("records paused = %v, want true", out["paused"])
	}
	if _, ok := out["storageError"]; !ok {
		t.Fatal("records should expose storageError")
	}

	add := serve(s, http.MethodPost, "/api/add-text", remote, desktopUA, `{"content":"secret"}`)
	addOut := decodeJSON(t, add)
	if addOut["paused"] != true || addOut["added"] != false {
		t.Fatalf("add while paused = %v", addOut)
	}
	if _, total := s.store.Page(store.KindText, 1, 10); total != 0 {
		t.Fatalf("paused add must not create a record, total = %d", total)
	}

	resume := serve(s, http.MethodPost, "/api/settings", remote, desktopUA, `{"paused":false}`)
	if resume.Code != http.StatusOK || s.store.Paused() {
		t.Fatalf("resume = %d, paused = %v", resume.Code, s.store.Paused())
	}
}

func TestSecurityHeaders(t *testing.T) {
	s := newTestServer(t)
	rec := serve(s, http.MethodGet, "/api/records", "127.0.0.1:5000", "", "")
	h := rec.Header()
	if h.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("nosniff missing: %q", h.Get("X-Content-Type-Options"))
	}
	if h.Get("X-Frame-Options") != "SAMEORIGIN" {
		t.Errorf("X-Frame-Options = %q", h.Get("X-Frame-Options"))
	}
	if h.Get("Referrer-Policy") != "no-referrer" {
		t.Errorf("Referrer-Policy = %q", h.Get("Referrer-Policy"))
	}
	if !strings.Contains(h.Get("Content-Security-Policy"), "frame-ancestors 'self'") {
		t.Errorf("CSP = %q", h.Get("Content-Security-Policy"))
	}
	csp := h.Get("Content-Security-Policy")
	if strings.Contains(csp, "unsafe-inline") {
		t.Errorf("CSP must not allow unsafe-inline: %q", csp)
	}
	if !strings.Contains(csp, "script-src 'self'") || !strings.Contains(csp, "style-src 'self'") {
		t.Errorf("CSP script/style directives = %q", csp)
	}
	// 剪贴板正文不得被浏览器或中间代理缓存。
	if !strings.Contains(h.Get("Cache-Control"), "no-store") {
		t.Errorf("Cache-Control = %q, want no-store", h.Get("Cache-Control"))
	}
	if h.Get("Pragma") != "no-cache" {
		t.Errorf("Pragma = %q, want no-cache", h.Get("Pragma"))
	}
	if h.Get("Expires") != "0" {
		t.Errorf("Expires = %q, want 0", h.Get("Expires"))
	}
	if h.Get("Cross-Origin-Resource-Policy") != "same-origin" {
		t.Errorf("CORP = %q, want same-origin", h.Get("Cross-Origin-Resource-Policy"))
	}
	if h.Get("Vary") != "Origin" {
		t.Errorf("Vary = %q, want Origin", h.Get("Vary"))
	}
}

// 静态资源：/app.js /theme.js /app.css 从注入的 webFS 读取，Content-Type
// 正确且禁用缓存；文件缺失时 404。
func TestStaticAssetRoutes(t *testing.T) {
	s := newTestServer(t)
	s.webFS = fstest.MapFS{
		"web/app.js":   {Data: []byte("console.log('app')")},
		"web/theme.js": {Data: []byte("/* theme */")},
		"web/app.css":  {Data: []byte("body{}")},
	}
	cases := []struct{ path, ctype string }{
		{"/app.js", "application/javascript; charset=utf-8"},
		{"/theme.js", "application/javascript; charset=utf-8"},
		{"/app.css", "text/css; charset=utf-8"},
	}
	for _, c := range cases {
		rec := serve(s, http.MethodGet, c.path, "127.0.0.1:5000", "", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d, want 200", c.path, rec.Code)
		}
		if got := rec.Header().Get("Content-Type"); got != c.ctype {
			t.Fatalf("%s Content-Type = %q, want %q", c.path, got, c.ctype)
		}
		if !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
			t.Fatalf("%s must be no-store: %q", c.path, rec.Header().Get("Cache-Control"))
		}
	}

	// 未注入文件（或文件缺失）→ 404，而不是 500 / 空 200。
	empty := newTestServer(t)
	empty.webFS = fstest.MapFS{}
	for _, c := range cases {
		if rec := serve(empty, http.MethodGet, c.path, "127.0.0.1:5000", "", ""); rec.Code != http.StatusNotFound {
			t.Fatalf("missing %s = %d, want 404", c.path, rec.Code)
		}
	}
}

// /api/qr 只允许生成本服务地址：loopback / 本机网卡地址 / 请求 Host。
func TestHandleQRRestrictedToServiceAddress(t *testing.T) {
	s := newTestServer(t)
	s.localIPs["192.168.1.10"] = true

	q := func(text string) *httptest.ResponseRecorder {
		return serve(s, http.MethodGet, "/api/qr?text="+url.QueryEscape(text), "127.0.0.1:5000", "", "")
	}
	for _, ok := range []string{
		"http://192.168.1.10:6116",
		"http://127.0.0.1:6116",
		"http://localhost:6116",
		"http://[::1]:6116",
	} {
		if rec := q(ok); rec.Code != http.StatusOK {
			t.Fatalf("qr for %q = %d, want 200 (%s)", ok, rec.Code, rec.Body.String())
		}
	}
	for _, bad := range []string{
		"https://evil.example/steal",
		"http://169.254.169.254/latest/meta-data",
		"javascript:alert(1)",
		"not a url",
	} {
		if rec := q(bad); rec.Code != http.StatusBadRequest {
			t.Fatalf("qr for %q = %d, want 400", bad, rec.Code)
		}
	}
	// C0/C1 控制字符被剔除后再编码：\r\n / ESC 不会影响地址判定。
	if rec := q("http://127.0.0.1:6116/a\r\n\x1b"); rec.Code != http.StatusOK {
		t.Fatalf("control chars should be stripped: %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestQRTextTooLong(t *testing.T) {
	s := newTestServer(t)
	long := "/api/qr?text=" + strings.Repeat("a", maxQRTextBytes+1)
	if rec := serve(s, http.MethodGet, long, "127.0.0.1:5000", "", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized QR text = %d, want 400", rec.Code)
	}
	if rec := serve(s, http.MethodGet, "/api/qr", "127.0.0.1:5000", "", ""); rec.Code != http.StatusOK {
		t.Fatalf("default QR = %d, want 200", rec.Code)
	}
}

// ── 磁盘水位 fail-closed / 写接口落盘失败 5xx ────────────────────────────

func TestGuardDiskUsageErrorFailsClosed(t *testing.T) {
	dir := t.TempDir()
	st, err := store.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	s := &Server{
		store:      st,
		suppressor: lancoclip.NewSuppressor(),
		localIPs:   map[string]bool{},
		shareURL:   "http://127.0.0.1:6116",
	}
	// 数据目录消失 → DiskUsage() 返回 err；写接口必须 503 而不是放行。
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	rec := guardedServeBody(s, http.MethodPost, "/api/add-text",
		map[string]string{"Content-Type": "application/json"}, `{"content":"x"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("disk usage error = %d, want 503 (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "暂时无法确认存储空间") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestWriteEndpointsReturn5xxOnSaveFailure(t *testing.T) {
	dir := t.TempDir()
	st, err := store.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	// 把 texts.json.tmp 预置成目录 → Save() 的 stageFile(os.WriteFile) 必失败，
	// 跨平台、与权限 / root 无关。
	if err := os.Mkdir(filepath.Join(dir, "texts.json.tmp"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	s := &Server{
		store:      st,
		suppressor: lancoclip.NewSuppressor(),
		localIPs:   map[string]bool{},
		shareURL:   "http://127.0.0.1:6116",
	}
	cases := []struct{ method, target, body string }{
		{http.MethodPost, "/api/clear", "{}"},
		{http.MethodPost, "/api/add-text", `{"content":"x"}`},
	}
	for _, c := range cases {
		rec := guardedServeBody(s, c.method, c.target,
			map[string]string{"Content-Type": "application/json"}, c.body)
		if rec.Code < 500 || rec.Code > 599 {
			t.Fatalf("%s = %d, want 5xx (%s)", c.target, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), `"ok":true`) {
			t.Fatalf("%s must not report ok on save failure: %s", c.target, rec.Body.String())
		}
	}
}

// ── /api/image：正常服务与软链拒绝 ───────────────────────────────────────

func TestHandleImageServesAndRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	st, err := store.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	s := &Server{
		store:      st,
		suppressor: lancoclip.NewSuppressor(),
		localIPs:   map[string]bool{},
		shareURL:   "http://127.0.0.1:6116",
	}
	pngBytes := []byte("store-accepts-arbitrary-bytes")
	if added, err := st.AddImage(pngBytes); err != nil || !added {
		t.Fatalf("AddImage: added=%v err=%v", added, err)
	}
	recs, _ := st.Page(store.KindImage, 1, 1)
	if len(recs) != 1 {
		t.Fatal("no image record")
	}
	path, ok := st.ImagePath(recs[0].ID)
	if !ok {
		t.Fatal("ImagePath missing")
	}
	target := "/api/image?id=" + strconv.FormatInt(recs[0].ID, 10)

	// 正常图片：200 + image/png + nosniff，且不再经过 ServeFile 的路径协商。
	rec := serve(s, http.MethodGet, target, "127.0.0.1:5000", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("image GET = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != "image/png" ||
		rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers: %v", rec.Header())
	}
	if rec.Body.String() != string(pngBytes) {
		t.Fatalf("body = %q, want %q", rec.Body.String(), pngBytes)
	}

	// 把图片文件换成指向数据目录之外的软链：必须被拒绝（404）。
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("SECRET"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove image: %v", err)
	}
	if err := os.Symlink(secret, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	rec = serve(s, http.MethodGet, target, "127.0.0.1:5000", "", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("symlinked image = %d, want 404", rec.Code)
	}
}

// ── 单实例探测：禁重定向 / URL 白名单 ───────────────────────────────────

func TestRunningInstanceURLRejectsForeignURL(t *testing.T) {
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "app": "lanoclip", "url": "https://evil.example/steal",
		})
	}))
	defer evil.Close()
	if got := runningInstanceURL(portOf(t, evil.URL), 1); got != "" {
		t.Fatalf("foreign URL should be rejected, got %q", got)
	}
}

func TestRunningInstanceURLNoRedirect(t *testing.T) {
	// 本机端口 302 到外部地址：client 不得跟随（否则就是一次 SSRF）。
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data", http.StatusFound)
	}))
	defer redirector.Close()
	if got := runningInstanceURL(portOf(t, redirector.URL), 1); got != "" {
		t.Fatalf("redirect should not be followed, got %q", got)
	}
}

func TestValidInstanceURL(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"http://127.0.0.1:6116", true},
		{"http://localhost:6116", true},
		{"http://[::1]:6116", true},
		{"https://127.0.0.1:6116", true},
		{"https://evil.example/steal", false},
		{"ftp://127.0.0.1:6116", false},
		{"", false},
	}
	for _, c := range cases {
		if _, ok := validInstanceURL(c.raw); ok != c.want {
			t.Errorf("validInstanceURL(%q) ok=%v, want %v", c.raw, ok, c.want)
		}
	}
	// 控制字符不得进入返回值（防日志注入）。
	for _, raw := range []string{
		"http://127.0.0.1:6116\r\nInjected: 1",
		"http://127.0.0.1:6116\x1b[31m",
	} {
		if clean, ok := validInstanceURL(raw); ok && strings.ContainsAny(clean, "\r\n\x1b") {
			t.Fatalf("control chars survived in %q", clean)
		}
	}
}
