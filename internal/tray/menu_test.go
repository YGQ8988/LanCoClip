//go:build (darwin && cgo) || windows

package tray

import "testing"

// TestHistoryMenuGroups checks the popup layout: one top-level row per 10
// entries, titled with the item range, each holding numbered entries. Group g
// item i must stay addressable as items[g*HistoryGroupSize+i].
func TestHistoryMenuGroups(t *testing.T) {
	items := make([]Item, 25)
	for i := range items {
		items[i] = Item{Kind: "text", ID: int64(i + 1), Label: "x"}
	}

	groups := historyMenuGroups(items)
	if len(groups) != 3 {
		t.Fatalf("groups = %d, want 3", len(groups))
	}
	wantTitles := []string{"1-10", "11-20", "21-25"}
	wantSizes := []int{10, 10, 5}
	for i, g := range groups {
		if g.Title != wantTitles[i] {
			t.Errorf("group[%d].Title = %q, want %q", i, g.Title, wantTitles[i])
		}
		if len(g.Items) != wantSizes[i] {
			t.Errorf("group[%d] has %d items, want %d", i, len(g.Items), wantSizes[i])
		}
	}
	// 组内序号与全局序号对应：第二组第一项 = 第 11 条
	if got := groups[1].Items[0]; got != "11. x" {
		t.Errorf("groups[1].Items[0] = %q, want %q", got, "11. x")
	}
	if got := groups[2].Items[4]; got != "25. x" {
		t.Errorf("groups[2].Items[4] = %q, want %q", got, "25. x")
	}
	// 组/项 → 全局索引的换算
	if idx := 1*HistoryGroupSize + 0; idx != 10 {
		t.Errorf("index of groups[1].Items[0] = %d, want 10", idx)
	}
}

// TestHistoryMenuGroupsEmpty makes sure no group is emitted for no items.
func TestHistoryMenuGroupsEmpty(t *testing.T) {
	if groups := historyMenuGroups(nil); len(groups) != 0 {
		t.Fatalf("groups = %d, want 0", len(groups))
	}
}
