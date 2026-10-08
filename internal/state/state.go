// Package state reads and writes the EasySB node state file
// (/etc/sing-box/easysb.conf). The key/value layout is kept from the legacy
// shell implementation, but later versions drop the keys whose component is
// gone: v4 removed the node-wide credential and the nginx subscription keys
// (credentials belong to the accounts in internal/user, and the endpoint is
// built from SUB_SERVE_PORT), v5 removed CORE_CHANNEL, CORE_SOURCE and
// STATS_API, and v6 moves the five protocol enable flags, their ports and their
// parameters out of the state file into the node store (internal/node): what
// the host serves is now a list of explicit nodes, so the per-protocol keys are
// read once to migrate a legacy deployment and then dropped on the next save.
// A file that still holds those keys loads fine and loses them on the next
// save.
package state

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/EasySBTeam/EasySB/internal/sysinfo"
)

// Protocol keys used across the state file and config generation.
const (
	ProtoAnyTLS       = "anytls"
	ProtoHysteria2    = "hysteria2"
	ProtoTUIC         = "tuic"
	ProtoVLESSReality = "vless-reality"
	ProtoVMessWSTLS   = "vmess-ws-tls"
)

// Keys lists protocols in the canonical order.
var Keys = []string{ProtoAnyTLS, ProtoHysteria2, ProtoTUIC, ProtoVLESSReality, ProtoVMessWSTLS}

// Labels maps protocol keys to human readable names.
var Labels = map[string]string{
	ProtoAnyTLS:       "AnyTLS",
	ProtoHysteria2:    "Hysteria2",
	ProtoTUIC:         "TUIC v5",
	ProtoVLESSReality: "VLESS-Vision-Reality",
	ProtoVMessWSTLS:   "VMess-WebSocket-TLS",
}

// Known reports whether a protocol key is one this build renders.
func Known(key string) bool {
	for _, k := range Keys {
		if k == key {
			return true
		}
	}
	return false
}

// DefaultPorts maps protocol keys to their default listen ports.
var DefaultPorts = map[string]string{
	ProtoAnyTLS:       "8000",
	ProtoHysteria2:    "8001",
	ProtoTUIC:         "8002",
	ProtoVLESSReality: "8003",
	ProtoVMessWSTLS:   "8004",
}

// Defaults for optional parameters.
const (
	DefaultHopRange = "2080:3000"
	DefaultSNI      = "apple.com"

	// DefaultSubServePort is the listen port of the built-in subscription
	// service that replaced the nginx site.
	DefaultSubServePort = 8443
	// DefaultSubSyncSeconds is how often usage is read from the core.
	DefaultSubSyncSeconds = 300
	// MinSubSyncSeconds bounds the accounting interval. A shorter interval only
	// adds gRPC round trips and config rewrites.
	MinSubSyncSeconds = 30
)

// Config is the persisted node configuration.
type Config struct {
	// Enabled, Ports, HopRange and Reality* are the v5 per-protocol fields. They
	// are read from a legacy file so the node migration can consume them, and are
	// never written back; internal/node is the source of truth for the host now.
	Enabled      map[string]bool
	Ports        map[string]string
	HopRange     string
	RealitySNI   string
	RealityPriv  string
	RealityPub   string
	RealitySID   string
	Domain       string
	CertDomain   string
	ACMEEmail    string
	NodeDeployed bool
	SubServePort int
	SubSyncSecs  int
	ServerIP     string
	raw          map[string]string
}

// LegacyProtocols reports whether the state file still carries the v5
// per-protocol keys. It is the trigger for the one-time node migration: a fresh
// install has no such keys, so it does not mint five nodes from the defaults.
func (c Config) LegacyProtocols() bool {
	for k := range c.raw {
		if strings.HasPrefix(k, "IS_") || strings.HasPrefix(k, "PORT_") ||
			k == "HY2_HOP_RANGE" || strings.HasPrefix(k, "REALITY_") {
			return true
		}
	}
	return false
}

