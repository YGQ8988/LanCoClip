//go:build windows

package hotkey

// keyTable maps normalized key tokens to Windows virtual-key codes.
// Letters and digits also live here so everything goes through one lookup.
var keyTable = map[string]uint32{
	// letters (VK_A..VK_Z)
	"a": 0x41, "b": 0x42, "c": 0x43, "d": 0x44, "e": 0x45, "f": 0x46,
	"g": 0x47, "h": 0x48, "i": 0x49, "j": 0x4a, "k": 0x4b, "l": 0x4c,
	"m": 0x4d, "n": 0x4e, "o": 0x4f, "p": 0x50, "q": 0x51, "r": 0x52,
	"s": 0x53, "t": 0x54, "u": 0x55, "v": 0x56, "w": 0x57, "x": 0x58,
	"y": 0x59, "z": 0x5a,
	// digits (VK_0..VK_9)
	"0": 0x30, "1": 0x31, "2": 0x32, "3": 0x33, "4": 0x34,
	"5": 0x35, "6": 0x36, "7": 0x37, "8": 0x38, "9": 0x39,
	// function keys (VK_F1..VK_F24)
	"f1": 0x70, "f2": 0x71, "f3": 0x72, "f4": 0x73, "f5": 0x74, "f6": 0x75,
	"f7": 0x76, "f8": 0x77, "f9": 0x78, "f10": 0x79, "f11": 0x7a, "f12": 0x7b,
	"f13": 0x7c, "f14": 0x7d, "f15": 0x7e, "f16": 0x7f, "f17": 0x80,
	"f18": 0x81, "f19": 0x82, "f20": 0x83, "f21": 0x84, "f22": 0x85,
	"f23": 0x86, "f24": 0x87,
	// punctuation (VK_OEM_*)
	";": 0xba, "=": 0xbb, ",": 0xbc, "-": 0xbd, ".": 0xbe, "/": 0xbf,
	"`": 0xc0, "[": 0xdb, "\\": 0xdc, "]": 0xdd, "'": 0xde,
}

const (
	modAlt      = 0x0001
	modControlW = 0x0002
	modShiftW   = 0x0004
	modWin      = 0x0008
)

// winMods converts a Combo into RegisterHotKey modifier flags (without
// MOD_NOREPEAT; the caller adds that).
func winMods(c Combo) uintptr {
	m := uintptr(0)
	if c.Meta {
		m |= modWin
	}
	if c.Ctrl {
		m |= modControlW
	}
	if c.Alt {
		m |= modAlt
	}
	if c.Shift {
		m |= modShiftW
	}
	return m
}
