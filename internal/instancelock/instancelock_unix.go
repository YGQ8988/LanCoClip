//go:build !windows

package instancelock

import (
	"os"
	"path/filepath"
	"syscall"
)

type fileLock struct{ f *os.File }

func acquire(dir string) (Lock, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	p := filepath.Join(dir, LockFileName)
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errorsIsWouldBlock(err) {
			return nil, ErrAlreadyRunning
		}
		return nil, err
	}
	return &fileLock{f: f}, nil
}

func errorsIsWouldBlock(err error) bool {
	return err == syscall.EWOULDBLOCK || err == syscall.EAGAIN
}

func (l *fileLock) Release() error {
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	return l.f.Close()
}
