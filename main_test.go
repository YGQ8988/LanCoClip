package main

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"testing"
)

func TestRotateLogIfNeeded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")

	// 小日志保持原位，不产生备份。
	if err := os.WriteFile(path, []byte("a few lines"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	rotateLogIfNeeded(path)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("small log should stay: %v", err)
	}
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Fatalf("small log should not create a backup, stat err = %v", err)
	}

	// 超过阈值就换一份，旧备份被覆盖。
	if err := os.WriteFile(path+".1", []byte("stale"), 0o644); err != nil {
		t.Fatalf("write backup: %v", err)
	}
	if err := os.WriteFile(path, make([]byte, maxLogBytes), 0o644); err != nil {
		t.Fatalf("write big log: %v", err)
	}
	rotateLogIfNeeded(path)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("oversized log should be moved away, stat err = %v", err)
	}
	got, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("read rotated log: %v", err)
	}
	if len(got) != maxLogBytes {
		t.Fatalf("backup size = %d, want %d (stale backup not replaced)", len(got), maxLogBytes)
	}
}

// 运行期按写入字节滚动：常驻托盘应用不能让 app.log 无界增长。
func TestRotatingLogRollsAtRuntime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	rl, err := newRotatingLog(path)
	if err != nil {
		t.Fatalf("newRotatingLog: %v", err)
	}
	chunk := bytes.Repeat([]byte("x"), 64<<10)
	for i := 0; i < maxLogBytes/len(chunk)+4; i++ {
		n, err := rl.Write(chunk)
		if err != nil || n != len(chunk) {
			t.Fatalf("Write #%d = (%d, %v)", i, n, err)
		}
	}
	rl.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat app.log: %v", err)
	}
	if info.Size() > maxLogBytes {
		t.Fatalf("app.log grew to %d bytes without rotation (max %d)", info.Size(), maxLogBytes)
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("rotation backup missing: %v", err)
	}
	// 关闭后仍可安全写入（log.SetOutput 可能还有在途日志）。
	if n, err := rl.Write([]byte("after close")); err != nil || n != len("after close") {
		t.Fatalf("Write after Close = (%d, %v)", n, err)
	}
}

// 指定 -data 时日志必须跟着数据目录走，不能写回默认目录。
func TestSetupFileLoggingUsesResolveDir(t *testing.T) {
	dir := t.TempDir()
	orig := log.Writer()
	defer log.SetOutput(orig)

	closeLog := setupFileLogging(dir)
	defer closeLog()
	log.Printf("audit-probe")

	data, err := os.ReadFile(filepath.Join(dir, "app.log"))
	if err != nil {
		t.Fatalf("log file not in resolved data dir: %v", err)
	}
	if !bytes.Contains(data, []byte("audit-probe")) {
		t.Fatalf("log line missing: %q", data)
	}
}
