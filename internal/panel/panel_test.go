package panel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EasySBTeam/EasySB/internal/state"
)

// testOptions builds a panel pointed entirely at a temporary directory. The Apply
// seam is a no-op so a CRUD test never touches the host's systemd units or the
// live core configuration, and authentication is off unless a test turns it on.
func testOptions(t *testing.T) Options {
	t.Helper()
	dir := t.TempDir()
	return Options{
		Version:        "test",
		ConfigPath:     filepath.Join(dir, "easysb-panel.conf"),
		NodesPath:      filepath.Join(dir, "easysb-nodes.json"),
		UsersPath:      filepath.Join(dir, "easysb-users.json"),
		ConfigJSON:     filepath.Join(dir, "config.json"),
		Log:            func(string) {},
		AllowAnonymous: true,
		Apply:          func(context.Context) error { return nil },
		State:          func() state.Config { return state.Config{Domain: "example.com", NodeDeployed: true} },
	}
}

// do runs one request against the service and returns the recorder.
func do(t *testing.T, svc *Service, method, target, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rec, req)
	return rec
}

// decodeBody decodes a JSON response into a map.
func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not JSON (%d): %s", rec.Code, rec.Body.String())
	}
	return out
}

func TestUnitBodyRunsPanelMode(t *testing.T) {
	body := UnitBody("/usr/bin/easysb")
	if !strings.Contains(body, "ExecStart=/usr/bin/easysb panel") {
		t.Fatalf("the unit must run the binary in panel mode:\n%s", body)
	}
}

// TestSelfTerminated covers the panel restart/stop case: systemd kills the very
// process that ran systemctl, so the command reports a termination even though the
// action took effect. Only stop and restart may be treated that way.
func TestSelfTerminated(t *testing.T) {
	terminated := errors.New("restart easysb-panel: signal: terminated")
	if !selfTerminated("restart", terminated) {
		t.Fatal("a terminated restart of the panel's own unit is a success")
	}
	if !selfTerminated("stop", errors.New("stop easysb-panel: signal: terminated")) {
		t.Fatal("a terminated stop of the panel's own unit is a success")
	}
	if !selfTerminated("restart", context.Canceled) {
		t.Fatal("a cancelled restart is a success")
	}
	if selfTerminated("start", terminated) {
		t.Fatal("a terminated start is a real failure")
	}
	if selfTerminated("restart", errors.New("restart easysb-panel: unit not found")) {
		t.Fatal("an unrelated restart error is a real failure")
	}
}

func TestConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "easysb-panel.conf")
	hash, err := HashPassword("hunter2-long")
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Listen: "127.0.0.1", Port: 2096, Username: "ops", PasswordHash: hash, TLS: true, CertFile: "c.pem", KeyFile: "k.pem"}
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Listen != "127.0.0.1" || got.Port != 2096 || got.Username != "ops" || !got.TLS {
		t.Fatalf("round trip lost fields: %+v", got)
	}
	if !VerifyPassword(got.PasswordHash, "hunter2-long") {
		t.Fatal("the stored hash does not verify the password it was made from")
	}
	if got.AccessURL() != "https://127.0.0.1:2096" {
		t.Fatalf("access URL is %q", got.AccessURL())
	}
}

