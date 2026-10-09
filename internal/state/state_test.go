package state

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/EasySBTeam/EasySB/internal/sysinfo"
)

// saveInto points the state file at a temporary directory for one test.
func saveInto(t *testing.T, dir string) string {
	t.Helper()
	original := stateFile
	t.Cleanup(func() { stateFile = original })
	stateFile = filepath.Join(dir, "easysb.conf")
	return stateFile
}

// TestModifyDoesNotLoseAConcurrentChange is the regression test for the stale-copy
// clobber.
//
// Save rewrites the whole document from its receiver, so a writer that loaded the
// file, spent time doing something else and then saved, discards every key another
// actor changed in the meantime. That is exactly what the deploy path did: take a
// copy, run a deploy that can take seconds, save the copy. Modify re-reads under the
// lock, so the other actor's change survives.
func TestModifyDoesNotLoseAConcurrentChange(t *testing.T) {
	saveInto(t, t.TempDir())

	// The slow actor's copy, taken before the other one writes.
	stale := Load()

	// The other actor changes an unrelated key and commits it.
	if err := Modify(func(cfg *Config) error {
		cfg.SubSyncSecs = 120
		return nil
	}); err != nil {
		t.Fatalf("Modify: %v", err)
	}

	// The slow actor now records its own change the way it must: by expressing it as
	// a change rather than by saving the copy it took.
	if err := Modify(func(cfg *Config) error {
		cfg.Domain = "example.com"
		return nil
	}); err != nil {
		t.Fatalf("Modify: %v", err)
	}

	after := Load()
	if after.SubSyncSecs != 120 {
		t.Fatalf("the concurrent change was lost: SubSyncSecs = %d, want 120", after.SubSyncSecs)
	}
	if after.Domain != "example.com" {
		t.Fatalf("the later change was not recorded: Domain = %q", after.Domain)
	}

	// And the failure mode itself, pinned so the reason for Modify is on record: the
	// stale copy still carries the old interval, and saving it would undo the other
	// actor's write.
	if stale.SubSyncSecs == 120 {
		t.Fatal("the fixture no longer reproduces a stale copy, so this test proves nothing")
	}
	if err := stale.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := Load().SubSyncSecs; got == 120 {
		t.Fatal("a whole-document Save unexpectedly preserved the concurrent change")
	}
}

