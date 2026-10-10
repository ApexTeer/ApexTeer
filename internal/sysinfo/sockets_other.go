//go:build !linux

package sysinfo

// SocketCounts has no portable implementation. The stub keeps the packages that
// embed sysinfo buildable on a non-Linux development host.
func SocketCounts() (tcp, udp int) {
	return 0, 0
}
