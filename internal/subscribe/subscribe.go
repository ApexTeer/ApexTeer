// Package subscribe renders one account's client documents: the sing-box JSON
// profile, the mihomo YAML profile, the base64 share-link document and the
// share links themselves. Every document is derived from the node store plus a
// single account: an account owns one credential per node it selected, so two
// nodes of the same protocol produce two independent entries.
package subscribe

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/EasySBTeam/EasySB/internal/cert"
	"github.com/EasySBTeam/EasySB/internal/node"
	"github.com/EasySBTeam/EasySB/internal/state"
	"github.com/EasySBTeam/EasySB/internal/user"
)

// Client identifies one subscription document format.
type Client string

const (
	// ClientSingBox serves the JSON profile consumed by sing-box (SFM/SFA/SFI).
	ClientSingBox Client = "singbox"
	// ClientMihomo serves a complete mihomo / Clash Meta YAML profile.
	ClientMihomo Client = "mihomo"
	// ClientV2Ray serves the base64 share-link document. It is the universal
	// format: v2rayN reads it directly, and passwall, passwall2 and homeproxy
	// all base64-decode the same document before parsing.
	ClientV2Ray Client = "v2ray"
)

// Clients lists the supported subscription formats in menu order.
var Clients = []Client{ClientSingBox, ClientMihomo, ClientV2Ray}

// ImportScheme is the sing-box client deep link used to import a remote profile.
// A bare subscription URL is not recognised by the client, which is what broke
// QR scanning in the legacy build.
const ImportScheme = "sing-box://import-remote-profile?url="

// SubPathPrefix is the single endpoint every format is served from. The format
// is chosen from the client's User-Agent, so one URL works in every client.
const SubPathPrefix = "/sub/"

// tagFor maps a protocol key to the tag of its outbound block in the client
// templates.
var tagFor = map[string]string{
	state.ProtoAnyTLS:       "anytls",
	state.ProtoHysteria2:    "hysteria2",
	state.ProtoTUIC:         "tuic",
	state.ProtoVMessWSTLS:   "vmess-ws-tls",
	state.ProtoVLESSReality: "vless-vision-reality",
}

// NodeName is the display name of one node in a client: the account name and
// the node name are both part of it, so a user who imports several
// subscriptions can tell them apart and two same-protocol nodes never collide.
func NodeName(username, nodeName string) string {
	return "EasySB-" + username + "-" + nodeName
}

// ActiveNodes returns the nodes an account may use, in the store's name order:
// every enabled node the account selected and whose credential is complete. It
// is the single predicate behind the documents, the share links and the node
// names, so the profile never advertises a node the core does not accept
// credentials for.
func ActiveNodes(nodes []node.Node, u user.User) []node.Node {
	var out []node.Node
	for _, n := range nodes {
		if !n.Enabled || !u.Selects(n.ID) || !u.CredentialReady(n.ID) {
			continue
		}
		out = append(out, n)
	}
	return out
}

// ContentType returns the MIME type of a format's document.
func ContentType(client Client) string {
	switch client {
	case ClientMihomo:
		return "text/yaml; charset=utf-8"
	case ClientV2Ray:
		return "text/plain; charset=utf-8"
	default:
		return "application/json; charset=utf-8"
	}
}

// Document renders the document one client format fetches.
func Document(cfg state.Config, nodes []node.Node, u user.User, client Client) ([]byte, error) {
	switch client {
	case ClientMihomo:
		return GenerateMihomo(cfg, nodes, u)
	case ClientV2Ray:
		return []byte(V2RayDocument(cfg, nodes, u)), nil
	default:
		return Generate(cfg, nodes, u)
	}
}

// SubscriptionURL returns the endpoint an account fetches. The token is the
// only credential in the URL, so the document is not guessable and can be
// revoked by rotating the token.
func SubscriptionURL(cfg state.Config, token string) string {
	return Endpoint(cfg) + token
}

// Endpoint returns the base of the subscription endpoint, without an account
// token. Every account's URL is this base plus its token.
func Endpoint(cfg state.Config) string {
	return baseURL(cfg) + SubPathPrefix
}

// ClientLink wraps the subscription URL into the payload a client imports from
// a QR code. Only sing-box needs a deep link: its scanner expects
// sing-box://import-remote-profile. Clash-family scanners (FlClash, Clash Meta)
// fetch the scanned text as a profile URL, so wrapping it in clash:// makes the
// import fail; they receive the plain endpoint instead. v2rayN likewise imports
// the plain URL.
func ClientLink(cfg state.Config, token string, client Client) string {
	subURL := SubscriptionURL(cfg, token)
	if client == ClientSingBox {
		return DeepLink(subURL)
	}
	return subURL
}

// baseURL returns scheme://host:port for subscription links. The scheme follows
// the certificate the endpoint can actually serve, so the URL a client is handed
// never disagrees with the listener behind it.
func baseURL(cfg state.Config) string {
	host := cfg.Host()
	scheme := "http"
	if cert.Usable(cfg.Domain) {
		scheme = "https"
	}
	// JoinHostPort brackets an IPv6 literal, which cfg.Host() can return when
	// SERVER_IP is one: host:port would leave the address and the port as a single
	// unparseable field.
	return fmt.Sprintf("%s://%s", scheme, net.JoinHostPort(host, strconv.Itoa(cfg.SubPort())))
}

// DeepLink wraps a subscription URL into the sing-box import deep link.
func DeepLink(subURL string) string {
	return ImportScheme + url.QueryEscape(subURL)
}

// ShareLink is one share URI together with the node it encodes, so a caller can
// label it without re-deriving which node it belongs to.
type ShareLink struct {
	Key  string
	Name string
	URI  string
}

