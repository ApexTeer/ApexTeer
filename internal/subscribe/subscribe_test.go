package subscribe

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/EasySBTeam/EasySB/internal/node"
	"github.com/EasySBTeam/EasySB/internal/state"
	"github.com/EasySBTeam/EasySB/internal/user"
)

// sampleConfig is the node state the client documents are derived from. Protocol
// parameters live on the nodes now, so only the host fields remain here.
func sampleConfig() state.Config {
	c := state.Default()
	c.ServerIP = "203.0.113.10"
	return c
}

// sampleNodes is one node per protocol, named after the protocol so the display
// names stay readable. The Reality parameters are pinned.
func sampleNodes() []node.Node {
	return []node.Node{
		{ID: "n-anytls", Name: state.ProtoAnyTLS, Protocol: state.ProtoAnyTLS, Port: 8000, Enabled: true},
		{ID: "n-hysteria2", Name: state.ProtoHysteria2, Protocol: state.ProtoHysteria2, Port: 8001, Enabled: true,
			Params: map[string]string{node.ParamHopRange: state.DefaultHopRange}},
		{ID: "n-tuic", Name: state.ProtoTUIC, Protocol: state.ProtoTUIC, Port: 8002, Enabled: true},
		{ID: "n-vless", Name: state.ProtoVLESSReality, Protocol: state.ProtoVLESSReality, Port: 8003, Enabled: true,
			Params: map[string]string{
				node.ParamRealitySNI:     "apple.com",
				node.ParamRealityPrivate: "PRIV",
				node.ParamRealityPublic:  "PUBKEY",
				node.ParamRealityShortID: "abcd1234",
			}},
		{ID: "n-vmess", Name: state.ProtoVMessWSTLS, Protocol: state.ProtoVMessWSTLS, Port: 8004, Enabled: true},
	}
}

// selections turns nodes into the account selection set.
func selections(nodes []node.Node) []user.Selection {
	out := make([]user.Selection, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, user.Selection{Node: n.ID, Protocol: n.Protocol})
	}
	return out
}

// nodeID maps a protocol to the fixture node that serves it.
func nodeID(protocol string) string {
	for _, n := range sampleNodes() {
		if n.Protocol == protocol {
			return n.ID
		}
	}
	return ""
}

// sampleAccount is one subscriber that selected every fixture node.
func sampleAccount() user.User {
	return user.New("demo", selections(sampleNodes()), time.Unix(0, 0))
}

// uris flattens share links into the URIs a client would read.
func uris(links []ShareLink) []string {
	out := make([]string, 0, len(links))
	for _, link := range links {
		out = append(out, link.URI)
	}
	return out
}

func TestDeepLinkEncodesURL(t *testing.T) {
	link := DeepLink("http://example.com:8443/sub/abc")
	if !strings.HasPrefix(link, ImportScheme) {
		t.Fatalf("link missing scheme: %q", link)
	}
	if strings.Contains(link, "://example.com") {
		t.Fatalf("url not percent-encoded: %q", link)
	}
	if !strings.Contains(link, "url=http%3A%2F%2Fexample.com") {
		t.Fatalf("unexpected encoding: %q", link)
	}
}

