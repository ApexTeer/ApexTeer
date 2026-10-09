package panel

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/EasySBTeam/EasySB/internal/cert"
	"github.com/EasySBTeam/EasySB/internal/deploy"
	"github.com/EasySBTeam/EasySB/internal/node"
	"github.com/EasySBTeam/EasySB/internal/state"
)

// jsonBody encodes a request body rather than building the JSON by hand. A path
// interpolated into a JSON string literal is not portable: on Windows it carries
// backslashes, and `\U` is an invalid JSON escape, so the request is rejected as a
// malformed body (400) before the validation under test is ever reached. That made
// this file's assertions fail on a Windows checkout for a reason that had nothing
// to do with the handler. Marshalling escapes the path the way the wire format
// requires.
func jsonBody(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("encode the request body: %v", err)
	}
	return string(data)
}

// TestPanelListensOnLoopbackByDefault covers the shipped default.
//
// The panel used to bind every interface over plain HTTP, so the admin password -
// posted as JSON - and the session cookie crossed the network in clear text, and
// every authenticated request is root-equivalent. The safe arrangement is now the one
// an operator gets without reading any documentation.
func TestPanelListensOnLoopbackByDefault(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Listen != "127.0.0.1" {
		t.Fatalf("the panel default listen address is %q, want loopback", cfg.Listen)
	}
	if !isLoopback(cfg.Listen) {
		t.Fatalf("DefaultListen %q is not recognised as loopback", cfg.Listen)
	}
	// A missing file must yield the same default rather than an empty host, which
	// would bind everything.
	loaded, err := LoadConfig(filepath.Join(t.TempDir(), "absent.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Listen != "127.0.0.1" {
		t.Fatalf("a missing config yielded listen %q", loaded.Listen)
	}
}

// TestListenEnvOverridesTheBinding is the documented escape hatch: a deployment that
// needs a public address sets it deliberately, through an environment variable rather
// than by editing state.
func TestListenEnvOverridesTheBinding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "easysb-panel.conf")
	if err := (Config{Listen: "127.0.0.1", Port: 2095}).Save(path); err != nil {
		t.Fatal(err)
	}
	if got, _ := LoadConfig(path); got.Listen != "127.0.0.1" {
		t.Fatalf("the file was not honoured: %q", got.Listen)
	}

	t.Setenv(ListenEnv, "0.0.0.0")
	got, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Listen != "0.0.0.0" {
		t.Fatalf("the environment override was ignored: %q", got.Listen)
	}
}

func TestIsLoopbackClassification(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1": true,
		"localhost": true,
		"::1":       true,
		"[::1]":     true,
		"":          false,
		"0.0.0.0":   false,
		"::":        false,
		"192.0.2.7": false,
	}
	for host, want := range cases {
		if got := isLoopback(host); got != want {
			t.Errorf("isLoopback(%q) = %v, want %v", host, got, want)
		}
	}
}

// TestRequestIsSecureFollowsTheScheme pins the cookie decision for the arrangement
// the installation docs recommend: a reverse proxy terminates TLS, so cfg.TLS is
// false while the browser's hop is HTTPS.
func TestRequestIsSecureFollowsTheScheme(t *testing.T) {
	plain := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	if requestIsSecure(plain) {
		t.Fatal("a plain request was treated as secure")
	}
	proxied := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	proxied.Header.Set("X-Forwarded-Proto", "https")
	if !requestIsSecure(proxied) {
		t.Fatal("a proxy-terminated HTTPS request was not treated as secure")
	}
	// A proxied plain request stays plain.
	httpProxied := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	httpProxied.Header.Set("X-Forwarded-Proto", "http")
	if requestIsSecure(httpProxied) {
		t.Fatal("a proxy-terminated HTTP request was treated as secure")
	}
}

