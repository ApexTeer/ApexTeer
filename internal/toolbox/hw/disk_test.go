package hw

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/EasySB-Team/EasySB/internal/toolbox"
)

// The disk fixtures. Sizes are what /sys/block reports in 512-byte sectors: 83886080
// sectors is 40 GiB, 500118192 is the 238 GiB NVMe, 20971520 is a 10 GiB virtio disk
// and 114688 is a 56 MiB loop device.
const (
	oneGiB = 1 << 30
)

// fullDiskFS is a host with a SATA disk in two partitions, an NVMe with one, a virtio
// disk and a loop device.
func fullDiskFS() *fakeFS {
	return newFakeFS().
		withDir(pathSysBlock, "sda", "nvme0n1", "vda", "loop0").
		withFile(pathSysBlock+"/sda/size", "83886080\n").
		withFile(pathSysBlock+"/sda/queue/rotational", "1\n").
		withFile(pathSysBlock+"/sda/device/model", "ST1000DM010-2EP102\n").
		withFile(pathSysBlock+"/sda/device/vendor", "ATA     \n").
		withLink(pathSysBlock+"/sda", "../../devices/pci0000:00/0000:00:1f.2/ata1/host0/target0:0:0/0:0:0:0/block/sda").
		withFile(pathSysBlock+"/nvme0n1/size", "500118192\n").
		withFile(pathSysBlock+"/nvme0n1/queue/rotational", "0\n").
		withFile(pathSysBlock+"/nvme0n1/device/model", "Samsung SSD 980 PRO 500GB\n").
		withLink(pathSysBlock+"/nvme0n1", "../../devices/pci0000:00/0000:00:1d.0/nvme/nvme0/nvme0n1").
		withFile(pathSysBlock+"/vda/size", "20971520\n").
		withFile(pathSysBlock+"/vda/queue/rotational", "1\n").
		withLink(pathSysBlock+"/vda", "../../devices/pci0000:00/0000:00:05.0/virtio1/block/vda").
		withFile(pathSysBlock+"/loop0/size", "114688\n").
		withLink(pathSysBlock+"/loop0", "../../devices/virtual/block/loop0").
		withFile(pathProcMounts, "/dev/sda1 / ext4 rw,relatime 0 0\n"+
			"/dev/sda2 /srv/data ext4 rw,relatime 0 0\n"+
			"/dev/nvme0n1p1 /var/lib/docker xfs rw,relatime 0 0\n"+
			"/dev/loop0 /snap/core20/1974 squashfs ro,nodev 0 0\n"+
			"tmpfs /run tmpfs rw,nosuid,nodev 0 0\n"+
			"proc /proc proc rw,nosuid 0 0\n").
		withStat("/", 20*oneGiB, 12*oneGiB).
		withStat("/srv/data", 20*oneGiB, 10*oneGiB).
		withStat("/var/lib/docker", 250*oneGiB, 200*oneGiB).
		withStat("/snap/core20/1974", 56*(1<<20), 0)
}

// fullDiskCmd is a host with smartctl installed: the SATA disk and the NVMe answer, the
// virtio disk refuses, and the loop device is never asked.
func fullDiskCmd(t *testing.T) *fakeCmd {
	t.Helper()
	return newFakeCmd().installed(toolSmartctl).
		answer(toolSmartctl, []string{"-j", "-a", "/dev/sda"},
			`{"power_on_time":{"hours":43210,"minutes":17},"power_cycle_count":1234}`, nil).
		answer(toolSmartctl, []string{"-j", "-a", "/dev/nvme0n1"},
			`{"power_on_time":{"hours":1500},"power_cycle_count":42}`, nil).
		answer(toolSmartctl, []string{"-j", "-a", "/dev/vda"},
			`{"messages":[{"string":"Smartctl open device: /dev/vda failed: No such device"}]}`,
			errors.New("exit status 2"))
}

