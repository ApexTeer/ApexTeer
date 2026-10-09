package subd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/EasySBTeam/EasySB/internal/sysinfo"
)

// The subscription unit runs this same binary with --serve, and it has to name the
// installed panel rather than a scratch copy: a unit rewritten to a copy that is then
// deleted takes every client's subscription down with it.
func TestSubscriptionUnitRunsThePanel(t *testing.T) {
	reloaded := stubDaemonReload(t)
	path := filepath.Join(t.TempDir(), "easysb.service")
	if err := writeUnit(path, "/usr/local/bin/easysb"); err != nil {
		t.Fatalf("writeUnit: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	text := string(body)
	if !strings.Contains(text, "ExecStart=/usr/local/bin/easysb --serve") {
		t.Fatalf("the unit does not serve subscriptions from the installed panel:\n%s", text)
	}
	// The endpoint is what every client's subscription URL points at, so the unit has to
	// survive a reboot of the node it depends on.
	if !strings.Contains(text, "Restart=always") {
		t.Fatalf("the endpoint unit should restart on failure:\n%s", text)
	}
	if !strings.Contains(text, "Wants="+sysinfo.ServiceName+".service") {
		t.Fatalf("the endpoint should be started with the node:\n%s", text)
	}
	// A unit systemd has not been told about is a unit systemd will not start, so writing the
	// file and reloading the daemon are one operation.
	if *reloaded != 1 {
		t.Fatalf("writing a systemd unit reloaded the daemon %d times, want 1", *reloaded)
	}
}

// TestWriteUnitInstallsAtomically covers what moving the write onto atomicfile is
// for: the unit lands with the mode the init system expects whatever the caller's
// umask is, the parent directory is created when it is missing, and the write leaves
// no temporary file behind. A leftover temporary beside a unit directory is one more
// file to explain, and a 0600 unit is one systemd refuses to read.
//
// The atomicity itself is structural (write a temp file, fsync, rename) and is
// exercised by internal/atomicfile's tests; what this pins is that the unit writers
// actually go through it and clean up after themselves.
func TestWriteUnitInstallsAtomically(t *testing.T) {
	stubDaemonReload(t)
	// A directory that does not exist yet: the writer has to create it.
	path := filepath.Join(t.TempDir(), "system", "easysb.service")

	if err := writeUnit(path, "/usr/local/bin/easysb"); err != nil {
		t.Fatalf("writeUnit into a missing directory: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if runtime.GOOS != "windows" {
		if got := info.Mode().Perm(); got != 0o644 {
			t.Fatalf("unit mode = %04o, want 0644: systemd will not read another one", got)
		}
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != filepath.Base(path) {
			t.Fatalf("the write left %q behind", e.Name())
		}
	}
}

// stubDaemonReload swaps the init system's reload for a counter, so a test that writes a unit
// file never touches the machine it runs on. It returns a pointer to the count, because the
// counter outlives the call.
func stubDaemonReload(t *testing.T) *int {
	t.Helper()
	count := new(int)
	restore := daemonReload
	daemonReload = func() error {
		*count++
		return nil
	}
	t.Cleanup(func() { daemonReload = restore })
	return count
}
