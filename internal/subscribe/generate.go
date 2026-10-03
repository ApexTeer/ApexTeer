package subscribe

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/MinimaxFlora/EasySB/internal/state"
	"github.com/MinimaxFlora/EasySB/internal/user"
)

//go:embed tun-fakeip.json
var templateJSON []byte

// Generate renders the sing-box client profile for one account.
func Generate(cfg state.Config, u user.User) ([]byte, error) {
	if cfg.Host() == "" {
		return nil, fmt.Errorf("no server address")
	}
	active := ActiveTags(cfg, u)
	if len(active) == 0 {
		return nil, fmt.Errorf("no protocol enabled for %q", u.Name)
	}
	if err := checkPorts(cfg, active); err != nil {
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
	hop := cfg.HopRange
	if hop == "" {
		hop = state.DefaultHopRange
	}
	rsni := cfg.RealitySNI
	if rsni == "" {
		rsni = state.DefaultSNI
	}

	// The template tags become the per-account node names. The selector and the
	// urltest group are rewritten from the same list, so a profile never
	// references a node it dropped.
	names := make(map[string]string, len(active))
	display := make([]string, 0, len(active))
	for _, tag := range active {
		names[tag] = NodeName(u.Name, tag)
		display = append(display, names[tag])
	}

	var kept []*jsonValue
	for _, ob := range outbounds.arr {
		tag := ob.get("tag").asString()
		switch tag {
		case "proxy", "auto", "direct":
			kept = append(kept, ob)
			continue
		}
		if !contains(active, tag) {
			continue
		}
		applyNode(ob, tag, cfg, u, hop, rsni)
		ob.setString("tag", names[tag])
		kept = append(kept, ob)
	}

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

func applyNode(ob *jsonValue, tag string, cfg state.Config, u user.User, hop, rsni string) {
	host := cfg.Host()
	ob.setString("server", host)
	switch tag {
	case "anytls":
		ob.setNumber("server_port", portInt(cfg, state.ProtoAnyTLS))
		ob.setString("password", u.Credential(state.ProtoAnyTLS).Password)
		setServerName(ob, host)
	case "hysteria2":
		ob.setStrings("server_ports", []string{hop})
		ob.setString("password", u.Credential(state.ProtoHysteria2).Password)
		setServerName(ob, host)
	case "tuic":
		cred := u.Credential(state.ProtoTUIC)
		ob.setNumber("server_port", portInt(cfg, state.ProtoTUIC))
		ob.setString("uuid", cred.UUID)
		ob.setString("password", cred.Password)
		setServerName(ob, host)
	case "vmess-ws-tls":
		ob.setNumber("server_port", portInt(cfg, state.ProtoVMessWSTLS))
		ob.setString("uuid", u.Credential(state.ProtoVMessWSTLS).UUID)
		setServerName(ob, host)
	case "vless-vision-reality":
		ob.setNumber("server_port", portInt(cfg, state.ProtoVLESSReality))
		ob.setString("uuid", u.Credential(state.ProtoVLESSReality).UUID)
		setServerName(ob, rsni)
		if tls := ob.get("tls"); tls != nil {
			if reality := tls.get("reality"); reality != nil {
				reality.setString("public_key", cfg.RealityPub)
				reality.setString("short_id", cfg.RealitySID)
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
func checkPorts(cfg state.Config, tags []string) error {
	for _, tag := range tags {
		if _, err := portNumber(cfg, keyForTag[tag]); err != nil {
			return err
		}
	}
	return nil
}

// portNumber resolves one protocol's listen port, falling back to the compiled
// default when the state file carries nothing.
func portNumber(cfg state.Config, key string) (int, error) {
	raw := strings.TrimSpace(cfg.Ports[key])
	if raw == "" {
		raw = strings.TrimSpace(state.DefaultPorts[key])
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("invalid port %q for %s", raw, key)
	}
	return n, nil
}

// portInt is portOf for the renderers, which validate every port up front with
// checkPorts before any of these calls run.
func portInt(cfg state.Config, key string) int {
	n, err := portNumber(cfg, key)
	if err != nil {
		return 0
	}
	return n
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

func contains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

// clashSecret derives the client's local management API secret from the account
// token, so every account gets a different value and none of them is the
// template's public default.
func clashSecret(u user.User) string {
	sum := sha256.Sum256([]byte("easysb-clash-api\x00" + u.Token))
	return hex.EncodeToString(sum[:16])
}
