//go:build (!darwin || !cgo) && !windows

package lancoclip

import (
	"context"
	"time"
)

// nativeReadText is unavailable here: text is read through pbpaste / xclip /
// xsel, so the caller always falls back to the helper processes.
func nativeReadText() (string, bool) { return "", false }

// fallbackPollInterval is how often platforms without a native lancoclip API
// probe for changes. Every probe spawns a helper process, so the cadence stays
// deliberately coarse.
const fallbackPollInterval = 500 * time.Millisecond

// lancoclipChanges falls back to plain interval polling. Without a native
// lancoclip API there is no cheap way to ask "did it change?" — every probe
// spawns a helper process, so the cadence is fixed at fallbackPollInterval.
func lancoclipChanges(ctx context.Context) <-chan struct{} {
	ch := make(chan struct{}, 1)
	go func() {
		t := time.NewTicker(fallbackPollInterval)
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
