package bbr

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/MinimaxFlora/EasySB/internal/download"
)

// Release is one published kernel this machine can install.
type Release struct {
	Tag     string
	Version string
	Profile Profile
}

// Releases lists the published kernels for this machine, newest version first and
// the standard build of a version before its max build. The whole list is kept
// rather than only the newest: a version that broke something can be pinned, and a
// running 7.2 keeps its own line visible while newer ones appear above it.
func Releases(ctx context.Context) ([]Release, error) {
	machine := unameMachine(ctx)
	arch, ok := ArchName(machine)
	if !ok {
		return nil, fmt.Errorf("no BBRv3 kernel is published for %q", machine)
	}
	tags, err := releaseTags(ctx)
	if err != nil {
		return nil, err
	}
	return releasesFromTags(tags, arch), nil
}

// newestVersions is how many kernel versions the list offers. The menu is a pick
// between the current and the previous build, not an archive: older versions are
// still installable from the kernel project's own releases, but they only lengthen
// a list read on a terminal.
const newestVersions = 2

// releasesFromTags picks the releases this architecture can install and sorts them
// newest first, the standard build of a version ahead of its max build. Only the
// newest versions are kept, so the menu stays the current and previous kernel. Every
// version here comes from the release list: nothing about it is written down in
// this repository, so a kernel published upstream shows up on its own.
func releasesFromTags(tags []string, arch string) []Release {
	list := make([]Release, 0, len(tags))
	for _, tag := range tags {
		tagArch, version, profile, ok := ParseTag(tag)
		if !ok || tagArch != arch {
			continue
		}
		list = append(list, Release{Tag: tag, Version: version, Profile: profile})
	}
	sort.Slice(list, func(i, j int) bool {
		if c := compareVersions(list[i].Version, list[j].Version); c != 0 {
			return c > 0
		}
		return list[i].Profile == Standard && list[j].Profile != Standard
	})
	// The sort puts versions in descending order, so the first newestVersions
	// distinct versions are the ones to keep; every profile published for those
	// versions stays.
	seen := make(map[string]bool, newestVersions)
	kept := list[:0]
	for _, rel := range list {
		if !seen[rel.Version] {
			if len(seen) == newestVersions {
				break
			}
			seen[rel.Version] = true
		}
		kept = append(kept, rel)
	}
	list = kept
	return list
}

