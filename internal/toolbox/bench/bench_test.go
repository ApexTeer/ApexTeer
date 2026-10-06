package bench

import (
	"context"
	"errors"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/EasySBTeam/EasySB/internal/toolbox"
)

// smallScale is Default() shrunk to a few megabytes: every test runs the real measurement
// path, just on a workload small enough that the whole package finishes in seconds. It is
// built from Default() on purpose, so a field added to Scale is picked up here instead of
// silently left at zero.
func smallScale() Scale {
	s := Default()
	s.CPUHashBytes = 4 << 20
	s.CPUSieveLimit = 20_000
	s.CPUFloatIters = 200_000
	s.CPUWorkers = 2
	s.MemBufferBytes = 4 << 20
	s.MemRounds = 1
	s.DiskFileBytes = 4 << 20
	s.DiskBlockBytes = 64 << 10
	s.DiskRandomOps = 100
	s.DiskRandomBlock = 4096
	s.DiskRandomBuffered = true
	s.DiskMountBytes = 4 << 20
	s.DiskMountBlock = 64 << 10
	return s
}

// stubFreeSpace and stubMemoryProbe replace a platform probe for the duration of a test, so
// the capacity paths can be driven without owning a full disk or a small machine. t.Cleanup
// also restores them after a Fatal.
func stubFreeSpace(t *testing.T, free int64, ok bool) {
	t.Helper()
	old := freeSpaceFunc
	freeSpaceFunc = func(string) (int64, bool) { return free, ok }
	t.Cleanup(func() { freeSpaceFunc = old })
}

func stubMemoryProbe(t *testing.T, avail int64, ok bool) {
	t.Helper()
	old := availableMemoryFunc
	availableMemoryFunc = func() (int64, bool) { return avail, ok }
	t.Cleanup(func() { availableMemoryFunc = old })
}

// scoreOf splits a score cell into its value and its unit, so a test checks the number and
// the unit the tool claimed rather than parsing the string by hand.
func scoreOf(t *testing.T, cell string) (float64, string) {
	t.Helper()
	fields := strings.Fields(cell)
	if len(fields) != 2 {
		t.Fatalf("score cell %q: want \"<number> <unit>\"", cell)
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		t.Fatalf("score cell %q: %v", cell, err)
	}
	return v, fields[1]
}

// notesContain reports whether any note carries the substring.
func notesContain(res toolbox.Result, substr string) bool {
	for _, n := range res.Notes {
		if strings.Contains(n, substr) {
			return true
		}
	}
	return false
}

func TestScaleWithDefaults(t *testing.T) {
	d := Default()
	if got := (Scale{}).withDefaults(); got != d {
		t.Errorf("zero Scale defaults to %+v, want %+v", got, d)
	}
	partial := Scale{CPUWorkers: 1, MemBufferBytes: 1 << 20}.withDefaults()
	if partial.CPUWorkers != 1 || partial.MemBufferBytes != 1<<20 {
		t.Errorf("set fields were overwritten: %+v", partial)
	}
	if partial.DiskFileBytes != d.DiskFileBytes || partial.MemRounds != d.MemRounds {
		t.Errorf("unset fields were not filled: %+v", partial)
	}
	if partial.DiskRandomBuffered {
		t.Error("withDefaults must not turn on the buffered random path: the zero value is already the panel's choice")
	}
}

func TestDefaultWorkloadIsBounded(t *testing.T) {
	d := Default()
	// The whole point of a fixed workload is that it is small enough for a VPS and large
	// enough to be a measurement. These bounds are what keeps a future edit honest.
	if d.CPUHashBytes < 1<<25 || d.CPUHashBytes > 1<<30 {
		t.Errorf("CPUHashBytes = %d, want between 32 MiB and 1 GiB", d.CPUHashBytes)
	}
	if d.CPUSieveLimit < 1_000_000 || d.CPUSieveLimit > 100_000_000 {
		t.Errorf("CPUSieveLimit = %d, want between 1e6 and 1e8", d.CPUSieveLimit)
	}
	if d.MemBufferBytes < 1<<24 {
		t.Errorf("MemBufferBytes = %d, want at least 16 MiB", d.MemBufferBytes)
	}
	if d.DiskFileBytes < 1<<28 {
		t.Errorf("DiskFileBytes = %d, want at least 256 MiB", d.DiskFileBytes)
	}
	if d.CPUWorkers != runtime.NumCPU() {
		t.Errorf("CPUWorkers = %d, want %d", d.CPUWorkers, runtime.NumCPU())
	}
}