// Default returns a Config populated with built-in defaults.
func Default() Config {
	c := Config{
		Enabled:      map[string]bool{},
		Ports:        map[string]string{},
		HopRange:     DefaultHopRange,
		RealitySNI:   DefaultSNI,
		SubServePort: DefaultSubServePort,
		SubSyncSecs:  DefaultSubSyncSeconds,
		NodeDeployed: false,
		raw:          map[string]string{},
	}
	for _, k := range Keys {
		c.Enabled[k] = true
		c.Ports[k] = DefaultPorts[k]
	}
	return c
}

// stateFile is where the node state is read and written. It is a variable so that a
// test can round-trip a state file in a temporary directory: what such a test is about
// is the document and the file left behind, and neither belongs in /etc on a build host.
//
// It is the only seam here on purpose. Save creates the directory this file lives in
// rather than a directory named separately, because a second variable is a second thing
// a test can forget to redirect - and on a machine where the drive root is writable,
// MkdirAll of the real /etc/sing-box succeeds quietly instead of failing, so the test
// passes while writing nowhere near where it thinks it is.
var stateFile = sysinfo.StateFile

// Load reads the state file, applying defaults for any missing value.
func Load() Config {
	c := Default()
	// The file is parsed by the one parser the host facts are read with too, so a
	// value that carries a quote or a backslash round-trips the same way in both.
	for key, val := range sysinfo.ReadKeyValues(stateFile) {
		c.raw[key] = val
	}
	c.applyRaw()
	return c
}

func (c *Config) applyRaw() {
	enabledKey := map[string]string{
		ProtoAnyTLS:       "IS_ANYTLS",
		ProtoHysteria2:    "IS_HYSTERIA2",
		ProtoTUIC:         "IS_TUIC",
		ProtoVLESSReality: "IS_VLESS_REALITY",
		ProtoVMessWSTLS:   "IS_VMESS_WS_TLS",
	}
	portKey := map[string]string{
		ProtoAnyTLS:       "PORT_ANYTLS",
		ProtoHysteria2:    "PORT_HYSTERIA2",
		ProtoTUIC:         "PORT_TUIC",
		ProtoVLESSReality: "PORT_VLESS_REALITY",
		ProtoVMessWSTLS:   "PORT_VMESS_WS_TLS",
	}
	for _, k := range Keys {
		if v, ok := c.raw[enabledKey[k]]; ok {
			c.Enabled[k] = strings.EqualFold(v, "true") || v == "yes" || v == "1"
		}
		if v := c.raw[portKey[k]]; v != "" {
			c.Ports[k] = v
		}
	}
	set := func(dst *string, key string) {
		if v := c.raw[key]; v != "" {
			*dst = v
		}
	}
	set(&c.HopRange, "HY2_HOP_RANGE")
	set(&c.RealitySNI, "REALITY_SNI")
	set(&c.RealityPriv, "REALITY_PRIVATE")
	set(&c.RealityPub, "REALITY_PUBLIC")
	set(&c.RealitySID, "REALITY_SHORT_ID")
	set(&c.Domain, "DOMAIN")
	set(&c.CertDomain, "CERT_DOMAIN")
	set(&c.ACMEEmail, "ACME_EMAIL")
	set(&c.ServerIP, "SERVER_IP")
	if n, err := strconv.Atoi(c.raw["SUB_SERVE_PORT"]); err == nil && n > 0 && n < 65536 {
		c.SubServePort = n
	}
	if n, err := strconv.Atoi(c.raw["SUB_SYNC_SECONDS"]); err == nil && n > 0 {
		c.SubSyncSecs = n
	}
	if v, ok := c.raw["NODE_DEPLOYED"]; ok {
		c.NodeDeployed = strings.EqualFold(v, "yes")
	}
}

// AnyEnabled reports whether at least one protocol is enabled.
func (c Config) AnyEnabled() bool {
	for _, k := range Keys {
		if c.Enabled[k] {
			return true
		}
	}
	return false
}

