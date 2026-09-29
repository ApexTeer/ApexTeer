package bench

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"time"

	"github.com/MinimaxFlora/EasySB/internal/toolbox"
)

// diskFileName is the file the single-disk benchmark writes under Options.ScratchDir. The
// name is fixed so a run interrupted by a kill leaves something an operator can recognise
// and delete, and so two tools never fight over one path.
const diskFileName = "easysb-bench-disk"

// minDiskFree is the space the disk benchmark refuses to run below. Below this a
// measurement would be a measurement of a full filesystem, and filling a system partition
// would be worse than the missing number.
const minDiskFree = 8 << 20

// diskShare is the fraction of the free space the sequential file may take. A quarter
// leaves room for the panel's own writes on a small VPS, and the shrink is reported.
const diskShare = 4

// freeSpaceFunc is the platform free-space probe. It is a variable so a test can drive the
// capacity path - refusing to run, and shrinking the file - without filling a disk.
var freeSpaceFunc = freeSpace

// RunDisk is the toolbox entry for the disk test.
func RunDisk(ctx context.Context, opts toolbox.Options) (toolbox.Result, error) {
	return RunDiskWith(ctx, opts, Default())
}

// RunDiskWith measures Options.ScratchDir: sequential write (with fsync), sequential read
// and 4K random IO. The file it creates is removed before it returns, including on error.
func RunDiskWith(ctx context.Context, opts toolbox.Options, s Scale) (toolbox.Result, error) {
	s = s.withDefaults()
	ctx, cancel := bound(ctx, opts)
	defer cancel()
	if err := checkCtx(ctx, "disk"); err != nil {
		return toolbox.Result{}, err
	}
	dir := opts.ScratchDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return toolbox.Result{}, fmt.Errorf("bench: disk: scratch directory %s is not usable: %w", dir, err)
	}
	path := filepath.Join(dir, diskFileName)

	size := s.DiskFileBytes
	shrinkNote := ""
	if free, ok := freeSpaceFunc(dir); ok {
		if free < minDiskFree {
			return toolbox.Result{}, fmt.Errorf("bench: disk: %s has %s free, and at least %s is needed to measure anything", dir, humanSize(free), humanSize(minDiskFree))
		}
		if ceiling := free / diskShare; size > ceiling {
			size = ceiling
			shrinkNote = fmt.Sprintf("The test file was shrunk to %s: %s had only %s free, and this tool never takes more than a quarter of it.", humanSize(size), dir, humanSize(free))
		}
	}

	// One file serves all three phases, which is also what keeps the page cache behaviour
	// of the read phase comparable with the write phase that just filled it.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o644)
	if err != nil {
		return toolbox.Result{}, fmt.Errorf("bench: disk: cannot create %s: %w", path, err)
	}
	defer func() {
		f.Close()
		os.Remove(path)
	}()

	block := make([]byte, s.DiskBlockBytes)
	for i := range block {
		block[i] = byte(i*31 + 7)
	}

	// Sequential write, then fsync. The fsync is timed separately so the row can say what
	// the durability cost, rather than hiding it in the throughput or leaving it out.
	opts.Logf("bench/disk: writing %s to %s", humanSize(size), path)
	writePass := func() (int64, error) {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return 0, fmt.Errorf("bench: disk: seek %s: %w", path, err)
		}
		var written int64
		for written < size {
			if err := checkCtx(ctx, "disk/write"); err != nil {
				return written, err
			}
			n := int64(len(block))
			if remaining := size - written; remaining < n {
				n = remaining
			}
			w, werr := f.Write(block[:n])
			written += int64(w)
			if werr != nil {
				return written, fmt.Errorf("bench: disk: writing %s failed after %s (out of space, or not writable?): %w",
					path, humanSize(written), werr)
			}
			if w == 0 {
				return written, fmt.Errorf("bench: disk: writing %s stalled at %s", path, humanSize(written))
			}
		}
		return written, nil
	}
	writeDur, writeBytes, err := timedIO(opts.Now, writePass)
	if err != nil {
		return toolbox.Result{}, err
	}
	written := size
	syncStart := opts.Now()
	if err := f.Sync(); err != nil {
		return toolbox.Result{}, fmt.Errorf("bench: disk: fsync of %s failed: %w", path, err)
	}
	syncDur := opts.Now().Sub(syncStart)

	// Sequential read of what was just written. No O_DIRECT: the panel does not take a
	// dependency to bypass the page cache, and the filesystems a VPS mounts (overlayfs,
	// tmpfs, ZFS) frequently refuse it.
	opts.Logf("bench/disk: reading %s back", humanSize(written))
	var rsum = uint64(fnvOffset)
	readDur, readBytes, err := timedIO(opts.Now, func() (int64, error) {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return 0, fmt.Errorf("bench: disk: seek %s: %w", path, err)
		}
		var read int64
		for read < written {
			if err := checkCtx(ctx, "disk/read"); err != nil {
				return read, err
			}
			n := int64(len(block))
			if remaining := written - read; remaining < n {
				n = remaining
			}
			r, rerr := f.Read(block[:n])
			if r > 0 {
				read += int64(r)
				// The fold reads a few bytes per block, so it costs nothing next to the IO
				// and the read still cannot be dropped.
				rsum = foldBlock(rsum, block[:r])
			}
			if rerr != nil {
				if errors.Is(rerr, io.EOF) {
					break
				}
				return read, fmt.Errorf("bench: disk: reading %s: %w", path, rerr)
			}
			if r == 0 {
				break
			}
		}
		return read, nil
	})
	if err != nil {
		return toolbox.Result{}, err
	}

	// 4K random IO over the file that is now on the device. The offsets come from a fixed
	// seed, so two runs of the same size walk the same pattern.
	randomBlock := s.DiskRandomBlock
	var (
		writeIOPS float64
		readIOPS  float64
		writeLat  time.Duration
		readLat   time.Duration
	)
	if int64(randomBlock) > written {
		res := diskResult(path, written, writeBytes, readBytes, s, writeDur, syncDur, readDur, rsum, shrinkNote, nil)
		res.Note("The file is smaller than one %d-byte random block, so the random phases were skipped.", randomBlock)
		return res, nil
	}
	slots := int(written / int64(randomBlock))
	rng := rand.New(rand.NewPCG(1, 2))
	payload := make([]byte, randomBlock)
	for i := range payload {
		payload[i] = byte(i*17 + 3)
	}
	buf := make([]byte, randomBlock)

	opts.Logf("bench/disk: %d random writes of %s", s.DiskRandomOps, humanSize(int64(randomBlock)))
	wsum := uint64(fnvOffset)
	writeDurR, writeOpsBytes, err := timedIO(opts.Now, func() (int64, error) {
		for i := 0; i < s.DiskRandomOps; i++ {
			if i&0x3f == 0 {
				if err := checkCtx(ctx, "disk/randwrite"); err != nil {
					return int64(i) * int64(randomBlock), err
				}
			}
			// The block carries its own sequence number, so the checksum in the result
			// proves every iteration wrote instead of one write being counted a thousand
			// times.
			putSequence(payload, i)
			off := int64(rng.IntN(slots)) * int64(randomBlock)
			if _, err := f.WriteAt(payload, off); err != nil {
				return int64(i) * int64(randomBlock), fmt.Errorf("bench: disk: random write to %s at %d: %w", path, off, err)
			}
			wsum = foldBlock(wsum, payload)
			if !s.DiskRandomBuffered {
				// Without this the writes sit in the page cache and the IOPS number would
				// describe memory, not the device.
				if err := f.Sync(); err != nil {
					return int64(i) * int64(randomBlock), fmt.Errorf("bench: disk: fsync after random write to %s: %w", path, err)
				}
			}
		}
		return int64(s.DiskRandomOps) * int64(randomBlock), nil
	})
	if err != nil {
		return toolbox.Result{}, err
	}
	if ops := writeOpsBytes / int64(randomBlock); ops > 0 && writeDurR > 0 {
		writeIOPS = float64(ops) / writeDurR.Seconds()
		writeLat = writeDurR / time.Duration(ops)
	}

	opts.Logf("bench/disk: %d random reads of %s", s.DiskRandomOps, humanSize(int64(randomBlock)))
	rng = rand.New(rand.NewPCG(1, 2))
	rsumR := uint64(fnvOffset)
	readDurR, readOpsBytes, err := timedIO(opts.Now, func() (int64, error) {
		for i := 0; i < s.DiskRandomOps; i++ {
			if i&0x3f == 0 {
				if err := checkCtx(ctx, "disk/randread"); err != nil {
					return int64(i) * int64(randomBlock), err
				}
			}
			off := int64(rng.IntN(slots)) * int64(randomBlock)
			n, err := f.ReadAt(buf, off)
			if n > 0 {
				rsumR = foldBlock(rsumR, buf[:n])
			}
			if err != nil && n < len(buf) {
				return int64(i) * int64(randomBlock), fmt.Errorf("bench: disk: random read from %s at %d: %w", path, off, err)
			}
		}
		return int64(s.DiskRandomOps) * int64(randomBlock), nil
	})
	if err != nil {
		return toolbox.Result{}, err
	}
	if ops := readOpsBytes / int64(randomBlock); ops > 0 && readDurR > 0 {
		readIOPS = float64(ops) / readDurR.Seconds()
		readLat = readDurR / time.Duration(ops)
	}

	random := &randomRun{
		block:         randomBlock,
		ops:           s.DiskRandomOps,
		buffered:      s.DiskRandomBuffered,
		writeIOPS:     writeIOPS,
		readIOPS:      readIOPS,
		writeLat:      writeLat,
		readLat:       readLat,
		writeChecksum: wsum,
		readChecksum:  rsumR,
	}
	return diskResult(path, written, writeBytes, readBytes, s, writeDur, syncDur, readDur, rsum, shrinkNote, random), nil
}

