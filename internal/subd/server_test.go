package subd

import (
	"context"
	"encoding/base64"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EasySBTeam/EasySB/internal/cert"
	"github.com/EasySBTeam/EasySB/internal/node"
	"github.com/EasySBTeam/EasySB/internal/state"
	"github.com/EasySBTeam/EasySB/internal/user"
)

// endpointNow is the clock every endpoint test runs on.
var endpointNow = time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

// testNodeID is the node every fixture account selects. The endpoint resolves a
// subscription's entries against the node store, so the fixtures share one node.
const testNodeID = "node-vless"

// testNode is the single Reality node the fixture endpoints serve.
func testNode() node.Node {
	n := node.New(state.ProtoVLESSReality, "vless", 8443, nil)
	n.ID = testNodeID
	return n
}

// nodePath writes the test node to a fresh file and returns its path.
func nodePath(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nodes.json")
	store, err := node.Load(path)
	if err != nil {
		t.Fatalf("load nodes: %v", err)
	}
	if err := store.Add(testNode()); err != nil {
		t.Fatalf("add node: %v", err)
	}
	return path
}

// nodeConfig is the node the subscription documents are rendered for.
func nodeConfig() state.Config {
	cfg := state.Default()
	cfg.ServerIP = "203.0.113.10"
	return cfg
}

// freePort asks the kernel for a port nothing listens on, so the service test
// never competes with a real deployment for 8443.
func freePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

// endpoint is a subscription service backed by one temp account file, driven
// through its handler rather than a real listener.
type endpoint struct {
	handler http.Handler
	path    string
	store   *user.Store
}

// newEndpoint writes the accounts the test asks for and returns the handler.
func newEndpoint(t *testing.T, accounts ...user.User) *endpoint {
	t.Helper()
	path := filepath.Join(t.TempDir(), "users.json")
	store, err := user.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, account := range accounts {
		if err := store.Add(account); err != nil {
			t.Fatalf("add %s: %v", account.Name, err)
		}
	}
	if err := store.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	o := Options{
		AccountsPath: path,
		NodesPath:    nodePath(t),
		Node:         nodeConfig,
		Now:          func() time.Time { return endpointNow },
		Log:          func(string) {},
	}
	return &endpoint{handler: o.handler(), path: path, store: store}
}

// account builds one active account whose token is derived from its name.
func account(name string, edit func(*user.User)) user.User {
	u := user.New(name, []user.Selection{{Node: testNodeID, Protocol: state.ProtoVLESSReality}}, endpointNow)
	u.Token = "token-" + name
	if edit != nil {
		edit(&u)
	}
	return u
}

// get performs one subscription request with a User-Agent.
func (e *endpoint) get(token, ua string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, SubPath+token, nil)
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

func TestEndpointServesSingBoxProfile(t *testing.T) {
	e := newEndpoint(t, account("alice", nil))
	rec := e.get("token-alice", "sing-box 1.10.0")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content type = %q", ct)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache control = %q, a subscription carries credentials", got)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "$schema") || !strings.Contains(body, "EasySB-alice") {
		t.Fatalf("unexpected sing-box profile:\n%s", body)
	}
}

func TestEndpointServesMihomoProfile(t *testing.T) {
	e := newEndpoint(t, account("alice", nil))
	rec := e.get("token-alice", "clash-verge/v2.0 mihomo")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/yaml") {
		t.Fatalf("content type = %q", ct)
	}
	if body := rec.Body.String(); !strings.Contains(body, "proxies:") {
		t.Fatalf("unexpected mihomo profile:\n%s", body)
	}
}

func TestEndpointServesBase64ForUnknownClients(t *testing.T) {
	e := newEndpoint(t, account("alice", nil))
	rec := e.get("token-alice", "v2rayN/6.0")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %q", rec.Code, rec.Body.String())
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(rec.Body.String()))
	if err != nil {
		t.Fatalf("body is not base64: %v", err)
	}
	if !strings.Contains(string(raw), "vless://") {
		t.Fatalf("base64 document missing share links:\n%s", raw)
	}
}

