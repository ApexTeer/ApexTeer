//go:build linux

package sysinfo

import "os"

// NetworkTotals returns the cumulative received and sent bytes across every
// non-loopback interface, read from /proc/net/dev. A missing file reports zero.
func NetworkTotals() (uint64, uint64) {
	data, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return 0, 0
	}
	return parseNetDev(data)
}
