// Package config renders the sing-box server configuration from the EasySB
// state and the account list. Every protocol inbound authenticates the accounts
// that selected it, and the stats API the panel accounts traffic through is
// declared here, because the same member list has to drive both.
package config

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/EasySBTeam/EasySB/internal/state"
	"github.com/EasySBTeam/EasySB/internal/user"
)

// StatsListen is the loopback endpoint of the core's stats service. It is not
// configurable: the panel is the only client, and a public stats port would leak
// every account name.
const StatsListen = "127.0.0.1:10085"

// Params is the input needed to render config.json.
type Params struct {
	Enabled       map[string]bool
	Ports         map[string]string
	Members       []Member
	HopRange      string
	RealitySNI    string
	RealityPriv   string
	RealitySID    string
	CertFullchain string
	CertKey       string
	// Stats asks for the experimental.v2ray_api block. It is not a host fact: it
	// is whether this panel was built with the with_v2ray_api tag (see
	// internal/sbcore), and a build without it rejects a config naming the API.
	Stats bool
}

// ParamsFromState maps a persisted config into rendering parameters.
func ParamsFromState(c state.Config) Params {
	return Params{
		Enabled:     c.Enabled,
		Ports:       c.Ports,
		HopRange:    c.HopRange,
		RealitySNI:  c.RealitySNI,
		RealityPriv: c.RealityPriv,
		RealitySID:  c.RealitySID,
	}
}

// Credentials is one member's secret fields for one protocol.
type Credentials struct {
	UUID     string
	Password string
}

// Member is one account as the core sees it. Name is the core user name, which
// EasySB sets to the account's subscription token: it is the identity the stats
// service reports, so a renamed account keeps its counters.
type Member struct {
	Name      string
	Protocols map[string]bool
	Cred      map[string]Credentials
}

func (m Member) selects(key string) bool {
	return m.Protocols[key]
}

func (m Member) credential(key string) Credentials {
	return m.Cred[key]
}

// MembersFrom converts accounts into the renderer's member list.
func MembersFrom(users []user.User) []Member {
	out := make([]Member, 0, len(users))
	for _, u := range users {
		protocols := make(map[string]bool, len(u.Protocols))
		cred := make(map[string]Credentials, len(u.Protocols))
		for _, key := range u.Protocols {
			protocols[key] = true
			c := u.Credential(key)
			cred[key] = Credentials{UUID: c.UUID, Password: c.Password}
		}
		out = append(out, Member{Name: u.Token, Protocols: protocols, Cred: cred})
	}
	return out
}

// selectMembers returns the members that may use one protocol, in name order.
func (p Params) selectMembers(key string) []Member {
	var out []Member
	for _, m := range p.Members {
		if m.selects(key) {
			out = append(out, m)
		}
	}
	return out
}

// protocolUsers renders the core user list of one protocol: one entry per
// member that selected it. tweak adjusts the fields a protocol needs beyond the
// credential itself, such as the VLESS flow.
func (p Params) protocolUsers(key string, tweak func(*coreUser)) []coreUser {
	var out []coreUser
	for _, m := range p.selectMembers(key) {
		cred := m.credential(key)
		entry := coreUser{Name: m.Name, UUID: cred.UUID, Password: cred.Password}
		if tweak != nil {
			tweak(&entry)
		}
		out = append(out, entry)
	}
	return out
}

var paddingScheme = []string{
	"stop=8",
	"0=30-30",
	"1=100-400",
	"2=400-500,c,500-1000,c,500-1000,c,500-1000,c,500-1000",
	"3=9-9,500-1000",
	"4=500-1000",
	"5=500-1000",
	"6=500-1000",
	"7=500-1000",
}

// NeedsCert reports whether any enabled protocol (other than Reality) requires
// a TLS certificate.
func (p Params) NeedsCert() bool {
	for _, k := range state.Keys {
		if k == state.ProtoVLESSReality {
			continue
		}
		if p.Enabled[k] {
			return true
		}
	}
	return false
}

// Build renders the complete server configuration document. Params.Stats decides
// whether the document carries the V2Ray API block the counters are read over:
// a core that was not built with that API refuses the whole document, so the
// block is left out rather than written and rejected.
func Build(p Params) ([]byte, error) {
	inbounds, err := buildInbounds(p)
	if err != nil {
		return nil, err
	}
	doc := serverConfig{
		Log:       logConfig{Level: "info", Timestamp: true},
		Inbounds:  inbounds,
		Outbounds: []outbound{{Type: "direct", Tag: "direct"}},
	}
	if p.Stats {
		doc.Experimental = &experimental{
			V2RayAPI: v2rayAPI{
				Listen: StatsListen,
				Stats:  statsEntry{Enabled: true, Users: p.memberNames()},
			},
		}
	}
	return json.MarshalIndent(doc, "", "  ")
}

// memberNames lists the core user names the stats service should count. The
// core counts nothing that is not named here, so this list and the inbounds are
// always built from the same member slice.
func (p Params) memberNames() []string {
	out := make([]string, 0, len(p.Members))
	for _, m := range p.Members {
		out = append(out, m.Name)
	}
	return out
}

type serverConfig struct {
	Log          logConfig     `json:"log"`
	Inbounds     []any         `json:"inbounds"`
	Outbounds    []outbound    `json:"outbounds"`
	Experimental *experimental `json:"experimental,omitempty"`
}

