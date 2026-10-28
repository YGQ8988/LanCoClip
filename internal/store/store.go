package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// Record kinds — text and images are stored, counted and paginated separately.
const (
	KindText  = "text"
	KindImage = "image"
)

// Data location — one place so the store, the log file and the docs cannot
// drift apart.
const (
	// DefaultDirName is the folder created under the user config directory.
	DefaultDirName = "LanCoClip"
	// EnvDir overrides the data directory (lower priority than -data).
	EnvDir = "LANOCLIP_DIR"
)

// DefaultDir returns the platform user config directory joined with
// DefaultDirName. Both the data store and the mirrored log file live there.
func DefaultDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, DefaultDirName), nil
}

// Per-kind caps. Images take far more disk space, so they are capped lower.
// The caps are configurable at runtime through the settings API
// (/api/settings) and persisted in settings.json; the values below are the
// defaults plus the bounds a user may choose from.
const (
	DefaultMaxTextRecords  = 500
	DefaultMaxImageRecords = 200
	minTextRecords         = 10
	maxTextRecords         = 5000
	minImageRecords        = 5
	maxImageRecords        = 1000
)

// Settings is the user-tunable retention configuration.
type Settings struct {
	MaxText       int    `json:"maxText"`
	MaxImage      int    `json:"maxImage"`
	HotkeyQR      string `json:"hotkeyQR,omitempty"`      // 二维码快捷键（canonical，如 "meta+shift+q"）；空 = 平台默认
	HotkeyHistory string `json:"hotkeyHistory,omitempty"` // 历史记录快捷键；空 = 平台默认
}

// Record is a single lancoclip snapshot: either text or an image.
type Record struct {
	ID        int64  `json:"id"`
	Kind      string `json:"kind"`              // KindText | KindImage
	Content   string `json:"content,omitempty"` // text payload
	Image     string `json:"image,omitempty"`   // image file name inside the images dir
	ImageSum  string `json:"imageSum,omitempty"`
	CreatedAt int64  `json:"createdAt"` // unix milliseconds
}

// Store keeps lancoclip records in memory and mirrors them to disk.
// Text and images live in separate lists and separate files:
//
//	<dir>/texts.json     文本记录
//	<dir>/images.json    图片记录（元数据）
//	<dir>/images/*.png   图片原图
//	<dir>/settings.json  保留条数配置（可运行时修改）
type Store struct {
	mu     sync.RWMutex
	dir    string
	imgDir string
	// 文本与图片各自编号：清空某一类时，只有那一类的 ID 从 1 重新数。
	// 代价是跨类型 ID 可能相同（文本 1 和图片 1 并存）——所有按 ID 的查询
	// 都限定在同类型内（ImagePath / ImageSum 只查图片，托盘复制文本用的是
	// 内容而不是 ID），所以不会串号。
	nextTextID  int64
	nextImageID int64
	texts       []Record
	images      []Record
	maxText     int // 文本保留条数上限（可配置）
	maxImage    int // 图片保留条数上限（可配置）
	// 全局快捷键（canonical 组合串，空 = 平台默认，见 HotkeyQR/HistHistory）
	hotkeyQR      string
	hotkeyHistory string
	dirty         bool
}

func (s *Store) textsPath() string    { return filepath.Join(s.dir, "texts.json") }
func (s *Store) imagesPath() string   { return filepath.Join(s.dir, "images.json") }
func (s *Store) legacyPath() string   { return filepath.Join(s.dir, "history.json") }
func (s *Store) settingsPath() string { return filepath.Join(s.dir, "settings.json") }

