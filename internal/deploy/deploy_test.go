package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EasySBTeam/EasySB/internal/cert"
	"github.com/EasySBTeam/EasySB/internal/node"
	"github.com/EasySBTeam/EasySB/internal/sbcore"
	"github.com/EasySBTeam/EasySB/internal/state"
	"github.com/EasySBTeam/EasySB/internal/user"
)

// writeInto points the deployment's config paths at a temporary directory for one test.
func writeInto(t *testing.T, dir string) {
	t.Helper()
	originalDir, originalPath := configDir, configPath
	t.Cleanup(func() { configDir, configPath = originalDir, originalPath })
	configDir = dir
	configPath = filepath.Join(dir, "config.json")
}

// testNodes are the protocols whose code needs no build tag, so the acceptance
// test runs in every build.
func testNodes() []node.Node {
	return []node.Node{
		node.New(state.ProtoAnyTLS, state.ProtoAnyTLS, 8000, nil),
		node.New(state.ProtoVMessWSTLS, state.ProtoVMessWSTLS, 8004, nil),
	}
}

// allProtocolNodes returns one node per protocol, which only a build carrying
// every tag can render.
func allProtocolNodes() []node.Node {
	nodes := make([]node.Node, 0, len(state.Keys))
	for i, key := range state.Keys {
		nodes = append(nodes, node.New(key, key, 8000+i, nil))
	}
	return nodes
}

// testAccount returns one account that selected every given node, with credentials.
func testAccount(t *testing.T, nodes []node.Node) user.User {
	t.Helper()
	selections := make([]user.Selection, 0, len(nodes))
	for _, n := range nodes {
		selections = append(selections, user.Selection{Node: n.ID, Protocol: n.Protocol})
	}
	account := user.New("alice", selections, time.Now())
	account.EnsureCredentials(nil)
	if account.Token == "" {
		t.Fatal("the test account has no token: the stats user list is keyed by it")
	}
	return account
}

// installTestCertificate puts a certificate pair for the domain where cert.Paths looks
// for it, so rendering a configuration needs no ACME account and touches nothing outside
// the temporary certificate directory.
func installTestCertificate(t *testing.T, dir, domain string) {
	t.Helper()
	t.Setenv(cert.DirEnv, dir)
	pairDir := filepath.Join(dir, domain)
	if err := os.MkdirAll(pairDir, 0o755); err != nil {
		t.Fatalf("create certificate dir: %v", err)
	}
	if err := cert.GenerateSelfSigned(filepath.Join(pairDir, "fullchain.cer"), filepath.Join(pairDir, "private.key"), domain); err != nil {
		t.Fatalf("generate self-signed pair: %v", err)
	}
}

