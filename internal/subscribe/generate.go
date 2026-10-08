package subscribe

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/EasySBTeam/EasySB/internal/node"
	"github.com/EasySBTeam/EasySB/internal/state"
	"github.com/EasySBTeam/EasySB/internal/user"
)

//go:embed tun-fakeip.json
var templateJSON []byte

// Generate renders the sing-box client profile for one account.
func Generate(cfg state.Config, nodes []node.Node, u user.User) ([]byte, error) {
	if cfg.Host() == "" {
		return nil, fmt.Errorf("no server address")
	}
	active := ActiveNodes(nodes, u)
	if len(active) == 0 {
		return nil, fmt.Errorf("no node available for %q", u.Name)
	}
	if err := checkPorts(active); err != nil {
		return nil, err
	}

	root, err := parseOrderedJSON(stripJSONC(templateJSON))
	if err != nil {
		return nil, fmt.Errorf("parse subscription template: %w", err)
	}

	outbounds := root.get("outbounds")
	if !outbounds.array() {
		return nil, fmt.Errorf("subscription template has no outbounds")
	}
	// The template holds one outbound block per protocol. Two nodes of the same
	// protocol share a block, so each node gets its own clone.
	index := make(map[string]*jsonValue, len(outbounds.arr))
	for _, ob := range outbounds.arr {
		index[ob.get("tag").asString()] = ob
	}

	display := make([]string, 0, len(active))
	var nodeOutbounds []*jsonValue
	for _, n := range active {
		tmpl := index[tagFor[n.Protocol]]
		if tmpl == nil {
			continue
		}
		ob := tmpl.clone()
		applyNode(ob, n, cfg, u)
		name := NodeName(u.Name, n.Name)
		ob.setString("tag", name)
		display = append(display, name)
		nodeOutbounds = append(nodeOutbounds, ob)
	}
	if len(nodeOutbounds) == 0 {
		return nil, fmt.Errorf("no node available for %q", u.Name)
	}

	// Keep the proxy/auto/direct groups, then the per-node outbounds, and rewrite
	// the two groups from the same display list so a profile never references a
	// node it dropped.
	var kept []*jsonValue
	for _, ob := range outbounds.arr {
		switch ob.get("tag").asString() {
		case "proxy", "auto", "direct":
			kept = append(kept, ob)
		}
	}
	kept = append(kept, nodeOutbounds...)
	for _, ob := range kept {
		switch ob.get("tag").asString() {
		case "proxy":
			ob.setStrings("outbounds", append([]string{"auto"}, display...))
		case "auto":
			ob.setStrings("outbounds", append([]string{}, display...))
		}
	}
	outbounds.arr = kept

	// The Clash-compatible API is a management surface, so it stays on loopback and
	// its secret is derived per account: a fixed secret in the template would hand
	// every user the same, publicly known password.
	if api := root.get("experimental").get("clash_api"); api != nil {
		api.setString("secret", clashSecret(u))
	}

	return json.MarshalIndent(root, "", "  ")
}

// applyNode fills one outbound clone with a node's address, port, credential and
// protocol parameters.
func applyNode(ob *jsonValue, n node.Node, cfg state.Config, u user.User) {
	host := cfg.Host()
	cred := u.Credential(n.ID)
	ob.setString("server", host)
	switch n.Protocol {
	case state.ProtoAnyTLS:
		ob.setNumber("server_port", n.Port)
		ob.setString("password", cred.Password)
		setServerName(ob, host)
	case state.ProtoHysteria2:
		hop := n.Param(node.ParamHopRange)
		if hop == "" {
			hop = state.DefaultHopRange
		}
		ob.setStrings("server_ports", []string{hop})
		ob.setString("password", cred.Password)
		setServerName(ob, host)
	case state.ProtoTUIC:
		ob.setNumber("server_port", n.Port)
		ob.setString("uuid", cred.UUID)
		ob.setString("password", cred.Password)
		setServerName(ob, host)
	case state.ProtoVMessWSTLS:
		ob.setNumber("server_port", n.Port)
		ob.setString("uuid", cred.UUID)
		setServerName(ob, host)
	case state.ProtoVLESSReality:
		sni := n.Param(node.ParamRealitySNI)
		if sni == "" {
			sni = state.DefaultSNI
		}
		ob.setNumber("server_port", n.Port)
		ob.setString("uuid", cred.UUID)
		setServerName(ob, sni)
		if tls := ob.get("tls"); tls != nil {
			if reality := tls.get("reality"); reality != nil {
				reality.setString("public_key", n.Param(node.ParamRealityPublic))
				reality.setString("short_id", n.Param(node.ParamRealityShortID))
			}
		}
	}
}

func setServerName(ob *jsonValue, name string) {
	if tls := ob.get("tls"); tls != nil {
		tls.setString("server_name", name)
	}
}

// checkPorts refuses a document whose server_port would be invalid. Emitting
// server_port: 0 instead only reached the user as "cannot connect", with nothing
// in the panel naming the real cause.
func checkPorts(nodes []node.Node) error {
	for _, n := range nodes {
		if n.Port < 1 || n.Port > 65535 {
			return fmt.Errorf("node %q has invalid port %d", n.Name, n.Port)
		}
	}
	return nil
}

// stripJSONC removes // line comments that appear outside string literals.
func stripJSONC(in []byte) []byte {
	var out []byte
	for _, line := range strings.Split(string(in), "\n") {
		quoted := false
		cut := len(line)
		for i := 0; i < len(line); i++ {
			c := line[i]
			if quoted && c == '\\' && i+1 < len(line) {
				// Skip the character the escape quotes, so \" does not toggle the
				// string state and a // that follows it is still stripped.
				i++
				continue
			}
			if c == '"' {
				quoted = !quoted
				continue
			}
			if !quoted && c == '/' && i+1 < len(line) && line[i+1] == '/' {
				cut = i
				break
			}
		}
		out = append(out, line[:cut]...)
		out = append(out, '\n')
	}
	return out
}

// clashSecret derives the client's local management API secret from the account
// token, so every account gets a different value and none of them is the
// template's public default.
func clashSecret(u user.User) string {
	sum := sha256.Sum256([]byte("easysb-clash-api\x00" + u.Token))
	return hex.EncodeToString(sum[:16])
}