// NewStore loads the records. When dir is empty it falls back to the
// LANOCLIP_DIR environment variable and then to the user config directory —
// the env/flag override lets tests (and multiple instances) run against an
// isolated data set without touching real data.
func NewStore(dir string) (*Store, error) {
	if dir == "" {
		dir = os.Getenv(EnvDir)
	}
	if dir == "" {
		if d, err := DefaultDir(); err == nil {
			dir = d
		} else {
			dir = DefaultDirName
		}
	}
	imgDir := filepath.Join(dir, "images")
	if err := os.MkdirAll(imgDir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{
		dir:         dir,
		imgDir:      imgDir,
		nextTextID:  1,
		nextImageID: 1,
		maxText:     DefaultMaxTextRecords,
		maxImage:    DefaultMaxImageRecords,
	}
	s.load()
	return s, nil
}

func (s *Store) load() {
	s.loadSettings()
	loadedTexts := s.loadFile(s.textsPath(), &s.texts)
	loadedImages := s.loadFile(s.imagesPath(), &s.images)

	// One-time migration from the single-file layout (history.json).
	if !loadedTexts && !loadedImages {
		var all []Record
		if s.loadFile(s.legacyPath(), &all) {
			for _, r := range all {
				if r.Kind == KindImage {
					s.images = append(s.images, r)
				} else {
					r.Kind = KindText
					s.texts = append(s.texts, r)
				}
			}
			s.dirty = true
			saveQuiet(s)
			// 保留旧文件但改名，避免下次再迁移
			_ = os.Rename(s.legacyPath(), s.legacyPath()+".migrated")
		}
	}

	// 两类各按各的现有 ID 续号（历史数据里文本 / 图片的号段本来就各自独立）
	for _, list := range [][]Record{s.texts, s.images} {
		for i := range list {
			if list[i].Kind == "" {
				list[i].Kind = KindText
			}
			if list[i].Kind == KindImage {
				if list[i].ID >= s.nextImageID {
					s.nextImageID = list[i].ID + 1
				}
			} else if list[i].ID >= s.nextTextID {
				s.nextTextID = list[i].ID + 1
			}
		}
	}

	// 兜底：settings.json 可能被手工改得比磁盘存量还小，加载时按最旧优先
	// 修剪一次，保证「不超出上限」的不变量成立（下次落盘即生效）
	s.mu.Lock()
	if trimList(&s.texts, s.maxText, nil)+trimList(&s.images, s.maxImage, s.removeImageFile) > 0 {
		s.dirty = true
	}
	s.mu.Unlock()
}

// loadFile reads a JSON array into dst; it reports whether the file existed
// and decoded successfully.
func (s *Store) loadFile(path string, dst *[]Record) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var records []Record
	if err := json.Unmarshal(data, &records); err != nil {
		return false
	}
	*dst = records
	return true
}

// readJSONFile decodes a JSON object into dst; it reports success.
func readJSONFile(path string, dst any) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return json.Unmarshal(data, dst) == nil
}

// loadSettings reads settings.json. A missing or broken file falls back to the
// defaults, and out-of-range values are clamped into the allowed bounds.
// Callers must not hold s.mu (invoked from load()).
func (s *Store) loadSettings() {
	s.maxText = DefaultMaxTextRecords
	s.maxImage = DefaultMaxImageRecords
	var cfg Settings
	if !readJSONFile(s.settingsPath(), &cfg) {
		return
	}
	s.maxText = clampLimit(cfg.MaxText, minTextRecords, maxTextRecords, DefaultMaxTextRecords)
	s.maxImage = clampLimit(cfg.MaxImage, minImageRecords, maxImageRecords, DefaultMaxImageRecords)
	s.hotkeyQR = strings.ToLower(strings.TrimSpace(cfg.HotkeyQR))
	s.hotkeyHistory = strings.ToLower(strings.TrimSpace(cfg.HotkeyHistory))
}

