package systray

import "testing"

// 原生层加固逻辑通过 reportAnomaly 上报异常，tray 把它写进 app.log。
// 验证 hook 的注册与触发链路（原生侧的调用点见 systray_darwin.m）。
func TestAnomalyHook(t *testing.T) {
	SetAnomalyHook(nil) // 先清掉可能存在的其他 hook

	got := ""
	SetAnomalyHook(func(msg string) { got = msg })
	defer SetAnomalyHook(nil)

	reportAnomaly("duplicate item 26 (21-30) detected in menu tree")
	if got != "duplicate item 26 (21-30) detected in menu tree" {
		t.Fatalf("hook got %q", got)
	}

	// 注销后不再回调
	SetAnomalyHook(nil)
	called := false
	SetAnomalyHook(func(string) { called = true })
	SetAnomalyHook(nil)
	reportAnomaly("ignored")
	if called {
		t.Fatal("hook fired after unregister")
	}
}
