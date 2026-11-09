package server

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// guardedServe builds a request with explicit headers (the shared serve()
// helper cannot set Origin / Sec-Fetch-Site).
func guardedServe(s *Server, method, target string, headers map[string]string) *httptest.ResponseRecorder {
	return guardedServeBody(s, method, target, headers, "{}")
}

func guardedServeBody(s *Server, method, target string, headers map[string]string, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Host = "127.0.0.1:6116"
	req.RemoteAddr = "127.0.0.1:5000"
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh) LanCoClipTest")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)
	return rec
}

func TestSameOriginWrite(t *testing.T) {
	cases := []struct {
		name   string
		origin string
		site   string
		want   bool
	}{
		{"非浏览器客户端", "", "", true},
		{"本站页面", "http://127.0.0.1:6116", "", true},
		{"本站页面带 Sec-Fetch-Site", "http://127.0.0.1:6116", "same-origin", true},
		{"直接导航", "", "none", true},
		{"跨站 Origin", "https://evil.example", "", false},
		{"跨站 Sec-Fetch-Site", "http://127.0.0.1:6116", "cross-site", false},
		{"同站但跨源提示", "http://127.0.0.1:6116", "same-site", false},
		{"null Origin", "null", "", false},
		{"file 源", "file://", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/clear", nil)
			r.Host = "127.0.0.1:6116"
			if c.origin != "" {
				r.Header.Set("Origin", c.origin)
			}
			if c.site != "" {
				r.Header.Set("Sec-Fetch-Site", c.site)
			}
			if got := sameOriginWrite(r); got != c.want {
				t.Fatalf("sameOriginWrite = %v, want %v", got, c.want)
			}
		})
	}
}

func TestGuardRejectsCrossSiteWrite(t *testing.T) {
	s := newTestServer(t)
	// 跨站 POST：403，且 handler 没有执行（记录仍在）。
	if rec := guardedServe(s, http.MethodPost, "/api/clear", map[string]string{
		"Origin": "https://evil.example",
	}); rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site clear = %d, want 403", rec.Code)
	}
	// 新增的读同源校验：带跨站 Origin 的 GET 也拒绝（读历史同样是敏感操作）。
	if rec := guardedServe(s, http.MethodGet, "/api/records", map[string]string{
		"Origin": "https://evil.example",
	}); rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site GET = %d, want 403", rec.Code)
	}
	// 同源 / 非浏览器 GET 读接口不受影响。
	if rec := guardedServe(s, http.MethodGet, "/api/records", nil); rec.Code != http.StatusOK {
		t.Fatalf("same-origin GET = %d, want 200", rec.Code)
	}
	// 非浏览器客户端（托盘 / 单实例探测 / curl）仍可用 —— 读校验绝不能套到写请求上。
	if rec := guardedServe(s, http.MethodPost, "/api/clear", nil); rec.Code != http.StatusOK {
		t.Fatalf("no-Origin clear = %d, want 200", rec.Code)
	}
}

func TestWriteLimiter(t *testing.T) {
	l := newWriteLimiter()
	for i := 0; i < maxWritesPerIP; i++ {
		if !l.allow("192.168.1.2", 0) {
			t.Fatalf("request %d rejected too early", i+1)
		}
	}
	if l.allow("192.168.1.2", 0) {
		t.Fatal("window should be exhausted")
	}
	// 另一个 IP 有独立配额。
	if !l.allow("192.168.1.3", 0) {
		t.Fatal("another IP should have its own bucket")
	}
	// 单次超大上传直接超字节窗口。
	if l.allow("192.168.1.4", maxWriteBytesPerIP+1) {
		t.Fatal("oversized upload should be rejected")
	}
}

func TestGuardRateLimit(t *testing.T) {
	s := newTestServer(t)
	for i := 0; i < maxWritesPerIP; i++ {
		if rec := guardedServe(s, http.MethodPost, "/api/clear", nil); rec.Code != http.StatusOK {
			t.Fatalf("request %d = %d, want 200", i+1, rec.Code)
		}
	}
	if rec := guardedServe(s, http.MethodPost, "/api/clear", nil); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("over-limit write = %d, want 429", rec.Code)
	}
	// 限流只作用于写请求，页面轮询照常。
	if rec := guardedServe(s, http.MethodGet, "/api/records", nil); rec.Code != http.StatusOK {
		t.Fatalf("GET after rate limit = %d, want 200", rec.Code)
	}
}

