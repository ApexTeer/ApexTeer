package cmd

import (
	"context"
	"fmt"
	"os"

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
