package lancoclip

import "testing"

// newTestWatcher builds a lancoclipWatcher over a fake lancoclip. The lancoclip content
// is whatever set() was last given; nothing touches the real lancoclip.
func newTestWatcher() (*lancoclipWatcher, func(string), func() []string) {
	cur := ""
	var got []string
	w := &lancoclipWatcher{
		readText:  func() (string, error) { return cur, nil },
		readImage: func() []byte { return nil },
		sup:       NewSuppressor(),
		onText:    func(s string) { got = append(got, s) },
		onImage:   func([]byte) { got = append(got, "[image]") },
	}
	return w, func(s string) { cur = s }, func() []string { return got }
}

// 连续复制：每次 probe 之间内容都变了，三条都必须各自入库。
// 这正是 500ms 轮询会丢中间几条的场景。
func TestWatcherRecordsEveryRapidChange(t *testing.T) {
	w, set, got := newTestWatcher()
	w.seed()

	for _, s := range []string{"第一条", "第二条", "第三条"} {
		set(s)
		w.probe()
	}

	gotList := got()
	if len(gotList) != 3 {
		t.Fatalf("recorded %d entries (%v), want 3", len(gotList), gotList)
	}
	for i, want := range []string{"第一条", "第二条", "第三条"} {
		if gotList[i] != want {
			t.Errorf("entry %d = %q, want %q", i, gotList[i], want)
		}
	}
}

// 同一次剪贴板变更可能触发多次探针（信号合并 + 补一次），内容没变就不该再记。
func TestWatcherSkipsUnchangedContent(t *testing.T) {
	w, set, got := newTestWatcher()
	w.seed()

	set("同一段文本")
	w.probe()
	w.probe()
	w.probe()

	if n := len(got()); n != 1 {
		t.Fatalf("recorded %d entries, want 1", n)
	}
}

// 剪贴板内容就是启动时已有的那份 → 不当成新复制。
func TestWatcherSeedSkipsStartupContent(t *testing.T) {
	w, set, got := newTestWatcher()
	set("启动前就在剪贴板里")
	w.seed()
	w.probe()

	if n := len(got()); n != 0 {
		t.Fatalf("recorded %d entries, want 0", n)
	}
}

func TestWatcherIgnoresBlankText(t *testing.T) {
	w, set, got := newTestWatcher()
	w.seed()

	set("   \n\t ")
	w.probe()

	if n := len(got()); n != 0 {
		t.Fatalf("recorded %d entries, want 0", n)
	}
}

// 剪贴板切到图片再切回同一段文本，文本要能重新记录（否则「复制同一张图后再
// 复制原来的文本」会被当成没变）。
func TestWatcherImageThenSameText(t *testing.T) {
	cur := ""
	var got []string
	img := []byte(nil)
	w := &lancoclipWatcher{
		readText:  func() (string, error) { return cur, nil },
		readImage: func() []byte { return img },
		sup:       NewSuppressor(),
		onText:    func(s string) { got = append(got, "text:"+s) },
		onImage:   func([]byte) { got = append(got, "image") },
	}
	w.seed()

	setText := func(s string) { cur, img = s, nil }
	setImage := func(b []byte) { img = b }

	setText("原文本")
	w.probe()
	setImage([]byte{1, 2, 3})
	w.probe()
	setText("原文本")
	w.probe()

	want := []string{"text:原文本", "image", "text:原文本"}
	if len(got) != len(want) {
		t.Fatalf("recorded %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// 从服务页面自己复制的内容不应该再入库（suppressor 消费一次）。
func TestWatcherSuppressesPagePayload(t *testing.T) {
	w, set, got := newTestWatcher()
	w.seed()

	set("从页面复制的文本")
	w.sup.AddText("从页面复制的文本")
	w.probe()

	if n := len(got()); n != 0 {
		t.Fatalf("recorded %d entries, want 0", n)
	}

	// suppressor 是一次性的：同一内容再复制一次就应该正常记录
	set("别的文本")
	w.probe()
	set("从页面复制的文本")
	w.probe()
	if n := len(got()); n != 2 {
		t.Fatalf("recorded %d entries, want 2", n)
	}
}
