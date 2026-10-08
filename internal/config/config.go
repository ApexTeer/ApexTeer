// Package config renders the sing-box server configuration from the node store
// and the account list. Every enabled node becomes one inbound; the accounts
// that selected it authenticate against it under a core user name that is unique
// per account and node. The stats API the panel accounts traffic through is
// declared here, because the same member list has to drive both.
package config

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/EasySBTeam/EasySB/internal/node"
	"github.com/EasySBTeam/EasySB/internal/state"
	"github.com/EasySBTeam/EasySB/internal/user"
)

// StatsListen is the loopback endpoint of the core's stats service. It is not
// configurable: the panel is the only client, and a public stats port would leak
// every account name.
const StatsListen = "127.0.0.1:10085"

// ErrNoNodes is returned when nothing is enabled, so the core has no inbound to
// serve. The caller reports it instead of writing a document the core refuses.
var ErrNoNodes = errors.New("no node enabled")

// Params is the input needed to render config.json.
type Params struct {
	// Nodes are the nodes to render, in display order. Disabled nodes are
	// skipped, so a caller may pass the whole store.
	Nodes         []node.Node
	Members       []Member
	CertFullchain string
	CertKey       string
	// Stats asks for the experimental.v2ray_api block. It is not a host fact: it
	// is whether this panel was built with the with_v2ray_api tag (see
	// internal/sbcore), and a build without it rejects a config naming the API.
	Stats bool
}

// Credentials is one member's secret fields for one node.
type Credentials struct {
	UUID     string
	Password string
}

// Member is one account as the core sees it, keyed by node id. Name is not
// stored here because the core user name is unique per (account, node): it is
// node.CoreName(Token, nodeID).
type Member struct {
	Token string
	Nodes map[string]bool
	Cred  map[string]Credentials
}

// MembersFrom converts accounts into the renderer's member list.
func MembersFrom(users []user.User) []Member {
	out := make([]Member, 0, len(users))
	for _, u := range users {
		selected := make(map[string]bool, len(u.Nodes))
		cred := make(map[string]Credentials, len(u.Nodes))
		for _, id := range u.Nodes {
			selected[id] = true
			c := u.Credential(id)
			cred[id] = Credentials{UUID: c.UUID, Password: c.Password}
		}
		out = append(out, Member{Token: u.Token, Nodes: selected, Cred: cred})
	}
	return out
}

// binding is one core user entry: one account on one node. Building every
// binding once and deriving both the inbounds and the stats list from it is what
// makes the two lists impossible to disagree.
type binding struct {
	node  node.Node
	token string
	cred  Credentials
}

func (b binding) coreName() string {
	return node.CoreName(b.token, b.node.ID)
}

// bindings lists every account that may use every node, in node order then
// member order.
func (p Params) bindings() []binding {
	var out []binding
	for _, n := range p.Nodes {
		if !n.Enabled {
			continue
		}
		for _, m := range p.Members {
			if !m.Nodes[n.ID] {
				continue
			}
			out = append(out, binding{node: n, token: m.Token, cred: m.Cred[n.ID]})
		}
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

// NeedsCert reports whether any enabled node (other than Reality) requires a TLS
// certificate.
func (p Params) NeedsCert() bool {
	for _, n := range p.Nodes {
		if !n.Enabled || n.Protocol == state.ProtoVLESSReality {
			continue
		}
		return true
	}
	return false
}

// Build renders the complete server configuration document. Params.Stats decides
// whether the document carries the V2Ray API block the counters are read over:
// a core that was not built with that API refuses the whole document, so the
// block is left out rather than written and rejected.
func Build(p Params) ([]byte, error) {
	bindings := p.bindings()
	inbounds, err := buildInbounds(p, bindings)
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
				Stats:  statsEntry{Enabled: true, Users: coreNames(bindings)},
			},
		}
	}
	return json.MarshalIndent(doc, "", "  ")
}

