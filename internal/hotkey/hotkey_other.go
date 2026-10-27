//go:build !darwin && !windows

// Package hotkey: no global hotkeys on platforms without a tray GUI.
package hotkey

// keyTable stays empty here; combo parsing still accepts single letters and
// digits (validated in combo.go without the table).
var keyTable = map[string]int{}

func Install(onQR, onHistory func(), qrCombo, histCombo string) error { return nil }

// Update is a no-op on platforms without global hotkeys.
func Update(qrCombo, histCombo string) error { return nil }
