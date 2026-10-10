//go:build linux

package sysinfo

import (
	"os"
	"strings"
)

// SocketCounts returns the host's established TCP sockets and its UDP sockets,
// read from /proc/net. Established TCP excludes listeners and half-open
// connections; UDP has no connection state, so every bound UDP socket is
// counted. A missing file contributes zero.
func SocketCounts() (tcp, udp int) {
	for _, name := range []string{"tcp", "tcp6"} {
		if data, err := os.ReadFile("/proc/net/" + name); err == nil {
			tcp += parseTCPEstablished(data)
		}
	}
	for _, name := range []string{"udp", "udp6"} {
		if data, err := os.ReadFile("/proc/net/" + name); err == nil {
			udp += countRows(data)
		}
	}
	return tcp, udp
}

// parseTCPEstablished counts the rows of a /proc/net/tcp table whose state
// column is 01 (TCP_ESTABLISHED). The first line is the column header.
func parseTCPEstablished(data []byte) int {
	count := 0
	for i, line := range strings.Split(string(data), "\n") {
		if i == 0 {
			continue
		}
		fields := strings.Fields(line)
		// local_address, rem_address, st: the state is the fourth column.
		if len(fields) >= 4 && fields[3] == "01" {
			count++
		}
	}
	return count
}

// countRows counts the data rows of a /proc/net table, skipping the header.
func countRows(data []byte) int {
	count := 0
	for i, line := range strings.Split(string(data), "\n") {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue
		}
		count++
	}
	return count
}
