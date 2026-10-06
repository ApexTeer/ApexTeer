// Package deploy turns the node state and the account list into a running
// deployment: it renders config.json, validates it with the core, restarts the
// service and records which accounts are live. The panel and the subscription
// service both go through it, so a change made in either one produces the same
// configuration.
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

// ErrNoRealityKey is returned when Reality is enabled but no keypair is recorded.
// The panel generates one during deployment; rendering without it would produce a
// document the core refuses as a whole.
var ErrNoRealityKey = errors.New("the Reality inbound needs a keypair, and none is recorded")

// ServerConfig renders the core configuration for a set of accounts. The
// listener set is exactly the given accounts, so a caller that leaves an account
// out also removes its credentials from the core.
//
// Whether the document carries the experimental.v2ray_api block is asked of the
// build, not of the host: with_v2ray_api is a compile-time tag, and a core
// without it rejects a configuration naming the API whole.
//
// The one thing the renderer refuses outright is a Reality inbound without a
// keypair: sing-box rejects the whole document over it ("invalidate private key"),
// so returning an error here names the missing piece instead of handing the
// service something it cannot start.
func ServerConfig(cfg state.Config, accounts []user.User) ([]byte, error) {
	if cfg.Enabled[state.ProtoVLESSReality] && cfg.RealityPriv == "" {
		return nil, ErrNoRealityKey
	}
	pair, err := cert.ResolveActive(cfg.Domain)
	if err != nil {
		return nil, err
	}
	params := config.ParamsFromState(cfg)
	params.CertFullchain = pair.Fullchain
	params.CertKey = pair.Key
	params.Members = config.MembersFrom(accounts)
	params.Stats = sbcore.StatsCapable()
	return config.Build(params)
}

// The document lands at these paths. They are variables so that a test can render a
// deployment into a temporary directory: what such a test is about is the document the
// core accepts and the mode it lands with, and neither belongs in /etc on a build host.
var (
	configDir  = sysinfo.WorkDir
	configPath = sysinfo.ConfigJSON
)

// WriteServerConfig renders the configuration and writes it to
// /etc/sing-box/config.json. The document carries every account's credentials — the
// uuid and password of each enabled protocol — so it is 0600, the same rule the account
// store follows. Only the core reads it, and the core runs as root.
func WriteServerConfig(cfg state.Config, accounts []user.User) ([]byte, error) {
	data, err := ServerConfig(cfg, accounts)
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

// Apply writes the configuration for the given accounts, validates it and
// restarts the core. A node that was never deployed is left alone: there is no
// certificate and no service to restart yet, and pre-created accounts must not
// fail the caller.
//
// There is no "is the core installed" gate any more: the core is this binary, so
// the only question left is whether the core accepts the document, which is
// answered by running the real engine over it.
func Apply(ctx context.Context, cfg state.Config, accounts []user.User) error {
	if !cfg.NodeDeployed {
		return nil
	}
	// The core config carries credentials, not quotas, remarks or expiry, so most
	// account edits render the very same document. Comparing first keeps such an
	// edit from restarting the core and dropping every live connection for a change
	// the core never sees.
	if live, err := ServerConfig(cfg, accounts); err == nil && service.Active(ctx) && sameAsLive(live) {
		return nil
	}
	if _, err := ApplyConfig(ctx, cfg, accounts); err != nil {
		return err
	}
	return service.Do(ctx, "restart")
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
func ApplyConfig(ctx context.Context, cfg state.Config, accounts []user.User) ([]byte, error) {
	data, err := ServerConfig(cfg, accounts)
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

// ApplyStore applies the accounts that may be live right now and records them,
// which is the single write path for a change made in the panel. The store is
// reloaded under the account lock, so the set that is applied and marked as such is
// the one on disk now, not a copy a caller read before another writer committed.
func ApplyStore(ctx context.Context, cfg state.Config, accountsPath string) error {
	store, lock, err := user.Locked(accountsPath)
	if err != nil {
		return err
	}
	defer lock.Unlock()
	now := time.Now()
	// A file that predates a credential field is repaired here, on a write path,
	// and the result is saved with the rest of this change.
	store.Repair()
	if err := Apply(ctx, cfg, store.Routable(now)); err != nil {
		return err
	}
	store.MarkApplied(now)
	return store.Save()
}
