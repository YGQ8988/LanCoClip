//go:build !darwin && !windows

package histpop

// Group is one top-level row plus its submenu items.
type Group struct {
	Title string
	Items []string
}

// Show is unsupported without a native GUI; callers should fall back to
// opening the web page.
func Show(groups []Group, onPick func(group, item int)) bool { return false }