// TestEndpointNamesDownloads covers the file name a client saves: it is the product,
// not the account. The token used to be the name, which is a credential sitting in
// somebody's download folder, and it differs per account in a folder that usually
// holds one profile. The name is sent in both forms RFC 6266 defines and the plain
// one stays unquoted: a quoted filename reaches the Clash family as `\"EasySB\"`,
// because they strip the surrounding quotes from a Debug-formatted header and nothing
// else. See scripts/clash_orbit_name_check.py for the parser this is written against.
func TestEndpointNamesDownloads(t *testing.T) {
	const want = "attachment; filename=EasySB; filename*=UTF-8''EasySB"
	for _, ua := range []string{"sing-box 1.10.0", "clash-verge/v2 mihomo", "clash-orbit/2.0", "v2rayN/6.0"} {
		e := newEndpoint(t, account("alice", nil))
		rec := e.get("token-alice", ua)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", ua, rec.Code)
		}
		if got := rec.Header().Get("Content-Disposition"); got != want {
			t.Errorf("%s: disposition = %q, want %q", ua, got, want)
		}
		if got := rec.Header().Get("Content-Disposition"); strings.Contains(got, "token-alice") {
			t.Errorf("%s: the file name must not carry the account token: %q", ua, got)
		}
	}
}

func TestEndpointClientOverride(t *testing.T) {
	e := newEndpoint(t, account("alice", nil))
	req := httptest.NewRequest(http.MethodGet, SubPath+"token-alice?client=mihomo", nil)
	req.Header.Set("User-Agent", "sing-box 1.10.0")
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)

	if body := rec.Body.String(); !strings.Contains(body, "proxies:") {
		t.Fatalf("client override ignored:\n%s", body)
	}
}

func TestEndpointRefusesInactiveAccounts(t *testing.T) {
	cases := []struct {
		name    string
		account user.User
		want    int
		reason  string
	}{
		{"disabled", account("off", func(u *user.User) { u.Enabled = false }), http.StatusForbidden, "disabled"},
		{"expired", account("old", func(u *user.User) {
			u.ExpireAt = endpointNow.Add(-time.Hour)
		}), http.StatusForbidden, "expired"},
		{"over quota", account("full", func(u *user.User) {
			u.QuotaBytes = 100
			u.UsedBytes = 100
		}), http.StatusForbidden, "quota"},
		{"no node", account("bare", func(u *user.User) {
			u.Deselect(testNodeID)
		}), http.StatusForbidden, "node"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEndpoint(t, tc.account)
			rec := e.get(tc.account.Token, "sing-box 1.10.0")
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
			if !strings.Contains(strings.ToLower(rec.Body.String()), tc.reason) {
				t.Fatalf("body %q should mention %q", rec.Body.String(), tc.reason)
			}
		})
	}
}

func TestEndpointRefusesUnknownToken(t *testing.T) {
	e := newEndpoint(t, account("alice", nil))
	for _, token := range []string{"token-bob", "", "%20"} {
		rec := e.get(token, "sing-box 1.10.0")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("token %q: status = %d, want 404", token, rec.Code)
		}
	}
}

func TestEndpointUnknownPathsAreNotFound(t *testing.T) {
	e := newEndpoint(t, account("alice", nil))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestEndpointRejectsWriteMethods(t *testing.T) {
	e := newEndpoint(t, account("alice", nil))
	req := httptest.NewRequest(http.MethodPost, SubPath+"token-alice", nil)
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); !strings.Contains(allow, "GET") {
		t.Fatalf("allow = %q", allow)
	}
}

func TestEndpointHEADHasNoBody(t *testing.T) {
	e := newEndpoint(t, account("alice", nil))
	req := httptest.NewRequest(http.MethodHead, SubPath+"token-alice", nil)
	req.Header.Set("User-Agent", "sing-box 1.10.0")
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("HEAD returned a body: %q", rec.Body.String())
	}
}

func TestEndpointIgnoresExtraPathSegments(t *testing.T) {
	e := newEndpoint(t, account("alice", nil))
	// Clients append their own suffixes and query strings to the URL the panel
	// shows, and those must not break the lookup.
	rec := e.get("token-alice/extra?x=1", "sing-box 1.10.0")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %q", rec.Code, rec.Body.String())
	}
}

