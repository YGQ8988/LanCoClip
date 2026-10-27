// Command genicons draws the menu-bar template icon, the app icon, the browser
// favicon and the Windows tray icon (.ico).
//
// It is intentionally dependency-free (stdlib only) and deterministic, so the
// build script can regenerate the assets at any time.
//
// Usage (from the project root):
//
//	go run ./tools/genicons
//	go run ./tools/genicons -from <src.jpg|src.png>
//
// Without -from everything is drawn procedurally. With -from the given artwork
// becomes the app icon source: near-white background is flooded to transparent
// from the edges, a geometric rounded-rect mask re-derives the boundary alpha
// (kills the white JPEG halo), and the artwork is composed onto the 1024
// canvas (89% visual tile) — favicon, Windows tray .ico, exe .ico and the
// colorful macOS menu-bar icon are all derived from it.
//
// Outputs:
//
//	internal/tray/menuicon.png      44x44 menu-bar icon (black template when
//	                                drawn, colorful when derived from artwork)
//	internal/tray/menuicon_win.ico  multi-size Windows tray icon
//	assets/appicon.png              1024x1024 app icon source (→ .icns via build-mac.sh)
//	web/favicon.png                 64x64 browser tab icon
//	winres/lanoclip.ico             multi-size exe icon (packed by go-winres)
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	_ "image/jpeg" // 注册 jpeg 解码器，供 image.Decode 使用
	"image/color"
	"image/png"
	"os"
	"path/filepath"
)

// ss is the supersample factor used for anti-aliasing.
const ss = 4

// rect is a rounded rectangle in normalized coordinates (0..1).
type rect struct{ x0, y0, x1, y1, r float64 }

func (rc rect) contains(x, y float64) bool {
	if x < rc.x0 || x > rc.x1 || y < rc.y0 || y > rc.y1 {
		return false
	}
	cx := clamp(x, rc.x0+rc.r, rc.x1-rc.r)
	cy := clamp(y, rc.y0+rc.r, rc.y1-rc.r)
	dx, dy := x-cx, y-cy
	return dx*dx+dy*dy <= rc.r*rc.r
}

