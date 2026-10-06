package subscribe

import (
	"strings"
	"testing"

	"github.com/EasySBTeam/EasySB/internal/state"
)

func TestGenerateMihomo(t *testing.T) {
	cfg := testConfig()
	cfg.Enabled[state.ProtoTUIC] = false
	cfg.Enabled[state.ProtoAnyTLS] = false

	acct := testAccount()
	data, err := GenerateMihomo(cfg, acct)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	for _, want := range []string{
		"mixed-port: 7890",
		"external-controller: 127.0.0.1:9090",
		`secret: "` + clashSecret(acct) + `"`,
		"external-ui: ui",
		"external-ui-url:",
		"unified-delay: true",
		"proxy-groups:",
		"type: load-balance",
		"🌍选择代理节点",
		"type: hysteria2",
		"type: vmess",
		"type: vless",
		"hop-interval: 30",
		`up: "20 Mbps"`,
		"fast-open: true",
		"reality-opts:",
		`"apple.com"`,
		"GEOSITE,CN,DIRECT",
		"MATCH,🌍选择代理节点",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("mihomo config missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "type: tuic") || strings.Contains(body, "type: anytls") {
		t.Fatalf("disabled protocols leaked into mihomo config:\n%s", body)
	}
}

func TestGenerateMihomoAnyTLS(t *testing.T) {
	cfg := testConfig()
	for _, tag := range []string{
		state.ProtoHysteria2,
		state.ProtoTUIC,
		state.ProtoVLESSReality,
		state.ProtoVMessWSTLS,
	} {
		cfg.Enabled[tag] = false
	}
	acct := testAccount()
	data, err := GenerateMihomo(cfg, acct)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	for _, want := range []string{
		"type: anytls",
		"idle-session-check-interval: 30",
		"idle-session-timeout: 30",
		"min-idle-session: 5",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("anytls config missing %q:\n%s", want, body)
		}
	}
}

func TestGenerateMihomoRequiresHost(t *testing.T) {
	cfg := testConfig()
	cfg.Domain = ""
	cfg.ServerIP = ""
	if _, err := GenerateMihomo(cfg, testAccount()); err == nil {
		t.Fatal("expected error without a server address")
	}
}

// TestGenerateMihomoTemplateActionsNotInComments guards against reintroducing
// template actions inside YAML comments, which would inject proxy entries above
// the document root and make the profile unparseable.
func TestGenerateMihomoTemplateActionsNotInComments(t *testing.T) {
	cfg := testConfig()
	acct := testAccount()
	data, err := GenerateMihomo(cfg, acct)
	if err != nil {
		t.Fatal(err)
	}
	inProxies := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "proxies:" {
			inProxies = true
			continue
		}
		if inProxies || trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "type:") {
			t.Fatalf("proxy entry leaked before the proxies: key: %q", line)
		}
	}
}
