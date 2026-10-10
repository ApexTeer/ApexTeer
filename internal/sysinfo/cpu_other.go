//go:build !linux

package sysinfo

// CPUJiffies has no portable implementation. The stub keeps the packages that
// embed sysinfo buildable on a non-Linux development host; the panel simply
// reports no CPU history there.
func CPUJiffies() (busy, total uint64) {
	return 0, 0
}
