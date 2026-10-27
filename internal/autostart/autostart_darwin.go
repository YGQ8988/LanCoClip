//go:build darwin

package autostart

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// macOS 开机自启：在 ~/Library/LaunchAgents 写一个 LaunchAgent plist，
// 登录时由 launchd 拉起。启停 = 写入/删除 plist 文件，"是否启用" 以文件是否存在为准。
const (
	launchAgentLabel = "com.lanoclip"
	launchAgentDir   = "Library/LaunchAgents"
)

func launchAgentPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, launchAgentDir, launchAgentLabel+".plist"), nil
}

// launchProgram returns the command LaunchAgent should run: inside an .app
// bundle we ask `open` to launch the bundle (correct icon / LaunchServices
// registration); a bare binary is executed directly.
func launchProgram() (program string, args []string) {
	exe, err := os.Executable()
	if err != nil {
		return "", nil
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if i := strings.Index(exe, ".app/Contents/MacOS/"); i >= 0 {
		bundle := exe[:i+len(".app")]
		return "/usr/bin/open", []string{"-gj", bundle}
	}
	return exe, nil
}

func Supported() bool { return true }

func Enabled() bool {
	p, err := launchAgentPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}

func Set(on bool) error {
	p, err := launchAgentPath()
	if err != nil {
		return err
	}
	if !on {
		_ = exec.Command("launchctl", "unload", p).Run()
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}

	program, args := launchProgram()
	if program == "" {
		return fmt.Errorf("cannot resolve executable path")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(home, launchAgentDir), 0o755); err != nil {
		return err
	}
	// escapeXML 最小化转义路径中可能出现的特殊字符
	escape := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n<dict>\n")
	b.WriteString("  <key>Label</key><string>" + launchAgentLabel + "</string>\n")
	b.WriteString("  <key>ProgramArguments</key>\n  <array>\n")
	b.WriteString("    <string>" + escape.Replace(program) + "</string>\n")
	for _, a := range args {
		b.WriteString("    <string>" + escape.Replace(a) + "</string>\n")
	}
	b.WriteString("  </array>\n")
	b.WriteString("  <key>RunAtLoad</key><true/>\n")
	b.WriteString("  <key>KeepAlive</key><false/>\n")
	b.WriteString("  <key>ProcessType</key><string>Interactive</string>\n")
	b.WriteString("</dict>\n</plist>\n")
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		return err
	}
	// load 失败不报错：plist 已就位，下次登录必然生效；本次失败多见于
	// 同 label 已有旧实例，unload 后重试一次
	_ = exec.Command("launchctl", "unload", p).Run()
	if out, err := exec.Command("launchctl", "load", p).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl load: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
