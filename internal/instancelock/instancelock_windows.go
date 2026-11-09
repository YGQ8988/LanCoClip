//go:build windows

package instancelock

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

type fileLock struct{ h windows.Handle }

func acquire(dir string) (Lock, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	p := filepath.Join(dir, LockFileName)
	name, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return nil, err
	}
	// Only share READ: a second process asking for a writable handle fails
	// with ERROR_SHARING_VIOLATION. The OS closes the handle when this process
	// exits, so no stale lock file can block a later launch.
	h, err := windows.CreateFile(name,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ,
		nil, windows.CREATE_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		if err == windows.ERROR_SHARING_VIOLATION {
			return nil, ErrAlreadyRunning
		}
		return nil, err
	}
	return &fileLock{h: h}, nil
}

func (l *fileLock) Release() error {
	return windows.CloseHandle(l.h)
}
