// Package node stores the protocol inbounds EasySB deploys. v5 replaces the one
// implicit node (the five enable flags and their ports in easysb.conf) with an
// explicit list: one node is one protocol inbound with its own port and its own
// protocol parameters. Accounts select nodes, so this package is the single
// source of truth for what the host serves.
package node

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/EasySBTeam/EasySB/internal/atomicfile"
	"github.com/EasySBTeam/EasySB/internal/secret"
	"github.com/EasySBTeam/EasySB/internal/state"
)

// CurrentVersion is the on-disk format version of the node file.
const CurrentVersion = 1

// Parameter keys carried per node. Only the protocols that need an extra value
// use one; the other protocols authenticate with the account credential alone.
const (
	ParamRealitySNI     = "reality_sni"
	ParamRealityPrivate = "reality_private"
	ParamRealityPublic  = "reality_public"
	ParamRealityShortID = "reality_short_id"
	ParamHopRange       = "hop_range"
)

// Node is one protocol inbound: exactly one protocol, one listen port and the
// parameters that protocol needs. ID is random and stable so a rename never
// breaks an account selection or a stored credential.
type Node struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Protocol  string            `json:"protocol"`
	Port      int               `json:"port"`
	Enabled   bool              `json:"enabled"`
	Params    map[string]string `json:"params,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
}

// Param returns one stored parameter, or the empty string.
func (n Node) Param(key string) string {
	return n.Params[key]
}

// CoreName is the core user name for one account on one node. The core reports
// counters keyed by this name, so it is what makes per-node accounting possible;
// neither a token nor a node id contains '@', so the pair can be split back.
func CoreName(token, nodeID string) string {
	return token + "@" + nodeID
}

// SplitCoreName reverses CoreName.
func SplitCoreName(name string) (token, nodeID string, ok bool) {
	i := strings.IndexByte(name, '@')
	if i <= 0 || i == len(name)-1 {
		return "", "", false
	}
	return name[:i], name[i+1:], true
}

// New builds a node with a fresh id and fills in the parameters its protocol
// needs, so a node always carries a complete parameter set.
func New(protocol, name string, port int, params map[string]string) Node {
	n := Node{
		ID:        secret.Token(),
		Name:      strings.TrimSpace(name),
		Protocol:  protocol,
		Port:      port,
		Enabled:   true,
		Params:    map[string]string{},
		CreatedAt: time.Now().UTC(),
	}
	for k, v := range params {
		n.Params[k] = v
	}
	n.EnsureParams()
	return n
}

// EnsureParams fills in the protocol parameters a node is missing: a Reality
// node gets a keypair, a short id and the default server name; a Hysteria2 node
// gets the default hopping range.
func (n *Node) EnsureParams() {
	if n.Params == nil {
		n.Params = map[string]string{}
	}
	switch n.Protocol {
	case state.ProtoVLESSReality:
		if n.Params[ParamRealitySNI] == "" {
			n.Params[ParamRealitySNI] = state.DefaultSNI
		}
		if n.Params[ParamRealityPrivate] == "" || n.Params[ParamRealityPublic] == "" {
			priv, pub := secret.RealityKeypair()
			if n.Params[ParamRealityPrivate] == "" {
				n.Params[ParamRealityPrivate] = priv
			}
			if n.Params[ParamRealityPublic] == "" {
				n.Params[ParamRealityPublic] = pub
			}
		}
		if n.Params[ParamRealityShortID] == "" {
			n.Params[ParamRealityShortID] = secret.ShortID()
		}
	case state.ProtoHysteria2:
		if n.Params[ParamHopRange] == "" {
			n.Params[ParamHopRange] = state.DefaultHopRange
		}
	}
}

// fileFormat is the on-disk node document.
type fileFormat struct {
	Version int    `json:"version"`
	Nodes   []Node `json:"nodes"`
}

// Store is the node file. Like the account store it is a JSON document loaded
// whole: the panel edits a handful of nodes, and a file that stays inspectable
// beats a database with a schema to migrate.
type Store struct {
	path  string
	nodes []Node
}

// Load reads the node file. A missing or empty file yields an empty store, which
// is what a fresh deployment starts from.
//
// A file that cannot be parsed is quarantined and the previous content is restored
// from the sibling backup. The node store is the only record of what the host
// serves: the ports, the protocols and the Reality keypair are not derivable from
// anything else, and the rendered config carries the keypair but no code rebuilds
// the store from it. A corrupt file is therefore recovered rather than fatal.
func Load(path string) (*Store, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		return &Store{path: path}, nil
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return &Store{path: path}, nil
	}
	s, err := parseStore(path, data)
	if err == nil {
		return s, nil
	}
	quarantined, qerr := quarantine(path, time.Now())
	if qerr != nil {
		return nil, err
	}
	recovered, rerr := recoverFromBackup(path)
	if rerr != nil {
		return nil, fmt.Errorf("%w (the unusable file was kept at %s)", err, quarantined)
	}
	return recovered, nil
}

// parseStore decodes and validates a node document that is already in memory.
func parseStore(path string, data []byte) (*Store, error) {
	var f fileFormat
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	s := &Store{path: path, nodes: f.Nodes}
	seen := make(map[string]bool, len(s.nodes))
	for i := range s.nodes {
		n := &s.nodes[i]
		if n.ID == "" || seen[n.ID] {
			return nil, fmt.Errorf("parse %s: duplicate or empty node id", path)
		}
		seen[n.ID] = true
		if !state.Known(n.Protocol) {
			return nil, fmt.Errorf("parse %s: node %q has unknown protocol %q", path, n.Name, n.Protocol)
		}
	}
	return s, nil
}

// quarantine moves an unusable store aside so it is not destroyed and the next save
// can install a fresh file. The suffix keeps the moment so two bad files do not
// overwrite each other.
func quarantine(path string, now time.Time) (string, error) {
	target := fmt.Sprintf("%s.corrupt-%s", path, now.UTC().Format("20060102T150405"))
	if err := os.Rename(path, target); err != nil {
		return "", err
	}
	return target, nil
}

// recoverFromBackup loads the sibling backup and installs it as the store again. A
// backup that is also unusable is refused rather than promoted.
func recoverFromBackup(path string) (*Store, error) {
	backup := atomicfile.BackupPath(path)
	data, err := os.ReadFile(backup)
	if err != nil {
		return nil, fmt.Errorf("no usable backup at %s: %w", backup, err)
	}
	s, err := parseStore(backup, data)
	if err != nil {
		return nil, err
	}
	if err := atomicfile.Write(path, data, 0o600); err != nil {
		return nil, err
	}
	s.path = path
	return s, nil
}

// Path returns the file the store persists to.
func (s *Store) Path() string { return s.path }

// Empty returns a store with no nodes and no file behind it. A caller that treats a
// missing store as "this host serves nothing" can pass it through the same code path
// as a loaded one instead of branching, and a zero Store cannot be built from
// outside this package because its fields are unexported.
func Empty() *Store { return &Store{} }

// Len returns the number of nodes.
func (s *Store) Len() int { return len(s.nodes) }

// Nodes returns every node, ordered by name.
func (s *Store) Nodes() []Node {
	out := make([]Node, len(s.nodes))
	copy(out, s.nodes)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Enabled returns the nodes that may be rendered, ordered by name.
func (s *Store) Enabled() []Node {
	var out []Node
	for _, n := range s.Nodes() {
		if n.Enabled {
			out = append(out, n)
		}
	}
	return out
}

// ByID returns one node, and whether the id is known.
func (s *Store) ByID(id string) (Node, bool) {
	for _, n := range s.nodes {
		if n.ID == id {
			return n, true
		}
	}
	return Node{}, false
}

// ProtocolOf reports the protocol of one node.
func (s *Store) ProtocolOf(id string) (string, bool) {
	n, ok := s.ByID(id)
	return n.Protocol, ok
}

// NameTaken reports whether a node name is already used.
func (s *Store) NameTaken(name, excludeID string) bool {
	for _, n := range s.nodes {
		if n.ID != excludeID && n.Name == name {
			return true
		}
	}
	return false
}

// Validate reports why a node cannot be stored. Exclude is the id being edited,
// which is allowed to keep its own name and port.
func (s *Store) Validate(n Node, excludeID string) error {
	name := strings.TrimSpace(n.Name)
	if name == "" {
		return errors.New("node name is required")
	}
	if len([]rune(name)) > 40 {
		return errors.New("node name is longer than 40 characters")
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return errors.New("node name contains a control character")
		}
	}
	if !state.Known(n.Protocol) {
		return fmt.Errorf("unknown protocol %q", n.Protocol)
	}
	if n.Port < 1 || n.Port > 65535 {
		return fmt.Errorf("node port %d is outside 1-65535", n.Port)
	}
	for _, other := range s.nodes {
		if other.ID == excludeID {
			continue
		}
		if other.Name == name {
			return fmt.Errorf("node %q already uses this name", other.Name)
		}
		if n.Enabled && other.Enabled && other.Port == n.Port {
			return fmt.Errorf("node %q already listens on port %d", other.Name, other.Port)
		}
	}
	return nil
}

// CheckSubPort refuses a node that would collide with the subscription service.
// The node store does not know that port, so the caller passes it in.
func CheckSubPort(n Node, subPort int) error {
	if n.Enabled && n.Port == subPort {
		return fmt.Errorf("port %d is the subscription service port", n.Port)
	}
	return nil
}

// Add stores a new node.
func (s *Store) Add(n Node) error {
	if err := s.Validate(n, ""); err != nil {
		return err
	}
	n.EnsureParams()
	s.nodes = append(s.nodes, n)
	return s.Save()
}

// Update edits one node in place. A rename keeps the id, so account selections
// and credentials stay valid.
func (s *Store) Update(id string, fn func(*Node) error) error {
	for i := range s.nodes {
		if s.nodes[i].ID != id {
			continue
		}
		edited := s.nodes[i]
		// Params is a map, so a shallow copy would let fn mutate the stored node
		// before Validate runs and the change is committed.
		edited.Params = map[string]string{}
		for k, v := range s.nodes[i].Params {
			edited.Params[k] = v
		}
		if err := fn(&edited); err != nil {
			return err
		}
		if err := s.Validate(edited, id); err != nil {
			return err
		}
		edited.EnsureParams()
		s.nodes[i] = edited
		return s.Save()
	}
	return fmt.Errorf("node %q not found", id)
}

// Remove deletes one node.
func (s *Store) Remove(id string) error {
	for i := range s.nodes {
		if s.nodes[i].ID != id {
			continue
		}
		s.nodes = append(s.nodes[:i], s.nodes[i+1:]...)
		return s.Save()
	}
	return fmt.Errorf("node %q not found", id)
}

// Save writes the file with 0600 permissions through a fresh temporary file, the
// same interrupted-write protection the account store uses.
func (s *Store) Save() error {
	if dir := filepath.Dir(s.path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(fileFormat{Version: CurrentVersion, Nodes: s.nodes}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	// atomicfile.WriteKeepingBackup is the interrupted-write protection the account
	// store uses too: a fresh temporary file beside the target, flushed, then renamed
	// over it, and the previous content kept as a sibling Load falls back to.
	return atomicfile.WriteKeepingBackup(s.path, data, 0o600)
}
