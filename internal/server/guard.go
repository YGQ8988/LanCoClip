package server

import (
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"lanoclip/internal/store"
)

// ── 请求的三道公共防线 ──────────────────────────────────────────────────
// 服务绑在 0.0.0.0 且按产品决策不做鉴权，因此这些都不是「口令」，而是
// 浏览器侧与资源侧的兜底：
//   0. Host 白名单：挡住 DNS rebinding（否则攻击者域名解析到 127.0.0.1 后，
//      Origin 与 Host 完全一致，只读接口也能被任意网页读走全部历史）；
//   1. 跨站写请求（CSRF）拦截：恶意网页不能悄悄清空历史 / 灌水 / 弹通知；
//   2. 每 IP 限流：写请求次数与累计上传字节都设窗口上限；
//   3. 数据目录水位：达到上限后拒绝新的推送，避免磁盘被打满；
//   4. 跨站读请求拦截：/api/* 的非写请求按同源策略校验，挡住恶意网页读取历史。
// 只读请求不走限流与水位，但仍受 Host 白名单、读同源校验与安全响应头约束。

const (
	writeWindow        = 10 * time.Second
	maxWritesPerIP     = 60
	maxWriteBytesPerIP = 96 << 20 // 一个窗口内累计 96 MiB，够传 3 张 32MB 图
	maxTrackedIPs      = 512
	// 所有写接口的请求体总上限。add-text / add-image 另有更严的 handler
	// 级限制；这里兜住 settings / suppress 这类裸 json.Decoder 的入口。
	maxWriteBodyBytes = 32 << 20
)

type writeBucket struct {
	start  time.Time
	writes int
	bytes  int64
}

type writeLimiter struct {
	mu   sync.Mutex
	byIP map[string]*writeBucket
}

func newWriteLimiter() *writeLimiter {
	return &writeLimiter{byIP: make(map[string]*writeBucket)}
}

func (l *writeLimiter) allow(ip string, nbytes int64) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	b := l.byIP[ip]
	newIP := b == nil
	if newIP || now.Sub(b.start) >= writeWindow {
		// 惰性淘汰：长期运行也不会因为每个来过的 IP 都留一个桶而涨内存。
		if len(l.byIP) >= maxTrackedIPs {
			for k, v := range l.byIP {
				if now.Sub(v.start) >= writeWindow {
					delete(l.byIP, k)
				}
			}
		}
		// 淘汰后仍是「新 IP 且 map 已满」：拒绝而非让 map 无限增长。
		if newIP && len(l.byIP) >= maxTrackedIPs {
			return false
		}
		b = &writeBucket{start: now}
		l.byIP[ip] = b
	}
	// 减法比较而非 b.bytes+nbytes：nbytes 已被 requestBytes 夹到有界范围，
	// 但直接相加仍可能在历史桶上被构造出溢出；这里彻底避免加法。
	if b.writes >= maxWritesPerIP || nbytes > maxWriteBytesPerIP-b.bytes {
		return false
	}
	b.writes++
	if nbytes > 0 {
		b.bytes += nbytes
	}
	// 累加后把字节数钳回合法区间，任何异常值都不会污染后续窗口。
	if b.bytes < 0 {
		b.bytes = 0
	} else if b.bytes > maxWriteBytesPerIP {
		b.bytes = maxWriteBytesPerIP
	}
	return true
}

// guardHost rejects requests whose Host header is not one of the addresses this
// service is actually reachable at. Without it, an attacker-controlled domain
// that rebinds to 127.0.0.1 becomes "same-origin" and can read every record
// through the unauthenticated read endpoints — Origin checks alone cannot stop
// that, because the browser sets both values from the same attacker domain.
//
// LANOCLIP_ALLOWED_HOSTS (comma separated, host or host:port) lets a user who
// reaches the page through a proxy / custom DNS name opt that name in.
func (s *Server) guardHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.hostAllowed(r.Host) {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "不允许的访问域名"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) hostAllowed(raw string) bool {
	h := strings.ToLower(hostOnly(strings.TrimSpace(raw)))
	if h == "" {
		return false
	}
	if h == "localhost" {
		return true
	}
	if ip := net.ParseIP(h); ip != nil && ip.IsLoopback() {
		return true
	}
	if extraHostAllowed(h) {
		return true
	}
	// 本机网卡地址随网络切换变化，复用 refreshNet 的动态结果。
	s.refreshNet()
	s.netMu.Lock()
	defer s.netMu.Unlock()
	return s.localIPs[h]
}