// TestGeneratedConfigIsAcceptedByTheCarriedCore is the acceptance test for the whole
// arrangement: the document the panel renders has to be accepted by the core compiled
// into this binary. There is no second core to fall back on and no sing-box in PATH — a
// document this engine refuses is a node that never starts, and the panel would only
// find out from the service's journal.
func TestGeneratedConfigIsAcceptedByTheCarriedCore(t *testing.T) {
	const domain = "example.com"
	installTestCertificate(t, t.TempDir(), domain)

	cfg := state.Default()
	cfg.Domain = domain

	nodes := testNodes()
	account := testAccount(t, nodes)
	document, err := ServerConfig(cfg, nodes, []user.User{account})
	if err != nil {
		t.Fatalf("ServerConfig: %v", err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, document, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := sbcore.Check(context.Background(), path); err != nil {
		t.Fatalf("the carried core refused the generated config: %v\n%s", err, document)
	}

	// The counters block follows the build, not the host: a core without the V2Ray API
	// rejects the whole document, so its presence has to track StatsCapable exactly.
	carries := strings.Contains(string(document), `"v2ray_api"`)
	if carries != sbcore.StatsCapable() {
		t.Fatalf("config carries v2ray_api = %v, but this build counts traffic = %v", carries, sbcore.StatsCapable())
	}
	if carries && !strings.Contains(string(document), account.Token) {
		t.Fatal("the stats user list must name the account token, or usage is never counted")
	}
}

// TestServerConfigNeedsNoCoreOnDisk pins the property that replaced the old "is the core
// installed" gate: rendering a configuration depends on the accounts, the state and the
// certificate, and on nothing on the host.
func TestServerConfigNeedsNoCoreOnDisk(t *testing.T) {
	const domain = "example.com"
	installTestCertificate(t, t.TempDir(), domain)

	cfg := state.Default()
	cfg.Domain = domain
	document, err := ServerConfig(cfg, testNodes(), nil)
	if err != nil {
		t.Fatalf("ServerConfig with no accounts: %v", err)
	}
	if len(document) == 0 {
		t.Fatal("no document rendered")
	}
	// A node without accounts is legal and starts; only the listener that carries them
	// needs a member to be there at all.
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, document, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := sbcore.Check(context.Background(), path); err != nil {
		t.Fatalf("the carried core refused a node with no accounts: %v\n%s", err, document)
	}
}

// TestServerConfigRefusesRealityWithoutKeypair keeps the renderer from producing a
// document the core rejects as a whole: sing-box refuses a Reality inbound whose private
// key is empty, and an error naming the missing keypair is more use than the core's own
// message arriving from the service's journal. This one asks the renderer only, so it
// holds in a build without the Reality tag too.
func TestServerConfigRefusesRealityWithoutKeypair(t *testing.T) {
	const domain = "example.com"
	installTestCertificate(t, t.TempDir(), domain)

	cfg := state.Default()
	cfg.Domain = domain
	reality := node.Node{
		ID:       "reality",
		Name:     "reality",
		Protocol: state.ProtoVLESSReality,
		Port:     8003,
		Enabled:  true,
		Params:   map[string]string{node.ParamRealityPrivate: ""},
	}
	if _, err := ServerConfig(cfg, []node.Node{reality}, nil); !errors.Is(err, ErrNoRealityKey) {
		t.Fatalf("ServerConfig without a keypair = %v, want %v", err, ErrNoRealityKey)
	}
}

// TestApplyConfigKeepsCredentialsRootOnly covers the one file the deploy path writes
// that carries every account's credentials: the uuid and password of each enabled
// protocol. The account store is 0600 and this document is the same secret in another
// shape, so a group or world readable mode here hands every local user of the host a set
// of working credentials.
//
// It asserts through ApplyConfig rather than a separate writer, because ApplyConfig is
// the only path that installs a document, and it is the one a regression would reach.
func TestApplyConfigKeepsCredentialsRootOnly(t *testing.T) {
	const domain = "example.com"
	installTestCertificate(t, t.TempDir(), domain)

	dir := t.TempDir()
	writeInto(t, dir)

	cfg := state.Default()
	cfg.Domain = domain

	nodes := testNodes()
	account := testAccount(t, nodes)
	document, err := ApplyConfig(context.Background(), cfg, nodes, []user.User{account})
	if err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	path := filepath.Join(dir, "config.json")
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the written config: %v", err)
	}
	if string(written) != string(document) {
		t.Fatal("the file on disk is not the document that was returned")
	}

	// The rename carries the temporary file's mode, so a config.json an earlier
	// version left at 0644 has to come back at 0600 rather than be left alone.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("widen the mode: %v", err)
	}
	if _, err := ApplyConfig(context.Background(), cfg, nodes, []user.User{account}); err != nil {
		t.Fatalf("ApplyConfig over an existing file: %v", err)
	}

	if runtime.GOOS == "windows" {
		// Windows has no Unix permission bits, so only the write path is asserted here.
		// The mode assertion is what Linux CI runs.
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat the written config: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("config.json mode = %04o, want 0600: it carries every account's credentials", got)
	}
}

