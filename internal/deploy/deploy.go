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

// WriteServerConfig renders the configuration and writes it to
// /etc/sing-box/config.json. The document carries every account's credentials, so
// it is 0600, the same rule the account store follows. Only the core reads it, and
// the core runs as root.
func WriteServerConfig(cfg state.Config, nodes []node.Node, accounts []user.User) ([]byte, error) {
	data, err := ServerConfig(cfg, nodes, accounts)
	if err != nil {
		return nil, err
	}
	if err := writeConfigFile(data); err != nil {
		return nil, err
	}
	return data, nil
}

// writeConfigFile installs an already rendered document at configPath. The
// directory is created first, and the mode is forced on an existing file because
// WriteFile leaves it alone: a config.json an earlier version left world-readable
// stays world-readable otherwise, and this file carries every account's credentials.
func writeConfigFile(data []byte) error {
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(configPath, 0o600); err != nil {
		return err
	}
	return nil
}

// checkConfig is the acceptance test a deployment runs before restarting the node. It
// is a variable so that a test can drive the rejection path without having to render a
// document the engine refuses, which would otherwise depend on the build tags.
var checkConfig = sbcore.Check

// Apply writes the configuration for the given nodes and accounts, validates it
// and makes it live. A first accepted configuration installs, enables and starts
// the core unit; a later change restarts it. When the running core already serves
// the same document, nothing happens, so an edit the core cannot see does not
// drop every live connection.
func Apply(ctx context.Context, cfg state.Config, nodes []node.Node, accounts []user.User) error {
	if !anyEnabled(nodes) {
		return ErrNoNodes
	}
	if live, err := ServerConfig(cfg, nodes, accounts); err == nil && service.Active(ctx) && sameAsLive(live) {
		return nil
	}
	if _, err := ApplyConfig(ctx, cfg, nodes, accounts); err != nil {
		return err
	}
	if service.Active(ctx) {
		return service.Do(ctx, "restart")
	}
	// First accepted configuration: install the unit and start the core. Enabling
	// is best-effort, because a caller without permission to enable must still be
	// able to start it.
	if err := service.WriteUnit(); err != nil {
		return err
	}
	_ = service.Do(ctx, "enable")
	return service.Do(ctx, "start")
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
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(configDir, "config.json.check-*")
	if err != nil {
		return nil, err
	}
	// CreateTemp makes the file 0600, which is the mode the document lands with,
	// because the rename carries the mode rather than the umask.
	checkPath := tmp.Name()
	defer os.Remove(checkPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := checkConfig(ctx, checkPath); err != nil {
		// The core's message names the field it refused, and the only place a
		// rejection is reported is a log line. Returning the sentinel alone left an
		// operator with "the core rejected the generated configuration" and nothing
		// to act on.
		return nil, fmt.Errorf("%w: %w", ErrRejected, err)
	}
	if err := os.Rename(checkPath, configPath); err != nil {
		return nil, err
	}
	return data, nil
}

// ApplyStore applies the nodes and the accounts that may be live right now and
// records the latter, which is the single write path for a change made in the
// panel. Both stores are reloaded under their locks, so the set that is applied
// and marked as such is the one on disk now.
func ApplyStore(ctx context.Context, cfg state.Config, nodesPath, accountsPath string) error {
	nodes, err := LoadNodes(nodesPath)
	if err != nil {
		return err
	}
	store, lock, err := user.Locked(accountsPath)
	if err != nil {
		return err
	}
	defer lock.Unlock()
	now := time.Now()
	// A file that predates a credential field is repaired here, on a write path,
	// and the result is saved with the rest of this change.
	protocols := make(map[string]string, len(nodes))
	for _, n := range nodes {
		protocols[n.ID] = n.Protocol
	}
	store.Repair(protocols)
	if err := Apply(ctx, cfg, nodes, store.Routable(now)); err != nil {
		return err
	}
	store.MarkApplied(now)
	return store.Save()
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
