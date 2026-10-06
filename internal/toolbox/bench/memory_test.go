package bench

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/EasySB-Team/EasySB/internal/toolbox"
)

// TestRunMemorySmall runs the real bandwidth measurement on a 4 MiB buffer and checks the
// shape, the units and the accumulator that proves the loops ran. The absolute bandwidth
// belongs to the machine running the test, so nothing here pins a value.
func TestRunMemorySmall(t *testing.T) {
	// Pin the capacity probe: the assertions below are about the measurement, and this must
	// not depend on how much memory the machine running the test happens to have.
	stubMemoryProbe(t, 0, false)
	s := smallScale()
	res, err := RunMemoryWith(context.Background(), toolbox.Options{}, s)
	if err != nil {
		t.Fatalf("RunMemoryWith: %v", err)
	}
	if len(res.Rows) != 3 {
		t.Fatalf("got %d rows, want 3: %v", len(res.Rows), res.Rows)
	}
	labels := []string{"Sequential write", "Sequential read", "Copy (read+write)"}
	for i, row := range res.Rows {
		if row[0] != labels[i] {
			t.Errorf("row %d label = %q, want %q", i, row[0], labels[i])
		}
		v, unit := scoreOf(t, row[1])
		if v <= 0 {
			t.Errorf("row %d (%s) bandwidth = %v, want a positive measurement", i, row[0], v)
		}
		if unit != unitMBps {
			t.Errorf("row %d (%s) unit = %q, want %q", i, row[0], unit, unitMBps)
		}
	}
	if !notesContain(res, "4.0 MiB") {
		t.Errorf("notes must name the buffer size: %v", res.Notes)
	}
	if !notesContain(res, "Accumulators") {
		t.Errorf("notes must carry the accumulators that prove the loops ran: %v", res.Notes)
	}
	if !notesContain(res, "STREAM") {
		t.Errorf("notes must say this is not STREAM/sysbench: %v", res.Notes)
	}
	if !strings.Contains(res.Summary, "write") || !strings.Contains(res.Summary, "read") || !strings.Contains(res.Summary, "copy") {
		t.Errorf("summary %q must carry all three phases", res.Summary)
	}
}

// TestRunMemoryCopyCountsBothDirections pins the one non-obvious unit in the result: a
// copy moves the buffer twice, and the note has to say so or the number reads 2x too high.
func TestRunMemoryCopyCountsBothDirections(t *testing.T) {
	stubMemoryProbe(t, 0, false)
	res, err := RunMemoryWith(context.Background(), toolbox.Options{}, smallScale())
	if err != nil {
		t.Fatalf("RunMemoryWith: %v", err)
	}
	note := res.Rows[2][2]
	if !strings.Contains(note, "8.0 MiB") {
		t.Errorf("copy note = %q, want the doubled byte count", note)
	}
}

// TestRunMemoryShrinksToFitAvailableMemory is the small-VPS case: the buffers are capped at
// an eighth of what the host can spare and the result says so, because two buffers of the
// requested size on a 512 MiB box means the kernel reclaims pages under the benchmark and the
// reading becomes a reading of swap.
func TestRunMemoryShrinksToFitAvailableMemory(t *testing.T) {
	stubMemoryProbe(t, 64<<20, true)
	s := smallScale()
	s.MemBufferBytes = 256 << 20
	res, err := RunMemoryWith(context.Background(), toolbox.Options{}, s)
	if err != nil {
		t.Fatalf("RunMemoryWith: %v", err)
	}
	if !notesContain(res, "shrunk to 8.0 MiB") {
		t.Errorf("notes must report the buffer actually used: %v", res.Notes)
	}
	if len(res.Rows) != 3 {
		t.Fatalf("got %d rows, want 3: %v", len(res.Rows), res.Rows)
	}
}

// TestRunMemoryRefusesWhenMemoryIsTiny is the other capacity outcome: too little memory for
// even the smallest useful buffer is an error with a reason, not a measurement of nothing.
func TestRunMemoryRefusesWhenMemoryIsTiny(t *testing.T) {
	stubMemoryProbe(t, 8<<20, true)
	_, err := RunMemoryWith(context.Background(), toolbox.Options{}, smallScale())
	if err == nil {
		t.Fatal("a host with 8 MiB available was accepted")
	}
	if !strings.Contains(err.Error(), "memory is available") {
		t.Errorf("error %q must explain the memory problem", err)
	}
}

func TestRunMemoryRejectsTinyBuffer(t *testing.T) {
	s := smallScale()
	s.MemBufferBytes = 128
	if _, err := RunMemoryWith(context.Background(), toolbox.Options{}, s); err == nil {
		t.Fatal("a 128 byte buffer was accepted; the measurement would be noise")
	}
}

// TestMemoryPassesAgree checks the two accumulator implementations against each other:
// a write pass followed by a read pass must produce the same sum, which is what proves
// the read loop reads what the write loop wrote.
func TestMemoryPassesAgree(t *testing.T) {
	buf := make([]byte, 4096)
	memWritePass(buf, 0x1234)
	if got, want := memReadPass(buf), seedSum(4096, 0x1234); got != want {
		t.Errorf("checksum of a written buffer = %#x, want %#x", got, want)
	}
}

// seedSum is the value memWritePass stores, computed independently: v = seed + i for every
// 8-byte word i, and the remaining bytes are the index itself.
func seedSum(n int, seed uint64) uint64 {
	var acc uint64
	i := 0
	for ; i+8 <= n; i += 8 {
		acc += seed + uint64(i)
	}
	for ; i < n; i++ {
		acc += uint64(byte(i))
	}
	return acc
}

// A reading that lands inside one clock tick still has to produce a rate. The platform this
// panel is developed on has a coarse clock: a few megabytes are read and copied faster than it
// moves, and the reading used to come out as 0 MB/s — a clock artefact printed where a
// measurement belongs. The pass is repeated until the clock moves, and the reading covers all
// of them.
func TestMemoryMeasurementSurvivesACoarseClock(t *testing.T) {
	stubMemoryProbe(t, 0, false)
	// A clock that stands still for two of every three calls: a single pass is faster than
	// one tick, so the measurement has to repeat the pass to have anything to divide by.
	calls := 0
	epoch := time.Now()
	clock := func() time.Time {
		calls++
		return epoch.Add(time.Duration(calls/3) * time.Millisecond)
	}
	res, err := RunMemoryWith(context.Background(), toolbox.Options{Clock: clock}, smallScale())
	if err != nil {
		t.Fatalf("RunMemoryWith: %v", err)
	}
	for i, row := range res.Rows {
		v, _ := scoreOf(t, row[1])
		if v <= 0 {
			t.Errorf("row %d (%s) bandwidth = %v under a coarse clock, want a positive rate", i, row[0], v)
		}
	}
}

// timed is the helper that keeps a fast pass from being timed as zero.
func TestTimedRepeatsUntilTheClockMoves(t *testing.T) {
	calls := 0
	epoch := time.Now()
	now := func() time.Time {
		calls++
		return epoch.Add(time.Duration(calls/4) * time.Millisecond)
	}
	passes := 0
	d, runs, acc := timed(now, func() uint64 {
		passes++
		return uint64(passes)
	})
	if d <= 0 {
		t.Fatalf("duration = %v, want a positive reading", d)
	}
	if runs < 2 {
		t.Fatalf("runs = %d, want the pass repeated until the clock moved", runs)
	}
	if passes != runs || acc != uint64(runs) {
		t.Fatalf("runs = %d with %d passes and accumulator %d", runs, passes, acc)
	}
}
