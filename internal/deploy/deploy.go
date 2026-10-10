// Package deploy turns the node store and the account list into a running
// deployment: it renders config.json, validates it with the core, installs and
// starts the core on first use, restarts it on later changes, and records which
// accounts are live. The panel and the subscription service both go through it,
// so a change made in either one produces the same configuration.
package deploy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/EasySBTeam/EasySB/internal/cert"
	"github.com/EasySBTeam/EasySB/internal/config"
	"github.com/EasySBTeam/EasySB/internal/node"
	"github.com/EasySBTeam/EasySB/internal/sbcore"
	"github.com/EasySBTeam/EasySB/internal/service"
	"github.com/EasySBTeam/EasySB/internal/state"
	"github.com/EasySBTeam/EasySB/internal/sysinfo"
	"github.com/EasySBTeam/EasySB/internal/user"
)

// ErrRejected is returned when the core refuses the generated configuration.
var ErrRejected = errors.New("the core rejected the generated configuration")

// ErrNoStats is returned by callers that need counters from a build that cannot
// provide them.
var ErrNoStats = errors.New("this build of the panel carries no V2Ray API")

// ErrNoNodes is returned when nothing is enabled, so there is no inbound to
// serve. The caller reports it instead of writing a document the core refuses.
var ErrNoNodes = config.ErrNoNodes

// ErrNoRealityKey is returned when a Reality node carries no keypair. The node
// store generates one when the node is created; a hand-edited store that lost it
// is refused here rather than handed to a core that rejects the whole document.
var ErrNoRealityKey = errors.New("the Reality node needs a keypair, and none is recorded")

// ServerConfig renders the core configuration for a set of nodes and accounts.
// The listener set is exactly the enabled nodes, and each node authenticates
// only the accounts that selected it.
//
// Whether the document carries the experimental.v2ray_api block is asked of the
// build, not of the host: with_v2ray_api is a compile-time tag, and a core
// without it rejects a configuration naming the API whole.
func ServerConfig(cfg state.Config, nodes []node.Node, accounts []user.User) ([]byte, error) {
	if !anyEnabled(nodes) {
		return nil, ErrNoNodes
	}
	for _, n := range nodes {
		if n.Enabled && n.Protocol == state.ProtoVLESSReality && n.Param(node.ParamRealityPrivate) == "" {
			return nil, ErrNoRealityKey
		}
	}
	pair, err := cert.ResolveActive(cfg.Domain)
	if err != nil {
		return nil, err
	}
	return config.Build(config.Params{
		Nodes:         nodes,
		Members:       config.MembersFrom(accounts),
		CertFullchain: pair.Fullchain,
		CertKey:       pair.Key,
		Stats:         sbcore.StatsCapable(),
	})
}

// anyEnabled reports whether at least one node is enabled.
func anyEnabled(nodes []node.Node) bool {
	for _, n := range nodes {
		if n.Enabled {
			return true
		}
	}
	return false
}

// The document lands at these paths. They are variables so that a test can render a
// deployment into a temporary directory: what such a test is about is the document the
// core accepts and the mode it lands with, and neither belongs in /etc on a build host.
var (
	configDir  = sysinfo.WorkDir
	configPath = sysinfo.ConfigJSON
)

// checkConfig is the acceptance test a deployment runs before restarting the node. It
// is a variable so that a test can drive the rejection path without having to render a
// document the engine refuses, which would otherwise depend on the build tags.
var checkConfig = sbcore.Check

// The systemd operations this package performs. They are variables for the same
// reason checkConfig is: ApplyStore is the single write path for every change the
// panel, the TUI and --provision make, and it had no test at all, because reaching
// its success path means running systemctl on a host that has no service to start.
// Injecting them lets the load-render-install-record-save sequence be driven end to
// end in a temporary directory. Production never reassigns them.
var (
	serviceActive    = service.Active
	serviceDo        = service.Do
	serviceWriteUnit = service.WriteUnit
)

