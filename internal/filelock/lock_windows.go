//go:build windows

package filelock

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockFile takes an exclusive lock over the file and waits for the holder to release
// it. Windows has no flock, so the byte-range lock that does the same job is taken
// over the first byte.
func lockFile(f *os.File) error {
	overlapped := new(windows.Overlapped)
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, overlapped)
}

func unlockFile(f *os.File) error {
	overlapped := new(windows.Overlapped)
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, overlapped)
}