// TestApplyConfigLeavesNoTemporaryBehind pins the other half of the atomic install: a
// document that is accepted leaves exactly the live file, and one the core refuses
// leaves the previous document untouched and no check file behind.
func TestApplyConfigLeavesNoTemporaryBehind(t *testing.T) {
	const domain = "example.com"
	installTestCertificate(t, t.TempDir(), domain)

	dir := t.TempDir()
	writeInto(t, dir)

	cfg := state.Default()
	cfg.Domain = domain
	nodes := testNodes()
	account := testAccount(t, nodes)

	if _, err := ApplyConfig(context.Background(), cfg, nodes, []user.User{account}); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	accepted, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatalf("read the accepted config: %v", err)
	}

	original := checkConfig
	t.Cleanup(func() { checkConfig = original })
	checkConfig = func(context.Context, string) error {
		return errors.New("the core refused this one")
	}
	if _, err := ApplyConfig(context.Background(), cfg, nodes, []user.User{account}); !errors.Is(err, ErrRejected) {
		t.Fatalf("ApplyConfig with a refusal = %v, want %v", err, ErrRejected)
	}

	if after, err := os.ReadFile(filepath.Join(dir, "config.json")); err != nil || !bytes.Equal(after, accepted) {
		t.Fatalf("a refused document changed the live config (err %v)", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "config.json" {
			t.Fatalf("a refused deploy left %q behind", e.Name())
		}
	}
}

// TestApplyWithNoNodesIsNoop pins the early return: a host whose node set is still
// empty must not be made to fail on a configuration there is no certificate for.
func TestApplyWithNoNodesIsNoop(t *testing.T) {
	writeInto(t, t.TempDir())

	cfg := state.Default()

	if err := Apply(context.Background(), cfg, nil, []user.User{testAccount(t, testNodes())}); !errors.Is(err, ErrNoNodes) {
		t.Fatalf("Apply with no node = %v, want %v", err, ErrNoNodes)
	}
}

// stubService replaces the systemd operations for one test, so the deploy's success
// path can be driven on a host with no service to start. It reports whether the unit
// was written and which actions were performed.
func stubService(t *testing.T, active bool) *[]string {
	t.Helper()
	originalActive, originalDo, originalWrite := serviceActive, serviceDo, serviceWriteUnit
	t.Cleanup(func() { serviceActive, serviceDo, serviceWriteUnit = originalActive, originalDo, originalWrite })

	var actions []string
	serviceActive = func(context.Context) bool { return active }
	serviceDo = func(_ context.Context, action string) error {
		actions = append(actions, action)
		return nil
	}
	serviceWriteUnit = func() error { return nil }
	return &actions
}

// TestApplyStorePersistsWhatItApplied covers the one write path the panel, the TUI
// and --provision all go through. It had no test: panel_test.go injects a no-op
// Apply, so nothing exercised ApplyStore at all.
//
// The property under test is the one the accounting loop depends on: a change that
// was written to disk must come back marked as applied, because the loop acts on
// the transition rather than restarting the core every cycle. The core itself is
// not on the host here, so the deploy stops at the service step; the document and
// the store are both written before that, which is what is asserted.
func TestApplyStorePersistsWhatItApplied(t *testing.T) {
	const domain = "example.com"
	installTestCertificate(t, t.TempDir(), domain)

	writeInto(t, t.TempDir())
	original := checkConfig
	t.Cleanup(func() { checkConfig = original })
	// The engine accepts the document. Running the real one is what
	// TestGeneratedConfigIsAcceptedByTheCarriedCore covers; here the point is the
	// store, and building the engine would only make this test slower.
	checkConfig = func(context.Context, string) error { return nil }

	dir := t.TempDir()
	nodesPath := filepath.Join(dir, "easysb-nodes.json")
	accountsPath := filepath.Join(dir, "easysb-users.json")

	store, err := node.Load(nodesPath)
	if err != nil {
		t.Fatal(err)
	}
	nodes := testNodes()
	for _, n := range nodes {
		if err := store.Add(n); err != nil {
			t.Fatalf("seed the node store: %v", err)
		}
	}

	accounts, err := user.Load(accountsPath)
	if err != nil {
		t.Fatal(err)
	}
	account := testAccount(t, nodes)
	if err := accounts.Add(account); err != nil {
		t.Fatalf("seed the account store: %v", err)
	}

	cfg := state.Default()
	cfg.Domain = domain

	// The host has no running core, so the deploy installs and starts the unit.
	actions := stubService(t, false)

	if err := ApplyStore(context.Background(), cfg, nodesPath, accountsPath); err != nil {
		t.Fatalf("ApplyStore: %v", err)
	}
	if len(*actions) == 0 || (*actions)[len(*actions)-1] != "start" {
		t.Fatalf("a first deploy did not start the core: %v", *actions)
	}

	written, err := os.ReadFile(filepath.Join(configDir, "config.json"))
	if err != nil {
		t.Fatalf("ApplyStore did not install a config: %v", err)
	}
	if !strings.Contains(string(written), account.Token) {
		t.Fatal("the installed config does not carry the account's core user name")
	}

	after, err := user.Load(accountsPath)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := after.Find(account.Name)
	if !ok {
		t.Fatal("the account disappeared from the store")
	}
	if !got.Applied {
		t.Fatal("an account that was applied is not marked as applied: the accounting loop would never act on it")
	}
	if !got.CredentialsReady() {
		t.Fatal("the applied account has no credentials recorded")
	}
}