// clampLimit keeps v inside [lo, hi]; a non-positive v means "unset" and
// yields the default.
func clampLimit(v, lo, hi, def int) int {
	if v <= 0 {
		return def
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// AddText stores a new text entry. It returns false when the content
// duplicates the most recent text record or is empty.
func (s *Store) AddText(content string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if content == "" {
		return false
	}
	if len(s.texts) > 0 && s.texts[0].Content == content {
		return false
	}
	s.texts = prepend(s.texts, s.newRecord(KindText, content, "", ""), s.maxText, nil)
	s.dirty = true
	return true
}

// AddImage stores a new image entry (PNG). It returns false when the bytes
// duplicate the most recent image.
func (s *Store) AddImage(png []byte) (bool, error) {
	if len(png) == 0 {
		return false, nil
	}
	sum := SumHex(png)

	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.images) > 0 && s.images[0].ImageSum == sum {
		return false, nil
	}

	id := s.nextImageID
	name := fmt.Sprintf("%d.png", id)
	if err := os.WriteFile(filepath.Join(s.imgDir, name), png, 0o644); err != nil {
		return false, err
	}
	s.images = prepend(s.images, s.newRecord(KindImage, "", name, sum), s.maxImage, s.removeImageFile)
	s.dirty = true
	return true, nil
}

// newRecord mints a record with a fresh id and timestamp. Callers hold s.mu.
// 文本 / 图片各取各的号段，所以 ID 只在同类型内唯一。
func (s *Store) newRecord(kind, content, image, sum string) Record {
	var id int64
	if kind == KindImage {
		id = s.nextImageID
		s.nextImageID++
	} else {
		id = s.nextTextID
		s.nextTextID++
	}
	r := Record{
		ID:        id,
		Kind:      kind,
		Content:   content,
		Image:     image,
		ImageSum:  sum,
		CreatedAt: time.Now().UnixMilli(),
	}
	return r
}

// prepend inserts r at the head, trimming the oldest entries. onDrop (optional)
// lets the caller clean up files of the dropped records.
func prepend(list []Record, r Record, max int, onDrop func(Record)) []Record {
	out := append([]Record{r}, list...)
	trimList(&out, max, onDrop)
	return out
}

// trimList drops the oldest entries (the tail of the newest-first list) until
// len(list) <= max, reporting how many were removed. onDrop (optional) cleans
// up files of the dropped records.
func trimList(list *[]Record, max int, onDrop func(Record)) int {
	removed := 0
	for len(*list) > max {
		last := (*list)[len(*list)-1]
		*list = (*list)[:len(*list)-1]
		if onDrop != nil {
			onDrop(last)
		}
		removed++
	}
	return removed
}

func (s *Store) removeImageFile(r Record) {
	if r.Kind == KindImage && r.Image != "" {
		_ = os.Remove(filepath.Join(s.imgDir, r.Image))
	}
}

// Page returns one page of records of the given kind (newest first) plus that
// kind's total count. page is 1-based; out-of-range pages yield an empty slice.
func (s *Store) Page(kind string, page, size int) ([]Record, int) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	list := s.texts
	if kind == KindImage {
		list = s.images
	}
	total := len(list)

	start := (page - 1) * size
	if start >= total {
		return []Record{}, total
	}
	end := start + size
	if end > total {
		end = total
	}
	out := make([]Record, end-start)
	copy(out, list[start:end])
	return out, total
}

// RecentMixed returns the n newest records across both kinds (text + image),
// merged by createdAt descending. Used by the tray menu history.
func (s *Store) RecentMixed(n int) []Record {
	if n <= 0 {
		return []Record{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	merged := make([]Record, 0, len(s.texts)+len(s.images))
	merged = append(merged, s.texts...)
	merged = append(merged, s.images...)
	sort.Slice(merged, func(i, j int) bool { return merged[i].CreatedAt > merged[j].CreatedAt })
	if len(merged) > n {
		merged = merged[:n]
	}
	return merged
}

// Counts returns the number of text and image records.
func (s *Store) Counts() (text, image int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.texts), len(s.images)
}

// MaxText / MaxImage return the current retention caps.
func (s *Store) MaxText() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.maxText
}

func (s *Store) MaxImage() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.maxImage
}

// DefaultHotkeyQR / DefaultHotkeyHistory: per-platform factory combos.
func DefaultHotkeyQR() string {
	if runtime.GOOS == "darwin" {
		return "meta+shift+q"
	}
	return "ctrl+shift+q"
}

func DefaultHotkeyHistory() string {
	if runtime.GOOS == "darwin" {
		return "meta+shift+v"
	}
	return "ctrl+shift+v"
}

// HotkeyQR / HotkeyHistory return the effective global hotkeys (canonical
// form; the platform default when the user has not customized them).
func (s *Store) HotkeyQR() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.hotkeyQR == "" {
		return DefaultHotkeyQR()
	}
	return s.hotkeyQR
}

func (s *Store) HotkeyHistory() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.hotkeyHistory == "" {
		return DefaultHotkeyHistory()
	}
	return s.hotkeyHistory
}

// SetHotkeys persists the hotkey combos ("" = back to the platform default).
func (s *Store) SetHotkeys(qr, hist string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hotkeyQR = strings.ToLower(strings.TrimSpace(qr))
	s.hotkeyHistory = strings.ToLower(strings.TrimSpace(hist))
	_ = writeJSONFile(s.settingsPath(), s.settingsSnapshot())
}

// SetLimits updates the retention caps and immediately trims the oldest
// records (image files are deleted along with their records) so the new caps
// hold right away. It persists settings.json under the lock and reports how
// many records of each kind were dropped. Values are clamped into the allowed
// bounds.
func (s *Store) SetLimits(maxText, maxImage int) (trimmedText, trimmedImage int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.maxText = clampLimit(maxText, minTextRecords, maxTextRecords, DefaultMaxTextRecords)
	s.maxImage = clampLimit(maxImage, minImageRecords, maxImageRecords, DefaultMaxImageRecords)

	trimmedText = trimList(&s.texts, s.maxText, nil)
	trimmedImage = trimList(&s.images, s.maxImage, s.removeImageFile)
	s.dirty = true

	// 配置立即落盘：即便紧接着崩溃，上限也不会退回旧值
	_ = writeJSONFile(s.settingsPath(), s.settingsSnapshot())
	return trimmedText, trimmedImage
}

