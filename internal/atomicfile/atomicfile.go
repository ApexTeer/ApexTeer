// Package atomicfile installs a file the way every state document in EasySB has
// to be installed: fully written to a fresh temporary file beside the target,
// flushed, and then renamed over it.
//
// The target is the one path a concurrent reader opens, and a rename within one
// directory is atomic, so a reader sees either the previous document or the new
// one and never a half-written file. Replacing the content in place with
// os.WriteFile would leave the window this package exists to remove, and a fixed
// temporary name (target+".tmp") would let two writers interleave into one
// truncated document that is then renamed into place.
//
// This is the single implementation of that sequence. It used to be copied into
// each store, and the copies had drifted: the accounting baseline was written
// with a plain os.WriteFile, and none of them flushed before renaming.
package atomicfile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Write installs data at path with the given mode, creating the parent directory
// (0755) when it is missing and leaving an existing one's mode alone. See
// WriteKeepingBackup for why the write is done this way.
func Write(path string, data []byte, mode fs.FileMode) error {
	return write(path, data, mode, false)
}

// WriteKeepingBackup installs data at path with the given mode, first copying
// whatever the target holds to path+".bak".
//
// The write itself is already crash-safe, so the backup is not there for a torn
// write: it is there for a target that is later found corrupt. Corruption comes from
// outside - a disk fault, a hand edit, a half-restored backup - and the file it
// happens to is the one document the deployment cannot be rebuilt from. The copy is
// taken before the rename, so it holds the last content that was known good rather
// than a second copy of the new content.
//
// It costs one extra file copy per save. The stores that carry credentials and have
// no way to regenerate them use it; the ones that are derivable do not.
func WriteKeepingBackup(path string, data []byte, mode fs.FileMode) error {
	return write(path, data, mode, true)
}

// BackupPath is the sibling WriteKeepingBackup keeps the previous content in, and
// the file the stores fall back to when the target cannot be parsed.
func BackupPath(path string) string { return path + ".bak" }

func write(path string, data []byte, mode fs.FileMode, backup bool) error {
	if path == "" {
		// Not a path that can be resolved to something useful: filepath.Dir would
		// return "." and the write would land in the working directory before
		// failing on the rename.
		return fs.ErrInvalid
	}
	dir := filepath.Dir(path)
	// The parent is created the way os.WriteFile's callers have always expected: this
	// replaced that call in several places (the systemd unit writers, the BBR sysctl
	// drop-ins) where the directory can legitimately be missing on a host that has not
	// had the service installed before. Without this the temporary file cannot even be
	// created, so the failure would be a regression rather than a missing convenience.
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	f, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	// Written once and closed below, so the deferred close is a backstop for an
	// early return rather than the normal path.
	defer f.Close()

	if _, err := f.Write(data); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		os.Remove(tmp)
		return err
	}
	// The backup is taken while the new content is already safely in the temporary
	// file, so a failure here leaves the target untouched.
	if backup {
		if err := copyToBackup(path, mode); err != nil {
			os.Remove(tmp)
			return err
		}
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	syncDir(dir)
	// A temporary file that outlives the call can only be one a killed process left
	// behind: every failure path above removes its own. They are 0600 and hold the
	// document's content - for the account store, every credential - so they are
	// worth clearing rather than leaving on the disk of a host nobody looks at.
	removeStaleTemps(path)
	return nil
}

// staleTempAge is how old a leftover temporary file has to be before it is considered
// abandoned.
//
// It cannot be zero, or a write would delete the temporary file a concurrent writer
// is still filling: those writers serialize on the store's file lock, and the lock is
// released between a process's writes, so another process can legitimately have a
// fresh temporary file in the directory when this runs. Only the caller's own target
// prefix is considered, and an hour is far longer than the write itself, so the age
// is the safety margin rather than a heuristic.
const staleTempAge = time.Hour

// removeStaleTemps deletes leftover temporary files belonging to one target.
//
// Scope is the target's own prefix (base+".tmp-"), not every temporary file in the
// directory, so a writer never touches the temporary file of a target it does not
// own. Failures are ignored: this is housekeeping after a successful write, and
// failing the write because an old file could not be removed would be a worse
// outcome than leaving it.
func removeStaleTemps(path string) {
	dir := filepath.Dir(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	prefix := filepath.Base(path) + ".tmp-"
	cutoff := time.Now().Add(-staleTempAge)
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		os.Remove(filepath.Join(dir, e.Name()))
	}
}

// copyToBackup copies the current target to its backup path with the given mode. A
// target that does not exist yet has nothing to back up and is not an error: the
// first save of a file simply has no previous content.
func copyToBackup(path string, mode fs.FileMode) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if len(data) == 0 {
		// An empty target is not content worth restoring.
		return nil
	}
	return write(BackupPath(path), data, mode, false)
}

// syncDir flushes a directory entry so a completed rename survives a power loss.
// It is best-effort: the flush is unavailable or refused on some platforms and
// filesystems, and failing a write that is already installed would be worse than
// reporting it, because the only consumer of the file is already able to read it.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}