func TestEndpointReportsUserinfo(t *testing.T) {
	e := newEndpoint(t, account("alice", func(u *user.User) {
		u.QuotaBytes = 10 << 30
		u.UploadBytes = 1 << 20
		u.DownloadBytes = 2 << 20
		u.ExpireAt = endpointNow.Add(48 * time.Hour)
	}))
	rec := e.get("token-alice", "clash-verge")
	got := rec.Header().Get("Subscription-Userinfo")
	want := "upload=1048576; download=2097152; total=10737418240; expire=" +
		strconv.FormatInt(endpointNow.Add(48*time.Hour).Unix(), 10)
	if got != want {
		t.Fatalf("userinfo = %q, want %q", got, want)
	}
}

func TestEndpointReflectsAccountEdits(t *testing.T) {
	e := newEndpoint(t, account("alice", nil))
	if rec := e.get("token-alice", "sing-box"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d before the edit", rec.Code)
	}
	// The panel edits the same file while the service runs; the service must
	// pick the change up without a restart.
	if err := e.store.Update("alice", func(u *user.User) error {
		u.Enabled = false
		return nil
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if rec := e.get("token-alice", "sing-box"); rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d after disabling the account", rec.Code)
	}
}

func TestEndpointLoadsAccountsPerRequest(t *testing.T) {
	e := newEndpoint(t, account("alice", nil))
	// A second account appears while the service is running.
	other := account("bob", nil)
	if err := e.store.Add(other); err != nil {
		t.Fatalf("add: %v", err)
	}
	if rec := e.get(other.Token, "sing-box"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d for a freshly added account", rec.Code)
	}
}

func TestTokenOf(t *testing.T) {
	cases := map[string]string{
		"/sub/abc":             "abc",
		"/sub/abc/":            "abc",
		"/sub/abc/def":         "abc",
		"/sub/":                "",
		"/other/abc":           "",
		"/sub/abc/../def":      "abc",
		"/sub/a%20b":           "a%20b",
		"/sub/token-with-dash": "token-with-dash",
	}
	for path, want := range cases {
		if got := tokenOf(path); got != want {
			t.Errorf("tokenOf(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestClientFromUserAgent(t *testing.T) {
	cases := map[string]string{
		"sing-box 1.10.0":   "singbox",
		"SFA/1.9.0":         "singbox",
		"clash-verge/1.5.0": "mihomo",
		"mihomo/1.18.0":     "mihomo",
		"v2rayN/6.31":       "v2ray",
		"":                  "v2ray",
		"some/scanner":      "v2ray",
	}
	for ua, want := range cases {
		if got := string(clientFromUserAgent(ua)); got != want {
			t.Errorf("clientFromUserAgent(%q) = %q, want %q", ua, got, want)
		}
	}
}

func TestUserinfoOmitsUnsetExpiry(t *testing.T) {
	got := userinfo(user.User{QuotaBytes: 1024})
	if strings.Contains(got, "expire") {
		t.Fatalf("an account without an expiry should not report one: %q", got)
	}
	if want := "upload=0; download=0; total=1024"; got != want {
		t.Fatalf("userinfo = %q, want %q", got, want)
	}
}

func TestTransportUsesPlainHTTPWithoutCertificate(t *testing.T) {
	o := Options{}
	cfg := nodeConfig()
	addr, certFile, keyFile := o.transport(cfg)
	if !strings.HasSuffix(addr, ":8443") {
		t.Fatalf("addr = %q, want the default subscription port", addr)
	}
	if certFile != "" || keyFile != "" {
		t.Fatalf("no certificate should be used without a domain: %q %q", certFile, keyFile)
	}
}

// TestTransportIgnoresTheSelfSignedPlaceholder keeps the listener and the printed
// URL in step: cert.Usable answers "does the endpoint have a certificate a client
// accepts", and a self-signed placeholder answers no, so serving TLS with it would
// hand clients an https:// address the listener could not honor.
func TestTransportIgnoresTheSelfSignedPlaceholder(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(cert.DirEnv, dir)
	pairDir := filepath.Join(dir, "example.com")
	if err := os.MkdirAll(pairDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := cert.GenerateSelfSigned(filepath.Join(pairDir, "fullchain.cer"), filepath.Join(pairDir, "private.key"), "example.com"); err != nil {
		t.Fatalf("GenerateSelfSigned: %v", err)
	}

	cfg := nodeConfig()
	cfg.Domain = "example.com"
	addr, certFile, keyFile := Options{}.transport(cfg)
	if !strings.HasSuffix(addr, ":8443") {
		t.Fatalf("addr = %q, want the default subscription port", addr)
	}
	if certFile != "" || keyFile != "" {
		t.Fatalf("a self-signed placeholder must not serve TLS: %q %q", certFile, keyFile)
	}
}

func TestRunServesAndStops(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	store, err := user.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := store.Add(account("alice", nil)); err != nil {
		t.Fatalf("add: %v", err)
	}

	// Bind an ephemeral port by asking the node state for a port the test found
	// free, so the run loop is exercised without touching the real 8443.
	probe, err := freePort()
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	cfg := nodeConfig()
	cfg.SubServePort = probe

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- Options{
			AccountsPath: path,
			NodesPath:    nodePath(t),
			Node:         func() state.Config { return cfg },
			Now:          func() time.Time { return endpointNow },
			Log:          func(string) {},
			Ready:        ready,
		}.Run(ctx)
	}()

	addr := <-ready
	resp, err := http.Get("http://" + addr + SubPath + "token-alice")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not stop after the context was cancelled")
	}
}

// TestRunAnnouncesTheVersion holds Options.Version to its own documentation: the service
// is the only long-running mode of this binary, and its journal line is what says which
// build is answering subscriptions on a host nobody opens a panel on.
func TestRunAnnouncesTheVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	if _, err := user.Load(path); err != nil {
		t.Fatalf("load: %v", err)
	}
	probe, err := freePort()
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	cfg := nodeConfig()
	cfg.SubServePort = probe

	var mu sync.Mutex
	var lines []string
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- Options{
			Version:      "9.9.9",
			AccountsPath: path,
			NodesPath:    nodePath(t),
			Node:         func() state.Config { return cfg },
			Log: func(line string) {
				mu.Lock()
				defer mu.Unlock()
				lines = append(lines, line)
			},
			Ready: ready,
		}.Run(ctx)
	}()

	<-ready
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not stop after the context was cancelled")
	}

	mu.Lock()
	defer mu.Unlock()
	if joined := strings.Join(lines, "\n"); !strings.Contains(joined, "9.9.9") {
		t.Fatalf("the startup line does not name the build: %q", joined)
	}
}