type experimental struct {
	V2RayAPI v2rayAPI `json:"v2ray_api"`
}

type v2rayAPI struct {
	Listen string     `json:"listen"`
	Stats  statsEntry `json:"stats"`
}

type statsEntry struct {
	Enabled bool     `json:"enabled"`
	Users   []string `json:"users"`
}

type logConfig struct {
	Level     string `json:"level"`
	Timestamp bool   `json:"timestamp"`
}

type outbound struct {
	Type string `json:"type"`
	Tag  string `json:"tag"`
}

type coreUser struct {
	Name     string `json:"name,omitempty"`
	Password string `json:"password,omitempty"`
	UUID     string `json:"uuid,omitempty"`
	Flow     string `json:"flow,omitempty"`
	AlterID  *int   `json:"alterId,omitempty"`
}

type tlsConfig struct {
	Enabled         bool           `json:"enabled"`
	CertificatePath string         `json:"certificate_path,omitempty"`
	KeyPath         string         `json:"key_path,omitempty"`
	ServerName      string         `json:"server_name,omitempty"`
	ALPN            []string       `json:"alpn,omitempty"`
	Reality         *realityConfig `json:"reality,omitempty"`
}

type realityConfig struct {
	Enabled   bool      `json:"enabled"`
	Handshake handshake `json:"handshake"`
	Private   string    `json:"private_key"`
	ShortID   []string  `json:"short_id"`
}

type handshake struct {
	Server string `json:"server"`
	Port   int    `json:"server_port"`
}

func (p Params) port(key string) (int, error) {
	raw := p.Ports[key]
	if raw == "" {
		raw = state.DefaultPorts[key]
	}
	var n int
	if _, err := fmt.Sscanf(raw, "%d", &n); err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("invalid port %q for %s", raw, key)
	}
	return n, nil
}

func (p Params) base(key string) (map[string]any, error) {
	port, err := p.port(key)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"tag":         key,
		"listen":      "::",
		"listen_port": port,
	}, nil
}

func (p Params) certTLS(alpn []string) tlsConfig {
	return tlsConfig{
		Enabled:         true,
		CertificatePath: p.CertFullchain,
		KeyPath:         p.CertKey,
		ALPN:            alpn,
	}
}

func buildInbounds(p Params) ([]any, error) {
	var out []any

	if p.Enabled[state.ProtoAnyTLS] {
		m, err := p.base(state.ProtoAnyTLS)
		if err != nil {
			return nil, err
		}
		m["type"] = "anytls"
		m["users"] = p.protocolUsers(state.ProtoAnyTLS, nil)
		m["padding_scheme"] = paddingScheme
		m["tls"] = p.certTLS([]string{"h3", "h2", "http/1.1"})
		out = append(out, m)
	}

	if p.Enabled[state.ProtoHysteria2] {
		m, err := p.base(state.ProtoHysteria2)
		if err != nil {
			return nil, err
		}
		m["type"] = "hysteria2"
		m["up_mbps"] = 100
		m["down_mbps"] = 20
		m["users"] = p.protocolUsers(state.ProtoHysteria2, nil)
		tls := p.certTLS([]string{"h3"})
		m["tls"] = tls
		out = append(out, m)
	}

	if p.Enabled[state.ProtoTUIC] {
		m, err := p.base(state.ProtoTUIC)
		if err != nil {
			return nil, err
		}
		m["type"] = "tuic"
		m["users"] = p.protocolUsers(state.ProtoTUIC, nil)
		m["congestion_control"] = "bbr"
		m["auth_timeout"] = "3s"
		m["zero_rtt_handshake"] = false
		m["heartbeat"] = "10s"
		m["tls"] = p.certTLS([]string{"h3"})
		out = append(out, m)
	}

	if p.Enabled[state.ProtoVLESSReality] {
		m, err := p.base(state.ProtoVLESSReality)
		if err != nil {
			return nil, err
		}
		m["type"] = "vless"
		m["tag"] = state.ProtoVLESSReality
		m["users"] = p.protocolUsers(state.ProtoVLESSReality, func(u *coreUser) {
			u.Flow = "xtls-rprx-vision"
		})
		m["tls"] = tlsConfig{
			Enabled:    true,
			ServerName: p.RealitySNI,
			Reality: &realityConfig{
				Enabled:   true,
				Handshake: handshake{Server: p.RealitySNI, Port: 443},
				Private:   p.RealityPriv,
				ShortID:   []string{p.RealitySID},
			},
		}
		out = append(out, m)
	}

	if p.Enabled[state.ProtoVMessWSTLS] {
		m, err := p.base(state.ProtoVMessWSTLS)
		if err != nil {
			return nil, err
		}
		zero := 0
		m["type"] = "vmess"
		m["users"] = p.protocolUsers(state.ProtoVMessWSTLS, func(u *coreUser) {
			u.AlterID = &zero
		})
		m["multiplex"] = map[string]any{"enabled": true, "padding": false}
		m["transport"] = map[string]any{
			"type":                   "ws",
			"path":                   "/vmess",
			"max_early_data":         2048,
			"early_data_header_name": "Sec-WebSocket-Protocol",
		}
		m["tls"] = p.certTLS(nil)
		out = append(out, m)
	}

	if len(out) == 0 {
		return nil, errors.New("no protocol enabled")
	}
	return out, nil
}
