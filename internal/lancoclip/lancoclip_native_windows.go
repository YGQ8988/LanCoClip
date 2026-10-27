//go:build windows

package lancoclip

// Native Windows lancoclip access for the lancoclipWatcher: text is read straight from
// CF_UNICODETEXT, and changes are delivered by WM_CLIPBOARDUPDATE instead of
// being polled with a PowerShell round trip (~hundreds of ms per probe, which
// is why rapid copies used to collapse into the last one).

import (
	"context"
	"fmt"
	"log"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	wmLancoclipUpdate = 0x031D // WM_CLIPBOARDUPDATE
	wmQuit            = 0x0012

	// hwndMessage is HWND_MESSAGE, i.e. (HWND)-3: a message-only window. It is
	// never shown and never appears in the window list; it exists solely to
	// receive lancoclip notifications.
	hwndMessage = ^uintptr(2)

	lancoclipClassName = "LanCoClipWatcher"
)

var (
	procRegisterClassExW              = user32.NewProc("RegisterClassExW")
	procCreateWindowExW               = user32.NewProc("CreateWindowExW")
	procDefWindowProcW                = user32.NewProc("DefWindowProcW")
	procDestroyWindow                 = user32.NewProc("DestroyWindow")
	procAddLancoclipFormatListener    = user32.NewProc("AddClipboardFormatListener")
	procRemoveLancoclipFormatListener = user32.NewProc("RemoveClipboardFormatListener")
	procGetMessageW                   = user32.NewProc("GetMessageW")
	procTranslateMessageW             = user32.NewProc("TranslateMessage")
	procDispatchMessageW              = user32.NewProc("DispatchMessageW")
	procGetLancoclipSequenceNumber    = user32.NewProc("GetClipboardSequenceNumber")
	procPostThreadMessageW            = user32.NewProc("PostThreadMessageW")
	procGetCurrentThreadID            = kernel32.NewProc("GetCurrentThreadId")
	procGetModuleHandleW              = kernel32.NewProc("GetModuleHandleW")

	clipNotifyMu sync.Mutex
	clipNotify   chan<- struct{}
)

// nativeReadText reads the lancoclip text through Win32. handled=false means
// the caller must fall back to the PowerShell helper (lancoclip busy, or an
// ANSI-only lancoclip whose code page only the helper decodes correctly).
func nativeReadText() (string, bool) {
	if !openLancoclip() {
		return "", false // 剪贴板被其他程序占用，交给子进程兜底
	}
	defer procCloseLancoclip.Call()

	if u, ok := lancoclipUnicodeText(); ok {
		return u, true
	}
	if r, _, _ := procIsLancoclipFormatAvailable.Call(cfText); r != 0 {
		return "", false // 只有 ANSI 文本（老程序）：让 PowerShell 按系统代码页解码
	}
	return "", true // 剪贴板里没有文本（图片、文件等）
}

// lancoclipUnicodeText reads CF_UNICODETEXT; ok=false when it is absent.
// The lancoclip must already be open.
func lancoclipUnicodeText() (string, bool) {
	h, _, _ := procGetLancoclipData.Call(cfUnicodeText)
	if h == 0 {
		return "", false
	}
	ptr, _, _ := procGlobalLock.Call(h)
	if ptr == 0 {
		return "", false
	}
	defer procGlobalUnlock.Call(h)

	size, _, _ := procGlobalSize.Call(h)
	if size < 2 {
		return "", false
	}
	buf := unsafe.Slice((*uint16)(ptrFromAddr(ptr)), int(size)/2)
	for len(buf) > 0 && buf[len(buf)-1] == 0 { // 去掉结尾的 NUL
		buf = buf[:len(buf)-1]
	}
	return windows.UTF16ToString(buf), true
}

// lancoclipChanges emits one signal per lancoclip write. The listener is the
// primary path; if Windows refuses to hand out a message-only window (or the
// listener API is unavailable), it degrades to polling the lancoclip sequence
// number, which is still a single cheap user32 call.
func lancoclipChanges(ctx context.Context, interval time.Duration) <-chan struct{} {
	ch := make(chan struct{}, 1)
	go func() {
		if err := runLancoclipListener(ctx, ch); err != nil {
			log.Printf("lancoclip change listener unavailable (%v); falling back to sequence polling", err)
			pollLancoclipSequence(ctx, ch, interval)
		}
	}()
	return ch
}

