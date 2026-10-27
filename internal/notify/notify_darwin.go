//go:build darwin && cgo

package notify

/*
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
	clickMu  sync.Mutex
	clickFn  func()
	delivered bool // at least one Notify call got through, so clicks may arrive
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
	clickMu.Unlock()
	if ok && fn != nil {
		go fn()
	}
}
