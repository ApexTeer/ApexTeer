package cmd

import (
	"fmt"
	"os"

	"github.com/EasySBTeam/EasySB/internal/node"
	"github.com/EasySBTeam/EasySB/internal/state"
	"github.com/EasySBTeam/EasySB/internal/sysinfo"
)

// migrateStores upgrades a v5 single-node deployment to the explicit node model
// before anything reads the stores. It is idempotent, and on an already migrated
// host it only completes a pending account-file schema upgrade.
func migrateStores() {
	created, err := node.Migrate(state.Load(), sysinfo.NodesFile, sysinfo.UsersFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "migrate: "+err.Error())
		os.Exit(1)
	}
	if created {
		fmt.Fprintln(os.Stderr, "migrated the v5 deployment to the node model")
	}
}
