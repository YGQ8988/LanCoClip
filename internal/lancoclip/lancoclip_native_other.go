//go:build (!darwin || !cgo) && !windows

package lancoclip

import (
	"context"
	"time"
)

// nativeReadText is unavailable here: text is read through pbpaste / xclip /
// xsel, so the caller always falls back to the helper processes.
func nativeReadText() (string, bool) { return "", false }

// lancoclipChanges falls back to plain interval polling. Without a native
// lancoclip API there is no cheap way to ask "did it change?" — every probe
// spawns a helper process, so the interval stays at the configured value.
func lancoclipChanges(ctx context.Context, interval time.Duration) <-chan struct{} {
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	ch := make(chan struct{}, 1)
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				signalChange(ch)
			}
		}
	}()
	return ch
}