// TestEnsureConfigBootstrapsCredentialOnce covers the first-run path every other
// test skips by writing a hash first: a fresh panel must get a password it can
// actually log in with, that password must survive a restart, and clearing the
// hash must produce a new one.
func TestEnsureConfigBootstrapsCredentialOnce(t *testing.T) {
	opts := testOptions(t)
	opts.AllowAnonymous = false

	password, err := EnsureConfig(opts.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if password == "" {
		t.Fatal("the first call must return a generated password")
	}
	cfg, err := LoadConfig(opts.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Username != "admin" || !VerifyPassword(cfg.PasswordHash, password) {
		t.Fatal("the persisted credential does not match the returned password")
	}

	// The generated credential actually logs in.
	svc := New(opts)
	login := do(t, svc, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"`+password+`"}`, nil)
	if login.Code != http.StatusOK {
		t.Fatalf("the bootstrapped credential must log in, got %d: %s", login.Code, login.Body.String())
	}

	// A restart must not mint a second password.
	again, err := EnsureConfig(opts.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if again != "" {
		t.Fatal("EnsureConfig must not generate a second password once one is stored")
	}
	reloaded, err := LoadConfig(opts.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.PasswordHash != cfg.PasswordHash {
		t.Fatal("a second start changed the stored password hash")
	}

	// Clearing the hash (the documented reset) mints a fresh credential.
	if err := os.WriteFile(opts.ConfigPath, []byte("PANEL_USER=\"admin\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reset, err := EnsureConfig(opts.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if reset == "" {
		t.Fatal("a cleared hash must regenerate a password")
	}
}

func TestLoginRejectsWrongPasswordAndIssuesSession(t *testing.T) {
	opts := testOptions(t)
	opts.AllowAnonymous = false
	hash, _ := HashPassword("correct-password")
	if err := (Config{Listen: "127.0.0.1", Port: 2095, Username: "admin", PasswordHash: hash}).Save(opts.ConfigPath); err != nil {
		t.Fatal(err)
	}
	svc := New(opts)

	bad := do(t, svc, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"wrong"}`, nil)
	if bad.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password must be 401, got %d: %s", bad.Code, bad.Body.String())
	}

	good := do(t, svc, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"correct-password"}`, nil)
	if good.Code != http.StatusOK {
		t.Fatalf("correct password must be 200, got %d: %s", good.Code, good.Body.String())
	}
	token, _ := decodeBody(t, good)["token"].(string)
	if token == "" {
		t.Fatal("login did not return a session token")
	}
	cookies := good.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("login did not set a session cookie")
	}

	// A cookie-authenticated read reaches the handler.
	session := do(t, svc, http.MethodGet, "/api/v1/auth/session", "", map[string]string{"Cookie": cookies[0].Name + "=" + cookies[0].Value})
	if session.Code != http.StatusOK {
		t.Fatalf("session read must be 200, got %d: %s", session.Code, session.Body.String())
	}
	if name, _ := decodeBody(t, session)["username"].(string); name != "admin" {
		t.Fatalf("session reports %q, want admin", name)
	}
}

// TestChangePasswordRevokesEverySession covers the remedy an operator reaches for
// when they suspect a session cookie has leaked: they change the password. Leaving
// the old tokens live would keep the very access the change was meant to end, for
// up to the full 12 hour TTL, so the handler drops every session - including the
// one that made the request - and clears the caller's cookie.
func TestChangePasswordRevokesEverySession(t *testing.T) {
	opts := testOptions(t)
	opts.AllowAnonymous = false
	hash, _ := HashPassword("correct-password")
	if err := (Config{Listen: "127.0.0.1", Port: 2095, Username: "admin", PasswordHash: hash}).Save(opts.ConfigPath); err != nil {
		t.Fatal(err)
	}
	svc := New(opts)

	// Two clients, as if the panel were open in two browsers.
	login := func() string {
		rec := do(t, svc, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"correct-password"}`, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("login must be 200, got %d: %s", rec.Code, rec.Body.String())
		}
		token, _ := decodeBody(t, rec)["token"].(string)
		if token == "" {
			t.Fatal("login did not return a token")
		}
		return token
	}
	first, second := login(), login()

	// Both are usable before the change.
	for i, token := range []string{first, second} {
		rec := do(t, svc, http.MethodGet, "/api/v1/auth/session", "", map[string]string{"Authorization": "Bearer " + token})
		if rec.Code != http.StatusOK {
			t.Fatalf("session %d must be readable before the change, got %d", i, rec.Code)
		}
	}

	changed := do(t, svc, http.MethodPost, "/api/v1/auth/password",
		`{"current":"correct-password","next":"a-new-password"}`,
		map[string]string{"Authorization": "Bearer " + first})
	if changed.Code != http.StatusOK {
		t.Fatalf("the password change must be 200, got %d: %s", changed.Code, changed.Body.String())
	}
	if revoked, _ := decodeBody(t, changed)["sessionsRevoked"].(bool); !revoked {
		t.Fatal("the response must report that sessions were revoked")
	}

	// Neither the caller's own token nor the other client's survives.
	for i, token := range []string{first, second} {
		rec := do(t, svc, http.MethodGet, "/api/v1/auth/session", "",
			map[string]string{"Authorization": "Bearer " + token})
		if rec.Code == http.StatusOK {
			t.Fatalf("session %d survived the password change", i)
		}
	}

	// The new password is the one that works now.
	if rec := do(t, svc, http.MethodPost, "/api/v1/auth/login",
		`{"username":"admin","password":"a-new-password"}`, nil); rec.Code != http.StatusOK {
		t.Fatalf("the new password must log in, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, svc, http.MethodPost, "/api/v1/auth/login",
		`{"username":"admin","password":"correct-password"}`, nil); rec.Code == http.StatusOK {
		t.Fatal("the old password still logs in")
	}
}

