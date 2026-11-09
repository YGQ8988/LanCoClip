#import <Carbon/Carbon.h>

// lancoclipHotkeyFired / hotkeyReapplyDone are Go functions exported via cgo
// //export; declared extern here so the Carbon handler can call straight
// into Go.
extern void lancoclipHotkeyFired(int id);
extern void hotkeyReapplyDone(int token, int status);
extern int hotkeyReapplyValid(int token);

static EventHotKeyRef gHotkeyQR = NULL;
static EventHotKeyRef gHotkeyHistory = NULL;
static int gIDQR = 1;
static int gIDHistory = 2;
static BOOL gHandlerInstalled = NO;
// 当前实际生效的组合：reapply 失败时用来恢复，避免两个快捷键全部失效。
static int gKeyQR = 0, gModsQR = 0;
static int gKeyHistory = 0, gModsHistory = 0;

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
        gHotkeyHistory = NULL;
        return st;
    }
    // 只有两个键都注册成功，才把它记为「当前生效组合」。
    gKeyQR = keyQR; gModsQR = modsQR;
    gKeyHistory = keyHistory; gModsHistory = modsHistory;
    return noErr;
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
// hotkeyReapplyDone, tagged with the caller's token so a late callback from a
// timed-out Update cannot be mistaken for the next transaction's result.
// Called from arbitrary Go goroutines.
//
// 失败时恢复旧组合：原来的顺序是「先注销旧键、再注册新键」，第二个新键
// 注册失败时旧键已经没了、新键也只注册了一半 → 两个全局快捷键全部失效
// 直到重启。现在记住旧组合，失败后重新注册回去（best-effort）。
void reapplyHotkeysOnMain(int token, int idQR, int keyQR, int modsQR, int idHistory, int keyHistory, int modsHistory) {
    void (^work)(void) = ^{
        // 事务可能已经超时放弃：此时不能再改实际生效的组合，否则用户
        // 看到「失败」，快捷键却已经被换掉了。
        if (!hotkeyReapplyValid(token)) return;

        int oldKeyQR = gKeyQR, oldModsQR = gModsQR;
        int oldKeyH = gKeyHistory, oldModsH = gModsHistory;
        BOOL hadOld = (gHotkeyQR != NULL || gHotkeyHistory != NULL);

        unregisterAll();
        OSStatus st = registerPair(idQR, keyQR, modsQR, idHistory, keyHistory, modsHistory);
        if (st != noErr && hadOld) {
            // 新组合注册失败：清掉 registerPair 的部分注册残留（幂等），
            // 再把之前生效的组合重新注册回去。
            unregisterAll();
            registerPair(gIDQR, oldKeyQR, oldModsQR, gIDHistory, oldKeyH, oldModsH);
        }
        // 在 block 内回传：异步派发时函数早已返回，栈上的结果变量不可用。
        // Go 侧按 token 过滤，超时后到达的旧结果会被丢弃。
        hotkeyReapplyDone(token, (int)st);
    };
    if ([NSThread isMainThread]) {
        work();
    } else {
        // 用 dispatch_async 而不是 dispatch_sync：主线程可能正被模态菜单
        // 占用，同步等待会把调用 hotkey.Update 的 HTTP goroutine 一起卡死。
        // Go 侧有 3s 超时兜底，这里只需要尽快把工作排进主队列。
        dispatch_async(dispatch_get_main_queue(), work);
    }
}
