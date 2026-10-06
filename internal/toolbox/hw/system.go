package hw

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/EasySBTeam/EasySB/internal/toolbox"
)

// The files the system table reads. They are named constants so a fixture and the code
// cannot drift apart silently.
const (
	pathCPUInfo    = "/proc/cpuinfo"
	pathCacheRoot  = "/sys/devices/system/cpu/cpu0/cache"
	pathCPUFreqMin = "/sys/devices/system/cpu/cpu0/cpufreq/cpuinfo_min_freq"
	pathCPUFreqMax = "/sys/devices/system/cpu/cpu0/cpufreq/cpuinfo_max_freq"
	pathMemInfo    = "/proc/meminfo"
	pathUptime     = "/proc/uptime"
	pathLoadAvg    = "/proc/loadavg"
	pathProcDir    = "/proc"
	pathHostname   = "/proc/sys/kernel/hostname"
	pathOSRelease  = "/etc/os-release"
	pathKernel     = "/proc/sys/kernel/osrelease"
	pathTimezone   = "/etc/timezone"
	pathLocaltime  = "/etc/localtime"
	pathDMIVendor  = "/sys/class/dmi/id/sys_vendor"
	pathDMIProduct = "/sys/class/dmi/id/product_name"
)

// External commands the system table may use. Both are optional: a minimal image may
// have neither, and the table then explains what it could not confirm instead of
// reporting a state nobody measured.
const (
	toolVirt       = "systemd-detect-virt"
	toolTimeDateCt = "timedatectl"
	virtTimeout    = 5 * time.Second
	clockTimeout   = 5 * time.Second
	// ipLookupTimeout bounds the egress lookup, the only reading here that depends on
	// something outside the host.
	ipLookupTimeout = 8 * time.Second
)

// ipAPIURL is ip-api.com's free lookup. Plain http is deliberate: the free tier does
// not answer https, and the request carries nothing but the connection itself. The
// fields list keeps the answer small and the parsing tight.
const ipAPIURL = "http://ip-api.com/json/?fields=status,message,query,country,regionName,city,isp,org,as"

// Row labels. A toolbox tool's own cell text is rendered as it comes, so these are in
// the language the panel's other tables use.
const (
	labelItem      = "项目"
	labelValue     = "值"
	labelHostname  = "主机名"
	labelDistro    = "系统"
	labelKernel    = "内核"
	labelArch      = "架构"
	labelVirt      = "虚拟化"
	labelCPUModel  = "CPU 型号"
	labelCPUCores  = "CPU 核心"
	labelCPUClock  = "CPU 主频"
	labelCPUCache  = "CPU 缓存"
	labelMemory    = "内存"
	labelSwap      = "Swap"
	labelUptime    = "开机时长"
	labelLoad      = "负载（1/5/15 分钟）"
	labelProcesses = "进程与线程"
	labelTimezone  = "时区"
	labelNTP       = "时间同步"
	labelEgressIP  = "出口 IP"
	labelIPOwner   = "IP 归属"
	labelASN       = "ASN 与运营商"
	labelCollected = "采集时间"
)

// systemHost collects one system table.
type systemHost struct {
	env  Env
	opts toolbox.Options
	// virt is the virtualization row's text once virtualization() has decided it, so
	// the summary can carry the same verdict. Empty means the row was left out.
	virt string
}