// applyStoreFixture prepares the two stores and the config paths an ApplyStore test
// needs, with the engine stubbed to accept and the host pretending to have no
// running core. It returns the config and the store paths.
func applyStoreFixture(t *testing.T, domain string) (state.Config, string, string) {
	t.Helper()
	installTestCertificate(t, t.TempDir(), domain)
	writeInto(t, t.TempDir())
	originalCheck := checkConfig
	t.Cleanup(func() { checkConfig = originalCheck })
	checkConfig = func(context.Context, string) error { return nil }
	stubService(t, false)

	dir := t.TempDir()
	return state.Config{Domain: domain, ServerIP: "203.0.113.9"},
		filepath.Join(dir, "easysb-nodes.json"),
		filepath.Join(dir, "easysb-users.json")
}

// seedNodes writes the given nodes into a fresh node store.
func seedNodes(t *testing.T, path string, nodes []node.Node) {
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

// TestApplyStoreSeesANodeWrittenBeforeItRan is the plain ordering property: the
// document is built from the node store as it is on disk when the deploy starts, not
// from a snapshot taken earlier. It is the shape the concurrency test below makes
// adversarial.
func TestApplyStoreSeesANodeWrittenBeforeItRan(t *testing.T) {
	cfg, nodesPath, accountsPath := applyStoreFixture(t, "example.com")
	nodes := testNodes()
	seedNodes(t, nodesPath, nodes)

	accounts, err := user.Load(accountsPath)
	if err != nil {
		t.Fatal(err)
	}
	account := testAccount(t, nodes)
	if err := accounts.Add(account); err != nil {
		t.Fatal(err)
	}

	if err := ApplyStore(context.Background(), cfg, nodesPath, accountsPath); err != nil {
		t.Fatalf("ApplyStore: %v", err)
	}

	written, err := os.ReadFile(filepath.Join(configDir, "config.json"))
	if err != nil {
		t.Fatalf("read the installed config: %v", err)
	}
	// One inbound per enabled node, and the account on each.
	for _, n := range nodes {
		if got := strings.Count(string(written), `"tag": "`+n.ID+`"`); got != 1 {
			t.Fatalf("node %q has %d inbounds in the installed config, want 1:\n%s", n.Name, got, written)
		}
	}
}

// TestApplyStoreDeploysEveryNodeOnDisk is the plain ordering property: the document
// is built from the node store as it is on disk, not from a snapshot taken before
// some earlier step. It is the shape the concurrency tests below make adversarial,
// and it is deliberately named for what it checks - "every node on disk appears in
// the document" - rather than for the lock discipline, which a single-threaded test
// cannot distinguish.
func TestApplyStoreDeploysEveryNodeOnDisk(t *testing.T) {
	cfg, nodesPath, accountsPath := applyStoreFixture(t, "example.com")

	first := testNodes()[0]
	seedNodes(t, nodesPath, []node.Node{first})

	// The account selects whatever nodes exist when it is created, so it is written
	// after the second node so that it covers both.
	second := node.New(state.ProtoTUIC, "second", 8100, nil)

	if err := ApplyStore(context.Background(), cfg, nodesPath, accountsPath); err != nil {
		t.Fatalf("first ApplyStore: %v", err)
	}
	afterFirst, err := os.ReadFile(filepath.Join(configDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(afterFirst), `"tag": "`+first.ID+`"`) {
		t.Fatal("the first deploy is missing the only node that existed")
	}

	// The panel adds a node. This is the write the stale snapshot used to miss.
	seedNodes(t, nodesPath, []node.Node{second})

	// An account that may use both, written the way the panel writes one.
	accounts, err := user.Load(accountsPath)
	if err != nil {
		t.Fatal(err)
	}
	combined := testAccount(t, []node.Node{first, second})
	if err := accounts.Add(combined); err != nil {
		t.Fatal(err)
	}

	if err := ApplyStore(context.Background(), cfg, nodesPath, accountsPath); err != nil {
		t.Fatalf("second ApplyStore: %v", err)
	}
	afterSecond, err := os.ReadFile(filepath.Join(configDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(afterSecond), `"tag": "`+second.ID+`"`) {
		t.Fatalf("the node added before the deploy is missing from the installed config:\n%s", afterSecond)
	}
	if !strings.Contains(string(afterSecond), combined.Token) {
		t.Fatal("the newly added account is not authenticated by the installed config")
	}
}

// TestApplyStoreSavesRepairedCredentialsBeforeItTouchesTheCore is the regression test
// for the credential ordering.
//
// Repair mints a credential for an account that has none, and the core is then
// restarted to authenticate with it. If the store is only saved afterwards, the
// running core accepts a uuid that exists nowhere on disk, and the next repair mints
// a different one and breaks every client that already imported the subscription.
//
// The check is load-bearing rather than incidental: it runs while the deploy is in
// progress, reads the account store from disk, and fails the deploy if the
// credential the renderer used is not there yet. Deleting the save before the deploy
// makes this test fail through that path.
func TestApplyStoreSavesRepairedCredentialsBeforeItTouchesTheCore(t *testing.T) {
	cfg, nodesPath, accountsPath := applyStoreFixture(t, "example.com")
	nodes := testNodes()
	seedNodes(t, nodesPath, nodes)

	// An account with no credentials recorded, which is the state a hand-edited or
	// legacy file leaves. It is planted as raw JSON rather than through user.New,
	// because New mints the credential fields its selections need: reproducing the
	// legacy shape is the point, so the document is written the way an operator's
	// older or hand-edited file would look.
	nodeIDs := make([]string, 0, len(nodes))
	for _, n := range nodes {
		nodeIDs = append(nodeIDs, n.ID)
	}
	legacy, err := json.Marshal(map[string]any{
		"version": user.CurrentVersion,
		"users": []map[string]any{{
			"name":    "legacy",
			"token":   "legacytoken",
			"enabled": true,
			"nodes":   nodeIDs,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(accountsPath, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	// The account must be routable for the deploy to have to repair it, and it must
	// start out without credentials or the test proves nothing.
	pre, err := user.Load(accountsPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := pre.Find("legacy"); !ok {
		t.Fatal("the fixture account was not loaded")
	} else if got.CredentialsReady() {
		t.Fatal("the fixture account already has credentials, so the repair path is not exercised")
	}
	if len(pre.Routable(time.Now())) != 0 {
		t.Fatal("the fixture account is already routable, so the deploy never repairs it")
	}

	// What the store held while the deploy ran, captured from inside the deploy.
	var (
		mu       sync.Mutex
		observed []user.User
		checked  bool
	)
	checkConfig = func(context.Context, string) error {
		onDisk, err := user.Load(accountsPath)
		if err != nil {
			return fmt.Errorf("the deploy cannot read the account store: %w", err)
		}
		mu.Lock()
		observed = onDisk.Users()
		checked = true
		mu.Unlock()
		return nil
	}

	if err := ApplyStore(context.Background(), cfg, nodesPath, accountsPath); err != nil {
		t.Fatalf("ApplyStore: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if !checked {
		t.Fatal("the acceptance check never ran, so nothing was observed")
	}
	if len(observed) != 1 {
		t.Fatalf("the store held %d accounts during the deploy, want 1", len(observed))
	}
	if !observed[0].CredentialsReady() {
		t.Fatal("the core was restarted with credentials the store had not recorded: " +
			"a crash here leaves the core authenticating a uuid that is nowhere on disk")
	}
}

// TestApplyStoreDoesNotHoldAStoreLockAcrossTheDeploy proves the deploy runs with
// neither store locked. The acceptance check is the only point inside the deploy a
// test can reach, so it performs a real locked write to the account store: with the
// lock held across the deploy that write blocks, fails, and fails the deploy, and
// when the lock is released before the deploy it succeeds and is still there
// afterwards.
func TestApplyStoreDoesNotHoldAStoreLockAcrossTheDeploy(t *testing.T) {
	cfg, nodesPath, accountsPath := applyStoreFixture(t, "example.com")
	nodes := testNodes()
	seedNodes(t, nodesPath, nodes)

	accounts, err := user.Load(accountsPath)
	if err != nil {
		t.Fatal(err)
	}
	account := testAccount(t, nodes)
	if err := accounts.Add(account); err != nil {
		t.Fatal(err)
	}

	const remark = "written while the deploy was running"
	checkConfig = func(context.Context, string) error {
		// A locked read-modify-write from another actor, which is what the panel and
		// the accounting loop both do. It must not have to wait for the deploy.
		done := make(chan error, 1)
		go func() {
			done <- withUserLock(accountsPath, func(s *user.Store) error {
				return s.Update(account.Name, func(u *user.User) error {
					u.Remark = remark
					return nil
				})
			})
		}()
		select {
		case err := <-done:
			if err != nil {
				return fmt.Errorf("a locked write during the deploy failed: %w", err)
			}
		case <-time.After(5 * time.Second):
			return errors.New("a locked write during the deploy blocked: the deploy is holding the store lock")
		}
		return nil
	}

	if err := ApplyStore(context.Background(), cfg, nodesPath, accountsPath); err != nil {
		t.Fatalf("ApplyStore: %v", err)
	}

	// The concurrent write was not clobbered by the deploy's own bookkeeping, which
	// is what re-loading under the lock for the final step is for.
	after, err := user.Load(accountsPath)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := after.Find(account.Name)
	if !ok {
		t.Fatal("the account disappeared")
	}
	if got.Remark != remark {
		t.Fatalf("the write made during the deploy was lost: remark = %q, want %q", got.Remark, remark)
	}
	if !got.Applied {
		t.Fatal("the account is not marked as applied after a successful deploy")
	}
	if !got.CredentialsReady() {
		t.Fatal("the account has no credentials recorded after the deploy")
	}
}

// TestApplyStoreKeepsTheCoreRejectionReason goes through ApplyStore rather than
// Apply, so the reason reaches the operator from the path the panel actually calls.
func TestApplyStoreKeepsTheCoreRejectionReason(t *testing.T) {
	const domain = "example.com"
	installTestCertificate(t, t.TempDir(), domain)

	writeInto(t, t.TempDir())
	original := checkConfig
	t.Cleanup(func() { checkConfig = original })
	checkConfig = func(context.Context, string) error {
		return errors.New(`unknown inbound type: "no-such-inbound"`)
	}

	dir := t.TempDir()
	nodesPath := filepath.Join(dir, "easysb-nodes.json")
	accountsPath := filepath.Join(dir, "easysb-users.json")

	store, err := node.Load(nodesPath)
	if err != nil {
		t.Fatal(err)
	}
	nodes := testNodes()
	for _, n := range nodes {
		if err := store.Add(n); err != nil {
			t.Fatal(err)
		}
	}

	cfg := state.Default()
	cfg.Domain = domain

	err = ApplyStore(context.Background(), cfg, nodesPath, accountsPath)
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("ApplyStore = %v, want an error that wraps %v", err, ErrRejected)
	}
	if !strings.Contains(err.Error(), "no-such-inbound") {
		t.Fatalf("ApplyStore dropped the core's own message: %v", err)
	}
}

// TestLoadNodesTreatsAnEmptyPathAsNoNodes pins the guard the callers rely on: the
// panel and the TUI can be pointed at a host before the node store exists.
func TestLoadNodesTreatsAnEmptyPathAsNoNodes(t *testing.T) {
	nodes, err := LoadNodes("")
	if err != nil {
		t.Fatalf("LoadNodes(\"\") = %v", err)
	}
	if len(nodes) != 0 {
		t.Fatalf("LoadNodes(\"\") returned %d nodes", len(nodes))
	}

	// A store that does not exist yet is empty rather than an error.
	nodes, err = LoadNodes(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("LoadNodes of a missing file: %v", err)
	}
	if len(nodes) != 0 {
		t.Fatalf("a missing store yielded %d nodes", len(nodes))
	}
}

// TestServerConfigRefusesAnUnknownProtocol keeps the renderer from writing a
// document the core would refuse as a whole, for a node the node store would not
// have accepted either.
func TestServerConfigRefusesAnUnknownProtocol(t *testing.T) {
	const domain = "example.com"
	installTestCertificate(t, t.TempDir(), domain)

	cfg := state.Default()
	cfg.Domain = domain
	rogue := node.Node{
		ID:       "rogue",
		Name:     "rogue",
		Protocol: "no-such-protocol",
		Port:     8100,
		Enabled:  true,
		Params:   map[string]string{},
	}
	if _, err := ServerConfig(cfg, []node.Node{rogue}, nil); err == nil {
		t.Fatal("ServerConfig rendered a node with an unknown protocol")
	}
}

// TestServerConfigRefusesAnOutOfRangePort is the renderer's own guard for a
// hand-edited store, which the node store's Validate never saw.
func TestServerConfigRefusesAnOutOfRangePort(t *testing.T) {
	const domain = "example.com"
	installTestCertificate(t, t.TempDir(), domain)

	cfg := state.Default()
	cfg.Domain = domain
	bad := node.Node{
		ID:       "bad-port",
		Name:     "bad-port",
		Protocol: state.ProtoAnyTLS,
		Port:     70000,
		Enabled:  true,
		Params:   map[string]string{},
	}
	if _, err := ServerConfig(cfg, []node.Node{bad}, nil); err == nil {
		t.Fatal("ServerConfig rendered a port outside 1-65535")
	}
}

// TestApplyKeepsTheCoreRejectionReason is why the sentinel is wrapped rather than
// returned bare: the panel's only report of a rejection is a log line, and the core's
// own message is what names the field the operator has to fix.
func TestApplyKeepsTheCoreRejectionReason(t *testing.T) {
	const domain = "example.com"
	installTestCertificate(t, t.TempDir(), domain)
	writeInto(t, t.TempDir())

	original := checkConfig
	t.Cleanup(func() { checkConfig = original })
	checkConfig = func(context.Context, string) error {
		return errors.New(`unknown inbound type: "no-such-inbound"`)
	}

	cfg := state.Default()
	cfg.Domain = domain

	nodes := testNodes()
	err := Apply(context.Background(), cfg, nodes, []user.User{testAccount(t, nodes)})
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("Apply = %v, want an error that wraps %v", err, ErrRejected)
	}
	if !strings.Contains(err.Error(), "no-such-inbound") {
		t.Fatalf("Apply dropped the core's own message: %v", err)
	}
}
