//go:build windows

package histpop

import (
	"sync"
	"syscall"
	"unsafe"
)

// Windows 原生弹出菜单：CreatePopupMenu + TrackPopupMenuEx(TPM_RETURNCMD)，
// 在调用线程上阻塞直到用户选择或取消。菜单归属一个本线程创建的隐藏窗口。
var (
	user32              = syscall.NewLazyDLL("user32.dll")
	procRegisterClassEx = user32.NewProc("RegisterClassExW")
	procCreateWindowEx  = user32.NewProc("CreateWindowExW")
	procDefWindowProc   = user32.NewProc("DefWindowProcW")
	procDestroyWindow   = user32.NewProc("DestroyWindow")
	procAppendMenu      = user32.NewProc("AppendMenuW")
	procTrackPopupMenu  = user32.NewProc("TrackPopupMenuEx")
	procGetCursorPos    = user32.NewProc("GetCursorPos")
	procDestroyMenu     = user32.NewProc("DestroyMenu")
	procCreatePopupMenu = user32.NewProc("CreatePopupMenu")
	procSetForeground   = user32.NewProc("SetForegroundWindow")
	procPostMessage     = user32.NewProc("PostMessageW")
	kernel32            = syscall.NewLazyDLL("kernel32.dll")
	procGetModuleHandle = kernel32.NewProc("GetModuleHandleW")
)

const (
	mfString       = 0x00000000
	mfPopup        = 0x00000010 // 该菜单项挂着子菜单（wIDNewItem 传子菜单句柄）
	mfGrayed       = 0x00000001 // 空组：灰掉不可点
	tpmReturncmd   = 0x0100
	tpmNonotify    = 0x0080
	tpmRightbutton = 0x0002
	wmNull         = 0x0000
)

// 每组条目 id 编码为 group*itemIDStep + item + 1，TrackPopupMenuEx 返回该 id。
const itemIDStep = 1000

// Group is one top-level row of the popup plus its submenu entries; hovering
// the row reveals the items.
type Group struct {
	Title string
	Items []string
}

type point struct{ X, Y int32 }

type wndClassEx struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  uintptr
	lpszClassName uintptr
	hIconSm       uintptr
}

const histPopClassName = "LanCoClipHistPop"

// wndProc is the minimal window procedure for the hidden owner window.
func wndProc(hwnd uintptr, msg uint32, wp, lp uintptr) uintptr {
	r, _, _ := procDefWindowProc.Call(hwnd, uintptr(msg), wp, lp)
	return r
}

var (
	classOnce sync.Once
	classErr  error
)

func ensureWindowClass() error {
	classOnce.Do(func() {
		className, _ := syscall.UTF16PtrFromString(histPopClassName)
		hmod, _, _ := procGetModuleHandle.Call(0)
		wc := wndClassEx{
			cbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
			lpfnWndProc:   syscall.NewCallback(wndProc),
			hInstance:     hmod,
			lpszClassName: uintptr(unsafe.Pointer(className)),
		}
		atom, _, _ := procRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc)))
		if atom == 0 {
			classErr = syscall.GetLastError()
		}
	})
	return classErr
}

// Show pops a cascading native menu at the cursor: one row per group, each
// opening its items in a submenu. Blocks until dismissed; onPick receives the
// chosen (group, item) indexes.
func Show(groups []Group, onPick func(group, item int)) bool {
	if len(groups) == 0 {
		return false
	}
	if err := ensureWindowClass(); err != nil {
		return false
	}

	cls, _ := syscall.UTF16PtrFromString(histPopClassName)
	title, _ := syscall.UTF16PtrFromString("")
	hmod, _, _ := procGetModuleHandle.Call(0)
	hwnd, _, _ := procCreateWindowEx.Call(
		0,
		uintptr(unsafe.Pointer(cls)),
		uintptr(unsafe.Pointer(title)),
		0, // dwStyle：不显示
		0, 0, 0, 0,
		0, 0, hmod, 0,
	)
	if hwnd == 0 {
		return false
	}
	defer procDestroyWindow.Call(hwnd)

	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return false
	}
	defer procDestroyMenu.Call(menu)

	// 顶层每组一行，挂子菜单：鼠标对焦该行后展开该组条目。
	var subs []uintptr
	defer func() {
		for _, sub := range subs {
			procDestroyMenu.Call(sub)
		}
	}()
	for g, grp := range groups {
		titlePtr, err := syscall.UTF16PtrFromString(grp.Title)
		if err != nil {
			continue
		}
		if len(grp.Items) == 0 {
			// 空历史提示：灰掉、不挂子菜单
			procAppendMenu.Call(menu, mfString|mfGrayed, 0, uintptr(unsafe.Pointer(titlePtr)))
			continue
		}
		sub, _, _ := procCreatePopupMenu.Call()
		if sub == 0 {
			continue
		}
		subs = append(subs, sub)
		for i, s := range grp.Items {
			p, err := syscall.UTF16PtrFromString(s)
			if err != nil {
				continue
			}
			id := uintptr(g*itemIDStep + i + 1)
			procAppendMenu.Call(sub, mfString, id, uintptr(unsafe.Pointer(p)))
		}
		// AppendMenuW(hMenu, uFlags, uIDNewItem, lpNewItem)：
		// 带 MF_POPUP 时第 3 个参数是子菜单句柄，不是菜单项 id
		procAppendMenu.Call(menu, mfPopup|mfString, sub, uintptr(unsafe.Pointer(titlePtr)))
	}

	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	// TrackPopupMenuEx(hMenu, fuFlags, x, y, hWnd, lptpm) —— 窗口句柄在第 5 位
	procSetForeground.Call(hwnd)
	ret, _, _ := procTrackPopupMenu.Call(
		menu,
		tpmReturncmd|tpmNonotify|tpmRightbutton,
		uintptr(pt.X), uintptr(pt.Y),
		hwnd, 0,
	)
	procPostMessage.Call(hwnd, wmNull, 0, 0)

	if ret == 0 {
		return true // 用户取消
	}
	g := (int(ret) - 1) / itemIDStep
	i := (int(ret) - 1) % itemIDStep
	if g < 0 || g >= len(groups) || i < 0 || i >= len(groups[g].Items) {
		return true // 索引异常，不回调
	}
	if onPick != nil {
		onPick(g, i)
	}
	return true
}
