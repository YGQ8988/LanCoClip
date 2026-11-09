//go:build darwin && cgo

package notify

/*
#cgo darwin CFLAGS: -fobjc-arc
#cgo LDFLAGS: -framework Cocoa -framework UserNotifications

#include <stdbool.h>
#include <stdlib.h>

// Implemented in notify_darwin.m (compiled by cgo as Objective-C).
bool lcpNotifyAvailable(void);
int lcpNotify(const char *title, const char *body);
*/
import "C"

import (
	"sync"
	"unsafe"
)

var (
	clickMu   sync.Mutex
	clickFn   func()
	delivered bool // 通知已投递、且点击回调尚未被消费
)

// Supported reports whether notifications can be delivered. macOS only allows
// UNUserNotificationCenter inside a signed .app bundle; a bare binary (go run)
// would crash the framework, so it is reported as unsupported.
func Supported() bool { return bool(C.lcpNotifyAvailable()) }

// Notify posts a notification. onClick runs on its own goroutine when the user
// clicks it; pass nil if no action is wanted.
func Notify(title, body string, onClick func()) error {
	if !Supported() {
		return ErrUnsupported
	}
	ct := C.CString(title)
	defer C.free(unsafe.Pointer(ct))
	cb := C.CString(body)
	defer C.free(unsafe.Pointer(cb))

	clickMu.Lock()
	clickFn = onClick
	delivered = true
	clickMu.Unlock()

	if C.lcpNotify(ct, cb) != 0 {
		// 投递失败：清掉刚设置的回调，避免之后点击别的历史通知误触发。
		clickMu.Lock()
		clickFn = nil
		delivered = false
		clickMu.Unlock()
		return ErrUnsupported
	}
	return nil
}

// goNotifyClicked is called from the Objective-C delegate (a non-Go thread) when
// the notification is clicked.
//
//export goNotifyClicked
func goNotifyClicked() {
	clickMu.Lock()
	fn := clickFn
	ok := delivered
	clickFn = nil     // 取出即清空，避免闭包长期持有
	delivered = false // 复位：点击任意历史通知不会再执行上一次的回调
	clickMu.Unlock()
	if ok && fn != nil {
		go fn()
	}
}