func clamp(v, lo, hi float64) float64 {
	if lo > hi {
		return (lo + hi) / 2
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// 剪贴板造型（实心剪影）：底板 + 顶部夹子连成一体。
// 实心比细描边在小尺寸（菜单栏 22pt）下更清晰，不会淡成一根细线。
var (
	board = rect{0.245, 0.200, 0.755, 0.885, 0.085}
	clip  = rect{0.415, 0.085, 0.585, 0.300, 0.045}

	// 底板上的三条文字行（挖空）
	lines = []rect{
		{0.335, 0.400, 0.665, 0.452, 0.022},
		{0.335, 0.520, 0.665, 0.572, 0.022},
		{0.335, 0.640, 0.555, 0.692, 0.022},
	}
)

var (
	white    = color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	black    = color.NRGBA{R: 0, G: 0, B: 0, A: 0xff}
	gradTop  = color.NRGBA{R: 0x5b, G: 0x93, B: 0xff, A: 0xff}
	gradBot  = color.NRGBA{R: 0x1d, G: 0x4e, B: 0xd8, A: 0xff}
	transpar = color.NRGBA{}
)

// tileMargin is the transparent border around the rounded app tile.
const tileMargin = 0.055

func tileRect(margin float64) rect {
	return rect{margin, margin, 1 - margin, 1 - margin, 0.225 * (1 - 2*margin)}
}

func tileGradient(ny, margin float64) color.NRGBA {
	t := clamp((ny-margin)/(1-2*margin), 0, 1)
	mix := func(a, b uint8) uint8 { return uint8(float64(a) + (float64(b)-float64(a))*t) }
	return color.NRGBA{R: mix(gradTop.R, gradBot.R), G: mix(gradTop.G, gradBot.G), B: mix(gradTop.B, gradBot.B), A: 0xff}
}

// markColorAt renders the rounded gradient tile with a white lancoclip.
// margin shrinks the tile padding (used to fill the small Windows tray icon);
// withLines=false drops the text lines, which turn to mush below ~24px.
func markColorAt(nx, ny, margin float64, withLines bool) color.NRGBA {
	if !tileRect(margin).contains(nx, ny) {
		return transpar
	}
	g := tileGradient(ny, margin)

	if board.contains(nx, ny) || clip.contains(nx, ny) {
		if withLines {
			for _, l := range lines {
				if l.contains(nx, ny) {
					return g // 文字行挖空
				}
			}
		}
		return white
	}
	return g
}

// appColorAt renders the app/favicon mark.
func appColorAt(nx, ny float64) color.NRGBA { return markColorAt(nx, ny, tileMargin, true) }

// menuColorAt renders the menu-bar template icon: a solid black lancoclip.
// Template images only use the alpha channel, so cut-outs must be transparent.
func menuColorAt(nx, ny float64) color.NRGBA {
	if !board.contains(nx, ny) && !clip.contains(nx, ny) {
		return transpar
	}
	for _, l := range lines {
		if l.contains(nx, ny) {
			return transpar
		}
	}
	return black
}

// render supersamples fn and box-averages the result down to size x size.
func render(size int, fn func(float64, float64) color.NRGBA) *image.NRGBA {
	big := image.NewNRGBA(image.Rect(0, 0, size*ss, size*ss))
	for y := 0; y < size*ss; y++ {
		for x := 0; x < size*ss; x++ {
			nx := (float64(x) + 0.5) / float64(size*ss)
			ny := (float64(y) + 0.5) / float64(size*ss)
			big.SetNRGBA(x, y, fn(nx, ny))
		}
	}

	out := image.NewNRGBA(image.Rect(0, 0, size, size))
	total := float64(ss * ss)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var r, g, b, a float64
			for dy := 0; dy < ss; dy++ {
				for dx := 0; dx < ss; dx++ {
					c := big.NRGBAAt(x*ss+dx, y*ss+dy)
					af := float64(c.A) / 255
					r += float64(c.R) * af
					g += float64(c.G) * af
					b += float64(c.B) * af
					a += af
				}
			}
			if a == 0 {
				out.SetNRGBA(x, y, transpar)
				continue
			}
			out.SetNRGBA(x, y, color.NRGBA{
				R: uint8(r / a),
				G: uint8(g / a),
				B: uint8(b / a),
				A: uint8(a / total * 255),
			})
		}
	}
	return out
}

// ── ICO 编码 ──────────────────────────────────────────────────
// Windows 的托盘图标必须是 .ico（systray 在 Windows 上按图标文件加载）。
// 这里输出最通用的 BMP 版式（32 位 BGRA + AND 掩码），兼容性最好。

func encodeICO(images []*image.NRGBA) []byte {
	payloads := make([][]byte, len(images))
	for i, img := range images {
		payloads[i] = encodeBMPPayload(img)
	}

	var buf bytes.Buffer
	write := func(v any) { _ = binary.Write(&buf, binary.LittleEndian, v) }

	// ICONDIR
	write(uint16(0)) // reserved
	write(uint16(1)) // type: icon
	write(uint16(len(images)))

	offset := 6 + 16*len(images)
	for i, img := range images {
		w, h := img.Bounds().Dx(), img.Bounds().Dy()
		buf.WriteByte(byte(w % 256)) // 256 记为 0
		buf.WriteByte(byte(h % 256))
		buf.WriteByte(0)  // palette size
		buf.WriteByte(0)  // reserved
		write(uint16(1))  // color planes
		write(uint16(32)) // bits per pixel
		write(uint32(len(payloads[i])))
		write(uint32(offset))
		offset += len(payloads[i])
	}
	for _, p := range payloads {
		buf.Write(p)
	}
	return buf.Bytes()
}

