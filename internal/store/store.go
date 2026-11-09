package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Record kinds — text and images are stored, counted and paginated separately.
const (
	KindText  = "text"
	KindImage = "image"
)

// imageNameRE only accepts the file names this program generates:
// <id>.png / <id>-<nano>.png. The orphan collector uses it so a PNG the user
// dropped into the images directory is never silently deleted.
var imageNameRE = regexp.MustCompile(`^\d+(-\d+)?\.png$`)

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
	Paused        bool   `json:"paused,omitempty"`        // 暂停记录：不再保存任何新的剪贴板内容
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
	paused        bool   // 暂停记录（持久化到 settings.json）
	saveErr       string // 最近一次写盘失败，供页面提示；成功后清空
	imagesMetaOK  bool   // images.json 是否可信，决定要不要回收无主图片
	settingsOK    bool   // settings.json 是否可信；损坏时禁止按默认上限裁剪
	rebuiltImages bool   // 图片元数据由磁盘重建，用于裁剪保护与日志
	dirty         bool
	// SetLimits 裁剪掉的记录暂存区：图片文件先不删，等调用方 Save() 成功
	// 后 CommitTrim() 才真正删文件；Save 失败则 RestoreTrim() 放回内存。
	// 这样「保存失败」绝不会再留下「数据已被删」的半生效状态。
	trimStashTexts  []Record
	trimStashImages []Record
}

func (s *Store) textsPath() string    { return filepath.Join(s.dir, "texts.json") }
func (s *Store) imagesPath() string   { return filepath.Join(s.dir, "images.json") }
func (s *Store) settingsPath() string { return filepath.Join(s.dir, "settings.json") }

// NewStore loads the records. When dir is empty it falls back to the
// LANOCLIP_DIR environment variable and then to the user config directory —
// the env/flag override lets tests (and multiple instances) run against an
// isolated data set without touching real data.
// ResolveDir applies the documented precedence (-data > $LANOCLIP_DIR >
// user config dir). It is exported so main can take the instance lock on the
// exact same directory *before* NewStore touches any file.
func ResolveDir(dir string) string {
	if dir == "" {
		dir = os.Getenv(EnvDir)
	}
	if dir == "" {
		if d, err := DefaultDir(); err == nil {
			return d
		}
		return DefaultDirName
	}
	return dir
}

func NewStore(dir string) (*Store, error) {
	dir = ResolveDir(dir)
	imgDir := filepath.Join(dir, "images")
	if err := os.MkdirAll(imgDir, 0o700); err != nil {
		return nil, err
	}
	// 历史是明文（密码 / 验证码 / token 都可能在内）。旧版本建的 0644 文件
	// 在这里收敛成仅本人可读；best-effort，只读文件系统不影响启动。
	hardenPermissions(dir)
	s := &Store{
		dir:         dir,
		imgDir:      imgDir,
		nextTextID:  1,
		nextImageID: 1,
		maxText:     DefaultMaxTextRecords,
		maxImage:    DefaultMaxImageRecords,
	}
	s.load()
	s.collectOrphanImages()
	s.cleanStaleFiles()
	return s, nil
}

// hardenPermissions tightens files created by older builds (0644) to 0600 —
// every regular file in the data directory, including crash leftovers
// (*.tmp, *.corrupt-*) which contain full plaintext history while they exist.
// The parent directories are 0700 so backups/syncs are the only real exposure
// path left. Best-effort: a read-only location simply keeps its current mode.
func hardenPermissions(dir string) {
	_ = os.Chmod(dir, 0o700)
	_ = os.Chmod(filepath.Join(dir, "images"), 0o700)
	for _, d := range []string{dir, filepath.Join(dir, "images")} {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				_ = os.Chmod(filepath.Join(d, e.Name()), 0o600)
			}
		}
	}
}

