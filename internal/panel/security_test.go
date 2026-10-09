package panel

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/EasySBTeam/EasySB/internal/cert"
)

// TestBBRStatusAndValidation covers the read path and the input guard: the status
// is always answerable, and enabling BBR refuses a queue discipline that is not
// one of the ones the kernel project ships.
func TestBBRStatusAndValidation(t *testing.T) {
	svc := New(testOptions(t))

	rec := do(t, svc, http.MethodGet, "/api/v1/bbr", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("bbr status: %d %s", rec.Code, rec.Body.String())
	}
	out := decodeBody(t, rec)
	if _, ok := out["congestion"]; !ok {
		t.Fatalf("bbr status is missing congestion: %s", rec.Body.String())
	}
	qdiscs, ok := out["qdiscs"].([]any)
	if !ok || len(qdiscs) == 0 {
		t.Fatalf("bbr status must list the qdiscs: %s", rec.Body.String())
	}

	bad := do(t, svc, http.MethodPost, "/api/v1/bbr/enable", `{"qdisc":"not-a-qdisc"}`, nil)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("an unknown qdisc must be 400, got %d: %s", bad.Code, bad.Body.String())
	}
}

// TestSecurityReportsFirewallAndValidatesTLS covers the security surface: the
// firewall backend is always reported, and enabling TLS is refused unless the
// pair actually parses, so a bad certificate cannot be persisted.
func TestSecurityReportsFirewallAndValidatesTLS(t *testing.T) {
	svc := New(testOptions(t))

	rec := do(t, svc, http.MethodGet, "/api/v1/security", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("security: %d %s", rec.Code, rec.Body.String())
	}
	out := decodeBody(t, rec)
	fw, ok := out["firewall"].(map[string]any)
	if !ok || fw["backend"] == nil {
		t.Fatalf("security must report a firewall backend: %s", rec.Body.String())
	}

	// A missing pair is refused.
	missing := do(t, svc, http.MethodPost, "/api/v1/security/tls", `{"enabled":true}`, nil)
	if missing.Code != http.StatusBadRequest {
		t.Fatalf("enabling TLS without a pair must be 400, got %d: %s", missing.Code, missing.Body.String())
	}

	// A pair that is not a certificate is refused.
	garbage := filepath.Join(t.TempDir(), "garbage.pem")
	if err := os.WriteFile(garbage, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	bad := do(t, svc, http.MethodPost, "/api/v1/security/tls",
		`{"enabled":true,"certFile":"`+garbage+`","keyFile":"`+garbage+`"}`, nil)
	if bad.Code != http.StatusUnprocessableEntity {
		t.Fatalf("an unparseable pair must be 422, got %d: %s", bad.Code, bad.Body.String())
	}

	// A real self-signed pair is accepted and persisted.
	dir := t.TempDir()
	certFile := filepath.Join(dir, "fullchain.cer")
	keyFile := filepath.Join(dir, "private.key")
	if err := cert.GenerateSelfSigned(certFile, keyFile, "panel.test"); err != nil {
		t.Fatal(err)
	}
	good := do(t, svc, http.MethodPost, "/api/v1/security/tls",
		`{"enabled":true,"certFile":"`+certFile+`","keyFile":"`+keyFile+`"}`, nil)
	if good.Code != http.StatusOK {
		t.Fatalf("a valid pair must enable TLS, got %d: %s", good.Code, good.Body.String())
	}
	cfg, err := LoadConfig(svc.opts.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.TLS || cfg.CertFile != certFile || cfg.KeyFile != keyFile {
		t.Fatalf("the TLS setting was not persisted: %+v", cfg)
	}

	// An unknown firewall action is a 404, not a silent no-op.
	if rec := do(t, svc, http.MethodPost, "/api/v1/security/firewall/bogus", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("an unknown firewall action must be 404, got %d", rec.Code)
	}
}
