//go:build windows

package autostart

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows/registry"
)

// Windows 开机自启：当前用户注册表 Run 键
// HKCU\Software\Microsoft\Windows\CurrentVersion\Run，值名固定为 LanCoClip。
const runKeyName = "Software\\Microsoft\\Windows\\CurrentVersion\\Run"
const runValueName = "LanCoClip"

func Supported() bool { return true }

func Enabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyName, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	val, _, err := k.GetStringValue(runValueName)
	return err == nil && val != ""
}

func Set(on bool) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyName, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return fmt.Errorf("open run key: %w", err)
	}
	defer k.Close()
	if !on {
		// 值不存在时 DeleteValue 返回错误，视为已关闭
		if err := k.DeleteValue(runValueName); err != nil && Enabled() {
			return err
		}
		return nil
	}
	// 路径带空格必须整体加引号，否则 Run 键会按空格拆参数
	return k.SetStringValue(runValueName, `"`+exe+`"`)
}