func encodeBMPPayload(img *image.NRGBA) []byte {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	var buf bytes.Buffer
	write := func(v any) { _ = binary.Write(&buf, binary.LittleEndian, v) }

	// BITMAPINFOHEADER：高度写两倍（XOR 位图 + AND 掩码）
	write(uint32(40))
	write(int32(w))
	write(int32(h * 2))
	write(uint16(1))
	write(uint16(32))
	write(uint32(0)) // BI_RGB
	write(uint32(w * h * 4))
	write(int32(0))
	write(int32(0))
	write(uint32(0))
	write(uint32(0))

	// XOR 位图：自下而上、BGRA
	for y := h - 1; y >= 0; y-- {
		for x := 0; x < w; x++ {
			c := img.NRGBAAt(x, y)
			buf.Write([]byte{c.B, c.G, c.R, c.A})
		}
	}

	// AND 掩码：1bpp，行按 4 字节对齐，自下而上；1 = 透明
	stride := ((w + 31) / 32) * 4
	mask := make([]byte, stride)
	for y := h - 1; y >= 0; y-- {
		for i := range mask {
			mask[i] = 0
		}
		for x := 0; x < w; x++ {
			if img.NRGBAAt(x, y).A == 0 {
				mask[x/8] |= 0x80 >> uint(x%8)
			}
		}
		buf.Write(mask)
	}
	return buf.Bytes()
}

func writePNG(path string, img image.Image) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func main() {
	// -from 模式：以外部图片作为 app 图标源（见包注释）
	if len(os.Args) == 3 && os.Args[1] == "-from" {
		if err := runFrom(os.Args[2]); err != nil {
			fmt.Fprintf(os.Stderr, "genicons: %v\n", err)
			os.Exit(1)
		}
		return
	}

	pngTargets := []struct {
		path string
		size int
		fn   func(float64, float64) color.NRGBA
	}{
		{"internal/tray/menuicon.png", 44, menuColorAt},
		{"assets/appicon.png", 1024, appColorAt},
		{"web/favicon.png", 64, appColorAt},
		{"web/appicon.png", 256, appColorAt},
	}
	for _, t := range pngTargets {
		if err := writePNG(t.path, render(t.size, t.fn)); err != nil {
			fmt.Fprintf(os.Stderr, "genicons: write %s: %v\n", t.path, err)
			os.Exit(1)
		}
		fmt.Printf("  generated %s (%dx%d)\n", t.path, t.size, t.size)
	}

	// Windows 托盘图标：多尺寸，小尺寸不放文字行（太糊），并缩小留白让图形更饱满
	sizes := []int{16, 24, 32, 48}
	imgs := make([]*image.NRGBA, len(sizes))
	for i, s := range sizes {
		margin := 0.02
		imgs[i] = render(s, func(nx, ny float64) color.NRGBA {
			return markColorAt(nx, ny, margin, s >= 32)
		})
	}
	icoPath := "internal/tray/menuicon_win.ico"
	if err := writeFile(icoPath, encodeICO(imgs)); err != nil {
		fmt.Fprintf(os.Stderr, "genicons: write %s: %v\n", icoPath, err)
		os.Exit(1)
	}
	fmt.Printf("  generated %s (%v)\n", icoPath, sizes)

	// exe 图标（资源管理器/任务栏）：尺寸更全，交由 go-winres 打进 .syso
	exeSizes := []int{16, 24, 32, 48, 64, 128, 256}
	exeImgs := make([]*image.NRGBA, len(exeSizes))
	for i, s := range exeSizes {
		exeImgs[i] = render(s, appColorAt)
	}
	exeIco := "winres/lanoclip.ico"
	if err := writeFile(exeIco, encodeICO(exeImgs)); err != nil {
		fmt.Fprintf(os.Stderr, "genicons: write %s: %v\n", exeIco, err)
		os.Exit(1)
	}
	fmt.Printf("  generated %s (%v)\n", exeIco, exeSizes)
}

// ── -from 模式：从外部图片派生图标 ────────────────────────────

// appTilePx 与程序化绘制的视觉占比一致：1024 × (1 - 2×0.055)。
const appTilePx = 911

