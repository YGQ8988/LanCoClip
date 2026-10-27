//go:build windows

package notify

import "lanoclip/internal/systray"

// Supported reports whether notifications can be delivered.
func Supported() bool { return true }

// Notify shows a tray balloon (a toast on Windows 10/11). onClick runs on its
// own goroutine when the balloon is clicked; pass nil if no action is wanted.
func Notify(title, body string, onClick func()) error {
	systray.SetNotifyClickHandler(onClick)
	return systray.Notify(title, body)
}
