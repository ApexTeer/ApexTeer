package user

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/EasySBTeam/EasySB/internal/state"
)

// crossProcEnv makes the test binary re-exec itself as a store writer in a fresh OS
// process. The in-process concurrency tests cannot cover this: they exercise one
// process's descriptors, and the whole point of the lock is that the panel, the TUI,
// the subscription service and the migration are separate processes.
const crossProcEnv = "EASYSB_CROSSPROC_STORE"

// TestMain runs the child writer before the ordinary test suite when the environment
// asks for it. The child does one locked read-modify-write and exits, so a failure
// there surfaces as a non-zero exit status in the parent.
func TestMain(m *testing.M) {
	if path := os.Getenv(crossProcEnv); path != "" {
		if err := crossProcessWrite(path, os.Getenv(crossProcEnv+"_ID")); err != nil {
			fmt.Fprintln(os.Stderr, "child writer:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// crossProcessWrite is the child's whole job: take the store lock, add one account,
// let it go. Nothing here is test-framework aware, because it runs in a child process
// with no *testing.T.
func crossProcessWrite(path, id string) error {
	// Bounded retry rather than one blocking call, so a broken lock shows up as a lost
	// update (the parent counts accounts) instead of hanging the suite.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		store, lock, err := Locked(path)
		if err != nil {
			return err
		}
		if err := store.Add(New("acct-"+id, []Selection{{Node: "n1", Protocol: state.ProtoAnyTLS}}, testNow)); err == nil {
			lock.Unlock()
			return nil
		}
		lock.Unlock()
		time.Sleep(2 * time.Millisecond)
	}
	return fmt.Errorf("gave up waiting for a free store")
}

// TestLockedExcludesAcrossProcesses is the cross-process counterpart of
// TestLockedKeepsConcurrentWriters. It starts real OS processes, each doing the same
// locked read-modify-write cycle, and asserts that every one of their changes is still
// in the file afterwards.
//
// This is the case the in-process test cannot reach. Sections 3.2 of
// docs/optimization-review.md measured it by hand with a throwaway build tagged
// nolockprobe; this test makes the cross-process property permanent. With the lock
// replaced by a no-op the writers read the same file and last-write-wins, so all but
// one change disappears.
func TestLockedExcludesAcrossProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip("starts several child processes")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the child re-exec is exercised on the platform the .deb targets")
	}
	path := filepath.Join(t.TempDir(), "users.json")
	if err := os.WriteFile(path, []byte(`{"version":2,"users":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	const writers = 6
	self, err := os.Executable()
	if err != nil {
		t.Skipf("cannot re-exec the test binary: %v", err)
	}

	var wg sync.WaitGroup
	errs := make([]error, writers)
	start := make(chan struct{})
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			cmd := exec.Command(self, "-test.run", "TestLockedExcludesAcrossProcesses")
			cmd.Env = append(os.Environ(),
				crossProcEnv+"="+path,
				crossProcEnv+"_ID="+fmt.Sprint(i),
			)
			errs[i] = cmd.Run()
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("child writer %d failed: %v", i, err)
		}
	}

	final, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if final.Len() != writers {
		t.Fatalf("store holds %d accounts after %d cross-process writers: a change was lost",
			final.Len(), writers)
	}
}
