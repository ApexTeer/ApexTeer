package bench

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/EasySBTeam/EasySB/internal/toolbox"
)

// mountInfoFixture is a trimmed /proc/self/mountinfo: a container root, a tmpfs, two real
// partitions of the same and of different disks, a read-only EFI partition and a path with
// a space, which the kernel escapes as \040.
const mountInfoFixture = `24 30 0:23 / /proc rw,nosuid,nodev,noexec,relatime shared:5 - proc proc rw
30 1 254:1 / / rw,relatime shared:1 - ext4 /dev/vda1 rw,errors=remount-ro
36 30 254:2 / /boot rw,relatime shared:6 - ext4 /dev/vda2 rw
40 30 0:32 / /run/user/0 rw,nosuid,nodev,relatime shared:20 - tmpfs tmpfs rw,size=102400k
50 30 8:1 / /mnt/data rw,relatime shared:30 - ext4 /dev/sda1 rw
55 30 8:2 / /mnt/efi ro,relatime shared:31 - vfat /dev/sdb1 rw,fmask=0077
60 30 0:41 / /var/lib/docker/overlay2/abc/merged rw,relatime shared:40 - overlay overlay rw,lowerdir=/a:/b
65 30 8:3 / /mnt/with\040space rw,relatime shared:50 - xfs /dev/sdc1 rw
`

// mountTableFixture is a trimmed /proc/mounts, the older format: no device numbers and a
// different field order.
const mountTableFixture = `/dev/vda1 / ext4 rw,relatime 0 0
tmpfs /tmp tmpfs rw,nosuid,nodev 0 0
overlay /var/lib/docker/overlay2/xyz/merged overlay rw,relatime,lowerdir=/a:/b 0 0
/dev/vdb1 /mnt/backup xfs rw,relatime 0 0
`

func TestParseMountsMountinfo(t *testing.T) {
	mounts := ParseMounts([]byte(mountInfoFixture))
	if len(mounts) != 8 {
		t.Fatalf("parsed %d mounts, want 8: %+v", len(mounts), mounts)
	}
	root := mounts[1]
	if root.Point != "/" || root.Device != "/dev/vda1" || root.FSType != "ext4" {
		t.Errorf("root mount = %+v, want / on /dev/vda1 (ext4)", root)
	}
	if root.Major != 254 || root.Minor != 1 {
		t.Errorf("root device numbers = %d:%d, want 254:1", root.Major, root.Minor)
	}
	if len(root.Options) == 0 || root.Options[0] != "rw" {
		t.Errorf("root options = %v, want the per-mount list starting with rw", root.Options)
	}
	if mounts[3].FSType != "tmpfs" || mounts[3].Device != "tmpfs" {
		t.Errorf("the tmpfs entry = %+v, want the pseudo device named after its type", mounts[3])
	}
	if mounts[6].FSType != "overlay" || mounts[6].Device != "overlay" {
		t.Errorf("the overlay entry = %+v", mounts[6])
	}
	if got := mounts[7].Point; got != "/mnt/with space" {
		t.Errorf("escaped mount point = %q, want the space unescaped", got)
	}
	if !mounts[5].ReadOnly() {
		t.Errorf("EFI mount %+v should report read-only", mounts[5])
	}
	if root.ReadOnly() {
		t.Errorf("root mount %+v should not report read-only", root)
	}
}

func TestParseMountsTable(t *testing.T) {
	mounts := ParseMounts([]byte(mountTableFixture))
	if len(mounts) != 4 {
		t.Fatalf("parsed %d mounts, want 4: %+v", len(mounts), mounts)
	}
	if mounts[0].Device != "/dev/vda1" || mounts[0].Point != "/" || mounts[0].FSType != "ext4" {
		t.Errorf("first mount = %+v", mounts[0])
	}
	if mounts[0].Major != 0 || mounts[0].Minor != 0 {
		t.Errorf("/proc/mounts carries no device numbers, got %d:%d", mounts[0].Major, mounts[0].Minor)
	}
	if got := strings.Join(mounts[3].Options, ","); got != "rw,relatime" {
		t.Errorf("options = %q, want rw,relatime", got)
	}
}

// TestParseMountsMixedFormats is the guard for the per-line format detection: a caller that
// concatenated the two files, or a line that is neither, must not stop the parse.
func TestParseMountsMixedFormats(t *testing.T) {
	doc := "30 1 254:1 / / rw,relatime shared:1 - ext4 /dev/vda1 rw\n" +
		"/dev/vdb1 /mnt/backup xfs rw,relatime 0 0\n" +
		"\n" +
		"garbage\n"
	mounts := ParseMounts([]byte(doc))
	if len(mounts) != 2 {
		t.Fatalf("parsed %d mounts, want 2: %+v", len(mounts), mounts)
	}
	if mounts[0].Device != "/dev/vda1" || mounts[1].Device != "/dev/vdb1" {
		t.Errorf("mounts = %+v, want the mountinfo line then the mounts line", mounts)
	}
}

