//go:build darwin && cgo

package lancoclip

// Native macOS lancoclip access for the lancoclipWatcher: NSPasteboard has no change
// notification API, so the standard approach is polling its change counter —
// cheap enough (a property read) to run many times per second, unlike the
// pbpaste subprocess it replaces.

/*
#cgo LDFLAGS: -framework Cocoa
#include <stdlib.h>
// Implemented in lancoclip_native_darwin.m (compiled as Objective-C).
long copyLancoclipChangeCount(void);
char *copyLancoclipText(void);
*/
import "C"

import (
	"context"
	"time"
	"unsafe"
)

// nativeReadText reads the lancoclip text straight from NSPasteboard. The
// second return value is always true here: macOS has a native reader, so the
// caller never has to fall back to pbpaste.
func nativeReadText() (string, bool) {
	p := C.copyLancoclipText()
	if p == nil {
		return "", true // no text on the lancoclip
	}
	defer C.free(unsafe.Pointer(p))
	return C.GoString(p), true
}

// lancoclipChanges emits one signal each time the lancoclip change counter
// moves, i.e. once per lancoclip write.
func lancoclipChanges(ctx context.Context) <-chan struct{} {
	ch := make(chan struct{}, 1)
	go func() {
		last := C.copyLancoclipChangeCount()
		t := time.NewTicker(changePollInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if cur := C.copyLancoclipChangeCount(); cur != last {
					last = cur
					signalChange(ch)
				}
			}
		}
	}()
	return ch
}
