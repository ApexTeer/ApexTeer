// Package update upgrades the installed EasySB package through apt. EasySB ships as
// a .deb from a signed apt source, so an upgrade is the package manager's job: the
// panel refreshes the source and installs the newest easysb, then asks the operator
// to restart it. There is no separate binary download any more.
package update

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/EasySB-Team/EasySB/internal/download"
)

// Repo is the EasySB repository that publishes the sources.
const Repo = "EasySB-Team/EasySB"

// PackageName is the Debian package the panel is installed as.
const PackageName = "easysb"

// versionURL points at the raw VERSION file on the default branch.
const versionURL = "https://raw.githubusercontent.com/" + Repo + "/master/VERSION"

// bareVersion strips a leading v and surrounding space so a version prints the same
// whether the caller carried the release tag or the bare number.
func bareVersion(version string) string {
	return strings.TrimPrefix(strings.TrimSpace(version), "v")
}

// ReleaseTag maps an EasySB version to its GitHub release tag.
func ReleaseTag(version string) string {
	return "v" + bareVersion(version)
}

// debArch maps a Go architecture to Debian's spelling. EasySB publishes only amd64
// and arm64, the two architectures the BBR kernels cover.
func debArch(goarch string) (string, bool) {
	switch goarch {
	case "amd64", "arm64":
		return goarch, true
	}
	return "", false
}

// PackageFileName builds the published .deb name for a version and architecture,
// the shape the Makefile and the release workflow both use, for example
// easysb_6.0.0-1_amd64.deb. A version that already carries a revision is used as is.
func PackageFileName(version, goarch string) (string, bool) {
	arch, ok := debArch(goarch)
	if !ok {
		return "", false
	}
	v := bareVersion(version)
	if !strings.Contains(v, "-") {
		v += "-1"
	}
	return PackageName + "_" + v + "_" + arch + ".deb", true
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

// Apply refreshes the apt source and upgrades the easysb package. It returns false
// when nothing changed - the installed binary already matches the published version,
// or apt found no newer package - and true when the package was replaced. current is
// the running EasySB version string. The apt output is streamed to log.
func Apply(ctx context.Context, current string, log func(string)) (bool, string, error) {
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

	if _, err := exec.LookPath("apt-get"); err != nil {
		return false, remote, errors.New("apt-get not found: EasySB upgrades through apt")
	}
	before := installedVersion(ctx)

	log("apt-get update")
	if err := runApt(ctx, log, "update"); err != nil {
		return false, remote, err
	}
	log("apt-get install -y " + PackageName)
	if err := runApt(ctx, log, "install", "-y", PackageName); err != nil {
		return false, remote, err
	}

	after := installedVersion(ctx)
	if after != "" && after == before {
		return false, remote, nil
	}
	return true, remote, nil
}

// installedVersion reads the installed package version, empty when the package is not
// installed or dpkg is unavailable.
func installedVersion(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "dpkg-query", "-W", "-f=${Version}", PackageName).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// runApt runs apt-get with the package's own output folded into log, so the task
// screen shows exactly what the package manager printed. DEBIAN_FRONTEND keeps it
// from opening a prompt on a machine with debconf.
func runApt(ctx context.Context, log func(string), args ...string) error {
	cmd := exec.CommandContext(ctx, "apt-get", args...)
	cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")

	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		pw.Close()
		return err
	}
	done := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			if line := strings.TrimRight(sc.Text(), "\r"); line != "" {
				log(line)
			}
		}
		close(done)
	}()

	err := cmd.Wait()
	pw.Close()
	<-done
	if err != nil {
		return fmt.Errorf("apt-get %s: %w", strings.Join(args, " "), err)
	}
	return nil
}
