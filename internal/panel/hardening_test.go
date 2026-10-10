package panel

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The console is served over whatever the operator exposes it on, and until now it sent no
// hardening headers at all. These tests pin the ones that were added and, just as
// importantly, pin that the redaction of the REALITY private key cannot break saving.

// TestSecurityHeadersAreOnEveryResponse covers the SPA, the API and an error: a header that
// only lands on one of them is worse than none, because it reads as protection that is not
// there.
func TestSecurityHeadersAreOnEveryResponse(t *testing.T) {
	svc := New(testOptions(t))

	cases := []struct {
		name, method, target, body string
	}{
		{"the SPA shell", "GET", "/", ""},
		{"an API response", "GET", "/api/v1/dashboard", ""},
		{"a not-found API path", "GET", "/api/v1/nope", ""},
		{"a rejected login", "POST", "/api/v1/auth/login", `{}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, svc, tc.method, tc.target, tc.body, nil)
			h := rec.Header()
			want := map[string]string{
				"X-Content-Type-Options": "nosniff",
				"X-Frame-Options":        "DENY",
				"Referrer-Policy":        "no-referrer",
			}
			for k, v := range want {
				if got := h.Get(k); got != v {
					t.Errorf("%s on %s = %q, want %q", k, tc.target, got, v)
				}
			}
			csp := h.Get("Content-Security-Policy")
			if csp == "" {
				t.Fatalf("no Content-Security-Policy on %s", tc.target)
			}
			// Only directives that cannot break a page are allowed in this policy. If one
			// that can is ever added, this test is the place that says so.
			for _, forbidden := range []string{"script-src", "style-src", "connect-src", "default-src"} {
				if strings.Contains(csp, forbidden) {
					t.Errorf("the policy now constrains %s, which needs a rendering test before "+
						"it ships: %q", forbidden, csp)
				}
			}
			for _, required := range []string{"frame-ancestors 'none'", "object-src 'none'", "base-uri 'self'"} {
				if !strings.Contains(csp, required) {
					t.Errorf("the policy lost %q: %q", required, csp)
				}
			}
		})
	}
}

// TestSecurityHeadersSurviveTheWebSocketWrapper guards the one response that is not an
// ordinary write: the terminal upgrades the connection and replaces the writer.
func TestSecurityHeadersSurviveTheWebSocketWrapper(t *testing.T) {
	svc := New(testOptions(t))
	rec := do(t, svc, "GET", "/api/v1/terminal/ws", "", nil)
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("the terminal route lost the hardening headers: nosniff = %q", got)
	}
}

// TestCoreConfigRedactsTheRealityPrivateKey is the point of the redaction: the displayed
// document must not carry the key.
func TestCoreConfigRedactsTheRealityPrivateKey(t *testing.T) {
	opts := testOptions(t)
	const secret = "PRIVATEKEYMATERIAL012345678901234567890"
	cfg := `{"inbounds":[{"type":"vless","tag":"n1","tls":{"enabled":true,"reality":{"enabled":true,"private_key":"` + secret + `","short_id":["f2929d06"]}}}],"outbounds":[{"type":"direct"}]}`
	if err := os.WriteFile(opts.ConfigJSON, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := New(opts)

	rec := do(t, svc, "GET", "/api/v1/core/config", "", nil)
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, secret) {
		t.Fatal("the REALITY private key was returned to the browser")
	}
	// The config travels as a JSON string, so the placeholder is escaped in the envelope
	// and only visible once the string is decoded.
	doc := decodeBody(t, rec)
	inner, _ := doc["config"].(string)
	if !strings.Contains(inner, keyMaterialPlaceholder) {
		t.Fatalf("the key was dropped without saying so; the operator would read the "+
			"document as complete: %s", inner)
	}

	// Everything else must survive, or the page stops being a faithful view.
	var parsed map[string]any
	if err := json.Unmarshal([]byte(inner), &parsed); err != nil {
		t.Fatalf("the redacted document no longer parses: %v", err)
	}
	inbounds := parsed["inbounds"].([]any)
	in := inbounds[0].(map[string]any)
	if in["tag"] != "n1" || in["type"] != "vless" {
		t.Fatalf("the redaction damaged the inbound: %v", in)
	}
	reality := in["tls"].(map[string]any)["reality"].(map[string]any)
	if reality["enabled"] != true {
		t.Fatal("reality.enabled was lost")
	}
	sids, ok := reality["short_id"].([]any)
	if !ok || len(sids) != 1 || sids[0] != "f2929d06" {
		t.Fatalf("short_id was lost: %v", reality["short_id"])
	}
	if len(parsed["outbounds"].([]any)) != 1 {
		t.Fatal("outbounds were lost")
	}
}

// TestCoreConfigWithoutRealityIsUnchanged keeps the redaction from touching a document that
// has no key in it. It must not re-serialize for no reason: the operator compares this view
// against the file on disk.
func TestCoreConfigWithoutRealityIsUnchanged(t *testing.T) {
	opts := testOptions(t)
	const cfg = `{"inbounds":[{"type":"anytls","tag":"a1","listen_port":8000}]}`
	if err := os.WriteFile(opts.ConfigJSON, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := New(opts)
	rec := do(t, svc, "GET", "/api/v1/core/config", "", nil)
	doc := decodeBody(t, rec)
	if got := doc["config"].(string); got != cfg {
		t.Fatalf("a document with no key was rewritten:\n got %q\nwant %q", got, cfg)
	}
}

// TestRedactionHandlesUnparsableContent: this is a display path, so a document that does not
// parse is shown as it is rather than replaced by nothing.
func TestRedactionHandlesUnparsableContent(t *testing.T) {
	const broken = `{"inbounds": [ this is not json`
	if got := redactKeyMaterial(broken); got != broken {
		t.Fatalf("an unparsable document was altered: %q", got)
	}
}

// TestRedactionLeavesAKeyOutsideRealityAlone: only the REALITY private key is redacted. A
// "private_key" in some other place is not this function's business.
func TestRedactionLeavesAKeyOutsideRealityAlone(t *testing.T) {
	const doc = `{"inbounds":[{"type":"anytls","tls":{"private_key":"a-path-ish-value"}}]}`
	got := redactKeyMaterial(doc)
	if !strings.Contains(got, "a-path-ish-value") {
		t.Fatalf("a private_key outside tls.reality was redacted: %q", got)
	}
}

// TestApplyDoesNotTakeTheConfigFromTheClient is what makes the redaction safe. If apply ever
// started accepting a submitted document, a redacted one would write the placeholder over
// the real key.
func TestApplyDoesNotTakeTheConfigFromTheClient(t *testing.T) {
	opts := testOptions(t)
	const secret = "REALKEYMATERIAL0123456789012345678901234"
	cfg := `{"inbounds":[{"type":"vless","tls":{"reality":{"private_key":"` + secret + `"}}}]}`
	if err := os.WriteFile(opts.ConfigJSON, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	// Apply is a no-op stub: this test is about what the handler does with the request
	// body, not about the deployment it would trigger.
	opts.Apply = func(context.Context) error { return nil }
	svc := New(opts)

	// Post a document carrying the placeholder, the way a careless client might.
	submitted, err := json.Marshal(map[string]any{
		"path":   opts.ConfigJSON,
		"config": `{"inbounds":[{"tls":{"reality":{"private_key":"` + keyMaterialPlaceholder + `"}}}]}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	do(t, svc, "POST", "/api/v1/core/apply", string(submitted), map[string]string{"X-EasySB-Panel": "1"})

	// Whatever the outcome, the file on disk must still hold the real key.
	after, err := os.ReadFile(opts.ConfigJSON)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(after), secret) {
		t.Fatalf("the stored configuration lost its REALITY private key: %s", after)
	}
}

// TestCoreConfigStillCarriesTheAccounts: the redaction must not be so broad that it hides
// what the page exists to show.
func TestCoreConfigStillCarriesTheAccounts(t *testing.T) {
	opts := testOptions(t)
	cfg := `{"inbounds":[{"type":"anytls","users":[{"name":"t@a1","password":"pw"}]}]}`
	if err := os.WriteFile(opts.ConfigJSON, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := New(opts)
	rec := do(t, svc, "GET", "/api/v1/core/config", "", nil)
	if !strings.Contains(rec.Body.String(), "t@a1") {
		t.Fatal("the redaction removed the account list the page exists to show")
	}
}
