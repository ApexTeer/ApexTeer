// Package sysinfo reports what the panel knows about the host it runs on - addresses,
// CPU, load, memory, swap, disk and uptime - and holds the runtime paths every other
// package writes to, so that /etc/sing-box is spelled out once.
package sysinfo

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/EasySBTeam/EasySB/internal/sbcore"
)

// The paths the deployment lives at and the units it installs. WorkDir is the one
// directory the rest are built from.
const (
	WorkDir     = "/etc/sing-box"
	ConfigJSON  = WorkDir + "/config.json"
	StateFile   = WorkDir + "/easysb.conf"
	ServiceName = "sing-box"

	CertDir        = WorkDir + "/cert"
	SelfSignedCert = CertDir + "/fullchain.cer"
	SelfSignedKey  = CertDir + "/private.key"
	LogFile        = WorkDir + "/easysb.log"

	SystemdUnit = "/etc/systemd/system/sing-box.service"

	// UsersFile holds the accounts that replaced the node-wide credential.
	UsersFile = WorkDir + "/easysb-users.json"
	// NodesFile holds the explicit protocol inbounds this host serves, which in
	// v6 replaced the single implicit node in the state file.
	NodesFile = WorkDir + "/easysb-nodes.json"
	// SubLogFile collects the subscription service log.
	SubLogFile = WorkDir + "/easysb-sub.log"
	// FirewallLedger records the port-hopping redirect rules EasySB has installed, so
	// they can be removed later without having to guess which node created them. A
	// rule whose node has since been deleted is otherwise unreachable: nothing on the
	// host knows it was ours.
	FirewallLedger = WorkDir + "/easysb-firewall.json"

	// SubServiceName is the subscription service's unit name. It is a unit of its own so
	// that the panel can restart it without touching the core.
	SubServiceName = "easysb"
	SubSystemdUnit = "/etc/systemd/system/easysb.service"

	// PanelPath is the preferred location of the panel binary: where the .deb puts it,
	// which is also where install.sh ends up, since it installs that package.
	// Anything that has to name the panel in a unit, a timer or a launcher takes its
	// candidates from PanelPaths rather than os.Executable(): a panel run from a scratch
	// copy (a test build, an unpacked tree) must not redirect the installed service to
	// that copy, because deleting the copy would then take the service down with it.
	PanelPath = "/usr/bin/easysb"
)

// PanelPaths are the candidate locations of the panel binary, most preferred first.
// The /usr/local entries are the legacy tarball install; they stay listed so an
// uninstall still finds and removes an old copy after the move to the .deb.
var PanelPaths = []string{PanelPath, "/usr/bin/sb", "/usr/local/bin/easysb", "/usr/local/bin/sb"}

// Status is one snapshot of the host for the dashboard: what the deployment is, how the
// services are doing, and the machine underneath them. A field the host would not answer
// stays at its zero value, so a blank cell means unknown rather than zero.
type Status struct {
	ScriptVersion string
	// CoreVersion is the sing-box release compiled into this panel, and
	// StatsCapable says whether that build carries the V2Ray API the traffic
	// columns are read from. Both are properties of the binary, not of the host:
	// there is no core to install, and no core file to ask.
	CoreVersion  string
	StatsCapable bool
	Service      string
	Autostart    string
	Domain       string
	SubPort      int
	SubSyncSecs  int
	Hop          string
	Ports        []PortInfo
	Deployed     bool
	StateFound   bool

	Hostname string
	OS       string
	Kernel   string
	Timezone string

	// LocalIPv4 and LocalIPv6 are the host's own addresses on its network
	// interfaces, kept apart so both families are visible at a glance.
	LocalIPv4 string
	LocalIPv6 string

	CPUCores int
	LoadAvg  string

	MemTotal  uint64
	MemAvail  uint64
	SwapTotal uint64
	SwapFree  uint64

	DiskTotal uint64
	DiskFree  uint64

	Uptime time.Duration

	// NetRxBytes and NetTxBytes are the cumulative bytes received and sent on
	// the host's non-loopback interfaces since boot, read from /proc/net/dev.
	// They are counters, so a rate is the difference between two snapshots.
	NetRxBytes uint64
	NetTxBytes uint64

	// DiskReadBytes and DiskWriteBytes are the cumulative bytes read from and
	// written to the host's whole block devices since boot, read from
	// /proc/diskstats. Like the network counters they are totals, so a rate is
	// the difference between two snapshots.
	DiskReadBytes  uint64
	DiskWriteBytes uint64
}

// PortInfo is one protocol's listener as the node state describes it: the port it is on
// and whether the protocol is switched on.
type PortInfo struct {
	Protocol string
	Port     string
	Enabled  bool
}

