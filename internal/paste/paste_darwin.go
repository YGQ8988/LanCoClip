//go:build darwin && cgo

// Package paste simulates a paste keystroke (Cmd+V on macOS, Ctrl+V on
// Windows) into the currently focused app — used when the user picks an
// entry from the hotkey history menu, so it lands directly in place.
package paste

/*
#cgo LDFLAGS: -framework CoreGraphics -framework ApplicationServices -framework Foundation -framework AppKit
#include <ApplicationServices/ApplicationServices.h>

// Implemented in paste_darwin.m.
int pasteTrusted(int prompt);
void pastePostCmdV(void);
*/
import "C"

import "errors"

var prompted bool // 只在首次未授权时弹一次系统授权提示

// FreezeFocus is a no-op on macOS: Do() targets the frontmost app directly
// (CGEventPostToPid), no focus restoration is needed.
func FreezeFocus() {}

// Do sends Cmd+V to the frontmost app. The first call while Accessibility
// permission is missing triggers the system prompt (macOS requires 辅助功能
// permission to post keyboard events) and returns an error.
func Do() error {
	trusted := C.pasteTrusted(boolToInt(!prompted))
	prompted = true
	if trusted == 0 {
		return errors.New("需要辅助功能权限才能模拟粘贴（系统已弹出授权提示）")
	}
	C.pastePostCmdV()
	return nil
}

func boolToInt(b bool) C.int {
	if b {
		return 1
	}
	return 0
}