func TestDisks(t *testing.T) {
	cases := []struct {
		name  string
		fs    *fakeFS
		cmd   *fakeCmd
		check func(t *testing.T, res toolbox.Result, cmd *fakeCmd)
	}{
		{
			name: "full fixture",
			fs:   fullDiskFS(),
			cmd:  newFakeCmd(),
			check: func(t *testing.T, res toolbox.Result, _ *fakeCmd) {
				// Filled in below by the smartctl case; here only the layout matters.
				if len(res.Rows) != 4 {
					t.Fatalf("got %d rows, want 4 (%v)", len(res.Rows), res.Rows)
				}
				want := map[string][]string{
					"sda":     {labelSize, "40 GiB", labelKind, "机械盘（rotational=1）", labelModel, "ATA ST1000DM010-2EP102", labelMount, "/, /srv/data", labelFree, "22 GiB / 40 GiB（已用 45%）"},
					"nvme0n1": {labelKind, "固态盘（NVMe）", labelModel, "Samsung SSD 980 PRO 500GB", labelFree, "200 GiB / 250 GiB（已用 20%）"},
					"vda":     {labelKind, "虚拟盘（virtio-blk", labelMount, "未挂载", labelFree, "—"},
					"loop0":   {labelKind, "回环设备（loop", labelMount, "/snap/core20/1974", labelFree, "0 B / 56 MiB（已用 100%）"},
				}
				for device, fields := range want {
					row, ok := findRow(res, device)
					if !ok {
						t.Fatalf("no row for %s: %v", device, res.Rows)
					}
					for i := 0; i+1 < len(fields); i += 2 {
						column := indexOf(t, res.Headers, fields[i])
						if !strings.Contains(row[column], fields[i+1]) {
							t.Errorf("%s %s = %q, want it to contain %q", device, fields[i], row[column], fields[i+1])
						}
					}
				}
				// The rotational flag is the host's report, and the virtio row says so.
				row, _ := findRow(res, "vda")
				if !strings.Contains(row[2], "rotational 是宿主机的报告") {
					t.Errorf("vda kind = %q", row[2])
				}
				if got := res.Summary; !strings.Contains(got, "4 个块设备") || !strings.Contains(got, "含 2 个虚拟或回环设备") {
					t.Errorf("summary = %q", got)
				}
				if !notesContain(res, "未安装 smartctl") {
					t.Errorf("a host without smartctl has to say so: %v", res.Notes)
				}
			},
		},
		{
			name: "smartctl reads two of three devices",
			fs:   fullDiskFS(),
			cmd:  nil, // set in the loop below, it needs t
			check: func(t *testing.T, res toolbox.Result, cmd *fakeCmd) {
				rows := map[string]string{}
				for _, row := range res.Rows {
					rows[row[0]] = row[6]
				}
				want := map[string]string{
					"sda":     "43210 小时 · 1234 次通电",
					"nvme0n1": "1500 小时 · 42 次通电",
					"vda":     "读不到（见说明）",
					"loop0":   "—（非物理盘，无 SMART）",
				}
				for device, want := range want {
					if rows[device] != want {
						t.Errorf("%s 通电时长 = %q, want %q", device, rows[device], want)
					}
				}
				if want := "smartctl 读不到 /dev/vda 的通电时长：Smartctl open device: /dev/vda failed: No such device"; !notesContain(res, want) {
					t.Errorf("no note says %q: %v", want, res.Notes)
				}
				if !strings.Contains(res.Summary, "通电时长可读 2/3") {
					t.Errorf("summary = %q", res.Summary)
				}
				// A device that has no SMART is never asked: the command list is the
				// proof, and it keeps smartctl off loop and device-mapper nodes.
				if cmd.ran("/dev/loop0") {
					t.Error("smartctl was run against a loop device")
				}
				if n := len(cmd.log); n != 3 {
					t.Errorf("smartctl ran %d times, want 3: %v", n, cmd.log)
				}
			},
		},
		{
			name: "smartctl output is not JSON",
			fs:   fullDiskFS(),
			cmd: newFakeCmd().installed(toolSmartctl).answer(toolSmartctl, []string{"-j", "-a", "/dev/sda"},
				"smartctl 6.5: Device does not support SMART\n", nil),
			check: func(t *testing.T, res toolbox.Result, _ *fakeCmd) {
				row, _ := findRow(res, "sda")
				if row[6] != "读不到（见说明）" {
					t.Errorf("sda 通电时长 = %q", row[6])
				}
				if !notesContain(res, "不是 JSON") {
					t.Errorf("an old smartctl has to be reported as such: %v", res.Notes)
				}
			},
		},
		{
			name: "no block devices",
			fs:   newFakeFS().withDir(pathSysBlock).withFile(pathProcMounts, "proc /proc proc rw 0 0\n"),
			cmd:  newFakeCmd(),
			check: func(t *testing.T, res toolbox.Result, _ *fakeCmd) {
				if len(res.Rows) != 0 {
					t.Fatalf("rows = %v, want none", res.Rows)
				}
				if res.Summary != "未发现块设备" {
					t.Errorf("summary = %q", res.Summary)
				}
				if !notesContain(res, "没有任何条目") {
					t.Errorf("an empty /sys/block needs an explanation: %v", res.Notes)
				}
			},
		},
		{
			name: "sysfs block directory unreadable",
			fs:   newFakeFS(),
			cmd:  newFakeCmd(),
			check: func(t *testing.T, res toolbox.Result, _ *fakeCmd) {
				if !notesContain(res, "无法列出 "+pathSysBlock) {
					t.Errorf("an unreadable /sys/block has to be named: %v", res.Notes)
				}
				if res.Summary != "未发现块设备" {
					t.Errorf("summary = %q", res.Summary)
				}
			},
		},
		{
			name: "container with an overlay root",
			fs: newFakeFS().
				withDir(pathSysBlock, "vda", "loop0").
				withFile(pathSysBlock+"/vda/size", "20971520\n").
				withFile(pathSysBlock+"/vda/queue/rotational", "1\n").
				withLink(pathSysBlock+"/vda", "../../devices/pci0000:00/0000:00:05.0/virtio1/block/vda").
				withFile(pathSysBlock+"/loop0/size", "114688\n").
				withLink(pathSysBlock+"/loop0", "../../devices/virtual/block/loop0").
				withFile(pathProcMounts, "overlay / overlay rw,lowerdir=/var/lib/docker/overlay2/a,upperdir=/var/lib/docker/overlay2/b 0 0\n"+
					"/dev/vda1 /boot ext4 rw,relatime 0 0\n").
				withStat("/", 250*oneGiB, 200*oneGiB).
				withStat("/boot", oneGiB, 512*(1<<20)),
			cmd: newFakeCmd(),
			check: func(t *testing.T, res toolbox.Result, _ *fakeCmd) {
				row, ok := findRow(res, "overlay")
				if !ok {
					t.Fatalf("the overlay root has to be listed: %v", res.Rows)
				}
				if !strings.Contains(row[2], "联合挂载") || !strings.Contains(row[4], "/") {
					t.Errorf("overlay row = %v", row)
				}
				if !notesContain(res, "overlayfs") || !notesContain(res, "不是块设备") {
					t.Errorf("the overlay note has to explain what it is: %v", res.Notes)
				}
				vda, _ := findRow(res, "vda")
				if !strings.Contains(vda[4], "/boot") {
					t.Errorf("the partition must map back to its disk, got %q", vda[4])
				}
				if !strings.Contains(vda[2], "virtio") {
					t.Errorf("vda kind = %q", vda[2])
				}
			},
		},
		{
			name: "rotational missing, model missing, no mount",
			fs: newFakeFS().
				withDir(pathSysBlock, "sdb").
				withFile(pathSysBlock+"/sdb/size", "1953525168\n").
				withLink(pathSysBlock+"/sdb", "../../devices/pci0000:00/0000:00:14.0/usb2/2-1/2-1:1.0/host6/target6:0:0/6:0:0:0/block/sdb").
				withFile(pathProcMounts, "proc /proc proc rw 0 0\n"+
					"/dev/sda1 / ext4 rw 0 0\n"),
			cmd: newFakeCmd(),
			check: func(t *testing.T, res toolbox.Result, _ *fakeCmd) {
				row, ok := findRow(res, "sdb")
				if !ok {
					t.Fatalf("no row for sdb: %v", res.Rows)
				}
				if !strings.Contains(row[2], "未报告 rotational") {
					t.Errorf("kind = %q", row[2])
				}
				if !strings.Contains(row[2], "总线 USB") {
					t.Errorf("the USB bus has to be named: %q", row[2])
				}
				if row[3] != dash {
					t.Errorf("model = %q, want %q", row[3], dash)
				}
				if !strings.Contains(row[4], "未挂载") || row[5] != dash {
					t.Errorf("mount/free = %q/%q", row[4], row[5])
				}
				if !notesContain(res, "没有报告型号与厂商") || !notesContain(res, "没有挂载点") {
					t.Errorf("notes = %v", res.Notes)
				}
			},
		},
		{
			name: "mount table unreadable",
			fs: newFakeFS().
				withDir(pathSysBlock, "sda").
				withFile(pathSysBlock+"/sda/size", "83886080\n").
				withFile(pathSysBlock+"/sda/queue/rotational", "1\n"),
			cmd: newFakeCmd(),
			check: func(t *testing.T, res toolbox.Result, _ *fakeCmd) {
				if !notesContain(res, "挂载点：无法读取 "+pathProcMounts) {
					t.Errorf("notes = %v", res.Notes)
				}
				row, _ := findRow(res, "sda")
				if row[4] != dash || row[5] != dash {
					t.Errorf("mount/free = %q/%q, want %q", row[4], row[5], dash)
				}
			},
		},
		{
			name: "statfs fails on the mount point",
			fs: newFakeFS().
				withDir(pathSysBlock, "sda").
				withFile(pathSysBlock+"/sda/size", "83886080\n").
				withFile(pathSysBlock+"/sda/queue/rotational", "0\n").
				withFile(pathProcMounts, "/dev/sda1 / ext4 rw 0 0\n"),
			cmd: newFakeCmd(),
			check: func(t *testing.T, res toolbox.Result, _ *fakeCmd) {
				row, _ := findRow(res, "sda")
				if row[5] != dash {
					t.Errorf("free = %q, want %q", row[5], dash)
				}
				if !notesContain(res, "剩余空间：/ 上的 statfs 失败") {
					t.Errorf("notes = %v", res.Notes)
				}
				if strings.Contains(res.Summary, "根挂载剩余") {
					t.Errorf("summary claims a root figure it does not have: %q", res.Summary)
				}
			},
		},
		{
			name: "escaped mount point and an nvme partition",
			fs: newFakeFS().
				withDir(pathSysBlock, "sda", "nvme0n1").
				withFile(pathSysBlock+"/sda/size", "83886080\n").
				withFile(pathSysBlock+"/sda/queue/rotational", "1\n").
				withFile(pathSysBlock+"/nvme0n1/size", "500118192\n").
				withFile(pathSysBlock+"/nvme0n1/queue/rotational", "0\n").
				withFile(pathProcMounts, `/dev/sda1 /mnt/disk\040one ext4 rw 0 0`+"\n"+
					"/dev/nvme0n1p2 /data xfs rw 0 0\n").
				withStat("/mnt/disk one", 40*oneGiB, 10*oneGiB).
				withStat("/data", 238*oneGiB, 100*oneGiB),
			cmd: newFakeCmd(),
			check: func(t *testing.T, res toolbox.Result, _ *fakeCmd) {
				sda, _ := findRow(res, "sda")
				if sda[4] != "/mnt/disk one" {
					t.Errorf("an escaped space has to be decoded, got %q", sda[4])
				}
				if !strings.Contains(sda[5], "已用 75%") {
					t.Errorf("sda free = %q", sda[5])
				}
				nvme, _ := findRow(res, "nvme0n1")
				if !strings.Contains(nvme[4], "/data") {
					t.Errorf("nvme0n1p2 has to map to nvme0n1, got %q", nvme[4])
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := tc.cmd
			if cmd == nil {
				cmd = fullDiskCmd(t)
			}
			res, err := DisksIn(t.Context(), testOptions(nil), Env{FS: tc.fs, Cmd: cmd})
			if err != nil {
				t.Fatalf("DisksIn: %v", err)
			}
			checkShape(t, res)
			wantHeaders := []string{labelDevice, labelSize, labelKind, labelModel, labelMount, labelFree, labelPowerOn}
			if strings.Join(res.Headers, ",") != strings.Join(wantHeaders, ",") {
				t.Fatalf("headers = %v, want %v", res.Headers, wantHeaders)
			}
			tc.check(t, res, cmd)
		})
	}
}

func indexOf(t *testing.T, list []string, want string) int {
	t.Helper()
	for i, v := range list {
		if v == want {
			return i
		}
	}
	t.Fatalf("%q is not a column of %v", want, list)
	return -1
}

func TestDeviceKind(t *testing.T) {
	rotational := true
	flash := false
	cases := []struct {
		name       string
		device     string
		rotational *bool
		link       string
		want       string
		synthetic  bool
	}{
		{name: "sata hdd", device: "sda", rotational: &rotational, want: "机械盘（rotational=1）"},
		{name: "sata ssd", device: "sdb", rotational: &flash, want: "固态盘（rotational=0）"},
		{name: "virtio", device: "vda", rotational: &rotational, link: "virtio1/block/vda",
			want: "虚拟盘（virtio-blk：宿主机分配，rotational 是宿主机的报告）", synthetic: true},
		{name: "xen", device: "xvda", want: "虚拟盘（Xen blkfront：宿主机分配，rotational 是宿主机的报告）", synthetic: true},
		{name: "loop", device: "loop7", rotational: &rotational, want: "回环设备（loop：镜像或快照文件，不是物理盘，无 SMART）", synthetic: true},
		{name: "device mapper", device: "dm-0", want: "设备映射（dm：LVM 或加密层，物理属性取决于底层设备）", synthetic: true},
		{name: "zram", device: "zram0", want: "内存块设备（zram：压缩内存，不是存储盘）", synthetic: true},
		{name: "mdraid", device: "md0", want: "软 RAID（md：由多个成员盘组成，物理属性取决于成员盘）", synthetic: true},
		{name: "nvme", device: "nvme0n1", rotational: &flash, want: "固态盘（NVMe）"},
		{name: "mmc", device: "mmcblk0", want: "闪存（eMMC/SD 卡）"},
		{name: "usb ssd", device: "sdc", rotational: &flash, link: "pci0000:00/usb2/2-1/block/sdc",
			want: "固态盘（rotational=0），总线 USB"},
		{name: "no flag", device: "sdd", want: "未报告 rotational，无法判断 SSD/HDD"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, synthetic := deviceKind(tc.device, tc.rotational, tc.link)
			if kind != tc.want {
				t.Errorf("kind = %q, want %q", kind, tc.want)
			}
			if synthetic != tc.synthetic {
				t.Errorf("synthetic = %v, want %v", synthetic, tc.synthetic)
			}
		})
	}
	if smartable("loop0") || smartable("dm-0") || smartable("zram0") {
		t.Error("a loop, dm or zram device must never be asked for SMART")
	}
	if !smartable("sda") || !smartable("nvme0n1") || !smartable("vda") {
		t.Error("a real or virtual disk is asked, so its refusal can be reported")
	}
}

