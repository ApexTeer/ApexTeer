package config

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/EasySB-Team/EasySB/internal/state"
	"github.com/EasySB-Team/EasySB/internal/user"
)

func fullParams() Params {
	c := state.Default()
	c.RealityPriv = "priv"
	c.RealityPub = "pub"
	c.RealitySID = "abcd1234"
	c.RealitySNI = "apple.com"
	account := user.New("demo", state.Keys, time.Unix(0, 0))
	p := ParamsFromState(c)
	p.CertFullchain = "/etc/sing-box/cert/fullchain.cer"
	p.CertKey = "/etc/sing-box/cert/private.key"
	p.Members = MembersFrom([]user.User{account})
	return p
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
	data, err := Build(fullParams())
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
	for _, tag := range state.Keys {
		if _, ok := byTag[tag]; !ok {
			t.Fatalf("missing inbound %s", tag)
		}
	}
	if p := byTag[state.ProtoAnyTLS]["listen_port"].(float64); p != 8000 {
		t.Fatalf("anytls port = %v", p)
	}
	if p := byTag[state.ProtoVMessWSTLS]["listen_port"].(float64); p != 8004 {
		t.Fatalf("vmess port = %v", p)
	}
	reality := byTag[state.ProtoVLESSReality]["tls"].(map[string]any)["reality"].(map[string]any)
	if reality["private_key"] != "priv" {
		t.Fatalf("reality private key = %v", reality["private_key"])
	}
}

func TestBuildRespectsDisabled(t *testing.T) {
	p := fullParams()
	p.Enabled = map[string]bool{state.ProtoAnyTLS: true}
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
	p.Enabled = map[string]bool{}
	if _, err := Build(p); err == nil {
		t.Fatal("expected error when no protocol is enabled")
	}
}

func TestBuildInvalidPort(t *testing.T) {
	p := fullParams()
	p.Enabled = map[string]bool{state.ProtoAnyTLS: true}
	p.Ports = map[string]string{state.ProtoAnyTLS: "70000"}
	if _, err := Build(p); err == nil {
		t.Fatal("expected error for out-of-range port")
	}
}

func TestNeedsCert(t *testing.T) {
	p := fullParams()
	if !p.NeedsCert() {
		t.Fatal("expected cert requirement with TLS protocols enabled")
	}
	p.Enabled = map[string]bool{state.ProtoVLESSReality: true}
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

// TestParamsFromStateLeavesStatsToTheCaller pins the split of responsibility: the state
// file describes the host, so it cannot say whether this build counts traffic. The deploy
// path asks the build (internal/sbcore) and sets Params.Stats itself, which is why a state
// file maps to a params value with the block switched off.
func TestParamsFromStateLeavesStatsToTheCaller(t *testing.T) {
	if ParamsFromState(state.Default()).Stats {
		t.Fatal("ParamsFromState must not claim the counters: the build decides that")
	}
}