// collect builds the whole table in the order an operator reads it: who the host is,
// what it computes with, how much memory it has, how long it has been up, and finally
// where its traffic leaves from.
func (h *systemHost) collect(ctx context.Context) toolbox.Result {
	res := toolbox.Result{Headers: []string{labelItem, labelValue}}
	fsys := h.env.FS

	readFile(fsys, pathHostname).row(&res, labelHostname)
	if name := osPrettyName(readFile(fsys, pathOSRelease).text); name != "" {
		res.Add(labelDistro, name)
	} else {
		res.Note("%s：没有读到 PRETTY_NAME 或 NAME 字段", labelDistro)
	}
	readFile(fsys, pathKernel).row(&res, labelKernel)

	// The architecture is a property of the binary, not of the host, so the row says
	// so: a 386 panel in a 64-bit container is telling the truth about neither.
	res.Add(labelArch, runtime.GOARCH)
	res.Note("架构来自面板二进制的构建目标（%s/%s），不是从主机读到的", runtime.GOOS, runtime.GOARCH)

	cpuReading := readFile(fsys, pathCPUInfo)
	cpu := parseCPUInfo(cpuReading.text)
	if cpuReading.text == "" {
		res.Note("CPU：%s，CPU 各项均无从报告", cpuReading.why)
	}
	h.virtualization(ctx, &res, cpu)
	h.cpu(&res, cpu)
	h.memory(&res)
	h.uptime(&res)
	h.load(&res)
	h.clock(ctx, &res)
	h.egress(ctx, &res)
	res.Add(labelCollected, h.opts.Now().Format("2006-01-02 15:04:05 MST"))

	res.Summary = systemSummary(cpu, parseMemInfo(readFile(fsys, pathMemInfo).text).total, h.virt)
	// The board line is the machine in three facts: what it is, how many cores, how much
	// memory. The virtualization verdict and the DMI identity are rows in the report.
	res.Board = boardSystemLine(cpu, parseMemInfo(readFile(fsys, pathMemInfo).text).total)
	res.Note("数据来源：/proc/cpuinfo、/sys/devices/system/cpu/cpu0/cache、/proc/meminfo、/proc/uptime、/proc/loadavg、/proc、/etc/os-release、/proc/sys/kernel/*、/sys/class/dmi/id/*。")
	return res
}

// file reads one path and returns the raw text with the reason it is unusable.
func (h *systemHost) file(path string) reading { return readFile(h.env.FS, path) }

// cpu fills the model, core count, clock and cache rows from /proc/cpuinfo.
func (h *systemHost) cpu(res *toolbox.Result, cpu cpuInfo) {
	if cpu.model != "" {
		res.Add(labelCPUModel, cpu.model)
	} else {
		res.Note("CPU 型号：%s 里没有 model name / Hardware 之类的字段", pathCPUInfo)
	}

	switch {
	case cpu.logical == 0:
		res.Note("%s：%s 里没有解析到 processor 记录", labelCPUCores, pathCPUInfo)
	case cpu.physical > 0 && cpu.sockets > 0:
		res.Add(labelCPUCores, fmt.Sprintf("%d 路 × %d 核 = %d 物理核 / %d 逻辑线程",
			cpu.sockets, cpu.physical/cpu.sockets, cpu.physical, cpu.logical))
	case cpu.physical > 0:
		res.Add(labelCPUCores, fmt.Sprintf("%d 物理核 / %d 逻辑线程", cpu.physical, cpu.logical))
	default:
		res.Add(labelCPUCores, fmt.Sprintf("逻辑线程 %d", cpu.logical))
		res.Note("物理核数：%s 没有同时给出 physical id 与 core id（ARM 与部分虚拟机如此），只能报告逻辑线程", pathCPUInfo)
	}

	switch {
	case cpu.mhzCount > 0:
		if cpu.mhzMax-cpu.mhzMin < 1 {
			res.Add(labelCPUClock, fmt.Sprintf("%.0f MHz（各核一致）", cpu.mhzMin))
		} else {
			res.Add(labelCPUClock, fmt.Sprintf("%.0f–%.0f MHz（均值 %.0f）", cpu.mhzMin, cpu.mhzMax, cpu.mhzAvg))
		}
		res.Note("主频是采集瞬间的采样值（%s 的 cpu MHz，共 %d 个核），会随负载与节能策略变化", pathCPUInfo, cpu.mhzCount)
	default:
		h.cpuClockFromCPUFreq(res)
	}

	caches, err := h.caches()
	switch {
	case err != nil:
		res.Note("CPU 缓存：无法列出 %s（%v）", pathCacheRoot, err)
	case len(caches) == 0:
		res.Note("CPU 缓存：%s 下没有 index* 条目，无法报告各级缓存", pathCacheRoot)
	default:
		res.Add(labelCPUCache, strings.Join(caches, " · "))
		res.Note("缓存来自 %s（同一插槽各核相同），未跨插槽合计", pathCacheRoot)
	}
}

