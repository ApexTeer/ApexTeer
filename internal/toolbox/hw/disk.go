package hw

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/EasySB-Team/EasySB/internal/toolbox"
)

// Paths the disk table reads.
const (
	pathSysBlock   = "/sys/block"
	pathProcMounts = "/proc/mounts"
)

// smartctl is the only source of power-on hours. It is used only when it is installed,
// and its failure is a note rather than a reading.
const toolSmartctl = "smartctl"

// smartTimeout bounds one device's smartctl call. A device that has gone away can hang
// the command, and one dead disk must not spend the tool's whole budget.
const smartTimeout = 10 * time.Second

// The power-on cells that are not a number. They say why in the cell itself, because a
// dash alone reads like a rendering bug.
const (
	smartNeedsTool  = "需 smartctl"
	smartUnreadable = "读不到（见说明）"
	smartNotDisk    = "—（非物理盘，无 SMART）"
)

// Row labels for the disk table.
const (
	labelDevice  = "设备"
	labelSize    = "大小"
	labelKind    = "类型"
	labelModel   = "型号"
	labelMount   = "挂载"
	labelFree    = "剩余"
	labelPowerOn = "通电时长"
)

// dash is the cell for a field that does not apply to a device at all, as opposed to one
// that could not be read.
const dash = "—"

// diskHost collects one disk table.
type diskHost struct {
	env  Env
	opts toolbox.Options
}

// collect walks /sys/block, pairs every device with the mounts that belong to it and
// reports what each one is.
func (h *diskHost) collect(ctx context.Context) toolbox.Result {
	res := toolbox.Result{Headers: []string{labelDevice, labelSize, labelKind, labelModel, labelMount, labelFree, labelPowerOn}}

	names, err := h.env.FS.ReadDir(pathSysBlock)
	if err != nil {
		res.Note("块设备：无法列出 %s（%v），没有可报告的块设备", pathSysBlock, err)
		res.Summary = "未发现块设备"
		return res
	}
	devices := h.readDevices(names)
	mounts, mountsWhy := h.mounts()
	if mountsWhy != "" {
		res.Note("挂载点：%s，所有设备的挂载点与剩余空间都无法报告", mountsWhy)
	}

	smartAvailable := false
	if _, err := h.env.Cmd.LookPath(toolSmartctl); err == nil {
		smartAvailable = true
	} else {
		res.Note("通电时长：未安装 %s（Debian/Ubuntu 装 smartmontools，RHEL 装 smartmontools），无法读取通电小时与通电次数；"+
			"普通 VPS 的虚拟盘通常不向后端转发 SMART，装了 smartctl 也常常读不到", toolSmartctl)
	}

	var (
		virtual, unmounted, noModel, smartTried, smartRead int
	)
	for _, dev := range devices {
		kind, synthetic := deviceKind(dev.name, dev.rotational, dev.link)
		if synthetic {
			virtual++
		}

		size := dash
		if dev.sizeKnown {
			size = humanBytes(dev.size)
		}

		model := deviceModel(dev)
		if model == "" {
			model = dash
			noModel++
		}

		devMounts := mountsFor(dev, mounts)
		mountCell := dash
		if len(devMounts) > 0 {
			points := make([]string, 0, len(devMounts))
			for _, m := range devMounts {
				points = append(points, m.point)
			}
			mountCell = strings.Join(points, ", ")
		} else if len(mounts) > 0 {
			mountCell = "未挂载"
			unmounted++
		}

		freeCell, freeNote := h.freeCell(devMounts)
		if freeNote != "" {
			res.Note("%s", freeNote)
		}

		powerCell := smartNotDisk
		switch {
		case !smartable(dev.name):
			// A loop device is a file and device-mapper is a mapping or a logical
			// volume: neither owns the media SMART describes.
		case !smartAvailable:
			powerCell = smartNeedsTool
		default:
			smartTried++
			var note string
			powerCell, note = h.powerOn(ctx, dev.name)
			if note != "" {
				res.Note("%s", note)
			} else {
				smartRead++
			}
		}

		res.Rows = append(res.Rows, []string{dev.name, size, kind, model, mountCell, freeCell, powerCell})
	}

	if len(devices) == 0 {
		res.Note("块设备：%s 下没有任何条目；容器里看不到宿主机磁盘是正常的（块设备不在容器的命名空间内）", pathSysBlock)
	}
	h.overlay(&res, mounts)
	if unmounted > 0 {
		res.Note("%d 个块设备在本命名空间没有挂载点（未挂载，或容器里看不到宿主机的挂载表）", unmounted)
	}
	if noModel > 0 {
		res.Note("%d 个设备没有报告型号与厂商（虚拟盘、回环设备与部分 USB 桥接不提供 %s/device/model）", noModel, pathSysBlock)
	}
	if smartTried > 0 {
		res.Note("通电时长来自 %s -j -a（用户层读数可能被后端屏蔽；未被屏蔽的盘才给小时数）", toolSmartctl)
	}

	res.Note("数据来源：%s/*（容量、rotational、型号、厂商）、%s（挂载点）、statfs(挂载点)（剩余空间，等于 df 的 size/avail）。",
		pathSysBlock, pathProcMounts)
	res.Summary = diskSummary(len(devices), virtual, smartRead, smartTried, h)
	res.Board = boardDiskLine(len(devices), h)
	return res
}

