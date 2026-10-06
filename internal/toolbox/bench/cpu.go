package bench

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/EasySBTeam/EasySB/internal/toolbox"
)

// cpuInfoPath is where a Linux kernel describes the processor. Anything else (Windows, a
// hardened kernel with /proc unmounted, a locked-down container) fails this read, which is
// a note rather than a failure: the benchmark still runs and still scores.
const cpuInfoPath = "/proc/cpuinfo"

// floatLoopOps is how many arithmetic operations one float loop iteration is counted as:
// a multiply, an add feeding the running sum, and the accumulator add.
const floatLoopOps = 3

// RunCPU is the toolbox entry for the CPU score.
func RunCPU(ctx context.Context, opts toolbox.Options) (toolbox.Result, error) {
	return RunCPUWith(ctx, opts, Default())
}

// RunCPUWith runs the CPU score with an explicit workload. The panel uses Default(); a
// test passes a Scale shrunk to a few megabytes so the suite finishes in milliseconds
// while running exactly the same code.
func RunCPUWith(ctx context.Context, opts toolbox.Options, s Scale) (toolbox.Result, error) {
	s = s.withDefaults()
	ctx, cancel := bound(ctx, opts)
	defer cancel()
	if err := checkCtx(ctx, "cpu"); err != nil {
		return toolbox.Result{}, err
	}
	workloads := cpuWorkloads()

	opts.Logf("bench/cpu: single core, %d workloads", len(workloads))
	single, err := runSuite(ctx, opts, s, workloads, 1)
	if err != nil {
		return toolbox.Result{}, err
	}
	opts.Logf("bench/cpu: %d workers, same work each", s.CPUWorkers)
	multi, err := runSuite(ctx, opts, s, workloads, s.CPUWorkers)
	if err != nil {
		return toolbox.Result{}, err
	}

	singleScore := opsPerSec(single.TotalOps, single.Elapsed)
	multiScore := opsPerSec(multi.TotalOps, multi.Elapsed)
	scaling := 0.0
	if singleScore > 0 {
		scaling = multiScore / singleScore
	}

	res := toolbox.Result{Headers: []string{"Item", "Score", "Notes"}}
	res.Rows = append(res.Rows, []string{
		"Single core",
		rate(singleScore, "Mops/s"),
		fmt.Sprintf("%d workloads run serially on one goroutine: %s retired in %s",
			len(workloads), humanOps(single.TotalOps), dur(single.Elapsed)),
	})
	res.Rows = append(res.Rows, []string{
		"Multi core",
		rate(multiScore, "Mops/s"),
		fmt.Sprintf("%d workers x the same work each: %s retired in %s, %.2fx the single-core score",
			multi.Workers, humanOps(multi.TotalOps), dur(multi.Elapsed), scaling),
	})
	for i, wl := range workloads {
		note := fmt.Sprintf("single core %s; %d cores together %s; %s",
			rate(single.Rows[i].Own, wl.unit), multi.Workers,
			rate(multi.Rows[i].Own, wl.unit), single.Rows[i].Check)
		res.Rows = append(res.Rows, []string{wl.label, rate(single.Rows[i].Own, wl.unit), note})
	}

	model, cores := cpuInfo()
	if model == "" {
		res.Note("CPU model unknown: %s could not be read, so only the core count is reported. This is what happens off Linux, where there is no /proc.", cpuInfoPath)
	} else {
		res.Note("CPU: %s, %d logical processors.", model, cores)
	}
	res.Note("The workloads are this panel's own: a SHA-256 pass over %s, a sieve of every integer up to %d, and a dependent multiply-add loop of %d iterations. There is no sysbench, Geekbench or lemonbench involved and no compatible score - only another run of the same panel version can be compared with this one, and changing any of those three sizes starts a new series.",
		humanSize(s.CPUHashBytes), s.CPUSieveLimit, s.CPUFloatIters)
	res.Note("The work is fixed, so a slow host measures for longer and a busy one scores lower: the numbers here are the floor of this host, not its peak. Run it on an idle box for a reading worth comparing.")
	res.Note("The score rows count retired operations over elapsed time, which mixes a byte hashed, an integer sieved and a float operation into one Mops/s. Read them against each other (single vs multi) rather than against another tool's number; the per-workload rows carry the units that do not mix.")
	res.Note("Every pass prints a checksum; two runs that disagree on one were not measuring the same work.")
	res.Summary = fmt.Sprintf("single %s, multi %s (x%.1f)",
		rate(singleScore, "Mops/s"), rate(multiScore, "Mops/s"), scaling)
	// The board keeps the two scores and drops the note about the scaling: the ratio is what
	// the report table is for.
	res.Board = fmt.Sprintf("单核 %s · 多核 %s",
		rate(singleScore, "Mops/s"), rate(multiScore, "Mops/s"))
	return res, nil
}