// cpuClockFromCPUFreq is the fallback for hosts whose cpuinfo carries no frequency,
// which is the normal state of an ARM board and of a number of hypervisors: the
// cpufreq driver knows the range even when cpuinfo stays silent.
func (h *systemHost) cpuClockFromCPUFreq(res *toolbox.Result) {
	minReading := h.file(pathCPUFreqMin)
	maxReading := h.file(pathCPUFreqMax)
	if lo, err := khzToMHz(minReading.text); err == nil && minReading.text != "" {
		if hi, err := khzToMHz(maxReading.text); err == nil && maxReading.text != "" {
			res.Add(labelCPUClock, fmt.Sprintf("%d–%d MHz（cpufreq 报告的可用范围）", lo, hi))
			return
		}
	}
	why := maxReading.why
	if why == "" {
		why = minReading.why
	}
	res.Note("CPU 主频：%s 没有 cpu MHz，%s 也不可读（%s）；ARM 与部分虚拟机的内核不上报主频", pathCPUInfo, pathCPUFreqMax, why)
}

// caches reads the cache hierarchy of cpu0 in index order. One CPU stands for the
// socket because the kernel repeats the same shared caches on every core.
func (h *systemHost) caches() ([]string, error) {
	names, err := h.env.FS.ReadDir(pathCacheRoot)
	if err != nil {
		return nil, err
	}
	indexes := make([]string, 0, len(names))
	for _, name := range names {
		if strings.HasPrefix(name, "index") {
			indexes = append(indexes, name)
		}
	}
	sort.Slice(indexes, func(i, j int) bool {
		a, errA := strconv.Atoi(strings.TrimPrefix(indexes[i], "index"))
		b, errB := strconv.Atoi(strings.TrimPrefix(indexes[j], "index"))
		if errA != nil || errB != nil {
			return indexes[i] < indexes[j]
		}
		return a < b
	})

	var out []string
	for _, name := range indexes {
		base := pathCacheRoot + "/" + name
		level := readFile(h.env.FS, base+"/level").text
		kind := readFile(h.env.FS, base+"/type").text
		size := readFile(h.env.FS, base+"/size").text
		if level == "" || size == "" {
			continue
		}
		label := "L" + level
		switch strings.ToLower(kind) {
		case "data":
			label += "d"
		case "instruction":
			label += "i"
		}
		out = append(out, label+" "+cacheSize(size))
	}
	return out, nil
}

// memory fills the memory and swap rows from /proc/meminfo.
func (h *systemHost) memory(res *toolbox.Result) {
	r := h.file(pathMemInfo)
	if r.text == "" {
		res.Note("内存：%s，内存与 Swap 都无法报告", r.why)
		return
	}
	mem := parseMemInfo(r.text)
	if mem.total == 0 {
		res.Note("内存：%s 没有 MemTotal 字段", pathMemInfo)
	} else {
		used := uint64(0)
		if mem.available <= mem.total {
			used = mem.total - mem.available
		}
		res.Add(labelMemory, fmt.Sprintf("%s 总 / %s 可用（已用 %s）",
			humanBytes(mem.total), humanBytes(mem.available), humanBytes(used)))
		if mem.memAvailableMissing {
			res.Note("内存可用量来自 MemFree：这台内核的 %s 没有 MemAvailable 字段（早于 3.14 的内核）", pathMemInfo)
		}
	}
	if mem.swapTotal == 0 {
		// No swap is a configuration rather than a missing reading, so it is a row and
		// not a note: an operator sizing a box wants to see that there is none.
		res.Add(labelSwap, "未配置")
		return
	}
	res.Add(labelSwap, fmt.Sprintf("%s 总 / %s 空闲", humanBytes(mem.swapTotal), humanBytes(mem.swapFree)))
}

// uptime fills the uptime row from /proc/uptime.
func (h *systemHost) uptime(res *toolbox.Result) {
	r := h.file(pathUptime)
	if r.text == "" {
		res.Note("开机时长：%s", r.why)
		return
	}
	d := parseUptime(r.text)
	if d <= 0 {
		res.Note("开机时长：%s 的内容无法解析（%q）", pathUptime, firstLine(r.text))
		return
	}
	res.Add(labelUptime, humanDuration(d))
}

