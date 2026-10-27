//go:build !((darwin && cgo) || windows)

package notify

// Supported reports whether notifications can be delivered.
func Supported() bool { return false }

// Notify is a no-op on platforms without a notification implementation.
func Notify(title, body string, onClick func()) error { return ErrUnsupported }
