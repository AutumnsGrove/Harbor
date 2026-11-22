// Package datalock is the "is the database in use?" question, answered
// portably (plans/12a).
//
// serve holds the lock for its whole life. A restore has to take it to replace
// the database, so a restore against a running Harbor fails instead of writing
// under a live connection. The lock is a file plus flock: it needs no pidfile,
// it cannot go stale after a crash, and it works the same in a container and on
// a Mac.
package datalock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// FileName is the lock's name inside a data directory.
const FileName = ".lock"

// ErrHeld means another process holds the lock: the database is in use.
var ErrHeld = errors.New("the data directory is in use")

// Lock is a held lock. Release it exactly once.
type Lock struct {
	file *os.File
}

// Acquire takes the exclusive lock in dataDir. It returns ErrHeld when another
// Harbor holds it, which is a normal, reportable state rather than a failure.
func Acquire(dataDir string) (*Lock, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("datalock: creating %s: %w", dataDir, err)
	}
	path := filepath.Join(dataDir, FileName)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("datalock: opening %s: %w", path, err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w (%s is held by another process)", ErrHeld, path)
		}
		return nil, fmt.Errorf("datalock: locking %s: %w", path, err)
	}
	return &Lock{file: file}, nil
}

// Release drops the lock. The file stays: deleting it would let two processes
// lock two different inodes with the same name.
func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	file := l.file
	l.file = nil
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_UN); err != nil {
		_ = file.Close()
		return fmt.Errorf("datalock: unlocking %s: %w", file.Name(), err)
	}
	return file.Close()
}
