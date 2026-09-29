// Package update self-updates the EasySB binary from the v<version> GitHub
// release tarball, mirroring the legacy script's self-update flow.
package update

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path"
	"runtime"
	"strings"
	"time"

	"github.com/MinimaxFlora/EasySB/internal/download"
)

// Repo is the EasySB repository that publishes the binaries.
const Repo = "MinimaxFlora/EasySB"

// bareVersion strips a leading v and surrounding space so a version prints the same
// whether the caller carried the release tag or the bare number.
func bareVersion(version string) string {
	return strings.TrimPrefix(strings.TrimSpace(version), "v")
}

// ReleaseTag maps an EasySB version to its GitHub release tag.
func ReleaseTag(version string) string {
	return "v" + bareVersion(version)
}

// versionURL points at the raw VERSION file on the default branch.
const versionURL = "https://raw.githubusercontent.com/" + Repo + "/master/VERSION"

// AssetName maps a Go architecture to the published asset suffix.
func AssetName(goarch string) (string, bool) {
	switch goarch {
	case "amd64":
		return "amd64", true
	case "arm64":
		return "arm64", true
	case "arm":
		return "armv7", true
	case "386":
		return "386", true
	case "riscv64":
		return "riscv64", true
	case "s390x":
		return "s390x", true
	}
	return "", false
}

// AssetFileName builds the published archive name for a version and architecture,
// the shape install.sh and the release workflow both use, for example
// easysb-5.0.0-linux-amd64.tar.gz.
func AssetFileName(version, goarch string) (string, bool) {
	asset, ok := AssetName(goarch)
	if !ok {
		return "", false
	}
	return "easysb-" + bareVersion(version) + "-linux-" + asset + ".tar.gz", true
}

// AssetURL returns the download URL for an EasySB version and the current
// architecture.
func AssetURL(version string) (string, error) {
	name, ok := AssetFileName(version, runtime.GOARCH)
	if !ok {
		return "", errors.New("unsupported architecture: " + runtime.GOARCH)
	}
	return "https://github.com/" + Repo + "/releases/download/" + ReleaseTag(version) + "/" + name, nil
}

// RemoteVersion downloads the published VERSION file.
func RemoteVersion(ctx context.Context) (string, error) {
	tmp, err := os.CreateTemp("", "easysb-version-*")
	if err != nil {
		return "", err
	}
	path := tmp.Name()
	tmp.Close()
	defer os.Remove(path)

	if err := download.Download(ctx, versionURL, path); err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// Apply downloads the current architecture binary and replaces the running
// executable. It returns false when the installed binary already matches the
// remote build. current is the running EasySB version string. The binary download
// reports itself to progress, which the panel turns into a bar.
func Apply(ctx context.Context, current string, log func(string), progress download.Progress) (bool, string, error) {
	remote, err := RemoteVersion(ctx)
	if err != nil {
		log("cannot read remote version: " + err.Error())
	}
	if remote != "" {
		log("remote version: " + remote)
		if current != "" && remote == current {
			return false, remote, nil
		}
	}

	target := remote
	if target == "" {
		target = current
	}
	if target == "" {
		return false, remote, errors.New("missing version for release tag")
	}
	url, err := AssetURL(target)
	if err != nil {
		return false, remote, err
	}
	exe, err := os.Executable()
	if err != nil {
		return false, remote, err
	}
	archive := exe + ".tar.gz"
	tmp := exe + ".new"

	log("GET " + url)
	if err := download.WithProgress(ctx, url, archive, 5*time.Minute, progress); err != nil {
		return false, remote, err
	}
	defer os.Remove(archive)
	if err := extractBinary(archive, tmp); err != nil {
		os.Remove(tmp)
		return false, remote, err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return false, remote, err
	}
	if same, err := sameFile(exe, tmp); err == nil && same {
		os.Remove(tmp)
		return false, remote, nil
	}
	if err := os.Rename(tmp, exe); err != nil {
		os.Remove(tmp)
		return false, remote, err
	}
	return true, remote, nil
}

// extractBinary writes the easysb member of a release archive to dest. The archive
// holds the binary, the license and the readme, and the binary sits at the root, so
// the member is matched by base name.
func extractBinary(archive, dest string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg || path.Base(hdr.Name) != "easysb" {
			continue
		}
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	}
	return errors.New("release archive carries no easysb binary")
}

func sameFile(a, b string) (bool, error) {
	ha, err := hashFile(a)
	if err != nil {
		return false, err
	}
	hb, err := hashFile(b)
	if err != nil {
		return false, err
	}
	return ha == hb, nil
}

func hashFile(path string) ([32]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return [32]byte{}, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return [32]byte{}, err
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out, nil
}