// load fills the load row and the process count. The two counts come from different
// files on purpose: /proc holds one directory per process, while loadavg's last field
// counts runnable and total threads, and an operator wants to tell a process count from
// a thread count.
func (h *systemHost) load(res *toolbox.Result) {
	r := h.file(pathLoadAvg)
	load, running, totalThreads, loadOK := parseLoadAvg(r.text)
	if !loadOK {
		if r.why != "" {
			res.Note("负载：%s", r.why)
		} else {
			res.Note("负载：%s 的内容无法解析（%q）", pathLoadAvg, firstLine(r.text))
		}
	} else {
		res.Add(labelLoad, load)
	}

	processes, why := h.processCount()
	switch {
	case processes > 0 && totalThreads != "":
		res.Add(labelProcesses, fmt.Sprintf("%d 个进程 · %s 个线程（运行中 %s）", processes, totalThreads, running))
		res.Note("进程数按 %s 下的数字目录计数（含内核线程），线程数来自 %s 的第四列", pathProcDir, pathLoadAvg)
	case processes > 0:
		res.Add(labelProcesses, fmt.Sprintf("%d 个进程", processes))
		res.Note("进程数按 %s 下的数字目录计数（含内核线程）", pathProcDir)
	case totalThreads != "":
		res.Add(labelProcesses, fmt.Sprintf("线程 %s（运行中 %s）", totalThreads, running))
		res.Note("无法计数进程：%s", why)
	default:
		res.Note("进程与线程：%s；%s", why, r.why)
	}
}

// processCount counts the numeric entries under /proc, which is one directory per
// process, kernel threads included.
func (h *systemHost) processCount() (int, string) {
	names, err := h.env.FS.ReadDir(pathProcDir)
	if err != nil {
		return 0, fmt.Sprintf("无法列出 %s（%v）", pathProcDir, err)
	}
	n := 0
	for _, name := range names {
		if _, err := strconv.Atoi(name); err == nil {
			n++
		}
	}
	return n, ""
}

// clock fills the timezone and NTP rows. timedatectl is the authority when it is
// installed — it knows the zone the runtime actually uses and whether NTP has completed
// a sync — and the /etc files are the fallback for a host without systemd.
func (h *systemHost) clock(ctx context.Context, res *toolbox.Result) {
	zone, zoneSource := "", ""
	if r := h.file(pathTimezone); r.text != "" {
		zone, zoneSource = r.text, pathTimezone
	} else if target, err := h.env.FS.Readlink(pathLocaltime); err == nil {
		if i := strings.Index(target, "zoneinfo/"); i >= 0 {
			zone, zoneSource = target[i+len("zoneinfo/"):], pathLocaltime+" 的链接"
		}
	}

	ntp := ""
	if _, err := h.env.Cmd.LookPath(toolTimeDateCt); err != nil {
		res.Note("时间同步：未安装 %s（非 systemd 系统或精简镜像），无法确认 NTP 状态", toolTimeDateCt)
	} else {
		out, runErr := runCommand(ctx, h.env.Cmd, clockTimeout, toolTimeDateCt, "show",
			"--property=Timezone", "--property=NTP", "--property=NTPSynchronized")
		props := parseKeyValue(out)
		if tz := props["Timezone"]; tz != "" {
			zone, zoneSource = tz, toolTimeDateCt
		}
		switch {
		case len(props) == 0:
			res.Note("时间同步：%s 没有可解析的输出（%s）", toolTimeDateCt, failureText(out, runErr))
		case props["NTP"] == "no":
			ntp = "未开启 NTP 服务"
		case props["NTPSynchronized"] == "yes":
			ntp = "已同步（NTP 已开启）"
		case props["NTPSynchronized"] == "no":
			ntp = "未同步（NTP 已开启，尚未完成一次同步）"
		default:
			res.Note("时间同步：%s 没有报告 NTP 状态", toolTimeDateCt)
		}
	}

	if zone == "" {
		res.Note("时区：%s 与 %s 都没有给出时区", pathTimezone, pathLocaltime)
	} else {
		res.Add(labelTimezone, zone)
		res.Note("时区来自 %s", zoneSource)
	}
	if ntp != "" {
		res.Add(labelNTP, ntp)
	}
}

