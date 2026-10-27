#import <Carbon/Carbon.h>

// lancoclipHotkeyFired / hotkeyReapplyDone are Go functions exported via cgo
// //export; declared extern here so the Carbon handler can call straight
// into Go.
extern void lancoclipHotkeyFired(int id);
extern void hotkeyReapplyDone(int status);

static EventHotKeyRef gHotkeyQR = NULL;
static EventHotKeyRef gHotkeyHistory = NULL;
static int gIDQR = 1;
static int gIDHistory = 2;
static BOOL gHandlerInstalled = NO;

static OSStatus hotkeyHandler(EventHandlerCallRef caller, EventRef event, void *userData) {
    if (GetEventClass(event) != kEventClassKeyboard) return noErr;
    // 只响应按下（Pressed）。RegisterEventHotKey 同时会派发松开（Released）
    // 事件，这里若不拦掉，回调会被触发两次——表现为二维码快捷键一次打开
    // 两个浏览器页签。
    if (GetEventKind(event) != kEventHotKeyPressed) return noErr;

    EventHotKeyID hkID;
    OSStatus st = GetEventParameter(event, kEventParamDirectObject, typeEventHotKeyID,
                                    NULL, sizeof(hkID), NULL, &hkID);
    if (st != noErr) return noErr;
    if (hkID.id == (UInt32)gIDQR) lancoclipHotkeyFired(gIDQR);
    else if (hkID.id == (UInt32)gIDHistory) lancoclipHotkeyFired(gIDHistory);
    return noErr;
}

static void ensureHandler(void) {
    if (gHandlerInstalled) return;
    EventTypeSpec types[1] = {
        { kEventClassKeyboard, kEventHotKeyPressed },
    };
    EventHandlerUPP upp = NewEventHandlerUPP(hotkeyHandler);
    if (InstallEventHandler(GetApplicationEventTarget(), upp, 1, types, NULL, NULL) == noErr) {
        gHandlerInstalled = YES;
    }
}

static void unregisterAll(void) {
    if (gHotkeyQR) { UnregisterEventHotKey(gHotkeyQR); gHotkeyQR = NULL; }
    if (gHotkeyHistory) { UnregisterEventHotKey(gHotkeyHistory); gHotkeyHistory = NULL; }
}

// registerPair registers both hotkeys; must run on the main thread. On a
// partial failure the already-registered key is rolled back.
static OSStatus registerPair(int idQR, int keyQR, int modsQR, int idHistory, int keyHistory, int modsHistory) {
    gIDQR = idQR;
    gIDHistory = idHistory;

    EventHotKeyID idQ = { 0, (UInt32)idQR };
    OSStatus st = RegisterEventHotKey((UInt32)keyQR, (UInt32)modsQR, idQ,
                                      GetApplicationEventTarget(), 0, &gHotkeyQR);
    if (st != noErr) return st;

    EventHotKeyID idH = { 0, (UInt32)idHistory };
    st = RegisterEventHotKey((UInt32)keyHistory, (UInt32)modsHistory, idH,
                             GetApplicationEventTarget(), 0, &gHotkeyHistory);
    if (st != noErr) {
        UnregisterEventHotKey(gHotkeyQR);
        gHotkeyQR = NULL;
    }
    return st;
}

// installHotkeys registers both hotkeys and installs the shared handler.
// Returns non-zero on failure.
OSStatus installHotkeys(int idQR, int keyQR, int modsQR, int idHistory, int keyHistory, int modsHistory) {
    ensureHandler();
    unregisterAll();
    return registerPair(idQR, keyQR, modsQR, idHistory, keyHistory, modsHistory);
}

// reapplyHotkeysOnMain re-registers both hotkeys on the main thread (Carbon
// registration is main-thread bound). Reports the result back into Go via
// hotkeyReapplyDone. Called from arbitrary Go goroutines.
void reapplyHotkeysOnMain(int idQR, int keyQR, int modsQR, int idHistory, int keyHistory, int modsHistory) {
    __block OSStatus st = noErr;
    void (^work)(void) = ^{
        unregisterAll();
        st = registerPair(idQR, keyQR, modsQR, idHistory, keyHistory, modsHistory);
    };
    if ([NSThread isMainThread]) {
        work();
    } else {
        // The main thread runs the Cocoa runloop (systray), which services
        // the main queue between events, so this completes promptly.
        dispatch_sync(dispatch_get_main_queue(), work);
    }
    hotkeyReapplyDone((int)st);
}