func TestDiskPaths(t *testing.T) {
	cases := []struct{ in, want string }{
		{"sda1", "sda"},
		{"vda12", "vda"},
		{"nvme0n1p2", "nvme0n1"},
		{"nvme0n1", "nvme0n1"},
		{"mmcblk0p1", "mmcblk0"},
		{"loop0", "loop"},
		{"mapper-vg", "mapper-vg"},
		{"sda", "sda"},
	}
	for _, tc := range cases {
		if got := parentDisk(tc.in); got != tc.want {
			t.Errorf("parentDisk(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := unescapeMount(`/mnt/disk\040one`); got != "/mnt/disk one" {
		t.Errorf("unescapeMount = %q", got)
	}
	if got := unescapeMount("/plain"); got != "/plain" {
		t.Errorf("unescapeMount = %q", got)
	}
}

// TestDisksWithoutMounts covers a host whose /proc/mounts lists only pseudo filesystems:
// the disks are still listed, with their mounts reported as not mounted.
func TestDisksWithoutMounts(t *testing.T) {
	fs := newFakeFS().
		withDir(pathSysBlock, "sda").
		withFile(pathSysBlock+"/sda/size", "83886080\n").
		withFile(pathSysBlock+"/sda/queue/rotational", "1\n").
		withFile(pathProcMounts, "proc /proc proc rw 0 0\ntmpfs /run tmpfs rw 0 0\n")
	res, err := DisksIn(context.Background(), testOptions(nil), Env{FS: fs, Cmd: newFakeCmd()})
	if err != nil {
		t.Fatalf("DisksIn: %v", err)
	}
	row, ok := findRow(res, "sda")
	if !ok {
		t.Fatalf("no row for sda: %v", res.Rows)
	}
	if row[4] != "未挂载" {
		t.Errorf("mount = %q, want 未挂载", row[4])
	}
	if !strings.Contains(res.Summary, "1 个块设备") {
		t.Errorf("summary = %q", res.Summary)
	}
}

// TestDiskCellWidths keeps every cell inside what a terminal table can draw: a cell
// wider than the panel would wrap and destroy the alignment of the whole table.
func TestDiskCellWidths(t *testing.T) {
	res, err := DisksIn(context.Background(), testOptions(nil), Env{FS: fullDiskFS(), Cmd: fullDiskCmd(t)})
	if err != nil {
		t.Fatalf("DisksIn: %v", err)
	}
	for _, row := range res.Rows {
		for i, cell := range row {
			if n := len([]rune(cell)); n > 72 {
				t.Errorf("row %q column %d is %d runes wide: %q", row[0], i, n, cell)
			}
		}
	}
}

// TestDisksNoteFormatting proves a note carrying a percent sign survives: notes are
// formatted strings, and a stray verb in a path or an error message would corrupt them.
func TestDisksNoteFormatting(t *testing.T) {
	fs := newFakeFS().
		withDir(pathSysBlock).
		withFile(pathProcMounts, "proc /proc proc rw 0 0\n")
	res, err := DisksIn(context.Background(), toolbox.Options{Timeout: 0}, Env{FS: fs, Cmd: newFakeCmd()})
	if err != nil {
		t.Fatalf("DisksIn: %v", err)
	}
	for _, note := range res.Notes {
		if strings.Contains(note, "%!") && !strings.Contains(note, "已用") {
			t.Errorf("a note was formatted with a stray verb: %q", note)
		}
	}
	if got := fmt.Sprint(res.Summary); got != "未发现块设备" {
		t.Errorf("summary = %q", got)
	}
}
