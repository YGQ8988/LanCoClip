//go:build darwin && cgo

package tray

/*
#cgo LDFLAGS: -framework AppKit
#include <objc/objc.h>
#include <objc/message.h>
#include <objc/runtime.h>

// Declare the AppKit C entry point without including AppKit.h: the cgo
// preamble is compiled as C, which cannot parse Objective-C headers.
extern int NSApplicationLoad(void);

static void setAgentActivationPolicy(void) {
    // A menu-bar-only (LSUIElement) process is not a "standard GUI launch",
    // so AppKit has not been loaded yet and objc_getClass would return NULL.
    // NSApplicationLoad() is Apple's documented way to bring up AppKit from a
    // non-standard process.
    NSApplicationLoad();

    Class appClass = objc_getClass("NSApplication");
    if (appClass == 0) return;
    SEL shared = sel_registerName("sharedApplication");
    id app = ((id (*)(Class, SEL))objc_msgSend)(appClass, shared);
    if (app == 0) return;
    app = ((id (*)(Class, SEL))objc_msgSend)(appClass, shared);
    if (app == 0) return;

    // NSApplicationActivationPolicyAccessory = 1: menu-bar resident, no Dock icon.
    ((void (*)(id, SEL, long))objc_msgSend)(app, sel_registerName("setActivationPolicy:"), 1);
}
*/
import "C"

// setAgentMode switches the activation policy to Accessory so the app lives in
// the menu bar without a Dock icon (and without the Dock "bounce" on launch).
func setAgentMode() {
	C.setAgentActivationPolicy()
}
