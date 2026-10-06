package bench

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/EasySBTeam/EasySB/internal/toolbox"
)

// TestRunDiskSmall runs the real disk benchmark on a few megabytes. It checks the four
// rows, their units, that the throughput and the IOPS are measurements rather than zeroes,
// and the promise the tool makes about the machine it runs on: the test file is gone.
func TestRunDiskSmall(t *testing.T) {
	cases := []struct {
		name     string
		buffered bool
		ops      int
	}{
		{"buffered random IO", true, 100},
		{"one fsync per random write", false, 20},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			scratch := t.TempDir()
			s := smallScale()
			s.DiskRandomBuffered = c.buffered
			s.DiskRandomOps = c.ops

			res, err := RunDiskWith(context.Background(), toolbox.Options{Scratch: scratch}, s)
			if err != nil {
				t.Fatalf("RunDiskWith: %v", err)
			}
			if len(res.Rows) != 4 {
				t.Fatalf("got %d rows, want 4: %v", len(res.Rows), res.Rows)
			}
			want := []struct {
				label string
				unit  string
			}{
				{"Sequential write", unitMBps},
				{"Sequential read", unitMBps},
				{"4K random write", "IOPS"},
				{"4K random read", "IOPS"},
			}
			for i, w := range want {
				row := res.Rows[i]
				if row[0] != w.label {
					t.Errorf("row %d label = %q, want %q", i, row[0], w.label)
				}
				v, unit := scoreOf(t, row[1])
				if v <= 0 {
					t.Errorf("row %d (%s) = %v, want a positive measurement", i, row[0], v)
				}
				if unit != w.unit {
					t.Errorf("row %d (%s) unit = %q, want %q", i, row[0], unit, w.unit)
				}
			}
			path := filepath.Join(scratch, diskFileName)
			if !notesContain(res, path) {
				t.Errorf("notes must name the temporary file %s: %v", path, res.Notes)
			}
			if !notesContain(res, "O_DIRECT") {
				t.Errorf("notes must say the page cache is not bypassed: %v", res.Notes)
			}
			if strings.HasSuffix(res.Rows[1][2], "checksum 0x0") || strings.HasSuffix(res.Rows[2][2], "checksum 0x0") {
				t.Errorf("a zero checksum means the reads read nothing, or the sequence numbers never reached the file: %v / %v", res.Rows[1], res.Rows[2])
			}
			if !notesContain(res, "not dd, fio or sysbench") {
				t.Errorf("notes must say the numbers are not comparable with dd/fio: %v", res.Notes)
			}
			if !strings.Contains(res.Summary, "4K random") {
				t.Errorf("summary %q must carry the random IO", res.Summary)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Errorf("the test file %s was left behind (stat err %v)", path, err)
			}
			if _, err := os.Stat(scratch); err != nil {
				t.Errorf("the scratch directory %s went missing: %v", scratch, err)
			}
		})
	}
}

// TestRunDiskScratchDirIsCreated checks the other half of the scratch contract: a
// directory that does not exist yet is created rather than reported as a failure.
func TestRunDiskScratchDirIsCreated(t *testing.T) {
	scratch := filepath.Join(t.TempDir(), "deeper", "still")
	if _, err := RunDiskWith(context.Background(), toolbox.Options{Scratch: scratch}, smallScale()); err != nil {
		t.Fatalf("RunDiskWith with a missing scratch directory: %v", err)
	}
	if fi, err := os.Stat(scratch); err != nil || !fi.IsDir() {
		t.Errorf("scratch directory was not created: %v", err)
	}
}

// TestRunDiskUnwritableScratch is the permission case: when the scratch path cannot be
// used, the tool must say so and name the path, not measure the system temporary directory
// instead. A file standing where the directory should be is a portable way to make a path
// unusable on every platform.
func TestRunDiskUnwritableScratch(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, []byte("not a directory\n"), 0o644); err != nil {
		t.Fatalf("preparing the blocked path: %v", err)
	}
	_, err := RunDiskWith(context.Background(), toolbox.Options{Scratch: blocked}, smallScale())
	if err == nil {
		t.Fatal("a scratch path that is a file was accepted")
	}
	if !strings.Contains(err.Error(), blocked) {
		t.Errorf("error %q does not name the unusable path %q", err, blocked)
	}
}

// TestRunDiskMissingSpace drives the capacity path by reporting a nearly full file system:
// below the floor the tool must refuse with a clear reason rather than fill the disk.
func TestRunDiskMissingSpace(t *testing.T) {
	stubFreeSpace(t, 1<<20, true)

	_, err := RunDiskWith(context.Background(), toolbox.Options{Scratch: t.TempDir()}, smallScale())
	if err == nil {
		t.Fatal("a file system with 1 MiB free was accepted")
	}
	if !strings.Contains(err.Error(), "free") || !strings.Contains(err.Error(), "needed") {
		t.Errorf("error %q must explain the capacity problem", err)
	}
}

// TestRunDiskShrinksToFitFreeSpace covers the other capacity outcome: enough room to run,
// not enough for the full file. The file shrinks, and the result says so.
func TestRunDiskShrinksToFitFreeSpace(t *testing.T) {
	stubFreeSpace(t, 16<<20, true)

	s := smallScale()
	s.DiskFileBytes = 512 << 20
	res, err := RunDiskWith(context.Background(), toolbox.Options{Scratch: t.TempDir()}, s)
	if err != nil {
		t.Fatalf("RunDiskWith: %v", err)
	}
	if !notesContain(res, "shrunk to 4.0 MiB") {
		t.Errorf("notes must report the shrink and the size used: %v", res.Notes)
	}
}

// TestRunDiskTooSmallForRandomIO covers the last branch: a file smaller than one random
// block means the random phases cannot run, and the result says that instead of dividing
// by zero or reporting a zero IOPS as if it were a measurement.
func TestRunDiskTooSmallForRandomIO(t *testing.T) {
	s := smallScale()
	s.DiskFileBytes = 2048
	s.DiskRandomBlock = 4096
	res, err := RunDiskWith(context.Background(), toolbox.Options{Scratch: t.TempDir()}, s)
	if err != nil {
		t.Fatalf("RunDiskWith: %v", err)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("got %d rows, want the two sequential ones: %v", len(res.Rows), res.Rows)
	}
	if !notesContain(res, "random phases were skipped") {
		t.Errorf("notes must explain the skipped random phases: %v", res.Notes)
	}
}

// TestFreeSpaceProbe covers the capacity probe on both sides of its build tag: on Linux it
// must answer for a real directory, and everywhere else it must admit it cannot - which is
// what makes RunDisk fall back to reporting a write failure instead of guessing at a size.
func TestFreeSpaceProbe(t *testing.T) {
	free, ok := freeSpace(t.TempDir())
	if runtime.GOOS == "linux" {
		if !ok {
			t.Fatal("statfs failed on a temporary directory")
		}
		if free <= 0 {
			t.Errorf("free space = %d, want a positive number", free)
		}
		return
	}
	if ok {
		t.Errorf("freeSpace claims a figure (%d) on %s, where it cannot know one", free, runtime.GOOS)
	}
}
