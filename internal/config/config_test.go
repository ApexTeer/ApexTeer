package config

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/EasySBTeam/EasySB/internal/node"
	"github.com/EasySBTeam/EasySB/internal/state"
	"github.com/EasySBTeam/EasySB/internal/user"
)

// newNode builds one node per protocol, with the Reality node carrying a known
// keypair so the rendered document can be asserted on.
func newNode(protocol string, port int) node.Node {
	return node.New(protocol, protocol, port, nil)
}

func newNodeReality(port int, priv, sid, sni string) node.Node {
	return node.New(state.ProtoVLESSReality, state.ProtoVLESSReality, port, map[string]string{
		node.ParamRealityPrivate: priv,
		node.ParamRealityPublic:  "pub",
		node.ParamRealityShortID: sid,
		node.ParamRealitySNI:     sni,
	})
}

func selectionsFor(nodes []node.Node) []user.Selection {
	out := make([]user.Selection, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, user.Selection{Node: n.ID, Protocol: n.Protocol})
	}
	return out
}

func fullParams() Params {
	nodes := []node.Node{
		newNode(state.ProtoAnyTLS, 8000),
		newNode(state.ProtoHysteria2, 8001),
		newNode(state.ProtoTUIC, 8002),
		newNodeReality(8003, "priv", "abcd1234", "apple.com"),
		newNode(state.ProtoVMessWSTLS, 8004),
	}
	account := user.New("demo", selectionsFor(nodes), time.Unix(0, 0))
	return Params{
		Nodes:         nodes,
		Members:       MembersFrom([]user.User{account}),
		CertFullchain: "/etc/sing-box/cert/fullchain.cer",
		CertKey:       "/etc/sing-box/cert/private.key",
	}
}

type parsedConfig struct {
	Log       struct{ Level string } `json:"log"`
	Inbounds  []map[string]any       `json:"inbounds"`
	Outbounds []struct {
		Type string `json:"type"`
		Tag  string `json:"tag"`
	} `json:"outbounds"`
}

func TestBuildAllProtocols(t *testing.T) {
	p := fullParams()
	data, err := Build(p)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var parsed parsedConfig
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(parsed.Inbounds) != 5 {
		t.Fatalf("expected 5 inbounds, got %d", len(parsed.Inbounds))
	}
	if len(parsed.Outbounds) != 1 || parsed.Outbounds[0].Tag != "direct" {
		t.Fatalf("unexpected outbounds: %+v", parsed.Outbounds)
	}

	byTag := map[string]map[string]any{}
	for _, in := range parsed.Inbounds {
		byTag[in["tag"].(string)] = in
	}
	for _, n := range p.Nodes {
		if _, ok := byTag[n.ID]; !ok {
			t.Fatalf("missing inbound for node %s", n.Name)
		}
	}
	anytls := inboundFor(t, p.Nodes, state.ProtoAnyTLS, byTag)
	if port := anytls["listen_port"].(float64); port != 8000 {
		t.Fatalf("anytls port = %v", port)
	}
	vmess := inboundFor(t, p.Nodes, state.ProtoVMessWSTLS, byTag)
	if port := vmess["listen_port"].(float64); port != 8004 {
		t.Fatalf("vmess port = %v", port)
	}
	realityIn := inboundFor(t, p.Nodes, state.ProtoVLESSReality, byTag)
	reality := realityIn["tls"].(map[string]any)["reality"].(map[string]any)
	if reality["private_key"] != "priv" {
		t.Fatalf("reality private key = %v", reality["private_key"])
	}
}

// TestBuildEmptyRendersNoInbound pins the document the deploy installs when the
// last node is removed: it parses, carries no inbound, keeps the single direct
// outbound, and drops the stats block because there are no users left to count.
func TestBuildEmptyRendersNoInbound(t *testing.T) {
	data, err := BuildEmpty()
	if err != nil {
		t.Fatalf("BuildEmpty: %v", err)
	}
	var parsed parsedConfig
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("the empty document does not parse: %v", err)
	}
	if len(parsed.Inbounds) != 0 {
		t.Fatalf("BuildEmpty rendered %d inbounds, want none", len(parsed.Inbounds))
	}
	if len(parsed.Outbounds) != 1 || parsed.Outbounds[0].Type != "direct" {
		t.Fatalf("BuildEmpty outbounds = %+v, want a single direct", parsed.Outbounds)
	}
	if strings.Contains(string(data), "v2ray_api") {
		t.Fatal("the empty document must not carry the stats block: there are no users to count")
	}
}

// inboundFor finds the rendered inbound of the first node on a protocol.
func inboundFor(t *testing.T, nodes []node.Node, protocol string, byTag map[string]map[string]any) map[string]any {
	t.Helper()
	for _, n := range nodes {
		if n.Protocol != protocol {
			continue
		}
		in, ok := byTag[n.ID]
		if !ok {
			t.Fatalf("no inbound for node %s", n.Name)
		}
		return in
	}
	t.Fatalf("no node on protocol %s", protocol)
	return nil
}

func TestBuildRespectsDisabled(t *testing.T) {
	p := fullParams()
	p.Nodes = p.Nodes[:1]
	data, err := Build(p)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var parsed parsedConfig
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Inbounds) != 1 {
		t.Fatalf("expected 1 inbound, got %d", len(parsed.Inbounds))
	}
	if parsed.Inbounds[0]["type"] != "anytls" {
		t.Fatalf("unexpected inbound: %+v", parsed.Inbounds[0])
	}
}

func TestBuildNoProtocol(t *testing.T) {
	p := fullParams()
	p.Nodes = nil
	if _, err := Build(p); err == nil {
		t.Fatal("expected error when no protocol is enabled")
	}
}

func TestBuildInvalidPort(t *testing.T) {
	p := fullParams()
	p.Nodes = p.Nodes[:1]
	p.Nodes[0].Port = 70000
	if _, err := Build(p); err == nil {
		t.Fatal("expected error for out-of-range port")
	}
}

func TestNeedsCert(t *testing.T) {
	p := fullParams()
	if !p.NeedsCert() {
		t.Fatal("expected cert requirement with TLS protocols enabled")
	}
	var reality node.Node
	for _, n := range p.Nodes {
		if n.Protocol == state.ProtoVLESSReality {
			reality = n
		}
	}
	p.Nodes = []node.Node{reality}
	if p.NeedsCert() {
		t.Fatal("reality alone should not require a cert")
	}
}

// TestBuildStatsBlockFollowsCore keeps the V2Ray API block tied to the core that
// will read it: sing-box refuses the whole config when the block names an API the
// binary was not built with, so a core without it has to deploy without the block
// rather than fail.
func TestBuildStatsBlockFollowsCore(t *testing.T) {
	p := fullParams()
	p.Stats = true
	withStats, err := Build(p)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.Contains(string(withStats), `"v2ray_api"`) {
		t.Fatal("a stats-capable core should get the v2ray_api block")
	}
	if !strings.Contains(string(withStats), `"users"`) {
		t.Fatal("the block should whitelist the accounts to count")
	}

	p.Stats = false
	without, err := Build(p)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if strings.Contains(string(without), "v2ray_api") || strings.Contains(string(without), "experimental") {
		t.Fatalf("a core without the API should get a config without it:\n%s", without)
	}
	if !strings.Contains(string(without), `"inbounds"`) {
		t.Fatal("the rest of the config should still be there")
	}
}
