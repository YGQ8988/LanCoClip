//go:build !windows && !darwin

package autostart

import "errors"

// 其他平台（linux 等）暂不支持图形界面下的自启配置。

func Supported() bool { return false }

func Enabled() bool { return false }

func Set(bool) error { return errors.New("autostart unsupported on this platform") }
