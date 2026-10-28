package store

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newTestStore builds a Store backed by a throwaway directory.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return st
}

func TestStorePage(t *testing.T) {
	s := newTestStore(t)
	for i := 1; i <= 25; i++ {
		s.AddText(fmt.Sprintf("item-%d", i))
	}

	// 最新在前：item-25 … item-1
	p1, total := s.Page(KindText, 1, 10)
	if total != 25 || len(p1) != 10 {
		t.Fatalf("page1 = %d records, total = %d; want 10 / 25", len(p1), total)
	}
	if p1[0].Content != "item-25" || p1[9].Content != "item-16" {
		t.Fatalf("page1 boundaries wrong: %q … %q", p1[0].Content, p1[9].Content)
	}

	p3, _ := s.Page(KindText, 3, 10)
	if len(p3) != 5 || p3[0].Content != "item-5" || p3[4].Content != "item-1" {
		t.Fatalf("page3 wrong: len=%d first=%q last=%q", len(p3), p3[0].Content, p3[4].Content)
	}

	if extra, _ := s.Page(KindText, 9, 10); len(extra) != 0 {
		t.Fatalf("out-of-range page should be empty, got %d", len(extra))
	}
}

// 文本与图片分开存储、分开计数、分开分页。
func TestStoreKindsAreIndependent(t *testing.T) {
	s := newTestStore(t)
	for i := 1; i <= 25; i++ {
		s.AddText(fmt.Sprintf("t-%d", i))
	}
	for i := 1; i <= 5; i++ {
		if _, err := s.AddImage([]byte{byte(i), 'P', 'N', 'G', 0, 0}); err != nil {
			t.Fatalf("AddImage: %v", err)
		}
	}

	tc, ic := s.Counts()
	if tc != 25 || ic != 5 {
		t.Fatalf("counts = %d/%d, want 25/5", tc, ic)
	}

	// 图片分页独立于文本：5 张图共 1 页，不受 25 条文本影响
	ip, iTotal := s.Page(KindImage, 1, 10)
	if iTotal != 5 || len(ip) != 5 || ip[0].Kind != KindImage {
		t.Fatalf("image page = %d/%d, want 5/5", len(ip), iTotal)
	}
	if extra, _ := s.Page(KindImage, 2, 10); len(extra) != 0 {
		t.Fatalf("image page 2 should be empty, got %d", len(extra))
	}

	// 清空文本不影响图片
	s.Clear(KindText)
	tc, ic = s.Counts()
	if tc != 0 || ic != 5 {
		t.Fatalf("after clearing text: %d/%d, want 0/5", tc, ic)
	}

	// 清空图片会删掉磁盘文件
	path, ok := s.ImagePath(ip[0].ID)
	if !ok {
		t.Fatal("ImagePath should find the image")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("image file missing: %v", err)
	}
	s.Clear(KindImage)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("clearing images should delete the files")
	}
}

