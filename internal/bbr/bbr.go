// Package bbr manages the two halves of BBR acceleration: switching on the
// congestion control the running kernel already ships, and installing the
// prebuilt BBRv3 kernel that the Linux-BBR-v3 project publishes.
//
// The kernels are not built here. That project's workflow compiles Linux with the
// BBRv3 patchset and publishes one .deb set per architecture and profile, so this
// package only has to pick a release, download its packages and hand them to dpkg.
package bbr

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Repo publishes the prebuilt kernels and the version stamp this package reads.
const Repo = "MinimaxFlora/Linux-BBR-v3"

// KernelBrand marks the packages built by the kernel project, in both the package
// name (linux-image-7.2.6-minimaxflora-bbrv3-max) and the kernel release.
const KernelBrand = "minimaxflora-bbrv3"

// SysctlConf and ModulesConf are EasySB's own drop-ins. They deliberately do not
// reuse the kernel project's 99-minimaxflora.conf: both tools can then be used on
// the same machine without one deleting the other's settings.
const (
	SysctlConf  = "/etc/sysctl.d/99-easysb-bbr.conf"
	ModulesConf = "/etc/modules-load.d/easysb-bbr.conf"
)

// versionINIURL is the version stamp published with the kernel project's own CLI.
// Reading it beats paging through the release list for a version number.
const versionINIURL = "https://github.com/" + Repo + "/releases/download/bbrv3-cli/version.ini"

// apiURL lists the published kernel releases.
const apiURL = "https://api.github.com/repos/" + Repo + "/releases"

// downloadTimeout is the budget for one kernel package. The image is a few hundred
// megabytes, well past the five minutes the core tarball needs.
const downloadTimeout = 20 * time.Minute

// Qdiscs are the queue disciplines the published kernels carry. BBR needs a fair
// queueing scheduler to pace packets; fq is the classic pairing.
var Qdiscs = []string{"fq", "fq_codel", "fq_pie", "cake"}

// Profile selects which published kernel flavour to install: the standard build,
// or the max build that trades some safety margin for throughput.
type Profile string

// The published kernel flavours. Standard is the default; Max trades safety margin for
// throughput.
const (
	Standard Profile = "standard"
	Max      Profile = "max"
)

// Suffix is the profile's marker in release tags and package names.
func (p Profile) Suffix() string {
	if p == Max {
		return "-max"
	}
	return ""
}

// KernelRelease is the kernel release string a profile's package installs, e.g.
// "7.2.6-minimaxflora-bbrv3-max". It is what uname -r reports after a reboot.
func (p Profile) KernelRelease(version string) string {
	return version + "-" + KernelBrand + p.Suffix()
}

// Tag is the release tag holding this profile's packages, e.g. "x86_64-7.2.6-max".
func (p Profile) Tag(version, arch string) string {
	return arch + "-" + version + p.Suffix()
}

// ArchName maps a machine architecture to the one the release tags use.
func ArchName(uname string) (string, bool) {
	switch strings.TrimSpace(uname) {
	case "x86_64", "amd64":
		return "x86_64", true
	case "aarch64", "arm64":
		return "arm64", true
	}
	return "", false
}

// DebArch maps a release architecture to the Debian package architecture.
func DebArch(arch string) string {
	if arch == "x86_64" {
		return "amd64"
	}
	return "arm64"
}

// AssetNames is the package set of a release, used when the API does not answer:
// the kernel image, its headers, and the matching libc headers.
func AssetNames(profile Profile, version, debArch string) []string {
	debianRevision := version + "-1"
	base := version + "-" + KernelBrand + profile.Suffix()
	return []string{
		"linux-image-" + base + "_" + debianRevision + "_" + debArch + ".deb",
		"linux-headers-" + base + "_" + debianRevision + "_" + debArch + ".deb",
		"linux-libc-dev_" + debianRevision + "_" + debArch + ".deb",
	}
}

// AssetURL is the download URL of one package inside a release.
func AssetURL(tag, name string) string {
	return "https://github.com/" + Repo + "/releases/download/" + tag + "/" + name
}

// versionRE matches the release tags that carry a kernel build.
var versionRE = regexp.MustCompile(`^(x86_64|arm64)-([0-9]+(?:\.[0-9]+)+)(-max)?$`)

// ParseTag splits a release tag into its architecture, kernel version and
// profile. ok is false for tags that are not kernel builds (the CLI release, the
// version stamp, anything renamed later).
func ParseTag(tag string) (arch, version string, profile Profile, ok bool) {
	m := versionRE.FindStringSubmatch(strings.TrimSpace(tag))
	if m == nil {
		return "", "", "", false
	}
	profile = Standard
	if m[3] == "-max" {
		profile = Max
	}
	return m[1], m[2], profile, true
}

// versionParts splits a version into its numeric parts, so 7.2.6 and 7.10 compare
// the way a human reads them rather than the way strings sort.
func versionParts(v string) []int64 {
	fields := strings.FieldsFunc(v, func(r rune) bool { return r < '0' || r > '9' })
	parts := make([]int64, 0, len(fields))
	for _, f := range fields {
		n, err := strconv.ParseInt(f, 10, 64)
		if err != nil {
			continue
		}
		parts = append(parts, n)
	}
	return parts
}

// compareVersions orders two versions: -1, 0 or 1. A missing part counts as zero,
// so 7.2 and 7.2.0 are the same version.
func compareVersions(a, b string) int {
	av, bv := versionParts(a), versionParts(b)
	for i := 0; i < len(av) || i < len(bv); i++ {
		var ai, bi int64
		if i < len(av) {
			ai = av[i]
		}
		if i < len(bv) {
			bi = bv[i]
		}
		if ai != bi {
			if ai < bi {
				return -1
			}
			return 1
		}
	}
	return 0
}

// VersionGE reports whether current is at least required.
func VersionGE(current, required string) bool {
	if required == "" {
		return true
	}
	if len(versionParts(current)) == 0 {
		return false
	}
	return compareVersions(current, required) >= 0
}

// NewestVersionFor returns the highest kernel version published for one
// architecture, or for every architecture when arch is empty. The architecture
// matters on the install path: a version published for arm64 alone is newer than
// anything on x86_64, but it cannot be installed here.
func NewestVersionFor(tags []string, arch string) string {
	newest := ""
	for _, tag := range tags {
		tagArch, version, _, ok := ParseTag(tag)
		if !ok || (arch != "" && tagArch != arch) {
			continue
		}
		if VersionGE(version, newest) {
			newest = version
		}
	}
	return newest
}
