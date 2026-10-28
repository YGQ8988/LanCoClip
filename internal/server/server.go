package server

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
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
	s.port = port
	s.shareURL = fmt.Sprintf("http://%s:%d", lanaddr.BestLocalIP(), port)
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
	if s.port == 0 {
		return
	}
	s.netMu.Lock()
	defer s.netMu.Unlock()
	if !s.netChecked.IsZero() && time.Since(s.netChecked) < netTTL {
		return
	}
	s.netChecked = time.Now()
	s.localIPs = lanaddr.LocalIPSet()
	if ip := lanaddr.BestLocalIP(); ip != "" {
		s.shareURL = fmt.Sprintf("http://%s:%d", ip, s.port)
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
func (s *Server) localURL() string { return fmt.Sprintf("http://127.0.0.1:%d", s.port) }

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

// TrayItemLabel formats one record as a tray menu title:
// "14:05 会议纪要要点…" / "01-02 14:05 [图片]".
func TrayItemLabel(r store.Record) string {
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
