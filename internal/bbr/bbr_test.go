package bbr

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionGE(t *testing.T) {
	cases := []struct {
		current, required string
		want              bool
	}{
		{"7.2.6", "7.2.0", true},
		{"7.2.0", "7.2.0", true},
		{"7.2.0", "7.2.6", false},
		// 7.10 is newer than 7.9, which string comparison gets wrong.
		{"7.10", "7.9", true},
		{"7.9", "7.10", false},
		{"6.12.48+deb13-amd64", "7.2.0", false},
		{"", "7.2.0", false},
		{"7.2.0", "", true},
	}
	for _, c := range cases {
		if got := VersionGE(c.current, c.required); got != c.want {
			t.Errorf("VersionGE(%q, %q) = %v, want %v", c.current, c.required, got, c.want)
		}
	}
}

func TestParseTag(t *testing.T) {
	arch, version, profile, ok := ParseTag("x86_64-7.2.6")
	if !ok || arch != "x86_64" || version != "7.2.6" || profile != Standard {
		t.Fatalf("standard tag parsed as %q %q %q %v", arch, version, profile, ok)
	}
	arch, version, profile, ok = ParseTag("arm64-7.2.6-max")
	if !ok || arch != "arm64" || version != "7.2.6" || profile != Max {
		t.Fatalf("max tag parsed as %q %q %q %v", arch, version, profile, ok)
	}
	for _, tag := range []string{"bbrv3-cli", "v4.0.0", "x86_64", "x86_64-latest", "7.2.6"} {
		if _, _, _, ok := ParseTag(tag); ok {
			t.Errorf("ParseTag(%q) should not accept a non-kernel tag", tag)
		}
	}
}

func TestNewestVersionPicksHighestKernel(t *testing.T) {
	tags := []string{"bbrv3-cli", "x86_64-7.2.0", "arm64-7.2.0-max", "x86_64-7.10.1", "x86_64-6.9.0"}
	if got := NewestVersionFor(tags, ""); got != "7.10.1" {
		t.Fatalf("NewestVersionFor = %q, want 7.10.1", got)
	}
	if got := NewestVersionFor([]string{"bbrv3-cli"}, ""); got != "" {
		t.Fatalf("NewestVersionFor with no kernel tags = %q, want empty", got)
	}
}

func TestProfileNaming(t *testing.T) {
	if got := Standard.Tag("7.2.6", "x86_64"); got != "x86_64-7.2.6" {
		t.Errorf("standard tag = %q", got)
	}
	if got := Max.Tag("7.2.6", "x86_64"); got != "x86_64-7.2.6-max" {
		t.Errorf("max tag = %q", got)
	}
	if got := Max.KernelRelease("7.2.6"); got != "7.2.6-minimaxflora-bbrv3-max" {
		t.Errorf("max kernel release = %q", got)
	}
	if got := Standard.KernelRelease("7.2.6"); got != "7.2.6-minimaxflora-bbrv3" {
		t.Errorf("standard kernel release = %q", got)
	}
}

func TestAssetNamesMatchPublishedPackages(t *testing.T) {
	got := AssetNames(Standard, "7.2.6", "amd64")
	want := []string{
		"linux-image-7.2.6-minimaxflora-bbrv3_7.2.6-1_amd64.deb",
		"linux-headers-7.2.6-minimaxflora-bbrv3_7.2.6-1_amd64.deb",
		"linux-libc-dev_7.2.6-1_amd64.deb",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("AssetNames = %v, want %v", got, want)
	}
	if got := AssetNames(Max, "7.2.6", "arm64")[0]; got != "linux-image-7.2.6-minimaxflora-bbrv3-max_7.2.6-1_arm64.deb" {
		t.Fatalf("max arm64 image = %q", got)
	}
	if got := AssetURL("x86_64-7.2.6", "linux-image-7.2.6-minimaxflora-bbrv3_7.2.6-1_amd64.deb"); !strings.HasPrefix(got,
		"https://github.com/MinimaxFlora/Linux-BBR-v3/releases/download/x86_64-7.2.6/") {
		t.Fatalf("AssetURL = %q", got)
	}
}

