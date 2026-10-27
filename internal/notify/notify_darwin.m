// notify_darwin.m — compiled by cgo as Objective-C.
//
// Delivers the "ready" notification through UNUserNotificationCenter (the only
// supported API since macOS 11; NSUserNotification is deprecated and silently
// dropped on recent releases) and forwards clicks back into Go.
#import <Cocoa/Cocoa.h>
#import <UserNotifications/UserNotifications.h>

// Implemented in Go: internal/notify/notify_darwin.go (//export goNotifyClicked).
extern void goNotifyClicked(void);

@interface LCPNotificationDelegate : NSObject <UNUserNotificationCenterDelegate>
@end

static LCPNotificationDelegate *gDelegate = nil;

@implementation LCPNotificationDelegate

// Show the banner even while the app is frontmost (a menu-bar app rarely is,
// but a fresh double-click launch briefly activates it).
- (void)userNotificationCenter:(UNUserNotificationCenter *)center
       willPresentNotification:(UNNotification *)notification
         withCompletionHandler:(void (^)(UNNotificationPresentationOptions))completionHandler {
    if (@available(macOS 11.0, *)) {
        completionHandler(UNNotificationPresentationOptionBanner |
                          UNNotificationPresentationOptionList |
                          UNNotificationPresentationOptionSound);
    } else {
        // macOS 10.x 只有旧的 alert 选项，常量在 11.0 起被标记废弃
#pragma clang diagnostic push
#pragma clang diagnostic ignored "-Wdeprecated-declarations"
        completionHandler(UNNotificationPresentationOptionAlert |
                          UNNotificationPresentationOptionSound);
#pragma clang diagnostic pop
    }
}

- (void)userNotificationCenter:(UNUserNotificationCenter *)center
 didReceiveNotificationResponse:(UNNotificationResponse *)response
          withCompletionHandler:(void (^)(void))completionHandler {
    goNotifyClicked();
    completionHandler();
}

@end

bool lcpNotifyAvailable(void) {
    // 没有 app bundle 的进程（终端里直接跑二进制）调用 UNUserNotificationCenter
    // 会因为 bundleProxy 为 nil 抛出 Obj-C 异常直接崩掉，这里先挡掉。
    return [[NSBundle mainBundle] bundleIdentifier] != nil;
}

int lcpNotify(const char *title, const char *body) {
    if (!lcpNotifyAvailable()) {
        return -1;
    }
    @autoreleasepool {
        UNUserNotificationCenter *center = [UNUserNotificationCenter currentNotificationCenter];
        if (gDelegate == nil) {
            gDelegate = [[LCPNotificationDelegate alloc] init];
        }
        center.delegate = gDelegate;

        // ⚠️ 必须在这里（同步）把 C 字符串转成 NSString：title/body 指向的内存
        // 由 Go 侧在 lcpNotify 返回后立即释放，而下面的授权回调是异步执行的，
        // 若在 block 里才转字符串，读到的是已释放内存 → 标题/内容变成乱码
        //（实际复现：标题成了堆里残留的「剪贴板」，正文是一串乱码）。
        // NSString 会被 block 自动 retain，生命周期安全。
        NSString *t = title ? [NSString stringWithUTF8String:title] : @"";
        NSString *b = body ? [NSString stringWithUTF8String:body] : @"";
        if (!t) t = @"";
        if (!b) b = @"";

        UNAuthorizationOptions opts = UNAuthorizationOptionAlert | UNAuthorizationOptionSound;
        [center requestAuthorizationWithOptions:opts
                              completionHandler:^(BOOL granted, NSError *error) {
            if (!granted) {
                NSLog(@"LanCoClip: notification permission denied (%@)", error);
                return;
            }
            UNMutableNotificationContent *content = [[UNMutableNotificationContent alloc] init];
            content.title = t;
            content.body = b;
            content.sound = [UNNotificationSound defaultSound];

            // trigger:nil 表示立即投递
            UNNotificationRequest *req =
                [UNNotificationRequest requestWithIdentifier:@"lanoclip-ready"
                                                     content:content
                                                     trigger:nil];
            [center addNotificationRequest:req
                     withCompletionHandler:^(NSError *e) {
                if (e) {
                    NSLog(@"LanCoClip: add notification failed: %@", e);
                }
            }];
        }];
    }
    return 0;
}
