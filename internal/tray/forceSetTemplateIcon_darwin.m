// forceSetTemplateIcon_darwin.m — compiled by cgo as Objective-C.
// Bypasses systray's internal icon path so the menu-bar icon renders reliably
// on macOS 11-26 (macOS 26 Tahoe has a status-item teardown race).
#import <Cocoa/Cocoa.h>
#import <objc/runtime.h>

void forceSetTemplateIcon(const char *bytes, int length) {
    @autoreleasepool {
        // 1) Build an NSImage from the raw PNG bytes.
        NSData *data = [NSData dataWithBytesNoCopy:(void *)bytes length:length freeWhenDone:NO];
        NSImage *image = [[NSImage alloc] initWithData:data];
        if (!image) return;

        // 2) Logical size 18pt + template rendering (auto light/dark). The
        //    artwork (black squircle with a knocked-out lancoclip) is a true
        //    silhouette: only its alpha matters, which is what templates use.
        [image setSize:NSMakeSize(18, 18)];
        [image setTemplate:YES];

        // 3) Reach systray's AppDelegate -> statusItem through KVC.
        id delegate = [[NSApplication sharedApplication] delegate];
        if (!delegate) return;
        NSStatusItem *statusItem = [delegate valueForKey:@"statusItem"];
        if (!statusItem) return;

        // 4) Assign the image to the button.
        [statusItem.button setImage:image];
        [statusItem.button setImagePosition:NSImageOnly];
    }
}