// ShareLinks returns the account's share links in node order. Every URI follows
// the de-facto scheme each client parses, so the same link works in sing-box,
// mihomo, v2rayN and friends.
func ShareLinks(cfg state.Config, nodes []node.Node, u user.User) []ShareLink {
	host := cfg.Host()
	var links []ShareLink
	for _, n := range ActiveNodes(nodes, u) {
		cred := u.Credential(n.ID)
		name := NodeName(u.Name, n.Name)
		var link string
		switch n.Protocol {
		case state.ProtoAnyTLS:
			link = anytlsLink(n, cred, host, name)
		case state.ProtoHysteria2:
			link = hysteria2Link(n, cred, host, name)
		case state.ProtoTUIC:
			link = tuicLink(n, cred, host, name)
		case state.ProtoVMessWSTLS:
			link = vmessLink(n, cred, host, name)
		case state.ProtoVLESSReality:
			link = vlessLink(n, cred, host, name)
		}
		if link != "" {
			links = append(links, ShareLink{Key: n.ID, Name: name, URI: link})
		}
	}
	return links
}

// V2RayDocument encodes the share links as one base64 document, the
// subscription format v2rayN and similar clients import.
func V2RayDocument(cfg state.Config, nodes []node.Node, u user.User) string {
	links := ShareLinks(cfg, nodes, u)
	uris := make([]string, 0, len(links))
	for _, link := range links {
		uris = append(uris, link.URI)
	}
	return base64.StdEncoding.EncodeToString([]byte(strings.Join(uris, "\n") + "\n"))
}

// fragment renders a node name for the URI fragment. Percent-encoding keeps a
// non-ASCII account name from breaking the URI grammar.
func fragment(name string) string {
	return url.PathEscape(name)
}

func anytlsLink(n node.Node, cred user.Credentials, host, name string) string {
	if cred.Password == "" {
		return ""
	}
	q := url.Values{}
	q.Set("sni", host)
	q.Set("insecure", "0")
	// The trailing slash before the query is required by the AnyTLS URI spec;
	// omitting it makes clients reject the link.
	return fmt.Sprintf("anytls://%s@%s/?%s#%s",
		url.User(cred.Password).String(), net.JoinHostPort(host, strconv.Itoa(n.Port)), q.Encode(),
		fragment(name))
}

func hysteria2Link(n node.Node, cred user.Credentials, host, name string) string {
	if cred.Password == "" {
		return ""
	}
	q := url.Values{}
	q.Set("sni", host)
	q.Set("insecure", "0")
	if hop := n.Param(node.ParamHopRange); hop != "" {
		// hysteria2 share links carry the hopping range as mport.
		q.Set("mport", strings.ReplaceAll(hop, ":", "-"))
	}
	return fmt.Sprintf("hysteria2://%s@%s/?%s#%s",
		url.User(cred.Password).String(), net.JoinHostPort(host, strconv.Itoa(n.Port)), q.Encode(),
		fragment(name))
}

func tuicLink(n node.Node, cred user.Credentials, host, name string) string {
	if cred.UUID == "" || cred.Password == "" {
		return ""
	}
	q := url.Values{}
	q.Set("congestion_control", "bbr")
	q.Set("udp_relay_mode", "native")
	q.Set("alpn", "h3")
	q.Set("sni", host)
	q.Set("insecure", "0")
	return fmt.Sprintf("tuic://%s@%s?%s#%s",
		url.UserPassword(cred.UUID, cred.Password).String(), net.JoinHostPort(host, strconv.Itoa(n.Port)), q.Encode(),
		fragment(name))
}

func vlessLink(n node.Node, cred user.Credentials, host, name string) string {
	if cred.UUID == "" {
		return ""
	}
	sni := n.Param(node.ParamRealitySNI)
	if sni == "" {
		sni = state.DefaultSNI
	}
	q := url.Values{}
	q.Set("type", "tcp")
	q.Set("encryption", "none")
	q.Set("flow", "xtls-rprx-vision")
	q.Set("security", "reality")
	q.Set("sni", sni)
	q.Set("fp", "chrome")
	q.Set("pbk", n.Param(node.ParamRealityPublic))
	q.Set("sid", n.Param(node.ParamRealityShortID))
	return fmt.Sprintf("vless://%s@%s?%s#%s",
		url.User(cred.UUID).String(), net.JoinHostPort(host, strconv.Itoa(n.Port)), q.Encode(),
		fragment(name))
}

func vmessLink(n node.Node, cred user.Credentials, host, name string) string {
	if cred.UUID == "" {
		return ""
	}
	payload := map[string]any{
		"v":        "2",
		"ps":       name,
		"add":      host,
		"port":     strconv.Itoa(n.Port),
		"id":       cred.UUID,
		"aid":      "0",
		"scy":      "auto",
		"security": "auto",
		"net":      "ws",
		"type":     "none",
		"host":     host,
		"path":     "/vmess",
		"tls":      "tls",
		"sni":      host,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return "vmess://" + base64.StdEncoding.EncodeToString(raw)
}

// QRCode renders payload as a terminal QR block art using qrencode.
func QRCode(payload string) (string, error) {
	path, err := exec.LookPath("qrencode")
	if err != nil {
		return "", fmt.Errorf("qrencode not installed")
	}
	// The renderer runs under a timeout so a qrencode that hangs cannot hold the
	// task goroutine open past the cancellation of the run that started it.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "-t", "UTF8", payload).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("qrencode: %w", err)
	}
	return strings.TrimRight(string(out), "\n"), nil
}
