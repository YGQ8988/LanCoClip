package lancoclip

import (
	"testing"

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