// Install downloads one published kernel and installs it, then leaves the reboot
// to the operator: switching the running kernel under a live proxy would drop
// every connection on the box. progress carries the download of each package to
// the panel, which is where the bar for a few hundred megabytes belongs.
func Install(ctx context.Context, log func(string), progress download.Progress, profile Profile, version string) error {
	machine := unameMachine(ctx)
	arch, ok := ArchName(machine)
	if !ok {
		return fmt.Errorf("no BBRv3 kernel is published for %q", machine)
	}
	if err := checkOS(releaseFile()); err != nil {
		return err
	}
	if version == "" {
		latest, err := LatestVersion(ctx)
		if err != nil {
			return err
		}
		version = latest
	}
	debArch := DebArch(arch)
	tag := profile.Tag(version, arch)
	names := AssetNames(profile, version, debArch)
	// The release's own asset list is authoritative: it also carries file names
	// the construction above cannot know about.
	if listed, err := releaseAssets(ctx, tag); err == nil && len(listed) > 0 {
		names = listed
	}
	log("release " + tag)

	// A directory of our own, named by MkdirTemp rather than by this package. The
	// packages that land here are checked with dpkg-deb and then handed to dpkg -i, and a
	// fixed path under /tmp is one an unprivileged user can have prepared beforehand -
	// as a directory they own, or as a symlink - to replace a package between the check
	// and the install, or to have the check pass on a file the install does not use.
	// 0700 under the sticky bit of /tmp leaves nobody else able to reach in.
	dir, err := os.MkdirTemp("/tmp", "easysb-bbr-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	debs := make([]string, 0, len(names))
	fresh := make(map[string]bool, len(names))
	for _, name := range names {
		dest := filepath.Join(dir, filepath.Base(name))
		log("GET " + name)
		if err := fetchFile(ctx, AssetURL(tag, name), dest, progress); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		// dpkg-deb also reads the package name, and refuses a file that is not
		// a readable package at all.
		pkg, err := run(ctx, "dpkg-deb", "-f", dest, "Package")
		if err != nil {
			return fmt.Errorf("%s is not a readable package", name)
		}
		if pkg = strings.TrimSpace(pkg); pkg != "" {
			fresh[pkg] = true
		}
		debs = append(debs, dest)
	}
	if len(debs) == 0 {
		return errors.New("the release has no packages")
	}

	log("dpkg -i " + strconv.Itoa(len(debs)) + " package(s)")
	if out, err := run(ctx, "dpkg", append([]string{"-i"}, debs...)...); err != nil {
		// Package state is now the operator's problem: dpkg may have unpacked
		// some of the set before failing, so say so instead of guessing.
		return fmt.Errorf("dpkg -i failed: %w\n%s", err, tail(out))
	}
	// The previous kernel is removed only once the new one is on disk: if the
	// install above had failed, purging first would have left the machine with
	// nothing but the stock kernel to boot.
	if _, err := purgeBrand(ctx, log, fresh); err != nil {
		return err
	}
	return updateBootloader(ctx, log)
}

// Remove uninstalls every package the kernel project installed and refreshes the
// bootloader. It reports whether there was anything to remove.
func Remove(ctx context.Context, log func(string)) (bool, error) {
	removed, err := purgeBrand(ctx, log, nil)
	if err != nil {
		return removed, err
	}
	if !removed {
		return false, nil
	}
	return true, updateBootloader(ctx, log)
}

// purgeBrand removes the kernel project's packages, images and headers alike: an
// upgrade replaces them as a set, and leaving old headers behind only confuses the
// next install. Names in keep are the ones the install just put on disk.
func purgeBrand(ctx context.Context, log func(string), keep map[string]bool) (bool, error) {
	var stale []string
	for _, pkg := range brandPackages(ctx) {
		if !keep[pkg] {
			stale = append(stale, pkg)
		}
	}
	if len(stale) == 0 {
		return false, nil
	}
	log("apt-get remove --purge " + strings.Join(stale, " "))
	args := append([]string{"remove", "--purge", "-y"}, stale...)
	if out, err := run(ctx, "apt-get", args...); err != nil {
		return true, fmt.Errorf("apt-get remove: %w\n%s", err, tail(out))
	}
	return true, nil
}

// brandPackages lists every installed package of the kernel project.
func brandPackages(ctx context.Context) []string {
	out, err := run(ctx, "dpkg-query", "-W", "-f", "${db:Status-Abbrev} ${binary:Package}\n", "*"+KernelBrand+"*")
	if err != nil {
		return nil
	}
	var pkgs []string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.HasPrefix(fields[0], "ii") {
			continue
		}
		if strings.Contains(fields[1], KernelBrand) {
			pkgs = append(pkgs, fields[1])
		}
	}
	return pkgs
}

// updateBootloader refreshes the boot menu so the new kernel can be selected.
func updateBootloader(ctx context.Context, log func(string)) error {
	if _, err := exec.LookPath("update-grub"); err != nil {
		log("update-grub is missing: check the boot menu before rebooting")
		return nil
	}
	log("update-grub")
	if out, err := run(ctx, "update-grub"); err != nil {
		return fmt.Errorf("update-grub: %w\n%s", err, tail(out))
	}
	return nil
}

// releaseFile reads /etc/os-release, empty when the file is missing.
func releaseFile() string {
	body, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return ""
	}
	return string(body)
}

// checkOS refuses the kernels on systems older than the ones the kernel project
// supports. A mismatched userspace does not fail loudly: it panics at boot, which
// on a VPS is a lot harder to undo than a refused install.
func checkOS(osRelease string) error {
	id, version := parseOSRelease(osRelease)
	switch id {
	case "":
		return errors.New("cannot read /etc/os-release: install the kernel from Linux-BBR-v3 instead")
	case "debian":
		if VersionGE(version, "12") {
			return nil
		}
		return fmt.Errorf("Debian %s is too old for these kernels: 12 or newer is required", version)
	case "ubuntu":
		if VersionGE(version, "24.04") {
			return nil
		}
		return fmt.Errorf("Ubuntu %s is too old for these kernels: 24.04 or newer is required", version)
	}
	if strings.Contains(osRelease, "debian") {
		return nil
	}
	return fmt.Errorf("%s is not a Debian-based system", id)
}

// parseOSRelease pulls ID and VERSION_ID out of /etc/os-release.
func parseOSRelease(body string) (id, version string) {
	for _, line := range strings.Split(body, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		switch strings.TrimSpace(key) {
		case "ID":
			id = strings.ToLower(value)
		case "VERSION_ID":
			version = value
		}
	}
	return id, version
}

// tail keeps the last few lines of a failed command, which is where dpkg and apt
// put the reason.
func tail(out string) string {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	const keep = 6
	if len(lines) <= keep {
		return out
	}
	return strings.Join(lines[len(lines)-keep:], "\n")
}
