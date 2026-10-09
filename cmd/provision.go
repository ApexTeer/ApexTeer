package cmd

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/EasySBTeam/EasySB/internal/provision"
)

// runProvision deploys a host from a manifest, printing each step and a summary
// of what was produced. It is the headless counterpart of the panel's node,
// domain and account screens, meant for a script (or a deploy skill) that has a
// document of the desired state and no terminal.
func runProvision(path string) {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "provision: "+err.Error())
		os.Exit(1)
	}
	spec, err := provision.Parse(data)
	if err != nil {
		fmt.Fprintln(os.Stderr, "provision: "+err.Error())
		os.Exit(1)
	}
	opt := provision.Default()
	opt.Log = func(line string) { fmt.Println(line) }
	res, err := provision.Run(context.Background(), spec, opt)
	if err != nil {
		fmt.Fprintln(os.Stderr, "provision: "+err.Error())
		os.Exit(1)
	}
	printProvisionResult(res)
}

// printProvisionResult writes the subscription URL of every account, which is
// what an operator needs to hand a client, plus the nodes that back them.
func printProvisionResult(res *provision.Result) {
	fmt.Println()
	fmt.Printf("deployed %d node(s), %d account(s)\n", len(res.Nodes), len(res.Accounts))
	for _, n := range res.Nodes {
		fmt.Printf("  node  %-18s %-14s :%d\n", n.Name, n.Protocol, n.Port)
	}
	for _, a := range res.Accounts {
		fmt.Printf("  user  %-18s %s\n", a.Name, a.URL)
	}
}