// cpuWorkload is one fixed workload; the composite score is built from all of them.
type cpuWorkload struct {
	// label names the workload's row.
	label string
	// unit is the unit of the workload's own row.
	unit string
	// ownDiv converts retired operations per second into unit: the hashing row counts
	// bytes per MiB, the others millions of operations.
	ownDiv float64
	// run retires a fixed amount of work and reports what it did.
	run func(ctx context.Context, opts toolbox.Options, s Scale) (cpuPass, error)
}

// cpuPass is one pass of one workload.
type cpuPass struct {
	// Ops is how much work the pass retired, in the composite score's operation count.
	Ops int64
	// Own is the pass throughput in the workload's own unit.
	Own float64
	// Elapsed is the measured section only; allocation and setup are outside it.
	Elapsed time.Duration
	// Check is what the pass computed, printed in the notes so a reader can see the loop
	// was not optimised away and can compare two runs value by value.
	Check string
}

// cpuWorkloads is the fixed suite. Order is display order.
func cpuWorkloads() []cpuWorkload {
	return []cpuWorkload{
		{label: "SHA-256", unit: unitMBps, ownDiv: 1 << 20, run: runSHA256},
		{label: "Prime sieve", unit: "Mops/s", ownDiv: 1e6, run: runSieve},
		{label: "Float loop", unit: "MFLOP/s", ownDiv: 1e6, run: runFloat},
	}
}

// shaChunk is the buffer the hash pass feeds the hasher. One MiB keeps the write count low
// while still giving the context a chance to cancel between blocks.
const shaChunk = 1 << 20

// runSHA256 hashes a fixed number of bytes from a fixed buffer. Reading the same buffer
// over and over is deliberate: the work is then identical on every host, which the
// score depends on.
func runSHA256(ctx context.Context, opts toolbox.Options, s Scale) (cpuPass, error) {
	buf := make([]byte, shaChunk)
	for i := range buf {
		buf[i] = byte(i*7 + 13)
	}
	total := s.CPUHashBytes
	h := sha256.New()
	var done int64
	start := opts.Now()
	for done < total {
		if err := checkCtx(ctx, "cpu/hash"); err != nil {
			return cpuPass{}, err
		}
		n := int64(len(buf))
		if remaining := total - done; remaining < n {
			n = remaining
		}
		if _, err := h.Write(buf[:n]); err != nil {
			return cpuPass{}, fmt.Errorf("bench: cpu/hash: %w", err)
		}
		done += n
	}
	elapsed := opts.Now().Sub(start)
	sum := h.Sum(nil)
	return cpuPass{
		Ops:     done,
		Own:     float64(done) / (1 << 20) / elapsed.Seconds(),
		Elapsed: elapsed,
		Check:   "sha256 " + hex.EncodeToString(sum[:8]) + " of " + humanSize(done),
	}, nil
}

// runSieve counts the primes below the sieve limit. The count is the accumulator.
func runSieve(ctx context.Context, opts toolbox.Options, s Scale) (cpuPass, error) {
	if err := checkCtx(ctx, "cpu/sieve"); err != nil {
		return cpuPass{}, err
	}
	limit := s.CPUSieveLimit
	if limit < 16 {
		limit = 16
	}
	start := opts.Now()
	primes := sieve(limit)
	elapsed := opts.Now().Sub(start)
	return cpuPass{
		Ops:     int64(limit),
		Own:     float64(limit) / elapsed.Seconds() / 1e6,
		Elapsed: elapsed,
		Check:   strconv.Itoa(primes) + " primes below " + strconv.Itoa(limit),
	}, nil
}

// sieve counts the primes in [2, limit] with the sieve of Eratosthenes.
func sieve(limit int) int {
	composite := make([]bool, limit+1)
	count := 0
	for i := 2; i <= limit; i++ {
		if composite[i] {
			continue
		}
		count++
		// i*i > limit means every multiple is already marked, and skipping the loop is
		// also what keeps i*i from overflowing on a 32-bit target.
		if i > limit/i {
			continue
		}
		for j := i * i; j <= limit; j += i {
			composite[j] = true
		}
	}
	return count
}