func hostOnly(v string) string {
	if h, _, err := net.SplitHostPort(v); err == nil {
		return h
	}
	return strings.Trim(v, "[]")
}

func extraHostAllowed(h string) bool {
	for _, v := range strings.Split(os.Getenv("LANOCLIP_ALLOWED_HOSTS"), ",") {
		if strings.ToLower(strings.TrimSpace(hostOnly(v))) == h {
			return true
		}
	}
	return false
}

// guardWrites wraps the router. It runs before method checks so a rejected
// request never reaches a handler.
func (s *Server) guardWrites(next http.Handler) http.Handler {
	limiter := s.writeLimiter()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isWriteMethod(r.Method) {
			next.ServeHTTP(w, r)
			return
		}
		if !sameOriginWrite(r) {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "跨站请求已被拒绝"})
			return
		}
		if !limiter.allow(clientIP(r), requestBytes(r)) {
			writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "请求过于频繁，请稍后再试"})
			return
		}
		// 所有写接口都要有请求体上限；handler 的更严限制会再包一层。
		r.Body = http.MaxBytesReader(w, r.Body, maxWriteBodyBytes)
		// 图片上传串行化：让水位检查看到上一次写入后的真实占用，避免并发
		// 请求同时通过检查再各自写 32MB 从而越过 1 GiB 上限。
		if r.URL.Path == "/api/add-image" {
			s.uploadMu.Lock()
			defer s.uploadMu.Unlock()
		}
		// 只有「往本机塞新记录」的入口受水位约束；清空 / 设置 / 抑制不受影响。
		if r.URL.Path == "/api/add-text" || r.URL.Path == "/api/add-image" {
			budget := s.diskBudget.Load()
			if budget <= 0 {
				budget = store.MaxDataDirBytes
			}
			used, err := s.store.DiskUsage()
			if err != nil {
				// 水位无法确认时 fail-closed：不能假装空间充足继续往本机塞数据。
				log.Printf("disk usage: %v", err)
				writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "暂时无法确认存储空间，请稍后重试"})
				return
			}
			if used >= budget {
				writeJSON(w, http.StatusInsufficientStorage, map[string]any{"error": "数据目录已达上限，请先清理历史记录"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func isWriteMethod(m string) bool {
	switch m {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// sameOriginWrite rejects a browser request that originated from another site.
// A cross-site <form>/fetch POST always carries a foreign Origin (and modern
// browsers also send Sec-Fetch-Site), while our own page uses relative URLs;
// non-browser clients (tray, single-instance probe, curl) send neither and
// stay allowed — that is intentional, they are not a CSRF vector.
func sameOriginWrite(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" &&
		site != "same-origin" && site != "none" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false // "null" / file:// / 畸形值
	}
	return strings.EqualFold(u.Host, r.Host)
}

// sameOriginRead rejects cross-site reads of /api/* data. A hostile page that
// fetches http://<LAN IP>:6116/api/records is tagged Sec-Fetch-Site: cross-site;
// direct navigation to that address is "none"/empty and a relative-URL fetch
// from that page is "same-origin"/"same-site" — all of those stay allowed, so
// the phone's own page and its API calls are unaffected. Non-browser clients
// (curl, tray, single-instance probe) send neither Sec-Fetch-Site nor Origin
// and are allowed too. A present Origin must still match the Host.
func sameOriginRead(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "none", "same-origin", "same-site":
		// ok
	case "cross-site":
		return false
	default:
		return false // 未知值一律拒绝（fail-closed）
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false // "null" / file:// / 畸形值
	}
	return strings.EqualFold(u.Host, r.Host)
}

// guardReads applies the same-origin read policy to /api/* requests that are
// not writes. The page at "/" and static assets are not /api/*, so a direct
// navigation and its subresources are untouched; write requests are handled by
// guardWrites and are deliberately excluded here (a no-Origin POST from the
// tray / single-instance probe / curl must keep working).
func (s *Server) guardReads(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") && !isWriteMethod(r.Method) && !sameOriginRead(r) {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "跨站读取已被拒绝"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func requestBytes(r *http.Request) int64 {
	if r.ContentLength < 0 {
		// chunked / 长度未知：按最大值保守预扣，避免字节窗口被完全绕过。
		return maxWriteBodyBytes
	}
	if r.ContentLength > maxWriteBodyBytes {
		// Content-Length 是客户端可控的：必须夹到有界范围，否则一个
		// 9223372036854775807 就能让字节窗口在加法里溢出成负数。
		return maxWriteBodyBytes
	}
	return r.ContentLength
}
