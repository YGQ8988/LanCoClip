//go:build (darwin && cgo) || windows

package tray

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"lanoclip/internal/systray"

	"lanoclip/internal/histpop"
	"lanoclip/internal/hotkey"
	"lanoclip/internal/notify"
	"lanoclip/internal/paste"
	"lanoclip/internal/qrpop"
)

// startupNotifyDelay is how long to wait after the tray is up before showing the
// "ready" notification.
const startupNotifyDelay = 1200 * time.Millisecond

// appTooltip is the text shown when the mouse hovers the tray / menu-bar icon.
// Shared so macOS and Windows cannot drift apart.
const appTooltip = "LanCoClip"

// Shared menu wiring for every platform with a native tray (macOS / Windows).
// Platform files supply the icon, the "open URL" helper and the clear-history
// confirmation dialog, then hand control to run below.

var (
	historyMu   sync.RWMutex
	historyCur  []Item // latest snapshot shown in the menu
	historyMIs  []*systray.MenuItem
	historyInit bool
	// historyApplied 记录每个条目上次应用到原生菜单的状态：内容没变就不再
	// 调 SetTitle/Enable/Show（每次都会触发 systray 的原生更新，托盘菜单
	// 打开时也如此；减少无谓更新既省开销，也让异常日志只剩真正的变化）。
	historyApplied []historyRowState
)

type historyRowState struct {
	title    string
	disabled bool
	hidden   bool
}

