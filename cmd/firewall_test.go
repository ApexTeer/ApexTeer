package cmd

import (
	"net"
	"os"
	"runtime"
	"strconv"
	"testing"
)

// TestPortServingSeesARealListener is the regression for a real defect: the first
// version of this check dialled the port over TCP and UDP, and a UDP dial always
// succeeds because a UDP "connection" is only a recorded peer. Every port therefore
// looked served, --prune-firewall removed nothing, and it reported "no orphan" on a
// host that had one.
func TestPortServingSeesARealListener(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the socket tables this reads are /proc/net")
	}
	if _, err := os.Stat("/proc/net/udp"); err != nil {
		t.Skip("/proc/net is not available")
	}

	// A port nothing can be bound to: outside 1-65535 is rejected outright.
	if portServing("0") {
		t.Error("port 0 reported as served")
	}
	if portServing("not-a-port") {
		t.Error("a non-numeric port reported as served")
	}

	// A UDP listener on a free port must be seen. This is the case the old
	// implementation got wrong in the opposite direction: it reported everything, so a
	// false positive here would not have caught the bug, but a false negative would
	// make the prune delete a rule it should keep.
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot bind a UDP socket: %v", err)
	}
	defer conn.Close()
	port := strconv.Itoa(conn.LocalAddr().(*net.UDPAddr).Port)
	if !portServing(port) {
		t.Errorf("a bound UDP port (%s) was not reported as served: the prune would "+
			"delete a rule whose target is alive", port)
	}

	// And a TCP listener likewise.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot bind a TCP socket: %v", err)
	}
	defer ln.Close()
	tport := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	if !portServing(tport) {
		t.Errorf("a listening TCP port (%s) was not reported as served", tport)
	}
}

// TestPortServingIsFalseForAnUnusedPort is the half that matters for the orphan this
// change exists for: 8101 has no listener, so its redirect is prunable.
func TestPortServingIsFalseForAnUnusedPort(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the socket tables this reads are /proc/net")
	}
	// Bind a port to learn one that is definitely free, then close it.
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot bind: %v", err)
	}
	port := strconv.Itoa(conn.LocalAddr().(*net.UDPAddr).Port)
	conn.Close()

	if portServing(port) {
		t.Errorf("port %s is not bound by anything but was reported as served: "+
			"an orphan redirect to it would never be pruned", port)
	}
}
