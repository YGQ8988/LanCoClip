//go:build (!darwin || !cgo) && !windows

package lancoclip

import "errors"

// readLancoclipImage is unsupported off macOS/Windows (or without cgo).
func readLancoclipImage() []byte { return nil }

// WriteImage is unsupported off macOS/Windows (or without cgo).
func WriteImage(png []byte) error {
	return errors.New("writing an image to the system lancoclip is unsupported on this platform")
}
