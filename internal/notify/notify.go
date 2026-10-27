// Package notify shows a single desktop notification and reports back when the
// user clicks it.
//
// Platform behaviour:
//
//	macOS    UNUserNotificationCenter (needs a real .app bundle; a bare binary
//	         has no bundle identifier and returns ErrUnsupported)
//	Windows  tray balloon via Shell_NotifyIcon, rendered as a toast by Win10/11
//	other    ErrUnsupported
package notify

import "errors"

// ErrUnsupported is returned when this build has no notification path — an
// unbundled macOS binary, cgo disabled, or a platform without an implementation.
var ErrUnsupported = errors.New("notify: unsupported on this platform")
