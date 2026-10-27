// lancoclip_image_darwin.m — reads the lancoclip image as PNG.
// Compiled by cgo as Objective-C (see lancoclip_image_darwin.go).
#import <Cocoa/Cocoa.h>
#include <stdlib.h>
#include <string.h>

// copyLancoclipPNG copies PNG bytes of the current lancoclip image into a
// malloc'd buffer and sets *outLength. It returns NULL when there is no image
// OR when the lancoclip also holds non-empty plain text — text wins, because
// some apps attach a TIFF rendering of the text they copy.
// The caller owns the returned buffer and must free() it.
unsigned char *copyLancoclipPNG(int *outLength) {
    @autoreleasepool {
        *outLength = 0;

        NSPasteboard *pb = [NSPasteboard generalPasteboard];
        NSArray *types = [pb types];

        if ([types containsObject:NSPasteboardTypeString]) {
            NSString *text = [pb stringForType:NSPasteboardTypeString];
            if (text != nil && text.length > 0) {
                return NULL;
            }
        }

        NSData *png = nil;
        if ([types containsObject:NSPasteboardTypePNG]) {
            png = [pb dataForType:NSPasteboardTypePNG];
        }
        if ((png == nil || png.length == 0) && [types containsObject:NSPasteboardTypeTIFF]) {
            NSData *tiff = [pb dataForType:NSPasteboardTypeTIFF];
            if (tiff.length > 0) {
                NSBitmapImageRep *rep = [NSBitmapImageRep imageRepWithData:tiff];
                if (rep != nil) {
                    png = [rep representationUsingType:NSBitmapImageFileTypePNG properties:@{}];
                }
            }
        }
        if (png == nil || png.length == 0) {
            return NULL;
        }

        unsigned char *buf = (unsigned char *)malloc(png.length);
        if (buf == NULL) {
            return NULL;
        }
        memcpy(buf, png.bytes, png.length);
        *outLength = (int)png.length;
        return buf;
    }
}

// writeLancoclipPNGImpl replaces the lancoclip contents with the given PNG
// image. Writes BOTH TIFF and PNG types: writeObjects(NSBitmapImageRep) only
// provides TIFF, and several apps (WeChat, VS Code, cross-platform apps…)
// paste images only when PNG is available. Returns 0 on success, 1 when the
// data is empty, 2 on failure.
static int writeLancoclipPNGImpl(const char *bytes, int length) {
    @autoreleasepool {
        if (bytes == NULL || length <= 0) {
            return 1;
        }
        NSData *png = [NSData dataWithBytes:bytes length:length];
        NSBitmapImageRep *rep = [NSBitmapImageRep imageRepWithData:png];
        if (rep == nil) {
            return 2;
        }

        NSPasteboard *pb = [NSPasteboard generalPasteboard];
        [pb clearContents];
        if (![pb setData:rep.TIFFRepresentation forType:NSPasteboardTypeTIFF]) {
            return 2;
        }
        // PNG 类型给只认 PNG 的应用；失败不致命（TIFF 已在剪贴板上）
        [pb setData:png forType:NSPasteboardTypePNG];
        return 0;
    }
}

// writeLancoclipPNG runs the write on the main thread. NSPasteboard is meant
// to be used from the main thread, and CopyItem can be called from tray click
// goroutines (background threads). Dispatches synchronously when off-main.
int writeLancoclipPNG(const char *bytes, int length) {
    if ([NSThread isMainThread]) {
        return writeLancoclipPNGImpl(bytes, length);
    }
    __block int ret = 2;
    dispatch_sync(dispatch_get_main_queue(), ^{
        ret = writeLancoclipPNGImpl(bytes, length);
    });
    return ret;
}
