// Package instancelock provides a cross-process lock on the data directory.
//
// The HTTP single-instance probe only protects the listening port; two
// processes launched at the same time can both miss each other and bind
// adjacent ports. They would then write the same texts.json / images.json and
// — worse — the second process's startup orphan cleanup could delete images
// the first one just wrote but had not yet flushed to images.json. An OS-level
// lock on the data directory closes that window: the lock is advisory and
// automatically released when the process exits (even on a crash), so there is
// no stale PID file to clean up.
package instancelock

import "errors"

// ErrAlreadyRunning means another live process holds the lock.
var ErrAlreadyRunning = errors.New("another instance is using this data directory")

// Lock is held for the lifetime of the process.
type Lock interface {
	Release() error
}

// LockFileName is the file the OS lock is taken on.
const LockFileName = "instance.lock"

// Acquire takes the lock on dir, creating the directory if needed.
// Callers should treat errors other than ErrAlreadyRunning as non-fatal
// (e.g. a filesystem that does not support locking) and log them.
func Acquire(dir string) (Lock, error) {
	return acquire(dir)
}