// runFloat runs a dependent multiply-add chain. Because x feeds itself, this measures the
// processor's serial float latency rather than its peak throughput, which is the honest
// thing to measure for a single workload in a panel: no compiler vectorises the chain away
// and no host gets credit for a wide unit it cannot keep fed.
func runFloat(ctx context.Context, opts toolbox.Options, s Scale) (cpuPass, error) {
	if err := checkCtx(ctx, "cpu/float"); err != nil {
		return cpuPass{}, err
	}
	iters := s.CPUFloatIters
	x := 1.0
	sum := 0.0
	start := opts.Now()
	for i := 0; i < iters; i++ {
		x = x*1.0000001 + 0.0000001
		sum += x
	}
	elapsed := opts.Now().Sub(start)
	ops := int64(iters) * floatLoopOps
	return cpuPass{
		Ops:     ops,
		Own:     float64(ops) / elapsed.Seconds() / 1e6,
		Elapsed: elapsed,
		Check:   fmt.Sprintf("%d iterations, x=%.9f sum=%.6f", iters, x, sum),
	}, nil
}

// suiteRun is one execution of the whole workload list.
type suiteRun struct {
	// Workers is how many copies ran at once.
	Workers int
	// Elapsed is the wall time of the whole suite.
	Elapsed time.Duration
	// Rows aggregates the passes of one workload across every worker: Ops summed, Own as
	// the aggregate throughput, Check taken from the first worker.
	Rows []cpuPass
	// TotalOps is every operation every worker retired.
	TotalOps int64
}

// runSuite runs the workload list `workers` times at once and aggregates the passes. Each
// worker keeps its own buffers, so no workload may share mutable state.
func runSuite(ctx context.Context, opts toolbox.Options, s Scale, workloads []cpuWorkload, workers int) (suiteRun, error) {
	if workers < 1 {
		workers = 1
	}
	passes := make([][]cpuPass, workers)
	errs := make([]error, workers)
	start := opts.Now()
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			out := make([]cpuPass, len(workloads))
			for i, wl := range workloads {
				p, err := wl.run(ctx, opts, s)
				if err != nil {
					errs[w] = err
					return
				}
				out[i] = p
			}
			passes[w] = out
		}(w)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return suiteRun{}, err
		}
	}
	wall := opts.Now().Sub(start)

	run := suiteRun{Workers: workers, Elapsed: wall, Rows: make([]cpuPass, len(workloads))}
	for i, wl := range workloads {
		agg := cpuPass{Check: passes[0][i].Check, Elapsed: wall}
		for _, pass := range passes {
			agg.Ops += pass[i].Ops
		}
		agg.Own = float64(agg.Ops) / wl.ownDiv / wall.Seconds()
		run.Rows[i] = agg
		run.TotalOps += agg.Ops
	}
	return run, nil
}

// opsPerSec is the composite score: retired operations per second, in millions.
func opsPerSec(ops int64, elapsed time.Duration) float64 {
	if ops <= 0 || elapsed <= 0 {
		return 0
	}
	return float64(ops) / elapsed.Seconds() / 1e6
}

// CPUInfo is what a cpuinfo document says about the processor.
type CPUInfo struct {
	// Model is the human-readable name, empty when the document has none.
	Model string
	// Cores is how many processor entries the document lists.
	Cores int
}

// cpuInfo reads the processor description. A failed read is not an error: the core count
// still comes from the runtime, which is why this returns empty strings rather than an
// error the benchmark would have to fail on.
func cpuInfo() (model string, cores int) {
	cores = runtime.NumCPU()
	data, err := os.ReadFile(cpuInfoPath)
	if err != nil {
		return "", cores
	}
	info := ParseCPUInfo(data)
	if info.Model != "" {
		model = info.Model
	}
	if info.Cores > 0 {
		cores = info.Cores
	}
	return model, cores
}

// ParseCPUInfo reads the model name and the processor count out of a /proc/cpuinfo
// document. It is exported and takes bytes rather than a path so the field layouts (x86,
// arm64, armv7, and the ARM kernels that only have "Processor") are testable on any
// machine, including the Windows and macOS boxes this panel is developed on.
func ParseCPUInfo(data []byte) CPUInfo {
	var out CPUInfo
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.Join(strings.Fields(value), " ")
		switch key {
		case "processor":
			if _, err := strconv.Atoi(value); err == nil {
				out.Cores++
			} else if out.Model == "" && value != "" {
				// ARMv7 kernels put the part name in "Processor: ARMv7 Processor rev 5
				// (v7l)" and use "processor: 0" for the per-CPU entries, so a non-numeric
				// value under this key is a model, not a core. Counting it as a core would
				// also report one processor more than the host has.
				out.Model = value
			}
		case "model name", "cpu model", "processor model", "hardware":
			// The first name the document offers wins. "model" alone is deliberately not
			// read: x86 uses it for the CPU family number, so it would report "85".
			if out.Model == "" && value != "" {
				out.Model = value
			}
		}
	}
	return out
}
