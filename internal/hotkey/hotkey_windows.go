//go:build windows

package hotkey

import (
	"errors"
	"fmt"
	"log"
	"runtime"
	"sync"
	"sync/atomic"
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
	procPostQuitMessage  = user32.NewProc("PostQuitMessage")
	kernel32             = syscall.NewLazyDLL("kernel32.dll")
	procGetCurrentThread = kernel32.NewProc("GetCurrentThreadId")
)

const (
	wmHotkey     = 0x0312
	wmReregister = 0x8000 + 0x0001 // WM_APP+1：请求消息循环线程重注册快捷键
	wmStop       = 0x8000 + 0x0002 // WM_APP+2：请求消息循环线程注销并退出
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
	// currentSpecs 记录当前实际生效的组合（只在消息循环 goroutine 内访问），
	// 供 doReregister 注册失败时恢复，避免两个快捷键全部失效。
	currentSpecs [2]comboSpec
	// updateMu 串行化整个 Update 事务：pending / reapplyResult 是包级状态，
	// 并发 Update 会互相覆盖，使结果投递到错误的 channel。
	updateMu sync.Mutex
	// stopped 记录 StopHotkeyLoop 是否已被调用。若停止意图先于消息循环
	// goroutine 启动（退出阶段 Install 才被调用），后者直接退出，避免留下
	// 一个再也没有人来停止的线程。
	stopped atomic.Bool
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

		// 停止意图可能先于本 goroutine 启动：直接退出，不再注册，
		// 否则会留下一个没人再能停止的消息循环。
		if stopped.Load() {
			done <- errors.New("全局快捷键已停止")
			return
		}

		tid, _, _ := procGetCurrentThread.Call()
		mu.Lock()
		threadID = uint32(tid)
		mu.Unlock()

		// RegisterHotKey(hWnd, id, fsModifiers, vk) —— 参数顺序必须是修饰键在前、虚拟键码在后
		if err := registerPair(specs); err != nil {
			// 注册失败：两个快捷键都没有生效，清掉状态，别让 Update/Stop
			// 误以为消息循环在跑。
			mu.Lock()
			installed = false
			threadID = 0
			mu.Unlock()
			done <- err
			return
		}

		// 与 doReregister 同属消息循环 goroutine，无需加锁。
		currentSpecs = specs
		mu.Lock()
		installed = true
		mu.Unlock()
		// 成功也走 done 立即返回，避免调用方白等下面的 3s 兜底超时。
		done <- nil

		var m msg
		for {
			// GetMessage 返回 -1 表示错误（如线程退出），0 表示 WM_QUIT
			r, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
			if r == 0 || int32(r) == -1 {
				return
			}
			switch m.message {
			case wmStop:
				// 退出路径：注销两个快捷键并结束消息循环，让
				// defer runtime.UnlockOSThread() 得以执行。
				procUnregisterHotKey.Call(0, hotkeyQRID)
				procUnregisterHotKey.Call(0, hotkeyHistID)
				procPostQuitMessage.Call(0)
				return
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
	select {
	case err := <-done:
		if err != nil {
			// 注册失败不再静默吞掉：组合键被占用时两个全局热键都不会生效，
			// 必须让调用方（设置页/日志）看到。
			log.Printf("hotkey: 全局快捷键注册失败: %v", err)
		}
		return err
	case <-time.After(3 * time.Second):
		// 注册失败会立即经 done 返回；走到这里说明消息循环已经正常跑起来
		// （例如 RegisterHotKey 之后 GetMessage 已进入阻塞）。
		return nil
	}
}

// doReregister (message-loop thread) swaps both registrations. If a new combo
// is already taken, it re-registers the previously active pair instead of
// leaving both global hotkeys dead until restart.
func doReregister(specs [2]comboSpec) error {
	old := currentSpecs
	procUnregisterHotKey.Call(0, hotkeyQRID)
	procUnregisterHotKey.Call(0, hotkeyHistID)
	if err := registerPair(specs); err != nil {
		// registerPair 已回滚部分注册的新键；恢复之前生效的组合（best-effort）。
		_ = registerPair(old)
		return err
	}
	currentSpecs = specs
	return nil
}

// StopHotkeyLoop asks the hotkey message loop to unregister both hotkeys and
// exit, so its deferred runtime.UnlockOSThread can run. It is called from the
// tray shutdown path; safe to call multiple times and before Install.
func StopHotkeyLoop() {
	// 先记下停止意图：若 Install 的 goroutine 还没起来，它看到后直接退出。
	stopped.Store(true)
	mu.Lock()
	tid := threadID
	threadID = 0
	installed = false
	mu.Unlock()
	if tid == 0 {
		return
	}
	procPostThreadMsg.Call(uintptr(tid), wmStop, 0, 0)
}

// Update re-registers both hotkeys with new combos. Parse/validation errors
// are returned synchronously; registration happens on the hotkey message
// loop thread and its result is awaited (bounded). Before a successful
// Install it returns an error (instead of silently doing nothing, which made
// the settings page report success for hotkeys that were never registered).
func Update(qrCombo, histCombo string) error {
	// 串行化整个事务：pending / reapplyResult / currentSpecs 是包级状态，
	// 并发 Update 会互相覆盖并让结果投递到错误的 channel。
	updateMu.Lock()
	defer updateMu.Unlock()

	specs, err := resolvePair(qrCombo, histCombo, store.DefaultHotkeyQR(), store.DefaultHotkeyHistory())
	if err != nil {
		return err
	}

	mu.Lock()
	if !installed || threadID == 0 {
		mu.Unlock()
		return errors.New("全局快捷键未注册，无法更新")
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
		// 超时后清掉包级 channel：本事务已经放弃，迟到的消息循环结果
		// （或下一次 Update 的 wmReregister）不能再投进这个无人接收的
		// channel，否则会和后续事务串台。
		mu.Lock()
		if reapplyResult == ch {
			reapplyResult = nil
		}
		mu.Unlock()
		return errors.New("注册快捷键超时")
	}
}
