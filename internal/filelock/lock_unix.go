//go:build !windows

package filelock

import (
	"os"
	"syscall"
)

// lockFile takes an exclusive advisory lock on the descriptor and blocks until the
// holder releases it, which is what serializes two processes over one file.
func lockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
}

func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