func TestShareLinks(t *testing.T) {
	c := sampleConfig()
	nodes := sampleNodes()
	account := sampleAccount()
	links := ShareLinks(c, nodes, account)
	joined := strings.Join(uris(links), "\n")
	for _, prefix := range []string{"anytls://", "hysteria2://", "tuic://", "vmess://", "vless://"} {
		if !strings.Contains(joined, prefix) {
			t.Fatalf("missing %s link in:\n%s", prefix, joined)
		}
	}

	var vmess string
	for _, l := range uris(links) {
		if strings.HasPrefix(l, "vmess://") {
			vmess = strings.TrimPrefix(l, "vmess://")
		}
	}
	raw, err := base64.StdEncoding.DecodeString(vmess)
	if err != nil {
		t.Fatalf("vmess base64 decode: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("vmess json decode: %v", err)
	}
	if payload["add"] != "203.0.113.10" || payload["id"] != account.Credential(nodeID(state.ProtoVMessWSTLS)).UUID {
		t.Fatalf("unexpected vmess payload: %v", payload)
	}
	// Some parsers read only "security"; v2rayN and mihomo read "scy".
	if payload["scy"] != "auto" || payload["security"] != "auto" {
		t.Fatalf("vmess encryption must be set under both keys: %v", payload)
	}
}

func TestShareLinksCoverEveryProtocol(t *testing.T) {
	c := sampleConfig()
	nodes := sampleNodes()
	links := ShareLinks(c, nodes, sampleAccount())
	if len(links) != len(nodes) {
		t.Fatalf("expected one link per node, got %d", len(links))
	}
	for i, n := range nodes {
		if links[i].Key != n.ID {
			t.Fatalf("link %d belongs to %s, want %s", i, links[i].Key, n.ID)
		}
	}
}

func TestShareLinksRespectsDisabled(t *testing.T) {
	c := sampleConfig()
	nodes := sampleNodes()
	for i := range nodes {
		nodes[i].Enabled = nodes[i].Protocol == state.ProtoVLESSReality
	}
	links := ShareLinks(c, nodes, sampleAccount())
	if len(links) != 1 || !strings.HasPrefix(links[0].URI, "vless://") {
		t.Fatalf("expected only vless link, got %v", uris(links))
	}
}

func TestShareLinksRespectsAccountSelection(t *testing.T) {
	c := sampleConfig()
	nodes := sampleNodes()
	account := sampleAccount()
	for _, n := range nodes {
		if n.Protocol != state.ProtoHysteria2 {
			account.Deselect(n.ID)
		}
	}
	links := ShareLinks(c, nodes, account)
	if len(links) != 1 || links[0].Key != nodeID(state.ProtoHysteria2) {
		t.Fatalf("expected only the hysteria2 link, got %v", uris(links))
	}
}

func TestActiveNodesFollowNodeAndAccount(t *testing.T) {
	nodes := sampleNodes()
	account := sampleAccount()
	if got := len(ActiveNodes(nodes, account)); got != len(nodes) {
		t.Fatalf("active nodes = %d, want %d", got, len(nodes))
	}

	for i := range nodes {
		if nodes[i].Protocol == state.ProtoTUIC {
			nodes[i].Enabled = false
		}
	}
	account.Deselect(nodeID(state.ProtoAnyTLS))
	active := ActiveNodes(nodes, account)
	for _, n := range active {
		if n.Protocol == state.ProtoTUIC || n.Protocol == state.ProtoAnyTLS {
			t.Fatalf("inactive node leaked: %v", active)
		}
	}
	if len(active) != len(nodes)-2 {
		t.Fatalf("active nodes = %v", active)
	}
}

func TestVLESSShareLinkKeepsCanonicalUUID(t *testing.T) {
	c := sampleConfig()
	nodes := sampleNodes()
	account := sampleAccount()
	var vless string
	for _, l := range uris(ShareLinks(c, nodes, account)) {
		if strings.HasPrefix(l, "vless://") {
			vless = l
		}
	}
	// homeproxy validates the node UUID with the LuCI uuid check and rejects
	// the 32 character form, so the share link must keep the canonical UUID.
	if !strings.HasPrefix(vless, "vless://"+account.Credential(nodeID(state.ProtoVLESSReality)).UUID+"@") {
		t.Fatalf("vless link should carry the canonical UUID: %s", vless)
	}
}

func TestSubscriptionEndpoint(t *testing.T) {
	c := sampleConfig()
	account := sampleAccount()
	want := "http://203.0.113.10:8443/sub/" + account.Token
	if got := Endpoint(c); got != "http://203.0.113.10:8443/sub/" {
		t.Fatalf("Endpoint = %q", got)
	}
	if got := SubscriptionURL(c, account.Token); got != want {
		t.Fatalf("SubscriptionURL = %q, want %q", got, want)
	}
	// Clash-family clients fetch the scanned payload as a profile URL, so the
	// mihomo QR must carry the plain endpoint rather than a clash:// deep link;
	// v2rayN imports the plain URL as well.
	if got := ClientLink(c, account.Token, ClientMihomo); got != want {
		t.Fatalf("mihomo link should be the plain URL: %q", got)
	}
	if got := ClientLink(c, account.Token, ClientV2Ray); got != want {
		t.Fatalf("v2ray link should be the plain URL: %q", got)
	}
	if got := ClientLink(c, account.Token, ClientSingBox); !strings.HasPrefix(got, ImportScheme) {
		t.Fatalf("sing-box link missing deep link scheme: %q", got)
	}
}

func TestShareLinksEncodeCredentials(t *testing.T) {
	c := sampleConfig()
	nodes := sampleNodes()
	account := sampleAccount()
	for _, protocol := range []string{state.ProtoAnyTLS, state.ProtoHysteria2} {
		id := nodeID(protocol)
		cred := account.Credential(id)
		cred.Password = "p@ss/word"
		account.Credentials[id] = cred
	}
	joined := strings.Join(uris(ShareLinks(c, nodes, account)), "\n")
	if strings.Contains(joined, "p@ss/word") {
		t.Fatalf("password not percent-encoded:\n%s", joined)
	}
	// AnyTLS and Hysteria2 URIs require a slash before the query, otherwise
	// clients reject the link.
	if !strings.Contains(joined, "anytls://p%40ss%2Fword@203.0.113.10:8000/?") {
		t.Fatalf("anytls link malformed:\n%s", joined)
	}
	if !strings.Contains(joined, "hysteria2://p%40ss%2Fword@203.0.113.10:8001/?") {
		t.Fatalf("hysteria2 link malformed:\n%s", joined)
	}
}

func TestV2RayDocument(t *testing.T) {
	c := sampleConfig()
	nodes := sampleNodes()
	account := sampleAccount()
	raw, err := base64.StdEncoding.DecodeString(V2RayDocument(c, nodes, account))
	if err != nil {
		t.Fatalf("decode v2ray document: %v", err)
	}
	if !strings.Contains(string(raw), "vless://") {
		t.Fatalf("v2ray document missing share links:\n%s", raw)
	}
	if !strings.HasSuffix(string(raw), "\n") {
		t.Fatalf("v2ray document should end with a newline: %q", raw)
	}
}

func TestNodeNameIncludesAccount(t *testing.T) {
	name := NodeName("alice", "vless-reality")
	if !strings.Contains(name, "alice") || !strings.Contains(name, "vless-reality") {
		t.Fatalf("node name = %q", name)
	}
}
