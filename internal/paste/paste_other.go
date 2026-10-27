//go:build !darwin && !windows

package paste

// Do is a no-op on platforms without global hotkeys / paste simulation.
func Do() error { return nil }

// FreezeFocus is a no-op where paste simulation is unsupported.
func FreezeFocus() {}
