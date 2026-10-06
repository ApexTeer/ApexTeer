package bench

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/EasySB-Team/EasySB/internal/toolbox"
)

// disksFileName is the file the per-mount pass writes on each filesystem. It is named
// apart from the single-disk test so a run of one tool can never be mistaken for the
// other's leftovers.
const disksFileName = "easysb-bench-disks"

// mountInfoPath and mountTablePath are the two kernel files that list mounted
// filesystems. mountinfo is preferred: it carries the device's major:minor numbers and
// the file system type in a fixed layout, while /proc/mounts is the older format whose
// device names have to be read as paths.
const (
	mountInfoPath  = "/proc/self/mountinfo"
	mountTablePath = "/proc/mounts"
)

// Mount is one entry of the kernel's mount table, reduced to what the disk pass needs.
type Mount struct {
	// Point is where the filesystem is mounted.
	Point string
	// Device is what it is mounted from: /dev/vda1, or a pseudo device such as
	// "overlay" or "tmpfs".
	Device string
	// FSType is the file system name.
	FSType string
	// Options are the per-mount options, without the superblock options.
	Options []string
	// Major and Minor are the device numbers when the kernel reported them, zero when the
	// document did not (that is /proc/mounts).
	Major, Minor int
}

// ReadOnly reports whether the mount is mounted read-only, in which case writing a test
// file to it is not a measurement of anything.
func (m Mount) ReadOnly() bool {
	for _, o := range m.Options {
		if o == "ro" {
			return true
		}
	}
	return false
}

// Skipped is a mount the per-disk pass will not touch, and why.
type Skipped struct {
	Mount  Mount
	Reason string
}

// MountSource reads the kernel's mount table. The panel uses Mounts; a test injects its
// own reader so the rules can be exercised on a platform with no /proc.
type MountSource func() ([]Mount, error)

// RunDisks is the toolbox entry for the per-mount disk pass.
func RunDisks(ctx context.Context, opts toolbox.Options) (toolbox.Result, error) {
	return RunDisksWith(ctx, opts, Default(), Mounts)
}

// RunDisksWith runs a small sequential write/read and an fsync on every writable block
// device mount, one row per device. It is deliberately separate from RunDisk: it writes on
// every disk of the host, and an operator asks for that on purpose.
func RunDisksWith(ctx context.Context, opts toolbox.Options, s Scale, src MountSource) (toolbox.Result, error) {
	s = s.withDefaults()
	ctx, cancel := bound(ctx, opts)
	defer cancel()
	if err := checkCtx(ctx, "disks"); err != nil {
		return toolbox.Result{}, err
	}
	mounts, err := src()
	if err != nil {
		return toolbox.Result{}, err
	}
	testable, skipped := Classify(mounts)

	res := toolbox.Result{Headers: []string{"Mount", "Device", "Sequential write", "Sequential read"}}
	for _, m := range testable {
		if err := checkCtx(ctx, "disks"); err != nil {
			return toolbox.Result{}, err
		}
		write, read, werr := benchMount(ctx, opts, s, m)
		if werr != nil {
			res.Rows = append(res.Rows, []string{m.Point, m.Device, "failed", "-"})
			res.Note("%s (%s) could not be measured: %v", m.Point, m.Device, werr)
			continue
		}
		res.Rows = append(res.Rows, []string{m.Point, m.Device, rate(write, unitMBps), rate(read, unitMBps)})
	}

	for _, sk := range skipped {
		res.Note("skipped %s (%s, %s): %s", sk.Mount.Point, sk.Mount.Device, sk.Mount.FSType, sk.Reason)
	}
	total := len(mounts)
	res.Note("The mount table came from %s, falling back to %s where it is absent. %d of %d entries were skipped, listed above with the reason.",
		mountInfoPath, mountTablePath, len(skipped), total)
	if len(testable) == 0 {
		res.Note("No writable block device was found, which is what a container looks like from inside: the panel then measures the file system it was given, not a disk it cannot see.")
	}
	if len(testable) > 0 {
		res.Note("Each device was written with a %s file of %s and read straight back, with an fsync after the write. This is a smoke measurement of every disk, not the full test: use the disk tool on a scratch directory for a number worth quoting.",
			humanSize(int64(s.DiskMountBlock)), humanSize(s.DiskMountBytes))
	}
	res.Note("Nothing is left behind: every test file is deleted before this returns. Files are written to the mount points themselves, so the run needs write permission there - a failure is reported per row rather than aborting the rest.")
	res.Summary = fmt.Sprintf("%d disks measured, %d mounts skipped", len(testable), len(skipped))
	res.Board = fmt.Sprintf("%d 块盘 · 跳过 %d 个挂载点", len(testable), len(skipped))
	if len(testable) == 0 {
		res.Summary = fmt.Sprintf("no writable block device, %d mounts skipped", len(skipped))
	}
	return res, nil
}