// 从页面复制不应产生重复记录：刚记下的同内容条目应被撤回
func TestDropRecentDuplicate(t *testing.T) {
	s := newTestStore(t)

	// 只有一条时不能撤回：这条就是原始记录本身
	if !s.AddText("dup") {
		t.Fatal("add failed")
	}
	if s.DropRecentText("dup", time.Minute) {
		t.Fatal("the only record with this content must never be dropped")
	}
	if tc, _ := s.Counts(); tc != 1 {
		t.Fatalf("texts = %d, want 1", tc)
	}

	// 真正重复（同内容存在更早的记录）时才撤回最新那条
	s.AddText("other")
	s.AddText("dup") // 现在 [dup, other, dup]
	if tc, _ := s.Counts(); tc != 3 {
		t.Fatalf("texts = %d, want 3", tc)
	}
	if !s.DropRecentText("dup", time.Minute) {
		t.Fatal("a duplicate of an earlier record should be dropped")
	}
	if tc, _ := s.Counts(); tc != 2 {
		t.Fatalf("texts = %d, want 2", tc)
	}

	if s.DropRecentText("missing", time.Minute) {
		t.Fatal("non-matching content must not be dropped")
	}
	if s.DropRecentText("other", -time.Second) {
		t.Fatal("outside the window nothing should be dropped")
	}

	// 图片：撤回时连文件一起删（同样要求存在更早的同内容记录）
	png := []byte{1, 2, 3, 'P', 'N', 'G'}
	if _, err := s.AddImage(png); err != nil {
		t.Fatal(err)
	}
	imgs, _ := s.Page(KindImage, 1, 10)
	if s.DropRecentImage(imgs[0].ImageSum, time.Minute) {
		t.Fatal("a unique image must never be dropped")
	}
	if _, err := s.AddImage([]byte{9, 9, 9}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddImage(png); err != nil {
		t.Fatal(err)
	}
	if !s.DropRecentImage(imgs[0].ImageSum, time.Minute) {
		t.Fatal("a duplicated image should be dropped")
	}
}

func TestStoreTextAndImages(t *testing.T) {
	s := newTestStore(t)

	if !s.AddText("alpha") {
		t.Fatal("first add should succeed")
	}
	if s.AddText("alpha") {
		t.Fatal("duplicate of latest should be rejected")
	}
	if s.AddText("") {
		t.Fatal("empty content should be rejected")
	}
	if !s.AddText("beta") {
		t.Fatal("second distinct add should succeed")
	}

	png := []byte{0x89, 'P', 'N', 'G', 1, 2, 3}
	added, err := s.AddImage(png)
	if err != nil || !added {
		t.Fatalf("AddImage = (%v, %v), want (true, nil)", added, err)
	}
	if added, _ := s.AddImage(png); added {
		t.Fatal("duplicate image should be rejected")
	}

	texts, tc := s.Page(KindText, 1, 10)
	images, ic := s.Page(KindImage, 1, 10)
	if tc != 2 || len(texts) != 2 || texts[0].Content != "beta" || texts[1].Content != "alpha" {
		t.Fatalf("text page wrong: %+v", texts)
	}
	if ic != 1 || len(images) != 1 || images[0].Kind != KindImage {
		t.Fatalf("image page wrong: %+v", images)
	}

	path, ok := s.ImagePath(images[0].ID)
	if !ok {
		t.Fatal("ImagePath should find the image record")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("image file missing on disk: %v", err)
	}

	s.Clear("")
	if tc, ic := s.Counts(); tc != 0 || ic != 0 {
		t.Fatalf("clear all left %d/%d", tc, ic)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("clear should delete image files")
	}
}

// 文本 / 图片各有一个号段：清空哪一类，那一类的 ID 就从 1 重新数，
// 另一类继续往后编号，互不影响（图片文件名 = <id>.png，清空时旧文件已删，
// 所以重号不会覆盖还在用的图）。
func TestClearRestartsIDsPerKind(t *testing.T) {
	s := newTestStore(t)
	for i := 1; i <= 3; i++ {
		s.AddText(fmt.Sprintf("t%d", i))
		if added, err := s.AddImage([]byte{0x89, 'P', 'N', 'G', byte(i)}); !added || err != nil {
			t.Fatalf("AddImage %d = (%v, %v)", i, added, err)
		}
	}
	// 两类各自编号，所以最新的文本 / 图片 ID 都是 3
	texts, _ := s.Page(KindText, 1, 10)
	imgs, _ := s.Page(KindImage, 1, 10)
	if texts[0].ID != 3 || imgs[0].ID != 3 {
		t.Fatalf("ids should be independent per kind, got text=%d image=%d", texts[0].ID, imgs[0].ID)
	}

	// 只清图片：图片回到 1，文本继续到 4
	s.Clear(KindImage)
	if added, _ := s.AddImage([]byte{0x89, 'P', 'N', 'G', 9}); !added {
		t.Fatal("AddImage after clearing images failed")
	}
	s.AddText("t4")
	texts, _ = s.Page(KindText, 1, 10)
	imgs, _ = s.Page(KindImage, 1, 10)
	if imgs[0].ID != 1 {
		t.Fatalf("image id should restart at 1 after clearing images, got %d", imgs[0].ID)
	}
	if texts[0].ID != 4 {
		t.Fatalf("text id should keep counting (4), got %d", texts[0].ID)
	}
	// 新图片写入的是清空后空出来的 1.png，内容必须是新这张
	p, ok := s.ImagePath(imgs[0].ID)
	if !ok {
		t.Fatal("new image record should resolve to a file")
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read image: %v", err)
	}
	if len(raw) == 0 || raw[len(raw)-1] != 9 {
		t.Fatalf("1.png should hold the newly uploaded image, got %v", raw)
	}

	// 只清文本：文本回到 1，图片继续到 2
	s.Clear(KindText)
	s.AddText("t1-again")
	if added, _ := s.AddImage([]byte{0x89, 'P', 'N', 'G', 8}); !added {
		t.Fatal("AddImage after clearing texts failed")
	}
	texts, _ = s.Page(KindText, 1, 10)
	imgs, _ = s.Page(KindImage, 1, 10)
	if texts[0].ID != 1 {
		t.Fatalf("text id should restart at 1 after clearing texts, got %d", texts[0].ID)
	}
	if imgs[0].ID != 2 {
		t.Fatalf("image id should keep counting (2), got %d", imgs[0].ID)
	}

	// 清空全部：两类都回到 1
	s.Clear("")
	s.AddText("fresh")
	if added, _ := s.AddImage([]byte{0x89, 'P', 'N', 'G', 7}); !added {
		t.Fatal("AddImage after clearing everything failed")
	}
	texts, _ = s.Page(KindText, 1, 10)
	imgs, _ = s.Page(KindImage, 1, 10)
	if texts[0].ID != 1 || imgs[0].ID != 1 {
		t.Fatalf("after clearing everything both ids should be 1, got text=%d image=%d", texts[0].ID, imgs[0].ID)
	}
}

// 保留条数可配置：调小后立即从最旧端修剪，图片连文件一起删。
func TestStoreSetLimitsTrimsOldestFirst(t *testing.T) {
	s := newTestStore(t)
	for i := 1; i <= 15; i++ {
		s.AddText(fmt.Sprintf("t-%d", i)) // 最新在前：t-15 … t-1
	}
	for i := 1; i <= 6; i++ {
		if _, err := s.AddImage([]byte{byte(i), 'P', 'N', 'G'}); err != nil {
			t.Fatalf("AddImage: %v", err)
		}
	}
	before, _ := s.Page(KindImage, 1, 10)
	oldestImgPath, ok := s.ImagePath(before[5].ID) // 第 6 新 = 最旧
	if !ok {
		t.Fatal("ImagePath should find the image")
	}

	trimmedText, trimmedImage := s.SetLimits(10, 5)
	if trimmedText != 5 || trimmedImage != 1 {
		t.Fatalf("trimmed = %d/%d, want 5/1", trimmedText, trimmedImage)
	}

	texts, tc := s.Page(KindText, 1, 10)
	if tc != 10 || len(texts) != 10 {
		t.Fatalf("texts after trim = %d/%d, want 10/10", tc, len(texts))
	}
	if texts[0].Content != "t-15" || texts[9].Content != "t-6" {
		t.Fatalf("oldest-first trim wrong: kept %q … %q", texts[0].Content, texts[9].Content)
	}

	images, ic := s.Page(KindImage, 1, 10)
	if ic != 5 || len(images) != 5 {
		t.Fatalf("images after trim = %d/%d, want 5/5", ic, len(images))
	}
	if images[0].ID != before[0].ID || images[4].ID != before[4].ID {
		t.Fatal("trim should keep the newest images")
	}
	if _, err := os.Stat(oldestImgPath); !os.IsNotExist(err) {
		t.Fatal("trimmed image file should be deleted from disk")
	}

	// 新上限立即对新增生效：再加 1 条文本就会挤掉最旧的
	s.AddText("t-16")
	texts, tc = s.Page(KindText, 1, 10)
	if tc != 10 || texts[9].Content != "t-7" {
		t.Fatalf("new cap not enforced on add: %d records, last = %q", tc, texts[len(texts)-1].Content)
	}

	// 越界值被夹进允许范围，而不是直接采用
	s.SetLimits(1, 1)
	if got := s.MaxText(); got != minTextRecords {
		t.Fatalf("MaxText after clamp = %d, want %d", got, minTextRecords)
	}
	if got := s.MaxImage(); got != minImageRecords {
		t.Fatalf("MaxImage after clamp = %d, want %d", got, minImageRecords)
	}
	s.SetLimits(99999, 99999)
	if got := s.MaxText(); got != maxTextRecords {
		t.Fatalf("MaxText after clamp = %d, want %d", got, maxTextRecords)
	}
	if got := s.MaxImage(); got != maxImageRecords {
		t.Fatalf("MaxImage after clamp = %d, want %d", got, maxImageRecords)
	}
}

// 设置的保留条数应落盘 settings.json，重启（重新 NewStore）后仍然有效。
func TestStoreSettingsPersist(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if s.MaxText() != DefaultMaxTextRecords || s.MaxImage() != DefaultMaxImageRecords {
		t.Fatalf("defaults = %d/%d, want %d/%d",
			s.MaxText(), s.MaxImage(), DefaultMaxTextRecords, DefaultMaxImageRecords)
	}
	s.SetLimits(50, 20)

	s2, err := NewStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if s2.MaxText() != 50 || s2.MaxImage() != 20 {
		t.Fatalf("persisted limits = %d/%d, want 50/20", s2.MaxText(), s2.MaxImage())
	}
}

// RecentMixed: text + image records merge into one newest-first list.
func TestStoreRecentMixed(t *testing.T) {
	s := newTestStore(t)
	// 交替写入：t1, i1, t2, i2, t3（AddText/AddImage 各自内部递增 createdAt）
	s.AddText("t1")
	if _, err := s.AddImage([]byte{1, 'P', 'N', 'G'}); err != nil {
		t.Fatalf("AddImage: %v", err)
	}
	s.AddText("t2")
	if _, err := s.AddImage([]byte{2, 'P', 'N', 'G'}); err != nil {
		t.Fatalf("AddImage: %v", err)
	}
	s.AddText("t3")

	got := s.RecentMixed(4)
	if len(got) != 4 {
		t.Fatalf("len = %d, want 4", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].CreatedAt < got[i].CreatedAt {
			t.Fatalf("not newest-first: %d < %d at %d", got[i-1].CreatedAt, got[i].CreatedAt, i)
		}
	}
	// 空库不 panic，返回空切片
	empty := newTestStore(t)
	if out := empty.RecentMixed(10); len(out) != 0 {
		t.Fatalf("empty store returned %d items", len(out))
	}
	if out := empty.RecentMixed(0); out == nil || len(out) != 0 {
		t.Fatalf("RecentMixed(0) should return empty slice, got %#v", out)
	}
}

// TestDefaultDir locks the on-disk folder name: renaming it would silently
// orphan every existing record, so it must stay in sync with the app name.
func TestDefaultDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if os.Getenv("XDG_CONFIG_HOME") != "" {
		t.Setenv("XDG_CONFIG_HOME", "")
	}
	t.Setenv(EnvDir, "")

	dir, err := DefaultDir()
	if err != nil {
		t.Fatalf("DefaultDir: %v", err)
	}
	want := filepath.Join(home, "Library", "Application Support", DefaultDirName)
	if dir != want {
		t.Fatalf("DefaultDir = %q, want %q", dir, want)
	}

	// NewStore("") must land in exactly the same place.
	st, err := NewStore("")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if st.dir != want {
		t.Fatalf("NewStore dir = %q, want %q", st.dir, want)
	}
}

// TestEnvDirOverride keeps the documented precedence: env var beats default.
func TestEnvDirOverride(t *testing.T) {
	iso := t.TempDir()
	t.Setenv(EnvDir, iso)

	st, err := NewStore("")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if st.dir != iso {
		t.Fatalf("NewStore dir = %q, want env dir %q", st.dir, iso)
	}
}
