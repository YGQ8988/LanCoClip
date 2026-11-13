package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
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
	// 新语义：SetLimits 只裁内存、不删文件——文件要等 Save 成功 +
	// CommitTrim 之后才真正删除，避免「保存失败但数据已丢」。
	if _, err := os.Stat(oldestImgPath); os.IsNotExist(err) {
		t.Fatal("trimmed image file must survive until Save+CommitTrim")
	}
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	s.CommitTrim()
	if _, err := os.Stat(oldestImgPath); !os.IsNotExist(err) {
		t.Fatal("trimmed image file should be deleted after CommitTrim")
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

// TestDiskUsage locks the water-mark helper: every file under the data
// directory (images/, JSON, logs) must count toward the limit.
func TestDiskUsage(t *testing.T) {
	st := newTestStore(t)
	if err := os.WriteFile(filepath.Join(st.dir, "blob.bin"), make([]byte, 4096), 0o644); err != nil {
		t.Fatalf("write blob: %v", err)
	}
	if err := os.WriteFile(filepath.Join(st.imgDir, "nested.png"), make([]byte, 2048), 0o644); err != nil {
		t.Fatalf("write image: %v", err)
	}
	used, err := st.DiskUsage()
	if err != nil {
		t.Fatalf("DiskUsage: %v", err)
	}
	if used < 4096+2048 {
		t.Fatalf("DiskUsage = %d, want >= %d", used, 4096+2048)
	}
}

// 暂停记录：不入库任何内容，且状态跨重启保持。
func TestPaused(t *testing.T) {
	st := newTestStore(t)
	if st.Paused() {
		t.Fatal("recording should be on by default")
	}
	if !st.AddText("first") {
		t.Fatal("AddText failed before pause")
	}
	if err := st.SetPaused(true); err != nil {
		t.Fatalf("SetPaused: %v", err)
	}
	if !st.Paused() {
		t.Fatal("Paused() should be true")
	}
	if st.AddText("secret") {
		t.Fatal("AddText must be ignored while paused")
	}
	if added, err := st.AddImage([]byte("image-bytes")); err != nil || added {
		t.Fatalf("AddImage while paused = (%v, %v), want (false, nil)", added, err)
	}
	if err := st.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	st2, err := NewStore(st.dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if !st2.Paused() {
		t.Fatal("paused state must survive a restart")
	}
	recs, total := st2.Page(KindText, 1, 10)
	if total != 1 || len(recs) != 1 || recs[0].Content != "first" {
		t.Fatalf("records after restart = %+v (total %d)", recs, total)
	}
	if err := st2.SetPaused(false); err != nil {
		t.Fatalf("unpause: %v", err)
	}
	if !st2.AddText("second") {
		t.Fatal("AddText should work again after resume")
	}
}

// 损坏的 JSON 不能静默清空历史：先改名留证，再回退默认值。
func TestCorruptJSONPreserved(t *testing.T) {
	st := newTestStore(t)
	st.AddText("hello")
	if err := st.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := os.WriteFile(st.textsPath(), []byte("{broken"), 0o644); err != nil {
		t.Fatalf("corrupt texts.json: %v", err)
	}
	st2, err := NewStore(st.dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := os.Stat(st.textsPath()); !os.IsNotExist(err) {
		t.Fatalf("broken file should be moved away, stat err = %v", err)
	}
	backups, _ := filepath.Glob(st.textsPath() + ".corrupt-*")
	if len(backups) != 1 {
		t.Fatalf("expected one corrupt backup, got %v", backups)
	}
	data, err := os.ReadFile(backups[0])
	if err != nil || string(data) != "{broken" {
		t.Fatalf("backup content = %q, err = %v", data, err)
	}
	if _, total := st2.Page(KindText, 1, 10); total != 0 {
		t.Fatalf("broken file should fall back to an empty store, total = %d", total)
	}
}

// 启动回收无主图片；但元数据损坏时宁可留着，绝不误删用户图片。
func TestCollectOrphanImages(t *testing.T) {
	st := newTestStore(t)
	if added, err := st.AddImage([]byte("image-1")); err != nil || !added {
		t.Fatalf("AddImage = (%v, %v)", added, err)
	}
	if err := st.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	orphan := filepath.Join(st.imgDir, "99.png")
	if err := os.WriteFile(orphan, []byte("x"), 0o644); err != nil {
		t.Fatalf("write orphan: %v", err)
	}
	// 用户手动放进目录的 PNG（不符合本程序命名规则）绝不能被回收。
	manual := filepath.Join(st.imgDir, "my-notes.png")
	if err := os.WriteFile(manual, []byte("keep"), 0o644); err != nil {
		t.Fatalf("write manual png: %v", err)
	}

	st2, err := NewStore(st.dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphan should be removed, stat err = %v", err)
	}
	if _, err := os.Stat(manual); err != nil {
		t.Fatalf("manually placed png must be kept: %v", err)
	}
	path, ok := st2.ImagePath(1)
	if !ok {
		t.Fatal("referenced image record lost")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("referenced image should survive: %v", err)
	}

	// 元数据损坏时跳过回收
	if err := os.WriteFile(st.imagesPath(), []byte("not json"), 0o644); err != nil {
		t.Fatalf("corrupt images.json: %v", err)
	}
	if err := os.WriteFile(orphan, []byte("x"), 0o644); err != nil {
		t.Fatalf("write orphan again: %v", err)
	}
	if _, err := NewStore(st.dir); err != nil {
		t.Fatalf("NewStore with broken metadata: %v", err)
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Fatalf("orphan must be kept when metadata is broken: %v", err)
	}
}

// 写盘失败要能被页面看到，恢复成功后提示要消失。
func TestSaveErrorVisible(t *testing.T) {
	st := newTestStore(t)
	st.AddText("x")
	// texts.json 的目标路径变成目录 → rename 必定失败
	if err := os.Mkdir(st.textsPath(), 0o755); err != nil {
		t.Fatalf("mkdir blocker: %v", err)
	}
	if err := st.Save(); err == nil {
		t.Fatal("Save should fail when target is a directory")
	}
	if st.SaveError() == "" {
		t.Fatal("SaveError should be visible to the page")
	}
	if err := os.Remove(st.textsPath()); err != nil {
		t.Fatalf("remove blocker: %v", err)
	}
	if err := st.Save(); err != nil {
		t.Fatalf("Save after recovery: %v", err)
	}
	if st.SaveError() != "" {
		t.Fatalf("SaveError should clear after a successful save, got %q", st.SaveError())
	}
}

// AddImage 绝不覆盖同名文件（Clear 后写盘失败时旧 <id>.png 可能还在）。
func TestAddImageNoOverwrite(t *testing.T) {
	st := newTestStore(t)
	existing := filepath.Join(st.imgDir, "1.png")
	if err := os.WriteFile(existing, []byte("original"), 0o644); err != nil {
		t.Fatalf("write existing: %v", err)
	}
	added, err := st.AddImage([]byte("new-image"))
	if err != nil || !added {
		t.Fatalf("AddImage = (%v, %v)", added, err)
	}
	data, err := os.ReadFile(existing)
	if err != nil || string(data) != "original" {
		t.Fatalf("existing file was overwritten: %q, err = %v", data, err)
	}
	path, ok := st.ImagePath(1)
	if !ok {
		t.Fatal("new record missing")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "new-image" {
		t.Fatalf("new image = %q, err = %v", got, err)
	}
}

// TestPageHugePageNoPanic locks the overflow guard: a page near MaxInt used to
// wrap (page-1)*size negative and panic on list[negative:negative].
func TestPageHugePageNoPanic(t *testing.T) {
	st := newTestStore(t)
	for i := 0; i < 3; i++ {
		st.AddText(fmt.Sprintf("record-%d", i))
	}
	huge := int(^uint(0) >> 1)
	for _, p := range []int{huge, huge / 2, 1 << 40, 2, -1} {
		got, total := st.Page(KindText, p, 10)
		if total != 3 {
			t.Fatalf("page %d: total = %d, want 3", p, total)
		}
		if p > 1 && len(got) != 0 {
			t.Fatalf("page %d: got %d records, want 0", p, len(got))
		}
	}
	if got, _ := st.Page(KindText, 1, 10); len(got) != 3 {
		t.Fatalf("page 1 = %d records, want 3", len(got))
	}
}

func TestDataFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows ACLs do not map to Unix mode bits")
	}
	dir := t.TempDir()
	st, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	st.AddText("secret")
	if err := st.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	for _, p := range []string{dir, filepath.Join(dir, "images"), filepath.Join(dir, "texts.json")} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		want := os.FileMode(0o600)
		if info.IsDir() {
			want = 0o700
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s mode = %v, want %v", p, info.Mode().Perm(), want)
		}
	}
}

func TestCleanStaleFiles(t *testing.T) {
	dir := t.TempDir()
	for i := 1; i <= 5; i++ {
		name := fmt.Sprintf("texts.json.corrupt-%019d", i)
		if err := os.WriteFile(filepath.Join(dir, name), []byte("broken"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"texts.json.tmp", "images.json.tmp"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("partial"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := NewStore(dir); err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var corrupt, tmp int
	for _, e := range entries {
		if strings.Contains(e.Name(), ".corrupt-") {
			corrupt++
		}
		if strings.HasSuffix(e.Name(), ".tmp") {
			tmp++
		}
	}
	if corrupt != 3 || tmp != 0 {
		t.Fatalf("after cleanup: corrupt=%d (want 3), tmp=%d (want 0)", corrupt, tmp)
	}
}

func TestRecentMixedMergesInOrder(t *testing.T) {
	st := newTestStore(t)
	st.mu.Lock()
	st.texts = []Record{
		{ID: 5, Kind: KindText, CreatedAt: 500},
		{ID: 3, Kind: KindText, CreatedAt: 300},
		{ID: 1, Kind: KindText, CreatedAt: 100},
	}
	st.images = []Record{
		{ID: 4, Kind: KindImage, CreatedAt: 400},
		{ID: 2, Kind: KindImage, CreatedAt: 200},
	}
	st.mu.Unlock()

	got := st.RecentMixed(4)
	want := []int64{5, 4, 3, 2}
	if len(got) != len(want) {
		t.Fatalf("RecentMixed(4) returned %d records, want %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("RecentMixed(4)[%d].ID = %d, want %d", i, got[i].ID, id)
		}
	}
	if len(st.RecentMixed(0)) != 0 || len(st.RecentMixed(99)) != 5 {
		t.Fatal("RecentMixed boundary cases wrong")
	}
}

// ── 审计回归：元数据损坏绝不能删用户图片 ──────────────────────────────

func countPNGFiles(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", dir, err)
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".png" {
			n++
		}
	}
	return n
}

// settings.json 损坏时回退默认上限（图片 200），旧实现会立刻 trimList
// 并真的 os.Remove 掉超出部分 —— 用户配过 1000 张就会静默丢 800 张。
func TestBrokenSettingsKeepsImageFiles(t *testing.T) {
	dir := t.TempDir()
	st, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	st.SetLimits(5000, 1000)
	const imageCount = 300
	for i := 0; i < imageCount; i++ {
		if _, err := st.AddImage([]byte(fmt.Sprintf("png-%d", i))); err != nil {
			t.Fatalf("AddImage(%d): %v", i, err)
		}
	}
	if err := st.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := countPNGFiles(t, filepath.Join(dir, "images")); got != imageCount {
		t.Fatalf("setup: %d png files, want %d", got, imageCount)
	}

	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatalf("break settings.json: %v", err)
	}
	st2, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore after corruption: %v", err)
	}
	if _, images := st2.Counts(); images != imageCount {
		t.Fatalf("after broken settings: %d image records, want %d", images, imageCount)
	}
	if got := countPNGFiles(t, filepath.Join(dir, "images")); got != imageCount {
		t.Fatalf("after broken settings: %d png files, want %d (files were deleted!)", got, imageCount)
	}
}

// images.json 损坏后要从磁盘重建元数据；否则一次 Save 会把空列表写成
// images.json，重启时 collectOrphanImages 把所有原图当孤儿删光。
func TestBrokenImagesMetaIsRebuiltAndSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	imgDir := filepath.Join(dir, "images")
	st, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, err := st.AddImage([]byte(fmt.Sprintf("img-%d", i))); err != nil {
			t.Fatalf("AddImage(%d): %v", i, err)
		}
	}
	if err := st.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "images.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatalf("break images.json: %v", err)
	}

	st2, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore after corruption: %v", err)
	}
	if _, images := st2.Counts(); images != 5 {
		t.Fatalf("rebuilt records = %d, want 5", images)
	}
	if got := countPNGFiles(t, imgDir); got != 5 {
		t.Fatalf("after rebuild: %d png files, want 5", got)
	}

	// 真实路径：本次会话不发生任何 AddText/AddImage/Clear（用户启动后只
	// 看了看历史就退出，或直接系统重启），只有 3s ticker 的 Save 兜底。
	// 重建必须把 images.json 写回磁盘，否则下次启动按「全新目录」把
	// 磁盘图片当孤儿删光。
	if err := st2.Save(); err != nil {
		t.Fatalf("ticker Save after rebuild: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "images.json"))
	if err != nil {
		t.Fatalf("rebuilt images.json should be persisted: %v", err)
	}
	var persisted []Record
	if err := json.Unmarshal(data, &persisted); err != nil || len(persisted) != 5 {
		t.Fatalf("persisted images.json = %d records (err %v), want 5", len(persisted), err)
	}

	st3, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore third time: %v", err)
	}
	if _, images := st3.Counts(); images != 5 {
		t.Fatalf("after restart: %d image records, want 5", images)
	}
	if got := countPNGFiles(t, imgDir); got != 5 {
		t.Fatalf("after restart: %d png files, want 5 (orphan collector deleted them!)", got)
	}
}