// egress fills the public IP and its registration from ip-api.com. This is the one
// reading that leaves the host, so it is bounded on its own and a failure is a note
// rather than a row: a lookup that did not happen must not look like a lookup that
// returned nothing.
func (h *systemHost) egress(ctx context.Context, res *toolbox.Result) {
	timeout := ipLookupTimeout
	if d := h.opts.Duration(); d > 0 && d < timeout {
		timeout = d
	}
	lctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(lctx, http.MethodGet, ipAPIURL, nil)
	if err != nil {
		res.Note("出口 IP：无法构造 ip-api.com 请求（%v）", err)
		return
	}
	req.Header.Set("User-Agent", "EasySB-toolbox")
	resp, err := h.opts.HTTP().Do(req)
	if err != nil {
		if lctx.Err() != nil {
			res.Note("出口 IP 与归属：查询 ip-api.com 超时（%s）", timeout)
			return
		}
		res.Note("出口 IP 与归属：查询 ip-api.com 失败（%v）", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		res.Note("出口 IP 与归属：ip-api.com 返回 HTTP %d", resp.StatusCode)
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		res.Note("出口 IP 与归属：读取 ip-api.com 的响应失败（%v）", err)
		return
	}
	var payload struct {
		Status  string `json:"status"`
		Info    string `json:"message"`
		Query   string `json:"query"`
		Country string `json:"country"`
		Region  string `json:"regionName"`
		City    string `json:"city"`
		ISP     string `json:"isp"`
		Org     string `json:"org"`
		AS      string `json:"as"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		res.Note("出口 IP 与归属：ip-api.com 的响应无法解析（%v）", err)
		return
	}
	if payload.Status != "success" {
		res.Note("出口 IP 与归属：ip-api.com 拒绝了这次查询（status=%s %s）", payload.Status, payload.Info)
		return
	}

	if payload.Query != "" {
		res.Add(labelEgressIP, payload.Query)
	}
	if where := strings.Join(nonEmpty(payload.Country, payload.Region, payload.City), " · "); where != "" {
		res.Add(labelIPOwner, where)
	}
	operator := payload.ISP
	if payload.Org != "" && payload.Org != payload.ISP {
		if operator == "" {
			operator = payload.Org
		} else {
			operator += " · " + payload.Org
		}
	}
	asn := payload.AS
	if operator != "" {
		if asn == "" {
			asn = operator
		} else {
			asn += "（ISP " + operator + "）"
		}
	}
	if asn != "" {
		res.Add(labelASN, asn)
	}
	res.Note("出口 IP 与归属来自 ip-api.com 的免费接口（只有 http，https 需付费）；除这次查询外，面板不发送任何数据")
}

// virtualization fills the virtualization row. systemd-detect-virt is the authority
// when it is installed — it also recognizes containers, which DMI cannot — and without
// it the answer is assembled from the DMI identity and the cpuinfo hypervisor flag.
// When neither is conclusive the row is left out and a note says so: "bare metal" and
// "we could not tell" are different facts about a host.
func (h *systemHost) virtualization(ctx context.Context, res *toolbox.Result, cpu cpuInfo) {
	dmi := h.dmi()
	verdict := h.detectVirt(ctx, res, dmi)
	if verdict == "" {
		verdict = h.dmiVirt(res, cpu, dmi)
	}
	if verdict == "" {
		return
	}
	h.virt = verdict
	res.Add(labelVirt, verdict)
}

// detectVirt asks systemd-detect-virt, returning "" when the tool is not installed or
// gave no answer; the note for each of those cases is written here.
func (h *systemHost) detectVirt(ctx context.Context, res *toolbox.Result, dmi string) string {
	if _, err := h.env.Cmd.LookPath(toolVirt); err != nil {
		res.Note("虚拟化：未安装 %s，改由 /sys/class/dmi/id 的厂商与 %s 的 hypervisor 标志判断（DMI 认不出容器）",
			toolVirt, pathCPUInfo)
		return ""
	}
	out, runErr := runCommand(ctx, h.env.Cmd, virtTimeout, toolVirt)
	answer := ""
	if runErr == nil {
		// A tool that exited non-zero did not answer, whatever it printed on the way
		// out: "detect-virt: failed" is not a platform.
		answer = firstLine(out)
	}
	switch {
	case answer == "none":
		return virtText("物理机（systemd-detect-virt 报 none）", dmi)
	case answer != "":
		verdict := "虚拟化平台 " + answer + "（systemd-detect-virt）"
		if virtContainers[answer] {
			verdict = "容器（systemd-detect-virt 报 " + answer + "）"
		}
		return virtText(verdict, dmi)
	default:
		res.Note("虚拟化：%s 没有给出类型（%s），改用 DMI 与 %s 的 hypervisor 标志判断",
			toolVirt, failureText(out, runErr), pathCPUInfo)
		return ""
	}
}

// dmiVirt is the fallback verdict: the hypervisor CPU flag first, then the DMI vendor
// against the two vendor lists. It returns "" when neither is conclusive.
func (h *systemHost) dmiVirt(res *toolbox.Result, cpu cpuInfo, dmi string) string {
	vendor := h.dmiVendor()
	switch {
	case cpu.hypervisor:
		return virtText("虚拟机（cpuinfo 报告 hypervisor 标志）", dmi)
	case isHypervisorVendor(vendor):
		return virtText("虚拟机（DMI 厂商是虚拟化厂商）", dmi)
	case isHardwareVendor(vendor):
		return virtText("物理机（DMI 厂商是硬件厂商，且没有 hypervisor 标志）", dmi)
	case dmi != "":
		res.Note("虚拟化：未能确认。DMI 报告 %s，%s 没有 hypervisor 标志，厂商也不在已知的虚拟化厂商之列", dmi, pathCPUInfo)
	default:
		res.Note("虚拟化：未能确认。%s 与 %s 都没有给出线索", pathDMIVendor, pathCPUInfo)
	}
	return ""
}

// dmiVendor is the board vendor alone, which is what the vendor lists match against.
func (h *systemHost) dmiVendor() string { return h.file(pathDMIVendor).text }

// dmi is the vendor and product, the identity printed next to a virtualization verdict.
func (h *systemHost) dmi() string {
	return strings.Join(nonEmpty(h.dmiVendor(), h.file(pathDMIProduct).text), " ")
}

// virtText joins a verdict with the hardware identity behind it.
func virtText(verdict, dmi string) string {
	if dmi == "" {
		return verdict
	}
	return verdict + "；DMI " + dmi
}

// virtContainers are the systemd-detect-virt answers that name a container rather than
// a hypervisor: the distinction matters because a container shares the host's kernel
// and its readings do not describe the machine the operator is sizing.
var virtContainers = map[string]bool{
	"docker": true, "lxc": true, "lxc-libvirt": true, "openvz": true, "podman": true,
	"rkt": true, "wsl": true, "proot": true, "systemd-nspawn": true, "private-users": true,
}

// hypervisorVendors are the DMI system vendors that mean the host is a guest.
var hypervisorVendors = []string{
	"qemu", "kvm", "vmware", "virtualbox", "innotek", "xen", "microsoft corporation",
	"amazon", "google", "digitalocean", "oracle", "parallels", "bochs", "openstack",
	"nutanix", "alibaba", "tencent", "kubevirt", "proxmox", "red hat",
}

// hardwareVendors are the DMI system vendors that mean a physical machine. The list is
// evidence, not proof: without systemd-detect-virt a bare-metal answer is as far as two
// independent signals reach, and the row names the signals it used.
var hardwareVendors = []string{
	"dell", "hewlett", "hp", "hpe", "supermicro", "asus", "asustek", "gigabyte", "msi",
	"lenovo", "inspur", "huawei", "tyan", "intel", "amd", "aaeon", "asrock", "acer",
	"fujitsu", "inventec", "quanta", "wiwynn", "h3c", "micro-star", "cisco", "datto",
	"kontron", "advantech", "ibm",
}

func isHypervisorVendor(vendor string) bool { return vendorIn(vendor, hypervisorVendors) }

func isHardwareVendor(vendor string) bool { return vendorIn(vendor, hardwareVendors) }

func vendorIn(vendor string, list []string) bool {
	vendor = strings.ToLower(strings.TrimSpace(vendor))
	if vendor == "" {
		return false
	}
	for _, want := range list {
		if strings.Contains(vendor, want) {
			return true
		}
	}
	return false
}

// cpuInfo is /proc/cpuinfo reduced to what the table shows. The counts come from the
// file rather than runtime.NumCPU() because a container can be limited to fewer CPUs
// than the host has, and the operator asked about the host.
type cpuInfo struct {
	model      string
	logical    int
	physical   int // 0 when the file pairs neither physical id with core id nor cpu cores
	sockets    int
	mhzMin     float64
	mhzMax     float64
	mhzAvg     float64
	mhzCount   int
	hypervisor bool
}

// parseCPUInfo reads one record per processor. A record ends at a blank line or at the
// next "processor" key, because some ARM kernels separate blocks with no blank line at
// all.
func parseCPUInfo(text string) cpuInfo {
	var (
		info    cpuInfo
		mhz     []float64
		cores   = map[[2]string]struct{}{}
		socket  = map[string]struct{}{}
		perCPU  int
		record  = map[string]string{}
		lastKey string
		flush   func()
		hasPerS bool
	)
	flush = func() {
		if len(record) == 0 {
			return
		}
		info.logical++
		if info.model == "" {
			info.model = firstField(record, "model name", "Processor", "Hardware", "cpu model", "cpu")
		}
		flags := record["flags"]
		if flags == "" {
			flags = record["Features"]
		}
		if hasToken(flags, "hypervisor") {
			info.hypervisor = true
		}
		physical, core := record["physical id"], record["core id"]
		if physical != "" && core != "" {
			cores[[2]string{physical, core}] = struct{}{}
		}
		if physical != "" {
			socket[physical] = struct{}{}
		}
		if n, err := strconv.Atoi(strings.TrimSpace(record["cpu cores"])); err == nil && n > 0 && !hasPerS {
			perCPU, hasPerS = n, true
		}
		if v := strings.TrimSpace(record["cpu MHz"]); v != "" {
			if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
				mhz = append(mhz, f)
			}
		}
		record = map[string]string{}
		lastKey = ""
	}
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			flush()
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			// The kernel wraps long values — x86 "flags" and ARM "Features" — onto
			// continuation lines that carry no key at all. They still belong to the
			// record, and the hypervisor flag is often on the second line.
			if lastKey != "" {
				record[lastKey] = strings.TrimSpace(record[lastKey] + " " + trimmed)
			}
			continue
		}
		key = strings.TrimSpace(key)
		if key == "processor" {
			flush()
		}
		record[key] = strings.TrimSpace(value)
		lastKey = key
	}
	flush()

	info.physical = len(cores)
	info.sockets = len(socket)
	if info.physical == 0 && hasPerS {
		// The older x86 spelling: "cpu cores" per socket, without core ids.
		info.physical = perCPU * max(len(socket), 1)
	}
	if info.mhzCount = len(mhz); info.mhzCount > 0 {
		info.mhzMin, info.mhzMax = mhz[0], mhz[0]
		sum := 0.0
		for _, v := range mhz {
			sum += v
			if v < info.mhzMin {
				info.mhzMin = v
			}
			if v > info.mhzMax {
				info.mhzMax = v
			}
		}
		info.mhzAvg = sum / float64(len(mhz))
	}
	return info
}

// khzToMHz converts the cpufreq files' kHz readings to whole MHz.
func khzToMHz(text string) (int, error) {
	n, err := strconv.ParseUint(strings.TrimSpace(text), 10, 64)
	if err != nil {
		return 0, err
	}
	return int(n / 1000), nil
}

// memSummary is /proc/meminfo reduced to the four numbers the table shows.
type memSummary struct {
	total     uint64
	available uint64
	swapTotal uint64
	swapFree  uint64
	// memAvailableMissing records that the available count fell back to MemFree, so
	// the table can say which field it used.
	memAvailableMissing bool
}

// parseMemInfo reads the KiB fields. MemAvailable is preferred and MemFree is the
// fallback for kernels older than 3.14; the two fields are collected separately because
// some kernels list MemFree first, and a fallback flag set while the real field is still
// to come would be a lie about which one was used.
func parseMemInfo(text string) memSummary {
	var (
		out      memSummary
		memFree  uint64
		hasFree  bool
		hasAvail bool
	)
	kb := func(value string) uint64 {
		fields := strings.Fields(strings.TrimSpace(value))
		if len(fields) == 0 {
			return 0
		}
		n, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			return 0
		}
		return n * 1024
	}
	for _, line := range strings.Split(text, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "MemTotal":
			out.total = kb(value)
		case "MemAvailable":
			out.available, hasAvail = kb(value), true
		case "MemFree":
			memFree, hasFree = kb(value), true
		case "SwapTotal":
			out.swapTotal = kb(value)
		case "SwapFree":
			out.swapFree = kb(value)
		}
	}
	if !hasAvail && hasFree {
		out.available = memFree
		out.memAvailableMissing = true
	}
	return out
}

// parseUptime reads the seconds column of /proc/uptime.
func parseUptime(text string) time.Duration {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return 0
	}
	secs, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || secs <= 0 {
		return 0
	}
	return time.Duration(secs * float64(time.Second))
}

// parseLoadAvg returns the three load figures, the runnable thread count, the total
// thread count and whether anything could be read.
func parseLoadAvg(text string) (load, running, total string, ok bool) {
	fields := strings.Fields(text)
	if len(fields) < 3 {
		return "", "", "", false
	}
	loads := make([]string, 0, 3)
	for _, f := range fields[:3] {
		v, err := strconv.ParseFloat(f, 64)
		if err != nil {
			return "", "", "", false
		}
		loads = append(loads, strconv.FormatFloat(v, 'f', 2, 64))
	}
	if len(fields) >= 4 {
		if run, all, found := strings.Cut(fields[3], "/"); found {
			running, total = run, all
		}
	}
	return strings.Join(loads, " / "), running, total, true
}

// osPrettyName picks the human name out of /etc/os-release, whose values some images
// quote and others do not.
func osPrettyName(text string) string {
	props := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		props[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	for _, key := range []string{"PRETTY_NAME", "NAME"} {
		if v := strings.TrimSpace(props[key]); v != "" {
			return v
		}
	}
	return ""
}

// systemSummary is the one line the toolbox board shows for the system tool.
func systemSummary(cpu cpuInfo, mem uint64, virt string) string {
	parts := nonEmpty(cpu.model)
	if cpu.logical > 0 {
		cores := fmt.Sprintf("%d 逻辑线程", cpu.logical)
		if cpu.physical > 0 {
			cores = fmt.Sprintf("%d 核 / %d 线程", cpu.physical, cpu.logical)
		}
		parts = append(parts, cores)
	}
	if mem > 0 {
		parts = append(parts, "内存 "+humanBytes(mem))
	}
	if virt != "" {
		// The summary carries the verdict alone: the DMI identity and the name of the
		// tool behind it belong in the row, not on the board's one line.
		short := virt
		if i := strings.Index(short, "（"); i > 0 {
			short = short[:i]
		}
		parts = append(parts, short)
	}
	if len(parts) == 0 {
		return "未能读取系统信息"
	}
	// The board's line is truncated by runes, never by bytes: cutting a multibyte
	// character in half would put an invalid string on the screen.
	const maxRunes = 120
	if runes := []rune(strings.Join(parts, " · ")); len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "…"
	}
	return strings.Join(parts, " · ")
}

// boardSystemLine is the system entry's one line on the 看板: the processor, the core count and
// the memory, in that order, because that is what an operator compares between two hosts. The
// virtualization verdict stays in the report, where the tool that answered is named beside it.
func boardSystemLine(cpu cpuInfo, mem uint64) string {
	parts := nonEmpty(cpu.model)
	if cpu.physical > 0 || cpu.logical > 0 {
		switch {
		case cpu.physical > 0 && cpu.logical > 0:
			parts = append(parts, fmt.Sprintf("%d 核 / %d 线程", cpu.physical, cpu.logical))
		case cpu.logical > 0:
			parts = append(parts, fmt.Sprintf("%d 线程", cpu.logical))
		}
	}
	if mem > 0 {
		parts = append(parts, humanBytes(mem))
	}
	if len(parts) == 0 {
		return "未能读取系统信息"
	}
	return strings.Join(parts, " · ")
}
