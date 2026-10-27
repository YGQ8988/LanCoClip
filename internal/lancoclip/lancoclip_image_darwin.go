//go:build darwin && cgo

package lancoclip

/*
#cgo LDFLAGS: -framework Cocoa -framework AppKit
#include <stdlib.h>
// Implemented in lancoclip_image_darwin.m (compiled as Objective-C).
unsigned char *copyLancoclipPNG(int *outLength);
int writeLancoclipPNG(const char *bytes, int length);
*/
import "C"

import (
	"errors"
	"unsafe"
)

// readLancoclipImage returns the PNG bytes of the lancoclip image, or nil when
// the lancoclip holds text (or no image at all).
func readLancoclipImage() []byte {
	var n C.int
	buf := C.copyLancoclipPNG(&n)
	if buf == nil || n <= 0 {
		return nil
	}
	defer C.free(unsafe.Pointer(buf))
	return C.GoBytes(unsafe.Pointer(buf), n)
}

// WriteImage replaces the lancoclip contents with the given PNG.
func WriteImage(png []byte) error {
	if len(png) == 0 {
		return errors.New("empty image")
	}
	var p *C.char
	if len(png) > 0 {
		p = (*C.char)(unsafe.Pointer(&png[0]))
	}
	switch C.writeLancoclipPNG(p, C.int(len(png))) {
	case 0:
		return nil
	case 1:
		return errors.New("empty image")
	default:
		return errors.New("failed to write image to lancoclip")
	}
}
