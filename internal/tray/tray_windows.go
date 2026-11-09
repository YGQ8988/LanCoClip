//go:build windows

package tray

import (
	_ "embed"
	"os/exec"
	"strings"

	"lanoclip/internal/hotkey"
	"lanoclip/internal/open"
	"lanoclip/internal/paste"
	"lanoclip/internal/qrpop"
	"lanoclip/internal/systray"
	"lanoclip/internal/winhide"
)

// stopPlatformLoops stops the Windows-only background loops (focus tracker,
// global hotkey message loop and QR popover UI thread) when the tray exits.
// All three are idempotent and safe to call before those loops ever started.
func stopPlatformLoops() {
	paste.StopFocusTracker()
	hotkey.StopHotkeyLoop()
	qrpop.StopQrPopover()
}

//go:embed menuicon_win.ico
var menuIconICO []byte

// Supported reports whether a native tray is available.
func Supported() bool { return true }

// Run shows the notification-area icon and blocks until the user quits.
func Run(app App, opts Options) {
	systray.Run(func() {
		systray.SetIcon(menuIconICO) // Windows 需要 .ico
		systray.SetTooltip(appTooltip)
		run(app, confirmClearAll, open.Open, opts)
	}, func() { stopMenuLoops(); stopPlatformLoops() })
}

// confirmClearAll shows a native confirmation dialog for "清除历史".
func confirmClearAll() bool {
	ps := `Add-Type -AssemblyName PresentationFramework; ` +
		`[System.Windows.MessageBox]::Show('确定清除全部剪贴板历史（文本和图片）？清除后无法恢复。', '清除历史', 'YesNo', 'Warning')`
	cmd := exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command", ps)
	winhide.Hide(cmd)
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "Yes"
}

// networkHint returns the Windows-only troubleshooting entry for "my phone
// cannot open the page".
//
// Windows Defender Firewall asks once whether to allow the inbound listener; if
// that prompt is dismissed — or the Wi-Fi is on the "Public" profile, which
// blocks inbound by default — the phone simply times out with no clue why.
// The tray menu is the only place a tray-only app can explain that.
func networkHint() (string, func()) {
	return "手机打不开？检查防火墙", showFirewallHelp
}

// showFirewallHelp explains the two settings that matter and offers to open the
// firewall control panel. It runs detached so a modal dialog cannot freeze the
// menu event loop.
func showFirewallHelp() {
	const ps = `$r=[System.Windows.MessageBox]::Show(` +
		`'手机打不开页面，通常是 Windows 防火墙拦住了入站连接。' + [char]10 + [char]10 + ` +
		`'1) 打开「Windows Defender 防火墙 - 允许应用通过防火墙」，勾选 LanCoClip 的「专用」；' + [char]10 + ` +
		`'2) 确认当前 Wi-Fi 在「设置 - 网络和 Internet - 属性」中被设为「专用网络」。' + [char]10 + [char]10 + ` +
		`'是否现在打开防火墙设置？', 'LanCoClip 网络访问', 'YesNo', 'Information'); ` +
		`if($r -eq 'Yes'){ Start-Process 'control.exe' 'firewall.cpl' }`
	cmd := exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command", ps)
	winhide.Hide(cmd)
	if err := cmd.Start(); err != nil {
		return
	}
	go func() { _ = cmd.Wait() }()
}

// Quit asks the tray loop to stop.
func Quit() { stopMenuLoops(); stopPlatformLoops(); systray.Quit() }
