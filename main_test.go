package main

import (
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
