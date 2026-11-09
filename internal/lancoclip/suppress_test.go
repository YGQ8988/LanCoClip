package lancoclip

import (
	"fmt"
	"testing"
	"time"

	"lanoclip/internal/store"
)

func TestSuppressorIsOneShot(t *testing.T) {
	sup := NewSuppressor()

	sup.AddText("hello")
	if !sup.TakeText("hello") {
		t.Fatal("registered text should be suppressed")
	}
	if sup.TakeText("hello") {
		t.Fatal("suppression must be one-shot")
	}
	if sup.TakeText("unrelated") {
		t.Fatal("unregistered text must not be suppressed")
	}

	sup.AddImageSum("sum-1")
	if !sup.TakeImageSum("sum-1") {
		t.Fatal("registered image should be suppressed")
	}
	if sup.TakeImageSum("sum-1") {
		t.Fatal("image suppression must be one-shot")
	}
}

// 从页面复制最新一条再粘贴回来：抑制登记 + 撤回逻辑协同——
// 只要存在更早的同内容记录才允许撤回，最新那条必须存活。
func TestSuppressCopyingNewestEntryKeepsIt(t *testing.T) {
	st, err := store.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	long := "【系统消息（tuhutest）】【企微同步失败】公司员工门店变更重试超过上限，companyEmployeeId=[27671]"
	st.AddText(long)

	sup := NewSuppressor()
	sup.AddText(long)
	if st.DropRecentText(long, suppressTTL) {
		t.Fatal("copying the newest entry from the page must not delete it")
	}
	if tc, _ := st.Counts(); tc != 1 {
		t.Fatalf("texts = %d, want 1 (record must survive)", tc)
	}
}

// AddText/AddImageSum 顺带清扫过期条目：从未被 Take 命中的条目不会永久滞留。
func TestSuppressorSweepsExpiredOnAdd(t *testing.T) {
	sup := NewSuppressor()
	now := time.Now()
	hFresh := store.SumHex([]byte("fresh"))

	sup.mu.Lock()
	sup.texts["stale-text-1"] = now.Add(-suppressTTL - time.Second)
	sup.texts["stale-text-2"] = now.Add(-suppressTTL - time.Hour)
	sup.texts[hFresh] = now.Add(-time.Second) // 未过期，应保留
	sup.imgs["stale-img"] = now.Add(-suppressTTL - time.Second)
	sup.mu.Unlock()

	sup.AddText("new text")
	sup.AddImageSum("new-sum")

	sup.mu.Lock()
	_, hasText1 := sup.texts["stale-text-1"]
	_, hasText2 := sup.texts["stale-text-2"]
	_, hasFresh := sup.texts[hFresh]
	_, hasNewText := sup.texts[store.SumHex([]byte("new text"))]
	_, hasStaleImg := sup.imgs["stale-img"]
	_, hasNewImg := sup.imgs["new-sum"]
	sup.mu.Unlock()

	if hasText1 || hasText2 {
		t.Error("AddText 应清扫掉过期的滞留条目")
	}
	if hasStaleImg {
		t.Error("AddImageSum 应清扫掉过期的滞留条目")
	}
	if !hasFresh {
		t.Error("未过期的条目不应被清扫")
	}
	if !hasNewText || !hasNewImg {
		t.Error("本次登记的新条目不应被清扫掉")
	}
}

// 硬上限：超出 suppressMaxEntries 时删除最旧条目，且保留较新的。
func TestSuppressorEnforcesHardCap(t *testing.T) {
	sup := NewSuppressor()
	now := time.Now()
	total := suppressMaxEntries + 50
	// 时间戳随序号递增（都在 TTL 内），序号 0 最旧。
	key := func(prefix string, i int) string { return fmt.Sprintf("%s-%d", prefix, i) }
	sup.mu.Lock()
	for i := 0; i < total; i++ {
		at := now.Add(-time.Duration(total-i) * time.Millisecond)
		sup.texts[key("t", i)] = at
		sup.imgs[key("i", i)] = at
	}
	sup.mu.Unlock()

	sup.AddText("after-cap")
	sup.AddImageSum("after-cap-img")

	sup.mu.Lock()
	nTexts, nImgs := len(sup.texts), len(sup.imgs)
	_, hasOldest := sup.texts[key("t", 0)]
	_, hasNewest := sup.texts[key("t", total-1)]
	_, hasOldestImg := sup.imgs[key("i", 0)]
	sup.mu.Unlock()

	if nTexts > suppressMaxEntries || nImgs > suppressMaxEntries {
		t.Fatalf("texts=%d imgs=%d，均不应超过 %d", nTexts, nImgs, suppressMaxEntries)
	}
	if hasOldest || hasOldestImg {
		t.Error("超出上限时应删除最旧的条目")
	}
	if !hasNewest {
		t.Error("不应删除较新的条目")
	}
}

// Take* 命中过期条目时删除并返回 false。
func TestSuppressorTakeExpiredRemovesEntry(t *testing.T) {
	sup := NewSuppressor()
	sup.AddText("stale")
	h := store.SumHex([]byte("stale"))
	sup.AddImageSum("stale-img")

	sup.mu.Lock()
	sup.texts[h] = time.Now().Add(-suppressTTL - time.Second)
	sup.imgs["stale-img"] = time.Now().Add(-suppressTTL - time.Second)
	sup.mu.Unlock()

	if sup.TakeText("stale") {
		t.Error("过期的文本条目不应被抑制")
	}
	if sup.TakeImageSum("stale-img") {
		t.Error("过期的图片条目不应被抑制")
	}

	sup.mu.Lock()
	_, textLeft := sup.texts[h]
	_, imgLeft := sup.imgs["stale-img"]
	sup.mu.Unlock()
	if textLeft || imgLeft {
		t.Error("命中过期条目后应删除")
	}
}