// run builds the tray menu and pumps click events until quit. It must be
// called from inside systray.Run's onReady callback.
func run(app App, confirmClear func() bool, openURL func(string), opts Options) {
	mOpen := systray.AddMenuItem("打开页面", "在默认浏览器中打开剪贴板历史页面")
	// 访问二维码：鼠标移到这里就把手机扫码地址以浮窗形式贴在光标旁；
	// 点击则走浏览器里的大图（与 ⌘⇧Q / Ctrl+Shift+Q 一致）。
	//
	// tooltip 刻意留空：macOS 会在 hover 约 1 秒后弹出菜单项自身的系统
	// tooltip，正好盖在二维码浮窗上，而用途已经由浮窗表达了。
	mQR := systray.AddMenuItem("访问二维码", "")
	mSettings := systray.AddMenuItem("设置", "打开页面并弹出设置（文本/图片保留条数/）")

	// 历史：30 条，每 10 条一组（组名直接标号 1-10 / 11-20 / 21-30），
	// 点击条目复制回剪贴板并模拟粘贴。
	mHistory := systray.AddMenuItem("历史", "最近 30 条剪贴板记录，点击条目复制回剪贴板")
	var entries []*systray.MenuItem
	for g := 0; g < HistoryCount/HistoryGroupSize; g++ {
		start, end := g*HistoryGroupSize, (g+1)*HistoryGroupSize
		gi := mHistory.AddSubMenuItem(fmt.Sprintf("%d-%d", start+1, end), "")
		for i := 0; i < HistoryGroupSize; i++ {
			entries = append(entries, gi.AddSubMenuItem("（空）", ""))
		}
	}

	mClear := systray.AddMenuItem("清除历史", "清除全部文本和图片记录（需二次确认）")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("退出", "退出LanCoClip")

	historyMu.Lock()
	historyMIs = entries
	historyInit = true
	historyMu.Unlock()
	// 原生菜单层的加固逻辑（父节点缺失、意外新增、同 id 重复）写进应用日志：
	// 一旦线上再出现「组标题出现在记录里」之类的结构错乱，这里能直接看到现场。
	systray.SetAnomalyHook(func(msg string) { logf("tray menu anomaly: %s", msg) })
	UpdateHistory(app.Recent(HistoryCount))

	// 全局快捷键（服务本机生效）：
	//   macOS command+shift+Q / Windows ctrl+shift+Q → 打开页面并弹出二维码
	//   macOS command+shift+V / Windows ctrl+shift+V → 原生菜单弹出最近
	//   30 条历史（不唤起浏览器），点击复制回剪贴板；弹不出来时回退打开页面
	if err := hotkey.Install(
		func() { openURL(withParam(app.LocalURL(), "qr=1")) },
		func() { showHistoryPopupGuarded(app, openURL) },
		opts.HotkeyQR, opts.HotkeyHistory,
	); err != nil {
		logf("hotkeys: %v", err)
	}

	// 启动提示：就绪后弹一条系统通知，点一下直接打开记录页面。
	// 稍等一会儿再发——Windows 的气泡要求托盘图标已经注册完成，
	// macOS 上也能避开启动瞬间的状态栏图标重建。
	go func() {
		time.Sleep(startupNotifyDelay)
		if err := notify.Notify("LanCoClip已就绪", "点击打开剪贴板记录页面", func() {
			openURL(app.LocalURL())
		}); err != nil {
			logf("startup notification: %v", err)
		}
	}()

	// 每个历史条目一个 goroutine：点击时取当前快照里对应位置的数据复制回剪贴板，
	// 并向当前焦点应用模拟一次粘贴（与快捷键历史菜单行为一致）。
	for i := range entries {
		go func(idx int) {
			for range entries[idx].ClickedCh {
				historyMu.RLock()
				var it Item
				if idx < len(historyCur) {
					it = historyCur[idx]
				}
				historyMu.RUnlock()
				if it.ID == 0 {
					continue
				}
				copyAndPaste(app, it)
			}
		}(i)
	}

	go func() {
		for {
			select {
			case <-mOpen.ClickedCh:
				openURL(app.LocalURL())
			case hovered := <-mQR.HoveredCh:
				if hovered {
					showQRPopover(app)
				} else {
					// 移开（或菜单关了）就收起：浮窗是被动的，不能赖在屏幕上
					qrpop.Hide()
				}
			case <-mQR.ClickedCh:
				// 点击时菜单会关掉，浮窗随之消失，所以点击走的是浏览器大图——
				// 同时也兜住了「浮窗在当前系统不可用」的情况。
				openURL(withParam(app.LocalURL(), "qr=1"))
			case <-mSettings.ClickedCh:
				u := app.LocalURL()
				if u != "" && !strings.Contains(u, "?") {
					u += "?settings=1"
				}
				openURL(u)
			case <-mClear.ClickedCh:
				if confirmClear() {
					if err := app.ClearAll(); err != nil {
						logf("clear all: %v", err)
					}
					UpdateHistory(app.Recent(HistoryCount))
				}
			case <-mQuit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()
}

// showQRPopover pops the phone-scannable address as a QR code beside the
// pointer. Hovering is the whole point of the entry, so nothing happens on
// failure: we must not open a browser just because the user moved the mouse.
func showQRPopover(app App) {
	if !qrpop.Supported() {
		return
	}
	if err := qrpop.Show(app.URL()); err != nil {
		// 地址还没定（端口未定/无网卡）时每次 hover 都会失败，只记一次。
		qrErrOnce.Do(func() { logf("tray qr popover: %v", err) })
	}
}

var qrErrOnce sync.Once

// withParam appends "p" to the page URL as a query parameter (?settings=1 风格).
func withParam(u, p string) string {
	if u == "" || strings.Contains(u, "?") {
		return u
	}
	return u + "?" + p
}

// showHistoryPopupGuarded wraps showHistoryPopup with the re-entry guard:
// within pasteGuardWindow after a simulated paste, hotkey triggers are
// ignored (they can only be echoes of the synthetic keystroke).
func showHistoryPopupGuarded(app App, openURL func(string)) {
	if pasteGuardActive() {
		logf("hotkey history: ignored (paste echo guard)")
		return
	}
	showHistoryPopup(app, openURL)
}

// showHistoryPopup pops a native menu with the latest 30 entries at the
// cursor; picking one copies it back to the lancoclip (same as the tray
// 历史 submenu, but without opening a browser). Falls back to the web page
// when no native popup is available.
func showHistoryPopup(app App, openURL func(string)) {
	items := app.Recent(HistoryCount)
	if len(items) == 0 {
		// 没有记录时弹个提示，避免按下快捷键毫无反馈
		hint := []histpop.Group{{Title: "（空）暂无剪贴板记录"}}
		if !histpop.Show(hint, nil) {
			openURL(withParam(app.LocalURL(), "history=1"))
		}
		return
	}
	// 与托盘「历史」子菜单一致：顶层只有 1-10 / 11-20 / 21-30 三行，鼠标移到
	// 某一行才展开该组的 10 条内容，条目带序号（1. 2. …）。
	groups := historyMenuGroups(items)
	shown := histpop.Show(groups, func(g, i int) {
		idx := g*HistoryGroupSize + i
		if idx < 0 || idx >= len(items) {
			return
		}
		copyAndPaste(app, items[idx])
	})
	if !shown {
		openURL(withParam(app.LocalURL(), "history=1"))
	}
}

// historyMenuGroups chops history items into popup groups: one top-level row
// per HistoryGroupSize block, titled with the item range ("1-10", "11-20", …),
// holding that block's numbered entries ("1. …"). Groups are filled strictly
// in order, so group g's item i is items[g*HistoryGroupSize+i].
func historyMenuGroups(items []Item) []histpop.Group {
	groups := make([]histpop.Group, 0, (len(items)+HistoryGroupSize-1)/HistoryGroupSize)
	for start := 0; start < len(items); start += HistoryGroupSize {
		end := start + HistoryGroupSize
		if end > len(items) {
			end = len(items)
		}
		g := histpop.Group{Title: fmt.Sprintf("%d-%d", start+1, end)}
		for i := start; i < end; i++ {
			g.Items = append(g.Items, fmt.Sprintf("%d. %s", i+1, items[i].Label))
		}
		groups = append(groups, g)
	}
	return groups
}

// pasteDelayByKind returns how long to wait after writing the lancoclip
// before simulating the paste. Images need longer: the lancoclip write and
// the receiving app's image handling settle slower than plain text.
func pasteDelayByKind(kind string) time.Duration {
	if kind == "image" {
		return 400 * time.Millisecond
	}
	return 120 * time.Millisecond
}

// copyAndPaste copies the entry back to the lancoclip and then simulates a
// paste (⌘V / Ctrl+V) into the currently focused app, so the content lands
// directly at the cursor. Used by both the hotkey history popup and the tray
// history submenu.
func copyAndPaste(app App, it Item) {
	// 点击即进入抑制窗口：模拟粘贴的合成按键万一仍被系统级快捷键机制
	// 拦截误判（重新变成 ⌘⇧V），历史菜单不会被再次弹出。
	muPasteGuard.Lock()
	lastPasteAt = time.Now()
	muPasteGuard.Unlock()
	// 冻结焦点记录：从现在到粘贴发出之间，前台可能短暂变成托盘/外壳窗口
	paste.FreezeFocus()
	if err := app.CopyItem(it); err != nil {
		logf("copy item: %v", err)
		return
	}
	go func() {
		time.Sleep(pasteDelayByKind(it.Kind))
		if err := paste.Do(); err != nil {
			logf("simulate paste: %v", err)
		}
		muPasteGuard.Lock()
		lastPasteAt = time.Now()
		muPasteGuard.Unlock()
	}()
}

// lastPasteAt / muPasteGuard 构成重入保险：模拟粘贴的合成按键万一仍被系统
// 混入残留修饰键（重新变成 ⌘⇧V），会再次触发历史快捷键。粘贴发出后的短
// 窗口内忽略重复触发，避免菜单自己把自己又弹出来。
var (
	muPasteGuard sync.Mutex
	lastPasteAt  time.Time
)

const pasteGuardWindow = 500 * time.Millisecond

func pasteGuardActive() bool {
	muPasteGuard.Lock()
	defer muPasteGuard.Unlock()
	return time.Since(lastPasteAt) < pasteGuardWindow
}

// UpdateHistory refreshes the tray history entries. Safe to call from any
// goroutine at any time; it is a no-op before the menu is built.
func UpdateHistory(items []Item) {
	historyMu.Lock()
	defer historyMu.Unlock()
	if !historyInit {
		return
	}
	if len(items) > HistoryCount {
		items = items[:HistoryCount]
	}
	historyCur = append([]Item(nil), items...)
	if len(historyApplied) != len(historyMIs) {
		// 零值（title 空、enabled、shown）与创建占位条目时的原生状态一致，
		// 第一轮比较自然会把所有差异应用一遍。
		historyApplied = make([]historyRowState, len(historyMIs))
	}
	for i, mi := range historyMIs {
		var want historyRowState
		if i < len(historyCur) && (historyCur[i].Kind == "text" || historyCur[i].Kind == "image") {
			want = historyRowState{title: historyCur[i].Label}
		} else {
			want = historyRowState{title: "（空）", disabled: true, hidden: true}
		}
		applied := historyApplied[i]
		if want.title != applied.title {
			mi.SetTitle(want.title)
		}
		if want.disabled != applied.disabled {
			if want.disabled {
				mi.Disable()
			} else {
				mi.Enable()
			}
		}
		if want.hidden != applied.hidden {
			if want.hidden {
				mi.Hide()
			} else {
				mi.Show()
			}
		}
		historyApplied[i] = want
	}
}
