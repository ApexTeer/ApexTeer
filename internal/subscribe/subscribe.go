// Package subscribe renders one account's client documents: the sing-box JSON
// profile, the mihomo YAML profile, the base64 share-link document and the
// share links themselves. Every document is derived from the node state plus a
// single account, because v4 gives each account its own credentials.
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

// tagFor maps a state protocol key to the tag used inside the client templates.
var tagFor = map[string]string{
	state.ProtoAnyTLS:       "anytls",
	state.ProtoHysteria2:    "hysteria2",
	state.ProtoTUIC:         "tuic",
	state.ProtoVMessWSTLS:   "vmess-ws-tls",
	state.ProtoVLESSReality: "vless-vision-reality",
}

// keyForTag is the reverse of tagFor.
var keyForTag = map[string]string{
	"anytls":               state.ProtoAnyTLS,
	"hysteria2":            state.ProtoHysteria2,
	"tuic":                 state.ProtoTUIC,
	"vmess-ws-tls":         state.ProtoVMessWSTLS,
	"vless-vision-reality": state.ProtoVLESSReality,
}

// tagOrder is the canonical template node order.
var tagOrder = []string{"anytls", "hysteria2", "tuic", "vmess-ws-tls", "vless-vision-reality"}

// nodeLabels maps a client template tag to the suffix used in client node
// names.
var nodeLabels = map[string]string{
	"anytls":               "AnyTLS",
	"hysteria2":            "Hysteria2",
	"tuic":                 "TUIC",
	"vmess-ws-tls":         "VMess-WS-TLS",
	"vless-vision-reality": "VLESS-Reality",
}

// NodeName is the display name of one node in a client: the account name is
// part of it so a user who imports several subscriptions can tell them apart,
// and so a screenshot of a connected client identifies who is connected.
func NodeName(username, tag string) string {
	return "EasySB-" + username + "-" + nodeLabels[tag]
}

// ActiveTags returns the client template tags an account may use, in canonical
// template order: the intersection of what the node serves and what the account
// selected. It is the single predicate behind the documents, the share links
// and the node names, so the profile never advertises a node the core does not
// accept credentials for.
func ActiveTags(cfg state.Config, u user.User) []string {
	var out []string
	for _, tag := range tagOrder {
		key := keyForTag[tag]
		if !cfg.Enabled[key] || !u.Selects(key) {
			continue
		}
		if !u.CredentialReady(key) {
			continue
		}
		out = append(out, tag)
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
func Document(cfg state.Config, u user.User, client Client) ([]byte, error) {
	switch client {
	case ClientMihomo:
		return GenerateMihomo(cfg, u)
	case ClientV2Ray:
		return []byte(V2RayDocument(cfg, u)), nil
	default:
		return Generate(cfg, u)
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

// ShareLink is one share URI together with the protocol it encodes, so a caller
// can label it without re-deriving which node it belongs to.
type ShareLink struct {
	Key string
	URI string
}

// ShareLinks returns the account's share links in canonical order. Every URI
// follows the de-facto scheme each client parses, so the same link works in
// sing-box, mihomo, v2rayN and friends.
func ShareLinks(cfg state.Config, u user.User) []ShareLink {
	host := cfg.Host()
	var links []ShareLink
	for _, key := range state.Keys {
		if !cfg.Enabled[key] || !u.Selects(key) {
			continue
		}
		cred := u.Credential(key)
		var link string
		switch key {
		case state.ProtoAnyTLS:
			link = anytlsLink(cfg, u.Name, cred, host)
		case state.ProtoHysteria2:
			link = hysteria2Link(cfg, u.Name, cred, host)
		case state.ProtoTUIC:
			link = tuicLink(cfg, u.Name, cred, host)
		case state.ProtoVMessWSTLS:
			link = vmessLink(cfg, u.Name, cred, host)
		case state.ProtoVLESSReality:
			link = vlessLink(cfg, u.Name, cred, host)
		}
		if link != "" {
			links = append(links, ShareLink{Key: key, URI: link})
		}
	}
	return links
}

// V2RayDocument encodes the share links as one base64 document, the
// subscription format v2rayN and similar clients import.
func V2RayDocument(cfg state.Config, u user.User) string {
	links := ShareLinks(cfg, u)
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

func anytlsLink(cfg state.Config, username string, cred user.Credentials, host string) string {
	if cred.Password == "" {
		return ""
	}
	q := url.Values{}
	q.Set("sni", host)
	q.Set("insecure", "0")
	port := portOf(cfg, state.ProtoAnyTLS)
	// The trailing slash before the query is required by the AnyTLS URI spec;
	// omitting it makes clients reject the link.
	return fmt.Sprintf("anytls://%s@%s/?%s#%s",
		url.User(cred.Password).String(), net.JoinHostPort(host, port), q.Encode(),
		fragment(NodeName(username, tagFor[state.ProtoAnyTLS])))
}

func hysteria2Link(cfg state.Config, username string, cred user.Credentials, host string) string {
	if cred.Password == "" {
		return ""
	}
	q := url.Values{}
	q.Set("sni", host)
	q.Set("insecure", "0")
	if cfg.HopRange != "" {
		// hysteria2 share links carry the hopping range as mport.
		q.Set("mport", strings.ReplaceAll(cfg.HopRange, ":", "-"))
	}
	port := portOf(cfg, state.ProtoHysteria2)
	return fmt.Sprintf("hysteria2://%s@%s/?%s#%s",
		url.User(cred.Password).String(), net.JoinHostPort(host, port), q.Encode(),
		fragment(NodeName(username, tagFor[state.ProtoHysteria2])))
}

func tuicLink(cfg state.Config, username string, cred user.Credentials, host string) string {
	if cred.UUID == "" || cred.Password == "" {
		return ""
	}
	q := url.Values{}
	q.Set("congestion_control", "bbr")
	q.Set("udp_relay_mode", "native")
	q.Set("alpn", "h3")
	q.Set("sni", host)
	q.Set("insecure", "0")
	port := portOf(cfg, state.ProtoTUIC)
	return fmt.Sprintf("tuic://%s@%s?%s#%s",
		url.UserPassword(cred.UUID, cred.Password).String(), net.JoinHostPort(host, port), q.Encode(),
		fragment(NodeName(username, tagFor[state.ProtoTUIC])))
}

func vlessLink(cfg state.Config, username string, cred user.Credentials, host string) string {
	if cred.UUID == "" {
		return ""
	}
	sni := cfg.RealitySNI
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
	q.Set("pbk", cfg.RealityPub)
	q.Set("sid", cfg.RealitySID)
	port := portOf(cfg, state.ProtoVLESSReality)
	return fmt.Sprintf("vless://%s@%s?%s#%s",
		url.User(cred.UUID).String(), net.JoinHostPort(host, port), q.Encode(),
		fragment(NodeName(username, tagFor[state.ProtoVLESSReality])))
}

func portOf(cfg state.Config, key string) string {
	port := cfg.Ports[key]
	if port == "" {
		port = state.DefaultPorts[key]
	}
	return port
}

func vmessLink(cfg state.Config, username string, cred user.Credentials, host string) string {
	if cred.UUID == "" {
		return ""
	}
	name := NodeName(username, tagFor[state.ProtoVMessWSTLS])
	payload := map[string]any{
		"v":        "2",
		"ps":       name,
		"add":      host,
		"port":     portOf(cfg, state.ProtoVMessWSTLS),
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
