package subscribe

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/EasySBTeam/EasySB/internal/state"
	"github.com/EasySBTeam/EasySB/internal/user"
)

func TestStripJSONC(t *testing.T) {
	in := []byte("{\n  \"url\": \"https://example.com/x\", // comment\n  \"n\": 1 // tail\n}\n")
	out := string(stripJSONC(in))
	if strings.Contains(out, "// comment") || strings.Contains(out, "// tail") {
		t.Fatalf("comments not stripped: %s", out)
	}
	if !strings.Contains(out, "https://example.com/x") {
		t.Fatalf("url inside string was damaged: %s", out)
	}
	if err := json.Unmarshal(stripJSONC(in), &map[string]any{}); err != nil {
		t.Fatalf("stripped document is not valid JSON: %v", err)
	}
}

func testConfig() state.Config {
	cfg := state.Default()
	cfg.Domain = "node.example.com"
	return cfg
}

// testAccount is the subscriber the generated documents are rendered for. The
// UUIDs are pinned so the assertions stay readable; the panel always ships
// generated ones.
func testAccount() user.User {
	account := user.New("demo", selections(sampleNodes()), time.Unix(0, 0))
	for id, cred := range account.Credentials {
		if cred.UUID != "" {
			cred.UUID = testUUID
		}
		account.Credentials[id] = cred
	}
	return account
}

// testUUID is the canonical UUID every rendered credential carries.
const testUUID = "11111111-2222-3333-4444-555555555555"

func TestGenerateIncludesOnlyEnabled(t *testing.T) {
	cfg := testConfig()
	nodes := sampleNodes()
	for i := range nodes {
		if nodes[i].Protocol == state.ProtoTUIC || nodes[i].Protocol == state.ProtoAnyTLS {
			nodes[i].Enabled = false
		}
	}

	data, err := Generate(cfg, nodes, testAccount())
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Outbounds []struct {
			Tag       string      `json:"tag"`
			Type      string      `json:"type"`
			Server    string      `json:"server"`
			ServerP   json.Number `json:"server_port"`
			Ports     []string    `json:"server_ports"`
			UUID      string      `json:"uuid"`
			Password  string      `json:"password"`
			Outbounds []string    `json:"outbounds"`
			TLS       struct {
				ServerName string `json:"server_name"`
				Reality    struct {
					PublicKey string `json:"public_key"`
					ShortID   string `json:"short_id"`
				} `json:"reality"`
			} `json:"tls"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("generated subscription is invalid JSON: %v", err)
	}

	tags := map[string]bool{}
	var proxyList []string
	for _, ob := range doc.Outbounds {
		tags[ob.Tag] = true
		if ob.Tag == "proxy" {
			proxyList = ob.Outbounds
		}
	}
	// Every node is named after the account and the node, so a client that
	// imports several accounts can tell them apart.
	name := func(tag string) string { return NodeName("demo", tag) }
	if tags[name("tuic")] || tags[name("anytls")] {
		t.Fatalf("disabled nodes leaked into subscription: %v", tags)
	}
	if !tags[name("hysteria2")] || !tags[name("vmess-ws-tls")] || !tags[name("vless-reality")] {
		t.Fatalf("enabled nodes missing: %v", tags)
	}
	if len(proxyList) == 0 || proxyList[0] != "auto" {
		t.Fatalf("proxy selector list not rewritten: %v", proxyList)
	}

	for _, ob := range doc.Outbounds {
		if ob.Tag == name("vless-reality") {
			if ob.Server != "node.example.com" || ob.ServerP.String() != "8003" {
				t.Fatalf("vless server fields wrong: %+v", ob)
			}
			if ob.TLS.ServerName != "apple.com" || ob.TLS.Reality.PublicKey != "PUBKEY" || ob.TLS.Reality.ShortID != "abcd1234" {
				t.Fatalf("reality fields wrong: %+v", ob.TLS)
			}
		}
		if ob.Tag == name("hysteria2") {
			if len(ob.Ports) != 1 || ob.Ports[0] != state.DefaultHopRange {
				t.Fatalf("hop range wrong: %v", ob.Ports)
			}
		}
	}
}

func TestGeneratePreservesTemplateOrder(t *testing.T) {
	data, err := Generate(testConfig(), sampleNodes(), testAccount())
	if err != nil {
		t.Fatal(err)
	}
	out := string(data)

	// Top-level sections keep the template order instead of encoding/json's
	// alphabetical map order.
	assertSubstringOrder(t, out,
		"\n  \"$schema\"", "\n  \"log\"", "\n  \"http_clients\"", "\n  \"dns\"",
		"\n  \"inbounds\"", "\n  \"route\"", "\n  \"experimental\"", "\n  \"outbounds\"")

	// A node keeps type first, then tag, address, port, secret and TLS.
	i := strings.Index(out, "\"type\": \"anytls\"")
	if i < 0 {
		t.Fatal("anytls node missing from the subscription")
	}
	assertSubstringOrder(t, out[i:],
		"\"type\"", "\"tag\"", "\"server\"", "\"server_port\"", "\"password\"", "\"tls\"")
}

func assertSubstringOrder(t *testing.T, haystack string, needles ...string) {
	t.Helper()
	last := -1
	for _, n := range needles {
		i := strings.Index(haystack, n)
		if i < 0 {
			t.Fatalf("%q missing", n)
		}
		if i < last {
			t.Fatalf("%q appears out of order", n)
		}
		last = i
	}
}

func TestGenerateRequiresHost(t *testing.T) {
	cfg := testConfig()
	cfg.Domain = ""
	cfg.ServerIP = ""
	if _, err := Generate(cfg, sampleNodes(), testAccount()); err == nil {
		t.Fatal("expected error without a server address")
	}
}
