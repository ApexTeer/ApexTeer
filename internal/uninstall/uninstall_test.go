package uninstall

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EasySBTeam/EasySB/internal/sysinfo"
)

// TestWithin is the guard that decides whether the certificate state directory has
// to be moved aside at all: a directory under the work directory goes away with it,
// so it has to be rescued first, while one the operator moved elsewhere with
// EASYSB_ACME_DIR is already safe.
func TestWithin(t *testing.T) {
	acme := filepath.Join(sysinfo.WorkDir, "acme")
	if !within(sysinfo.WorkDir, acme) {
		t.Errorf("within(%q) is false for a directory inside it", acme)
	}
	if !within(sysinfo.WorkDir, sysinfo.WorkDir) {
		t.Error("the work directory is within itself")
	}
	if within(sysinfo.WorkDir, sysinfo.WorkDir+"-other") {
		t.Error("a sibling that shares a prefix is not inside the work directory")
	}
	if within(sysinfo.WorkDir, filepath.Dir(sysinfo.WorkDir)) {
		t.Error("the parent of the work directory is not inside it")
	}
	if within(sysinfo.WorkDir, "") {
		t.Error("an empty path is not inside the work directory")
	}
}

// TestStashAndRestoreCertificates covers the promise the uninstall keeps: the
// certificates on the host outlive the deployment, so a reinstall does not have to
// spend a Let's Encrypt rate limit to get them back.
func TestStashAndRestoreCertificates(t *testing.T) {
	work := t.TempDir()
	dir := filepath.Join(work, "acme", "example.com")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	pair := filepath.Join(dir, "fullchain.cer")
	if err := os.WriteFile(pair, []byte("certificate"), 0o644); err != nil {
		t.Fatal(err)
	}

	stashed, err := stashCerts(filepath.Join(work, "acme"))
	if err != nil {
		t.Fatalf("stashCerts: %v", err)
	}
	if _, err := os.Stat(filepath.Join(work, "acme")); !os.IsNotExist(err) {
		t.Fatalf("the state directory is still in the work directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stashed, "example.com", "fullchain.cer")); err != nil {
		t.Fatalf("the pair did not come along: %v", err)
	}

	if restored := restoreCerts(stashed, filepath.Join(work, "acme")); restored != "" {
		t.Fatalf("restoreCerts: %s", restored)
	}
	back, err := os.ReadFile(pair)
	if err != nil {
		t.Fatalf("the state directory was not put back: %v", err)
	}
	if string(back) != "certificate" {
		t.Errorf("the pair came back as %q", back)
	}
	if _, err := os.Stat(stashed); !os.IsNotExist(err) {
		t.Errorf("the stash was left behind: %v", err)
	}
}

// TestRestoreWithoutAStash covers the paths where nothing was moved: a state
// directory outside the work directory, and a stash that failed before it made one.
func TestRestoreWithoutAStash(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "acme")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if restored := restoreCerts("", dir); restored != "" {
		t.Errorf("restoreCerts with nothing stashed = %q, want empty", restored)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("with nothing stashed the state directory must be left alone: %v", err)
	}
}

// TestRestoreReportsWhereTheCertificatesAre keeps a failed restore from being
// quiet: the operator is told the path the pair is sitting in, because nothing else
// on the host will point at it.
func TestRestoreReportsWhereTheCertificatesAre(t *testing.T) {
	stashed := filepath.Join(t.TempDir(), "gone", "acme")
	restored := restoreCerts(stashed, filepath.Join(t.TempDir(), "acme"))
	if restored == "" {
		t.Fatal("a restore that cannot work must say so")
	}
	if !strings.Contains(restored, stashed) {
		t.Errorf("the message does not say where the certificates are: %q", restored)
	}
}
