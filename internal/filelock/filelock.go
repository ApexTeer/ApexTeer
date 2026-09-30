// Package filelock is an exclusive advisory lock on a path, shared by the processes
// that read-modify-write the same file. The lock is held on a sidecar beside the
// target, so the target's own create-and-rename cycle never fights the lock itself.
package filelock

import (
	"os"
	"path/filepath"
)

// Lock is a held exclusive lock. Release it with Unlock.
type Lock struct {
	file *os.File
}

// Path is the sidecar a target path is locked through. It holds no data; only the
// lock the kernel keeps on it matters.
func Path(target string) string {
	return target + ".lock"
}

// Acquire takes the exclusive lock for a target path, waiting for a holder to
// release it. The directory and the sidecar are created if they are missing.
func Acquire(target string) (*Lock, error) {
	if dir := filepath.Dir(target); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	f, err := os.OpenFile(Path(target), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, err
	}
	return &Lock{file: f}, nil
}

// Unlock releases the lock. A nil or already released lock is a no-op, so a caller
// can defer it next to the branch that returns before the lock was taken.
func (l *Lock) Unlock() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := unlockFile(l.file)
	if cerr := l.file.Close(); err == nil {
		err = cerr
	}
	l.file = nil
	return err
}