// randomRun is the 4K phase's outcome, kept separate from the sequential numbers so the
// result builder stays readable.
type randomRun struct {
	block     int
	ops       int
	buffered  bool
	writeIOPS float64
	readIOPS  float64
	writeLat  time.Duration
	readLat   time.Duration
	// writeChecksum and readChecksum are folded from the blocks each phase actually moved.
	// They are printed rather than dropped so a reader can see the IO happened - and so two
	// runs that disagree were not measuring the same thing.
	writeChecksum uint64
	readChecksum  uint64
}

// diskResult assembles the table and the notes. It is shared by the full run and by the
// run that skips the random phases because the file came out too small.
func diskResult(path string, written, writeBytes, readBytes int64, s Scale, writeDur, syncDur, readDur time.Duration, readSum uint64, shrinkNote string, random *randomRun) toolbox.Result {
	res := toolbox.Result{Headers: []string{"Item", "Throughput", "Notes"}}
	res.Rows = append(res.Rows, []string{
		"Sequential write",
		rate(mibPerSec(writeBytes, writeDur+syncDur), unitMBps),
		fmt.Sprintf("%s written in %s in %s blocks, including the final fsync (%s)",
			humanSize(written), dur(writeDur+syncDur), humanSize(int64(s.DiskBlockBytes)), dur(syncDur)),
	})
	res.Rows = append(res.Rows, []string{
		"Sequential read",
		rate(mibPerSec(readBytes, readDur), unitMBps),
		fmt.Sprintf("%s read back in %s; checksum %#x", humanSize(written), dur(readDur), readSum),
	})
	if random != nil {
		wNote := fmt.Sprintf("%d writes of %s at random offsets in the %s file, %s each",
			random.ops, humanSize(int64(random.block)), humanSize(written), dur(random.writeLat))
		if !random.buffered {
			wNote += ", each followed by an fsync"
		}
		res.Rows = append(res.Rows, []string{
			"4K random write",
			rate(random.writeIOPS, "IOPS"),
			wNote + fmt.Sprintf("; checksum %#x", random.writeChecksum),
		})
		res.Rows = append(res.Rows, []string{
			"4K random read",
			rate(random.readIOPS, "IOPS"),
			fmt.Sprintf("%d reads of %s at random offsets, %s each; checksum %#x",
				random.ops, humanSize(int64(random.block)), dur(random.readLat), random.readChecksum),
		})
	}

	res.Note("Temporary file: %s - created for this run and deleted when it returned.", path)
	res.Note("This is the panel's own write/read/random loop, not dd, fio or sysbench, and the numbers are not comparable with theirs. It measures the file system as much as the device: the page cache, the journal, and whether the mount is overlayfs, tmpfs or ZFS all move these numbers.")
	res.Note("No O_DIRECT: the buffers are ordinary ones, so the sequential read and every random read can be answered from the page cache. Read the read rows as an upper bound on the device, and the write rows (which do fsync) as the more trustworthy half.")
	res.Note("Filling the free space would be a worse outcome than a missing number, so the file is capped at a quarter of the free space on the scratch directory; the capacity check is a platform probe and is skipped where the platform cannot answer (anything but Linux).")
	if shrinkNote != "" {
		res.Note("%s", shrinkNote)
	}
	res.Note("Options.ScratchDir is where this runs. It is the system temporary directory by default, which on many VPS images is a tmpfs in RAM: to measure a real disk, point the scratch directory at it.")
	res.Summary = fmt.Sprintf("write %s, read %s", rate(mibPerSec(writeBytes, writeDur+syncDur), unitMBps), rate(mibPerSec(readBytes, readDur), unitMBps))
	// The board leaves the 4K random pair to the report: two throughput figures are what an
	// operator compares between hosts, and the four-number summary is a row, not a line.
	res.Board = fmt.Sprintf("写 %s · 读 %s",
		rate(mibPerSec(writeBytes, writeDur+syncDur), unitMBps), rate(mibPerSec(readBytes, readDur), unitMBps))
	if random != nil {
		res.Summary += fmt.Sprintf(", 4K random %s write / %s read",
			rate(random.writeIOPS, "IOPS"), rate(random.readIOPS, "IOPS"))
	}
	return res
}

// fnvOffset is the FNV-1a 64-bit offset basis. Checksums start here rather than at zero so
// that a block of zeroes cannot swallow the blocks folded after it.
const fnvOffset = 0xcbf29ce484222325

// foldBlock folds a block into a running checksum and returns it. It reads at most the
// first 64 bytes of the block, so it costs nothing next to the IO, and it multiplies as it
// folds, so two blocks cannot cancel each other out the way a plain sum of patterns does -
// a sum is exactly the function that turns a checksum into zero.
func foldBlock(acc uint64, block []byte) uint64 {
	if len(block) > 64 {
		block = block[:64]
	}
	for _, b := range block {
		acc = (acc ^ uint64(b)) * 0x100000001b3
	}
	return acc
}

// putSequence stamps the operation's sequence number into the first bytes of a block, so
// the checksum of a random-write phase proves every operation wrote something of its own.
func putSequence(block []byte, seq int) {
	if len(block) < 8 {
		return
	}
	v := uint64(seq) + 1
	for i := 0; i < 8; i++ {
		block[i] = byte(v >> (8 * i))
	}
}
