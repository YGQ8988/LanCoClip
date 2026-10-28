package tray

import "log"

// Item is one history entry shown in the tray menu.
type Item struct {
	Kind  string // "text" | "image" (store kinds)
	ID    int64  // store record id
	Label string // menu title, e.g. "12:30 会议纪要要点…"
	Text  string // full text payload when Kind == "text"
}

// App is what the tray menu needs from the host application.
type App interface {
	// URL is the LAN address of the web page (what a phone has to use).
	URL() string
	// LocalURL is the same page over loopback (http://127.0.0.1:port). Menu
	// actions open this one: a loopback page is a secure context, so the async
	// Clipboard API — which copying an image requires — is actually available.
	LocalURL() string
	// Recent returns up to n newest history items (text + image merged).
	Recent(n int) []Item
	// CopyItem puts a history item back onto the lancoclip.
	CopyItem(it Item) error
	// ClearAll drops every text and image record.
	ClearAll() error
}

// logf is a tiny indirection so types.go stays import-light.
func logf(format string, args ...any) { log.Printf(format, args...) }

// Tray menu layout constants.
const (
	// HistoryCount is how many entries the tray "历史" submenu shows.
	HistoryCount = 30
	// HistoryGroupSize is how many entries share one sub-group.
	HistoryGroupSize = 10
)

// Options carries host-provided tray settings. Hotkey combos use the
// canonical hotkey.Combo string form ("meta+shift+q"); empty values fall
// back to the platform defaults inside the hotkey package.
type Options struct {
	HotkeyQR      string // 二维码快捷键，如 "meta+shift+q"
	HotkeyHistory string // 历史记录快捷键，如 "meta+shift+v"
}
