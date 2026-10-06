//go:build with_quic && with_utls

package deploy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EasySB-Team/EasySB/internal/sbcore"
	"github.com/EasySB-Team/EasySB/internal/secret"
	"github.com/EasySB-Team/EasySB/internal/state"
	"github.com/EasySB-Team/EasySB/internal/user"
)

// TestEveryProtocolIsAcceptedByTheCarriedCore renders a node with the tagged protocols
// too: Hysteria2 and TUIC live behind with_quic and the Reality inbound behind with_utls,
// so this is the test that pins the release tag set (release/TAGS). What it proves is
// that the document the panel renders for a five-protocol node is accepted by the core
// inside this very binary — the arrangement the panel now depends on, since there is no
// downloaded core to fall back on.
func TestEveryProtocolIsAcceptedByTheCarriedCore(t *testing.T) {
	const domain = "example.com"
	installTestCertificate(t, t.TempDir(), domain)

	cfg := state.Default()
	cfg.Domain = domain
	for _, key := range state.Keys {
		cfg.Enabled[key] = true
	}
	cfg.RealityPriv, cfg.RealityPub = secret.RealityKeypair()
	cfg.RealitySID = secret.ShortID()

	account := testAccount(t)
	document, err := ServerConfig(cfg, []user.User{account})
	if err != nil {
		t.Fatalf("ServerConfig: %v", err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, document, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := sbcore.Check(context.Background(), path); err != nil {
		t.Fatalf("the carried core refused a five-protocol node: %v\n%s", err, document)
	}
	for _, want := range []string{`"type": "hysteria2"`, `"type": "tuic"`, `"type": "vless"`, `"type": "anytls"`, `"type": "vmess"`} {
		if !strings.Contains(string(document), want) {
			t.Fatalf("the document does not carry %s:\n%s", want, document)
		}
	}
}
