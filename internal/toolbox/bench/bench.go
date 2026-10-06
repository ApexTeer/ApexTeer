// Package bench measures what this host can do: a CPU score, memory bandwidth and disk
// throughput. It is the panel's own answer to the "CPU 跑分 / 内存 / 磁盘 dd" block of the
// familiar VPS review scripts, and one rule decides everything else in here:
//
//   - Nothing is downloaded and nothing outside the standard library is executed. A fresh
//     VPS has no sysbench, no fio and no dd worth calling, and the panel promises a single
//     static binary. Every number below comes from loops this process runs itself.
//
// The price of that rule is comparability, and the results pay it out loud: a score here
// means "work retired per second of this panel version's own fixed workload". It is
// meaningful against another run of the same panel build on another host, and it is not a
// sysbench, fio, dd or Geekbench number. Every result's notes say so; do not reword that
// away, because an operator who reads 1200 MB/s here as "my NVMe does 1200 MB/s" has been
// misled by a number that also depends on the filesystem, on the page cache and on how
// busy the box was.
//
// Two rules come from the toolbox contract:
//
//   - The workload is fixed and small enough to finish, so a slow host measures for longer
//     instead of measuring something else. Where an input must be sized (the disk test,
//     which must not fill a small VPS), the size actually used is reported and the reason
//     for shrinking it is a note.
//   - Every measured loop leaves behind an accumulator that reaches the result. The
//     compiler is then not free to delete the loop, and a reader can compare two runs
//     value by value.
package bench

import (
	"context"
	"fmt"
	"runtime"
	"strconv"
	"time"

	"github.com/EasySB-Team/EasySB/internal/toolbox"
)

// Group is the toolbox menu group every benchmark here is filed under.
const Group = "hardware"

// Tool IDs. They are also the i18n key suffixes ("toolbox_"+ID), so keep them short and
// stable: renaming one orphans its translation.
const (
	// CPUID is the CPU score entry.
	CPUID = "bench-cpu"
	// MemoryID is the memory bandwidth entry.
	MemoryID = "bench-mem"
	// DiskID is the disk entry: sequential write, sequential read and 4K random IO on
	// Options.ScratchDir.
	DiskID = "bench-disk"
	// DisksID is the per-mount entry. It is a separate menu item because it writes to
	// every writable block device on the host, which an operator asks for deliberately;
	// the three tools above are what Tools() hands the registry.
	DisksID = "bench-disks"
)

// unitMBps labels every bandwidth in this package. One MB here is 2^20 bytes, the reading
// the review scripts an operator is used to also produce.
const unitMBps = "MB/s"

// Scale is how much work one benchmark performs. It exists so the panel can run a full
// size suite while a test runs the same code path on a few megabytes.
//
// The zero value is not a suite: withDefaults fills every unset number from Default(), and
// the panel always calls the options-free form. A test takes Default() and shrinks the
// fields it cares about, which is the only way to make an intentional difference visible:
// a zero field can always mean "use the default".
type Scale struct {
	// CPUHashBytes is how many bytes of SHA-256 are hashed per pass. Hashing the fixed
	// buffer repeatedly is what makes the work fixed rather than input-dependent.
	CPUHashBytes int64
	// CPUSieveLimit is the sieve bound: every integer in [2, limit] is visited, and the
	// primes found are the accumulator.
	CPUSieveLimit int
	// CPUFloatIters is the iteration count of the dependent multiply-add loop.
	CPUFloatIters int
	// CPUWorkers is the parallelism of the multi-core score. Default runtime.NumCPU().
	CPUWorkers int

	// MemBufferBytes is the size of each sequential buffer (source, and the second one
	// the copy writes into).
	MemBufferBytes int
	// MemRounds is how many times each memory phase is repeated; the best round is
	// reported, which is what removes the first pass' page faults from the number.
	MemRounds int

	// DiskFileBytes is the size of the sequential test file.
	DiskFileBytes int64
	// DiskBlockBytes is the block size of the sequential phases.
	DiskBlockBytes int
	// DiskRandomOps is how many 4K operations each random phase performs.
	DiskRandomOps int
	// DiskRandomBlock is the random operation size.
	DiskRandomBlock int
	// DiskRandomBuffered leaves the 4K random writes buffered: no fsync per operation.
	// The zero value is false, which is what the panel wants - one fsync per random write
	// is what makes the IOPS number a property of the device instead of the page cache.
	// A test sets it when it wants the suite fast rather than representative.
	DiskRandomBuffered bool
	// DiskMountBytes is the file size the per-mount pass writes on each filesystem.
	DiskMountBytes int64
	// DiskMountBlock is the block size of the per-mount pass.
	DiskMountBlock int
}

