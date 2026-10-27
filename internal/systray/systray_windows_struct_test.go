//go:build windows

package systray

import (
	"testing"
	"unsafe"
)

// NOTIFYICONDATAW 的 uTimeout/uVersion 在 Windows SDK 里是联合体（4 字节），
// 如果误建为两个独立字段，结构体会比真实布局大 4 字节，InfoTitle/InfoFlags
// 全部错位——气泡通知（NIF_INFO）就会静默失败。这里锁定真实尺寸 976（x64）。
func TestNotifyIconDataSize(t *testing.T) {
	const want = 976 // sizeof(NOTIFYICONDATAW) on 64-bit
	if got := unsafe.Sizeof(notifyIconData{}); got != want {
		t.Fatalf("notifyIconData size = %d, want %d (struct layout drifted from NOTIFYICONDATAW)", got, want)
	}
}
