package lancoclip

import (
	"sync"
	"time"

	"lanoclip/internal/store"
)

// suppressTTL bounds how long a registered payload is ignored, so a stale entry
// (e.g. the page's lancoclip write never actually happened) cannot swallow a
// later, genuine copy of the same content.
const suppressTTL = 2 * time.Minute

// Suppressor remembers payloads that were copied *from the service page*, so the
// lancoclip lancoclipWatcher does not record them again as new history entries.
//
// Entries are one-shot (consumed on first match) and expire after suppressTTL.
type Suppressor struct {
	mu    sync.Mutex
	texts map[string]time.Time // sha256(text) -> registered at
	imgs  map[string]time.Time // image sha256  -> registered at
}

func NewSuppressor() *Suppressor {
	return &Suppressor{texts: map[string]time.Time{}, imgs: map[string]time.Time{}}
}

func (s *Suppressor) AddText(text string) {
	if text == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.texts[store.SumHex([]byte(text))] = time.Now()
}

func (s *Suppressor) AddImageSum(sum string) {
	if sum == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.imgs[sum] = time.Now()
}

// TakeText reports whether text came from the page, consuming the entry.
func (s *Suppressor) TakeText(text string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := store.SumHex([]byte(text))
	at, ok := s.texts[h]
	if !ok {
		return false
	}
	delete(s.texts, h)
	return time.Since(at) <= suppressTTL
}

// TakeImageSum reports whether the image came from the page, consuming the entry.
func (s *Suppressor) TakeImageSum(sum string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.imgs[sum]
	if !ok {
		return false
	}
	delete(s.imgs, sum)
	return time.Since(at) <= suppressTTL
}