// benchMount measures one mount point: a sequential write with fsync, then a read back.
// Both numbers are in MB/s.
func benchMount(ctx context.Context, opts toolbox.Options, s Scale, m Mount) (write, read float64, err error) {
	path := filepath.Join(m.Point, disksFileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, 0, fmt.Errorf("cannot create %s: %w", path, err)
	}
	defer func() {
		f.Close()
		os.Remove(path)
	}()

	size := s.DiskMountBytes
	block := make([]byte, s.DiskMountBlock)
	for i := range block {
		block[i] = byte(i*29 + 11)
	}

	opts.Logf("bench/disks: writing %s to %s", humanSize(size), path)
	// Both phases go through timedIO: on a fast device, or with the small size the tests use,
	// one pass can finish inside a single tick of the clock, and a zero-nanosecond reading
	// would be printed as "0 MB/s" — an artefact of the clock, not a measurement of the mount.
	writePass := func() (int64, error) {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return 0, fmt.Errorf("seek %s: %w", path, err)
		}
		var written int64
		for written < size {
			if err := checkCtx(ctx, "disks/write"); err != nil {
				return written, err
			}
			n := int64(len(block))
			if remaining := size - written; remaining < n {
				n = remaining
			}
			w, werr := f.Write(block[:n])
			written += int64(w)
			if werr != nil {
				return written, fmt.Errorf("writing %s failed after %s: %w", path, humanSize(written), werr)
			}
			if w == 0 {
				return written, fmt.Errorf("writing %s stalled at %s", path, humanSize(written))
			}
		}
		return written, nil
	}
	writeDur, writeBytes, err := timedIO(opts.Now, writePass)
	if err != nil {
		return 0, 0, err
	}
	written := size
	if err := f.Sync(); err != nil {
		return 0, 0, fmt.Errorf("fsync of %s failed: %w", path, err)
	}
	var sum = uint64(fnvOffset)
	readDur, readBytes, err := timedIO(opts.Now, func() (int64, error) {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return 0, fmt.Errorf("seek %s: %w", path, err)
		}
		var got int64
		for got < written {
			if err := checkCtx(ctx, "disks/read"); err != nil {
				return got, err
			}
			n := int64(len(block))
			if remaining := written - got; remaining < n {
				n = remaining
			}
			r, rerr := f.Read(block[:n])
			if r > 0 {
				got += int64(r)
				sum = foldBlock(sum, block[:r])
			}
			if rerr != nil {
				if errors.Is(rerr, io.EOF) {
					break
				}
				return got, fmt.Errorf("reading %s: %w", path, rerr)
			}
			if r == 0 {
				break
			}
		}
		if got < written {
			// A short read is a failure of this mount, not a throughput of zero: the panel
			// must not report "0 MB/s" as if it had measured something.
			return got, fmt.Errorf("reading %s returned %s of %s", path, humanSize(got), humanSize(written))
		}
		return got, nil
	})
	if err != nil {
		return 0, 0, err
	}
	opts.Logf("bench/disks: %s: %s read back, checksum %#x", m.Point, humanSize(readBytes), sum)
	return mibPerSec(writeBytes, writeDur), mibPerSec(readBytes, readDur), nil
}