func TestArchName(t *testing.T) {
	for machine, want := range map[string]string{
		"x86_64":  "x86_64",
		"amd64":   "x86_64",
		"aarch64": "arm64",
		"arm64":   "arm64",
	} {
		if got, ok := ArchName(machine); !ok || got != want {
			t.Errorf("ArchName(%q) = %q, %v; want %q", machine, got, ok, want)
		}
	}
	for _, machine := range []string{"i686", "armv7l", "riscv64", ""} {
		if _, ok := ArchName(machine); ok {
			t.Errorf("ArchName(%q) should be unsupported", machine)
		}
	}
	if DebArch("x86_64") != "amd64" || DebArch("arm64") != "arm64" {
		t.Fatal("DebArch does not map the release architectures")
	}
}

func TestParseStamp(t *testing.T) {
	body := `# kernel project stamp
[cli]
commit=14635050
built=2026-08-25T13:06:56Z
[kernel]
version=7.2.0   # newest published kernel
[extra]
version=9.9.9
`
	if got := parseStamp(body); got != "7.2.0" {
		t.Fatalf("parseStamp = %q, want 7.2.0", got)
	}
	// A stamp without the kernel section is not a version answer.
	if got := parseStamp("[cli]\ncommit=abc\n"); got != "" {
		t.Fatalf("parseStamp without [kernel] = %q", got)
	}
}

func TestParseStampWithOneSectionPerBuild(t *testing.T) {
	// This is the shape the kernel project actually publishes: its build appends a
	// [kernel] section per release, so the oldest version comes first. Reporting
	// the first one is how the panel ends up offering a kernel from months ago.
	body := `[cli]
commit=187b4b8e
built=2026-08-30T19:25:27Z
[kernel]
version=7.2.0
[kernel]
version=7.2.2
[kernel]
version=7.2.6
`
	if got := parseStamp(body); got != "7.2.6" {
		t.Fatalf("parseStamp = %q, want the newest section 7.2.6", got)
	}
	// Numeric order, not the order they happen to be written in.
	jumbled := "[kernel]\nversion=7.2.6\n[kernel]\nversion=7.10.0\n[kernel]\nversion=7.9.0\n"
	if got := parseStamp(jumbled); got != "7.10.0" {
		t.Fatalf("parseStamp = %q, want 7.10.0", got)
	}
}

func TestNewestVersionFor(t *testing.T) {
	tags := []string{
		"bbrv3-cli",
		"arm64-7.3.0",
		"x86_64-7.2.6",
		"x86_64-7.2.6-max",
		"arm64-7.2.6",
		"x86_64-7.2.2",
	}
	// A version published for arm64 alone is newer than anything on x86_64, and
	// installing it there would only produce a download that cannot exist.
	if got := NewestVersionFor(tags, "x86_64"); got != "7.2.6" {
		t.Fatalf("NewestVersionFor(x86_64) = %q, want 7.2.6", got)
	}
	if got := NewestVersionFor(tags, "arm64"); got != "7.3.0" {
		t.Fatalf("NewestVersionFor(arm64) = %q, want 7.3.0", got)
	}
	if got := NewestVersionFor(tags, ""); got != "7.3.0" {
		t.Fatalf("NewestVersionFor(any) = %q, want 7.3.0", got)
	}
	if got := NewestVersionFor(tags, "riscv64"); got != "" {
		t.Fatalf("NewestVersionFor(riscv64) = %q, want empty", got)
	}
}