// Apply writes the configuration for the given nodes and accounts, validates it
// and makes it live. A first accepted configuration installs, enables and starts
// the core unit; a later change restarts it. When the running core already serves
// the same document, nothing happens, so an edit the core cannot see does not
// drop every live connection.
//
// An empty node set is not "nothing to do": it is the document the host must end
// up serving once the last node is removed, so the removed node stops answering.
// That teardown is Clear, and it is reached from here so the panel, the TUI and
// the subscription service all retire a listener the same way.
func Apply(ctx context.Context, cfg state.Config, nodes []node.Node, accounts []user.User) error {
	if !anyEnabled(nodes) {
		return Clear(ctx, cfg)
	}
	if live, err := ServerConfig(cfg, nodes, accounts); err == nil && serviceActive(ctx) && sameAsLive(live) {
		return nil
	}
	if _, err := ApplyConfig(ctx, cfg, nodes, accounts); err != nil {
		return err
	}
	if serviceActive(ctx) {
		return restartAfterChange(ctx)
	}
	// First accepted configuration: install the unit and start the core. Enabling
	// is best-effort, because a caller without permission to enable must still be
	// able to start it.
	if err := serviceWriteUnit(); err != nil {
		return err
	}
	_ = serviceDo(ctx, "enable")
	// A previous failure can latch systemd's start limit; clearing it lets the
	// start below proceed instead of failing with "start request repeated too quickly".
	_ = serviceDo(ctx, "reset-failed")
	return serviceDo(ctx, "start")
}

// Clear retires the running deployment after the last enabled node is deleted or
// disabled. Rewriting the document alone is not enough: the core keeps serving the
// inbounds it was started with, so a node the panel no longer lists would go on
// answering on its port with credentials that are no longer shown. The live
// document is replaced by one with no inbound and the core is stopped, which is
// what takes the listener down.
//
// A host that never had a deployment - the core is not running and no document is
// on disk - has nothing to retire, so an empty node set stays a no-op and
// ErrNoNodes is returned for the caller to report as such.
func Clear(ctx context.Context, cfg state.Config) error {
	active := serviceActive(ctx)
	if !active && !hasLiveConfig() {
		return ErrNoNodes
	}
	data, err := config.BuildEmpty()
	if err != nil {
		return err
	}
	if err := install(ctx, data); err != nil {
		return err
	}
	if !active {
		return nil
	}
	if err := serviceDo(ctx, "stop"); err != nil {
		return err
	}
	// The core exits non-zero on the stop signal, which leaves an intentional
	// teardown reading "failed" on the dashboard. Clear the latched state so the
	// unit shows the idle state the operator asked for.
	_ = serviceDo(ctx, "reset-failed")
	return nil
}

// hasLiveConfig reports whether a rendered document is present on disk.
func hasLiveConfig() bool {
	info, err := os.Stat(configPath)
	return err == nil && info.Size() > 0
}

// restartAfterChange applies a change to an already-running core. The generated
// document was already accepted by the core in ApplyConfig, so a failed restart is
// environmental: the previous instance can still hold the v2ray API port
// (127.0.0.1:10085), and several changes landing back to back can trip systemd's
// start limit, which latches the unit into a failed state. Clear the failed state
// and try once more before reporting the restart as a failure.
func restartAfterChange(ctx context.Context) error {
	err := serviceDo(ctx, "restart")
	if err == nil {
		return nil
	}
	_ = serviceDo(ctx, "reset-failed")
	select {
	case <-ctx.Done():
		return err
	case <-time.After(750 * time.Millisecond):
	}
	return serviceDo(ctx, "restart")
}

// sameAsLive reports whether data is exactly what the live config file holds.
func sameAsLive(data []byte) bool {
	current, err := os.ReadFile(configPath)
	if err != nil {
		return false
	}
	return bytes.Equal(current, data)
}

// ApplyConfig renders the configuration, has the core accept it and only then moves
// it to the live path. The core is shown a temporary file beside the live one, so a
// document it refuses leaves the configuration a running node is serving untouched,
// and the install is a rename within one directory. It returns the accepted document.
func ApplyConfig(ctx context.Context, cfg state.Config, nodes []node.Node, accounts []user.User) ([]byte, error) {
	data, err := ServerConfig(cfg, nodes, accounts)
	if err != nil {
		return nil, err
	}
	if err := install(ctx, data); err != nil {
		return nil, err
	}
	return data, nil
}

