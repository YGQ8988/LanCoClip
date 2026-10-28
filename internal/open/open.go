// Package open opens a URL in the OS-default browser. It is the single home of
// the three platform branches (macOS open / Windows rundll32 / Linux xdg-open)
// that used to be copy-pasted in main and the tray package.
package open

import (
	"os/exec"
	"runtime"

	"lanoclip/internal/winhide"
)

// Open opens url in the default browser.
//
// Windows uses rundll32 (not `cmd /c start`) to avoid the console window that a
// GUI-subsystem binary would otherwise flash; winhide.Hide hides rundll32's own
// console flash on the platforms where that matters.
func Open(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	winhide.Hide(cmd)
	_ = cmd.Start()
}
