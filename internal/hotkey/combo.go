// Combo parsing shared by the macOS (Carbon) and Windows (RegisterHotKey)
// hotkey implementations, and by the settings API on every platform (main.go
// validates user combos before persisting them). A combo is stored as a
// canonical string like "meta+shift+q" / "ctrl+alt+f2", with modifiers in a
// fixed order and a lowercase key token. "meta" means Command on macOS and the
// Windows key on Windows; "ctrl" is Ctrl on both.

package hotkey

import (
	"fmt"
	"strings"
)

// Combo is a parsed global-hotkey combination.
type Combo struct {
	Meta, Ctrl, Alt, Shift bool
	Key                    string // normalized key token, e.g. "q", "f2", ","
}

// Parse turns a canonical combo string into a Combo. It enforces that a main
// key exists and that at least one non-Shift modifier is present (a Shift-only
// shortcut would fire while the user is merely typing).
func Parse(s string) (Combo, error) {
	var c Combo
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return c, fmt.Errorf("快捷键不能为空")
	}
	if len(s) > 64 {
		return c, fmt.Errorf("快捷键格式无效")
	}
	parts := strings.Split(s, "+")
	var key string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			return c, fmt.Errorf("快捷键格式无效")
		}
		switch p {
		case "meta", "cmd", "command", "win":
			c.Meta = true
			continue
		case "ctrl", "control":
			c.Ctrl = true
			continue
		case "alt", "option", "opt":
			c.Alt = true
			continue
		case "shift":
			c.Shift = true
			continue
		}
		if key != "" {
			return c, fmt.Errorf("快捷键只能包含一个主键")
		}
		key = p
	}
	if key == "" {
		return c, fmt.Errorf("快捷键缺少主键（如 Q、1、F2）")
	}
	if !validKey(key) {
		return c, fmt.Errorf("不支持按键 “%s”", key)
	}
	if !c.Meta && !c.Ctrl && !c.Alt {
		return c, fmt.Errorf("至少需要 Cmd/Ctrl/Alt 中一个修饰键")
	}
	c.Key = key
	return c, nil
}

// Same reports whether two combos press the same physical keys.
func Same(a, b Combo) bool { return a == b }

// validKey reports whether key is an accepted main-key token.
func validKey(key string) bool {
	if _, ok := keyTable[key]; ok {
		return true
	}
	// single ASCII letter / digit
	if len(key) == 1 {
		ch := key[0]
		return (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9')
	}
	return false
}