// TestEveryRouteRequiresASession is the guard that makes routes() worth having.
//
// The panel's whole authorization model is one wrapper, applied at registration.
// A route registered without it is reachable by anyone who can reach the port, and
// every authenticated request is root-equivalent here (the unit sets no User, and
// the panel offers a PTY), so nothing about that mistake is small. Enumerating the
// table means adding an endpoint without authentication fails a test instead of
// shipping.
//
// Every unauthenticated request must be rejected before its handler runs. A 405 is
// accepted too: net/http answers a method no pattern claims with that, which is
// also a refusal and keeps the test independent of which method each endpoint wants.
func TestEveryRouteRequiresASession(t *testing.T) {
	opts := testOptions(t)
	opts.AllowAnonymous = false
	hash, _ := HashPassword("correct-password")
	if err := (Config{Listen: "127.0.0.1", Port: 2095, Username: "admin", PasswordHash: hash}).Save(opts.ConfigPath); err != nil {
		t.Fatal(err)
	}
	svc := New(opts)
	if len(svc.routes()) == 0 {
		t.Fatal("the route table is empty, so this test would pass vacuously")
	}

	const placeholder = "test-value"
	concrete := func(pattern string) string {
		// The mux patterns carry {id} / {name} / {action}; a request needs a literal.
		re := regexp.MustCompile(`\{[a-zA-Z]+\}`)
		return re.ReplaceAllString(pattern, placeholder)
	}

	checked := 0
	for _, rt := range svc.routes() {
		if rt.public {
			continue
		}
		checked++
		target := concrete(rt.pattern)
		rec := do(t, svc, rt.method, target, "", nil)
		switch rec.Code {
		case http.StatusUnauthorized:
			// The expected answer: require rejected it.
		case http.StatusMethodNotAllowed:
			// No pattern claims this method, which is also a refusal.
		default:
			t.Errorf("%s %s answered %d without a session, want 401 or 405: %s",
				rt.method, target, rec.Code, rec.Body.String())
		}
	}
	if checked == 0 {
		t.Fatal("no non-public route was checked")
	}
}

// TestEveryRouteHasAHandler fails when routes() grows an entry that handlerFor does
// not serve, which would otherwise be a panic at startup or a silently dead route.
func TestEveryRouteHasAHandler(t *testing.T) {
	svc := New(testOptions(t))
	for _, rt := range svc.routes() {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%s %s has no handler: %v", rt.method, rt.pattern, r)
				}
			}()
			if h := svc.handlerFor(rt); h == nil {
				t.Errorf("%s %s maps to a nil handler", rt.method, rt.pattern)
			}
		}()
	}
}

// seedPanelNodes writes nodes into a fresh node store for the panel tests.
func seedPanelNodes(t *testing.T, path string, nodes []node.Node) {
	t.Helper()
	store, err := node.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes {
		if err := store.Add(n); err != nil {
			t.Fatalf("seed node %q: %v", n.Name, err)
		}
	}
}