// TestRunClosesReadyWhenItCannotBind pins the contract Ready's doc comment claims:
// waiting on it is always enough to learn the outcome.
//
// It used to be closed only on the success path, so a caller that only waits on it
// could not tell a service that is starting from one that has already given up. The
// bind here is made to fail by holding the port, which is also what a port taken
// between a caller choosing one and Run binding it looks like.
func TestRunClosesReadyWhenItCannotBind(t *testing.T) {
	// Hold a port for the duration of the test, so Run cannot have it. The wildcard
	// address is the one the service binds, so holding only 127.0.0.1 would not
	// conflict: those are separate sockets.
	held, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("hold a port: %v", err)
	}
	defer held.Close()
	port := held.Addr().(*net.TCPAddr).Port

	cfg := nodeConfig()
	cfg.SubServePort = port
	ready := make(chan string, 1)
	done := make(chan error, 1)
	// A context that is cancelled when the test ends: even though a failed bind no
	// longer starts anything, a Run that succeeds later would still be ticking, and
	// a goroutine writing into the test's temporary directory races its removal.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		done <- Options{
			AccountsPath: filepath.Join(t.TempDir(), "users.json"),
			NodesPath:    nodePath(t),
			Node:         func() state.Config { return cfg },
			Log:          func(string) {},
			Ready:        ready,
		}.Run(ctx)
	}()

	select {
	case _, ok := <-ready:
		if ok {
			t.Fatal("Ready carried an address for a listener that could not be bound")
		}
		// Closed without a value: the caller learns the outcome here rather than
		// having to time out.
	case <-time.After(10 * time.Second):
		t.Fatal("Ready was never closed after the bind failed, so a caller would hang")
	}

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run reported success for a listener it could not bind")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after failing to bind")
	}
}

func TestVersionFallsBackWhenUnset(t *testing.T) {
	if got := (Options{}).version(); got == "" {
		t.Fatal("an unset version leaves the log line trailing off")
	}
}
