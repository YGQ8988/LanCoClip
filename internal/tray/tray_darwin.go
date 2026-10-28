//go:build darwin && cgo

package tray

/*
#cgo LDFLAGS: -framework Cocoa -framework AppKit
// Implemented in forceSetTemplateIcon_darwin.m (compiled by cgo as Objective-C).
void forceSetTemplateIcon(const char *bytes, int length);
*/
import "C"

import (
	_ "embed"
	"os/exec"
	"strings"
	"time"
	"unsafe"

	"lanoclip/internal/open"
	"lanoclip/internal/systray"
)

//go:embed menuicon.png
var menuIconPNG []byte

// Supported reports whether a native menu-bar tray is available.
func Supported() bool { return true }

// Run shows the menu-bar icon and blocks until the user quits.
// It must be called from the main goroutine (Cocoa requires the main thread).
func Run(app App, opts Options) {
	setAgentMode()

	systray.Run(func() {
		// 菜单栏用模板图标（单色、自动适配深浅色）：menuicon.png 由
		// tools/genicons -from 从应用图标源图派生——黑色圆角方块、剪贴板
		// 挖空，只取 alpha 即为合法模板图。
		systray.SetTemplateIcon(menuIconPNG, menuIconPNG)
		systray.SetTitle("")
		systray.SetTooltip("剪贴板")

		// Fallback: set NSStatusItem.button.image through Cocoa directly.
		// systray's own icon path can silently fail on macOS 26 (Tahoe); re-setting
		// the image on the main thread (with retries) recovers it.
		if len(menuIconPNG) > 0 {
			cptr := (*C.char)(unsafe.Pointer(&menuIconPNG[0]))
			C.forceSetTemplateIcon(cptr, C.int(len(menuIconPNG)))
			go func() {
				for _, d := range []time.Duration{300 * time.Millisecond, 1200 * time.Millisecond} {
					time.Sleep(d)
					C.forceSetTemplateIcon(cptr, C.int(len(menuIconPNG)))
				}
			}()
		}

		run(app, confirmClearAll, open.Open, opts)
	}, func() {})
}

// confirmClearAll shows a native confirmation dialog for "清除历史".
// It reports whether the user confirmed.
//
// 注意：macOS 26 的 AppleScript 解析器不支持 `display dialog ... message ...`
// 形式（连纯英文都报 -2740 语法错误），说明文字必须并入正文用 \n 换行。
func confirmClearAll() bool {
	script := `display dialog "确定清除全部剪贴板历史（文本和图片）？\n清除后无法恢复。" buttons {"取消", "清除"} default button "清除" with icon caution`
	out, err := exec.Command("osascript", "-e", script).Output()
	if err != nil {
		return false // 用户点了「取消」或对话框出错
	}
	return strings.Contains(string(out), "button returned:清除")
}

// Quit asks the menu-bar loop to stop.
func Quit() { systray.Quit() }
