// lancoclip_native_darwin.m — native lancoclip access used by the lancoclipWatcher.
// Read-only paths only: writing still goes through lancoclip_image_darwin.m.
#import <Cocoa/Cocoa.h>
#include <stdlib.h>
#include <string.h>

// copyLancoclipChangeCount returns the lancoclip change counter. Every write
// to the lancoclip bumps it, so comparing two values tells us whether anything
// changed without copying the contents — a property read, not an IPC round trip.
long copyLancoclipChangeCount(void) {
    @autoreleasepool {
        return (long)[[NSPasteboard generalPasteboard] changeCount];
    }
}

// copyLancoclipText copies the lancoclip's plain-text content into a malloc'd
// NUL-terminated UTF-8 buffer. Returns NULL when the lancoclip holds no text.
// The caller owns the buffer and must free() it.
char *copyLancoclipText(void) {
    @autoreleasepool {
        NSPasteboard *pb = [NSPasteboard generalPasteboard];
        if (![[pb types] containsObject:NSPasteboardTypeString]) {
            return NULL;
        }
        NSString *text = [pb stringForType:NSPasteboardTypeString];
        if (text == nil) {
            return NULL;
        }
        // UTF8String returns autoreleased memory that is only valid until the
        // pool drains, so copy it out right away.
        const char *utf8 = [text UTF8String];
        if (utf8 == NULL) {
            return NULL;
        }
        size_t n = strlen(utf8);
        char *buf = (char *)malloc(n + 1);
        if (buf == NULL) {
            return NULL;
        }
        memcpy(buf, utf8, n + 1);
        return buf;
    }
}