// install has the core accept data and, only then, moves it to the live path. The
// core is shown a temporary file beside the live one, so a document it refuses
// leaves the configuration a running node is serving untouched, and the install is
// a rename within one directory. CreateTemp makes the file 0600, which is the mode
// the document lands with, because the rename carries the mode rather than the
// umask.
func install(ctx context.Context, data []byte) error {
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(configDir, "config.json.check-*")
	if err != nil {
		return err
	}
	checkPath := tmp.Name()
	defer os.Remove(checkPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := checkConfig(ctx, checkPath); err != nil {
		// The core's message names the field it refused, and the only place a
		// rejection is reported is a log line. Returning the sentinel alone left an
		// operator with "the core rejected the generated configuration" and nothing
		// to act on.
		return fmt.Errorf("%w: %w", ErrRejected, err)
	}
	if err := os.Rename(checkPath, configPath); err != nil {
		return err
	}
	return nil
}

// ApplyStore applies the nodes and the accounts that may be live right now and
// records the latter, which is the single write path for a change made in the
// panel. Both stores are reloaded under their own locks, so the set that is
// applied and marked as such is the one on disk now.
//
// The order of the four steps is deliberate, because each of them was a way for
// two concurrent changes to disagree:
//
//  1. The node store is read under its lock. Reading it outside any lock - which
//     is what this used to do, before taking the account lock seventeen lines
//     later - let a node the panel had just added be missing from the document the
//     core was restarted with, while easysb-nodes.json still listed it as enabled
//     and the subscription document still advertised it.
//  2. The account store is loaded under its lock, repaired and SAVED, and the
//     routable set is derived from that saved state. The repair generates a
//     credential for an account that has none, and the core is about to
//     authenticate with it: saving first is what keeps the running core's
//     credentials from living only in this process's memory. A crash after the
//     next step used to leave the core accepting a uuid that was nowhere on disk,
//     and the next repair would mint a different one and break every imported
//     client.
//  3. The deploy runs OUTSIDE both locks. It renders, has the core accept the
//     document, and restarts the service - the slowest work in the program, with
//     no timeout on the systemctl calls. Holding a store lock across it meant a
//     wedged restart blocked every other writer, and the panel's own write mutex
//     with it.
//  4. The live set is recorded under the account lock again, so the flag lands on
//     whatever the store holds now rather than on a copy that a concurrent write
//     has since replaced.
func ApplyStore(ctx context.Context, cfg state.Config, nodesPath, accountsPath string) error {
	// 1. The nodes the document will be built from.
	var nodes []node.Node
	if err := withNodeLock(nodesPath, func(store *node.Store) error {
		nodes = store.Nodes()
		return nil
	}); err != nil {
		return err
	}
	// 2. Repair and persist the account credentials, and take the routable set from
	// the state that was just written.
	var accounts []user.User
	if err := withUserLock(accountsPath, func(store *user.Store) error {
		now := time.Now()
		protocols := make(map[string]string, len(nodes))
		for _, n := range nodes {
			protocols[n.ID] = n.Protocol
		}
		store.Repair(protocols)
		if err := store.Save(); err != nil {
			return err
		}
		accounts = store.Routable(now)
		return nil
	}); err != nil {
		return err
	}

	// 3. Deploy, holding no lock.
	if err := Apply(ctx, cfg, nodes, accounts); err != nil {
		return err
	}

	// 4. Record which accounts are live, against the store as it is now.
	return withUserLock(accountsPath, func(store *user.Store) error {
		store.MarkApplied(time.Now())
		return store.Save()
	})
}

// withNodeLock runs fn against the node store with the store's file lock held, so a
// read-modify-write cannot lose a concurrent change and a read cannot observe a
// store another process is part way through replacing.
//
// An empty path means "this host has no node store", which LoadNodes has always
// treated as no nodes rather than as an error. It is handled here too, because
// locking an empty path would create a sidecar beside the working directory.
func withNodeLock(path string, fn func(*node.Store) error) error {
	if path == "" {
		return fn(node.Empty())
	}
	store, lock, err := node.Locked(path)
	if err != nil {
		return err
	}
	defer lock.Unlock()
	return fn(store)
}

// withUserLock is the account store counterpart of withNodeLock.
func withUserLock(path string, fn func(*user.Store) error) error {
	store, lock, err := user.Locked(path)
	if err != nil {
		return err
	}
	defer lock.Unlock()
	return fn(store)
}

// LoadNodes loads the node store and returns every node. A caller that renders
// filters enabled nodes; an empty store is not an error here.
func LoadNodes(path string) ([]node.Node, error) {
	if path == "" {
		return nil, nil
	}
	store, err := node.Load(path)
	if err != nil {
		return nil, err
	}
	return store.Nodes(), nil
}