// Default is the workload the panel runs. The sizes are chosen so the whole toolbox stays
// inside toolbox.DefaultTimeout on a small VPS (one or two slow cores, a slow disk) while
// each measurement is still long enough to see past a hiccup: roughly 2s of CPU, 3s of
// memory traffic and under a minute of disk.
func Default() Scale {
	return Scale{
		CPUHashBytes:    96 << 20,
		CPUSieveLimit:   5_000_000,
		CPUFloatIters:   60_000_000,
		CPUWorkers:      runtime.NumCPU(),
		MemBufferBytes:  256 << 20,
		MemRounds:       3,
		DiskFileBytes:   512 << 20,
		DiskBlockBytes:  1 << 20,
		DiskRandomOps:   1000,
		DiskRandomBlock: 4096,
		DiskMountBytes:  64 << 20,
		DiskMountBlock:  512 << 10,
	}
}

// withDefaults returns s with every unset number taken from Default(). Bools keep their
// zero value, which is also the default behaviour, so a bool is never ambiguous.
func (s Scale) withDefaults() Scale {
	d := Default()
	if s.CPUHashBytes <= 0 {
		s.CPUHashBytes = d.CPUHashBytes
	}
	if s.CPUSieveLimit <= 0 {
		s.CPUSieveLimit = d.CPUSieveLimit
	}
	if s.CPUFloatIters <= 0 {
		s.CPUFloatIters = d.CPUFloatIters
	}
	if s.CPUWorkers <= 0 {
		s.CPUWorkers = d.CPUWorkers
	}
	if s.MemBufferBytes <= 0 {
		s.MemBufferBytes = d.MemBufferBytes
	}
	if s.MemRounds <= 0 {
		s.MemRounds = d.MemRounds
	}
	if s.DiskFileBytes <= 0 {
		s.DiskFileBytes = d.DiskFileBytes
	}
	if s.DiskBlockBytes <= 0 {
		s.DiskBlockBytes = d.DiskBlockBytes
	}
	if s.DiskRandomOps <= 0 {
		s.DiskRandomOps = d.DiskRandomOps
	}
	if s.DiskRandomBlock <= 0 {
		s.DiskRandomBlock = d.DiskRandomBlock
	}
	if s.DiskMountBytes <= 0 {
		s.DiskMountBytes = d.DiskMountBytes
	}
	if s.DiskMountBlock <= 0 {
		s.DiskMountBlock = d.DiskMountBlock
	}
	return s
}

// Tools returns the three toolbox entries the registry wires into the menu. The per-mount
// disk pass is DisksTool, exported separately: it is the same measurement against every
// disk on the host and belongs behind its own menu item.
func Tools() []toolbox.Tool {
	return []toolbox.Tool{
		{ID: CPUID, Group: Group, Run: RunCPU},
		{ID: MemoryID, Group: Group, Run: RunMemory},
		{ID: DiskID, Group: Group, Run: RunDisk},
	}
}

// DisksTool is the per-mount disk entry.
func DisksTool() toolbox.Tool {
	return toolbox.Tool{ID: DisksID, Group: Group, Run: RunDisks}
}