// Mounts reads the kernel's mount table, preferring mountinfo. A platform without /proc
// gets an error naming both files, which is what the panel shows instead of a table of
// zeroes.
func Mounts() ([]Mount, error) {
	if data, err := os.ReadFile(mountInfoPath); err == nil {
		return ParseMounts(data), nil
	}
	data, err := os.ReadFile(mountTablePath)
	if err != nil {
		return nil, fmt.Errorf("bench: no mount table: neither %s nor %s could be read (%w); this host does not expose /proc", mountInfoPath, mountTablePath, err)
	}
	return ParseMounts(data), nil
}

// ParseMounts parses either /proc/self/mountinfo or /proc/mounts. The formats are told
// apart per line by their shape, so a caller can hand over whichever file it could read.
func ParseMounts(data []byte) []Mount {
	var out []Mount
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if m, ok := parseMountInfoLine(line); ok {
			out = append(out, m)
			continue
		}
		if m, ok := parseMountsLine(line); ok {
			out = append(out, m)
		}
	}
	return out
}

// parseMountInfoLine parses one /proc/self/mountinfo line:
//
//	36 35 98:0 /mnt /mnt rw,noatime master:1 - ext4 /dev/sda1 rw,errors=continue
//
// The fields before "-" are fixed by the kernel's documentation, so the separator is what
// makes this parse reliable; the source given after it may be "none" for a pseudo device.
func parseMountInfoLine(line string) (Mount, bool) {
	fields := strings.Fields(line)
	if len(fields) < 10 {
		return Mount{}, false
	}
	sep := -1
	for i, f := range fields {
		if f == "-" {
			sep = i
			break
		}
	}
	if sep < 0 || sep+2 >= len(fields) {
		return Mount{}, false
	}
	major, minor, ok := parseDeviceNumbers(fields[2])
	if !ok {
		return Mount{}, false
	}
	m := Mount{
		Point:   unescapeMount(fields[4]),
		FSType:  fields[sep+1],
		Device:  unescapeMount(fields[sep+2]),
		Options: strings.Split(fields[5], ","),
		Major:   major,
		Minor:   minor,
	}
	if m.Device == "" || m.Device == "none" {
		m.Device = m.FSType
	}
	if m.Point == "" || m.FSType == "" {
		return Mount{}, false
	}
	return m, true
}

// parseMountsLine parses one /proc/mounts line:
//
//	/dev/vda1 / ext4 rw,relatime 0 0
func parseMountsLine(line string) (Mount, bool) {
	fields := strings.Fields(line)
	if len(fields) < 3 {
		return Mount{}, false
	}
	m := Mount{
		Device: unescapeMount(fields[0]),
		Point:  unescapeMount(fields[1]),
		FSType: fields[2],
	}
	if len(fields) >= 4 {
		m.Options = strings.Split(fields[3], ",")
	}
	if m.Device == "" || m.Point == "" || m.FSType == "" {
		return Mount{}, false
	}
	return m, true
}

// parseDeviceNumbers reads the "major:minor" field of a mountinfo line.
func parseDeviceNumbers(s string) (major, minor int, ok bool) {
	left, right, found := strings.Cut(s, ":")
	if !found {
		return 0, 0, false
	}
	major, err := strconv.Atoi(left)
	if err != nil {
		return 0, 0, false
	}
	minor, err = strconv.Atoi(right)
	if err != nil {
		return 0, 0, false
	}
	return major, minor, true
}

