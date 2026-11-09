package server

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"lanoclip/internal/lanaddr"
	"lanoclip/internal/lancoclip"
	"lanoclip/internal/open"
	"lanoclip/internal/store"
	"lanoclip/internal/tray"
)

// Version and BuildTime are injected at link time by build-mac.sh /
// build-windows.sh (-ldflags "-X lanoclip/internal/server.Version=..."). They
// are surfaced on the page so it is obvious which build is actually running.
var Version = "dev"
var BuildTime = "dev"

// Open opens a URL in the default browser. It is a variable (rather than a plain
// call to open.Open) so tests can capture the URL instead of really launching a
// browser.
var Open = open.Open

// Server bundles the shared state used by the HTTP handlers.
//
// localIPs / shareURL are a cache of the last network probe: switching networks
// (a different Wi-Fi, a DHCP renewal) invalidates the old address, so production
// recomputes on a TTL (see refreshNet). port == 0 means a test-built server that
// keeps its preset values untouched.
type Server struct {
	store      *store.Store
	webFS      fs.FS
	suppressor *lancoclip.Suppressor
	port       int
	localIPs   map[string]bool
	shareURL   string

	netMu      sync.Mutex
	netChecked time.Time // when localIPs/shareURL were last recomputed

	// settingsMu 串行化 /api/settings 的 POST 事务：并发提交时若两条请求
	// 都读到同一份旧值，后失败的一方回滚会把先成功一方已生效的设置抹掉。
	settingsMu sync.Mutex

	limiterOnce sync.Once
	limiter     *writeLimiter // 写请求限流，惰性初始化以兼容测试里直构的 Server
	diskBudget  atomic.Int64  // 仅测试注入；<=0 时用 store.MaxDataDirBytes
	uploadMu    sync.Mutex    // 串行化图片上传，保证磁盘水位检查看到真实占用
}

// writeLimiter returns the shared limiter, creating it on first route build.
func (s *Server) writeLimiter() *writeLimiter {
	s.limiterOnce.Do(func() { s.limiter = newWriteLimiter() })
	return s.limiter
}

// New builds a Server. webFS is the embedded web directory (embed cannot reach
// outside the package, so the caller embeds and injects it).
func New(st *store.Store, webFS fs.FS) *Server {
	return &Server{
		store:      st,
		webFS:      webFS,
		suppressor: lancoclip.NewSuppressor(),
		localIPs:   lanaddr.LocalIPSet(),
	}
}

// Suppressor is the instance's suppression set; the lancoclip watcher and the
// /api/suppress handler must share it so a page copy retracts the same entry the
// watcher would otherwise record.
func (s *Server) Suppressor() *lancoclip.Suppressor { return s.suppressor }

