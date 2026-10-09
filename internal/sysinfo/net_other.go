//go:build !linux

package sysinfo

// NetworkTotals has no portable implementation. The stub keeps the packages
// that embed sysinfo buildable on a non-Linux development host; the panel
// simply reports no traffic figures there.
func NetworkTotals() (uint64, uint64) {
	return 0, 0
}