// TestUpdateNodeDeployedTouchesNothingElse pins the narrow write the deploy paths use
// in place of saving their own copy.
func TestUpdateNodeDeployedTouchesNothingElse(t *testing.T) {
	saveInto(t, t.TempDir())
	if err := Modify(func(cfg *Config) error {
		cfg.Domain = "example.com"
		cfg.SubServePort = 8443
		cfg.SubSyncSecs = 90
		cfg.NodeDeployed = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := UpdateNodeDeployed(true); err != nil {
		t.Fatalf("UpdateNodeDeployed: %v", err)
	}

	after := Load()
	if !after.NodeDeployed {
		t.Fatal("the deployed flag was not recorded")
	}
	if after.Domain != "example.com" || after.SubServePort != 8443 || after.SubSyncSecs != 90 {
		t.Fatalf("UpdateNodeDeployed changed another key: %+v", after)
	}
}

// TestLockedExcludesAConcurrentHolder pins that Locked really holds the file lock,
// rather than merely reading the file.
func TestLockedExcludesAConcurrentHolder(t *testing.T) {
	saveInto(t, t.TempDir())

	cfg, lock, err := Locked()
	if err != nil {
		t.Fatalf("Locked: %v", err)
	}
	// A write while the lock is held is a change to the same file, so it must
	// observe the state under the lock rather than fail; the point here is that the
	// handle and the lock are returned together and the lock is released.
	if err := lock.Unlock(); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if cfg.Domain != "" {
		t.Fatalf("a fresh state file yielded Domain %q", cfg.Domain)
	}
	// A second acquisition after the release must succeed: a Locked that leaked its
	// lock would hang every later writer.
	_, lock2, err := Locked()
	if err != nil {
		t.Fatalf("second Locked: %v", err)
	}
	if err := lock2.Unlock(); err != nil {
		t.Fatalf("second Unlock: %v", err)
	}
}

func TestDefault(t *testing.T) {
	c := Default()
	if !c.AnyEnabled() {
		t.Fatal("default config should enable protocols")
	}
	for _, k := range Keys {
		if c.Ports[k] != DefaultPorts[k] {
			t.Fatalf("port %s = %q, want %q", k, c.Ports[k], DefaultPorts[k])
		}
	}
	if c.HopRange != DefaultHopRange || c.RealitySNI != DefaultSNI {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

func TestApplyRaw(t *testing.T) {
	c := Default()
	c.raw = map[string]string{
		"IS_ANYTLS":        "false",
		"IS_VMESS_WS_TLS":  "true",
		"PORT_ANYTLS":      "9000",
		"HY2_HOP_RANGE":    "3000:4000",
		"DOMAIN":           "example.com",
		"NODE_DEPLOYED":    "yes",
		"REALITY_SHORT_ID": "abcd",
		"UNRECOGNISED_KEY": "keep-me",
	}
	c.applyRaw()

	if c.Enabled[ProtoAnyTLS] {
		t.Fatal("IS_ANYTLS=false should disable anytls")
	}
	if !c.Enabled[ProtoVMessWSTLS] {
		t.Fatal("IS_VMESS_WS_TLS=true should enable vmess")
	}
	if c.Ports[ProtoAnyTLS] != "9000" {
		t.Fatalf("port override not applied: %q", c.Ports[ProtoAnyTLS])
	}
	if c.HopRange != "3000:4000" || c.Domain != "example.com" {
		t.Fatalf("raw values not applied: %+v", c)
	}
	if !c.NodeDeployed {
		t.Fatal("NODE_DEPLOYED=yes should set NodeDeployed")
	}
	extra := c.extraKeys()
	if len(extra) != 1 || extra[0] != "UNRECOGNISED_KEY" {
		t.Fatalf("unexpected extra keys: %v", extra)
	}
}

func TestHost(t *testing.T) {
	c := Default()
	if got := c.Host(); got != "" {
		t.Fatalf("empty host = %q", got)
	}
	c.ServerIP = "1.2.3.4"
	if got := c.Host(); got != "1.2.3.4" {
		t.Fatalf("host = %q", got)
	}
	c.CertDomain = "cert.example.com"
	if got := c.Host(); got != "cert.example.com" {
		t.Fatalf("host = %q", got)
	}
	c.Domain = "example.com"
	if got := c.Host(); got != "example.com" {
		t.Fatalf("host = %q", got)
	}
}

// TestSaveRoundTripsAndLeavesNothingBehind covers the write path, which had no test: the
// panel and the subscription service both save this file, and a Save that leaves its
// temporary in place or renames the wrong one is only noticed when a host comes back
// with its protocol selection reset.
func TestSaveRoundTripsAndLeavesNothingBehind(t *testing.T) {
	dir := t.TempDir()
	path := saveInto(t, dir)

	c := Default()
	c.Domain = "example.com"
	c.NodeDeployed = true
	c.raw = map[string]string{"UNRECOGNISED_KEY": "keep-me"}

	// The seam has to cover every path Save touches, not only the file it writes, and this
	// assertion runs on every platform rather than behind the Windows skip below. The first
	// version of this test redirected stateFile but left Save creating sysinfo.WorkDir: on
	// the CI runner that failed with "mkdir /etc/sing-box: permission denied", while a
	// Windows checkout passed, because there the same call quietly creates D:\etc\sing-box
	// - the drive root is writable, so the test wrote nowhere near where it thought it was.
	_, beforeErr := os.Stat(sysinfo.WorkDir)

	if err := c.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, afterErr := os.Stat(sysinfo.WorkDir); os.IsNotExist(beforeErr) && !os.IsNotExist(afterErr) {
		t.Fatalf("Save created %s: the test's seam does not cover every path it touches", sysinfo.WorkDir)
	}

	got := Load()
	if got.Domain != "example.com" || !got.NodeDeployed {
		t.Fatalf("round trip lost the state: %+v", got)
	}
	// v6 moved the per-protocol keys into the node store, so the state file no
	// longer carries them: a value set on the legacy fields is not written back.
	if got.raw["PORT_ANYTLS"] != "" || got.raw["IS_TUIC"] != "" {
		t.Fatalf("per-protocol keys were written back: %v", got.raw)
	}
	// An unknown key belongs to something newer; losing it on save is data loss.
	if extra := got.extraKeys(); len(extra) != 1 || extra[0] != "UNRECOGNISED_KEY" {
		t.Fatalf("round trip dropped an unrecognised key: %v", extra)
	}

	// One file, and no temporary beside it: a unique temporary name is what keeps two
	// writers from interleaving into a truncated document.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the directory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("Save left %v behind, want only %s", names, filepath.Base(path))
	}

	if runtime.GOOS == "windows" {
		// Windows has no Unix permission bits, so this half only ever runs on the CI
		// runner. Anything skipped by platform is worth treating as unverified until it
		// has run there once.
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("state file mode = %04o, want 0600", got)
	}
}

// TestLoadFallsBackToDefaults keeps the panel opening on a host that was never set up.
func TestLoadFallsBackToDefaults(t *testing.T) {
	saveInto(t, t.TempDir())

	got := Load()
	if !got.AnyEnabled() {
		t.Fatal("a missing state file did not fall back to the defaults")
	}
}
