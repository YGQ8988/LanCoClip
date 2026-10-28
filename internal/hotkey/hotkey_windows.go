//go:build windows

package hotkey

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"lanoclip/internal/store"
)

// Windows 全局快捷键：user32 RegisterHotKey 注册到当前线程，GetMessage 循环
// 收 WM_HOTKEY。不需要 cgo，交叉编译可用。Update 通过 PostThreadMessage 让
// 消息循环线程自己完成注销/重注册（RegisterHotKey 绑定注册线程）。
var (
	user32               = syscall.NewLazyDLL("user32.dll")
	procRegisterHotKey   = user32.NewProc("RegisterHotKey")
	procUnregisterHotKey = user32.NewProc("UnregisterHotKey")
	procGetMessage       = user32.NewProc("GetMessageW")
	procTranslateMessage = user32.NewProc("TranslateMessage")
	procDispatchMessage  = user32.NewProc("DispatchMessageW")
	procPostThreadMsg    = user32.NewProc("PostThreadMessageW")
	kernel32             = syscall.NewLazyDLL("kernel32.dll")
	procGetCurrentThread = kernel32.NewProc("GetCurrentThreadId")
)

const (
	wmHotkey     = 0x0312
	wmReregister = 0x8000 + 0x0001 // WM_APP+1：请求消息循环线程重注册快捷键
	modNoRepeat  = 0x4000
	hotkeyQRID   = 1
	hotkeyHistID = 2
)

type msg struct {
	hwnd     uintptr
	message  uint32
	wParam   uintptr
	lParam   uintptr
	time     uint32
	ptX, ptY int32
}

// comboSpec is one resolved hotkey: virtual key code + modifier flags.
type comboSpec struct {
	vk   uint32
	mods uintptr
}

func resolve(s, fallback string) (comboSpec, error) {
	if s == "" {
		s = fallback
	}
	c, err := Parse(s)
	if err != nil {
		return comboSpec{}, err
	}
	vk, ok := keyTable[c.Key]
	if !ok {
		return comboSpec{}, fmt.Errorf("不支持按键 %q", c.Key)
	}
	return comboSpec{vk: vk, mods: winMods(c) | modNoRepeat}, nil
}

var (
	mu        sync.Mutex
	onQR      func()
	onHistory func()
	installed bool
	threadID  uint32
	// pending 新组合；reapplyResult 用于回传注册结果
	pending       [2]comboSpec
	reapplyResult chan error
)

// resolvePair resolves both combos (empty = platform default) and rejects a
// pair that presses the same keys.
func resolvePair(qrCombo, histCombo, fbQR, fbHist string) ([2]comboSpec, error) {
	specQR, err := resolve(qrCombo, fbQR)
	if err != nil {
		return [2]comboSpec{}, fmt.Errorf("二维码快捷键无效: %w", err)
	}
	specH, err := resolve(histCombo, fbHist)
	if err != nil {
		return [2]comboSpec{}, fmt.Errorf("历史快捷键无效: %w", err)
	}
	if specQR == specH {
		return [2]comboSpec{}, errors.New("两个快捷键不能相同")
	}
	return [2]comboSpec{specQR, specH}, nil
}

// registerPair registers both hotkeys, rolling back the first one when the
// second is already taken. Runs on the message-loop thread (RegisterHotKey is
// bound to the registering thread).
//
// RegisterHotKey(hWnd, id, fsModifiers, vk) —— 参数顺序必须是修饰键在前、虚拟键码在后。
func registerPair(specs [2]comboSpec) error {
	if r, _, _ := procRegisterHotKey.Call(0, hotkeyQRID, specs[0].mods, uintptr(specs[0].vk)); r == 0 {
		return errors.New("RegisterHotKey 失败（组合键可能被占用）")
	}
	if r, _, _ := procRegisterHotKey.Call(0, hotkeyHistID, specs[1].mods, uintptr(specs[1].vk)); r == 0 {
		procUnregisterHotKey.Call(0, hotkeyQRID)
		return errors.New("RegisterHotKey 失败（组合键可能被占用）")
	}
	return nil
}

// Install registers the two hotkeys and pumps the hotkey message loop on a
// locked OS thread. Non-fatal: failures (e.g. hotkey already taken) are
// reported through the returned error but do not stop startup.
func Install(onQRFn, onHistoryFn func(), qrCombo, histCombo string) error {
	specs, err := resolvePair(qrCombo, histCombo, store.DefaultHotkeyQR(), store.DefaultHotkeyHistory())
	if err != nil {
		return err
	}

	mu.Lock()
	onQR, onHistory = onQRFn, onHistoryFn
	mu.Unlock()

	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		tid, _, _ := procGetCurrentThread.Call()
		mu.Lock()
		threadID = uint32(tid)
		mu.Unlock()

		// RegisterHotKey(hWnd, id, fsModifiers, vk) —— 参数顺序必须是修饰键在前、虚拟键码在后
		if err := registerPair(specs); err != nil {
			done <- err
			return
		}

		mu.Lock()
		installed = true
		mu.Unlock()

		var m msg
		for {
			// GetMessage 返回 -1 表示错误（如线程退出），0 表示 WM_QUIT
			r, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
			if r == 0 || int32(r) == -1 {
				return
			}
			switch m.message {
			case wmHotkey:
				switch m.wParam {
				case hotkeyQRID:
					mu.Lock()
					f := onQR
					mu.Unlock()
					if f != nil {
						f()
					}
				case hotkeyHistID:
					mu.Lock()
					f := onHistory
					mu.Unlock()
					if f != nil {
						f()
					}
				}
				continue
			case wmReregister:
				mu.Lock()
				ch := reapplyResult
				specs := pending
				reapplyResult = nil
				mu.Unlock()
				if ch == nil {
					continue
				}
				ch <- doReregister(specs)
				continue
			}
			procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
			procDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
		}
	}()
	return nil
}

// doReregister (message-loop thread) swaps both registrations, rolling back
// to best-effort when a new combo is already taken.
func doReregister(specs [2]comboSpec) error {
	procUnregisterHotKey.Call(0, hotkeyQRID)
	procUnregisterHotKey.Call(0, hotkeyHistID)
	return registerPair(specs)
}

// Update re-registers both hotkeys with new combos. Parse/validation errors
// are returned synchronously; registration happens on the hotkey message
// loop thread and its result is awaited (bounded). No-op before Install.
func Update(qrCombo, histCombo string) error {
	specs, err := resolvePair(qrCombo, histCombo, store.DefaultHotkeyQR(), store.DefaultHotkeyHistory())
	if err != nil {
		return err
	}

	mu.Lock()
	if !installed || threadID == 0 {
		mu.Unlock()
		return nil
	}
	ch := make(chan error, 1)
	pending = specs
	reapplyResult = ch
	tid := threadID
	mu.Unlock()

	procPostThreadMsg.Call(uintptr(tid), wmReregister, 0, 0)
	select {
	case err := <-ch:
		return err
	case <-time.After(3 * time.Second):
		return errors.New("注册快捷键超时")
	}
}