// wndClassEx mirrors WNDCLASSEXW (amd64/arm64 layout).
type wndClassEx struct {
	size       uint32
	style      uint32
	wndProc    uintptr
	clsExtra   int32
	wndExtra   int32
	instance   uintptr
	icon       uintptr
	cursor     uintptr
	background uintptr
	menuName   *uint16
	className  *uint16
	iconSm     uintptr
}

// winMsg mirrors MSG.
type winMsg struct {
	hwnd     uintptr
	message  uint32
	wParam   uintptr
	lParam   uintptr
	time     uint32
	ptX, ptY int32
}

func lancoclipWndProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	if msg == wmLancoclipUpdate {
		clipNotifyMu.Lock()
		ch := clipNotify
		clipNotifyMu.Unlock()
		if ch != nil {
			signalChange(ch)
		}
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wParam, lParam)
	return r
}

// lancoclipWndProcAddr pins the callback: the address is handed to the OS, so
// the trampoline must outlive the window.
var lancoclipWndProcAddr = syscall.NewCallback(lancoclipWndProc)

// runLancoclipListener owns a locked OS thread with a message-only window and
// pumps its message loop until ctx is cancelled. Blocks for the process
// lifetime on success.
func runLancoclipListener(ctx context.Context, ch chan<- struct{}) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	className, err := syscall.UTF16PtrFromString(lancoclipClassName)
	if err != nil {
		return err
	}
	hInst, _, _ := procGetModuleHandleW.Call(0)

	wc := wndClassEx{
		size:      uint32(unsafe.Sizeof(wndClassEx{})),
		wndProc:   lancoclipWndProcAddr,
		instance:  hInst,
		className: className,
	}
	if r, _, e := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return fmt.Errorf("RegisterClassExW: %v", e)
	}
	// CreateWindowExW(dwExStyle, lpClassName, lpWindowName, dwStyle, x, y, w, h,
	//                 hWndParent, hMenu, hInstance, lpParam)
	hwnd, _, e := procCreateWindowExW.Call(
		0, uintptr(unsafe.Pointer(className)), 0, 0,
		0, 0, 0, 0,
		hwndMessage, 0, hInst, 0,
	)
	if hwnd == 0 {
		return fmt.Errorf("CreateWindowExW: %v", e)
	}
	defer procDestroyWindow.Call(hwnd)

	if r, _, e := procAddLancoclipFormatListener.Call(hwnd); r == 0 {
		return fmt.Errorf("AddClipboardFormatListener: %v", e)
	}
	defer procRemoveLancoclipFormatListener.Call(hwnd)

	clipNotifyMu.Lock()
	clipNotify = ch
	clipNotifyMu.Unlock()
	defer func() {
		clipNotifyMu.Lock()
		clipNotify = nil
		clipNotifyMu.Unlock()
	}()

	// WM_QUIT is the only way to leave the blocking GetMessage loop, and it has
	// to be posted to this thread from outside.
	tid, _, _ := procGetCurrentThreadID.Call()
	go func() {
		<-ctx.Done()
		procPostThreadMessageW.Call(tid, wmQuit, 0, 0)
	}()

	var m winMsg
	for {
		// GetMessageW returns 0 for WM_QUIT and -1 on error.
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if r == 0 || int32(r) == -1 {
			return nil
		}
		procTranslateMessageW.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

// pollLancoclipSequence is the fallback detector: GetClipboardSequenceNumber
// increments on every lancoclip write, so one user32 call per tick is enough —
// no subprocess involved.
func pollLancoclipSequence(ctx context.Context, ch chan<- struct{}, interval time.Duration) {
	last, _, _ := procGetLancoclipSequenceNumber.Call()
	t := time.NewTicker(effectivePoll(interval))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if cur, _, _ := procGetLancoclipSequenceNumber.Call(); cur != last {
				last = cur
				signalChange(ch)
			}
		}
	}
}
