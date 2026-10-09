package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/EasySBTeam/EasySB/internal/config"
	"github.com/EasySBTeam/EasySB/internal/deploy"
	"github.com/EasySBTeam/EasySB/internal/node"
	"github.com/EasySBTeam/EasySB/internal/state"
	"github.com/EasySBTeam/EasySB/internal/stats"
	"github.com/EasySBTeam/EasySB/internal/subd"
	"github.com/EasySBTeam/EasySB/internal/sysinfo"
	"github.com/EasySBTeam/EasySB/internal/user"
)

// runSubscribeService serves the subscription endpoint and enforces the account
// policy. It backs the easysb service unit and is the only long-running mode of
// this binary.
func runSubscribeService() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logf := func(line string) { fmt.Printf("%s %s\n", time.Now().Format(time.RFC3339), line) }
	logf("EasySB subscription service " + versionLine())

	options := subd.Options{
		Version:      resolveVersion(),
		AccountsPath: sysinfo.UsersFile,
		NodesPath:    sysinfo.NodesFile,
		Dial:         func() (stats.Counter, error) { return stats.Dial(config.StatsListen) },
		Apply: func(ctx context.Context, cfg state.Config, nodes []node.Node, accounts []user.User) error {
			return deploy.Apply(ctx, cfg, nodes, accounts)
		},
		Log: logf,
	}
	if err := options.Run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
