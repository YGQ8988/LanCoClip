package lancoclip

// DIB (device-independent bitmap) decoding for the Windows lancoclip.
//
// CF_DIB stores a packed BITMAPINFO: a BITMAPINFOHEADER followed by the pixel
// array (palette entries sit between them for indexed formats). Rows are
// bottom-up unless biHeight is negative, and every row is padded to a 4-byte
// boundary. This file is platform independent so the parsing can be unit
// tested on any OS; the Windows-only glue lives in lancoclip_image_windows.go.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/png"
)

const (
	dibHeaderSize = 40 // BITMAPINFOHEADER

	biRGB       = 0
	biBitfields = 3
)

// dibToPNG converts a packed CF_DIB payload into PNG bytes.
//
// Only uncompressed 24/32-bit bitmaps are handled — that is what every Windows
// screenshot tool and image viewer puts on the lancoclip. RLE and indexed
// formats are rejected (the caller simply skips that lancoclip content).
func dibToPNG(dib []byte) ([]byte, error) {
	if len(dib) < dibHeaderSize {
		return nil, errors.New("dib: truncated header")
	}
	headerSize := int(binary.LittleEndian.Uint32(dib[0:4]))
	width := int(int32(binary.LittleEndian.Uint32(dib[4:8])))
	rawHeight := int32(binary.LittleEndian.Uint32(dib[8:12]))
	planes := binary.LittleEndian.Uint16(dib[12:14])
	bitCount := int(binary.LittleEndian.Uint16(dib[14:16]))
	compression := binary.LittleEndian.Uint32(dib[16:20])

	if headerSize < dibHeaderSize || headerSize > len(dib) {
		return nil, errors.New("dib: bad header size")
	}
	if planes != 1 {
		return nil, errors.New("dib: bad plane count")
	}
	if width <= 0 || rawHeight == 0 {
		return nil, errors.New("dib: bad dimensions")
	}
	// A screenshot can legitimately be large, but not unbounded: refuse anything
	// that would allocate absurd amounts of memory from a corrupt header.
	if width > 20000 || rawHeight > 20000 || rawHeight < -20000 {
		return nil, errors.New("dib: dimensions too large")
	}
	if bitCount != 24 && bitCount != 32 {
		return nil, errors.New("dib: unsupported bit depth")
	}
	if compression != biRGB && compression != biBitfields {
		return nil, errors.New("dib: unsupported compression")
	}

	topDown := rawHeight < 0
	height := int(rawHeight)
	if topDown {
		height = -height
	}

	offset := headerSize
	// BITMAPINFOHEADER + BI_BITFIELDS stores three colour masks right after the
	// header; a BITMAPV4/V5 header already includes them in headerSize.
	if compression == biBitfields && headerSize == dibHeaderSize {
		offset += 12
	}
	stride := ((width*bitCount + 31) / 32) * 4
	if offset+stride*height > len(dib) {
		return nil, errors.New("dib: truncated pixel data")
	}

	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	// 32bpp CF_DIB is usually XRGB: the fourth byte is reserved and every
	// application fills it differently (0x00, 0xFF or garbage). Treat the whole
	// bitmap as opaque unless at least one pixel actually carries alpha, which is
	// what a transparent screenshot would look like.
	anyAlpha := false
	for y := 0; y < height; y++ {
		srcRow := y
		if !topDown {
			srcRow = height - 1 - y
		}
		row := dib[offset+srcRow*stride:]
		for x := 0; x < width; x++ {
			var r, g, b, a byte
			if bitCount == 32 {
				p := row[x*4:]
				b, g, r, a = p[0], p[1], p[2], p[3]
				if a != 0 {
					anyAlpha = true
				}
			} else {
				p := row[x*3:]
				b, g, r, a = p[0], p[1], p[2], 0xff
			}
			i := img.PixOffset(x, y)
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = r, g, b, a
		}
	}
	if !anyAlpha {
		for i := 3; i < len(img.Pix); i += 4 {
			img.Pix[i] = 0xff
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// pngToDIB converts PNG bytes into a 32bpp top-down BITMAPINFOHEADER + pixels
// payload, the layout SetClipboardData expects under CF_DIB. It is the inverse
// of dibToPNG and is used when a history image is copied back to the lancoclip.
func pngToDIB(pngBytes []byte) ([]byte, error) {
	if len(pngBytes) == 0 {
		return nil, errors.New("empty image")
	}
	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil, errors.New("empty image")
	}

	stride := w * 4
	dib := make([]byte, dibHeaderSize+stride*h)
	binary.LittleEndian.PutUint32(dib[0:], dibHeaderSize)
	binary.LittleEndian.PutUint32(dib[4:], uint32(int32(w)))
	// Negative height marks a top-down bitmap, so rows need no flipping.
	binary.LittleEndian.PutUint32(dib[8:], uint32(int32(-h)))
	binary.LittleEndian.PutUint16(dib[12:], 1)
	binary.LittleEndian.PutUint16(dib[14:], 32)
	binary.LittleEndian.PutUint32(dib[16:], biRGB)
	binary.LittleEndian.PutUint32(dib[20:], uint32(stride*h))

	for y := 0; y < h; y++ {
		row := dib[dibHeaderSize+y*stride:]
		for x := 0; x < w; x++ {
			// NRGBA keeps the channels un-premultiplied, which is what the
			// lancoclip expects; RGBA() would hand back premultiplied values.
			c := color.NRGBAModel.Convert(img.At(b.Min.X+x, b.Min.Y+y)).(color.NRGBA)
			row[x*4], row[x*4+1], row[x*4+2], row[x*4+3] = c.B, c.G, c.R, c.A
		}
	}
	return dib, nil
}