// coreNames lists the core user names the stats service should count. The core
// counts nothing that is not named here, so this list and the inbounds are built
// from the same binding slice.
func coreNames(bindings []binding) []string {
	out := make([]string, 0, len(bindings))
	for _, b := range bindings {
		out = append(out, b.coreName())
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

// checkPort refuses a node whose port cannot be a listen_port. The node store
// validates 1-65535 on save; this is the renderer's own guard so a hand-edited
// file cannot reach the core.
func checkPort(n node.Node) error {
	if n.Port < 1 || n.Port > 65535 {
		return fmt.Errorf("node %q has invalid port %d", n.Name, n.Port)
	}
	return nil
}

func (p Params) certTLS(alpn []string) tlsConfig {
	return tlsConfig{
		Enabled:         true,
		CertificatePath: p.CertFullchain,
		KeyPath:         p.CertKey,
		ALPN:            alpn,
	}
}

// buildInbounds renders one inbound per enabled node. The account entries of a
// node come from the bindings of that node only.
func buildInbounds(p Params, bindings []binding) ([]any, error) {
	byNode := make(map[string][]binding, len(p.Nodes))
	for _, b := range bindings {
		byNode[b.node.ID] = append(byNode[b.node.ID], b)
	}

	var out []any
	for _, n := range p.Nodes {
		if !n.Enabled {
			continue
		}
		if err := checkPort(n); err != nil {
			return nil, err
		}
		users := coreUsers(byNode[n.ID], n.Protocol)
		m := map[string]any{
			"tag":         n.ID,
			"listen":      "::",
			"listen_port": n.Port,
			"users":       users,
		}
		switch n.Protocol {
		case state.ProtoAnyTLS:
			m["type"] = "anytls"
			m["padding_scheme"] = paddingScheme
			m["tls"] = p.certTLS([]string{"h3", "h2", "http/1.1"})
		case state.ProtoHysteria2:
			m["type"] = "hysteria2"
			m["up_mbps"] = 100
			m["down_mbps"] = 20
			m["tls"] = p.certTLS([]string{"h3"})
		case state.ProtoTUIC:
			m["type"] = "tuic"
			m["congestion_control"] = "bbr"
			m["auth_timeout"] = "3s"
			m["zero_rtt_handshake"] = false
			m["heartbeat"] = "10s"
			m["tls"] = p.certTLS([]string{"h3"})
		case state.ProtoVLESSReality:
			sni := n.Param(node.ParamRealitySNI)
			if sni == "" {
				sni = state.DefaultSNI
			}
			m["type"] = "vless"
			m["tls"] = tlsConfig{
				Enabled:    true,
				ServerName: sni,
				Reality: &realityConfig{
					Enabled:   true,
					Handshake: handshake{Server: sni, Port: 443},
					Private:   n.Param(node.ParamRealityPrivate),
					ShortID:   []string{n.Param(node.ParamRealityShortID)},
				},
			}
		case state.ProtoVMessWSTLS:
			m["type"] = "vmess"
			m["multiplex"] = map[string]any{"enabled": true, "padding": false}
			m["transport"] = map[string]any{
				"type":                   "ws",
				"path":                   "/vmess",
				"max_early_data":         2048,
				"early_data_header_name": "Sec-WebSocket-Protocol",
			}
			m["tls"] = p.certTLS(nil)
		default:
			return nil, fmt.Errorf("node %q has unknown protocol %q", n.Name, n.Protocol)
		}
		out = append(out, m)
	}

	if len(out) == 0 {
		return nil, ErrNoNodes
	}
	return out, nil
}

// coreUsers renders one node's user list. The VLESS flow and the VMess alter id
// are the fields a protocol needs beyond the credential itself.
func coreUsers(bindings []binding, protocol string) []coreUser {
	out := make([]coreUser, 0, len(bindings))
	for _, b := range bindings {
		entry := coreUser{Name: b.coreName(), UUID: b.cred.UUID, Password: b.cred.Password}
		switch protocol {
		case state.ProtoVLESSReality:
			entry.Flow = "xtls-rprx-vision"
		case state.ProtoVMessWSTLS:
			zero := 0
			entry.AlterID = &zero
		}
		out = append(out, entry)
	}
	return out
}
