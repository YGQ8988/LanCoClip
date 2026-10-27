#import <Cocoa/Cocoa.h>

// histpopPicked is a Go function exported via cgo //export.
extern void histpopPicked(int tag);

@interface HistPopTarget : NSObject
@end
@implementation HistPopTarget
- (void)picked:(NSMenuItem *)sender {
    histpopPicked((int)sender.tag);
}
@end

// showHistoryMenu builds a cascading NSMenu: one top-level row per group
// ("1-10", "11-20", …) with that group's entries in a submenu, so the popup
// opens compact and the content shows on hover. `labels` is the flattened
// entry list, `offsets` holds each group's [start, end) range (groupCount+1
// entries). Item tags encode group*1000 + item + 1 for the Go callback.
// Pops up at the current mouse location and blocks until dismissed.
int showHistoryMenu(const char **groupTitles, const char **labels,
                    const int *offsets, int groupCount) {
    if (!groupTitles || !offsets || groupCount <= 0) return -1;

    static HistPopTarget *target = nil;
    static dispatch_once_t once;
    dispatch_once(&once, ^{ target = [HistPopTarget new]; });

    NSMenu *menu = [[NSMenu alloc] initWithTitle:@""];
    for (int g = 0; g < groupCount; g++) {
        NSString *title = [NSString stringWithUTF8String:groupTitles[g]];
        if (!title) title = @"";

        int start = offsets[g];
        int end = offsets[g + 1];

        NSMenuItem *parent = [[NSMenuItem alloc] initWithTitle:title
                                                        action:nil
                                                 keyEquivalent:@""];
        if (start >= end) {
            // 没有条目（空历史提示）：灰掉，不弹子菜单
            parent.enabled = NO;
            [menu addItem:parent];
            continue;
        }

        NSMenu *sub = [[NSMenu alloc] initWithTitle:title];
        for (int i = start; i < end; i++) {
            NSString *label = labels ? [NSString stringWithUTF8String:labels[i]] : @"";
            if (!label) label = @"";
            NSMenuItem *it = [[NSMenuItem alloc] initWithTitle:label
                                                        action:@selector(picked:)
                                                 keyEquivalent:@""];
            it.target = target;
            it.tag = g * 1000 + (i - start) + 1;
            [sub addItem:it];
        }
        [parent setSubmenu:sub];
        [menu addItem:parent];
    }

    // popUpMenuPositioningItem expects bottom-left screen coordinates, which
    // is exactly what mouseLocation returns.
    NSPoint mouse = [NSEvent mouseLocation];
    [menu popUpMenuPositioningItem:nil atLocation:mouse inView:nil];
    return 0;
}