// toNRGBA 把任意解码后的图片转成可逐像素修改的 NRGBA。
func toNRGBA(img image.Image) *image.NRGBA {
	b := img.Bounds()
	out := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			out.Set(x, y, img.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return out
}

// floodWhiteBorder 从画布四边泛洪：把与边缘连通的近白像素全部变透明。
// 图标内部的白色（纸张、设备高光）被彩色包裹、不与边缘连通，得以保留。
func floodWhiteBorder(img *image.NRGBA) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	nearWhite := func(c color.NRGBA) bool { return c.R >= 240 && c.G >= 240 && c.B >= 240 }
	visited := make([]bool, w*h)
	stack := make([]int, 0, w*h/4)
	seed := func(x, y int) {
		c := img.NRGBAAt(b.Min.X+x, b.Min.Y+y)
		i := y*w + x
		if !visited[i] && c.A != 0 && nearWhite(c) {
			visited[i] = true
			stack = append(stack, i)
		}
	}
	for x := 0; x < w; x++ {
		seed(x, 0)
		seed(x, h-1)
	}
	for y := 0; y < h; y++ {
		seed(0, y)
		seed(w-1, y)
	}
	for len(stack) > 0 {
		i := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		x, y := i%w, i/w
		if x > 0 {
			seed(x-1, y)
		}
		if x < w-1 {
			seed(x+1, y)
		}
		if y > 0 {
			seed(x, y-1)
		}
		if y < h-1 {
			seed(x, y+1)
		}
	}
	for i, done := range visited {
		if done {
			img.SetNRGBA(i%w, i/w, color.NRGBA{})
		}
	}
}

// opaqueBounds 返回包含所有非透明像素的最小矩形；全透明时 ok=false。
func opaqueBounds(img *image.NRGBA) (image.Rectangle, bool) {
	b := img.Bounds()
	minX, minY, maxX, maxY := b.Dx(), b.Dy(), -1, -1
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			if img.NRGBAAt(b.Min.X+x, b.Min.Y+y).A != 0 {
				if x < minX {
					minX = x
				}
				if x > maxX {
					maxX = x
				}
				if y < minY {
					minY = y
				}
				if y > maxY {
					maxY = y
				}
			}
		}
	}
	if maxX < 0 {
		return image.Rectangle{}, false
	}
	return image.Rect(minX, minY, maxX+1, maxY+1), true
}

// cropOpaque 返回包含所有非透明像素的最小区域。
func cropOpaque(img *image.NRGBA) *image.NRGBA {
	r, ok := opaqueBounds(img)
	if !ok {
		return img
	}
	return img.SubImage(r).(*image.NRGBA)
}

// applyRoundedRectMask 用几何圆角矩形重算边界 alpha。
//
// JPEG 白底图在圆角边界留下一圈「前景色与白混合」的抗锯齿像素，泛洪抠图
// 只删纯白、删不掉它们，缩小后就成了白边。这里量出 artwork 的圆角半径，
// 用 4x 超采样计算圆角矩形覆盖率，边界 alpha 全部按几何重算：
// 几何之外的泛白像素直接归零，边界过渡干净且平滑。
func applyRoundedRectMask(img *image.NRGBA) {
	bounds, ok := opaqueBounds(img)
	if !ok {
		return
	}
	// 圆角半径：顶行第一个不透明像素的 x 偏移 / 左列第一个的 y 偏移，取大者。
	// 顶行落在圆角的"最上沿"，其不透明段从 r 处开始。
	top := bounds.Min.Y
	left := bounds.Min.X
	r := 0
	for x := bounds.Min.X; x < bounds.Max.X; x++ {
		if img.NRGBAAt(x, top).A != 0 {
			r = x - left
			break
		}
	}
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		if img.NRGBAAt(left, y).A != 0 {
			if rr := y - top; rr > r {
				r = rr
			}
			break
		}
	}
	maxR := (bounds.Dx() / 2) - 1
	if r > maxR {
		r = maxR
	}
	if r <= 0 {
		return // 直角矩形，无需掩码
	}

	const ss = 4
	x0, y0 := float64(bounds.Min.X), float64(bounds.Min.Y)
	x1, y1 := float64(bounds.Max.X), float64(bounds.Max.Y)
	rf := float64(r)
	inside := func(px, py float64) bool {
		cx := clamp(px, x0+rf, x1-rf)
		cy := clamp(py, y0+rf, y1-rf)
		dx, dy := px-cx, py-cy
		return dx*dx+dy*dy <= rf*rf
	}
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			var hit int
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					if inside(float64(x)+float64(sx+1)/(ss+1), float64(y)+float64(sy+1)/(ss+1)) {
						hit++
					}
				}
			}
			cov := uint8(hit * 255 / (ss * ss))
			c := img.NRGBAAt(x, y)
			if cov < c.A {
				c.A = cov
				img.SetNRGBA(x, y, c)
			}
		}
	}
}

