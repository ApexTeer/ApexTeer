// Package update self-updates the EasySB binary from the v<version> GitHub
// release tarball, mirroring the legacy script's self-update flow.
package update

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	// The tarball replaces the running binary as root, so it is checked against the
	// sha256 GitHub publishes for the release asset before it is unpacked. That digest
	// is read over TLS from the releases API, which makes a tarball that arrived from
	// anywhere other than the release itself fail here. An older release that carries
	// no digest only loses the extra check.
	if name, ok := AssetFileName(target, runtime.GOARCH); ok {
		digest, err := AssetDigest(ctx, target, name)
		switch {
		case err != nil:
			log("cannot read release digest: " + err.Error())
		case digest == "":
			log("release carries no sha256 digest")
		default:
			if err := verifyDigest(archive, digest); err != nil {
				return false, remote, err
			}
			log("verified " + digest)
		}
	}
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
	// Keep the binary being replaced, so a release that will not start can be
	// rolled back on the host instead of needing another download.
	if current, err := os.ReadFile(exe); err == nil {
		_ = os.WriteFile(exe+".bak", current, 0o755)
	}
	if err := os.Rename(tmp, exe); err != nil {
		os.Remove(tmp)
		return false, remote, err
	}
	return true, remote, nil
}

// releaseAsset is the part of a releases-API asset a check needs: the name to find
// the right one and the sha256 digest GitHub computed for it.
type releaseAsset struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

// releaseInfo is the part of the release document that carries the assets.
type releaseInfo struct {
	Assets []releaseAsset `json:"assets"`
}

// AssetDigest reads the sha256 digest GitHub publishes for a release asset, in the
// "sha256:<hex>" form the releases API reports. It returns an empty digest and no
// error when the answer holds none - an older release predates the field, or GitHub
// withheld it - so the caller can still update with the download check alone.
func AssetDigest(ctx context.Context, version, name string) (string, error) {
	url := "https://api.github.com/repos/" + Repo + "/releases/tags/" + ReleaseTag(version)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "EasySB")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("release metadata: %s", resp.Status)
	}
	var doc releaseInfo
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return "", err
	}
	for _, a := range doc.Assets {
		if a.Name == name {
			return a.Digest, nil
		}
	}
	return "", nil
}

// verifyDigest compares the sha256 of a downloaded file against a "sha256:<hex>"
// digest the releases API published.
func verifyDigest(file, digest string) error {
	want, ok := strings.CutPrefix(digest, "sha256:")
	if !ok {
		return fmt.Errorf("unsupported digest %q", digest)
	}
	got, err := hashFile(file)
	if err != nil {
		return err
	}
	sum := hex.EncodeToString(got[:])
	if !strings.EqualFold(sum, want) {
		return fmt.Errorf("sha256 mismatch: got %s want %s", sum, want)
	}
	return nil
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