func TestGuardDiskWaterMark(t *testing.T) {
	s := newTestServer(t)
	addHeaders := map[string]string{"Content-Type": "application/json"}
	addBody := `{"content":"hello"}`
	// 落一份 texts.json，保证目录非空（水位按目录内所有文件大小累加）。
	s.store.AddText("seed")
	if err := s.store.Save(); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	s.diskBudget.Store(1) // 此时必然超
	rec := guardedServeBody(s, http.MethodPost, "/api/add-text", addHeaders, addBody)
	if rec.Code != http.StatusInsufficientStorage {
		t.Fatalf("over-budget add = %d, want 507", rec.Code)
	}
	// 清空 / 设置不受水位约束。
	if rec := guardedServe(s, http.MethodPost, "/api/clear", nil); rec.Code != http.StatusOK {
		t.Fatalf("clear over budget = %d, want 200", rec.Code)
	}
	s.diskBudget.Store(0) // 恢复默认 1 GiB
	if rec := guardedServeBody(s, http.MethodPost, "/api/add-text", addHeaders, addBody); rec.Code != http.StatusOK {
		t.Fatalf("add under budget = %d, want 200", rec.Code)
	}
}

// ── Host 白名单（DNS rebinding） ──────────────────────────────────────────

func hostProbe(s *Server, host string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/records", nil)
	req.Host = host
	req.RemoteAddr = "127.0.0.1:5000"
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)
	return rec
}

func TestHostAllowList(t *testing.T) {
	s := newTestServer(t)
	s.localIPs["192.168.1.10"] = true // port==0 时 refreshNet 不重算，模拟本机网卡

	cases := []struct {
		host string
		want int
	}{
		{"127.0.0.1:6116", http.StatusOK},
		{"localhost:6116", http.StatusOK},
		{"[::1]:6116", http.StatusOK},
		{"192.168.1.10:6116", http.StatusOK},
		{"evil.example:6116", http.StatusForbidden},
		{"192.168.1.99:6116", http.StatusForbidden},
		{"", http.StatusForbidden},
	}
	for _, c := range cases {
		if got := hostProbe(s, c.host).Code; got != c.want {
			t.Errorf("Host %q = %d, want %d", c.host, got, c.want)
		}
	}
}

func TestHostAllowListEnvOverride(t *testing.T) {
	t.Setenv("LANOCLIP_ALLOWED_HOSTS", "clip.local, proxy.internal:6116")
	s := newTestServer(t)
	if got := hostProbe(s, "clip.local:6116").Code; got != http.StatusOK {
		t.Fatalf("env host clip.local = %d, want 200", got)
	}
	if got := hostProbe(s, "proxy.internal:6116").Code; got != http.StatusOK {
		t.Fatalf("env host proxy.internal = %d, want 200", got)
	}
	if got := hostProbe(s, "other.example:6116").Code; got != http.StatusForbidden {
		t.Fatalf("unlisted host = %d, want 403", got)
	}
}

// ── 请求体上限与 chunked 预扣 ────────────────────────────────────────────

type endlessA struct{ left int64 }

func (r *endlessA) Read(p []byte) (int, error) {
	if r.left <= 0 {
		return 0, io.EOF
	}
	n := len(p)
	if int64(n) > r.left {
		n = int(r.left)
	}
	for i := 0; i < n; i++ {
		p[i] = 'a'
	}
	r.left -= int64(n)
	return n, nil
}

func TestWriteBodyLimit(t *testing.T) {
	s := newTestServer(t)
	// 合法 JSON 前缀 + 40MB 填充，逼 Decoder 一直读到 MaxBytesReader 上限。
	body := io.MultiReader(strings.NewReader(`{"hotkeyQR":"`),
		&endlessA{left: maxWriteBodyBytes + 8<<20}, strings.NewReader(`"}`))
	req := httptest.NewRequest(http.MethodPost, "/api/settings", body)
	req.Host = "127.0.0.1:6116"
	req.RemoteAddr = "127.0.0.1:5000"
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh) LanCoClipTest")
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized settings body = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

func TestChunkedRequestChargesByteWindow(t *testing.T) {
	s := newTestServer(t)
	post := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/clear", strings.NewReader(""))
		req.Host = "127.0.0.1:6116"
		req.RemoteAddr = "127.0.0.1:5000"
		req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh) LanCoClipTest")
		req.ContentLength = -1 // chunked：无 Content-Length
		rec := httptest.NewRecorder()
		s.Routes().ServeHTTP(rec, req)
		return rec
	}
	// 每次预扣 32 MiB，窗口 96 MiB：第 4 次必须被限流拦下。
	for i := 1; i <= 3; i++ {
		if rec := post(); rec.Code != http.StatusOK {
			t.Fatalf("chunked request %d = %d, want 200", i, rec.Code)
		}
	}
	if rec := post(); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("4th chunked request = %d, want 429", rec.Code)
	}
}

// ── 限流整数溢出与 map 上限 ──────────────────────────────────────────────

func TestRequestBytesClampsHugeContentLength(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/add-text", nil)
	r.ContentLength = math.MaxInt64
	if got := requestBytes(r); got != maxWriteBodyBytes {
		t.Fatalf("requestBytes(MaxInt64) = %d, want clamp to %d", got, maxWriteBodyBytes)
	}
	r.ContentLength = -1 // chunked：无 Content-Length，按最大值预扣
	if got := requestBytes(r); got != maxWriteBodyBytes {
		t.Fatalf("requestBytes(chunked) = %d, want %d", got, maxWriteBodyBytes)
	}
	r.ContentLength = 123
	if got := requestBytes(r); got != 123 {
		t.Fatalf("requestBytes(normal) = %d, want 123", got)
	}
}