// Collect reads the whole snapshot: the state file, the two services' state, and the
// device readings. Everything it returns is read rather than remembered, so a page
// refreshed after a change shows the change.
func Collect(scriptVersion string) Status {
	st := Status{
		ScriptVersion: scriptVersion,
		Service:       "unknown",
		Autostart:     "unknown",
	}
	st.CoreVersion, st.StatsCapable = sbcore.Version(), sbcore.StatsCapable()
	st.Service = serviceState("is-active")
	st.Autostart = serviceState("is-enabled")

	collectDevice(&st)

	state := readState()
	st.StateFound = len(state) > 0
	st.Domain = state["DOMAIN"]
	if st.Domain == "" {
		st.Domain = state["CERT_DOMAIN"]
	}
	st.SubPort, _ = strconv.Atoi(state["SUB_SERVE_PORT"])
	st.SubSyncSecs, _ = strconv.Atoi(state["SUB_SYNC_SECONDS"])
	st.Hop = state["HY2_HOP_RANGE"]

	// The nodes are the source of truth for what is listening: one row per node,
	// with its own name so two nodes on the same protocol stay distinguishable.
	if ports := readNodePorts(); len(ports) > 0 {
		st.Deployed = true
		st.Ports = ports
	} else if _, err := os.Stat(ConfigJSON); err == nil {
		st.Deployed = true
	}
	return st
}

// collectDevice fills the host description shown on the dashboard: hostname,
// distribution name, kernel release and timezone, then local addresses, CPU,
// memory, swap, disk and uptime.
func collectDevice(st *Status) {
	if h, err := os.Hostname(); err == nil {
		st.Hostname = strings.TrimSpace(h)
	}
	st.OS = osName()
	st.Kernel = kernelRelease()
	st.Timezone = timezone()
	st.LocalIPv4, st.LocalIPv6 = localIPs()
	st.CPUCores = runtime.NumCPU()
	st.LoadAvg = loadAvg()
	st.MemTotal, st.MemAvail, st.SwapTotal, st.SwapFree = memory()
	st.DiskTotal, st.DiskFree = diskUsage("/")
	st.Uptime = uptime()
	st.NetRxBytes, st.NetTxBytes = NetworkTotals()
	st.DiskReadBytes, st.DiskWriteBytes = DiskTotals()
}

// Usage returns the live memory, swap and root-filesystem usage in bytes, which
// is what the overview trend samples every couple of seconds. It reads
// /proc/meminfo once and the root filesystem once, and returns zero for a
// reading the host would not give.
func Usage() (memUsed, memTotal, swapUsed, swapTotal, diskUsed, diskTotal uint64) {
	memTotal, memAvail, swapTotal, swapFree := memory()
	if memTotal > memAvail {
		memUsed = memTotal - memAvail
	}
	if swapTotal > swapFree {
		swapUsed = swapTotal - swapFree
	}
	diskTotal, diskFree := diskUsage("/")
	if diskTotal > diskFree {
		diskUsed = diskTotal - diskFree
	}
	return memUsed, memTotal, swapUsed, swapTotal, diskUsed, diskTotal
}

// sectorSize is the unit /proc/diskstats counts transfers in. The kernel has
// reported 512-byte sectors there since long before 4K-native disks, and it
// stays 512 regardless of the device's physical sector size.
const sectorSize = 512

// wholeDiskRE matches the whole block devices whose traffic is worth counting.
// Partitions (sda1, nvme0n1p2) and stacked devices (dm-0, md0) are left out:
// the partitions are already inside their disk's numbers, and the device-mapper
// and md layers would double-count IO that the underlying disks already report.
var wholeDiskRE = regexp.MustCompile(`^(sd[a-z]+|vd[a-z]+|xvd[a-z]+|hd[a-z]+|nvme[0-9]+n[0-9]+|mmcblk[0-9]+)$`)

// parseDiskStats sums the sectors read and written across the whole block
// devices in /proc/diskstats. Each line is "major minor name reads ... sectors
// read ... writes ... sectors written ...", so the read sector count is field 5
// and the write sector count is field 9 once the name is included.
func parseDiskStats(data []byte) (read, write uint64) {
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 10 || !wholeDiskRE.MatchString(fields[2]) {
			continue
		}
		if sectors, err := strconv.ParseUint(fields[5], 10, 64); err == nil {
			read += sectors
		}
		if sectors, err := strconv.ParseUint(fields[9], 10, 64); err == nil {
			write += sectors
		}
	}
	return read * sectorSize, write * sectorSize
}

// parseNetDev sums the bytes columns of /proc/net/dev across every interface
// except the loopback. The kernel prints two header lines, then one line per
// interface as "name: rx_bytes rx_packets ... tx_bytes ...".
func parseNetDev(data []byte) (rx, tx uint64) {
	for _, line := range strings.Split(string(data), "\n") {
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if strings.TrimSpace(name) == "lo" {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) < 9 {
			continue
		}
		r, err1 := strconv.ParseUint(fields[0], 10, 64)
		t, err2 := strconv.ParseUint(fields[8], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		rx += r
		tx += t
	}
	return rx, tx
}

// localIPs returns the first usable IPv4 and IPv6 address on an up, non-loopback
// interface. A globally routable IPv6 address wins over a unique-local one.
func localIPs() (string, string) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", ""
	}
	var v4, v6, v6Global string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipnet.IP
			if ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			if ip4 := ip.To4(); ip4 != nil {
				if v4 == "" {
					v4 = ip4.String()
				}
				continue
			}
			if v6 == "" {
				v6 = ip.String()
			}
			if v6Global == "" && !ip.IsPrivate() {
				v6Global = ip.String()
			}
		}
	}
	if v6Global != "" {
		v6 = v6Global
	}
	return v4, v6
}

