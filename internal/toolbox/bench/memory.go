package bench

import (
	"context"
	"encoding/binary"
	"fmt"
	"runtime"
	"time"

	"github.com/EasySBTeam/EasySB/internal/toolbox"
)

// memChunk is the granularity of the copy loop and of the source buffer's fill: large
// enough that the loop overhead disappears next to the memory traffic.
const memChunk = 1 << 20

// memHeadroom divides the host's available memory: the test holds two buffers at once and
// the panel needs the rest of the machine for itself, so one buffer may take an eighth.
const memHeadroom = 8

// minMemBuffer is the smallest buffer worth measuring on. Below it the phases are shorter
// than the timer's noise and a small VPS should be told it is short of memory instead.
const minMemBuffer = 4 << 20

// availableMemoryFunc is the probe the memory test sizes its buffers with. It is a variable
// so a test can drive the shrink and the refusal without owning a small machine.
var availableMemoryFunc = availableMemory

// RunMemory is the toolbox entry for the memory bandwidth test.
func RunMemory(ctx context.Context, opts toolbox.Options) (toolbox.Result, error) {
	return RunMemoryWith(ctx, opts, Default())
}

// RunMemoryWith measures sequential write, read and copy bandwidth over a fixed buffer.
// The panel uses Default() (256 MiB, best of three rounds); a test passes a few megabytes
// and one round.
func RunMemoryWith(ctx context.Context, opts toolbox.Options, s Scale) (toolbox.Result, error) {
	s = s.withDefaults()
	ctx, cancel := bound(ctx, opts)
	defer cancel()
	if err := checkCtx(ctx, "memory"); err != nil {
		return toolbox.Result{}, err
	}
	n := s.MemBufferBytes
	if n < 4096 {
		return toolbox.Result{}, fmt.Errorf("bench: memory: buffer of %s is too small to measure", humanSize(int64(n)))
	}
	// Two buffers are held at once, so a 256 MiB buffer is half a gigabyte of resident
	// memory: on the small VPS this panel is built for that is enough to make the kernel
	// reclaim pages under the benchmark and score the swap, or to get the panel killed.
	shrinkNote := ""
	if avail, ok := availableMemoryFunc(); ok {
		ceiling := avail / memHeadroom
		if ceiling < minMemBuffer {
			return toolbox.Result{}, fmt.Errorf("bench: memory: only %s of memory is available, and at least %s is needed for one buffer", humanSize(avail), humanSize(minMemBuffer))
		}
		if int64(n) > ceiling {
			n = int(ceiling)
			shrinkNote = fmt.Sprintf("The buffers were shrunk to %s each: the host reports only %s of memory available, and this test holds two buffers at once.", humanSize(int64(n)), humanSize(avail))
		}
	}
	// Two buffers: the copy needs a source and a destination. They are allocated before
	// any timer starts, because a large allocation is the runtime asking the kernel for
	// pages, which is not memory bandwidth.
	src := make([]byte, n)
	dst := make([]byte, n)
	seed := uint64(0x9e3779b97f4a7c15)

	opts.Logf("bench/memory: %s buffers, best of %d rounds", humanSize(int64(n)), s.MemRounds)

	writeDur, writeRuns, writeCheck := bestRound(s.MemRounds, opts.Now, func() uint64 {
		memWritePass(src, seed)
		// The read-back is outside the timer: it proves the stores landed (the compiler
		// cannot drop a store it then has to read) without paying for a second traversal
		// in the number.
		return memReadPass(src)
	})
	if err := checkCtx(ctx, "memory"); err != nil {
		return toolbox.Result{}, err
	}

	readDur, readRuns, readCheck := bestRound(s.MemRounds, opts.Now, func() uint64 {
		return memReadPass(src)
	})
	if err := checkCtx(ctx, "memory"); err != nil {
		return toolbox.Result{}, err
	}

	copyDur, copyRuns, copyCheck := bestRound(s.MemRounds, opts.Now, func() uint64 {
		for off := 0; off < n; off += memChunk {
			end := off + memChunk
			if end > n {
				end = n
			}
			copy(dst[off:end], src[off:end])
		}
		return memReadPass(dst)
	})
	// The buffers must outlive the timers; without this the compiler is entitled to treat
	// the second one as dead once the checksums are computed.
	runtime.KeepAlive(src)
	runtime.KeepAlive(dst)

	res := toolbox.Result{Headers: []string{"Item", "Bandwidth", "Notes"}}
	res.Rows = append(res.Rows, []string{
		"Sequential write",
		rate(mibPerSec(int64(n)*int64(writeRuns), writeDur), unitMBps),
		fmt.Sprintf("%s written with 8-byte stores, best of %d in %s",
			humanSize(int64(n)), s.MemRounds, dur(writeDur)),
	})
	res.Rows = append(res.Rows, []string{
		"Sequential read",
		rate(mibPerSec(int64(n)*int64(readRuns), readDur), unitMBps),
		fmt.Sprintf("%s summed with 8-byte loads, best of %d in %s",
			humanSize(int64(n)), s.MemRounds, dur(readDur)),
	})
	res.Rows = append(res.Rows, []string{
		"Copy (read+write)",
		rate(mibPerSec(int64(n)*2*int64(copyRuns), copyDur), unitMBps),
		fmt.Sprintf("%s copied in %s; counted as %s moved, because a copy reads and writes",
			humanSize(int64(n)), dur(copyDur), humanSize(int64(n)*2)),
	})

	res.Note("This is this process' own loop over a heap buffer of %s, not STREAM and not sysbench - a separate, incompatible measurement. One %s here is 2^20 bytes.", humanSize(int64(n)), unitMBps)
	if shrinkNote != "" {
		res.Note("%s", shrinkNote)
	}
	res.Note("The buffer is capped at an eighth of the memory the host reports as available, so that measuring the memory of a small VPS cannot turn into swapping on it; a host without /proc, or one whose kernel will not say, keeps the requested size.")
	res.Note("Each phase is reported as its fastest of %d rounds, which is what removes the first pass' page faults from a freshly allocated buffer. A phase that is slow on every round is the host, not the allocator.", s.MemRounds)
	res.Note("The buffers are allocated before the timers start, and each timed loop leaves an accumulator that reaches this result, so no part of it can be optimised away. Accumulators: write %#x, read %#x, copy %#x.", writeCheck, readCheck, copyCheck)
	res.Note("A small VPS pays for its memory bandwidth in cache misses: with a buffer larger than the last-level cache, the number is main-memory bandwidth, and a host that overcommits memory or is swapping scores much lower here than its CPU score suggests.")
	res.Board = fmt.Sprintf("写 %s · 读 %s · 拷贝 %s",
		rate(mibPerSec(int64(n)*int64(writeRuns), writeDur), unitMBps),
		rate(mibPerSec(int64(n)*int64(readRuns), readDur), unitMBps),
		rate(mibPerSec(int64(n)*2*int64(copyRuns), copyDur), unitMBps))
	res.Summary = fmt.Sprintf("write %s, read %s, copy %s",
		rate(mibPerSec(int64(n)*int64(writeRuns), writeDur), unitMBps),
		rate(mibPerSec(int64(n)*int64(readRuns), readDur), unitMBps),
		rate(mibPerSec(int64(n)*2*int64(copyRuns), copyDur), unitMBps))
	return res, nil
}

