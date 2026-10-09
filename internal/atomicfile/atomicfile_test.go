package atomicfile

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// This helper is the single implementation of the install sequence every state
// document in EasySB uses, so its own guarantees are what the stores rely on: the
// target is replaced whole, it lands with the requested mode, and no temporary
// sibling survives a successful call.

func TestWriteInstallsTheContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	data := []byte(`{"version":1}`)
	if err := Write(path, data, 0o600); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("content = %q, want %q", got, data)
	}
}

func TestWriteReplacesRatherThanTruncates(t *testing.T) {
	// The failure this guards against is a partially written target: a shorter
	// document replacing a longer one must leave no tail of the old content, which
	// is what an in-place overwrite that fails midway produces.
	path := filepath.Join(t.TempDir(), "state.json")
	long := []byte(strings.Repeat("a", 4096))
	if err := Write(path, long, 0o600); err != nil {
		t.Fatal(err)
	}
	short := []byte(`{"version":2}`)
	if err := Write(path, short, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, short) {
		t.Fatalf("the replaced file carries a tail of the previous content: %q", got)
	}
}

func TestWriteAppliesTheRequestedMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows has no Unix permission bits; the mode assertion is what Linux CI runs.
		return
	}
	path := filepath.Join(t.TempDir(), "state.json")
	if err := Write(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %04o, want 0600", got)
	}

	// The mode is applied to the temporary file before the rename, so replacing a
	// file an earlier version left wider has to narrow it back.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode after replacing a wider file = %04o, want 0600", got)
	}
}

func TestWriteLeavesNoTemporaryBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := Write(path, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "state.json" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("the directory holds %v, want only the target", names)
	}
}

func TestWriteReportsAMissingDirectoryRatherThanCreatingIt(t *testing.T) {
	// The parent directory is deliberately the caller's: the state files live under
	// /etc/sing-box at 0755, a per-domain certificate directory at 0700, and one
	// shared mode here would be wrong for one of them. A missing directory must be
	// reported, not silently created with the wrong mode.
	path := filepath.Join(t.TempDir(), "absent", "state.json")
	if err := Write(path, []byte("x"), 0o600); err == nil {
		t.Fatal("Write created its own parent directory")
	}
}

// TestWriteRemovesStaleTempsOfItsOwnTarget covers the housekeeping a successful write
// performs.
//
// A temporary file that survives the call can only be one a killed process left
// behind: every failure path removes its own. Those files hold the document's
// content - for the account store, every credential - so a host accumulated them
// silently. The age threshold is part of the contract rather than an implementation
// detail: writes serialize on the store's file lock and the lock is released between
// a process's writes, so a concurrent writer can legitimately have a fresh temporary
// file in the directory while this runs.
func TestWriteRemovesStaleTempsOfItsOwnTarget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	prefix := filepath.Base(path) + ".tmp-"

	stale := filepath.Join(dir, prefix+"abandoned")
	fresh := filepath.Join(dir, prefix+"inflight")
	other := filepath.Join(dir, "other.json.tmp-abandoned")
	for _, name := range []string{stale, fresh, other} {
		if err := os.WriteFile(name, []byte("leftover"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// The last two are old; the middle one is not. The third belongs to another
	// target entirely and must not be touched whatever its age.
	old := time.Now().Add(-2 * staleTempAge)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(other, old, old); err != nil {
		t.Fatal(err)
	}

	if err := Write(path, []byte("{}"), 0o600); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("an abandoned temporary file was left behind (stat err %v)", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("a fresh temporary file was removed, which would break a concurrent writer: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("another target's temporary file was removed: %v", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "{}" {
		t.Fatalf("the write itself did not land: %q (%v)", got, err)
	}
}

func TestWriteRejectsAnEmptyPath(t *testing.T) {
	// An empty path cannot be resolved to anything useful, and it must be refused
	// rather than resolved against the working directory: filepath.Dir("") is ".",
	// so without the guard this writes a real file into the process's own working
	// directory before failing on the rename.
	if err := Write("", []byte("x"), 0o600); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("Write(\"\") = %v, want %v", err, fs.ErrInvalid)
	}
}

// TestWriteModeIsIndependentOfTheUmask pins why Write applies the mode to the
// temporary file explicitly instead of relying on how that file was created.
// os.CreateTemp asks for 0600 and those bits are subject to the umask, and the
// stores are written as root from an interactive shell, a systemd unit and the
// install script, each with its own umask. A credential file that lands 0600 under
// one of those and unreadable under another only shows up on someone else's host.
//
// The umask cannot be varied here directly: it is process-wide, so narrowing it
// around the write would also apply to every other file any goroutine creates in
// that window - including the testing package's own temporary directories - and a
// flaky failure would say nothing about Write. What is checked instead is the
// invariant that makes the umask irrelevant: the mode Write leaves is exactly the
// one it was asked for, whether the target was absent or already existed at some
// other mode.
func TestWriteModeIsIndependentOfTheUmask(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows has no Unix permission bits.
		return
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	// A target that does not exist yet.
	if err := Write(path, []byte("first"), 0o640); err != nil {
		t.Fatalf("Write: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Fatalf("mode of a new target = %04o, want exactly 0640", got)
	}

	// A target that already exists at a different, wider mode: the replacement has
	// to carry the requested mode rather than inherit the old one.
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("second"), 0o600); err != nil {
		t.Fatalf("Write over an existing target: %v", err)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode after replacing a 0666 target = %04o, want exactly 0600", got)
	}
}