func loadAvg() string {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return ""
	}
	return parseLoadAvg(data)
}

func parseLoadAvg(data []byte) string {
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return ""
	}
	out := make([]string, 0, 3)
	for _, f := range fields[:3] {
		v, err := strconv.ParseFloat(f, 64)
		if err != nil {
			return ""
		}
		out = append(out, strconv.FormatFloat(v, 'f', 2, 64))
	}
	return strings.Join(out, " ")
}

func memory() (uint64, uint64, uint64, uint64) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, 0, 0
	}
	return parseMeminfo(data)
}

// parseMeminfo returns total and available memory plus total and free swap, in
// bytes. MemAvailable is preferred; MemFree is the fallback on kernels that
// predate it.
func parseMeminfo(data []byte) (total, avail, swapTotal, swapFree uint64) {
	for _, line := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "MemTotal":
			total = parseKB(v)
		case "MemAvailable":
			avail = parseKB(v)
		case "MemFree":
			if avail == 0 {
				avail = parseKB(v)
			}
		case "SwapTotal":
			swapTotal = parseKB(v)
		case "SwapFree":
			swapFree = parseKB(v)
		}
	}
	return total, avail, swapTotal, swapFree
}

func parseKB(s string) uint64 {
	fields := strings.Fields(strings.TrimSpace(s))
	if len(fields) == 0 {
		return 0
	}
	n, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return 0
	}
	return n * 1024
}

func uptime() time.Duration {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	return parseUptime(data)
}

func parseUptime(data []byte) time.Duration {
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0
	}
	secs, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || secs <= 0 {
		return 0
	}
	return time.Duration(secs * float64(time.Second))
}

func osName() string {
	if v := osReleaseValue("PRETTY_NAME"); v != "" {
		return v
	}
	if v := osReleaseValue("NAME"); v != "" {
		return v
	}
	return ""
}

func osReleaseValue(key string) string {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) != key {
			continue
		}
		return strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return ""
}

func kernelRelease() string {
	if data, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		if v := strings.TrimSpace(string(data)); v != "" {
			return v
		}
	}
	if out, err := run(2*time.Second, "uname", "-r"); err == nil {
		return strings.TrimSpace(out)
	}
	return ""
}

func timezone() string {
	if data, err := os.ReadFile("/etc/timezone"); err == nil {
		if v := strings.TrimSpace(string(data)); v != "" {
			return v
		}
	}
	if link, err := os.Readlink("/etc/localtime"); err == nil {
		if i := strings.Index(link, "zoneinfo/"); i >= 0 {
			return link[i+len("zoneinfo/"):]
		}
	}
	if name := time.Now().Format("MST"); name != "" {
		return name
	}
	return "UTC"
}

func serviceState(verb string) string {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return "unknown"
	}
	out, _ := run(3*time.Second, "systemctl", verb, ServiceName)
	out = strings.TrimSpace(out)
	if out == "" {
		return "unknown"
	}
	switch verb {
	case "is-active":
		if out == "active" {
			return "running"
		}
		return "stopped"
	default:
		if strings.HasPrefix(out, "enabled") {
			return "enabled"
		}
		return "disabled"
	}
}

func readState() map[string]string {
	return ReadKeyValues(StateFile)
}

// ReadKeyValues parses the `KEY="value"` configuration file the legacy shell tool
// wrote and /etc/sing-box/easysb.conf still uses. A line longer than the scanner's
// default limit is accepted rather than silently truncating the rest of the file,
// and a value written through %q round-trips because it is unquoted with the
// matching rule.
func ReadKeyValues(path string) map[string]string {
	values := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return values
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		values[key] = unquoteValue(val)
	}
	return values
}

// unquoteValue reads one value the way the shell that wrote it would: a
// double-quoted value is a Go-escaped literal (the writer uses %q), so it is
// unquoted with strconv.Unquote; a single-quoted or bare value is taken literally.
func unquoteValue(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		if unquoted, err := strconv.Unquote(v); err == nil {
			return unquoted
		}
	}
	return strings.Trim(v, `"'`)
}

// readNodePorts reads the node file the panel writes. It parses the small
// document directly rather than importing internal/node, because the state
// package this one feeds already imports it and the two would form a cycle.
func readNodePorts() []PortInfo {
	data, err := os.ReadFile(NodesFile)
	if err != nil {
		return nil
	}
	var doc struct {
		Nodes []struct {
			Name    string `json:"name"`
			Port    int    `json:"port"`
			Enabled bool   `json:"enabled"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil
	}
	out := make([]PortInfo, 0, len(doc.Nodes))
	for _, n := range doc.Nodes {
		port := ""
		if n.Enabled {
			port = strconv.Itoa(n.Port)
		}
		out = append(out, PortInfo{Protocol: n.Name, Port: port, Enabled: n.Enabled})
	}
	return out
}

func run(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	return string(out), err
}