// bestRound runs one phase rounds times and returns the fastest reading, how many passes that
// reading covered, and the last round's accumulator. The fastest is reported because a single
// noisy round should not decide the number; the accumulator is kept to prove the loop ran.
func bestRound(rounds int, now func() time.Time, pass func() uint64) (time.Duration, int, uint64) {
	if rounds < 1 {
		rounds = 1
	}
	var (
		best     time.Duration
		bestRuns int
		check    uint64
	)
	for i := 0; i < rounds; i++ {
		d, runs, c := timed(now, pass)
		check = c
		if i == 0 || d < best {
			best, bestRuns = d, runs
		}
	}
	return best, bestRuns, check
}

func memWritePass(buf []byte, seed uint64) uint64 {
	var acc uint64
	i := 0
	for ; i+8 <= len(buf); i += 8 {
		v := seed + uint64(i)
		binary.LittleEndian.PutUint64(buf[i:], v)
		acc += v
	}
	for ; i < len(buf); i++ {
		buf[i] = byte(i)
		acc += uint64(buf[i])
	}
	return acc
}

// memReadPass sums buf in 8-byte loads. The sum is the accumulator the read bandwidth
// depends on, so a compiler cannot turn the read loop into nothing.
func memReadPass(buf []byte) uint64 {
	var acc uint64
	i := 0
	for ; i+8 <= len(buf); i += 8 {
		acc += binary.LittleEndian.Uint64(buf[i:])
	}
	for ; i < len(buf); i++ {
		acc += uint64(buf[i])
	}
	return acc
}