// TestChangePasswordRejectsAWrongCurrentPassword pins the other half: the current
// password is required, and a refused change must not revoke anything, or a
// mistyped field would log the operator out of a working panel.
func TestChangePasswordRejectsAWrongCurrentPassword(t *testing.T) {
	opts := testOptions(t)
	opts.AllowAnonymous = false
	hash, _ := HashPassword("correct-password")
	if err := (Config{Listen: "127.0.0.1", Port: 2095, Username: "admin", PasswordHash: hash}).Save(opts.ConfigPath); err != nil {
		t.Fatal(err)
	}
	svc := New(opts)

	login := do(t, svc, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"correct-password"}`, nil)
	token, _ := decodeBody(t, login)["token"].(string)

	refused := do(t, svc, http.MethodPost, "/api/v1/auth/password",
		`{"current":"not-the-password","next":"a-new-password"}`,
		map[string]string{"Authorization": "Bearer " + token})
	if refused.Code != http.StatusUnauthorized {
		t.Fatalf("a wrong current password must be 401, got %d: %s", refused.Code, refused.Body.String())
	}

	// The session is untouched, and the stored credential did not move.
	after := do(t, svc, http.MethodGet, "/api/v1/auth/session", "", map[string]string{"Authorization": "Bearer " + token})
	if after.Code != http.StatusOK {
		t.Fatalf("a refused change revoked the session, got %d", after.Code)
	}
	if rec := do(t, svc, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"correct-password"}`, nil); rec.Code != http.StatusOK {
		t.Fatalf("a refused change replaced the password, got %d", rec.Code)
	}
}

func TestCookieMutationRequiresCSRFHeader(t *testing.T) {
	opts := testOptions(t)
	opts.AllowAnonymous = false
	hash, _ := HashPassword("correct-password")
	if err := (Config{Listen: "127.0.0.1", Port: 2095, Username: "admin", PasswordHash: hash}).Save(opts.ConfigPath); err != nil {
		t.Fatal(err)
	}
	svc := New(opts)

	login := do(t, svc, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"correct-password"}`, nil)
	cookie := login.Result().Cookies()[0]
	auth := map[string]string{"Cookie": cookie.Name + "=" + cookie.Value}
	node := `{"name":"n1","protocol":"anytls","port":8000}`

	// A cookie-authenticated write without the header is refused: a cross-site
	// form can set the cookie but cannot set X-EasySB-Panel.
	if rec := do(t, svc, http.MethodPost, "/api/v1/nodes", node, auth); rec.Code != http.StatusForbidden {
		t.Fatalf("a cookie write without X-EasySB-Panel must be 403, got %d: %s", rec.Code, rec.Body.String())
	}

	// With the header it goes through.
	withHeader := map[string]string{"Cookie": cookie.Name + "=" + cookie.Value, "X-EasySB-Panel": "1"}
	if rec := do(t, svc, http.MethodPost, "/api/v1/nodes", node, withHeader); rec.Code != http.StatusCreated {
		t.Fatalf("a cookie write with X-EasySB-Panel must be 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// A bearer client does not rely on the ambient cookie, so it is exempt.
	bearer := map[string]string{"Authorization": "Bearer " + cookie.Value}
	if rec := do(t, svc, http.MethodPost, "/api/v1/nodes", `{"name":"n2","protocol":"anytls","port":8001}`, bearer); rec.Code != http.StatusCreated {
		t.Fatalf("a bearer write must not need the CSRF header, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestLoginRateLimitLocksAfterFiveFailures(t *testing.T) {
	opts := testOptions(t)
	opts.AllowAnonymous = false
	hash, _ := HashPassword("correct-password")
	if err := (Config{Listen: "127.0.0.1", Port: 2095, Username: "admin", PasswordHash: hash}).Save(opts.ConfigPath); err != nil {
		t.Fatal(err)
	}
	svc := New(opts)
	for i := 0; i < 5; i++ {
		if rec := do(t, svc, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"wrong"}`, nil); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d must be 401, got %d", i+1, rec.Code)
		}
	}
	if rec := do(t, svc, http.MethodPost, "/api/v1/auth/login", `{"username":"admin","password":"correct-password"}`, nil); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("after five failures the source must be locked, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestNodeCRUDAndDeleteClearsSelections(t *testing.T) {
	svc := New(testOptions(t))

	if rec := do(t, svc, http.MethodPost, "/api/v1/nodes", `{"name":"primary","protocol":"anytls","port":8000}`, nil); rec.Code != http.StatusCreated {
		t.Fatalf("create node: %d %s", rec.Code, rec.Body.String())
	}
	list := decodeBody(t, do(t, svc, http.MethodGet, "/api/v1/nodes", "", nil))
	nodes, _ := list["nodes"].([]any)
	if len(nodes) != 1 {
		t.Fatalf("the list has %d nodes, want 1", len(nodes))
	}
	first := nodes[0].(map[string]any)
	id, _ := first["id"].(string)
	if id == "" {
		t.Fatal("the created node has no id")
	}

	// An account that selected the node: With no node list the default is every
	// enabled node, which is the one we just made.
	if rec := do(t, svc, http.MethodPost, "/api/v1/users", `{"name":"alice"}`, nil); rec.Code != http.StatusCreated {
		t.Fatalf("create user: %d %s", rec.Code, rec.Body.String())
	}
	store, err := svc.loadUsers()
	if err != nil {
		t.Fatal(err)
	}
	account, _ := store.Find("alice")
	if !account.Selects(id) {
		t.Fatalf("the new account should have selected %s, nodes=%v", id, account.Nodes)
	}

	if rec := do(t, svc, http.MethodDelete, "/api/v1/nodes/"+id, "", nil); rec.Code != http.StatusOK {
		t.Fatalf("delete node: %d %s", rec.Code, rec.Body.String())
	}
	store, err = svc.loadUsers()
	if err != nil {
		t.Fatal(err)
	}
	account, _ = store.Find("alice")
	if account.Selects(id) {
		t.Fatal("a deleted node must not linger as an account selection")
	}
	if _, ok := account.Credentials[id]; ok {
		t.Fatal("a deleted node must not leave its credential behind")
	}
}

