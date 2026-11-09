//go:build !windows

package instancelock

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
)

// TestAcquireExclusive uses a child process: on BSD/macOS flock is per-process,
// so a second Acquire from the *same* process would not prove mutual exclusion.
func TestAcquireExclusive(t *testing.T) {
	if os.Getenv("LANOCLIP_LOCK_HELPER") == "1" {
		_, err := Acquire(os.Getenv("LANOCLIP_LOCK_DIR"))
		if errors.Is(err, ErrAlreadyRunning) {
			os.Exit(0)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "helper:", err)
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, "helper unexpectedly got the lock")
		os.Exit(1)
	}

	dir := t.TempDir()
	l, err := Acquire(dir)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestAcquireExclusive$")
	cmd.Env = append(os.Environ(), "LANOCLIP_LOCK_HELPER=1", "LANOCLIP_LOCK_DIR="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("second process should be rejected: %v\n%s", err, out)
	}

	if err := l.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	l2, err := Acquire(dir)
	if err != nil {
		t.Fatalf("Acquire after release: %v", err)
	}
	defer l2.Release()

	info, err := os.Stat(dir + "/" + LockFileName)
	if err != nil {
		t.Fatalf("stat lock file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("lock file mode = %v, want 0600", info.Mode().Perm())
	}
}
