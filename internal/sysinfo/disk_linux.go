//go:build linux

package sysinfo

import (
	"os"
	"syscall"
)

// diskUsage returns the total and available bytes of the filesystem holding
// path.
func diskUsage(path string) (uint64, uint64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0
	}
	return st.Blocks * uint64(st.Bsize), st.Bavail * uint64(st.Bsize)
}

// DiskTotals returns the cumulative bytes read from and written to the host's
// whole block devices since boot, read from /proc/diskstats. A missing file
// reports zero.
func DiskTotals() (uint64, uint64) {
	data, err := os.ReadFile("/proc/diskstats")
	if err != nil {
		return 0, 0
	}
	return parseDiskStats(data)
}
