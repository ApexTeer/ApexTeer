package panel

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/EasySBTeam/EasySB/internal/node"
	"github.com/EasySBTeam/EasySB/internal/user"
)

// emptyIfNil returns a non-nil empty slice so JSON encodes [] rather than null.
func emptyIfNil(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// statFile is os.Stat with the error kept, so callers can tell "missing" from
// "unreadable".
func statFile(path string) (os.FileInfo, error) {
	return os.Stat(path)
}

// runSystemctl runs one systemctl command with a timeout and returns its combined
// output. It never inherits a locale, so the output is parseable.
func runSystemctl(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "systemctl", args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	return strings.TrimRight(string(out), "\n"), err
}

// serviceEnabled reports whether a unit is enabled to start at boot.
func serviceEnabled(ctx context.Context, name string) bool {
	return exec.CommandContext(ctx, "systemctl", "is-enabled", "--quiet", name).Run() == nil
}

// tailLines returns at most n lines from the tail of a file, reading a bounded
// amount from the end so a multi-gigabyte log does not load into memory.
func tailLines(path string, n int, maxBytes int64) ([]string, error) {
	if n <= 0 {
		n = 200
	}
	if maxBytes <= 0 {
		maxBytes = 2 << 20
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	offset := int64(0)
	if size > maxBytes {
		offset = size - maxBytes
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	// A truncated tail may begin mid-line; drop that partial line.
	if offset > 0 && len(lines) > 1 {
		lines = lines[1:]
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}

// withNodeLock runs a read-modify-write against the node store under the store's
// file lock, so a concurrent writer in the panel or the subscription service
// cannot lose the change. It also takes the in-process mutex, which serializes the
// panel's own writers before they contend on the file lock.
func (s *Service) withNodeLock(fn func(*node.Store) error) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	store, lock, err := node.Locked(s.opts.NodesPath)
	if err != nil {
		return err
	}
	if err := fn(store); err != nil {
		lock.Unlock()
		return err
	}
	lock.Unlock()
	return nil
}

// withUserLock is the account-store counterpart of withNodeLock.
func (s *Service) withUserLock(fn func(*user.Store) error) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	store, lock, err := user.Locked(s.opts.UsersPath)
	if err != nil {
		return err
	}
	if err := fn(store); err != nil {
		lock.Unlock()
		return err
	}
	lock.Unlock()
	return nil
}