// bound puts the tool's own budget on top of the caller's context. The panel already sets a
// deadline, and it is the same value, so this changes nothing there; it is here so a caller
// that forgot one still gets a benchmark that stops instead of one that runs for an hour.
func bound(ctx context.Context, opts toolbox.Options) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, opts.Duration())
}

// checkCtx turns a cancelled context into the error the panel shows. Benchmarks are the
// longest thing in the toolbox, so they are the ones a user is most likely to interrupt.
func checkCtx(ctx context.Context, what string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("bench: %s cancelled: %w", what, err)
	}
	return nil
}

// rate renders a throughput with a readable number of digits: none above a thousand, one
// in the tens, two below ten, so a column of numbers stays aligned and a slow VM is not
// rounded to nothing.
func rate(v float64, unit string) string {
	switch {
	case v >= 1000:
		return strconv.FormatFloat(v, 'f', 0, 64) + " " + unit
	case v >= 10:
		return strconv.FormatFloat(v, 'f', 1, 64) + " " + unit
	default:
		return strconv.FormatFloat(v, 'f', 2, 64) + " " + unit
	}
}

// humanSize renders a byte count the way an operator reads it.
func humanSize(n int64) string {
	switch {
	case n >= 1<<30:
		return strconv.FormatFloat(float64(n)/(1<<30), 'f', 1, 64) + " GiB"
	case n >= 1<<20:
		return strconv.FormatFloat(float64(n)/(1<<20), 'f', 1, 64) + " MiB"
	case n >= 1<<10:
		return strconv.FormatFloat(float64(n)/(1<<10), 'f', 1, 64) + " KiB"
	default:
		return strconv.FormatInt(n, 10) + " B"
	}
}

// humanOps renders an operation count in millions, which is the unit the CPU score's
// operations are counted in.
func humanOps(n int64) string {
	return strconv.FormatFloat(float64(n)/1e6, 'f', 1, 64) + "M ops"
}

// dur renders a duration at the precision these measurements care about: milliseconds for
// a phase, microseconds for a single IO.
func dur(d time.Duration) string {
	if d < time.Millisecond {
		return d.Round(time.Microsecond).String()
	}
	return d.Round(time.Millisecond).String()
}

// mibPerSec is the bandwidth of n bytes moved in d, in the 2^20 MB these tools report.
func mibPerSec(n int64, d time.Duration) float64 {
	if n <= 0 || d <= 0 {
		return 0
	}
	return float64(n) / (1 << 20) / d.Seconds()
}

// maxRepeatPasses bounds how many passes one reading may cover when the clock is too coarse to
// time a single one.
const maxRepeatPasses = 1024

// timed runs a memory pass and reports how long it took, how many passes that was, and the last
// accumulator. A pass faster than the clock's resolution would otherwise be measured as zero
// nanoseconds, and a zero-nanosecond reading becomes a bandwidth of 0 MB/s — not a slow host,
// just a clock that did not move. The pass is therefore repeated until the clock advances, and
// the reading covers all of them.
func timed(now func() time.Time, pass func() uint64) (time.Duration, int, uint64) {
	start := now()
	var acc uint64
	for runs := 1; ; runs++ {
		acc = pass()
		elapsed := now().Sub(start)
		if elapsed > 0 || runs >= maxRepeatPasses {
			return elapsed, runs, acc
		}
	}
}

// timedIO is timed for IO that can fail: it runs a pass and reports how long it took and how
// many bytes it moved, repeating it while the clock has not moved. A pass faster than the
// clock's resolution would otherwise be timed as zero and printed as 0 MB/s or 0 IOPS — a clock
// artefact where a measurement belongs. The reading covers every pass.
func timedIO(now func() time.Time, pass func() (int64, error)) (time.Duration, int64, error) {
	start := now()
	var total int64
	for runs := 1; ; runs++ {
		n, err := pass()
		if err != nil {
			return 0, total, err
		}
		total += n
		if elapsed := now().Sub(start); elapsed > 0 || runs >= maxRepeatPasses {
			return elapsed, total, nil
		}
	}
}
