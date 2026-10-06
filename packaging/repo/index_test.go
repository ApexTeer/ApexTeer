// Package repo_test exercises packaging/repo/index.sh against a real .deb.
//
// The test needs apt-ftparchive (apt-utils), gpg and dpkg-deb, so it skips on a
// machine without them rather than failing the pre-commit gate. When they are
// present it locks down the flat layout the release depends on: a bare Filename,
// a Release that does not checksum itself, and the three signature files.
package repo_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// repoRoot walks up from this file until it finds the module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above test file")
		}
		dir = parent
	}
}

func tool(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s not installed: %v", name, err)
	}
	return path
}

// makeDeb builds a minimal installable-looking package; apt-ftparchive only
// reads its control member, so the payload can be empty.
func makeDeb(t *testing.T, root, control string) string {
	t.Helper()
	deb := tool(t, "dpkg-deb")
	stage := filepath.Join(root, "pkgroot")
	if err := os.MkdirAll(filepath.Join(stage, "DEBIAN"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "DEBIAN", "control"), []byte(control), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "dist", "easysb_6.0.0-1_amd64.deb")
	cmd := exec.Command(deb, "--build", "--root-owner-group", stage, out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dpkg-deb --build: %v\n%s", err, b)
	}
	return out
}

// makeKey generates a throwaway signing key in its own GNUPGHOME and returns the
// home and the primary fingerprint.
func makeKey(t *testing.T, root string) (home, fingerprint string) {
	t.Helper()
	gpg := tool(t, "gpg")
	home = filepath.Join(root, "gnupg")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	gen := exec.Command(gpg, "--batch", "--pinentry-mode", "loopback", "--passphrase", "",
		"--quick-generate-key", "EasySB Test <test@example.com>", "ed25519", "sign", "never")
	gen.Env = append(os.Environ(), "GNUPGHOME="+home)
	if b, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("gpg --quick-generate-key: %v\n%s", err, b)
	}
	list := exec.Command(gpg, "--batch", "--list-secret-keys", "--with-colons")
	list.Env = append(os.Environ(), "GNUPGHOME="+home)
	b, err := list.Output()
	if err != nil {
		t.Fatalf("gpg --list-secret-keys: %v", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "fpr:") {
			return home, strings.Split(line, ":")[9]
		}
	}
	t.Fatal("no fingerprint in gpg output")
	return "", ""
}

func run(t *testing.T, dir string, env []string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", name, err, b)
	}
}

func TestIndexFlatLayout(t *testing.T) {
	if testing.Short() {
		t.Skip("needs apt-ftparchive, gpg and dpkg-deb")
	}
	tool(t, "apt-ftparchive")
	tool(t, "gpg")

	root := t.TempDir()
	dist := filepath.Join(root, "dist")
	repoDir := filepath.Join(root, "repo")
	if err := os.MkdirAll(dist, 0o755); err != nil {
		t.Fatal(err)
	}
	makeDeb(t, root, "Package: easysb\nVersion: 6.0.0-1\nArchitecture: amd64\n"+
		"Maintainer: EasySB Test <test@example.com>\nDescription: test package\n")
	gnupgHome, fingerprint := makeKey(t, root)
	t.Cleanup(func() {
		kill := exec.Command("gpgconf", "--kill", "all")
		kill.Env = append(os.Environ(), "GNUPGHOME="+gnupgHome)
		_ = kill.Run()
	})

	run(t, repoRoot(t), []string{
		"DIST=" + dist,
		"REPO_DIR=" + repoDir,
		"PKG_NAME=easysb",
		"VERSION=6.0.0",
		"PKG_DESC=EasySB test",
		"DEBARCH_MAP=amd64=amd64",
		"GPG_KEY_ID=" + fingerprint,
		"GNUPGHOME=" + gnupgHome,
	}, "bash", "packaging/repo/index.sh")

	read := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(repoDir, name))
		if err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
		return string(b)
	}

	packages := read("Packages")
	if !strings.Contains(packages, "Filename: easysb_6.0.0-1_amd64.deb") {
		t.Errorf("Packages should carry a bare Filename:\n%s", packages)
	}
	if strings.Contains(packages, "Filename: ./") {
		t.Errorf("Packages still has the ./ prefix:\n%s", packages)
	}

	// Release is generated in a temp file outside the tree, so it must not list
	// itself among the checksummed files.
	release := read("Release")
	for _, line := range strings.Split(release, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == "Release" {
			t.Errorf("Release checksums itself:\n%s", release)
		}
	}
	// Release checksums the indexes; the .deb is checksummed inside Packages.
	for _, want := range []string{"Packages\n", "Packages.gz\n"} {
		if !strings.Contains(release, want) {
			t.Errorf("Release is missing %q:\n%s", strings.TrimSpace(want), release)
		}
	}

	if s := read("InRelease"); !strings.Contains(s, "BEGIN PGP SIGNED MESSAGE") {
		t.Errorf("InRelease is not clearsigned:\n%s", s)
	}
	if s := read("Release.gpg"); !strings.Contains(s, "BEGIN PGP SIGNATURE") {
		t.Errorf("Release.gpg is not a detached signature:\n%s", s)
	}
	if s := read("easysb-archive-keyring.asc"); !strings.Contains(s, "BEGIN PGP PUBLIC KEY BLOCK") {
		t.Errorf("keyring is not an armored public key:\n%s", s)
	}
	read("install.sh")
}
