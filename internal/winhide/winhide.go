//go:build windows

// Package winhide keeps helper processes from flashing a console window.
//
// The app is a GUI-subsystem binary, but every exec of an external command
// (powershell Get-Clipboard poll, cmd /c clip, rundll32, osascript …) would
// still pop up a console window unless CREATE_NO_WINDOW is passed.
package winhide

import (
	"os/exec"
	"syscall"
)

const createNoWindow = 0x08000000

// Hide applies CREATE_NO_WINDOW (+ HideWindow) to cmd so no console flashes.
func Hide(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
}