// unescapeMount undoes the octal escapes the kernel writes into mount paths: a space or a
// tab in a path arrives as \040 or \011, and a path left escaped would name a directory
// that does not exist.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// skipFSTypes are the file systems a disk benchmark must not write to: memory, kernel
// interfaces, container snapshots and network file systems. Writing a test file to any of
// them measures something other than the host's disks - and on a container it is the only
// thing mounted, which is why the result explains the skip rather than failing.
var skipFSTypes = map[string]string{
	"proc":        "kernel interface, not storage",
	"sysfs":       "kernel interface, not storage",
	"devtmpfs":    "kernel device tree, not storage",
	"devpts":      "kernel interface, not storage",
	"cgroup":      "kernel interface, not storage",
	"cgroup2":     "kernel interface, not storage",
	"tmpfs":       "memory-backed file system, so a write measures RAM",
	"ramfs":       "memory-backed file system, so a write measures RAM",
	"overlay":     "container overlay, so a write measures the upper layer, not a disk",
	"squashfs":    "read-only image, usually a snap or a container layer",
	"erofs":       "read-only image, usually a container layer",
	"iso9660":     "read-only optical image",
	"nsfs":        "kernel namespace handle",
	"mqueue":      "kernel interface, not storage",
	"debugfs":     "kernel interface, not storage",
	"tracefs":     "kernel interface, not storage",
	"securityfs":  "kernel interface, not storage",
	"bpf":         "kernel interface, not storage",
	"pstore":      "kernel interface, not storage",
	"configfs":    "kernel interface, not storage",
	"fusectl":     "kernel interface, not storage",
	"efivarfs":    "firmware variables, not storage",
	"autofs":      "kernel automount, not storage",
	"binfmt_misc": "kernel interface, not storage",
	"hugetlbfs":   "memory-backed file system, so a write measures RAM",
	"nfs":         "network file system, so a write measures the network",
	"nfs4":        "network file system, so a write measures the network",
	"cifs":        "network file system, so a write measures the network",
	"smb3":        "network file system, so a write measures the network",
	"9p":          "network file system, so a write measures the network",
	"virtiofs":    "host-backed file system, so a write measures the hypervisor",
	"glusterfs":   "network file system, so a write measures the network",
	"ceph":        "network file system, so a write measures the network",
	"rbd":         "network block device, so a write measures the network",
	"afs":         "network file system, so a write measures the network",
	"vfat":        "FAT file system, usually the small EFI system partition",
	"msdos":       "FAT file system, usually the small EFI system partition",
	"exfat":       "removable-media file system",
	"nilfs2":      "log-structured file system, syncs make it unusable as a throughput test",
	"ecryptfs":    "stacked encryption layer, not a device",
}

// kernelPaths are mount points that are never storage, whatever their file system claims.
var kernelPaths = []string{"/proc", "/sys", "/dev", "/run"}

// Classify splits a mount table into the mounts worth writing to and the ones skipped,
// each with the reason. It is pure and it keeps the shallowest mount point of a device, so
// a disk mounted at both / and /boot is measured once - the per-device question is what an
// operator asks.
func Classify(mounts []Mount) (testable []Mount, skipped []Skipped) {
	seen := map[string]string{} // device -> the mount point already chosen for it
	for _, m := range mounts {
		if reason := skipReason(m, seen); reason != "" {
			skipped = append(skipped, Skipped{Mount: m, Reason: reason})
			continue
		}
		seen[m.Device] = m.Point
		testable = append(testable, m)
	}
	sort.Slice(testable, func(i, j int) bool { return testable[i].Point < testable[j].Point })
	return testable, skipped
}

// skipReason returns why m is not testable, or "" when it is. The file system type is
// asked first: "an overlay mounted from /dev/vda1" and "a tmpfs" both need the explanation
// that names the file system, while "not a block device" is the fallback for the pseudo
// devices the table does not know.
func skipReason(m Mount, seen map[string]string) string {
	if reason, ok := skipFSTypes[m.FSType]; ok {
		return reason
	}
	if strings.HasPrefix(m.FSType, "fuse.") {
		return "FUSE mount, which is another program, not a device"
	}
	if !strings.HasPrefix(m.Device, "/dev/") {
		// Anything the kernel did not mount from a device node is not a disk to write to.
		return fmt.Sprintf("%q is not a block device", m.Device)
	}
	for _, p := range kernelPaths {
		if m.Point == p || strings.HasPrefix(m.Point, p+"/") {
			return "kernel-owned path"
		}
	}
	if m.ReadOnly() {
		return "mounted read-only"
	}
	if point, ok := seen[m.Device]; ok {
		return fmt.Sprintf("already measured at %s", point)
	}
	return ""
}
