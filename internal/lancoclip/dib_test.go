package lancoclip

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/png"
	"testing"
)

// makeDIB builds a packed DIB for tests. rows are given top-down; when topDown
// is false they are written bottom-up like a real CF_DIB.
func makeDIB(width, height, bitCount int, topDown bool, rows [][]byte) []byte {
	stride := ((width*bitCount + 31) / 32) * 4
	dib := make([]byte, dibHeaderSize+stride*height)
	binary.LittleEndian.PutUint32(dib[0:], dibHeaderSize)
	binary.LittleEndian.PutUint32(dib[4:], uint32(int32(width)))
	if topDown {
		binary.LittleEndian.PutUint32(dib[8:], uint32(int32(-height)))
	} else {
		binary.LittleEndian.PutUint32(dib[8:], uint32(int32(height)))
	}
	binary.LittleEndian.PutUint16(dib[12:], 1)
	binary.LittleEndian.PutUint16(dib[14:], uint16(bitCount))
	for y := 0; y < height; y++ {
		src := y
		if !topDown {
			src = height - 1 - y
		}
		copy(dib[dibHeaderSize+src*stride:], rows[y])
	}
	return dib
}

func decodePNG(t *testing.T, b []byte) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("png decode: %v", err)
	}
	return img
}

func TestDIBToPNG32BottomUp(t *testing.T) {
	// rows[0] 是图片最上面一行。bottom-up 存储时它被写在 DIB 的最后一行，
	// 解码后必须翻回顶部，否则整张图会上下颠倒。
	top := []byte{1, 2, 3, 0, 4, 5, 6, 0}       // (3,2,1)、(6,5,4)
	bottom := []byte{7, 8, 9, 0, 10, 11, 12, 0} // (9,8,7)、(12,11,10)
	dib := makeDIB(2, 2, 32, false, [][]byte{top, bottom})

	out, err := dibToPNG(dib)
	if err != nil {
		t.Fatalf("dibToPNG: %v", err)
	}
	img := decodePNG(t, out)
	if got := img.Bounds(); got.Dx() != 2 || got.Dy() != 2 {
		t.Fatalf("size = %dx%d, want 2x2", got.Dx(), got.Dy())
	}
	// 左上角 = top 行第一个像素 (3,2,1)，alpha 被补成不透明
	r, g, b, a := img.At(0, 0).RGBA()
	if r>>8 != 3 || g>>8 != 2 || b>>8 != 1 || a>>8 != 255 {
		t.Errorf("pixel(0,0) = (%d,%d,%d,%d), want (3,2,1,255)", r>>8, g>>8, b>>8, a>>8)
	}
	// 左下角 = bottom 行第一个像素 (9,8,7)
	r, g, b, _ = img.At(0, 1).RGBA()
	if r>>8 != 9 || g>>8 != 8 || b>>8 != 7 {
		t.Errorf("pixel(0,1) = (%d,%d,%d), want (9,8,7)", r>>8, g>>8, b>>8)
	}
}

func TestDIBToPNGTopDown(t *testing.T) {
	row0 := []byte{1, 2, 3, 0, 4, 5, 6, 0}
	row1 := []byte{7, 8, 9, 0, 10, 11, 12, 0}
	dib := makeDIB(2, 2, 32, true, [][]byte{row0, row1})

	img := decodePNG(t, mustPNG(t, dib))
	r, g, b, _ := img.At(0, 0).RGBA()
	if r>>8 != 3 || g>>8 != 2 || b>>8 != 1 {
		t.Errorf("top-down pixel(0,0) = (%d,%d,%d), want (3,2,1)", r>>8, g>>8, b>>8)
	}
}

func TestDIBToPNG24Padding(t *testing.T) {
	// 3 像素宽 = 9 字节，stride 补到 12；最后一像素不能读到补位字节
	row := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 0xAA, 0xAA, 0xAA}
	dib := makeDIB(3, 1, 24, true, [][]byte{row})

	img := decodePNG(t, mustPNG(t, dib))
	if got := img.Bounds().Dx(); got != 3 {
		t.Fatalf("width = %d, want 3", got)
	}
	r, g, b, a := img.At(2, 0).RGBA()
	if r>>8 != 9 || g>>8 != 8 || b>>8 != 7 || a>>8 != 255 {
		t.Errorf("pixel(2,0) = (%d,%d,%d,%d), want (9,8,7,255)", r>>8, g>>8, b>>8, a>>8)
	}
}

func TestDIBToPNGKeepsRealAlpha(t *testing.T) {
	// 有像素带非零 alpha 时，按真实 alpha 输出（半透明截图）
	row := []byte{0, 0, 255, 128}
	dib := makeDIB(1, 1, 32, true, [][]byte{row})
	img := decodePNG(t, mustPNG(t, dib))
	if _, _, _, a := img.At(0, 0).RGBA(); a>>8 != 128 {
		t.Errorf("alpha = %d, want 128", a>>8)
	}
}

func TestDIBToPNGRejects(t *testing.T) {
	cases := map[string][]byte{
		"空数据":    nil,
		"头被截断":   make([]byte, 20),
		"8 位索引色": makeDIB(1, 1, 8, true, [][]byte{{0, 0, 0, 0}}),
	}
	for name, dib := range cases {
		if _, err := dibToPNG(dib); err == nil {
			t.Errorf("%s: 期望报错，实际成功", name)
		}
	}
	// 声明 2 行但只给 1 行的数据
	short := makeDIB(2, 2, 32, true, [][]byte{{1, 2, 3, 0, 4, 5, 6, 0}, {0, 0, 0, 0, 0, 0, 0, 0}})
	short = short[:len(short)-8]
	if _, err := dibToPNG(short); err == nil {
		t.Error("截断像素数据：期望报错，实际成功")
	}
}

func TestPNGToDIBRoundTrip(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	src.Pix = []byte{255, 0, 0, 255, 0, 128, 255, 255} // 红、蓝
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatal(err)
	}

	dib, err := pngToDIB(buf.Bytes())
	if err != nil {
		t.Fatalf("pngToDIB: %v", err)
	}
	back := decodePNG(t, mustPNG(t, dib))
	r, g, b, a := back.At(0, 0).RGBA()
	if r>>8 != 255 || g>>8 != 0 || b>>8 != 0 || a>>8 != 255 {
		t.Errorf("round-trip pixel(0,0) = (%d,%d,%d,%d), want (255,0,0,255)", r>>8, g>>8, b>>8, a>>8)
	}
	r, g, b, _ = back.At(1, 0).RGBA()
	if r>>8 != 0 || g>>8 != 128 || b>>8 != 255 {
		t.Errorf("round-trip pixel(1,0) = (%d,%d,%d), want (0,128,255)", r>>8, g>>8, b>>8)
	}
}

func mustPNG(t *testing.T, dib []byte) []byte {
	t.Helper()
	out, err := dibToPNG(dib)
	if err != nil {
		t.Fatalf("dibToPNG: %v", err)
	}
	return out
}
