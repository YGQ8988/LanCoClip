#import <AppKit/AppKit.h>
#import <ApplicationServices/ApplicationServices.h>

// pasteTrusted reports whether the process may post synthetic keyboard
// events. When prompt is YES, macOS shows the Accessibility consent dialog
// for this app the first time.
int pasteTrusted(int prompt) {
    if (prompt) {
        NSDictionary *opts = @{ (__bridge id)kAXTrustedCheckOptionPrompt : @YES };
        return AXIsProcessTrustedWithOptions((CFDictionaryRef)opts) ? 1 : 0;
    }
    return AXIsProcessTrusted() ? 1 : 0;
}

// clearStuckModifiers posts keyUp events (with real modifier keycodes) for
// Shift/Option/Cmd/Control. Posting a flags-changed with keycode 0 does NOT
// update the window server's modifier state; only real keycodes do. Without
// this, a stuck Shift from the user's hotkey combo (e.g. Cmd+Shift+V) can
// leak into the synthetic Cmd+V below.
static void clearStuckModifiers(CGEventSourceRef src) {
    const CGKeyCode mods[4] = { (CGKeyCode)56, (CGKeyCode)58, (CGKeyCode)55, (CGKeyCode)59 }; // shift, option, cmd, control
    for (int i = 0; i < 4; i++) {
        CGEventRef up = CGEventCreateKeyboardEvent(src, mods[i], NO);
        if (!up) continue;
        CGEventSetType(up, kCGEventFlagsChanged);
        CGEventSetFlags(up, 0);
        CGEventPost(kCGHIDEventTap, up);
        CFRelease(up);
    }
}

// pastePostCmdV sends Cmd+V (kVK_ANSI_V = 9) to the app that currently has
// keyboard focus. It prefers CGEventPostToPid: the keystroke is delivered
// straight to the target process, never passing through the system-wide
// hotkey evaluation — so even if stale modifier flags linger, it cannot
// re-trigger our own Cmd+Shift+V history hotkey. Falls back to the HID tap
// when no frontmost app can be determined.
void pastePostCmdV(void) {
    pid_t pid = 0;
    @try {
        NSRunningApplication *front = [[NSWorkspace sharedWorkspace] frontmostApplication];
        if (front) pid = front.processIdentifier;
    } @catch (NSException *e) { pid = 0; }

    CGEventSourceRef src = CGEventSourceCreate(kCGEventSourceStateHIDSystemState);
    if (!src) return;

    clearStuckModifiers(src);

    CGEventRef down = CGEventCreateKeyboardEvent(src, (CGKeyCode)9, YES);
    CGEventRef up = CGEventCreateKeyboardEvent(src, (CGKeyCode)9, NO);
    if (down) CGEventSetFlags(down, kCGEventFlagMaskCommand);
    if (up) CGEventSetFlags(up, kCGEventFlagMaskCommand);

    if (pid > 0) {
        if (down) CGEventPostToPid(pid, down);
        if (up) CGEventPostToPid(pid, up);
    } else {
        if (down) CGEventPost(kCGHIDEventTap, down);
        if (up) CGEventPost(kCGHIDEventTap, up);
    }

    if (down) CFRelease(down);
    if (up) CFRelease(up);
    if (src) CFRelease(src);
}
