// Package deploy turns the node state and the account list into a running
// deployment: it renders config.json, validates it with the core, restarts the
// service and records which accounts are live. The panel and the subscription
// service both go through it, so a change made in either one produces the same
// configuration.
package deploy

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/MinimaxFlora/EasySB/internal/cert"
	"github.com/MinimaxFlora/EasySB/internal/config"
	"github.com/MinimaxFlora/EasySB/internal/sbcore"
	"github.com/MinimaxFlora/EasySB/internal/service"
	"github.com/MinimaxFlora/EasySB/internal/state"
	"github.com/MinimaxFlora/EasySB/internal/sysinfo"
	"github.com/MinimaxFlora/EasySB/internal/user"
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
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		return nil, err
	}
	// WriteFile leaves the mode of a file that already exists alone, so a config.json
	// written by an earlier version stays world-readable until its mode is set here.
	if err := os.Chmod(configPath, 0o600); err != nil {
		return nil, err
	}
	return data, nil
}

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
	if _, err := WriteServerConfig(cfg, accounts); err != nil {
		return err
	}
	if err := sbcore.Check(ctx, configPath); err != nil {
		return ErrRejected
	}
	return service.Do(ctx, "restart")
}

// ApplyStore applies the accounts that may be live right now and records them,
// which is the single write path for a change made in the panel.
func ApplyStore(ctx context.Context, cfg state.Config, store *user.Store) error {
	now := time.Now()
	if err := Apply(ctx, cfg, store.Routable(now)); err != nil {
		return err
	}
	store.MarkApplied(now)
	return store.Save()
}
