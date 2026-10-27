//go:build !windows && !(darwin && cgo)

package tray

import "log"

// Supported reports whether a native tray is available.
// The tray is only implemented for macOS (cgo) and Windows.
func Supported() bool { return false }

// Run is a no-op on platforms without a native tray.
func Run(app App, opts Options) { log.Println("tray not supported on this platform") }

// UpdateHistory is a no-op on platforms without a native tray.
func UpdateHistory(items []Item) {}

// Quit is a no-op on platforms without a native tray.
func Quit() {}