func TestWriteLimiterNoByteOverflow(t *testing.T) {
	l := newWriteLimiter()
	if !l.allow("ip", 1) {
		t.Fatal("first small write should be allowed")
	}
	// 旧漏洞：b.bytes+nbytes 溢出成负数，96MiB 字节窗口被永久抹掉。
	if l.allow("ip", math.MaxInt64) {
		t.Fatal("oversized charge should be rejected")
	}
	// 关键：注入失败后字节窗口仍完好，后续请求依旧受上限约束。
	if l.allow("ip", maxWriteBytesPerIP) {
		t.Fatal("byte window must remain intact after an overflow attempt")
	}
}

func TestWriteLimiterCapsTrackedIPs(t *testing.T) {
	l := newWriteLimiter()
	for i := 0; i < maxTrackedIPs; i++ {
		if !l.allow(fmt.Sprintf("ip-%d", i), 0) {
			t.Fatalf("IP %d should fit", i)
		}
	}
	// 所有桶都还新鲜：新 IP 必须被拒绝，而不是让 map 无限增长。
	if l.allow("one-too-many", 0) {
		t.Fatal("new IP must be rejected when tracked IPs are all fresh")
	}
	// 桶过期后应被惰性淘汰，腾出位置给新 IP。
	now := time.Now()
	for _, b := range l.byIP {
		b.start = now.Add(-writeWindow)
	}
	if !l.allow("after-eviction", 0) {
		t.Fatal("expired buckets should be evicted to make room")
	}
}

// ── 读接口同源校验 ──────────────────────────────────────────────────────

func TestSameOriginRead(t *testing.T) {
	cases := []struct {
		name   string
		origin string
		site   string
		host   string
		want   bool
	}{
		{"curl/托盘/单实例探测", "", "", "127.0.0.1:6116", true},
		{"直接导航 none", "", "none", "127.0.0.1:6116", true},
		{"同源相对 URL", "", "same-origin", "127.0.0.1:6116", true},
		{"同站", "", "same-site", "127.0.0.1:6116", true},
		{"同源且带同源 Origin", "http://127.0.0.1:6116", "same-origin", "127.0.0.1:6116", true},
		{"LAN IP 直连同源 Origin", "http://192.168.1.10:6116", "same-origin", "192.168.1.10:6116", true},
		{"跨站 Sec-Fetch-Site", "", "cross-site", "127.0.0.1:6116", false},
		{"跨站 Origin", "https://evil.example", "", "127.0.0.1:6116", false},
		{"Origin 与 Host 不符", "http://127.0.0.1:6116", "", "192.168.1.10:6116", false},
		{"null Origin", "null", "", "127.0.0.1:6116", false},
		{"file 源", "file://", "", "127.0.0.1:6116", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/records", nil)
			r.Host = c.host
			if c.origin != "" {
				r.Header.Set("Origin", c.origin)
			}
			if c.site != "" {
				r.Header.Set("Sec-Fetch-Site", c.site)
			}
			if got := sameOriginRead(r); got != c.want {
				t.Fatalf("sameOriginRead = %v, want %v", got, c.want)
			}
		})
	}
}

func TestGuardRejectsCrossSiteRead(t *testing.T) {
	s := newTestServer(t)
	// 跨站 fetch（Sec-Fetch-Site: cross-site）读历史 → 403。
	if rec := guardedServe(s, http.MethodGet, "/api/records", map[string]string{
		"Sec-Fetch-Site": "cross-site",
	}); rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site GET = %d, want 403", rec.Code)
	}
	// 无 Sec-Fetch-Site / 无 Origin 的 curl、托盘、单实例探测 → 200。
	if rec := guardedServe(s, http.MethodGet, "/api/records", nil); rec.Code != http.StatusOK {
		t.Fatalf("curl GET = %d, want 200", rec.Code)
	}
	// 页面 GET "/" 的直接导航是 none，且不属于 /api/*，不经过读校验。
	// 这里直接测 guardReads 的路由范围，不依赖 webFS。
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "127.0.0.1:6116"
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec2 := httptest.NewRecorder()
	s.guardReads(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(rec2, req)
	if rec2.Code != http.StatusNoContent {
		t.Fatalf("non-API path should bypass read guard, got %d", rec2.Code)
	}
}

func TestGuardReadsLeavesWriteMethodAlone(t *testing.T) {
	s := newTestServer(t)
	// 写请求即使带 cross-site 提示，也应由 guardWrites 处理，guardReads 不拦。
	called := false
	h := s.guardReads(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/clear", nil)
	req.Host = "127.0.0.1:6116"
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || !called {
		t.Fatalf("write method should pass guardReads untouched: code=%d called=%v", rec.Code, called)
	}
}