// cleanStaleFiles removes crash leftovers that otherwise accumulate forever
// inside the 1 GiB disk budget: this program's own *.tmp files and all but the
// three most recent *.corrupt-<nano> backups (newest ones are kept for
// forensics). Only well-known names are touched — arbitrary user files that
// happen to end in .tmp or contain ".corrupt-" are left alone.
func (s *Store) cleanStaleFiles() {
	for _, name := range []string{s.textsPath() + ".tmp", s.imagesPath() + ".tmp", s.settingsPath() + ".tmp"} {
		_ = os.Remove(name)
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	var backups []string
	for _, e := range entries {
		if !e.IsDir() && isCorruptBackupName(e.Name()) {
			backups = append(backups, e.Name())
		}
	}
	if len(backups) <= 3 {
		return
	}
	// 按文件名末尾的 UnixNano 数值排序，而不是字典序：备份前缀分别是
	// texts.json / images.json / settings.json，字典序会先按前缀分组，
	// 跨组时「保留最近 3 份」会误删最新的留证。
	sort.SliceStable(backups, func(i, j int) bool {
		return corruptStamp(backups[i]) < corruptStamp(backups[j])
	})
	for _, name := range backups[:len(backups)-3] {
		if err := os.Remove(filepath.Join(s.dir, name)); err != nil {
			log.Printf("store: remove stale backup %s: %v", name, err)
		}
	}
}

// isCorruptBackupName matches exactly "<something>.corrupt-<digits>" so a
// random file merely containing ".corrupt-" is never counted or deleted.
func isCorruptBackupName(name string) bool {
	const marker = ".corrupt-"
	i := strings.LastIndex(name, marker)
	if i <= 0 {
		return false
	}
	suffix := name[i+len(marker):]
	if suffix == "" {
		return false
	}
	for _, c := range suffix {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// corruptStamp extracts the UnixNano suffix of a *.corrupt-<nano> backup.
func corruptStamp(name string) int64 {
	const marker = ".corrupt-"
	i := strings.LastIndex(name, marker)
	if i < 0 {
		return 0
	}
	v, err := strconv.ParseInt(name[i+len(marker):], 10, 64)
	if err != nil {
		return 0
	}
	return v
}

func (s *Store) load() {
	s.settingsOK = s.loadSettings()

	// 记住图片元数据是否可信：损坏时宁可留着无主文件，也不能误删用户图片。
	// 先判断文件是否存在，用来区分「全新数据目录」和「元数据损坏」。
	_, statErr := os.Stat(s.imagesPath())
	imagesFileExisted := statErr == nil
	s.imagesMetaOK = s.loadFile(s.imagesPath(), &s.images)
	if !s.imagesMetaOK {
		if !imagesFileExisted {
			if dirHasPNG(s.imgDir) {
				// images.json 被手工删除/外部清空，但磁盘上还有图片：
				// 与损坏同样处理，从磁盘重建，绝不能当「全新目录」回收。
				s.rebuiltImages = s.rebuildImagesFromDisk()
			} else {
				// 全新目录：磁盘上没有图片，空列表是可信的，正常落盘即可。
				s.imagesMetaOK = true
			}
		} else {
			// 文件存在却解析失败（readJSONFile 已改名留证）：从磁盘重建一份
			// 元数据，让每张原图都有引用。否则下一次 Save 会把空列表写成
			// images.json，重启后 collectOrphanImages 会把所有 .png 当孤儿删光。
			s.rebuiltImages = s.rebuildImagesFromDisk()
		}
	}
	s.loadFile(s.textsPath(), &s.texts)

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

	// 配置损坏 / 图片元数据由磁盘重建时，绝不能按默认上限裁掉存量：
	// trimList 对图片会真的 os.Remove，一次损坏就可能永久删掉几百张原图。
	// 把上限抬到至少容纳磁盘存量，让用户先看到完整历史，之后再自行调整。
	if !s.settingsOK || s.rebuiltImages {
		if len(s.texts) > s.maxText {
			s.maxText = len(s.texts)
		}
		if len(s.images) > s.maxImage {
			s.maxImage = len(s.images)
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

// rebuildImagesFromDisk reconstructs image metadata from the PNG files left in
// the images directory after images.json was lost or corrupted. Every existing
// file gets a record, so the orphan collector cannot delete user photos.
// Timestamps and IDs are best-effort (file mtime / sequential), which is far
// better than losing the images entirely.
func (s *Store) rebuildImagesFromDisk() bool {
	entries, err := os.ReadDir(s.imgDir)
	if err != nil {
		log.Printf("store: cannot rebuild image metadata: %v", err)
		return false
	}
	recs := make([]Record, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".png" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		// ImageSum 必须补上：它承载「页面复制图片后的抑制上报」与去重。
		// 缺了它，重建后的图片每次复制都会被 watcher 再记一条。
		sum, err := fileSumHex(filepath.Join(s.imgDir, name))
		if err != nil {
			log.Printf("store: hash rebuilt image %s: %v", name, err)
		}
		recs = append(recs, Record{
			ID:        int64(len(recs) + 1),
			Kind:      KindImage,
			Image:     name,
			ImageSum:  sum,
			CreatedAt: info.ModTime().UnixMilli(),
		})
	}
	sort.SliceStable(recs, func(i, j int) bool {
		return recs[i].CreatedAt > recs[j].CreatedAt
	})
	s.images = recs
	s.imagesMetaOK = true
	// 必须置 dirty：重建结果要随下一次 Save 落回 images.json，否则
	// images.json 一直不存在，下次启动会按「全新目录」把磁盘图片当孤儿删光。
	s.dirty = true
	log.Printf("store: images.json unavailable; rebuilt %d image records from disk", len(recs))
	return true
}

// dirHasPNG reports whether the directory contains at least one PNG file.
func dirHasPNG(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".png" {
			return true
		}
	}
	return false
}

// fileSumHex streams a file through sha256 without loading it into memory
// (a rebuilt image can be tens of MiB).
func fileSumHex(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// loadFile reads a JSON array into dst; it reports whether the file existed
// and decoded successfully.
func (s *Store) loadFile(path string, dst *[]Record) bool {
	return readJSONFile(path, dst)
}

// readJSONFile decodes a JSON array or object into dst; it reports success.
func readJSONFile(path string, dst any) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false // 文件不存在属正常情况
	}
	if err := json.Unmarshal(data, dst); err != nil {
		// 不能让一次损坏把历史「静默清空」并立刻被下次 Save 覆盖：
		// 先把现场改名留证，再回退默认值，同时在日志里留痕。
		backup := fmt.Sprintf("%s.corrupt-%d", path, time.Now().UnixNano())
		if renErr := os.Rename(path, backup); renErr != nil {
			log.Printf("store: broken %s (%v); could not preserve it: %v", path, err, renErr)
		} else {
			// 留证文件本身就是完整明文历史，权限必须立刻收敛。
			_ = os.Chmod(backup, 0o600)
			log.Printf("store: broken %s (%v) preserved as %s; starting with defaults", path, err, backup)
		}
		return false
	}
	return true
}

// loadSettings reads settings.json. A missing or broken file falls back to the
// defaults, and out-of-range values are clamped into the allowed bounds.
// Callers must not hold s.mu (invoked from load()).
func (s *Store) loadSettings() bool {
	s.maxText = DefaultMaxTextRecords
	s.maxImage = DefaultMaxImageRecords
	var cfg Settings
	if !readJSONFile(s.settingsPath(), &cfg) {
		return false
	}
	s.maxText = clampLimit(cfg.MaxText, minTextRecords, maxTextRecords, DefaultMaxTextRecords)
	s.maxImage = clampLimit(cfg.MaxImage, minImageRecords, maxImageRecords, DefaultMaxImageRecords)
	s.hotkeyQR = strings.ToLower(strings.TrimSpace(cfg.HotkeyQR))
	s.hotkeyHistory = strings.ToLower(strings.TrimSpace(cfg.HotkeyHistory))
	s.paused = cfg.Paused
	return true
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

// maxTextPayloadBytes caps a single text record — the same 512 KiB limit the
// push API uses. Without it a locally-copied multi-MiB log / JSON blob makes
// texts.json (rewritten in full on every save) and every page response balloon.
const maxTextPayloadBytes = 512 << 10

// AddText stores a new text entry. It returns false when the content
// duplicates the most recent text record or is empty.
func (s *Store) AddText(content string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.paused || content == "" {
		return false
	}
	if len(content) > maxTextPayloadBytes {
		log.Printf("store: text record truncated from %d to %d bytes", len(content), maxTextPayloadBytes)
		content = truncateUTF8(content, maxTextPayloadBytes)
	}
	if len(s.texts) > 0 && s.texts[0].Content == content {
		return false
	}
	s.texts = prepend(s.texts, s.newRecord(KindText, content, "", ""), s.maxText, nil)
	s.dirty = true
	return true
}

// truncateUTF8 cuts s to at most max bytes without splitting a rune.
func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
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

	if s.paused || (len(s.images) > 0 && s.images[0].ImageSum == sum) {
		return false, nil
	}

	// O_EXCL：绝不覆盖同名文件。若 Clear 后写盘失败、旧 <id>.png 还在，
	// 覆盖会让磁盘上的旧记录指向一张新图；换名即可保持两边一致。
	id := s.nextImageID
	name := fmt.Sprintf("%d.png", id)
	path := filepath.Join(s.imgDir, name)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if os.IsExist(err) {
		name = fmt.Sprintf("%d-%d.png", id, time.Now().UnixNano())
		path = filepath.Join(s.imgDir, name)
		f, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	}
	if err != nil {
		return false, err
	}
	if _, err := f.Write(png); err != nil {
		f.Close()
		_ = os.Remove(path)
		return false, err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
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
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 1
	}
	// 先用除法判越界，再做乘法：(page-1)*size 在 page 接近 MaxInt 时会静默
	// 溢出成负数，绕过 start>=total 并在下面的切片表达式里 panic。
	if int64(page-1) > int64(total)/int64(size) {
		return []Record{}, total
	}
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

	// 两个列表都是「最新在头」（prepend 插入，CreatedAt 单调不减），所以用
	// 双指针归并取前 n 条即可，不必每次捕获都复制全量再 O(n log n) 排序。
	out := make([]Record, 0, n)
	i, j := 0, 0
	for len(out) < n && (i < len(s.texts) || j < len(s.images)) {
		if j >= len(s.images) ||
			(i < len(s.texts) && s.texts[i].CreatedAt >= s.images[j].CreatedAt) {
			out = append(out, s.texts[i])
			i++
		} else {
			out = append(out, s.images[j])
			j++
		}
	}
	return out
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
// The settings.json write error is returned so callers can surface it instead
// of reporting success while the combo would silently revert on restart.
func (s *Store) SetHotkeys(qr, hist string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hotkeyQR = strings.ToLower(strings.TrimSpace(qr))
	s.hotkeyHistory = strings.ToLower(strings.TrimSpace(hist))
	return s.persistSettings()
}

// SetLimits updates the retention caps and trims the in-memory lists right
// away. Crucially it does NOT delete any image file yet: the trimmed-out
// records are stashed instead. Callers must Save() afterwards and then either
// CommitTrim() (save succeeded — files are now deleted) or RestoreTrim()
// (save failed — records go back to memory). This guarantees a failed save
// never leaves "records already deleted" behind. Values are clamped into the
// allowed bounds; settings.json is persisted under the lock.
func (s *Store) SetLimits(maxText, maxImage int) (trimmedText, trimmedImage int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.maxText = clampLimit(maxText, minTextRecords, maxTextRecords, DefaultMaxTextRecords)
	s.maxImage = clampLimit(maxImage, minImageRecords, maxImageRecords, DefaultMaxImageRecords)

	s.trimStashTexts = nil
	s.trimStashImages = nil
	trimmedText = trimList(&s.texts, s.maxText, func(r Record) {
		s.trimStashTexts = append(s.trimStashTexts, r)
	})
	trimmedImage = trimList(&s.images, s.maxImage, func(r Record) {
		s.trimStashImages = append(s.trimStashImages, r)
	})
	s.dirty = true

	// 配置立即落盘：即便紧接着崩溃，上限也不会退回旧值
	_ = s.persistSettings()
	return trimmedText, trimmedImage
}

// CommitTrim deletes the image files of the records stashed by SetLimits and
// drops the stash. Call it only after Save() has succeeded.
func (s *Store) CommitTrim() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.trimStashImages {
		s.removeImageFile(r)
	}
	s.trimStashTexts = nil
	s.trimStashImages = nil
}

// RestoreTrim puts the records stashed by SetLimits back into the in-memory
// lists (image files were never deleted). Call it when the Save() that should
// have persisted the new caps failed; the next Save() then writes the full
// history back out.
func (s *Store) RestoreTrim() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.trimStashTexts) > 0 {
		// trimList 从最旧开始移除，stash 因此是「旧→新」；放回前要反转，
		// 保持列表 newest-first 的顺序不变（RecentMixed 归并依赖它）。
		for i, j := 0, len(s.trimStashTexts)-1; i < j; i, j = i+1, j-1 {
			s.trimStashTexts[i], s.trimStashTexts[j] = s.trimStashTexts[j], s.trimStashTexts[i]
		}
		s.texts = append(s.texts, s.trimStashTexts...)
	}
	if len(s.trimStashImages) > 0 {
		for i, j := 0, len(s.trimStashImages)-1; i < j; i, j = i+1, j-1 {
			s.trimStashImages[i], s.trimStashImages[j] = s.trimStashImages[j], s.trimStashImages[i]
		}
		s.images = append(s.images, s.trimStashImages...)
	}
	if len(s.trimStashTexts) > 0 || len(s.trimStashImages) > 0 {
		s.dirty = true
	}
	s.trimStashTexts = nil
	s.trimStashImages = nil
}

// settingsSnapshot builds the persisted settings struct (caller holds s.mu).
func (s *Store) settingsSnapshot() Settings {
	return Settings{
		MaxText:       s.maxText,
		MaxImage:      s.maxImage,
		HotkeyQR:      s.hotkeyQR,
		HotkeyHistory: s.hotkeyHistory,
		Paused:        s.paused,
	}
}

// Paused reports whether recording is paused.
func (s *Store) Paused() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.paused
}

// SetPaused toggles recording and persists the choice so it survives a
// restart. The write is best-effort for the in-memory state but the error is
// surfaced to the caller (tray) and to the page via SaveError.
func (s *Store) SetPaused(on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paused = on
	return s.persistSettings()
}

// persistSettings writes settings.json. Callers must hold s.mu.
func (s *Store) persistSettings() error {
	err := writeJSONFile(s.settingsPath(), s.settingsSnapshot())
	if err != nil {
		s.noteSaveErr("保存设置", err)
	} else {
		s.saveErr = ""
	}
	return err
}

// SaveError returns the most recent write failure (empty when healthy), so the
// page can warn instead of silently losing records.
func (s *Store) SaveError() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.saveErr
}

