//go:build darwin && cgo

package hotkey

// keyTable maps normalized key tokens to Carbon virtual key codes
// (US ANSI layout). Single letters and digits resolve via the table too,
// so everything goes through one lookup path.
var keyTable = map[string]int{
	// letters
	"a": 0x00, "s": 0x01, "d": 0x02, "f": 0x03, "h": 0x04, "g": 0x05,
	"z": 0x06, "x": 0x07, "c": 0x08, "v": 0x09, "b": 0x0b, "q": 0x0c,
	"w": 0x0d, "e": 0x0e, "r": 0x0f, "y": 0x10, "t": 0x11, "o": 0x1f,
	"u": 0x20, "i": 0x22, "p": 0x23, "l": 0x25, "j": 0x26, "k": 0x28,
	"n": 0x2d, "m": 0x2e,
	// digits
	"0": 0x1d, "1": 0x12, "2": 0x13, "3": 0x14, "4": 0x15, "5": 0x17,
	"6": 0x16, "7": 0x1a, "8": 0x1c, "9": 0x19,
	// function keys
	"f1": 122, "f2": 120, "f3": 99, "f4": 118, "f5": 96, "f6": 97,
	"f7": 98, "f8": 100, "f9": 101, "f10": 109, "f11": 103, "f12": 111,
	"f13": 105, "f14": 107, "f15": 113, "f16": 106, "f17": 64,
	"f18": 79, "f19": 80, "f20": 90,
	// punctuation & specials
	"-": 0x1b, "=": 0x18, "[": 0x21, "]": 0x1e, "\\": 0x2a,
	";": 0x29, "'": 0x27, ",": 0x2b, ".": 0x2f, "/": 0x2c, "`": 0x32,
	"space": 0x31, "tab": 0x30, "enter": 0x24,
}

// carbonMods converts a Combo into Carbon modifier masks.
func carbonMods(c Combo) int {
	mods := 0
	if c.Meta {
		mods |= 1 << 8 // cmdKey
	}
	if c.Shift {
		mods |= 1 << 9 // shiftKey
	}
	if c.Alt {
		mods |= 1 << 11 // optionKey
	}
	if c.Ctrl {
		mods |= 1 << 12 // controlKey
	}
	return mods
}