func TestSubscriptionsOverviewCarriesNoToken(t *testing.T) {
	svc := New(testOptions(t))
	if rec := do(t, svc, http.MethodPost, "/api/v1/nodes", `{"name":"primary","protocol":"anytls","port":8000}`, nil); rec.Code != http.StatusCreated {
		t.Fatalf("create node: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, svc, http.MethodPost, "/api/v1/users", `{"name":"alice"}`, nil); rec.Code != http.StatusCreated {
		t.Fatalf("create user: %d %s", rec.Code, rec.Body.String())
	}
	store, _ := svc.loadUsers()
	account, _ := store.Find("alice")

	rec := do(t, svc, http.MethodGet, "/api/v1/subscriptions", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("subscriptions: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if account.Token == "" || strings.Contains(body, account.Token) {
		t.Fatalf("the subscription overview leaked an account token: %s", body)
	}
	out := decodeBody(t, rec)
	if endpoint, _ := out["endpoint"].(string); !strings.Contains(endpoint, "example.com") {
		t.Fatalf("the endpoint should carry the state host, got %q", endpoint)
	}
}

func TestStaticServesEmbeddedIndex(t *testing.T) {
	svc := New(testOptions(t))
	rec := do(t, svc, http.MethodGet, "/", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("the SPA index must be 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("the index must be served as HTML, got %q", ct)
	}
}

// TestSecurityEntryMasksPanel checks that a configured entry is required to reach
// the panel at all: the bare address answers 404, and the panel (including its
// API) is only served under the prefix.
func TestSecurityEntryMasksPanel(t *testing.T) {
	opts := testOptions(t)
	hash, _ := HashPassword("correct-password")
	if err := (Config{Listen: "127.0.0.1", Port: 2095, Username: "admin", PasswordHash: hash, SecurityEntry: "manage"}).Save(opts.ConfigPath); err != nil {
		t.Fatal(err)
	}
	svc := New(opts)

	if rec := do(t, svc, http.MethodGet, "/", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("the bare address must be hidden, got %d", rec.Code)
	}
	if rec := do(t, svc, http.MethodGet, "/api/v1/panel", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("the bare API must be hidden, got %d", rec.Code)
	}
	index := do(t, svc, http.MethodGet, "/manage", "", nil)
	if index.Code != http.StatusOK {
		t.Fatalf("the entry must serve the panel, got %d", index.Code)
	}
	if !strings.Contains(index.Body.String(), `<base href="/manage/"`) {
		t.Fatalf("the served index must carry the entry base tag: %s", index.Body.String())
	}
	if rec := do(t, svc, http.MethodGet, "/manage/api/v1/panel", "", nil); rec.Code != http.StatusOK {
		t.Fatalf("the entry API must answer, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestPanelConfigUpdatePersistsAndSwapsEntry covers the settings write: it saves
// the listen address, port and security entry, and the entry takes effect on the
// running service without a restart.
func TestPanelConfigUpdatePersistsAndSwapsEntry(t *testing.T) {
	opts := testOptions(t)
	svc := New(opts)

	rec := do(t, svc, http.MethodPost, "/api/v1/panel/config", `{"listen":"127.0.0.1","port":3095,"securityEntry":"secret"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("panel/config: %d %s", rec.Code, rec.Body.String())
	}
	// A listen/port change is owned by the listener, so a restart is required;
	// the test process is not the unit, so Active() is false and none is spawned.
	if restart, _ := decodeBody(t, rec)["restartRequired"].(bool); !restart {
		t.Fatal("changing the port must report a restart is required")
	}

	cfg, err := LoadConfig(opts.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "127.0.0.1" || cfg.Port != 3095 || cfg.SecurityEntry != "secret" {
		t.Fatalf("config did not persist: %+v", cfg)
	}
	if rec := do(t, svc, http.MethodGet, "/", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("the entry must apply live, got %d", rec.Code)
	}
	if rec := do(t, svc, http.MethodGet, "/secret/api/v1/panel", "", nil); rec.Code != http.StatusOK {
		t.Fatalf("the new entry must answer live, got %d", rec.Code)
	}

	// Clearing the entry restores the root mount. The request must travel through
	// the entry that is now in force.
	if rec := do(t, svc, http.MethodPost, "/secret/api/v1/panel/config", `{"securityEntry":""}`, nil); rec.Code != http.StatusOK {
		t.Fatalf("clearing the entry: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, svc, http.MethodGet, "/", "", nil); rec.Code != http.StatusOK {
		t.Fatalf("the root must answer once the entry is cleared, got %d", rec.Code)
	}
}

func TestValidateSecurityEntryAndListenHost(t *testing.T) {
	if err := ValidateSecurityEntry(""); err != nil {
		t.Fatalf("an empty entry clears the setting: %v", err)
	}
	if err := ValidateSecurityEntry("/manage/"); err != nil {
		t.Fatalf("surrounding slashes are tolerated: %v", err)
	}
	if err := ValidateSecurityEntry("bad entry"); err == nil {
		t.Fatal("an entry with a space must be rejected")
	}
	if err := ValidateSecurityEntry("a/b"); err == nil {
		t.Fatal("an entry with a slash must be rejected")
	}
	if err := validateListenHost("0.0.0.0"); err != nil {
		t.Fatalf("0.0.0.0 is a valid listen address: %v", err)
	}
	if err := validateListenHost("::"); err != nil {
		t.Fatalf(":: is a valid listen address: %v", err)
	}
	if err := validateListenHost("panel.example.com"); err != nil {
		t.Fatalf("a host name is a valid listen address: %v", err)
	}
	if err := validateListenHost(""); err == nil {
		t.Fatal("an empty listen address must be rejected")
	}
	if err := validateListenHost("has space"); err == nil {
		t.Fatal("a listen address with a space must be rejected")
	}
}

// TestNetworkEndpoint checks the lightweight counter endpoint the dashboard
// polls for a rate: it must answer JSON with both cumulative byte counts.
func TestNetworkEndpoint(t *testing.T) {
	svc := New(testOptions(t))
	rec := do(t, svc, http.MethodGet, "/api/v1/system/network", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("network: %d %s", rec.Code, rec.Body.String())
	}
	out := decodeBody(t, rec)
	for _, key := range []string{"rxBytes", "txBytes", "readBytes", "writeBytes"} {
		if _, ok := out[key]; !ok {
			t.Fatalf("network response is missing %q: %s", key, rec.Body.String())
		}
	}
}
