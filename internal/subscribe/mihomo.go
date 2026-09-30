package subscribe

import (
	_ "embed"
	"fmt"
	"strings"
	"text/template"

	"github.com/MinimaxFlora/EasySB/internal/state"
	"github.com/MinimaxFlora/EasySB/internal/user"
)

//go:embed mihomo.yaml
var mihomoTemplate string

var mihomoTpl = template.Must(template.New("mihomo").Parse(mihomoTemplate))

// GenerateMihomo renders a complete mihomo / Clash Meta profile for one
// account.
func GenerateMihomo(cfg state.Config, u user.User) ([]byte, error) {
	if cfg.Host() == "" {
		return nil, fmt.Errorf("no server address")
	}
	active := ActiveTags(cfg, u)
	if len(active) == 0 {
		return nil, fmt.Errorf("no protocol enabled for %q", u.Name)
	}
	hop := cfg.HopRange
	if hop == "" {
		hop = state.DefaultHopRange
	}
	rsni := cfg.RealitySNI
	if rsni == "" {
		rsni = state.DefaultSNI
	}

	var proxies, nodes strings.Builder
	for _, tag := range active {
		name, block := mihomoProxy(tag, cfg, u, hop, rsni)
		if block == "" {
			continue
		}
		proxies.WriteString(block)
		nodes.WriteString("      - " + yamlString(name) + "\n")
	}

	var out strings.Builder
	if err := mihomoTpl.Execute(&out, map[string]string{
		"Proxies": proxies.String(),
		"Nodes":   nodes.String(),
	}); err != nil {
		return nil, err
	}
	return []byte(out.String()), nil
}

// mihomoProxy renders one proxy entry and returns its name and YAML block.
func mihomoProxy(tag string, cfg state.Config, u user.User, hop, rsni string) (string, string) {
	host := cfg.Host()
	name := NodeName(u.Name, tag)
	var b strings.Builder

	switch tag {
	case "anytls":
		fmt.Fprintf(&b, "  - name: %s\n", yamlString(name))
		b.WriteString("    type: anytls\n")
		yamlKV(&b, "server", host)
		yamlInt(&b, "port", portInt(cfg, state.ProtoAnyTLS))
		yamlKV(&b, "password", u.Credential(state.ProtoAnyTLS).Password)
		b.WriteString("    client-fingerprint: chrome\n")
		b.WriteString("    udp: true\n")
		b.WriteString("    idle-session-check-interval: 30\n")
		b.WriteString("    idle-session-timeout: 30\n")
		b.WriteString("    min-idle-session: 5\n")
		yamlKV(&b, "sni", host)
		b.WriteString("    alpn:\n      - h3\n      - h2\n      - http/1.1\n")
		b.WriteString("    skip-cert-verify: false\n")
	case "hysteria2":
		fmt.Fprintf(&b, "  - name: %s\n", yamlString(name))
		b.WriteString("    type: hysteria2\n")
		yamlKV(&b, "server", host)
		yamlInt(&b, "port", portInt(cfg, state.ProtoHysteria2))
		yamlKV(&b, "ports", strings.ReplaceAll(hop, ":", "-"))
		yamlKV(&b, "password", u.Credential(state.ProtoHysteria2).Password)
		yamlKV(&b, "sni", host)
		b.WriteString("    alpn:\n      - h3\n")
		b.WriteString("    up: \"20 Mbps\"\n")
		b.WriteString("    down: \"100 Mbps\"\n")
		b.WriteString("    hop-interval: 30\n")
		b.WriteString("    fast-open: true\n")
		b.WriteString("    skip-cert-verify: false\n")
	case "tuic":
		fmt.Fprintf(&b, "  - name: %s\n", yamlString(name))
		b.WriteString("    type: tuic\n")
		yamlKV(&b, "server", host)
		yamlInt(&b, "port", portInt(cfg, state.ProtoTUIC))
		yamlKV(&b, "uuid", u.Credential(state.ProtoTUIC).UUID)
		yamlKV(&b, "password", u.Credential(state.ProtoTUIC).Password)
		yamlKV(&b, "sni", host)
		b.WriteString("    alpn:\n      - h3\n")
		b.WriteString("    reduce-rtt: false\n")
		b.WriteString("    udp-relay-mode: native\n")
		b.WriteString("    congestion-controller: bbr\n")
		b.WriteString("    skip-cert-verify: false\n")
	case "vmess-ws-tls":
		fmt.Fprintf(&b, "  - name: %s\n", yamlString(name))
		b.WriteString("    type: vmess\n")
		yamlKV(&b, "server", host)
		yamlInt(&b, "port", portInt(cfg, state.ProtoVMessWSTLS))
		yamlKV(&b, "uuid", u.Credential(state.ProtoVMessWSTLS).UUID)
		b.WriteString("    alterId: 0\n")
		b.WriteString("    cipher: auto\n")
		b.WriteString("    udp: true\n")
		b.WriteString("    tls: true\n")
		b.WriteString("    skip-cert-verify: false\n")
		yamlKV(&b, "servername", host)
		b.WriteString("    client-fingerprint: chrome\n")
		b.WriteString("    network: ws\n")
		b.WriteString("    ws-opts:\n")
		b.WriteString("      path: /vmess\n")
		b.WriteString("      headers:\n")
		fmt.Fprintf(&b, "        Host: %s\n", yamlString(host))
	case "vless-vision-reality":
		fmt.Fprintf(&b, "  - name: %s\n", yamlString(name))
		b.WriteString("    type: vless\n")
		yamlKV(&b, "server", host)
		yamlInt(&b, "port", portInt(cfg, state.ProtoVLESSReality))
		yamlKV(&b, "uuid", u.Credential(state.ProtoVLESSReality).UUID)
		b.WriteString("    network: tcp\n")
		b.WriteString("    udp: true\n")
		b.WriteString("    tls: true\n")
		b.WriteString("    flow: xtls-rprx-vision\n")
		yamlKV(&b, "servername", rsni)
		b.WriteString("    client-fingerprint: chrome\n")
		b.WriteString("    reality-opts:\n")
		yamlKVIndent(&b, "      ", "public-key", cfg.RealityPub)
		yamlKVIndent(&b, "      ", "short-id", cfg.RealitySID)
	default:
		return "", ""
	}
	return name, b.String()
}

func yamlKV(b *strings.Builder, key, value string) {
	yamlKVIndent(b, "    ", key, value)
}

func yamlKVIndent(b *strings.Builder, indent, key, value string) {
	fmt.Fprintf(b, "%s%s: %s\n", indent, key, yamlString(value))
}

func yamlInt(b *strings.Builder, key string, value int) {
	fmt.Fprintf(b, "    %s: %d\n", key, value)
}

// yamlString double-quotes a scalar and escapes it so node names and
// credentials can never break the document.
func yamlString(s string) string {
	// The backslash is escaped first, so the escapes added below are not escaped
	// again. A newline, carriage return or tab would otherwise end the scalar and
	// split the document.
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	s = strings.ReplaceAll(s, "\n", "\\n")
	s = strings.ReplaceAll(s, "\r", "\\r")
	s = strings.ReplaceAll(s, "\t", "\\t")
	return "\"" + s + "\""
}