// images.json 被手工删除（而非损坏）但磁盘上还有图片时，同样必须从磁盘
// 重建，绝不能当成「全新目录」把现有图片当孤儿回收。
func TestMissingImagesJSONWithFilesOnDisk(t *testing.T) {
	dir := t.TempDir()
	imgDir := filepath.Join(dir, "images")
	st, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := st.AddImage([]byte(fmt.Sprintf("img-%d", i))); err != nil {
			t.Fatalf("AddImage(%d): %v", i, err)
		}
	}
	if err := st.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "images.json")); err != nil {
		t.Fatalf("remove images.json: %v", err)
	}

	st2, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore after removal: %v", err)
	}
	if _, images := st2.Counts(); images != 3 {
		t.Fatalf("rebuilt records = %d, want 3", images)
	}
	if got := countPNGFiles(t, imgDir); got != 3 {
		t.Fatalf("png files = %d, want 3 (files were deleted!)", got)
	}
	if err := st2.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	st3, err := NewStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if _, images := st3.Counts(); images != 3 {
		t.Fatalf("after reopen: %d image records, want 3", images)
	}
}

// SetLimits 的裁剪必须可回滚：Save 失败时 RestoreTrim 把被裁记录放回
// 内存，落盘恢复后写回的仍是完整历史（对应服务端「保存失败却已删数据」）。
func TestSetLimitsSaveFailureRestoresRecords(t *testing.T) {
	st := newTestStore(t)
	for i := 1; i <= 12; i++ {
		st.AddText(fmt.Sprintf("t-%02d", i))
	}
	// texts.json.tmp 建成目录 → stageFile 必定失败
	if err := os.Mkdir(st.textsPath()+".tmp", 0o755); err != nil {
		t.Fatalf("mkdir blocker: %v", err)
	}

	trimmed, _ := st.SetLimits(10, 5)
	if trimmed != 2 {
		t.Fatalf("trimmed = %d, want 2", trimmed)
	}
	if err := st.Save(); err == nil {
		t.Fatal("Save should fail with the blocker in place")
	}
	// 失败路径：回滚被裁记录
	st.RestoreTrim()
	if texts, _ := st.Counts(); texts != 12 {
		t.Fatalf("after RestoreTrim: %d texts, want 12", texts)
	}
	// 顺序也必须保持 newest-first（RecentMixed 归并依赖）
	page, _ := st.Page(KindText, 1, 12)
	if page[0].Content != "t-12" || page[11].Content != "t-01" {
		t.Fatalf("order broken after restore: %q … %q", page[0].Content, page[11].Content)
	}

	if err := os.Remove(st.textsPath() + ".tmp"); err != nil {
		t.Fatalf("remove blocker: %v", err)
	}
	st.SetLimits(10, 5)
	if err := st.Save(); err != nil {
		t.Fatalf("Save after recovery: %v", err)
	}
	st.CommitTrim()
	st2, err := NewStore(st.dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if texts, _ := st2.Counts(); texts != 10 {
		t.Fatalf("after commit: %d texts, want 10", texts)
	}
}

// 本机捕获的超长文本要截断到 512 KiB（与推送上限同源），避免 texts.json
// 与页面响应被单条记录撑爆。
func TestAddTextTruncatesHugePayload(t *testing.T) {
	st := newTestStore(t)
	big := strings.Repeat("a", maxTextPayloadBytes+100)
	st.AddText(big)
	page, total := st.Page(KindText, 1, 10)
	if total != 1 || len(page) != 1 {
		t.Fatalf("stored = %d, want 1", total)
	}
	if got := len(page[0].Content); got != maxTextPayloadBytes {
		t.Fatalf("stored length = %d, want %d", got, maxTextPayloadBytes)
	}
	// 多字节边界不产生半个 rune
	bigUTF := strings.Repeat("你好", (maxTextPayloadBytes/3)+50)
	st.AddText(bigUTF)
	page, _ = st.Page(KindText, 1, 10)
	if !utf8.ValidString(page[0].Content) {
		t.Fatal("truncated text must stay valid UTF-8")
	}
}

// 极端兜底：元数据不可信且无法从磁盘重建时，Save 只能写 texts.json，
// 绝不能用内存里的空列表覆盖 images.json。
func TestSaveSkipsImagesWhenMetadataUntrusted(t *testing.T) {
	dir := t.TempDir()
	st, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := st.AddImage([]byte("only-image")); err != nil {
		t.Fatalf("AddImage: %v", err)
	}
	if err := st.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	path := filepath.Join(dir, "images.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	st.mu.Lock()
	st.imagesMetaOK = false
	st.mu.Unlock()
	st.AddText("new text")
	if err := st.Save(); err != nil {
		t.Fatalf("Save with untrusted metadata: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile after: %v", err)
	}
	if string(before) != string(after) {
		t.Fatal("images.json was overwritten while metadata is untrusted")
	}
	if _, images := st.Counts(); images != 1 {
		t.Fatalf("in-memory image records = %d, want 1", images)
	}
}

// 收藏是文本记录上的标记：切换、计数、单独分页都只影响这一个字段。
func TestFavoriteToggleAndPaging(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	for i := 1; i <= 40; i++ {
		st.AddText(fmt.Sprintf("item-%02d", i))
	}
	all, total := st.Page(KindText, 1, 100)
	if total != 40 {
		t.Fatalf("texts = %d, want 40", total)
	}
	// 收藏三条：最新、中间、最旧
	for _, idx := range []int{0, 20, 39} {
		if found, changed := st.SetFavorite(all[idx].ID, true); !found || !changed {
			t.Fatalf("SetFavorite(%d) found=%v changed=%v", all[idx].ID, found, changed)
		}
	}
	// 重复收藏同一 ID 不应标记为变更
	if found, changed := st.SetFavorite(all[0].ID, true); !found || changed {
		t.Fatalf("idempotent SetFavorite found=%v changed=%v", found, changed)
	}
	if n := st.FavoriteCount(); n != 3 {
		t.Fatalf("FavoriteCount = %d, want 3", n)
	}
	// 收藏页每页 2 条：第 1 页 2 条、第 2 页 1 条，且 total 是收藏数而不是文本总数
	p1, ftotal := st.Page(KindFavorite, 1, 2)
	if ftotal != 3 || len(p1) != 2 {
		t.Fatalf("favorite page1 = %d/%d, want 2/3", len(p1), ftotal)
	}
	if !p1[0].Favorite || !p1[1].Favorite {
		t.Fatal("favorite page contains non-favourite records")
	}
	if p1[0].Content != "item-40" || p1[1].Content != "item-20" {
		t.Fatalf("favorite page1 order = %q,%q", p1[0].Content, p1[1].Content)
	}
	p2, _ := st.Page(KindFavorite, 2, 2)
	if len(p2) != 1 || p2[0].Content != "item-01" {
		t.Fatalf("favorite page2 = %v", p2)
	}
	// 取消收藏后收藏页立刻少一条，文本页保持不变
	if found, changed := st.SetFavorite(all[0].ID, false); !found || !changed {
		t.Fatalf("unfavorite found=%v changed=%v", found, changed)
	}
	if _, ftotal := st.Page(KindFavorite, 1, 10); ftotal != 2 {
		t.Fatalf("favorites after unfavorite = %d, want 2", ftotal)
	}
	if _, total := st.Page(KindText, 1, 100); total != 40 {
		t.Fatalf("texts changed by unfavorite: %d", total)
	}
	// 不存在的 ID：found=false，不改任何状态
	if found, changed := st.SetFavorite(9999, true); found || changed {
		t.Fatalf("unknown id found=%v changed=%v", found, changed)
	}
	// 收藏标记要能落盘并在重启后恢复（它和文本同文件）
	if err := st.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	st2, err := NewStore(st.dir)
	if err != nil {
		t.Fatalf("NewStore reopen: %v", err)
	}
	if n := st2.FavoriteCount(); n != 2 {
		t.Fatalf("favorites after restart = %d, want 2", n)
	}
}

// 收藏不受文本保留条数上限淘汰：用户点了收藏就是「这条我要留着」。
func TestFavoritesSurviveTextTrim(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	for i := 1; i <= 15; i++ {
		st.AddText(fmt.Sprintf("t%02d", i))
	}
	all, _ := st.Page(KindText, 1, 20)
	oldest := all[len(all)-1]
	if found, _ := st.SetFavorite(oldest.ID, true); !found {
		t.Fatalf("favorite %d: not found", oldest.ID)
	}

	// 上限收到 10：只淘汰最旧的非收藏（14 条非收藏 → 10 条），收藏那条保留
	trimmed, _ := st.SetLimits(10, 5)
	if trimmed != 4 {
		t.Fatalf("trimmed = %d, want 4 (favourite must not be trimmed)", trimmed)
	}
	texts, total := st.Page(KindText, 1, 20)
	if total != 11 {
		t.Fatalf("texts after trim = %d, want 11 (10 non-fav + 1 fav)", total)
	}
	found := false
	for _, r := range texts {
		if r.ID == oldest.ID {
			found = r.Favorite
		}
	}
	if !found {
		t.Fatal("favourite record was trimmed away")
	}
	if n := st.FavoriteCount(); n != 1 {
		t.Fatalf("FavoriteCount after trim = %d, want 1", n)
	}

	// 再追加新记录：非收藏数量保持在上限内，收藏继续留着
	st.AddText("t16")
	if n := st.FavoriteCount(); n != 1 {
		t.Fatalf("FavoriteCount after add = %d, want 1", n)
	}
	if _, total := st.Page(KindText, 1, 30); total != 11 {
		t.Fatalf("texts after add = %d, want 11", total)
	}
}

// 清空文本时保留收藏，且号段不能回退到会跟收藏撞号的位置。
func TestClearTextKeepsFavorites(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	for i := 1; i <= 3; i++ {
		st.AddText(fmt.Sprintf("c%d", i))
	}
	all, _ := st.Page(KindText, 1, 10)
	keep := all[1]
	if found, _ := st.SetFavorite(keep.ID, true); !found {
		t.Fatalf("favorite %d: not found", keep.ID)
	}

	st.Clear(KindText)
	texts, total := st.Page(KindText, 1, 10)
	if total != 1 || len(texts) != 1 || texts[0].ID != keep.ID {
		t.Fatalf("after clear: total=%d records=%v (want only the favourite)", total, texts)
	}
	st.AddText("after-clear")
	texts, total = st.Page(KindText, 1, 10)
	if total != 2 {
		t.Fatalf("after add: total=%d, want 2", total)
	}
	if texts[0].ID == keep.ID {
		t.Fatalf("new record reused the favourite's id %d", keep.ID)
	}

	// 取消收藏后再清空 → 一条不剩，号段归 1
	if found, _ := st.SetFavorite(keep.ID, false); !found {
		t.Fatalf("unfavorite %d: not found", keep.ID)
	}
	st.Clear(KindText)
	if total := func() int { _, n := st.Page(KindText, 1, 10); return n }(); total != 0 {
		t.Fatalf("after clear-all: total=%d, want 0", total)
	}
	st.AddText("fresh")
	if recs, _ := st.Page(KindText, 1, 10); recs[0].ID != 1 {
		t.Fatalf("first record after empty clear: id=%d, want 1", recs[0].ID)
	}
}

// 批量取消收藏：只清标记、不删记录；落盘并重启后仍无收藏，此后清空文本才真的清光。
func TestUnfavoriteAll(t *testing.T) {
	dir := t.TempDir()
	st, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	for i := 1; i <= 5; i++ {
		st.AddText(fmt.Sprintf("item-%d", i))
	}
	all, total := st.Page(KindText, 1, 10)
	if total != 5 {
		t.Fatalf("texts = %d, want 5", total)
	}
	for _, idx := range []int{0, 2, 4} {
		if found, changed := st.SetFavorite(all[idx].ID, true); !found || !changed {
			t.Fatalf("SetFavorite(%d) found=%v changed=%v", all[idx].ID, found, changed)
		}
	}

	ids := st.UnfavoriteAll()
	if len(ids) != 3 {
		t.Fatalf("UnfavoriteAll ids = %v, want 3", ids)
	}
	if n := st.FavoriteCount(); n != 0 {
		t.Fatalf("FavoriteCount = %d, want 0", n)
	}
	if _, n := st.Page(KindFavorite, 1, 10); n != 0 {
		t.Fatalf("favorite total = %d, want 0", n)
	}
	texts, total := st.Page(KindText, 1, 10)
	if total != 5 {
		t.Fatalf("texts after unfavorite-all = %d, want 5", total)
	}
	for _, r := range texts {
		if r.Favorite {
			t.Fatalf("record %d still favourite", r.ID)
		}
	}
	// 幂等：没有收藏时不返回 ID，也不重复标脏
	if ids := st.UnfavoriteAll(); len(ids) != 0 {
		t.Fatalf("second UnfavoriteAll = %v, want empty", ids)
	}
	// Save 失败回滚用的RestoreFavorites：不存在的 ID 忽略，已有的恢复标记
	st.RestoreFavorites([]int64{all[0].ID, 9999})
	if n := st.FavoriteCount(); n != 1 {
		t.Fatalf("FavoriteCount after restore = %d, want 1", n)
	}
	st.RestoreFavorites(nil)
	if ids := st.UnfavoriteAll(); len(ids) != 1 {
		t.Fatalf("cleanup UnfavoriteAll = %v, want 1", ids)
	}

	if err := st.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	st2, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore reopen: %v", err)
	}
	if n := st2.FavoriteCount(); n != 0 {
		t.Fatalf("favorites after restart = %d, want 0", n)
	}
	if _, n := st2.Page(KindText, 1, 10); n != 5 {
		t.Fatalf("texts after restart = %d, want 5", n)
	}
	// 标记没了以后，清空文本不再保留任何记录
	st2.Clear(KindText)
	if _, n := st2.Page(KindText, 1, 10); n != 0 {
		t.Fatalf("texts after clear = %d, want 0", n)
	}
}
