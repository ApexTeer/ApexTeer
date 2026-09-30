package subscribe

import (
	_ "embed"
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

func portInt(cfg state.Config, key string) int {
	raw := cfg.Ports[key]
	if raw == "" {
		raw = state.DefaultPorts[key]
	}
	n, _ := strconv.Atoi(raw)
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