// blockDevice is one /sys/block entry as read from the host.
type blockDevice struct {
	name string
	// size is in bytes, converted from the 512-byte sectors sysfs reports.
	size      uint64
	sizeKnown bool
	// rotational is the host's claim about the media, nil when the file is missing.
	rotational *bool
	model      string
	vendor     string
	// link is the target of the /sys/block link, which names the driver behind the
	// device (virtio-pci, usb, ata...).
	link string
}

// readDevices reads the per-device attributes out of /sys/block. A missing attribute is
// left zero so the row can say so instead of printing a number nobody read.
func (h *diskHost) readDevices(names []string) []blockDevice {
	out := make([]blockDevice, 0, len(names))
	for _, name := range names {
		if name == "" || strings.HasPrefix(name, ".") {
			continue
		}
		base := pathSysBlock + "/" + name
		dev := blockDevice{name: name}
		if sectors, err := strconv.ParseUint(h.attribute(base+"/size"), 10, 64); err == nil {
			// /sys/block/<dev>/size is in 512-byte sectors whatever the device's own
			// sector size is.
			dev.size, dev.sizeKnown = sectors*512, true
		}
		switch h.attribute(base + "/queue/rotational") {
		case "0", "1":
			rotational := h.attribute(base+"/queue/rotational") == "1"
			dev.rotational = &rotational
		}
		dev.model = h.attribute(base + "/device/model")
		dev.vendor = h.attribute(base + "/device/vendor")
		if link, err := h.env.FS.Readlink(base); err == nil {
			dev.link = link
		}
		out = append(out, dev)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// attribute reads one short sysfs attribute, reporting an empty string for anything
// that is missing, empty or unreadable — the callers here only care whether a value is
// there.
func (h *diskHost) attribute(path string) string {
	data, err := h.env.FS.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// mountPoint is one line of /proc/mounts.
type mountPoint struct {
	device string
	point  string
	fsType string
}

// mounts reads /proc/mounts. The mount table is the only way to know which filesystem a
// device holds, and in a container it shows the container's own view, which is exactly
// what the free space below it will describe.
func (h *diskHost) mounts() ([]mountPoint, string) {
	r := readFile(h.env.FS, pathProcMounts)
	if r.text == "" {
		return nil, r.why
	}
	var out []mountPoint
	for _, line := range strings.Split(r.text, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		out = append(out, mountPoint{
			device: unescapeMount(fields[0]),
			point:  unescapeMount(fields[1]),
			fsType: fields[2],
		})
	}
	if len(out) == 0 {
		return nil, pathProcMounts + " 里没有可解析的挂载项"
	}
	return out, ""
}

// mountsFor returns the mounts of one block device, again in mount table order. A
// partition belongs to its disk, so /dev/sda1 and /dev/sda2 both answer for sda; the
// exact name is tried first because parentDisk("loop0") would name no device at all.
func mountsFor(dev blockDevice, all []mountPoint) []mountPoint {
	var out []mountPoint
	for _, m := range all {
		if !strings.HasPrefix(m.device, "/dev/") {
			continue
		}
		base := m.device[strings.LastIndex(m.device, "/")+1:]
		if base == dev.name || parentDisk(base) == dev.name {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].point < out[j].point })
	return out
}

// parentDisk maps a partition to the disk it lives on: sda1 -> sda, vda12 -> vda,
// nvme0n1p2 -> nvme0n1, mmcblk0p1 -> mmcblk0. A whole device name is returned
// unchanged, because the two spellings of a partition are different: sd/vd/loop append
// digits to a stem of letters, while nvme and mmcblk put a p in front of the partition
// number. The caller tries the exact device name first, so a name that is itself a
// device (loop0, mmcblk0) never depends on this mapping.
func parentDisk(name string) string {
	if i := strings.LastIndex(name, "p"); i > 0 && i+1 < len(name) &&
		allDigits(name[i-1:i]) && allDigits(name[i+1:]) {
		return name[:i]
	}
	stem := strings.TrimRight(name, "0123456789")
	if stem == name {
		return name
	}
	for _, r := range stem {
		if r < 'a' || r > 'z' {
			// nvme0n1 and mmcblk0 keep a digit in their own name, so their trailing
			// number is part of the disk, not a partition.
			return name
		}
	}
	return stem
}

// allDigits reports whether s is a non-empty run of digits.
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// unescapeMount undoes the octal escapes /proc/mounts uses for spaces and tabs in a
// path, so a mount point with a space is displayed as one.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(s)
}

// freeCell renders the space the device's filesystems have left, summed over its mount
// points. Summing is what an operator wants for a disk with several partitions; the
// note says so, because the figure is then not one df line.
func (h *diskHost) freeCell(mounts []mountPoint) (cell, note string) {
	if len(mounts) == 0 {
		return dash, ""
	}
	var total, available uint64
	var failed []string
	var firstErr error
	for _, m := range mounts {
		t, a, err := h.env.FS.Statfs(m.point)
		if err != nil {
			failed = append(failed, m.point)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		total += t
		available += a
	}
	if total == 0 {
		return dash, fmt.Sprintf("剩余空间：%s 上的 statfs 失败（%v），无法报告剩余空间",
			strings.Join(failed, "、"), firstErr)
	}
	if len(failed) > 0 {
		note = fmt.Sprintf("剩余空间：%s 上的 statfs 失败，该挂载点未计入合计", strings.Join(failed, "、"))
	}
	return fmt.Sprintf("%s / %s（已用 %d%%）", humanBytes(available), humanBytes(total), percentUsed(total, available)), note
}

// overlay appends a row for an overlay root. A container's / is an overlay mount rather
// than a block device, and it is the filesystem whose free space actually matters there,
// so it is listed with its nature spelled out.
func (h *diskHost) overlay(res *toolbox.Result, mounts []mountPoint) {
	for _, m := range mounts {
		if m.fsType != "overlay" {
			continue
		}
		free := dash
		if total, available, err := h.env.FS.Statfs(m.point); err == nil && total > 0 {
			free = fmt.Sprintf("%s / %s（已用 %d%%）", humanBytes(available), humanBytes(total), percentUsed(total, available))
		}
		res.Rows = append(res.Rows, []string{
			"overlay", dash, "联合挂载（容器根文件系统，不是块设备）", dash, m.point, free, "—（非块设备，无 SMART）",
		})
		res.Note("overlay：%s 是 overlayfs 联合挂载，由镜像层叠加而成，不是块设备：没有 rotational、没有型号、没有 SMART，"+
			"它的容量来自宿主机的盘，而那块盘在容器里看不到", m.point)
		return
	}
}

// deviceKind names what a block device is and whether it is synthetic. Two facts matter
// to an operator: whether the device backs a physical disk — which is what makes
// rotational mean SSD or HDD — and which layer put it there. A virtio disk reports
// rotational=1 by default even when the host's storage is flash, so the rotational cell
// says what it is: the host's own report.
func deviceKind(name string, rotational *bool, link string) (kind string, synthetic bool) {
	switch {
	case strings.HasPrefix(name, "loop"):
		return "回环设备（loop：镜像或快照文件，不是物理盘，无 SMART）", true
	case strings.HasPrefix(name, "dm-"):
		return "设备映射（dm：LVM 或加密层，物理属性取决于底层设备）", true
	case strings.HasPrefix(name, "zram"):
		return "内存块设备（zram：压缩内存，不是存储盘）", true
	case strings.HasPrefix(name, "md"):
		return "软 RAID（md：由多个成员盘组成，物理属性取决于成员盘）", true
	case strings.HasPrefix(name, "vd"):
		return withBus("虚拟盘（virtio-blk：宿主机分配，rotational 是宿主机的报告）", link), true
	case strings.HasPrefix(name, "xvd"):
		return withBus("虚拟盘（Xen blkfront：宿主机分配，rotational 是宿主机的报告）", link), true
	case strings.HasPrefix(name, "nvme"):
		return withBus("固态盘（NVMe）", link), false
	case strings.HasPrefix(name, "mmcblk"):
		return "闪存（eMMC/SD 卡）", false
	}
	switch {
	case rotational == nil:
		return withBus("未报告 rotational，无法判断 SSD/HDD", link), false
	case *rotational:
		return withBus("机械盘（rotational=1）", link), false
	default:
		return withBus("固态盘（rotational=0）", link), false
	}
}

// withBus adds the controller a device sits behind when the /sys link names one that
// changes how the readings should be read: a virtio or USB disk's rotational flag says
// more about the controller than about the media behind it.
func withBus(kind, link string) string {
	switch {
	case strings.Contains(link, "virtio") && !strings.Contains(kind, "virtio"):
		return kind + "，总线 virtio"
	case strings.Contains(link, "/usb") && !strings.Contains(kind, "USB"):
		return kind + "，总线 USB"
	}
	return kind
}

// smartable reports whether a device can have SMART data at all.
func smartable(name string) bool {
	for _, prefix := range []string{"loop", "dm-", "zram"} {
		if strings.HasPrefix(name, prefix) {
			return false
		}
	}
	return true
}

// deviceModel joins the vendor and the model the device reports.
func deviceModel(dev blockDevice) string {
	return strings.Join(nonEmpty(dev.vendor, dev.model), " ")
}

// smartctlJSON is the subset of `smartctl -j -a` the table shows. The pointers tell a
// missing field from a zero one, because 0 hours on a new disk is a reading and no hours
// at all is not.
type smartctlJSON struct {
	PowerOnTime *struct {
		Hours   *int `json:"hours"`
		Minutes *int `json:"minutes"`
	} `json:"power_on_time"`
	PowerCycleCount *int `json:"power_cycle_count"`
	Messages        []struct {
		String string `json:"string"`
	} `json:"messages"`
}

// powerOn asks smartctl for one device's power-on hours and cycle count. The caller has
// already established that the binary is installed and that the device can have SMART
// data. A device that answers with an error becomes a note naming it — on a VPS that is
// the normal outcome, because the hypervisor rarely forwards SMART to a virtual disk.
func (h *diskHost) powerOn(ctx context.Context, name string) (cell, note string) {
	out, err := runCommand(ctx, h.env.Cmd, smartTimeout, toolSmartctl, "-j", "-a", "/dev/"+name)
	var payload smartctlJSON
	if jsonErr := json.Unmarshal([]byte(out), &payload); jsonErr != nil {
		return smartUnreadable, fmt.Sprintf("smartctl 对 /dev/%s 的输出不是 JSON（需要 7.0 以上并支持 -j）：%s",
			name, failureText(out, err))
	}
	reason := ""
	if len(payload.Messages) > 0 {
		reason = payload.Messages[0].String
	}
	if err != nil || payload.PowerOnTime == nil {
		if reason == "" {
			reason = failureText(out, err)
		}
		return smartUnreadable, fmt.Sprintf("smartctl 读不到 /dev/%s 的通电时长：%s", name, reason)
	}

	hours, minutes := payload.PowerOnTime.Hours, payload.PowerOnTime.Minutes
	switch {
	case hours == nil && minutes == nil:
		return smartUnreadable, fmt.Sprintf("smartctl 对 /dev/%s 的响应里没有 power_on_time 字段", name)
	case hours != nil && *hours > 0:
		cell = fmt.Sprintf("%d 小时", *hours)
	default:
		cell = fmt.Sprintf("%d 分", deref(minutes))
	}
	if payload.PowerCycleCount == nil {
		cell += " · 未报告通电次数"
	} else {
		cell += fmt.Sprintf(" · %d 次通电", *payload.PowerCycleCount)
	}
	return cell, ""
}

func deref(n *int) int {
	if n == nil {
		return 0
	}
	return *n
}

// diskSummary is the one line the toolbox board shows for the disk tool.
func diskSummary(devices, virtual, smartRead, smartTried int, h *diskHost) string {
	if devices == 0 {
		return "未发现块设备"
	}
	parts := []string{fmt.Sprintf("%d 个块设备", devices)}
	if virtual > 0 {
		parts = append(parts, fmt.Sprintf("含 %d 个虚拟或回环设备", virtual))
	}
	if total, available, err := h.env.FS.Statfs("/"); err == nil && total > 0 {
		parts = append(parts, fmt.Sprintf("根挂载剩余 %s / %s（已用 %d%%）",
			humanBytes(available), humanBytes(total), percentUsed(total, available)))
	}
	switch {
	case smartTried == 0:
		parts = append(parts, "通电时长 "+smartNeedsTool)
	case smartRead == 0:
		parts = append(parts, fmt.Sprintf("通电时长读不到（0/%d）", smartTried))
	default:
		parts = append(parts, fmt.Sprintf("通电时长可读 %d/%d", smartRead, smartTried))
	}
	return strings.Join(parts, " · ")
}

// boardDiskLine is the disk entry's one line on the 看板: how many devices the host has and how
// full the root mount is, which is the pair an operator acts on. The SMART reading and the
// virtual-device count are rows in the report.
func boardDiskLine(devices int, h *diskHost) string {
	if devices == 0 {
		return "未发现块设备"
	}
	parts := []string{fmt.Sprintf("%d 个块设备", devices)}
	if total, available, err := h.env.FS.Statfs("/"); err == nil && total > 0 {
		parts = append(parts, fmt.Sprintf("根挂载已用 %d%%", percentUsed(total, available)))
	}
	return strings.Join(parts, " · ")
}