// TestPanelPortCollisionIsRefused covers the port guard on the panel's own
// configuration.
//
// The panel shares the host with the core's inbounds and the subscription service.
// A port that collides with one of those used to be persisted and only discovered at
// the next restart, when the panel fails to bind and the operator has locked
// themselves out of the interface they would use to fix it. The check runs before
// anything is saved.
func TestPanelPortCollisionIsRefused(t *testing.T) {
	opts := testOptions(t)
	opts.AllowAnonymous = false
	hash, _ := HashPassword("correct-password")
	if err := (Config{Listen: "127.0.0.1", Port: 2095, Username: "admin", PasswordHash: hash}).Save(opts.ConfigPath); err != nil {
		t.Fatal(err)
	}
	// The state says the subscription service is on this port, and a node is on
	// another, so both refusals are exercised.
	subPort := 8443
	nodePort := 8100
	opts.State = func() state.Config {
		return state.Config{Domain: "example.com", NodeDeployed: true, SubServePort: subPort}
	}
	seedPanelNodes(t, opts.NodesPath, []node.Node{
		node.New(state.ProtoAnyTLS, "first", 8000, nil),
		node.New(state.ProtoTUIC, "second", nodePort, nil),
	})

	svc := New(opts)
	login := do(t, svc, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"correct-password"}`, nil)
	token, _ := decodeBody(t, login)["token"].(string)
	auth := map[string]string{"Authorization": "Bearer " + token}

	// The subscription service's port.
	rec := do(t, svc, http.MethodPost, "/api/v1/panel/config",
		jsonBody(t, map[string]any{"port": subPort}), auth)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("the subscription port must be refused, got %d: %s", rec.Code, rec.Body.String())
	}

	// A port an enabled node holds.
	rec = do(t, svc, http.MethodPost, "/api/v1/panel/config",
		jsonBody(t, map[string]any{"port": nodePort}), auth)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a node's port must be refused, got %d: %s", rec.Code, rec.Body.String())
	}

	// Nothing was persisted by either refusal.
	cfg, err := LoadConfig(opts.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 2095 {
		t.Fatalf("a refused change was persisted: port = %d, want 2095", cfg.Port)
	}

	// A free port is accepted.
	rec = do(t, svc, http.MethodPost, "/api/v1/panel/config",
		jsonBody(t, map[string]any{"port": 2096}), auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("a free port must be accepted, got %d: %s", rec.Code, rec.Body.String())
	}
	cfg, err = LoadConfig(opts.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 2096 {
		t.Fatalf("the accepted port was not persisted: got %d, want 2096", cfg.Port)
	}
}

// TestInternalErrorsAreNotDisclosed covers the error-mapping rule at its two
// central helpers, which is where every store and deploy failure passes through.
//
// The rule has two halves and both are checked, because a blanket "hide everything"
// would be as wrong as the status quo: a configuration the core refused has to keep
// naming the field the operator must fix, and a store that cannot be read has to stop
// handing the client the absolute path of the file it failed on.
func TestInternalErrorsAreNotDisclosed(t *testing.T) {
	svc := New(testOptions(t))

	t.Run("a refused configuration keeps the core's message", func(t *testing.T) {
		rec := httptest.NewRecorder()
		svc.applyError(rec, fmt.Errorf("%w: unknown inbound type", deploy.ErrRejected))
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422", rec.Code)
		}
		if body := rec.Body.String(); !strings.Contains(body, "unknown inbound type") {
			t.Fatalf("the core's own reason was dropped: %s", body)
		}
	})

	t.Run("an internal deploy failure is sanitized", func(t *testing.T) {
		secret := filepath.Join(t.TempDir(), "private-path", "easysb-users.json")
		rec := httptest.NewRecorder()
		svc.applyError(rec, fmt.Errorf("open %s: permission denied", secret))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", rec.Code)
		}
		body := rec.Body.String()
		if strings.Contains(body, secret) || strings.Contains(body, "private-path") {
			t.Fatalf("the internal error disclosed a filesystem path: %s", body)
		}
		if strings.Contains(body, "permission denied") {
			t.Fatalf("the internal error disclosed the OS failure: %s", body)
		}
		if !strings.Contains(body, "ref ") {
			t.Fatalf("a sanitized error must carry a reference to the log line: %s", body)
		}
	})

	t.Run("a store validation message is kept", func(t *testing.T) {
		rec := httptest.NewRecorder()
		svc.storeError(rec, errors.New("node port 0 is outside 1-65535"))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
		if body := rec.Body.String(); !strings.Contains(body, "outside 1-65535") {
			t.Fatalf("a validation message was hidden from the form: %s", body)
		}
	})

	t.Run("a not-found error keeps its status and wording", func(t *testing.T) {
		rec := httptest.NewRecorder()
		svc.storeError(rec, errors.New(`user "alice" not found`))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
		if body := rec.Body.String(); !strings.Contains(body, "not found") {
			t.Fatalf("the not-found wording was dropped: %s", body)
		}
	})

	t.Run("a corrupt store does not disclose its path", func(t *testing.T) {
		secret := filepath.Join(t.TempDir(), "private-path", "easysb-users.json")
		rec := httptest.NewRecorder()
		svc.storeError(rec, fmt.Errorf("parse %s: unexpected end of JSON input", secret))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("a load failure is internal, so status = %d, want 500", rec.Code)
		}
		if body := rec.Body.String(); strings.Contains(body, "private-path") || strings.Contains(body, "parse ") {
			t.Fatalf("the store path was disclosed: %s", body)
		}
	})

	t.Run("a path error is not mistaken for a validation message", func(t *testing.T) {
		// A filesystem error names the file it failed on, and a store path could
		// begin with a word the validation list matches ("node ..."). The filesystem
		// prefixes are therefore checked first, so this stays a sanitized 500 rather
		// than being echoed back as if the operator had typed something wrong.
		secret := filepath.Join(t.TempDir(), "node-private.json")
		rec := httptest.NewRecorder()
		svc.storeError(rec, fmt.Errorf("open %s: permission denied", secret))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want a sanitized 500", rec.Code)
		}
		if body := rec.Body.String(); strings.Contains(body, "node-private.json") {
			t.Fatalf("the store path was disclosed: %s", body)
		}
	})
}

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
	body := jsonBody(t, map[string]any{"enabled": true, "certFile": garbage, "keyFile": garbage})
	bad := do(t, svc, http.MethodPost, "/api/v1/security/tls", body, nil)
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
		jsonBody(t, map[string]any{"enabled": true, "certFile": certFile, "keyFile": keyFile}), nil)
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
