package lancoclip

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"lanoclip/internal/store"
	"lanoclip/internal/winhide"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/unicode"
)

// readLancoclipText returns the current system lancoclip text as UTF-8.
func readLancoclipText() (string, error) {
	// Native read first. It costs microseconds, while the helper processes
	// (pbpaste, xclip, PowerShell) cost tens to hundreds of milliseconds — and
	// the lancoclipWatcher probes once per lancoclip change, so that gap decides how
	// many copies can be captured in a row.
	if s, ok := nativeReadText(); ok {
		return normalizeText(s), nil
	}

	var raw string
	var err error
	switch runtime.GOOS {
	case "darwin":
		raw, err = runCmd("pbpaste")
	case "linux":
		// Requires xclip installed; fall back to xsel when available.
		raw, err = runCmd("xclip", "-selection", "clipboard", "-o")
		if err != nil {
			raw, err = runCmd("xsel", "--clipboard", "--output")
		}
	case "windows":
		raw, err = runCmd("powershell", "-NoProfile", "-Command", "Get-Clipboard")
	default:
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return normalizeText(raw), nil
}

func runCmd(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	winhide.Hide(cmd)
	cmd.Env = utf8Env()
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// runStdinCmd runs a lancoclip helper that consumes its stdin (pbcopy,
// xclip/xsel, clip). Every platform branch below shares the same shape, so the
// exec setup lives here.
func runStdinCmd(name string, args []string, stdin []byte) error {
	cmd := exec.Command(name, args...)
	winhide.Hide(cmd)
	cmd.Env = utf8Env()
	cmd.Stdin = bytes.NewReader(stdin)
	return cmd.Run()
}

// WriteText puts text onto the system lancoclip. Used by the tray
// menu to copy a history entry back.
func WriteText(s string) error {
	switch runtime.GOOS {
	case "darwin":
		return runStdinCmd("pbcopy", nil, []byte(s))
	case "linux":
		if err := runStdinCmd("xclip", []string{"-selection", "clipboard"}, []byte(s)); err == nil {
			return nil
		}
		return runStdinCmd("xsel", []string{"--clipboard", "--input"}, []byte(s))
	case "windows":
		// clip.exe 按系统 ANSI 代码页（中文系统是 GBK/CP936）解读管道字节，
		// 直接喂 UTF-8 中文会乱码；UTF-16LE + BOM 前缀会被识别为 Unicode。
		u16, err := unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewEncoder().Bytes([]byte(s))
		if err != nil {
			return err
		}
		return runStdinCmd("cmd", []string{"/c", "clip"}, u16)
	default:
		return errors.New("write to the system lancoclip is unsupported on this platform")
	}
}

// utf8Env forces a UTF-8 locale for lancoclip helpers.
//
// A .app launched from Finder inherits no locale, and macOS pbpaste then emits
// text in the system legacy encoding (e.g. GB18030) instead of UTF-8, so
// non-ASCII text reaches the page mangled. LC_CTYPE alone is enough — and it
// must be alone: when LANG and LC_CTYPE are both present, pbpaste falls back to
// the legacy encoding again.
func utf8Env() []string {
	out := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "LANG=") ||
			strings.HasPrefix(kv, "LC_ALL=") ||
			strings.HasPrefix(kv, "LC_CTYPE=") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "LC_CTYPE=UTF-8")
}

// normalizeText repairs lancoclip bytes that came back in the system legacy
// encoding rather than UTF-8. GB18030 is a superset of GBK/GB2312, so it is the
// right guess on a zh_CN system; if the bytes are already valid UTF-8 (or
// cannot be repaired) the input is returned unchanged.
func normalizeText(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	if fixed, err := simplifiedchinese.GB18030.NewDecoder().String(s); err == nil && utf8.ValidString(fixed) {
		return fixed
	}
	return s
}

// changePollInterval is the cadence at which the platform asks "did the
// lancoclip change?". Every write bumps a counter (macOS) or increments a
// sequence number (Windows) that can be read without copying the data, so this
// check is almost free — 40ms is short enough that consecutive copies are
// recorded separately instead of collapsing into the last one.
const changePollInterval = 40 * time.Millisecond

// signalChange delivers a change notification without blocking. The channel is
// a "something changed" flag, not a queue: the probe reads the current
// lancoclip state anyway, so a second pending signal carries no extra
// information.
func signalChange(ch chan<- struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// lancoclipWatcher turns lancoclip changes into history records, keeping the
// last seen text/image so one change is recorded exactly once. The readers are
// fields (not direct calls) so tests can drive the lancoclipWatcher without touching the
// real lancoclip.
type lancoclipWatcher struct {
	readText  func() (string, error)
	readImage func() []byte
	sup       *Suppressor
	onText    func(string)
	onImage   func([]byte)
	lastText  string
	lastSum   string
}

func newLancoclipWatcher(sup *Suppressor, onText func(string), onImage func([]byte)) *lancoclipWatcher {
	return &lancoclipWatcher{
		readText:  readLancoclipText,
		readImage: readLancoclipImage,
		sup:       sup,
		onText:    onText,
		onImage:   onImage,
	}
}

// seed remembers the current lancoclip so that what is already there at startup
// is not recorded as a fresh copy.
func (w *lancoclipWatcher) seed() {
	if s, err := w.readText(); err == nil {
		w.lastText = s
	}
	w.lastSum = imageSum(w.readImage())
}

// probe reads the lancoclip once and reports content it has not seen before.
func (w *lancoclipWatcher) probe() {
	if img := w.readImage(); len(img) > 0 {
		sum := imageSum(img)
		if sum == w.lastSum {
			return
		}
		w.lastSum = sum
		w.lastText = "" // lancoclip switched to an image; allow the same text next time
		if w.sup != nil && w.sup.TakeImageSum(sum) {
			return // 从页面复制的图片：不再入库
		}
		if w.onImage != nil {
			w.onImage(img)
		}
		return
	}
	w.lastSum = ""

	content, err := w.readText()
	if err != nil || content == w.lastText || strings.TrimSpace(content) == "" {
		return
	}
	w.lastText = content
	if w.sup != nil && w.sup.TakeText(content) {
		return // 从页面复制的文本：不再入库
	}
	if w.onText != nil {
		w.onText(content)
	}
}

// Watch records lancoclip changes until ctx is cancelled. Payloads
// registered in sup (content copied from the service page itself) are skipped
// so they are not recorded twice.
//
// Change detection is platform-specific (see lancoclip_native_*.go): macOS
// polls the lancoclip change counter, Windows listens for WM_CLIPBOARDUPDATE,
// other platforms fall back to fixed-interval polling.
func Watch(ctx context.Context, sup *Suppressor,
	onText func(string), onImage func([]byte)) {
	w := newLancoclipWatcher(sup, onText, onImage)
	w.seed()

	changes := lancoclipChanges(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-changes:
			w.probe()
		}
	}
}

// imageSum returns a stable fingerprint of the PNG bytes ("" when empty).
func imageSum(png []byte) string {
	if len(png) == 0 {
		return ""
	}
	return store.SumHex(png)
}
