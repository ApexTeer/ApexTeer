package update

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseTag(t *testing.T) {
	cases := map[string]string{
		"3.0.0":   "v3.0.0",
		"v3.0.0":  "v3.0.0",
		" 3.0.0 ": "v3.0.0",
	}
	for in, want := range cases {
		if got := ReleaseTag(in); got != want {
			t.Fatalf("ReleaseTag(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPackageFileName(t *testing.T) {
	cases := map[string]string{
		"amd64": "easysb_6.0.0-1_amd64.deb",
		"arm64": "easysb_6.0.0-1_arm64.deb",
	}
	for goarch, want := range cases {
		got, ok := PackageFileName("6.0.0", goarch)
		if !ok || got != want {
			t.Fatalf("PackageFileName(6.0.0, %q) = %q,%v want %q", goarch, got, ok, want)
		}
	}
	if got, ok := PackageFileName("v6.0.0-1", "amd64"); !ok || got != "easysb_6.0.0-1_amd64.deb" {
		t.Fatalf("PackageFileName(v6.0.0-1, amd64) = %q,%v want the published name once", got, ok)
	}
	if _, ok := PackageFileName("6.0.0", "mips"); ok {
		t.Fatal("expected unsupported architecture to be rejected")
	}
}

func TestRunAptStreamsOutput(t *testing.T) {
	dir := fakeTool(t, "apt-get", `#!/bin/sh
echo "Reading package lists..."
echo "Setting up easysb (6.0.0-1) ..."
exit 0
`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var lines []string
	if err := runApt(context.Background(), func(s string) { lines = append(lines, s) }, "update"); err != nil {
		t.Fatalf("runApt error: %v", err)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Reading package lists...") || !strings.Contains(joined, "Setting up easysb") {
		t.Fatalf("runApt did not stream the command output: %q", joined)
	}
}

func TestRunAptReportsFailure(t *testing.T) {
	dir := fakeTool(t, "apt-get", "#!/bin/sh\nexit 100\n")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := runApt(context.Background(), func(string) {}, "install", "-y", "easysb"); err == nil {
		t.Fatal("expected a nonzero apt-get exit to fail")
	}
}

func TestInstalledVersion(t *testing.T) {
	dir := fakeTool(t, "dpkg-query", "#!/bin/sh\nprintf '6.0.0-1'\n")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if got := installedVersion(context.Background()); got != "6.0.0-1" {
		t.Fatalf("installedVersion = %q, want 6.0.0-1", got)
	}
}

// fakeTool drops an executable named name into a temp dir and returns that dir, so a
// test can shadow a real command through PATH.
func fakeTool(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
		t.Fatalf("write fake %s: %v", name, err)
	}
	return dir
}