func TestCheckOS(t *testing.T) {
	cases := []struct {
		name, body string
		ok         bool
	}{
		{"debian 13", "ID=debian\nVERSION_ID=\"13\"\n", true},
		{"debian 12", "ID=debian\nVERSION_ID=\"12\"\n", true},
		{"debian 11", "ID=debian\nVERSION_ID=\"11\"\n", false},
		{"ubuntu 24.04", "ID=ubuntu\nVERSION_ID=\"24.04\"\n", true},
		{"ubuntu 22.04", "ID=ubuntu\nVERSION_ID=\"22.04\"\n", false},
		{"fedora", "ID=fedora\nVERSION_ID=\"41\"\n", false},
		{"missing", "", false},
	}
	for _, c := range cases {
		err := checkOS(c.body)
		if c.ok && err != nil {
			t.Errorf("%s: unexpected error %v", c.name, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s: expected the install to be refused", c.name)
		}
	}
}

func TestPreviousSettingsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "99-easysb-bbr.conf")
	live := previousSettings{congestion: "reno", qdisc: "fq_codel"}

	if err := os.WriteFile(path, []byte(live.note()+"\nnet.core.default_qdisc = fq\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readPrevious(path); got != live {
		t.Fatalf("readPrevious = %+v, want %+v", got, live)
	}
	// A file written by hand, or by another tool, carries no note.
	plain := filepath.Join(t.TempDir(), "other.conf")
	if err := os.WriteFile(plain, []byte("net.ipv4.tcp_congestion_control = bbr\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readPrevious(plain); got != (previousSettings{}) {
		t.Fatalf("readPrevious on a file without a note = %+v", got)
	}
	if got := readPrevious(filepath.Join(t.TempDir(), "missing")); got != (previousSettings{}) {
		t.Fatalf("readPrevious on a missing file = %+v", got)
	}

	// Enabling twice must not record BBR as the thing to restore.
	afterFirst := previousSettings{congestion: "bbr", qdisc: "fq"}
	if got := keepOldest(live, afterFirst); got != live {
		t.Errorf("keepOldest = %+v, want the first record %+v", got, live)
	}
	if got := keepOldest(previousSettings{}, afterFirst); got != afterFirst {
		t.Errorf("keepOldest with nothing recorded = %+v, want %+v", got, afterFirst)
	}
}

func TestNoteSanitizesValues(t *testing.T) {
	// The note is read back with a whitespace split, so anything a kernel
	// parameter cannot contain is dropped rather than written.
	got := previousSettings{congestion: "reno\nrm -rf /", qdisc: "fq codel"}.note()
	if strings.ContainsAny(got, "\n") {
		t.Fatalf("the note kept a newline: %q", got)
	}
	if want := previousPrefix + "renorm-rf fqcodel"; got != want {
		t.Fatalf("note = %q, want %q", got, want)
	}
}

func TestReleasesComeOnlyFromThePublishedList(t *testing.T) {
	// Whatever the kernel project publishes is what the panel offers, in the order
	// a human reads versions. Nothing here is pinned in this repository, so no
	// test-only version may appear in the result.
	tags := []string{
		"bbrv3-cli",
		"arm64-9.1.0",
		"x86_64-9.1.0-max",
		"x86_64-9.1.0",
		"x86_64-9.0.9",
		"x86_64-9.10.0-max",
		"x86_64-latest",
	}
	got := releasesFromTags(tags, "x86_64")
	want := []Release{
		{Tag: "x86_64-9.10.0-max", Version: "9.10.0", Profile: Max},
		{Tag: "x86_64-9.1.0", Version: "9.1.0", Profile: Standard},
		{Tag: "x86_64-9.1.0-max", Version: "9.1.0", Profile: Max},
	}
	if len(got) != len(want) {
		t.Fatalf("releasesFromTags returned %d releases (%v), want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("release %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if len(releasesFromTags(tags, "riscv64")) != 0 {
		t.Error("an architecture without published kernels must come back empty")
	}
}

func TestReleasesKeepOnlyTheNewestVersions(t *testing.T) {
	// The menu is the current and previous kernel, each in whichever profiles the
	// project published, and never a longer archive.
	tags := []string{
		"x86_64-9.9.9",
		"x86_64-9.9.9-max",
		"x86_64-9.9.8",
		"x86_64-9.9.8-max",
		"x86_64-9.9.7",
		"x86_64-9.9.6-max",
	}
	got := releasesFromTags(tags, "x86_64")
	want := []Release{
		{Tag: "x86_64-9.9.9", Version: "9.9.9", Profile: Standard},
		{Tag: "x86_64-9.9.9-max", Version: "9.9.9", Profile: Max},
		{Tag: "x86_64-9.9.8", Version: "9.9.8", Profile: Standard},
		{Tag: "x86_64-9.9.8-max", Version: "9.9.8", Profile: Max},
	}
	if len(got) != len(want) {
		t.Fatalf("releasesFromTags returned %d releases (%v), want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("release %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestOutdatedKernel(t *testing.T) {
	cases := []struct {
		installed, latest string
		want              string
	}{
		{"7.2.0-minimaxflora-bbrv3", "7.2.6", "7.2.6"},
		{"7.2.0-minimaxflora-bbrv3-max", "7.2.6", "7.2.6"},
		{"7.2.6-minimaxflora-bbrv3", "7.2.6", ""},
		{"7.2.6-minimaxflora-bbrv3-max", "7.2.6", ""},
		{"7.2.6-minimaxflora-bbrv3", "7.2.7", "7.2.7"},
		{"7.6.0-minimaxflora-bbrv3", "7.10.0", "7.10.0"},
		{"7.10.0-minimaxflora-bbrv3", "7.6.0", ""},
		// Nothing installed, or nothing published: there is no upgrade to report.
		{"", "7.2.6", ""},
		{"7.2.0-minimaxflora-bbrv3", "", ""},
	}
	for _, c := range cases {
		st := Collected{Latest: c.latest}
		st.Running = "6.12.48+deb13-amd64"
		if c.installed != "" {
			st.Kernels = []string{"linux-image-" + c.installed}
		}
		got, ok := st.Outdated()
		if got != c.want || ok != (c.want != "") {
			t.Errorf("installed %q, latest %q: Outdated = %q, %v; want %q", c.installed, c.latest, got, ok, c.want)
		}
	}
}

func TestValidQdisc(t *testing.T) {
	for _, q := range Qdiscs {
		if !validQdisc(q) {
			t.Errorf("%q should be accepted", q)
		}
	}
	for _, q := range []string{"", "pfifo_fast", "fq; rm -rf /", "net.core.default_qdisc"} {
		if validQdisc(q) {
			t.Errorf("%q should be rejected", q)
		}
	}
}

func TestStatusReadings(t *testing.T) {
	st := Status{
		Running:    "6.12.48+deb13-amd64",
		Congestion: "cubic",
		Available:  "reno cubic bbr",
		Qdisc:      "fq_codel",
		Kernels:    []string{"linux-image-7.2.0-minimaxflora-bbrv3"},
	}
	if st.Enabled() {
		t.Error("cubic is not BBR")
	}
	if !st.AvailableBBR() {
		t.Error("the available list carries bbr, so it is offered")
	}
	if st.CustomRunning() {
		t.Error("the stock kernel is not a published one")
	}
	if !st.NeedsReboot() {
		t.Error("an installed kernel that is not running needs a reboot")
	}
	if got := st.CustomKernel(); got != "7.2.0-minimaxflora-bbrv3" {
		t.Errorf("CustomKernel = %q", got)
	}
	if got := st.CustomProfile(); got != Standard {
		t.Errorf("CustomProfile = %q", got)
	}

	st.Kernels = append(st.Kernels, "linux-image-7.2.6-minimaxflora-bbrv3-max")
	st.Running = "7.2.6-minimaxflora-bbrv3-max"
	st.Congestion = "bbr"
	if !st.Enabled() || !st.CustomRunning() || st.NeedsReboot() {
		t.Errorf("running the published kernel should need no reboot: %+v", st)
	}
	if got := st.CustomKernel(); got != "7.2.6-minimaxflora-bbrv3-max" {
		t.Errorf("CustomKernel should pick the newest, got %q", got)
	}
	if got := st.CustomProfile(); got != Max {
		t.Errorf("CustomProfile = %q", got)
	}
}
