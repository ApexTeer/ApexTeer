package filelock

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// The lock is the only thing serializing the panel, the TUI and the subscription
// service over one state file, and it had no test file at all. A lock that silently
// fails to exclude would turn every read-modify-write in the program into a
// lost-update race, so what is checked here is exclusion, not just that Acquire
// returns without an error.

func TestPathIsASidecarBesideTheTarget(t *testing.T) {
	// The lock is taken on a sidecar so the target's own replace-by-rename cycle
	// never fights the lock: locking the target itself would break the moment a
	// writer renamed a new file over it, because the lock follows the inode.
	target := filepath.Join("etc", "sing-box", "easysb-users.json")
	want := target + ".lock"
	if got := Path(target); got != want {
		t.Fatalf("Path(%q) = %q, want %q", target, got, want)
	}
	if Path(target) == target {
		t.Fatal("the lock must not be taken on the target itself")
	}
}

func TestAcquireCreatesTheDirectoryAndSidecar(t *testing.T) {
	// The stores live under /etc/sing-box, which does not exist on a fresh host and
	// is created by the first write. Acquire has to do the same or the first writer
	// fails on a missing directory rather than on anything real.
	target := filepath.Join(t.TempDir(), "nested", "dir", "easysb.conf")
	lock, err := Acquire(target)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer lock.Unlock()

	if _, err := os.Stat(Path(target)); err != nil {
		t.Fatalf("the sidecar was not created: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("Acquire created the target file itself (stat err %v); it must only create the sidecar", err)
	}
}

func TestSidecarIsRootOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows has no Unix permission bits.
		return
	}
	target := filepath.Join(t.TempDir(), "easysb-users.json")
	lock, err := Acquire(target)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer lock.Unlock()

	info, err := os.Stat(Path(target))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("sidecar mode = %04o, want 0600", got)
	}
}

func TestUnlockIsIdempotentAndNilSafe(t *testing.T) {
	// Callers defer Unlock next to a branch that can return before the lock was
	// taken, so a nil or already-released lock has to be a no-op rather than a panic.
	var nilLock *Lock
	if err := nilLock.Unlock(); err != nil {
		t.Fatalf("Unlock on a nil lock = %v", err)
	}
	target := filepath.Join(t.TempDir(), "x.json")
	lock, err := Acquire(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Unlock(); err != nil {
		t.Fatalf("first Unlock = %v", err)
	}
	if err := lock.Unlock(); err != nil {
		t.Fatalf("second Unlock = %v", err)
	}
}

func TestLockExcludesASecondHolder(t *testing.T) {
	target := filepath.Join(t.TempDir(), "easysb-users.json")

	// A holder that is not released for as long as this test runs.
	holder, err := Acquire(target)
	if err != nil {
		t.Fatalf("the first Acquire: %v", err)
	}
	defer holder.Unlock()

	// A second holder must block. The attempt is made in a goroutine so the test can
	// observe that it is still waiting instead of blocking forever, and the result is
	// carried out rather than just signalled: a bare close would report success for a
	// second Acquire that failed for some unrelated reason, which is the one thing
	// this test exists to distinguish.
	type result struct {
		lock *Lock
		err  error
	}
	acquired := make(chan result, 1)
	go func() {
		// The lock is reported and released before returning: sending and then
		// blocking on a signal would deadlock, because the test is waiting for this
		// very send. Releasing it here also means the sidecar is closed by the time
		// the test ends, which matters on Windows where an open handle blocks the
		// temporary directory's removal.
		second, err := Acquire(target)
		if err != nil {
			acquired <- result{err: err}
			return
		}
		acquired <- result{lock: second}
	}()

	// The first observation decides, but the failure is reported only after the lock
	// is released. Calling t.Fatalf from inside the select would run the deferred
	// Unlock during a Goexit and leave the second goroutine blocked on a lock nobody
	// is going to release, so a diagnosis would turn into a five second timeout.
	var (
		early     bool
		earlyErr  error
		earlyLock *Lock
	)
	select {
	case got := <-acquired:
		early, earlyErr, earlyLock = true, got.err, got.lock
	case <-time.After(300 * time.Millisecond):
		// Still waiting, which is the exclusion this package exists to provide.
	}

	// Release before asserting, whatever the outcome.
	holder.Unlock()

	if early {
		if earlyErr != nil {
			t.Fatalf("the second Acquire failed while the first lock was held: %v", earlyErr)
		}
		if earlyLock != nil {
			earlyLock.Unlock()
		}
		t.Fatal("a second Acquire succeeded while the first lock was still held")
	}

	select {
	case got := <-acquired:
		if got.err != nil {
			t.Fatalf("the second Acquire failed after the lock was released: %v", got.err)
		}
		if got.lock == nil {
			t.Fatal("the second Acquire reported success but returned no lock")
		}
		if err := got.lock.Unlock(); err != nil {
			t.Fatalf("releasing the second lock: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a second Acquire never completed after the first lock was released")
	}
}

func TestReacquireAfterReleaseSucceeds(t *testing.T) {
	// The read-modify-write cycle is acquire, load, mutate, save, release, over and
	// over. If a release did not actually free the lock, the second cycle would block.
	target := filepath.Join(t.TempDir(), "easysb-users.json")
	for i := 0; i < 3; i++ {
		lock, err := Acquire(target)
		if err != nil {
			t.Fatalf("cycle %d: Acquire: %v", i, err)
		}
		if err := lock.Unlock(); err != nil {
			t.Fatalf("cycle %d: Unlock: %v", i, err)
		}
	}
}
