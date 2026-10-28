//go:build darwin && cgo

// Package hotkey registers global hotkeys that work even when the app is not
// focused. On macOS we use Carbon RegisterEventHotKey: unlike NSEvent global
// monitors it does NOT require the user to grant Accessibility permission.
package hotkey

/*
#cgo LDFLAGS: -framework Carbon -framework Foundation
#include <Carbon/Carbon.h>

// Implemented in hotkey_darwin.m; they call back into Go through the
// //export-ed lancoclipHotkeyFired / hotkeyReapplyDone below.
OSStatus installHotkeys(int idQR, int keyQR, int modsQR, int idHistory, int keyHistory, int modsHistory);
void reapplyHotkeysOnMain(int idQR, int keyQR, int modsQR, int idHistory, int keyHistory, int modsHistory);
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
	reapCh    chan int // result of the latest reapply, buffered 1
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

// hotkeyReapplyDone receives the result of reapplyHotkeysOnMain.
//
//export hotkeyReapplyDone
func hotkeyReapplyDone(status C.int) {
	mu.Lock()
	ch := reapCh
	mu.Unlock()
	if ch != nil {
		ch <- int(status)
	}
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

	mu.Lock()
	reapCh = make(chan int, 1)
	mu.Unlock()
	C.reapplyHotkeysOnMain(C.int(1), C.int(keyQR), C.int(modsQR),
		C.int(2), C.int(keyH), C.int(modsH))

	mu.Lock()
	ch := reapCh
	mu.Unlock()
	select {
	case st := <-ch:
		if st != 0 {
			return fmt.Errorf("注册快捷键失败（可能已被其他应用占用）: %d", st)
		}
		return nil
	case <-time.After(3 * time.Second):
		return errors.New("注册快捷键超时")
	}
}