// resizeArea 区域平均缩放（盒滤波）：按目标像素映射回的源矩形做
// 预乘 alpha 加权平均，缩小无锯齿、透明边缘不发白晕。
func resizeArea(src *image.NRGBA, dw, dh int) *image.NRGBA {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw == 0 || sh == 0 {
		return image.NewNRGBA(image.Rect(0, 0, dw, dh))
	}
	out := image.NewNRGBA(image.Rect(0, 0, dw, dh))
	fw, fh := float64(sw), float64(sh)
	for y := 0; y < dh; y++ {
		sy0, sy1 := float64(y)*fh/float64(dh), float64(y+1)*fh/float64(dh)
		iy0, iy1 := int(sy0), int(sy1)
		if iy1 <= iy0 {
			iy1 = iy0 + 1
		}
		if iy1 > sh {
			iy1 = sh
		}
		for x := 0; x < dw; x++ {
			sx0, sx1 := float64(x)*fw/float64(dw), float64(x+1)*fw/float64(dw)
			ix0, ix1 := int(sx0), int(sx1)
			if ix1 <= ix0 {
				ix1 = ix0 + 1
			}
			if ix1 > sw {
				ix1 = sw
			}
			var r, g, bl, a float64
			for yy := iy0; yy < iy1; yy++ {
				for xx := ix0; xx < ix1; xx++ {
					c := src.NRGBAAt(b.Min.X+xx, b.Min.Y+yy)
					af := float64(c.A) / 255
					r += float64(c.R) * af
					g += float64(c.G) * af
					bl += float64(c.B) * af
					a += af
				}
			}
			n := float64((ix1 - ix0) * (iy1 - iy0))
			if a == 0 {
				out.SetNRGBA(x, y, color.NRGBA{})
				continue
			}
			out.SetNRGBA(x, y, color.NRGBA{
				R: uint8(r / a),
				G: uint8(g / a),
				B: uint8(bl / a),
				A: uint8(a / n * 255),
			})
		}
	}
	return out
}

