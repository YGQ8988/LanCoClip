//go:build windows

package tray

import (
	_ "embed"
	"os/exec"
	"strings"

	"lanoclip/internal/systray"
	"lanoclip/internal/winhide"
)

//go:embed menuicon_win.ico
var menuIconICO []byte

// Supported reports whether a native tray is available.
func Supported() bool { return true }

// Run shows the notification-area icon and blocks until the user quits.
func Run(app App, opts Options) {
	systray.Run(func() {
		systray.SetIcon(menuIconICO) // Windows 需要 .ico
		systray.SetTooltip("剪贴板")
		run(app, confirmClearAll, openBrowser, opts)
	}, func() {})
}

// confirmClearAll shows a native confirmation dialog for "清除历史".
func confirmClearAll() bool {
	ps := `Add-Type -AssemblyName PresentationFramework; ` +
		`[System.Windows.MessageBox]::Show('确定清除全部剪贴板历史（文本和图片）？清除后无法恢复。', '清除历史', 'YesNo', 'Warning')`
	cmd := exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command", ps)
	winhide.Hide(cmd)
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "Yes"
}

// Quit asks the tray loop to stop.
func Quit() { systray.Quit() }

// openBrowser opens url in the default browser. rundll32 avoids the console
// window flash that `cmd /c start` causes in a GUI-subsystem binary, and
// hideCmd removes rundll32's own console flash.
func openBrowser(url string) {
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	winhide.Hide(cmd)
	_ = cmd.Start()
}