// settingsSnapshot builds the persisted settings struct (caller holds s.mu).
func (s *Store) settingsSnapshot() Settings {
	return Settings{
		MaxText:       s.maxText,
		MaxImage:      s.maxImage,
		HotkeyQR:      s.hotkeyQR,
		HotkeyHistory: s.hotkeyHistory,
	}
}

// ImagePath returns the on-disk path of an image record.
func (s *Store) ImagePath(id int64) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.images {
		if r.ID == id && r.Image != "" {
			return filepath.Join(s.imgDir, filepath.Base(r.Image)), true
		}
	}
	return "", false
}

// ImageSum returns the content hash of an image record.
func (s *Store) ImageSum(id int64) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.images {
		if r.ID == id {
			return r.ImageSum, r.ImageSum != ""
		}
	}
	return "", false
}

// DropRecentText removes the newest text record when it is a *duplicate* of an
// earlier record and was created within d.
//
// The retraction only exists to undo a duplicate that appeared because the
// content was copied from the service page and the page reported it a moment
// after the lancoclip had already changed. Requiring a second, older record
// with the same content is what makes that safe: a record that is the only one
// with its content is the original and must never be deleted (that is exactly
// the case when the user re-copies the newest entry from the page).
func (s *Store) DropRecentText(content string, d time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept, _, ok := dropRecentDuplicate(s.texts, func(r Record) bool {
		return r.Content == content
	}, d)
	if !ok {
		return false
	}
	s.texts = kept
	s.dirty = true
	return true
}

// DropRecentImage is DropRecentText for image records (and removes the file).
func (s *Store) DropRecentImage(sum string, d time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept, dropped, ok := dropRecentDuplicate(s.images, func(r Record) bool {
		return r.ImageSum == sum
	}, d)
	if !ok {
		return false
	}
	s.images = kept
	s.removeImageFile(dropped)
	s.dirty = true
	return true
}

// dropRecentDuplicate drops the head record when match(head) holds, it was
// created within d, and at least one older record matches too. It reports the
// remaining list, the dropped record, and whether the drop happened. Callers
// decide what to do with the dropped record (images need file cleanup).
func dropRecentDuplicate(list []Record, match func(Record) bool, d time.Duration) ([]Record, Record, bool) {
	if len(list) < 2 {
		return list, Record{}, false
	}
	head := list[0]
	if !match(head) || time.Since(time.UnixMilli(head.CreatedAt)) > d {
		return list, Record{}, false
	}
	for _, r := range list[1:] {
		if match(r) {
			return list[1:], head, true
		}
	}
	return list, Record{}, false
}

// Clear drops records of one kind ("" clears everything) and deletes the
// matching image files.
func (s *Store) Clear(kind string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 清空某一类时，那一类的 ID 从 1 重新数（用户的直观预期：「清了就该从 1 开始」）。
	// 两类各有一个号段，所以清图片不影响文本继续往后编号，反之亦然。
	if kind == "" || kind == KindImage {
		for _, r := range s.images {
			s.removeImageFile(r)
		}
		s.images = nil
		s.nextImageID = 1
	}
	if kind == "" || kind == KindText {
		s.texts = nil
		s.nextTextID = 1
	}
	s.dirty = true
}

// Save writes both lists to disk when there is something to persist.
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dirty {
		return nil
	}
	if err := writeJSONFile(s.textsPath(), orEmpty(s.texts)); err != nil {
		return err
	}
	if err := writeJSONFile(s.imagesPath(), orEmpty(s.images)); err != nil {
		return err
	}
	s.dirty = false
	return nil
}

func writeJSONFile(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// SumHex is the single sha256 fingerprint used for image bytes, page-reported
// text and image suppression alike.
func SumHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// orEmpty keeps an empty list on disk as [] rather than null.
func orEmpty(records []Record) []Record {
	if records == nil {
		return []Record{}
	}
	return records
}

// saveQuiet persists during load/migration, where the mutex is not yet held by
// callers and errors are not actionable.
func saveQuiet(s *Store) {
	_ = writeJSONFile(s.textsPath(), orEmpty(s.texts))
	_ = writeJSONFile(s.imagesPath(), orEmpty(s.images))
}
