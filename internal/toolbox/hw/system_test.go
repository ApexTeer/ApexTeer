package hw

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"testing"

	"github.com/EasySB-Team/EasySB/internal/toolbox"
)

func TestSystemInfo(t *testing.T) {
	// virtualFixture drops the hypervisor flag so the DMI vendor decides the verdict.
	physicalCPU := strings.ReplaceAll(cpuInfoFixture, " hypervisor", "")
	noMemAvailable := "MemTotal: 8192000 kB\nMemFree: 1024000 kB\n"

	cases := []struct {
		name   string
		fs     *fakeFS
		cmd    *fakeCmd
		client toolbox.HTTPDoer
		check  func(t *testing.T, res toolbox.Result, cmd *fakeCmd)
	}{
		{
			name: "full fixture",
			fs:   fullSystemFS(),
			cmd:  fullSystemCmd(),
			check: func(t *testing.T, res toolbox.Result, _ *fakeCmd) {
				want := map[string]string{
					labelHostname:  "vps01",
					labelDistro:    "Ubuntu 24.04.1 LTS",
					labelKernel:    "6.1.0-18-cloud-amd64",
					labelArch:      runtime.GOARCH,
					labelVirt:      "虚拟化平台 kvm（systemd-detect-virt）；DMI QEMU Standard PC (i440FX + PIIX, 1996)",
					labelCPUModel:  "Intel(R) Xeon(R) CPU E5-2680 v4 @ 2.40GHz",
					labelCPUCores:  "2 路 × 2 核 = 4 物理核 / 4 逻辑线程",
					labelCPUClock:  "2394–2600 MHz（均值 2447）",
					labelCPUCache:  "L1d 32K · L1i 32K · L2 256K · L3 35M",
					labelMemory:    "7.8 GiB 总 / 3.9 GiB 可用（已用 3.9 GiB）",
					labelSwap:      "2.0 GiB 总 / 2.0 GiB 空闲",
					labelUptime:    "11 天 10 小时 20 分",
					labelLoad:      "0.52 / 0.31 / 0.22",
					labelTimezone:  "Asia/Shanghai",
					labelNTP:       "已同步（NTP 已开启）",
					labelEgressIP:  "203.0.113.7",
					labelIPOwner:   "美国 · 加利福尼亚 · 洛杉矶",
					labelASN:       "AS13335 Cloudflare, Inc.（ISP Cloudflare, Inc.）",
					labelCollected: "2026-09-26 12:00:00 UTC",
				}
				for label, value := range want {
					got, ok := rowValue(res, label)
					if !ok {
						t.Errorf("row %q is missing (notes: %v)", label, res.Notes)
						continue
					}
					if got != value {
						t.Errorf("%s = %q, want %q", label, got, value)
					}
				}
				if got, _ := rowValue(res, labelProcesses); got != "3 个进程 · 312 个线程（运行中 2）" {
					t.Errorf("%s = %q", labelProcesses, got)
				}
				if notesContain(res, "无法读取") {
					t.Errorf("a full fixture should have no read failures: %v", res.Notes)
				}
				for _, want := range []string{"主频是采集瞬间", "缓存来自", "数据来源", "架构来自面板二进制"} {
					if !notesContain(res, want) {
						t.Errorf("no note mentions %q: %v", want, res.Notes)
					}
				}
				for _, want := range []string{"E5-2680 v4", "4 核 / 4 线程", "内存 7.8 GiB", "虚拟化平台 kvm"} {
					if !strings.Contains(res.Summary, want) {
						t.Errorf("summary %q does not mention %q", res.Summary, want)
					}
				}
			},
		},
		{
			name: "arm cpuinfo without frequency or core ids, no commands, no caches",
			fs: newFakeFS().
				withFile(pathCPUInfo, cpuInfoARM).
				withFile(pathMemInfo, memInfoFixture).
				withFile(pathLoadAvg, "1.00 2.00 3.00\n").
				withFile(pathHostname, "nanopi\n").
				withFile(pathOSRelease, osReleaseFixture).
				withDir(pathProcDir, "acpi", "self"),
			cmd: newFakeCmd(),
			check: func(t *testing.T, res toolbox.Result, _ *fakeCmd) {
				if got, _ := rowValue(res, labelCPUCores); got != "逻辑线程 2" {
					t.Errorf("%s = %q", labelCPUCores, got)
				}
				if got, _ := rowValue(res, labelCPUModel); got != "ARMv8 Processor rev 1 (v8l)" {
					t.Errorf("%s = %q", labelCPUModel, got)
				}
				if _, ok := rowValue(res, labelCPUClock); ok {
					t.Error("a host without cpu MHz must not get a frequency row")
				}
				for _, want := range []string{
					"没有同时给出 physical id 与 core id",
					"没有 cpu MHz",
					"无法列出",
					"虚拟化：未能确认",
					"未安装 timedatectl",
					"进程与线程",
				} {
					if !notesContain(res, want) {
						t.Errorf("no note mentions %q: %v", want, res.Notes)
					}
				}
				if got, _ := rowValue(res, labelLoad); got != "1.00 / 2.00 / 3.00" {
					t.Errorf("%s = %q", labelLoad, got)
				}
				if _, ok := rowValue(res, labelProcesses); ok {
					t.Error("without a readable /proc there is no process count")
				}
			},
		},
		{
			name: "cpuinfo unreadable",
			fs: newFakeFS().
				withFile(pathMemInfo, memInfoFixture).
				withFile(pathUptime, "3600.00 0.00\n").
				withFile(pathLoadAvg, "0.00 0.00 0.00 1/100 123\n").
				withDir(pathProcDir, "1").
				withFile(pathKernel, "5.15.0\n"),
			cmd: newFakeCmd(),
			check: func(t *testing.T, res toolbox.Result, _ *fakeCmd) {
				if !notesContain(res, "CPU：无法读取 "+pathCPUInfo) {
					t.Errorf("a missing /proc/cpuinfo has to be named: %v", res.Notes)
				}
				for _, label := range []string{labelCPUModel, labelCPUCores, labelCPUClock} {
					if _, ok := rowValue(res, label); ok {
						t.Errorf("%s must not exist without cpuinfo", label)
					}
				}
				if got, _ := rowValue(res, labelMemory); !strings.Contains(got, "7.8 GiB 总") {
					t.Errorf("%s = %q", labelMemory, got)
				}
				if got, _ := rowValue(res, labelProcesses); got != "1 个进程 · 100 个线程（运行中 1）" {
					t.Errorf("%s = %q", labelProcesses, got)
				}
				if got, _ := rowValue(res, labelKernel); got != "5.15.0" {
					t.Errorf("%s = %q", labelKernel, got)
				}
			},
		},
		{
			name: "bare metal without systemd-detect-virt",
			fs: newFakeFS().
				withFile(pathCPUInfo, physicalCPU).
				withFile(pathDMIVendor, "Supermicro\n").
				withFile(pathDMIProduct, "SYS-1029U-TR4\n"),
			cmd: newFakeCmd().installed(toolTimeDateCt).
				answer(toolTimeDateCt, []string{"show", "--property=Timezone", "--property=NTP", "--property=NTPSynchronized"},
					"Timezone=UTC\nNTP=no\nNTPSynchronized=no\n", nil),
			check: func(t *testing.T, res toolbox.Result, _ *fakeCmd) {
				got, _ := rowValue(res, labelVirt)
				if !strings.Contains(got, "物理机（DMI 厂商是硬件厂商") {
					t.Errorf("%s = %q", labelVirt, got)
				}
				if !notesContain(res, "未安装 "+toolVirt) {
					t.Errorf("the fallback has to say why it was used: %v", res.Notes)
				}
				if got, _ := rowValue(res, labelNTP); got != "未开启 NTP 服务" {
					t.Errorf("%s = %q", labelNTP, got)
				}
				if got, _ := rowValue(res, labelTimezone); got != "UTC" {
					t.Errorf("%s = %q", labelTimezone, got)
				}
			},
		},
		{
			name: "systemd-detect-virt reports a container",
			fs:   fullSystemFS(),
			cmd:  newFakeCmd().installed(toolVirt).answer(toolVirt, nil, "docker\n", nil).installed(toolTimeDateCt),
			check: func(t *testing.T, res toolbox.Result, _ *fakeCmd) {
				got, _ := rowValue(res, labelVirt)
				if !strings.Contains(got, "容器（systemd-detect-virt 报 docker）") {
					t.Errorf("%s = %q", labelVirt, got)
				}
			},
		},
		{
			name: "systemd-detect-virt fails, DMI and the cpu flag decide",
			fs:   fullSystemFS(),
			cmd:  newFakeCmd().installed(toolVirt).answer(toolVirt, nil, "detect-virt: failed\n", errors.New("exit status 1")),
			check: func(t *testing.T, res toolbox.Result, _ *fakeCmd) {
				got, _ := rowValue(res, labelVirt)
				if !strings.Contains(got, "cpuinfo 报告 hypervisor 标志") {
					t.Errorf("%s = %q", labelVirt, got)
				}
				if !notesContain(res, "没有给出类型") {
					t.Errorf("the failed command has to be reported: %v", res.Notes)
				}
			},
		},
		{
			name:   "ip-api refuses the lookup",
			fs:     fullSystemFS(),
			cmd:    fullSystemCmd(),
			client: &fakeClient{status: http.StatusOK, body: `{"status":"fail","message":"reserved range"}`},
			check: func(t *testing.T, res toolbox.Result, _ *fakeCmd) {
				if !notesContain(res, "拒绝了这次查询") {
					t.Errorf("a refused lookup is a note: %v", res.Notes)
				}
				for _, label := range []string{labelEgressIP, labelIPOwner, labelASN} {
					if _, ok := rowValue(res, label); ok {
						t.Errorf("%s must not be filled without a lookup", label)
					}
				}
			},
		},
		{
			name:   "ip-api unreachable",
			fs:     fullSystemFS(),
			cmd:    fullSystemCmd(),
			client: &fakeClient{err: errors.New("dial tcp: connection refused")},
			check: func(t *testing.T, res toolbox.Result, _ *fakeCmd) {
				if !notesContain(res, "查询 ip-api.com 失败") {
					t.Errorf("a failed lookup is a note: %v", res.Notes)
				}
				if res.Summary == "" {
					t.Error("summary is empty")
				}
			},
		},
		{
			name: "timedatectl reports an unfinished sync and the runtime zone",
			fs:   fullSystemFS(),
			cmd: newFakeCmd().installed(toolTimeDateCt).answer(toolTimeDateCt,
				[]string{"show", "--property=Timezone", "--property=NTP", "--property=NTPSynchronized"},
				"Timezone=Europe/Berlin\nNTP=yes\nNTPSynchronized=no\n", nil),
			client: &fakeClient{status: http.StatusOK, body: ipAPISuccess},
			check: func(t *testing.T, res toolbox.Result, _ *fakeCmd) {
				if got, _ := rowValue(res, labelNTP); got != "未同步（NTP 已开启，尚未完成一次同步）" {
					t.Errorf("%s = %q", labelNTP, got)
				}
				if got, _ := rowValue(res, labelTimezone); got != "Europe/Berlin" {
					t.Errorf("%s = %q, the runtime zone wins over /etc/timezone", labelTimezone, got)
				}
			},
		},
		{
			name: "meminfo without MemAvailable and without swap",
			fs:   newFakeFS().withFile(pathMemInfo, noMemAvailable),
			cmd:  newFakeCmd(),
			check: func(t *testing.T, res toolbox.Result, _ *fakeCmd) {
				got, _ := rowValue(res, labelMemory)
				if !strings.Contains(got, "1000 MiB 可用") {
					t.Errorf("%s = %q, the MemFree fallback has to be used", labelMemory, got)
				}
				if !notesContain(res, "来自 MemFree") {
					t.Errorf("the fallback field has to be named: %v", res.Notes)
				}
				if got, _ := rowValue(res, labelSwap); got != "未配置" {
					t.Errorf("%s = %q", labelSwap, got)
				}
			},
		},
		{
			name: "unreadable meminfo and uptime, missing loadavg",
			fs:   newFakeFS().withFile(pathUptime, "not a number\n"),
			cmd:  newFakeCmd(),
			check: func(t *testing.T, res toolbox.Result, _ *fakeCmd) {
				for _, want := range []string{"内存：无法读取", "开机时长：", "负载：无法读取", "时区："} {
					if !notesContain(res, want) {
						t.Errorf("no note mentions %q: %v", want, res.Notes)
					}
				}
				if _, ok := rowValue(res, labelMemory); ok {
					t.Error("memory must not be reported without MemTotal")
				}
				if _, ok := rowValue(res, labelUptime); ok {
					t.Error("uptime must not be reported from unparseable input")
				}
			},
		},
		{
			name: "garbage loadavg and zero uptime",
			fs:   newFakeFS().withFile(pathLoadAvg, "garbage\n").withFile(pathUptime, "0.0 1.0\n"),
			cmd:  newFakeCmd(),
			check: func(t *testing.T, res toolbox.Result, _ *fakeCmd) {
				if !notesContain(res, "负载："+pathLoadAvg+" 的内容无法解析") {
					t.Errorf("an unparseable loadavg has to be named: %v", res.Notes)
				}
				if !notesContain(res, "开机时长："+pathUptime+" 的内容无法解析") {
					t.Errorf("a zero uptime has to be explained: %v", res.Notes)
				}
				for _, label := range []string{labelLoad, labelUptime} {
					if _, ok := rowValue(res, label); ok {
						t.Errorf("%s must not be reported from garbage", label)
					}
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := SystemInfoIn(t.Context(), testOptions(tc.client), Env{FS: tc.fs, Cmd: tc.cmd})
			if err != nil {
				t.Fatalf("SystemInfoIn: %v", err)
			}
			checkShape(t, res)
			if len(res.Headers) != 2 || res.Headers[0] != labelItem || res.Headers[1] != labelValue {
				t.Fatalf("headers = %v, want 项目/值", res.Headers)
			}
			tc.check(t, res, tc.cmd)
		})
	}
}

func TestParseCPUInfo(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		check func(t *testing.T, info cpuInfo)
	}{
		{
			name: "x86 with core ids and frequencies",
			text: cpuInfoFixture,
			check: func(t *testing.T, info cpuInfo) {
				if info.logical != 4 || info.physical != 4 || info.sockets != 2 {
					t.Errorf("logical/physical/sockets = %d/%d/%d, want 4/4/2", info.logical, info.physical, info.sockets)
				}
				if !info.hypervisor {
					t.Error("the hypervisor flag on a continuation line was not read")
				}
				if info.mhzCount != 4 || info.mhzMin != 2394.375 || info.mhzMax != 2599.999 {
					t.Errorf("mhz %d %v..%v", info.mhzCount, info.mhzMin, info.mhzMax)
				}
			},
		},
		{
			name: "arm without physical ids or frequencies",
			text: cpuInfoARM,
			check: func(t *testing.T, info cpuInfo) {
				if info.logical != 2 {
					t.Errorf("logical = %d, want 2", info.logical)
				}
				if info.physical != 0 || info.sockets != 0 {
					t.Errorf("physical/sockets = %d/%d, want 0/0", info.physical, info.sockets)
				}
				if info.mhzCount != 0 {
					t.Errorf("mhz count = %d, want 0", info.mhzCount)
				}
				if info.hypervisor {
					t.Error("Features without hypervisor must not set the flag")
				}
			},
		},
		{
			name: "cpu cores without core ids multiplies the sockets",
			text: "processor : 0\ncpu cores : 8\nphysical id : 0\n\nprocessor : 1\ncpu cores : 8\nphysical id : 1\n",
			check: func(t *testing.T, info cpuInfo) {
				if info.physical != 16 || info.sockets != 2 {
					t.Errorf("physical/sockets = %d/%d, want 16/2", info.physical, info.sockets)
				}
			},
		},
		{
			name: "empty and garbage",
			text: "\n\nnot a cpuinfo\n",
			check: func(t *testing.T, info cpuInfo) {
				if info.logical != 0 || info.model != "" || info.mhzCount != 0 {
					t.Errorf("garbage produced %+v", info)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { tc.check(t, parseCPUInfo(tc.text)) })
	}
}

func TestSystemFormatting(t *testing.T) {
	t.Run("meminfo", func(t *testing.T) {
		mem := parseMemInfo(memInfoFixture)
		if mem.total != 8192000*1024 || mem.available != 4096000*1024 {
			t.Errorf("total/available = %d/%d", mem.total, mem.available)
		}
		if mem.swapTotal != 2097152*1024 || mem.memAvailableMissing {
			t.Errorf("swap = %d, fallback = %v", mem.swapTotal, mem.memAvailableMissing)
		}
		if got := parseMemInfo("MemTotal: 1024 kB\nMemFree: 512 kB\n"); got.available != 512*1024 || !got.memAvailableMissing {
			t.Errorf("MemFree fallback = %+v", got)
		}
		if got := parseMemInfo("nothing here"); got.total != 0 {
			t.Errorf("garbage produced %+v", got)
		}
	})

	t.Run("loadavg", func(t *testing.T) {
		load, running, total, ok := parseLoadAvg("0.52 0.31 0.22 2/312 45678\n")
		if !ok || load != "0.52 / 0.31 / 0.22" || running != "2" || total != "312" {
			t.Errorf("got %q %q/%q ok=%v", load, running, total, ok)
		}
		if _, _, _, ok := parseLoadAvg("garbage\n"); ok {
			t.Error("garbage must not parse")
		}
	})

	t.Run("uptime", func(t *testing.T) {
		if got := humanDuration(parseUptime("90061.5 1.0\n")); got != "1 天 1 小时 1 分" {
			t.Errorf("uptime = %q", got)
		}
		if got := parseUptime("0.0 1.0\n"); got != 0 {
			t.Errorf("zero uptime = %v", got)
		}
	})

	t.Run("cache and byte sizes", func(t *testing.T) {
		for _, tc := range []struct{ in, want string }{
			{"32K", "32K"}, {"256K", "256K"}, {"35840K", "35M"}, {"1536K", "1.5M"},
			{"1024", "1.0 KiB"}, {"", ""}, {"64KB", "64K"},
		} {
			if got := cacheSize(tc.in); got != tc.want {
				t.Errorf("cacheSize(%q) = %q, want %q", tc.in, got, tc.want)
			}
		}
		for _, tc := range []struct {
			in   uint64
			want string
		}{
			{0, "0 B"}, {512, "512 B"}, {1024, "1.0 KiB"}, {42949672960, "40 GiB"}, {8388608000, "7.8 GiB"},
		} {
			if got := humanBytes(tc.in); got != tc.want {
				t.Errorf("humanBytes(%d) = %q, want %q", tc.in, got, tc.want)
			}
		}
		if got := percentUsed(100, 30); got != 70 {
			t.Errorf("percentUsed = %d, want 70", got)
		}
		if got := percentUsed(100, 120); got != 0 {
			t.Errorf("an available figure above the total must clamp, got %d", got)
		}
	})

	t.Run("os-release", func(t *testing.T) {
		if got := osPrettyName(osReleaseFixture); got != "Ubuntu 24.04.1 LTS" {
			t.Errorf("osPrettyName = %q", got)
		}
		if got := osPrettyName(`NAME=Alpine Linux`); got != "Alpine Linux" {
			t.Errorf("osPrettyName = %q", got)
		}
		if got := osPrettyName(""); got != "" {
			t.Errorf("osPrettyName(\"\") = %q", got)
		}
	})

	t.Run("key=value", func(t *testing.T) {
		props := parseKeyValue(timedatectlFixture)
		if props["NTP"] != "yes" || props["Time zon"] != "" || props["Timezone"] != "Asia/Shanghai" {
			t.Errorf("props = %v", props)
		}
		if len(parseKeyValue("no equals sign\n")) != 0 {
			t.Error("a line without = must be skipped")
		}
	})

	t.Run("vendor lists", func(t *testing.T) {
		if !isHypervisorVendor("QEMU") || isHypervisorVendor("Supermicro") {
			t.Error("a hypervisor vendor was misclassified")
		}
		if !isHardwareVendor("Dell Inc.") || isHardwareVendor("QEMU") {
			t.Error("a hardware vendor was misclassified")
		}
		if isHypervisorVendor("") || isHardwareVendor("   ") {
			t.Error("an empty vendor matches nothing")
		}
	})
}

// TestTools checks the registry contract: two entries, one group, both runnable.
func TestTools(t *testing.T) {
	tools := Tools()
	if len(tools) != 2 {
		t.Fatalf("Tools() returned %d entries, want 2", len(tools))
	}
	seen := map[string]bool{}
	for _, tool := range tools {
		if tool.ID == "" || tool.Group != GroupHardware || tool.Run == nil {
			t.Errorf("tool %+v is incomplete", tool)
		}
		if seen[tool.ID] {
			t.Errorf("duplicate tool id %q", tool.ID)
		}
		seen[tool.ID] = true
	}
	for _, id := range []string{ToolInfoID, ToolDiskID} {
		if !seen[id] {
			t.Errorf("Tools() is missing %q", id)
		}
	}
	// The ids are also i18n key suffixes; the panel's table carries these two.
	if ToolInfoID != "hw-info" || ToolDiskID != "hw-disk" {
		t.Errorf("ids changed: %q %q, the registry and internal/i18n/table.go say hw-info/hw-disk",
			ToolInfoID, ToolDiskID)
	}
}

// TestHostSmoke runs both tools against the real host with the network stubbed out. It
// is what keeps the osFS and osCommander paths compiled and exercised: on a development
// machine every reading lands in a note, and on a Linux box it reads the real files.
func TestHostSmoke(t *testing.T) {
	offline := &fakeClient{err: errors.New("offline")}

	sys, err := SystemInfo(t.Context(), testOptions(offline))
	if err != nil {
		t.Fatalf("SystemInfo: %v", err)
	}
	for i, row := range sys.Rows {
		if len(row) != 2 {
			t.Fatalf("system row %d has %d cells: %v", i, len(row), row)
		}
	}
	if sys.Summary == "" {
		t.Error("system summary is empty")
	}
	if len(sys.Rows) == 0 && len(sys.Notes) == 0 {
		t.Fatal("a table has to say something")
	}

	disks, err := Disks(t.Context(), testOptions(offline))
	if err != nil {
		t.Fatalf("Disks: %v", err)
	}
	checkShape(t, disks)
	for i, row := range disks.Rows {
		if len(row) != 7 {
			t.Fatalf("disk row %d has %d cells: %v", i, len(row), row)
		}
	}
}

// TestContextCancelled proves both tools stay inside the context they were handed: the
// egress lookup is the one call that can outlive its budget, so it is the one checked.
func TestContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client := &fakeClient{err: errors.New("context canceled")}
	res, err := SystemInfoIn(ctx, testOptions(client), Env{FS: fullSystemFS(), Cmd: fullSystemCmd()})
	if err != nil {
		t.Fatalf("SystemInfoIn: %v", err)
	}
	if !notesContain(res, "ip-api.com") {
		t.Errorf("the cancelled lookup has to be reported: %v", res.Notes)
	}

	disks, err := DisksIn(ctx, testOptions(client), Env{FS: fullDiskFS(), Cmd: fullDiskCmd(t)})
	if err != nil {
		t.Fatalf("DisksIn: %v", err)
	}
	if len(disks.Rows) == 0 {
		t.Fatal("a cancelled context is not a reason to lose the disk table")
	}
	if got := fmt.Sprint(disks.Summary); !strings.Contains(got, "块设备") {
		t.Errorf("summary = %q", got)
	}
}