// Host returns the public host used by subscriptions and share links.
func (c Config) Host() string {
	if c.Domain != "" {
		return c.Domain
	}
	if c.CertDomain != "" {
		return c.CertDomain
	}
	return c.ServerIP
}

// SyncInterval is the accounting interval, never shorter than
// MinSubSyncSeconds however the state file was edited.
func (c Config) SyncInterval() time.Duration {
	secs := c.SubSyncSecs
	if secs < MinSubSyncSeconds {
		secs = MinSubSyncSeconds
	}
	return time.Duration(secs) * time.Second
}

// SubPort is the effective port of the subscription endpoint.
func (c Config) SubPort() int {
	if c.SubServePort > 0 {
		return c.SubServePort
	}
	return DefaultSubServePort
}

// Save writes the state back to disk with 0600 permissions.
func (c Config) Save() error {
	// The directory comes from stateFile, not from sysinfo.WorkDir: this is the line that
	// used to reach /etc during a test. On the CI runner it failed with "mkdir
	// /etc/sing-box: permission denied"; on a Windows checkout the same call succeeded and
	// created D:\etc\sing-box, so the test looked green while the seam leaked.
	if dir := filepath.Dir(stateFile); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	lines := []string{"# EasySB state"}
	deployed := "no"
	if c.NodeDeployed {
		deployed = "yes"
	}
	pairs := [][2]string{
		{"DOMAIN", c.Domain},
		{"CERT_DOMAIN", c.CertDomain},
		{"ACME_EMAIL", c.ACMEEmail},
		{"NODE_DEPLOYED", deployed},
		{"SUB_SERVE_PORT", strconv.Itoa(c.SubServePort)},
		{"SUB_SYNC_SECONDS", strconv.Itoa(c.SubSyncSecs)},
		{"SERVER_IP", c.ServerIP},
	}
	for _, p := range pairs {
		lines = append(lines, fmt.Sprintf("%s=%q", p[0], p[1]))
	}

	// Preserve any unrecognised keys from the previous file.
	for _, k := range c.extraKeys() {
		lines = append(lines, fmt.Sprintf("%s=%q", k, c.raw[k]))
	}

	// A fresh temporary name per write, not StateFile+".tmp", the way the account
	// store writes its own file: a fixed name is a path anything with write access to
	// the directory can have prepared as a symlink beforehand, and two writers sharing
	// one temporary path can interleave into a truncated document that is then renamed
	// into place. CreateTemp applies 0600 too, so the state never lands under the umask
	// of the invoking shell.
	f, err := os.CreateTemp(filepath.Dir(stateFile), filepath.Base(stateFile)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, stateFile); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func (c Config) extraKeys() []string {
	known := map[string]bool{
		"IS_ANYTLS": true, "IS_HYSTERIA2": true, "IS_TUIC": true,
		"IS_VLESS_REALITY": true, "IS_VMESS_WS_TLS": true,
		"PORT_ANYTLS": true, "PORT_HYSTERIA2": true, "PORT_TUIC": true,
		"PORT_VLESS_REALITY": true, "PORT_VMESS_WS_TLS": true,
		"HY2_HOP_RANGE": true, "REALITY_SNI": true, "REALITY_PRIVATE": true,
		"REALITY_PUBLIC": true, "REALITY_SHORT_ID": true, "DOMAIN": true,
		"CERT_DOMAIN": true, "ACME_EMAIL": true, "NODE_DEPLOYED": true,
		// Keys of the switchable core. They are still listed as known so that a
		// state file written by v4 loses them on the next save instead of carrying
		// them along as unknown keys; nothing reads them any more.
		"CORE_CHANNEL": true, "CORE_SOURCE": true, "STATS_API": true,
		"SUB_SERVE_PORT": true, "SUB_SYNC_SECONDS": true, "SERVER_IP": true,
	}
	var out []string
	for k := range c.raw {
		if !known[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
