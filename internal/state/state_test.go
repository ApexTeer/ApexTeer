package state

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/MinimaxFlora/EasySB/internal/sysinfo"
)

// saveInto points the state file at a temporary directory for one test.
func saveInto(t *testing.T, dir string) string {
	t.Helper()
	original := stateFile
	t.Cleanup(func() { stateFile = original })
	stateFile = filepath.Join(dir, "easysb.conf")
	return stateFile
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
	c.Ports[ProtoAnyTLS] = "9443"
	c.Enabled[ProtoTUIC] = false
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
	if got.Ports[ProtoAnyTLS] != "9443" {
		t.Fatalf("round trip lost the port override: %q", got.Ports[ProtoAnyTLS])
	}
	if got.Enabled[ProtoTUIC] {
		t.Fatal("round trip re-enabled a protocol that was turned off")
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