func TestToolsShape(t *testing.T) {
	tools := Tools()
	if len(tools) != 3 {
		t.Fatalf("Tools() returned %d entries, want 3", len(tools))
	}
	want := []string{CPUID, MemoryID, DiskID}
	for i, tool := range tools {
		if tool.ID != want[i] {
			t.Errorf("tool %d ID = %q, want %q", i, tool.ID, want[i])
		}
		if tool.Group != Group {
			t.Errorf("%s group = %q, want %q", tool.ID, tool.Group, Group)
		}
		if tool.Run == nil {
			t.Errorf("%s has no Run", tool.ID)
		}
	}
	if got := DisksTool(); got.ID != DisksID || got.Group != Group || got.Run == nil {
		t.Errorf("DisksTool() = %+v, want id %q in group %q", got, DisksID, Group)
	}
}

func TestFormatting(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"rate small", rate(0.5, unitMBps), "0.50 MB/s"},
		{"rate tens", rate(12.34, unitMBps), "12.3 MB/s"},
		{"rate thousands", rate(1235.6, "IOPS"), "1236 IOPS"},
		{"rate zero", rate(0, "IOPS"), "0.00 IOPS"},
		{"size bytes", humanSize(512), "512 B"},
		{"size kib", humanSize(4 << 10), "4.0 KiB"},
		{"size mib", humanSize(96 << 20), "96.0 MiB"},
		{"size gib", humanSize(3 << 30), "3.0 GiB"},
		{"ops", humanOps(190_000_000), "190.0M ops"},
		{"dur sub-milli", dur(250 * time.Microsecond), "250µs"},
		{"dur milli", dur(1500 * time.Millisecond), "1.5s"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, c.got, c.want)
		}
	}
	if got := mibPerSec(1<<20, time.Second); got != 1 {
		t.Errorf("mibPerSec(1 MiB, 1s) = %v, want 1", got)
	}
	if got := mibPerSec(1<<20, 0); got != 0 {
		t.Errorf("mibPerSec with no time = %v, want 0", got)
	}
}

func TestCheckCtxReportsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := checkCtx(ctx, "cpu"); err == nil {
		t.Fatal("checkCtx on a cancelled context returned nil")
	}
	if err := checkCtx(context.Background(), "cpu"); err != nil {
		t.Fatalf("checkCtx on a live context: %v", err)
	}
}

// TestToolsHonourTheOptionsTimeout is the budget half of "controlled runtime": with a
// deadline of one millisecond and a workload that cannot finish in it, every tool must stop
// and say so - and the disk tool must still clean up the file it started.
func TestToolsHonourTheOptionsTimeout(t *testing.T) {
	scratch := t.TempDir()
	s := smallScale()
	s.MemBufferBytes = 64 << 20
	s.MemRounds = 1
	s.DiskFileBytes = 64 << 20
	opts := toolbox.Options{Timeout: time.Millisecond, Scratch: scratch}

	cases := []struct {
		name string
		run  func(context.Context, toolbox.Options, Scale) (toolbox.Result, error)
	}{
		{"cpu", RunCPUWith},
		{"memory", RunMemoryWith},
		{"disk", RunDiskWith},
	}
	for _, c := range cases {
		_, err := c.run(context.Background(), opts, s)
		if err == nil {
			t.Errorf("%s: a one millisecond budget was ignored", c.name)
			continue
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("%s: error %v does not carry the deadline", c.name, err)
		}
	}
	entries, err := os.ReadDir(scratch)
	if err != nil {
		t.Fatalf("reading the scratch directory: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("the cancelled disk run left %d files behind: %v", len(entries), entries)
	}
}

// TestToolsRespectCancellation is what keeps a user's Esc from being ignored: every entry
// point must return the cancellation instead of starting a two-minute benchmark.
func TestToolsRespectCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := smallScale()
	cases := []struct {
		name string
		run  func(context.Context, toolbox.Options, Scale) (toolbox.Result, error)
	}{
		{"cpu", RunCPUWith},
		{"memory", RunMemoryWith},
		{"disk", RunDiskWith},
	}
	for _, c := range cases {
		if _, err := c.run(ctx, toolbox.Options{Scratch: t.TempDir()}, s); err == nil {
			t.Errorf("%s: a cancelled context was ignored", c.name)
		}
	}
	if _, err := RunDisksWith(ctx, toolbox.Options{}, s, func() ([]Mount, error) {
		return []Mount{{Point: t.TempDir(), Device: "/dev/vda1", FSType: "ext4"}}, nil
	}); err == nil {
		t.Error("disks: a cancelled context was ignored")
	}
}
