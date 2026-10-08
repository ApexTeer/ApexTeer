package node

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"

	"github.com/EasySBTeam/EasySB/internal/state"
	"github.com/EasySBTeam/EasySB/internal/user"
)

// Migrate upgrades a v5 single-node deployment to the explicit node model. It
// runs once, before anything reads the stores, and is idempotent:
//
//   - When the node file is absent and the state file carries the v5
//     per-protocol keys, one node is created per enabled protocol, carrying that
//     protocol's port and parameters.
//   - When the node file exists, node creation is skipped; only a still-v1
//     account file is upgraded.
//
// The returned bool reports whether the node file was created, which lets the
// caller log the migration exactly once.
func Migrate(cfg state.Config, nodesPath, accountsPath string) (bool, error) {
	if _, err := os.Stat(nodesPath); err == nil {
		// The node file already exists. A node store written by this version is
		// complete; the only thing that can still be pending is an account file a
		// v5 run left behind.
		return false, migrateAccounts(nodesPath, accountsPath)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}

	if !cfg.LegacyProtocols() {
		// Nothing to migrate: a fresh install has no per-protocol keys, and
		// minting the five defaults as nodes would invent a deployment.
		return false, nil
	}

	store, lock, err := Locked(nodesPath)
	if err != nil {
		return false, err
	}
	defer lock.Unlock()
	for _, n := range legacyNodes(cfg) {
		if err := store.Add(n); err != nil {
			return false, fmt.Errorf("migrate node %q: %w", n.Name, err)
		}
	}
	if err := migrateAccounts(nodesPath, accountsPath); err != nil {
		return false, err
	}
	return true, nil
}

// migrateAccounts upgrades a v1 account file using the protocol-to-node mapping
// of the node store. It is a no-op once the account file is v2 or absent.
func migrateAccounts(nodesPath, accountsPath string) error {
	store, err := Load(nodesPath)
	if err != nil {
		return err
	}
	_, err = user.MigrateV2(accountsPath, ProtocolIndex(store))
	return err
}

// ProtocolIndex maps a protocol key to the id of the first node that serves it,
// which is how a v1 account selection described by protocol is translated into
// a node selection. A protocol with several nodes is ambiguous, but a v1 file
// predates that possibility.
func ProtocolIndex(store *Store) map[string]string {
	out := map[string]string{}
	for _, n := range store.Nodes() {
		if _, ok := out[n.Protocol]; !ok {
			out[n.Protocol] = n.ID
		}
	}
	return out
}

// legacyNodes builds the nodes a v5 state file described, one per enabled
// protocol, in canonical protocol order.
func legacyNodes(cfg state.Config) []Node {
	var out []Node
	for _, key := range state.Keys {
		if !cfg.Enabled[key] {
			continue
		}
		port := legacyPort(cfg, key)
		params := map[string]string{}
		switch key {
		case state.ProtoHysteria2:
			params[ParamHopRange] = cfg.HopRange
		case state.ProtoVLESSReality:
			params[ParamRealitySNI] = cfg.RealitySNI
			params[ParamRealityPrivate] = cfg.RealityPriv
			params[ParamRealityPublic] = cfg.RealityPub
			params[ParamRealityShortID] = cfg.RealitySID
		}
		out = append(out, New(key, state.Labels[key], port, params))
	}
	return out
}

// legacyPort reads one protocol's listen port, falling back to the compiled
// default when the state file carries nothing usable.
func legacyPort(cfg state.Config, key string) int {
	for _, raw := range []string{cfg.Ports[key], state.DefaultPorts[key]} {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n < 65536 {
			return n
		}
	}
	return 0
}
