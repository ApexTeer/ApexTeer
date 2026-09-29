package bbr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/MinimaxFlora/EasySB/internal/download"
)

// sysctlGet reads one kernel parameter (empty when it cannot be read).
func sysctlGet(ctx context.Context, key string) string {
	out, err := run(ctx, "sysctl", "-n", key)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// unameRelease is the kernel the machine is running right now.
func unameRelease(ctx context.Context) string {
	out, err := run(ctx, "uname", "-r")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// unameMachine is the machine architecture as the kernel reports it.
func unameMachine(ctx context.Context) string {
	out, err := run(ctx, "uname", "-m")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// Status is one reading of the machine's congestion control state.
type Status struct {
	Arch       string // release architecture, empty when unsupported
	Running    string // running kernel release, e.g. 6.12.48+deb13-amd64
	Congestion string // net.ipv4.tcp_congestion_control
	Available  string // net.ipv4.tcp_available_congestion_control
	Qdisc      string // net.core.default_qdisc
	Kernels    []string
}

// Enabled reports whether BBR is the active congestion control.
func (s Status) Enabled() bool { return s.Congestion == "bbr" }

// Supported reports whether the published kernels cover this architecture.
func (s Status) Supported() bool { return s.Arch != "" }

// AvailableBBR reports whether the running kernel offers BBR at all. The module can
// be loadable without being loaded, so the available list is the honest answer.
func (s Status) AvailableBBR() bool {
	return strings.Contains(s.Available, "bbr") || s.Enabled()
}

// CustomKernel is the installed BBRv3 kernel release, or "" when none is
// installed. The newest one wins: an upgrade can leave the old package behind.
func (s Status) CustomKernel() string {
	newest := ""
	for _, name := range s.Kernels {
		release := strings.TrimPrefix(name, "linux-image-")
		if VersionGE(release, newest) {
			newest = release
		}
	}
	return newest
}

// CustomProfile is the installed kernel's profile.
func (s Status) CustomProfile() Profile {
	if strings.Contains(s.CustomKernel(), "-max") {
		return Max
	}
	return Standard
}

// CustomVersion is the version part of the installed release, e.g. 7.2.6 out of
// 7.2.6-minimaxflora-bbrv3.
func (s Status) CustomVersion() string {
	kernel := s.CustomKernel()
	if i := strings.Index(kernel, "-"); i > 0 {
		return kernel[:i]
	}
	return kernel
}

// Outdated reports a published version newer than the installed kernel, and which
// one. Both sides come from the kernel project: the installed release and the
// newest published version. Nothing is pinned in this repository, so a kernel
// published upstream shows up here on the next status read.
func (c Collected) Outdated() (string, bool) {
	current := c.CustomVersion()
	if current == "" || c.Latest == "" {
		return "", false
	}
	if compareVersions(c.Latest, current) > 0 {
		return c.Latest, true
	}
	return "", false
}

// CustomRunning reports whether the running kernel is one of the published ones.
func (s Status) CustomRunning() bool {
	return strings.Contains(s.Running, KernelBrand)
}

// NeedsReboot reports whether a BBRv3 kernel is installed but not yet running.
func (s Status) NeedsReboot() bool {
	return len(s.Kernels) > 0 && !s.CustomRunning()
}

// Collected is Status plus the network lookups, which the status screen shows
// separately because they fail on their own.
type Collected struct {
	Status
	Latest    string // newest published kernel version, "" when unknown
	LatestErr string
}

// LocalStatus reads the machine's state without touching the network, for the
// callers that only need to know what is installed and what is running.
func LocalStatus(ctx context.Context) Status {
	st := Status{
		Running:    unameRelease(ctx),
		Congestion: sysctlGet(ctx, "net.ipv4.tcp_congestion_control"),
		Available:  sysctlGet(ctx, "net.ipv4.tcp_available_congestion_control"),
		Qdisc:      sysctlGet(ctx, "net.core.default_qdisc"),
		Kernels:    installedKernels(ctx),
	}
	if arch, ok := ArchName(unameMachine(ctx)); ok {
		st.Arch = arch
	}
	return st
}

// Collect reads the local state and asks the kernel project for its newest
// version. The local half always answers; a failed lookup only fills LatestErr.
func Collect(ctx context.Context) Collected {
	out := Collected{Status: LocalStatus(ctx)}
	latest, err := LatestVersion(ctx)
	if err != nil {
		out.LatestErr = err.Error()
		return out
	}
	out.Latest = latest
	return out
}

// installedKernels lists the installed packages of the kernel project.
func installedKernels(ctx context.Context) []string {
	out, err := run(ctx, "dpkg-query", "-W", "-f", "${db:Status-Abbrev} ${binary:Package}\n", "linux-image-*"+KernelBrand+"*")
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
	sort.Strings(pkgs)
	return pkgs
}

// previousSettings is the congestion control and queue discipline the kernel was
// running before EasySB wrote its drop-in. Recording them is what makes clearing
// an honest undo: the distro default is not always the kernel's own default.
type previousSettings struct {
	congestion string
	qdisc      string
}

// previousPrefix marks the line EasySB keeps at the top of its sysctl drop-in.
const previousPrefix = "# easysb previous: "

// captureSettings reads the values that are live right now.
func captureSettings(ctx context.Context) previousSettings {
	return previousSettings{
		congestion: sysctlGet(ctx, "net.ipv4.tcp_congestion_control"),
		qdisc:      sysctlGet(ctx, "net.core.default_qdisc"),
	}
}

// note renders the settings as the drop-in comment.
func (p previousSettings) note() string {
	return previousPrefix + sanitizeValue(p.congestion) + " " + sanitizeValue(p.qdisc)
}

// keepOldest prefers what an earlier run recorded: the note has to describe the
// settings BBR replaced, not the ones BBR itself is running.
func keepOldest(recorded, live previousSettings) previousSettings {
	if recorded != (previousSettings{}) {
		return recorded
	}
	return live
}

// readPrevious parses the comment back out of a drop-in, empty when there is none.
func readPrevious(path string) previousSettings {
	body, err := os.ReadFile(path)
	if err != nil {
		return previousSettings{}
	}
	for _, line := range strings.Split(string(body), "\n") {
		if !strings.HasPrefix(line, previousPrefix) {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, previousPrefix))
		if len(fields) == 0 {
			continue
		}
		p := previousSettings{congestion: fields[0]}
		if len(fields) > 1 {
			p.qdisc = fields[1]
		}
		return p
	}
	return previousSettings{}
}

// restore puts the recorded values back, skipping any control the kernel no
// longer offers.
func (p previousSettings) restore(ctx context.Context, log func(string)) {
	if p.congestion != "" && available(ctx, p.congestion) {
		log("sysctl -w net.ipv4.tcp_congestion_control=" + p.congestion)
		_, _ = run(ctx, "sysctl", "-w", "net.ipv4.tcp_congestion_control="+p.congestion)
	}
	if p.qdisc != "" {
		log("sysctl -w net.core.default_qdisc=" + p.qdisc)
		_, _ = run(ctx, "sysctl", "-w", "net.core.default_qdisc="+p.qdisc)
	}
}

// sanitizeValue keeps a sysctl reading to the characters a kernel control can
// hold, so the note cannot smuggle anything into the file.
func sanitizeValue(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Enable switches the running kernel to BBR and persists the choice. qdisc is one
// of Qdiscs; fq is used when it is empty. The log gets the bare commands, the
// same way the core installer logs its steps; the caller frames them with
// localized lines.
func Enable(ctx context.Context, log func(string), qdisc string) error {
	if qdisc == "" {
		qdisc = Qdiscs[0]
	}
	if !validQdisc(qdisc) {
		return fmt.Errorf("unknown qdisc: %s", qdisc)
	}
	// Read what is live before touching it, or the undo has nothing to restore.
	previous := captureSettings(ctx)
	// The module has to be in place before the parameter can name it: writing
	// tcp_congestion_control=bbr on a kernel that lacks the module fails.
	if !available(ctx, "bbr") {
		log("modprobe tcp_bbr")
		_, _ = run(ctx, "modprobe", "tcp_bbr")
		if !available(ctx, "bbr") {
			return errors.New("this kernel has no bbr module: install the BBRv3 kernel first")
		}
	}
	modprobeQuiet(ctx, "sch_"+strings.ReplaceAll(qdisc, "-", "_"))

	log("sysctl -w net.core.default_qdisc=" + qdisc)
	if _, err := run(ctx, "sysctl", "-w", "net.core.default_qdisc="+qdisc); err != nil {
		return fmt.Errorf("net.core.default_qdisc=%s: %w", qdisc, err)
	}
	log("sysctl -w net.ipv4.tcp_congestion_control=bbr")
	if _, err := run(ctx, "sysctl", "-w", "net.ipv4.tcp_congestion_control=bbr"); err != nil {
		return fmt.Errorf("net.ipv4.tcp_congestion_control=bbr: %w", err)
	}

	// Read back instead of trusting sysctl's exit code: a kernel without BBR
	// accepts the write and silently keeps cubic.
	got := sysctlGet(ctx, "net.ipv4.tcp_congestion_control")
	if got != "bbr" {
		return fmt.Errorf("congestion control is still %q", got)
	}
	log("write " + SysctlConf)
	log("write " + ModulesConf)
	return persist(qdisc, previous)
}

// Clear removes EasySB's drop-ins and puts the settings it replaced back.
func Clear(ctx context.Context, log func(string)) (bool, error) {
	previous := readPrevious(SysctlConf)
	cleared := false
	for _, path := range []string{SysctlConf, ModulesConf} {
		err := os.Remove(path)
		if err == nil {
			cleared = true
			log("remove " + path)
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return cleared, err
		}
	}
	// The live values: a reboot would drop them anyway, but leaving BBR on after
	// the operator asked for it to be cleared would be misleading.
	previous.restore(ctx, log)
	return cleared, nil
}

// persist writes the drop-ins that bring the choice back after a reboot. The
// module list matters as much as the sysctl: without tcp_bbr loaded early, the
// parameter below cannot name bbr.
func persist(qdisc string, previous previousSettings) error {
	// An earlier run's record wins over what is live now: enabling twice would
	// otherwise record "bbr" as the thing to restore, and clearing would leave
	// the operator with BBR on and no note of what it replaced.
	previous = keepOldest(readPrevious(SysctlConf), previous)
	body := previous.note() + "\nnet.core.default_qdisc = " + qdisc + "\nnet.ipv4.tcp_congestion_control = bbr\n"
	if err := writeRoot(SysctlConf, body); err != nil {
		return err
	}
	modules := "tcp_bbr\n"
	if qdisc != "fq" {
		modules += "sch_" + strings.ReplaceAll(qdisc, "-", "_") + "\n"
	}
	return writeRoot(ModulesConf, modules)
}

// writeRoot writes a file with the permissions a sysctl drop-in needs.
func writeRoot(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// validQdisc reports whether name is one of the queue disciplines BBR is paired
// with. Anything else would be written into a sysctl file unchecked.
func validQdisc(name string) bool {
	for _, q := range Qdiscs {
		if q == name {
			return true
		}
	}
	return false
}

// available reports whether the running kernel offers this congestion control.
func available(ctx context.Context, name string) bool {
	list := sysctlGet(ctx, "net.ipv4.tcp_available_congestion_control")
	for _, field := range strings.Fields(list) {
		if field == name {
			return true
		}
	}
	return false
}

// modprobeQuiet loads a module when the kernel has it, and stays silent when it
// does not: a missing sch_ module only costs the optional queueing discipline.
func modprobeQuiet(ctx context.Context, module string) {
	_, _ = run(ctx, "modprobe", module)
}

// LatestVersion reads the newest published kernel version. The version stamp
// published with the kernel project's CLI answers first because it is one small
// file; the release list is the fallback.
// LatestVersion returns the newest kernel version this machine can install.
//
// The release list answers first, filtered by architecture, because that list is
// what the install path consumes. The version stamp is only a fallback: it is
// published by the kernel project's own build and has been seen trailing the
// releases, so taking it as the answer would offer an older kernel than the one
// sitting in the release list.
func LatestVersion(ctx context.Context) (string, error) {
	arch, _ := ArchName(unameMachine(ctx))
	tags, err := releaseTags(ctx)
	if err == nil {
		if v := NewestVersionFor(tags, arch); v != "" {
			return v, nil
		}
	}
	if v, err := versionFromStamp(ctx); err == nil && v != "" {
		return v, nil
	}
	if err != nil {
		return "", err
	}
	return "", errors.New("no published kernel found")
}

// versionFromStamp downloads and parses the [kernel] version stamp.
func versionFromStamp(ctx context.Context) (string, error) {
	tmp, err := os.CreateTemp("", "easysb-bbr-version-*")
	if err != nil {
		return "", err
	}
	path := tmp.Name()
	tmp.Close()
	defer os.Remove(path)

	if err := fetchFile(ctx, versionINIURL, path, nil); err != nil {
		return "", err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return parseStamp(string(body)), nil
}

// stampSectionRE finds the [kernel] section of the version stamp.
var stampSectionRE = regexp.MustCompile(`(?m)^\s*\[kernel\]\s*$`)

// parseStamp pulls the newest version out of the [kernel] sections of a version.ini
// body. The kernel project's build appends a section per release instead of
// rewriting one, so a stamp on disk reads:
//
//	[cli]
//	commit=187b4b8e
//	[kernel]
//	version=7.2.0
//	[kernel]
//	version=7.2.2
//	[kernel]
//	version=7.2.6
//
// Taking the first one would report the oldest kernel the project ever built, so
// every section is read and the highest version wins.
func parseStamp(body string) string {
	newest := ""
	inKernel := false
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if stampSectionRE.MatchString(line) {
			inKernel = true
			continue
		}
		if strings.HasPrefix(line, "[") {
			inKernel = false
			continue
		}
		if !inKernel || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "version" {
			continue
		}
		// The stamp is hand-editable, so a trailing comment is not a version.
		if i := strings.Index(value, "#"); i >= 0 {
			value = value[:i]
		}
		if version := strings.TrimSpace(value); VersionGE(version, newest) {
			newest = version
		}
	}
	return newest
}

// releaseTags lists every release tag of the kernel project.
func releaseTags(ctx context.Context) ([]string, error) {
	body, err := httpGet(ctx, apiURL+"?per_page=100")
	if err != nil {
		return nil, err
	}
	var rels []struct {
		Tag string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &rels); err != nil {
		return nil, err
	}
	tags := make([]string, 0, len(rels))
	for _, r := range rels {
		tags = append(tags, r.Tag)
	}
	return tags, nil
}

// releaseAssets lists the asset names of one release tag, and the fallback the
// caller should use when the API is unreachable.
func releaseAssets(ctx context.Context, tag string) ([]string, error) {
	full := apiURL + "/tags/" + tag
	body, err := httpGet(ctx, full)
	if err != nil {
		return nil, err
	}
	var rel struct {
		Assets []struct {
			Name string `json:"name"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(body, &rel); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(rel.Assets))
	for _, a := range rel.Assets {
		if strings.HasSuffix(a.Name, ".deb") {
			names = append(names, a.Name)
		}
	}
	return names, nil
}

// run executes a command with the same locale pinning the rest of the panel uses,
// so dpkg and apt output stay parseable regardless of the operator's shell.
func run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "DEBIAN_FRONTEND=noninteractive")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// httpGet fetches one API document.
func httpGet(ctx context.Context, url string) ([]byte, error) {
	hc := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "EasySB")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return body, nil
}

// download fetches one package straight from GitHub. The kernels are installed
// from the release the kernel project published, so the bytes never pass through a
// third party: what dpkg unpacks is what its build produced. progress, when it is
// set, is how the panel draws a bar for a package that takes minutes.
// fetchFile streams a URL to disk through the shared downloader, reporting the bytes
// when the caller wants readings.
func fetchFile(ctx context.Context, url, dest string, progress download.Progress) error {
	return download.WithProgress(ctx, url, dest, downloadTimeout, progress)
}
