//go:build windows

package paste

import (
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

// Windows 模拟粘贴：SendInput 发 Ctrl+V 四个键盘事件。无需任何特殊权限。
//
// 焦点问题：托盘/弹出菜单会抢走焦点，直接发 Ctrl+V 会落到托盘窗口上。
// 因此后台 goroutine 持续记录「最近的、不属于本进程的前台窗口」，
// 粘贴前先 SetForegroundWindow 还给目标窗口再发送。
var user32 = syscall.NewLazyDLL("user32.dll")
var procSendInput = user32.NewProc("SendInput")
var procGetForegroundWindow = user32.NewProc("GetForegroundWindow")
var procGetWindowThreadPID = user32.NewProc("GetWindowThreadProcessId")
var procSetForeground = user32.NewProc("SetForegroundWindow")
var procIsWindow = user32.NewProc("IsWindow")
var procIsWindowVisible = user32.NewProc("IsWindowVisible")
var procIsIconic = user32.NewProc("IsIconic")
var procShowWindow = user32.NewProc("ShowWindow")
var procKeybdEvent = user32.NewProc("keybd_event")

const (
	inputKeyboard  = 1
	vkControl      = 0x11
	vkV            = 0x56
	vkMenu         = 0x12
	keyeventfKeyUp = 0x0002
	swRestore      = 9
)

// keybdInput 对应 KEYBDINPUT；input 是 INPUT 结构（type + union，
// union 按 MOUSEINPUT 的 32 字节补齐，sizeof(INPUT)=40，cbSize 不对 SendInput 会失败）。
type keybdInput struct {
	vk    uint16
	scan  uint16
	flags uint32
	time  uint32
	extra uintptr
}

type input struct {
	typ uint32
	_   uint32
	ki  keybdInput
	_   [8]byte // union 补齐到 40 字节
}

// ── 焦点追踪 ────────────────────────────────────────────────
var (
	focusOnce   sync.Once
	focusMu     sync.Mutex
	lastFocus   uintptr
	focusFrozen int32
)

// FreezeFocus 暂停焦点记录：菜单项点击后到粘贴发出前的窗口里，
// 前台可能短暂变成资源管理器外壳，不应覆盖已记住的目标窗口。
// 由托盘/快捷键菜单的点击回调调用；Do() 发送前自动解冻。
func FreezeFocus() { atomic.StoreInt32(&focusFrozen, 1) }

func startFocusTracker() {
	focusOnce.Do(func() {
		go func() {
			for {
				if atomic.LoadInt32(&focusFrozen) == 0 {
					if h, _, _ := procGetForegroundWindow.Call(); h != 0 && !isOurWindow(h) {
						focusMu.Lock()
						lastFocus = h
						focusMu.Unlock()
					}
				}
				time.Sleep(300 * time.Millisecond)
			}
		}()
	})
}

func isOurWindow(h uintptr) bool {
	var pid uint32
	procGetWindowThreadPID.Call(h, uintptr(unsafe.Pointer(&pid)))
	return pid != 0 && pid == uint32(os.Getpid())
}

// restoreFocus 把焦点还给最近记录的用户窗口（托盘菜单会抢焦点）。
func restoreFocus() {
	focusMu.Lock()
	h := lastFocus
	focusMu.Unlock()
	if h == 0 {
		return
	}
	if r, _, _ := procIsWindow.Call(h); r == 0 {
		return
	}
	if r, _, _ := procIsWindowVisible.Call(h); r == 0 {
		return
	}
	if cur, _, _ := procGetForegroundWindow.Call(); cur == h {
		return // 已经是目标窗口，无需切换
	}
	if r, _, _ := procIsIconic.Call(h); r != 0 {
		procShowWindow.Call(h, swRestore) // 目标被最小化时还原，否则粘贴会落进隐形窗口
	}
	// ALT 轻击：让系统认为本进程刚接收过输入，解除后台进程
	// SetForegroundWindow 的限制（Windows 的经典做法）。
	procKeybdEvent.Call(vkMenu, 0, 0, 0)
	procKeybdEvent.Call(vkMenu, 0, keyeventfKeyUp, 0)
	procSetForeground.Call(h)
	time.Sleep(150 * time.Millisecond) // 等待目标窗口完成激活
}

// Do sends Ctrl+V into the previously focused app.
func Do() error {
	startFocusTracker()
	atomic.StoreInt32(&focusFrozen, 0)
	restoreFocus()

	events := []input{
		{typ: inputKeyboard, ki: keybdInput{vk: vkControl}},
		{typ: inputKeyboard, ki: keybdInput{vk: vkV}},
		{typ: inputKeyboard, ki: keybdInput{vk: vkV, flags: keyeventfKeyUp}},
		{typ: inputKeyboard, ki: keybdInput{vk: vkControl, flags: keyeventfKeyUp}},
	}
	// SendInput 成功时返回注入的事件数（这里是 4），0 表示失败
	r, _, _ := procSendInput.Call(
		uintptr(len(events)),
		uintptr(unsafe.Pointer(&events[0])),
		unsafe.Sizeof(events[0]),
	)
	if r == 0 {
		return errors.New("SendInput failed")
	}
	return nil
}
