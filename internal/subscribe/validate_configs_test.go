//go:build validate

package subscribe

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/EasySBTeam/EasySB/internal/sbcore"
	"github.com/EasySBTeam/EasySB/internal/secret"
)

func TestZZValidateRenderedDocuments(t *testing.T) {
	out := `D:\chatgpt\clash\out`
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig()
	priv, pub := secret.RealityKeypair()
	cfg.RealityPriv, cfg.RealityPub, cfg.RealitySID = priv, pub, secret.ShortID()
	acct := testAccount()

	js, err := Generate(cfg, acct)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	jp := filepath.Join(out, "client.json")
	if err := os.WriteFile(jp, js, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := sbcore.Check(context.Background(), jp); err != nil {
		t.Fatalf("sing-box REJECTED the generated config: %v", err)
	}
	t.Log("OK: sing-box accepted the rendered fixture " + jp)

	ym, err := GenerateMihomo(cfg, acct)
	if err != nil {
		t.Fatalf("GenerateMihomo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(out, "client.yaml"), ym, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Log("OK: wrote fixture yaml")
}

// Validates the config actually served by the live subscription, not a fixture.
func TestZZValidateLiveConfig(t *testing.T) {
	live := `D:\chatgpt\clash\out\live.json`
	if _, err := os.Stat(live); err != nil {
		t.Skip("no live config captured")
	}
	if err := sbcore.Check(context.Background(), live); err != nil {
		t.Fatalf("sing-box REJECTED the LIVE config: %v", err)
	}
	t.Log("OK: sing-box accepted the LIVE subscription config " + live)
}
