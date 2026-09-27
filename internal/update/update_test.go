package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAssetName(t *testing.T) {
	cases := map[string]string{
		"amd64": "amd64", "arm64": "arm64", "arm": "armv7",
		"386": "386", "riscv64": "riscv64", "s390x": "s390x",
	}
	for in, want := range cases {
		got, ok := AssetName(in)
		if !ok || got != want {
			t.Fatalf("AssetName(%q) = %q,%v want %q", in, got, ok, want)
		}
	}
	if _, ok := AssetName("mips"); ok {
		t.Fatal("expected unsupported architecture to be rejected")
	}
}

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

func TestAssetURLUsesVersionTag(t *testing.T) {
	got, err := AssetURL("3.0.0")
	if err != nil {
		t.Fatalf("AssetURL(3.0.0) error: %v", err)
	}
	if !strings.Contains(got, "/releases/download/v3.0.0/easysb-3.0.0-linux-") {
		t.Fatalf("AssetURL(3.0.0) = %q, want a v3.0.0 download path", got)
	}
	if !strings.HasSuffix(got, ".tar.gz") {
		t.Fatalf("AssetURL(3.0.0) = %q, want a tarball", got)
	}
}

func TestAssetFileName(t *testing.T) {
	cases := map[string]string{
		"amd64": "easysb-5.0.0-linux-amd64.tar.gz",
		"arm":   "easysb-5.0.0-linux-armv7.tar.gz",
		"386":   "easysb-5.0.0-linux-386.tar.gz",
	}
	for goarch, want := range cases {
		got, ok := AssetFileName("5.0.0", goarch)
		if !ok || got != want {
			t.Fatalf("AssetFileName(5.0.0, %q) = %q,%v want %q", goarch, got, ok, want)
		}
	}
	if got, ok := AssetFileName("v5.0.0", "amd64"); !ok || got != "easysb-5.0.0-linux-amd64.tar.gz" {
		t.Fatalf("AssetFileName(v5.0.0, amd64) = %q,%v want the bare version", got, ok)
	}
	if _, ok := AssetFileName("5.0.0", "mips"); ok {
		t.Fatal("expected unsupported architecture to be rejected")
	}
}

func TestExtractBinary(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "easysb-5.0.0-linux-amd64.tar.gz")
	writeArchive(t, archive, map[string]string{
		"LICENSE":   "license",
		"easysb":    "binary",
		"README.md": "readme",
	})
	dest := filepath.Join(t.TempDir(), "easysb")
	if err := extractBinary(archive, dest); err != nil {
		t.Fatalf("extractBinary error: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read extracted binary: %v", err)
	}
	if string(got) != "binary" {
		t.Fatalf("extracted %q, want the easysb member", got)
	}
}

func TestExtractBinaryWithoutMember(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "easysb-5.0.0-linux-amd64.tar.gz")
	writeArchive(t, archive, map[string]string{"LICENSE": "license"})
	if err := extractBinary(archive, filepath.Join(t.TempDir(), "easysb")); err == nil {
		t.Fatal("expected an archive without the easysb member to fail")
	}
}

// writeArchive builds the release tarball shape the tests read back: gzip tar with
// the given members at the root.
func writeArchive(t *testing.T, name string, members map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for member, body := range members {
		hdr := &tar.Header{Name: member, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header: %v", err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("write body: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	if err := os.WriteFile(name, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write archive: %v", err)
	}
}