// templateIcon 从彩色 artwork 派生菜单栏模板图标（黑色 + alpha）。
//
// macOS 的 template 图只取 alpha 通道、由系统着色（浅色/深色菜单栏自动适配），
// 所以这里做成「实心圆角方块 + 浅色图形挖空」：剪贴板纸、笔记本、手机这些
// 白/浅色块变成透明孔，蓝色底保持实心。WiFi 弧线、虚线箭头这类细线在 18pt
// 下会糊成噪点，按连通区域面积/尺寸过滤掉，只保留三块主要图形。
func templateIcon(canvas *image.NRGBA, size int) *image.NRGBA {
	b := canvas.Bounds()
	w, h := b.Dx(), b.Dy()

	// 1) 浅色像素标记为候选孔
	hole := make([]bool, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := canvas.NRGBAAt(x, y)
			if c.A == 0 {
				continue
			}
			lum := (299*int(c.R) + 587*int(c.G) + 114*int(c.B)) / 1000
			hole[y*w+x] = lum >= 200
		}
	}

	// 2) 连通区域筛选：面积太小或某一维太窄的（细线、虚线、网点）丢弃；
	//    通过筛选的里只保留面积最大的一个（剪贴板纸）作为挖空孔。笔记本/
	//    手机在 18pt 下白边不足 1px，挖出来只剩一圈灰噪点，不如保持实心。
	visited := make([]bool, w*h)
	minArea := w * h * 5 / 1000 // 0.5% 画布
	minSide := w * 45 / 1000    // 4.5% 画布
	stack := make([]int, 0, 4096)
	var best []int
	bestArea := 0
	for i := 0; i < w*h; i++ {
		if !hole[i] || visited[i] {
			continue
		}
		stack = append(stack[:0], i)
		visited[i] = true
		comp := stack[:0:0]
		comp = append(comp, i)
		minX, minY, maxX, maxY := w, h, -1, -1
		for len(stack) > 0 {
			p := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			px, py := p%w, p/w
			if px < minX {
				minX = px
			}
			if px > maxX {
				maxX = px
			}
			if py < minY {
				minY = py
			}
			if py > maxY {
				maxY = py
			}
			if px > 0 && hole[p-1] && !visited[p-1] {
				visited[p-1] = true
				stack = append(stack, p-1)
				comp = append(comp, p-1)
			}
			if px < w-1 && hole[p+1] && !visited[p+1] {
				visited[p+1] = true
				stack = append(stack, p+1)
				comp = append(comp, p+1)
			}
			if py > 0 && hole[p-w] && !visited[p-w] {
				visited[p-w] = true
				stack = append(stack, p-w)
				comp = append(comp, p-w)
			}
			if py < h-1 && hole[p+w] && !visited[p+w] {
				visited[p+w] = true
				stack = append(stack, p+w)
				comp = append(comp, p+w)
			}
		}
		if len(comp) >= minArea && maxX-minX >= minSide && maxY-minY >= minSide && len(comp) > bestArea {
			bestArea = len(comp)
			best = comp
		}
	}
	isHole := make([]bool, w*h)
	var bx0, by0, bx1, by1 int
	hasHole := best != nil
	if hasHole {
		bx0, by0, bx1, by1 = w, h, -1, -1
		for _, p := range best {
			isHole[p] = true
			px, py := p%w, p/w
			if px < bx0 {
				bx0 = px
			}
			if px > bx1 {
				bx1 = px
			}
			if py < by0 {
				by0 = py
			}
			if py > by1 {
				by1 = py
			}
		}
	}

	// 3) 输出黑色实心方块；挖空孔取最大连通分量（剪贴板），按原比例放大到
	//    约 48% 方块高并水平垂直居中——原始构图里剪贴板偏上，去掉笔记本/
	//    手机后直接沿用会头重脚轻。放大用区域平均采样，边缘自带抗锯齿；
	//    纸上的文字行不属于该分量，保持黑色，与品牌图标一致。
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := canvas.NRGBAAt(x, y)
			if c.A == 0 {
				continue
			}
			out.SetNRGBA(x, y, color.NRGBA{R: 0, G: 0, B: 0, A: c.A})
		}
	}
	if hasHole {
		bw, bh := bx1-bx0+1, by1-by0+1
		th := h * 66 / 100
		tw := bw * th / bh
		if tw > w*80/100 {
			tw = w * 80 / 100
			th = bh * tw / bw
		}
		ox, oy := (w-tw)/2, (h-th)/2
		for y := 0; y < th; y++ {
			sy0 := by0 + y*bh/th
			sy1 := by0 + (y+1)*bh/th
			if sy1 <= sy0 {
				sy1 = sy0 + 1
			}
			for x := 0; x < tw; x++ {
				sx0 := bx0 + x*bw/tw
				sx1 := bx0 + (x+1)*bw/tw
				if sx1 <= sx0 {
					sx1 = sx0 + 1
				}
				var hit, total int
				for yy := sy0; yy < sy1 && yy <= by1; yy++ {
					for xx := sx0; xx < sx1 && xx <= bx1; xx++ {
						total++
						if isHole[yy*w+xx] {
							hit++
						}
					}
				}
				if total == 0 {
					continue
				}
				cov := float64(hit) / float64(total)
				c := out.NRGBAAt(ox+x, oy+y)
				c.A = uint8(float64(c.A) * (1 - cov))
				out.SetNRGBA(ox+x, oy+y, c)
			}
		}

		// 纸面文字行（黑色，即模板渲染下的前景色）：三行横线让白纸一眼
		// 认出是剪贴板。整块垂直居中略偏下（避开顶部夹子），两端不出纸边。
		const lines = 3
		lw := tw * 50 / 100
		lh := max(2, th*7/100)
		gap := max(2, th*9/100)
		lx := ox + (tw-lw)/2
		blockH := lh*lines + gap*(lines-1)
		ly := oy + (th-blockH)/2 + th*4/100
		if ly+blockH > oy+th {
			ly = oy + th - blockH
		}
		for i := 0; i < lines; i++ {
			y0 := ly + i*(lh+gap)
			for y := y0; y < y0+lh; y++ {
				for x := lx; x < lx+lw; x++ {
					out.SetNRGBA(x, y, color.NRGBA{R: 0, G: 0, B: 0, A: 255})
				}
			}
		}
	}
	return resizeArea(out, size, size)
}

