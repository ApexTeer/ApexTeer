package cmd

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/EasySBTeam/EasySB/internal/firewall"
	"github.com/EasySBTeam/EasySB/internal/node"
	"github.com/EasySBTeam/EasySB/internal/state"
	"github.com/EasySBTeam/EasySB/internal/sysinfo"
)

// runApplyFirewall applies the port-hopping rules and exits. It backs the
// easysb-firewall boot unit.
func runApplyFirewall() {
	cfg := state.Load()
	log := func(line string) { fmt.Println(line) }
	nodes, err := node.Load(sysinfo.NodesFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := firewall.Apply(context.Background(), cfg, nodes.Nodes(), log); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := firewall.WriteUnit(nodes.Nodes()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// runPruneFirewall removes port-hopping redirects that nothing needs any more: rules
// left behind by a version that had no ledger, whose node was deleted before the rules
// could be tied to it. It backs --prune-firewall, an operator action rather than a boot
// step, because deciding that a redirect is unwanted is a judgement about this host and
// belongs to a person, not to a unit that runs unattended.
//
// It is deliberately narrow. A redirect is only removed when its destination range and
// target match the shape this program installs, the target is not a port any enabled
// node uses, and nothing is listening on that port. Anything else is left alone and
// reported, so a rule belonging to another service is never taken away.
func runPruneFirewall() {
	log := func(line string) { fmt.Println(line) }
	nodes, err := node.Load(sysinfo.NodesFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	removed, err := firewall.PruneOrphans(context.Background(), nodes.Nodes(), portServing, log)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if len(removed) == 0 {
		fmt.Println("no orphan port-hopping rules found")
	}
}

// portServing reports whether something on this host is listening on a local port,
// over TCP or UDP.
//
// It reads the kernel's own socket tables rather than dialling. Dialling cannot answer
// this question for UDP: a UDP "connection" is only a recorded peer, so DialTimeout
// succeeds whether or not anything is bound, and an earlier version of this function
// therefore reported every port as served and pruned nothing at all. A local port is
// the last field of each row in /proc/net/{tcp,udp}, in hex.
func portServing(port string) bool {
	want, err := strconv.Atoi(port)
	if err != nil || want < 1 || want > 65535 {
		return false
	}
	needle := fmt.Sprintf(":%04X", want)
	for _, f := range []string{"/proc/net/tcp", "/proc/net/udp", "/proc/net/tcp6", "/proc/net/udp6"} {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for i, line := range strings.Split(string(data), "\n") {
			if i == 0 { // header
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 4 {
				continue
			}
			// fields[1] is local_address as HEXIP:HEXPORT; the last field is the state.
			// 0A is TCP_LISTEN. A UDP socket has no listen state, so a bound one is
			// reported as 07 (CLOSE) and that is the only state worth counting.
			if !strings.HasSuffix(fields[1], needle) {
				continue
			}
			if strings.HasPrefix(f, "/proc/net/tcp") {
				if fields[3] == "0A" {
					return true
				}
				continue
			}
			return true
		}
	}
	return false
}
