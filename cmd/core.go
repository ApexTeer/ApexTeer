package cmd

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/EasySBTeam/EasySB/internal/sbcore"
	"github.com/EasySBTeam/EasySB/internal/sysinfo"
)

// runCoreCommand is the core the panel carries, exposed the way a service unit
// needs it: `core run` is the node (`ExecStart=… core run -c <config>`), `core
// check` validates a configuration without starting anything, and `core version`
// answers what this build carries.
func runCoreCommand(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: easysb core run|check|version [-c <config>]")
		os.Exit(2)
	}
	command := args[0]
	flags := flag.NewFlagSet("core "+command, flag.ExitOnError)
	configPath := flags.String("c", sysinfo.ConfigJSON, "配置文件 / configuration file")
	switch command {
	case "run", "check":
		if err := flags.Parse(args[1:]); err != nil {
			os.Exit(2)
		}
	case "version":
		fmt.Printf("EasySB %s\nsing-box %s\nbuild: %s\n", versionLine(), sbcore.Version(), coreCapability())
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown core command %q: use run, check or version\n", command)
		os.Exit(2)
	}

	if command == "check" {
		if err := sbcore.Check(context.Background(), *configPath); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("config ok: " + *configPath)
		return
	}

	// The node runs until the service manager stops it: SIGTERM ends the context
	// and the engine closes its listeners on the way out.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logf := func(line string) { fmt.Printf("%s %s\n", time.Now().Format(time.RFC3339), line) }
	logf("EasySB " + versionLine() + " · sing-box " + sbcore.Version() + " · " + coreCapability())
	logf("config: " + *configPath)
	if err := sbcore.Run(ctx, *configPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// coreCapability names the one build flag that changes what the panel can do.
func coreCapability() string {
	if sbcore.StatsCapable() {
		return "with_v2ray_api (per-account traffic counters)"
	}
	return "no v2ray api (usage cannot be counted)"
}
