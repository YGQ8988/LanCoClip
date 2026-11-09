//go:build darwin && cgo

// Package hotkey registers global hotkeys that work even when the app is not
// focused. On macOS we use Carbon RegisterEventHotKey: unlike NSEvent global
// monitors it does NOT require the user to grant Accessibility permission.
package hotkey

/*
#cgo darwin CFLAGS: -fobjc-arc
#cgo LDFLAGS: -framework Carbon -framework Foundation
#include <Carbon/Carbon.h>

// Implemented in hotkey_darwin.m; they call back into Go through the
// //export-ed lancoclipHotkeyFired / hotkeyReapplyDone below.
OSStatus installHotkeys(int idQR, int keyQR, int modsQR, int idHistory, int keyHistory, int modsHistory);
void reapplyHotkeysOnMain(int token, int idQR, int keyQR, int modsQR, int idHistory, int keyHistory, int modsHistory);
*/
import "C"

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"lanoclip/internal/store"
)

var (
	mu        sync.Mutex
	onQR      func()
	onHistory func()
	installed bool
	// reapCh 是「当前等待结果的事务」的 channel，buffered 1；reapSeq 是它
	// 的代号。回调把代号带回来，只有代号匹配才投递——超时放弃的事务即使
	// 回调迟到，也不会把结果投进下一次事务的 channel。
	reapCh  chan int
	reapSeq int
	// updateMu 串行化整个 Update 事务：reapCh 是包级状态，并发 Update 会
	// 互相覆盖，使 hotkeyReapplyDone 把结果投递到错误 channel，甚至阻塞
	// Carbon 主线程。
	updateMu sync.Mutex
)

// lancoclipHotkeyFired is called from the Carbon event handler (main thread).
//
//export lancoclipHotkeyFired
func lancoclipHotkeyFired(id C.int) {
	mu.Lock()
	qr, hist := onQR, onHistory
	mu.Unlock()
	switch int(id) {
	case 1:
		if qr != nil {
			qr()
		}
	case 2:
		if hist != nil {
			hist()
		}
	}
}

// hotkeyReapplyDone receives the result of reapplyHotkeysOnMain. token
// identifies the Update transaction that queued the work; a stale callback
// (its transaction already timed out) is dropped instead of being delivered
// to whatever channel a newer Update installed.
//
//export hotkeyReapplyDone
func hotkeyReapplyDone(token C.int, status C.int) {
	mu.Lock()
	if reapCh == nil || int(token) != reapSeq {
		// 没有等待者（超时后已置 nil）或回调属于已放弃的事务：直接丢弃，
		// 绝不阻塞 Carbon 主线程。
		mu.Unlock()
		return
	}
	ch := reapCh
	reapCh = nil // 取出即清空，迟到的第二次回调会被上面的判断丢弃
	mu.Unlock()
	select {
	case ch <- int(status):
	default: // buffered 1 且刚清空，正常不会走到；兜底避免阻塞主线程
	}
}

// hotkeyReapplyValid reports whether the Update transaction identified by
// token still has a waiter. The main-thread block checks this before touching
// the registrations, so work queued by an Update that has already timed out
// is skipped instead of changing the live hotkeys behind the caller's back
// (the caller has already seen an error and will not adapt its stored value).
//
//export hotkeyReapplyValid
func hotkeyReapplyValid(token C.int) C.int {
	mu.Lock()
	ok := reapCh != nil && int(token) == reapSeq
	mu.Unlock()
	if ok {
		return 1
	}
	return 0
}

// installCombo resolves a combo string ("" falls back to the historical
// default) into keycode + Carbon mods.
func installCombo(s, fallback string) (int, int, error) {
	if s == "" {
		s = fallback
	}
	c, err := Parse(s)
	if err != nil {
		return 0, 0, err
	}
	k, ok := keyTable[c.Key]
	if !ok {
		return 0, 0, fmt.Errorf("不支持按键 %q", c.Key)
	}
	return k, carbonMods(c), nil
}

// Install registers the two hotkeys. Must be called from the main thread
// (i.e. from inside the tray's onReady callback): Carbon event targets are
// main-thread bound.
func Install(onQRFn, onHistoryFn func(), qrCombo, histCombo string) error {
	keyQR, modsQR, err := installCombo(qrCombo, store.DefaultHotkeyQR())
	if err != nil {
		return fmt.Errorf("二维码快捷键无效: %w", err)
	}
	keyH, modsH, err := installCombo(histCombo, store.DefaultHotkeyHistory())
	if err != nil {
		return fmt.Errorf("历史快捷键无效: %w", err)
	}

	mu.Lock()
	onQR, onHistory = onQRFn, onHistoryFn
	mu.Unlock()
	status := C.installHotkeys(C.int(1), C.int(keyQR), C.int(modsQR),
		C.int(2), C.int(keyH), C.int(modsH))
	if status != 0 {
		return fmt.Errorf("RegisterEventHotKey failed: %d", int(status))
	}
	mu.Lock()
	installed = true
	mu.Unlock()
	return nil
}

// Update re-registers both hotkeys with new combos. Safe to call from any
// goroutine: the actual registration is dispatched onto the main thread,
// where the Cocoa/Carbon runloop lives. It is a no-op before Install.
func Update(qrCombo, histCombo string) error {
	// 串行化整个事务：reapCh 是包级状态，并发 Update 会互相覆盖并让
	// hotkeyReapplyDone 投递到错误 channel（甚至阻塞 Carbon 主线程）。
	updateMu.Lock()
	defer updateMu.Unlock()

	mu.Lock()
	if !installed {
		mu.Unlock()
		return nil
	}
	mu.Unlock()

	keyQR, modsQR, err := installCombo(qrCombo, store.DefaultHotkeyQR())
	if err != nil {
		return fmt.Errorf("二维码快捷键无效: %w", err)
	}
	keyH, modsH, err := installCombo(histCombo, store.DefaultHotkeyHistory())
	if err != nil {
		return fmt.Errorf("历史快捷键无效: %w", err)
	}
	if keyQR == keyH && modsQR == modsH {
		return errors.New("两个快捷键不能相同")
	}

	ch := make(chan int, 1)
	mu.Lock()
	reapSeq++
	token := reapSeq
	reapCh = ch
	mu.Unlock()
	C.reapplyHotkeysOnMain(C.int(token), C.int(1), C.int(keyQR), C.int(modsQR),
		C.int(2), C.int(keyH), C.int(modsH))

	select {
	case st := <-ch:
		if st != 0 {
			return fmt.Errorf("注册快捷键失败（可能已被其他应用占用）: %d", st)
		}
		return nil
	case <-time.After(3 * time.Second):
		// 放弃本事务：清掉包级 channel，迟到的回调（带旧 token）会被
		// hotkeyReapplyDone 丢弃，不会串到下一次 Update。
		mu.Lock()
		if reapCh == ch {
			reapCh = nil
		}
		mu.Unlock()
		return errors.New("注册快捷键超时")
	}
}