func TestUnescapeMount(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/mnt/plain", "/mnt/plain"},
		{`/mnt/with\040space`, "/mnt/with space"},
		{`/mnt/with\011tab`, "/mnt/with	tab"},
		{`/mnt/back\134slash`, `/mnt/back\slash`},
		{`/mnt/trailing\`, `/mnt/trailing\`},
		{`/mnt/bad\9zz`, `/mnt/bad\9zz`},
	}
	for _, c := range cases {
		if got := unescapeMount(c.in); got != c.want {
			t.Errorf("unescapeMount(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestClassify(t *testing.T) {
	ext4 := func(dev, point string, opts ...string) Mount {
		return Mount{Point: point, Device: dev, FSType: "ext4", Options: append([]string{"rw"}, opts...)}
	}
	cases := []struct {
		name         string
		mounts       []Mount
		wantTestable []string
		wantReasons  map[string]string // mount point -> substring of the reason
	}{
		{
			name:         "a real partition is testable",
			mounts:       []Mount{ext4("/dev/vda1", "/")},
			wantTestable: []string{"/"},
		},
		{
			name: "container and kernel file systems are skipped",
			mounts: []Mount{
				{Point: "/", Device: "overlay", FSType: "overlay", Options: []string{"rw"}},
				{Point: "/tmp", Device: "tmpfs", FSType: "tmpfs", Options: []string{"rw"}},
				{Point: "/proc", Device: "proc", FSType: "proc", Options: []string{"rw"}},
				{Point: "/run/lock", Device: "tmpfs", FSType: "tmpfs", Options: []string{"rw"}},
			},
			wantReasons: map[string]string{
				"/":         "container overlay",
				"/tmp":      "memory-backed",
				"/proc":     "kernel interface",
				"/run/lock": "memory-backed",
			},
		},
		{
			name: "a read-only mount is skipped",
			mounts: []Mount{
				{Point: "/", Device: "/dev/vda1", FSType: "ext4", Options: []string{"ro", "relatime"}},
			},
			wantReasons: map[string]string{"/": "read-only"},
		},
		{
			name: "one row per device",
			mounts: []Mount{
				ext4("/dev/vda1", "/"),
				ext4("/dev/vda1", "/boot"),
				ext4("/dev/vdb1", "/mnt/data"),
			},
			wantTestable: []string{"/", "/mnt/data"},
			wantReasons:  map[string]string{"/boot": "already measured at /"},
		},
		{
			name: "an EFI partition is left alone",
			mounts: []Mount{
				{Point: "/boot/efi", Device: "/dev/vda1", FSType: "vfat", Options: []string{"rw"}},
			},
			wantReasons: map[string]string{"/boot/efi": "EFI system partition"},
		},
		{
			name: "network and FUSE file systems are skipped",
			mounts: []Mount{
				{Point: "/mnt/nfs", Device: "/dev/vda1", FSType: "nfs4", Options: []string{"rw"}},
				{Point: "/mnt/ssh", Device: "user@host:/", FSType: "fuse.sshfs", Options: []string{"rw"}},
			},
			wantReasons: map[string]string{
				"/mnt/nfs": "network file system",
				"/mnt/ssh": "FUSE",
			},
		},
		{
			name: "a kernel path is never storage",
			mounts: []Mount{
				ext4("/dev/vda1", "/dev/shm/backing"),
			},
			wantReasons: map[string]string{"/dev/shm/backing": "kernel-owned path"},
		},
		{
			name: "the testable mounts are sorted by mount point",
			mounts: []Mount{
				ext4("/dev/vdc1", "/mnt/z"),
				ext4("/dev/vda1", "/"),
				ext4("/dev/vdb1", "/mnt/a"),
			},
			wantTestable: []string{"/", "/mnt/a", "/mnt/z"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			testable, skipped := Classify(c.mounts)
			var points []string
			for _, m := range testable {
				points = append(points, m.Point)
			}
			if strings.Join(points, ",") != strings.Join(c.wantTestable, ",") {
				t.Errorf("testable = %v, want %v", points, c.wantTestable)
			}
			byPoint := map[string]string{}
			for _, sk := range skipped {
				byPoint[sk.Mount.Point] = sk.Reason
			}
			if len(byPoint) != len(c.wantReasons) {
				t.Errorf("skipped %v, want %d entries with reasons", byPoint, len(c.wantReasons))
			}
			for point, want := range c.wantReasons {
				got, ok := byPoint[point]
				if !ok {
					t.Errorf("%s was not skipped, want reason %q", point, want)
					continue
				}
				if !strings.Contains(got, want) {
					t.Errorf("skip reason for %s = %q, want it to mention %q", point, got, want)
				}
			}
		})
	}
}

// TestRunDisksWithInjectedMounts is the per-disk pass against real directories: the mount
// table is injected (so this runs on a machine with no /proc) while the measurement itself
// is the real one, writing and reading files and deleting them.
func TestRunDisksWithInjectedMounts(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	source := func() ([]Mount, error) {
		return []Mount{
			{Point: first, Device: "/dev/vda1", FSType: "ext4", Options: []string{"rw"}},
			{Point: second, Device: "/dev/vdb1", FSType: "xfs", Options: []string{"rw"}},
			{Point: first, Device: "/dev/vda1", FSType: "ext4", Options: []string{"rw"}},
			{Point: "/tmp", Device: "tmpfs", FSType: "tmpfs", Options: []string{"rw"}},
		}, nil
	}
	res, err := RunDisksWith(context.Background(), toolbox.Options{}, smallScale(), source)
	if err != nil {
		t.Fatalf("RunDisksWith: %v", err)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("got %d rows, want one per device: %v", len(res.Rows), res.Rows)
	}
	for _, row := range res.Rows {
		if len(row) != 4 {
			t.Fatalf("row %v has %d cells, want 4", row, len(row))
		}
		for i, cell := range row[2:] {
			v, unit := scoreOf(t, cell)
			if v <= 0 {
				t.Errorf("%s %s = %v, want a positive measurement", row[0], row[2+i], v)
			}
			if unit != unitMBps {
				t.Errorf("%s unit = %q, want %q", row[0], unit, unitMBps)
			}
		}
		if !strings.HasPrefix(row[1], "/dev/") {
			t.Errorf("device column = %q, want the block device", row[1])
		}
	}
	if !notesContain(res, "skipped /tmp (tmpfs") {
		t.Errorf("notes must name the skipped mount and why: %v", res.Notes)
	}
	if !notesContain(res, "already measured at "+first) {
		t.Errorf("notes must explain the second mount of the same device: %v", res.Notes)
	}
	if !notesContain(res, "Nothing is left behind") {
		t.Errorf("notes must make the cleanup promise: %v", res.Notes)
	}
	if !strings.Contains(res.Summary, "2 disks measured") {
		t.Errorf("summary %q must count the disks", res.Summary)
	}
	for _, dir := range []string{first, second} {
		path := filepath.Join(dir, disksFileName)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("the test file %s was left behind (stat err %v)", path, err)
		}
	}
}

// TestRunDisksWithNoBlockDevice is the container case: every mount is memory or overlay, so
// there is nothing to measure. The result must say that rather than show zeros.
func TestRunDisksWithNoBlockDevice(t *testing.T) {
	res, err := RunDisksWith(context.Background(), toolbox.Options{}, smallScale(), func() ([]Mount, error) {
		return []Mount{
			{Point: "/", Device: "overlay", FSType: "overlay", Options: []string{"rw"}},
			{Point: "/tmp", Device: "tmpfs", FSType: "tmpfs", Options: []string{"rw"}},
			{Point: "/proc", Device: "proc", FSType: "proc", Options: []string{"rw"}},
		}, nil
	})
	if err != nil {
		t.Fatalf("RunDisksWith: %v", err)
	}
	if len(res.Rows) != 0 {
		t.Errorf("got %d rows, want none: %v", len(res.Rows), res.Rows)
	}
	if !notesContain(res, "container") {
		t.Errorf("notes must explain what skipping everything means: %v", res.Notes)
	}
	if !strings.Contains(res.Summary, "no writable block device") {
		t.Errorf("summary %q must say there was nothing to measure", res.Summary)
	}
}

// TestRunDisksWithWriteFailure checks the per-mount failure path: one unwritable mount must
// not abort the others, and the row has to say which one failed.
func TestRunDisksWithWriteFailure(t *testing.T) {
	good := t.TempDir()
	// A file standing in for the mount point makes the test file impossible to create.
	bad := filepath.Join(t.TempDir(), "mountpoint")
	if err := os.WriteFile(bad, []byte("x"), 0o644); err != nil {
		t.Fatalf("preparing the unwritable mount point: %v", err)
	}
	res, err := RunDisksWith(context.Background(), toolbox.Options{}, smallScale(), func() ([]Mount, error) {
		return []Mount{
			{Point: bad, Device: "/dev/vda1", FSType: "ext4", Options: []string{"rw"}},
			{Point: good, Device: "/dev/vdb1", FSType: "ext4", Options: []string{"rw"}},
		}, nil
	})
	if err != nil {
		t.Fatalf("RunDisksWith: %v", err)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("got %d rows, want one per device: %v", len(res.Rows), res.Rows)
	}
	byPoint := map[string][]string{}
	for _, row := range res.Rows {
		byPoint[row[0]] = row
	}
	if byPoint[bad][2] != "failed" {
		t.Errorf("row for the unwritable mount = %v, want a failed write", byPoint[bad])
	}
	if byPoint[good][2] == "failed" {
		t.Errorf("the writable mount was reported as failed: %v", byPoint[good])
	}
	if !notesContain(res, "could not be measured") {
		t.Errorf("notes must explain the failure: %v", res.Notes)
	}
}

func TestRunDisksSourceError(t *testing.T) {
	_, err := RunDisksWith(context.Background(), toolbox.Options{}, smallScale(), func() ([]Mount, error) {
		return nil, errors.New("no mount table")
	})
	if err == nil {
		t.Fatal("a failing mount source was ignored")
	}
}

// TestMountsWithoutProc is the Windows and macOS path: the panel must report that it cannot
// list disks, not return an empty table that reads like "no disks".
func TestMountsWithoutProc(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("this host has /proc; the failure path is exercised by TestRunDisksSourceError")
	}
	_, err := Mounts()
	if err == nil {
		t.Fatal("Mounts() returned a mount table on a platform with no /proc")
	}
	if !strings.Contains(err.Error(), mountInfoPath) || !strings.Contains(err.Error(), mountTablePath) {
		t.Errorf("error %q must name both files it tried", err)
	}
}

// TestMountsReadsTheRealTable is the Linux end-to-end path: the host's own mountinfo has to
// parse, and the classifier must not offer a pseudo file system or a read-only mount as a
// disk to write to. It is skipped elsewhere, where there is no /proc to read.
func TestMountsReadsTheRealTable(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("no /proc outside Linux")
	}
	mounts, err := Mounts()
	if err != nil {
		t.Fatalf("Mounts: %v", err)
	}
	if len(mounts) == 0 {
		t.Fatal("the host's own mount table parsed to nothing")
	}
	var root bool
	for _, m := range mounts {
		if m.Point != "/" {
			continue
		}
		root = true
		if m.FSType == "" || m.Device == "" {
			t.Errorf("root mount parsed incomplete: %+v", m)
		}
	}
	if !root {
		t.Error("no root mount was parsed")
	}
	testable, skipped := Classify(mounts)
	if len(testable)+len(skipped) != len(mounts) {
		t.Errorf("Classify accounted for %d of %d mounts: every mount must be either measured or explained",
			len(testable)+len(skipped), len(mounts))
	}
	for _, m := range testable {
		if !strings.HasPrefix(m.Device, "/dev/") {
			t.Errorf("%s was offered as a disk but its device is %q", m.Point, m.Device)
		}
		if _, bad := skipFSTypes[m.FSType]; bad {
			t.Errorf("%s (%s) was offered as a disk", m.Point, m.FSType)
		}
		if m.ReadOnly() {
			t.Errorf("%s is mounted read-only and was still offered as a disk", m.Point)
		}
	}
	t.Logf("%d mounts: %d testable, %d skipped", len(mounts), len(testable), len(skipped))
	for _, sk := range skipped {
		t.Logf("  skipped %s (%s, %s): %s", sk.Mount.Point, sk.Mount.Device, sk.Mount.FSType, sk.Reason)
	}
}

// The per-disk smoke measurement has the same clock problem the memory pass has: a small file
// on a fast device is written and read back before the clock moves, and the reading used to
// come out as 0 MB/s. A clock that only advances every few calls pins the fix.
func TestRunDisksWithSurvivesACoarseClock(t *testing.T) {
	dir := t.TempDir()
	source := func() ([]Mount, error) {
		return []Mount{{Point: dir, Device: "/dev/vda1", FSType: "ext4", Options: []string{"rw"}}}, nil
	}
	calls := 0
	epoch := time.Now()
	clock := func() time.Time {
		calls++
		return epoch.Add(time.Duration(calls/3) * time.Millisecond)
	}
	res, err := RunDisksWith(context.Background(), toolbox.Options{Clock: clock}, smallScale(), source)
	if err != nil {
		t.Fatalf("RunDisksWith: %v", err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("got %d rows, want one: %v", len(res.Rows), res.Rows)
	}
	for i, cell := range res.Rows[0][2:] {
		v, unit := scoreOf(t, cell)
		if v <= 0 {
			t.Errorf("%s = %v under a coarse clock, want a positive rate", res.Headers[2+i], v)
		}
		if unit != unitMBps {
			t.Errorf("%s unit = %q, want %q", res.Headers[2+i], unit, unitMBps)
		}
	}
}