// runFrom 以 srcPath 的图片为源，重产 appicon.png / favicon.png /
// menuicon.png（菜单栏模板图）/ menuicon_win.ico / winres/lanoclip.ico。
func runFrom(srcPath string) error {
	f, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	src, _, err := image.Decode(f)
	f.Close()
	if err != nil {
		return fmt.Errorf("decode %s: %w", srcPath, err)
	}

	work := toNRGBA(src)
	floodWhiteBorder(work)
	// 几何圆角掩码：把圆角边界残留的蓝白混合像素按几何 alpha 重算，去掉白边
	applyRoundedRectMask(work)
	art := cropOpaque(work)
	if art.Bounds().Dx() < 16 || art.Bounds().Dy() < 16 {
		return fmt.Errorf("artwork too small after background removal (%dx%d) — is the background really white?",
			art.Bounds().Dx(), art.Bounds().Dy())
	}

	// 等比缩放放进 1024 画布，视觉占比与程序化版本一致（89%），水平垂直居中
	aw, ah := art.Bounds().Dx(), art.Bounds().Dy()
	scale := float64(appTilePx) / float64(max(aw, ah))
	tw, th := int(float64(aw)*scale+0.5), int(float64(ah)*scale+0.5)
	tile := resizeArea(art, tw, th)
	canvas := image.NewNRGBA(image.Rect(0, 0, 1024, 1024))
	ox, oy := (1024-tw)/2, (1024-th)/2
	for y := 0; y < th; y++ {
		for x := 0; x < tw; x++ {
			canvas.SetNRGBA(ox+x, oy+y, tile.NRGBAAt(x, y))
		}
	}

	if err := writePNG("assets/appicon.png", canvas); err != nil {
		return err
	}
	fmt.Println("  generated assets/appicon.png (1024x1024, from artwork)")
	if err := writePNG("web/favicon.png", resizeArea(canvas, 64, 64)); err != nil {
		return err
	}
	fmt.Println("  generated web/favicon.png (64x64, from artwork)")
	// 页面头部大图标：裁到图形边界后 256px，2x/3x 屏上 56~80 CSS px 依然锐利
	if err := writePNG("web/appicon.png", resizeArea(cropOpaque(canvas), 256, 256)); err != nil {
		return err
	}
	fmt.Println("  generated web/appicon.png (256x256, from artwork)")

	// macOS 菜单栏：同一造型的单色模板图（黑色 + 挖空浅色图形）
	if err := writePNG("internal/tray/menuicon.png", templateIcon(canvas, 44)); err != nil {
		return err
	}
	fmt.Println("  generated internal/tray/menuicon.png (44x44 template, from artwork)")

	// Windows 托盘与 exe 图标：小尺寸从带留白的完整画布缩
	sizes := []int{16, 24, 32, 48}
	imgs := make([]*image.NRGBA, len(sizes))
	for i, s := range sizes {
		imgs[i] = resizeArea(canvas, s, s)
	}
	if err := writeFile("internal/tray/menuicon_win.ico", encodeICO(imgs)); err != nil {
		return err
	}
	fmt.Printf("  generated internal/tray/menuicon_win.ico (%v, from artwork)\n", sizes)

	exeSizes := []int{16, 24, 32, 48, 64, 128, 256}
	exeImgs := make([]*image.NRGBA, len(exeSizes))
	for i, s := range exeSizes {
		exeImgs[i] = resizeArea(canvas, s, s)
	}
	if err := writeFile("winres/lanoclip.ico", encodeICO(exeImgs)); err != nil {
		return err
	}
	fmt.Printf("  generated winres/lanoclip.ico (%v, from artwork)\n", exeSizes)
	return nil
}