// SetBoundPort records the port actually bound (after port fallback) and
// rebuilds the share URL from the current best LAN address.
func (s *Server) SetBoundPort(port int) {
	s.netMu.Lock()
	defer s.netMu.Unlock()
	s.port = port
	// BestLocalHost 会为 IPv6-only 的机器返回带方括号的地址，直接拼进 URL 即可。
	s.shareURL = fmt.Sprintf("http://%s:%d", lanaddr.BestLocalHost(), port)
	s.netChecked = time.Now() // this probe counts as one refresh
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
func (s *Server) refreshNet() {
	s.netMu.Lock()
	defer s.netMu.Unlock()
	// port 也在锁内读：SetBoundPort 会同时改 port 与 shareURL，锁外读会与
	// 绑定端口的写入产生数据竞争。
	if s.port == 0 {
		return
	}
	if !s.netChecked.IsZero() && time.Since(s.netChecked) < netTTL {
		return
	}
	s.netChecked = time.Now()
	s.localIPs = lanaddr.LocalIPSet()
	if host := lanaddr.BestLocalHost(); host != "" {
		s.shareURL = fmt.Sprintf("http://%s:%d", host, s.port)
	}
}

// currentShareURL returns the up-to-date access URL (refreshNet-aware).
func (s *Server) currentShareURL() string {
	s.refreshNet()
	s.netMu.Lock()
	defer s.netMu.Unlock()
	return s.shareURL
}

// URL implements tray.App: the LAN address a phone has to use.
func (s *Server) URL() string { return s.currentShareURL() }

// LocalURL implements tray.App: the loopback address the tray menu opens.
// Only the browser's own address changes — the QR rendered inside the page
// still carries the LAN share URL — so the page becomes a secure context and
// navigator.clipboard (needed to copy images) stops being undefined.
func (s *Server) LocalURL() string { return s.localURL() }

// localURL is the page address on this machine itself. Unlike URL() it does not
// depend on the LAN address, so it keeps working with no network at all — which
// is exactly the case for "you already have this running, here is its page".
// port is read under netMu because SetBoundPort writes it there.
func (s *Server) localURL() string {
	s.netMu.Lock()
	defer s.netMu.Unlock()
	return fmt.Sprintf("http://127.0.0.1:%d", s.port)
}

// Recent implements tray.App: the newest records across both kinds, formatted
// for the tray menu.
func (s *Server) Recent(n int) []tray.Item {
	recs := s.store.RecentMixed(n)
	items := make([]tray.Item, 0, len(recs))
	for _, r := range recs {
		items = append(items, tray.Item{
			Kind:  r.Kind,
			ID:    r.ID,
			Label: TrayItemLabel(r),
			Text:  r.Content,
		})
	}
	return items
}

// CopyItem implements tray.App: put a history entry back onto the lancoclip.
// The lancoclipWatcher will pick it up and float it back to the top of the
// history, which is the expected behaviour of a lancoclip manager.
func (s *Server) CopyItem(it tray.Item) error {
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
func (s *Server) ClearAll() error {
	s.store.Clear("")
	if err := s.store.Save(); err != nil {
		return err
	}
	log.Println("cleared all history from the tray")
	return nil
}

// Paused / SetPaused implement tray.App: the tray menu exposes a toggle so
// sensitive copies (passwords, OTPs) can be excluded without quitting the app.
func (s *Server) Paused() bool            { return s.store.Paused() }
func (s *Server) SetPaused(on bool) error { return s.store.SetPaused(on) }

// TrayItemLabel formats one record as a tray menu title:
// "14:05 会议纪要要点…" / "01-02 14:05 [图片]".
func TrayItemLabel(r store.Record) string {
	t := time.UnixMilli(r.CreatedAt)
	now := time.Now()
	stamp := t.Format("15:04")
	// 年/月/日都要比：只比 Day+Month 会把「去年同月同日」误判成今天。
	if t.Year() != now.Year() || t.Month() != now.Month() || t.Day() != now.Day() {
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

// ── single-instance support ─────────────────────────────────────────────────

// PortScanRange is how many consecutive ports a launch considers: both when
// binding (ListenFirstFree) and when probing for an instance already running
// (RunningInstanceURL). The two must agree, or a second launch could miss the
// running instance and start a duplicate watcher on the next free port.
const PortScanRange = 10

// RunningInstanceURL probes the ports this launch would bind, looking for an
// instance that is already serving. The bool reports whether one was found; the
// string is that instance's local page URL.
func RunningInstanceURL(start, tries int) (string, bool) {
	if u := runningInstanceURL(start, tries); u != "" {
		return u, true
	}
	return "", false
}

// runningInstanceURL returns the page URL of an already-running instance, or ""
// when nothing answers.
//
// The probe is a POST to /api/activate and the answer must identify itself as
// lanoclip: a foreign service sitting on one of the ports (a dev server, say)
// must not be mistaken for our own instance, or every launch would refuse to
// start. Each attempt is short-lived — a closed port fails instantly, and the
// only real cost is the HTTP timeout on a port that is open but slow.
func runningInstanceURL(start, tries int) string {
	// 禁跟随重定向：本机端口上若是个会 302 到外部地址的服务，跟随它就等于
	// 用一次「启动探测」打出一发 SSRF。ErrUseLastResponse 让 client.Do 直接
	// 交回 3xx 本身，probeInstance 再按内容判定。
	client := &http.Client{
		Timeout: 300 * time.Millisecond,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	type hit struct {
		port int
		url  string
	}
	// 端口之间互不依赖，并发探测把最坏启动延迟从 10×300ms 降到一次超时。
	hits := make(chan hit, tries)
	var wg sync.WaitGroup
	for i := 0; i < tries; i++ {
		wg.Add(1)
		go func(port int) {
			defer wg.Done()
			if u := probeInstance(client, port); u != "" {
				hits <- hit{port: port, url: u}
			}
		}(start + i)
	}
	wg.Wait()
	close(hits)

	// 多个命中时取端口最小的那个，即最早启动、最可能是主实例的一个。
	best := hit{port: int(^uint(0) >> 1)}
	for h := range hits {
		if h.port < best.port {
			best = h
		}
	}
	return best.url
}

// probeInstance asks one port to identify itself. A foreign service must not
// be mistaken for LanCoClip, otherwise every launch would refuse to start.
func probeInstance(client *http.Client, port int) string {
	req, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d/api/activate", port), strings.NewReader("{}"))
	if err != nil {
		return ""
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var body struct {
		App string `json:"app"`
		URL string `json:"url"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<12)).Decode(&body)
	if body.App != "lanoclip" {
		return ""
	}
	if body.URL != "" {
		// 探测响应里的 URL 必须指向本机：否则把被投毒的地址带回启动流程 /
		// 日志就是 SSRF / 日志注入。非法值直接当成「没探到实例」。
		if clean, ok := validInstanceURL(body.URL); ok {
			return clean
		}
		return ""
	}
	return fmt.Sprintf("http://127.0.0.1:%d", port)
}

// validInstanceURL 校验单实例探测响应里的 URL：scheme 只接受 http/https，
// Host 必须是 loopback（127.0.0.1 / localhost / ::1）或本机网卡地址。控制字符
// 被剔除，避免注入日志或终端转义序列。通过返回清理后的 URL，失败返回 ("", false)。
func validInstanceURL(raw string) (string, bool) {
	clean := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, raw)
	u, err := url.Parse(clean)
	if err != nil {
		return "", false
	}
	if !strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https") {
		return "", false
	}
	host := u.Hostname()
	if host == "" {
		return "", false
	}
	if isLocalInstanceHost(host) {
		return clean, true
	}
	return "", false
}

func isLocalInstanceHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return true
	}
	return lanaddr.LocalIPSet()[host]
}

// ListenFirstFree binds the first free TCP port in [start, start+tries).
func ListenFirstFree(start, tries int) (net.Listener, error) {
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