// noteSaveErr records a failure for the UI. The detail (including absolute
// paths) goes to the log only — the UI string must stay generic so it cannot
// leak the local username / directory to LAN readers. Caller holds s.mu.
func (s *Store) noteSaveErr(what string, err error) {
	log.Printf("store: %s failed: %v", what, err)
	s.saveErr = fmt.Sprintf("%s失败，请稍后重试", what)
}

// collectOrphanImages removes PNGs in the images directory that no record
// references. It only runs when images.json loaded successfully — otherwise a
// broken metadata file would make every user image look "orphaned" — and it
// only deletes files that follow this program's naming scheme, so a PNG the
// user (or a backup restore) dropped into the directory survives.
func (s *Store) collectOrphanImages() {
	if !s.imagesMetaOK {
		return
	}
	want := make(map[string]bool, len(s.images))
	for _, r := range s.images {
		if r.Image != "" {
			want[filepath.Base(r.Image)] = true
		}
	}
	entries, err := os.ReadDir(s.imgDir)
	if err != nil {
		log.Printf("store: scan images dir: %v", err)
		return
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".png" || want[e.Name()] {
			continue
		}
		if !imageNameRE.MatchString(e.Name()) {
			// 非本程序命名（用户手放 / 备份多拷）的文件一律不动。
			continue
		}
		path := filepath.Join(s.imgDir, e.Name())
		if err := os.Remove(path); err != nil {
			log.Printf("store: remove orphan image %s: %v", e.Name(), err)
		} else {
			log.Printf("store: removed orphan image %s", e.Name())
		}
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
// Both payloads are marshalled (and both temp files written) before either
// file is switched over, so a marshal / write failure cannot leave one half
// of the history newer than the other. The final two renames are not a
// transaction, but a failure there is rare and dirty stays true for a retry.
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dirty {
		return nil
	}
	textData, err := json.MarshalIndent(orEmpty(s.texts), "", "  ")
	if err != nil {
		s.noteSaveErr("编码文本记录", err)
		return err
	}
	// imagesMetaOK=false 表示元数据损坏且无法从磁盘重建。此时绝不能把内存里
	// 的空列表写成 images.json —— 下次启动会把磁盘上所有原图当孤儿删光。
	// 宁可只保存文本，保留 .corrupt-* 留证，等用户手工修复。
	var imageData []byte
	if s.imagesMetaOK {
		imageData, err = json.MarshalIndent(orEmpty(s.images), "", "  ")
		if err != nil {
			s.noteSaveErr("编码图片记录", err)
			return err
		}
	}
	textTmp := s.textsPath() + ".tmp"
	imageTmp := s.imagesPath() + ".tmp"
	if err := stageFile(textTmp, textData); err != nil {
		s.noteSaveErr("写文本记录", err)
		return err
	}
	if s.imagesMetaOK {
		if err := stageFile(imageTmp, imageData); err != nil {
			_ = os.Remove(textTmp)
			s.noteSaveErr("写图片记录", err)
			return err
		}
	}
	if err := os.Rename(textTmp, s.textsPath()); err != nil {
		_ = os.Remove(textTmp)
		if s.imagesMetaOK {
			_ = os.Remove(imageTmp)
		}
		s.noteSaveErr("替换文本记录", err)
		return err
	}
	if s.imagesMetaOK {
		if err := os.Rename(imageTmp, s.imagesPath()); err != nil {
			_ = os.Remove(imageTmp)
			s.noteSaveErr("替换图片记录", err)
			return err
		}
	}
	s.dirty = false
	s.saveErr = ""
	return nil
}

func writeJSONFile(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data)
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := stageFile(tmp, data); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		log.Printf("store: rename %s: %v", tmp, err)
		return err
	}
	return nil
}

func stageFile(tmp string, data []byte) error {
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		log.Printf("store: write %s: %v", tmp, err)
		return err
	}
	return nil
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

// MaxDataDirBytes 是整个数据目录（记录、图片、日志、损坏备份）的软上限。
// 达到后 /api/add-* 拒绝新推送，避免无鉴权的局域网接口把磁盘打满；
// 本机剪贴板捕获不走这道检查，核心功能不受影响。
const MaxDataDirBytes = 1 << 30 // 1 GiB

// DiskUsage sums the sizes of every regular file under the data directory.
// It is called only on write endpoints, where a millisecond-level walk of a
// few hundred files is negligible next to the upload itself.
func (s *Store) DiskUsage() (int64, error) {
	var total int64
	// 单个文件在遍历中消失（3s 落盘 ticker 恰好 rename/删 .tmp）不应让
	// 整个统计失败：guardWrites 现在对统计错误是 fail-closed 的，一次偶发
	// ENOENT 就会误拒所有推送。只有数据目录本身读不到才返回错误。
	err := filepath.WalkDir(s.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == s.dir {
				return err
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		total += info.Size()
		return nil
	})
	return total, err
}
