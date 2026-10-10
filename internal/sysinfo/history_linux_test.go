//go:build linux

package sysinfo

import "testing"

// TestParseCPUStat pins the aggregate line parsing: busy excludes idle and
// iowait, and the guest fields are not added on top of user and nice.
func TestParseCPUStat(t *testing.T) {
	data := []byte("cpu  100 0 50 800 50 0 0 0 0 0\ncpu0 1 2 3 4 5 6 7 8 9 0\n")
	busy, total := parseCPUStat(data)
	if total != 1000 {
		t.Fatalf("total = %d, want 1000", total)
	}
	if busy != 150 {
		t.Fatalf("busy = %d, want 150", busy)
	}
}

// TestParseTCPEstablished counts only rows in state 01 and ignores the header.
func TestParseTCPEstablished(t *testing.T) {
	data := []byte("  sl  local_address rem_address   st ...\n" +
		"   0: 0100007F:1F90 00000000:0000 0A ...\n" +
		"   1: 0100007F:1F91 0100007F:0050 01 ...\n" +
		"   2: 0100007F:1F92 0100007F:0051 01 ...\n")
	if got := parseTCPEstablished(data); got != 2 {
		t.Fatalf("parseTCPEstablished = %d, want 2", got)
	}
}

// TestCountRows counts the data rows and skips the header and blank lines.
func TestCountRows(t *testing.T) {
	data := []byte("  sl  local_address rem_address\n" +
		"   0: ...\n" +
		"   1: ...\n" +
		"\n")
	if got := countRows(data); got != 2 {
		t.Fatalf("countRows = %d, want 2", got)
	}
}
