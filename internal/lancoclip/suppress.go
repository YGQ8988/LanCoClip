package lancoclip

import (
	"sync"
	"time"

	"lanoclip/internal/store"
)

// suppressTTL bounds how long a registered payload is ignored, so a stale entry
// (e.g. the page's lancoclip write never actually happened) cannot swallow a
// later, genuine copy of the same content.
const (
	suppressTTL        = 2 * time.Minute
	suppressMaxEntries = 2048 // 每个 map 的硬上限，避免异常写入无限增长
)

// Suppressor remembers payloads that were copied *from the service page*, so the
// lancoclip lancoclipWatcher does not record them again as new history entries.
//
// Entries are one-shot (consumed on first match) and expire after suppressTTL.
// Take* 只在 hash 精确命中时删除条目，所以从未被命中的条目要靠 Add* 时的
// 清扫 + 硬上限兜底，否则会永久滞留。
type Suppressor struct {
	mu    sync.Mutex
	texts map[string]time.Time // sha256(text) -> registered at
	imgs  map[string]time.Time // image sha256  -> registered at
}

func NewSuppressor() *Suppressor {
	return &Suppressor{texts: map[string]time.Time{}, imgs: map[string]time.Time{}}
}

// sweepExpired drops entries older than suppressTTL. Caller must hold s.mu.
func sweepExpired(m map[string]time.Time, now time.Time) {
	for h, at := range m {
		if now.Sub(at) > suppressTTL {
			delete(m, h)
		}
	}
}

// enforceCap drops the oldest entries until m holds at most
// suppressMaxEntries. Caller must hold s.mu.
func enforceCap(m map[string]time.Time) {
	for len(m) > suppressMaxEntries {
		var oldest string
		var oldestAt time.Time
		found := false
		for h, at := range m {
			if !found || at.Before(oldestAt) {
				oldest, oldestAt, found = h, at, true
			}
		}
		if !found {
			return
		}
		delete(m, oldest)
	}
}

func (s *Suppressor) AddText(text string) {
	if text == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	sweepExpired(s.texts, now)
	s.texts[store.SumHex([]byte(text))] = now
	enforceCap(s.texts)
}

func (s *Suppressor) AddImageSum(sum string) {
	if sum == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	sweepExpired(s.imgs, now)
	s.imgs[sum] = now
	enforceCap(s.imgs)
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
	delete(s.texts, h) // 命中即消费，过期条目也一并删除
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
	delete(s.imgs, sum) // 命中即消费，过期条目也一并删除
	return time.Since(at) <= suppressTTL
}
