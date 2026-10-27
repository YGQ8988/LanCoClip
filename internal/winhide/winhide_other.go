//go:build !windows

package winhide

import "os/exec"

// Hide is a no-op outside Windows; only Windows needs the CREATE_NO_WINDOW
// flag to keep child processes from flashing a console window. Having it on
// every platform lets callers import winhide unconditionally instead of
// maintaining per-package hideCmd wrappers.
func Hide(*exec.Cmd) {}
