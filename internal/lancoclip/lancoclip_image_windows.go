//go:build windows

package lancoclip

// Windows lancoclip image support (CF_DIB / CF_DIBV5) via Win32.
//
// Reading goes through the lancoclip directly instead of shelling out to
// PowerShell: the helper-based path polls every few hundred milliseconds, and
// spawning a process each tick would be far too expensive. The DIB parsing
// itself lives in dib.go so it can be unit tested on any platform.

import (
	"errors"
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	cfText        = 1
	cfDIB         = 8
	cfUnicodeText = 13
	cfDIBV5       = 17

	gmemMoveable = 0x0002
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procOpenLancoclip    = user32.NewProc("OpenClipboard")
	procCloseLancoclip   = user32.NewProc("CloseClipboard")
	procEmptyLancoclip   = user32.NewProc("EmptyClipboard")
	procGetLancoclipData = user32.NewProc("GetClipboardData")
	procSetLancoclipData = user32.NewProc("SetClipboardData")

	procIsLancoclipFormatAvailable = user32.NewProc("IsClipboardFormatAvailable")
	procGlobalAlloc                = kernel32.NewProc("GlobalAlloc")
	procGlobalFree                 = kernel32.NewProc("GlobalFree")
	procGlobalLock                 = kernel32.NewProc("GlobalLock")
	procGlobalUnlock               = kernel32.NewProc("GlobalUnlock")
	procGlobalSize                 = kernel32.NewProc("GlobalSize")
)

// openLancoclip retries briefly: the lancoclip is exclusive, and another
// process copying something holds it for a few milliseconds at a time.
func openLancoclip() bool {
	for i := 0; i < 3; i++ {
		if r, _, _ := procOpenLancoclip.Call(0); r != 0 {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// ptrFromAddr rebuilds a pointer from an address returned by a Win32 call.
// vet flags the direct uintptr → unsafe.Pointer conversion because it cannot
// prove the address is still valid; here it is — the block is locked with
// GlobalLock and stays valid until GlobalUnlock, which runs before the caller
// is done with the slice.
func ptrFromAddr(u uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&u))
}

// readLancoclipImage returns the lancoclip image as PNG, or nil when the
// lancoclip holds text (or no image). Caller must not hold the lancoclip open.
func readLancoclipImage() []byte {
	if !openLancoclip() {
		return nil
	}
	defer procCloseLancoclip.Call()

	// Same rule as macOS: a non-empty text flavour wins over the bitmap. Excel
	// and friends put a rendered picture on the lancoclip alongside the text
	// they copied, and that must be recorded as text, not as a screenshot.
	if lancoclipHasText() {
		return nil
	}

	format := uintptr(cfDIB)
	if r, _, _ := procIsLancoclipFormatAvailable.Call(cfDIB); r == 0 {
		if r5, _, _ := procIsLancoclipFormatAvailable.Call(cfDIBV5); r5 == 0 {
			return nil
		}
		format = cfDIBV5
	}

	h, _, _ := procGetLancoclipData.Call(format)
	if h == 0 {
		return nil
	}
	ptr, _, _ := procGlobalLock.Call(h)
	if ptr == 0 {
		return nil
	}
	defer procGlobalUnlock.Call(h)

	size, _, _ := procGlobalSize.Call(h)
	if size == 0 {
		return nil
	}

	png, err := dibToPNG(unsafe.Slice((*byte)(ptrFromAddr(ptr)), int(size)))
	if err != nil {
		return nil
	}
	return png
}

// lancoclipHasText reports whether a text flavour with real content is present.
// The lancoclip must already be open.
func lancoclipHasText() bool {
	// A text flavour always ends with a NUL terminator, so anything larger than
	// that terminator carries actual characters.
	if h, _, _ := procGetLancoclipData.Call(cfUnicodeText); h != 0 {
		if size, _, _ := procGlobalSize.Call(h); size > 2 {
			return true
		}
	}
	if h, _, _ := procGetLancoclipData.Call(cfText); h != 0 {
		if size, _, _ := procGlobalSize.Call(h); size > 1 {
			return true
		}
	}
	return false
}

// WriteImage replaces the lancoclip contents with the given PNG.
func WriteImage(pngBytes []byte) error {
	dib, err := pngToDIB(pngBytes)
	if err != nil {
		return err
	}
	if !openLancoclip() {
		return errors.New("无法打开剪贴板（可能被其他程序占用）")
	}
	defer procCloseLancoclip.Call()

	if r, _, e := procEmptyLancoclip.Call(); r == 0 {
		return fmt.Errorf("清空剪贴板失败: %v", e)
	}
	hMem, _, _ := procGlobalAlloc.Call(gmemMoveable, uintptr(len(dib)))
	if hMem == 0 {
		return errors.New("分配剪贴板内存失败")
	}
	ptr, _, _ := procGlobalLock.Call(hMem)
	if ptr == 0 {
		procGlobalFree.Call(hMem)
		return errors.New("锁定剪贴板内存失败")
	}
	copy(unsafe.Slice((*byte)(ptrFromAddr(ptr)), len(dib)), dib)
	procGlobalUnlock.Call(hMem)

	if r, _, e := procSetLancoclipData.Call(cfDIB, hMem); r == 0 {
		procGlobalFree.Call(hMem)
		return fmt.Errorf("写入剪贴板失败: %v", e)
	}
	// 成功后这块内存归系统所有，不能再 GlobalFree。
	return nil
}
