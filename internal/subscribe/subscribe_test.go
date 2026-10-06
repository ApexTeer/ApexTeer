package subscribe

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/EasySB-Team/EasySB/internal/state"
	"github.com/EasySB-Team/EasySB/internal/user"
)

// sampleNode is the node state the client documents are derived from.
func sampleNode() state.Config {
	c := state.Default()
	c.ServerIP = "203.0.113.10"
	c.RealityPub = "PUBKEY"
	c.RealitySID = "abcd1234"
	c.RealitySNI = "apple.com"
	return c
}

// sampleAccount is one subscriber, with a credential for every protocol.
func sampleAccount() user.User {
	return user.New("demo", state.Keys, time.Unix(0, 0))
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
	c := sampleNode()
	account := sampleAccount()
	links := ShareLinks(c, account)
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
	if payload["add"] != "203.0.113.10" || payload["id"] != account.Credential(state.ProtoVMessWSTLS).UUID {
		t.Fatalf("unexpected vmess payload: %v", payload)
	}
	// Some parsers read only "security"; v2rayN and mihomo read "scy".
	if payload["scy"] != "auto" || payload["security"] != "auto" {
		t.Fatalf("vmess encryption must be set under both keys: %v", payload)
	}
}

func TestShareLinksCoverEveryProtocol(t *testing.T) {
	c := sampleNode()
	links := ShareLinks(c, sampleAccount())
	if len(links) != len(state.Keys) {
		t.Fatalf("expected one link per protocol, got %d", len(links))
	}
	for i, key := range state.Keys {
		if links[i].Key != key {
			t.Fatalf("link %d belongs to %s, want %s", i, links[i].Key, key)
		}
	}
}

func TestShareLinksRespectsDisabled(t *testing.T) {
	c := sampleNode()
	for _, k := range state.Keys {
		c.Enabled[k] = k == state.ProtoVLESSReality
	}
	links := ShareLinks(c, sampleAccount())
	if len(links) != 1 || !strings.HasPrefix(links[0].URI, "vless://") {
		t.Fatalf("expected only vless link, got %v", uris(links))
	}
}

func TestShareLinksRespectsAccountSelection(t *testing.T) {
	c := sampleNode()
	account := sampleAccount()
	for _, key := range state.Keys {
		if key != state.ProtoHysteria2 {
			account.Deselect(key)
		}
	}
	links := ShareLinks(c, account)
	if len(links) != 1 || links[0].Key != state.ProtoHysteria2 {
		t.Fatalf("expected only the hysteria2 link, got %v", uris(links))
	}
}

func TestActiveTagsFollowNodeAndAccount(t *testing.T) {
	c := sampleNode()
	account := sampleAccount()
	if got := len(ActiveTags(c, account)); got != len(state.Keys) {
		t.Fatalf("active tags = %d, want %d", got, len(state.Keys))
	}

	c.Enabled[state.ProtoTUIC] = false
	account.Deselect(state.ProtoAnyTLS)
	tags := ActiveTags(c, account)
	for _, tag := range tags {
		if tag == "tuic" || tag == "anytls" {
			t.Fatalf("inactive tag leaked: %v", tags)
		}
	}
	if len(tags) != len(state.Keys)-2 {
		t.Fatalf("active tags = %v", tags)
	}
}

func TestVLESSShareLinkKeepsCanonicalUUID(t *testing.T) {
	c := sampleNode()
	account := sampleAccount()
	var vless string
	for _, l := range uris(ShareLinks(c, account)) {
		if strings.HasPrefix(l, "vless://") {
			vless = l
		}
	}
	// homeproxy validates the node UUID with the LuCI uuid check and rejects
	// the 32 character form, so the share link must keep the canonical UUID.
	if !strings.HasPrefix(vless, "vless://"+account.Credential(state.ProtoVLESSReality).UUID+"@") {
		t.Fatalf("vless link should carry the canonical UUID: %s", vless)
	}
}

func TestSubscriptionEndpoint(t *testing.T) {
	c := sampleNode()
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
	c := sampleNode()
	account := sampleAccount()
	for _, key := range []string{state.ProtoAnyTLS, state.ProtoHysteria2} {
		cred := account.Credential(key)
		cred.Password = "p@ss/word"
		account.Credentials[key] = cred
	}
	joined := strings.Join(uris(ShareLinks(c, account)), "\n")
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
	c := sampleNode()
	account := sampleAccount()
	raw, err := base64.StdEncoding.DecodeString(V2RayDocument(c, account))
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
	name := NodeName("alice", "vless-vision-reality")
	if !strings.Contains(name, "alice") || !strings.Contains(name, "VLESS") {
		t.Fatalf("node name = %q", name)
	}
}
