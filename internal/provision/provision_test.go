package provision

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/EasySBTeam/EasySB/internal/cert"
	"github.com/EasySBTeam/EasySB/internal/node"
	"github.com/EasySBTeam/EasySB/internal/state"
	"github.com/EasySBTeam/EasySB/internal/user"
)

// TestExampleManifestRuns keeps the shipped example honest: a field renamed in
// the struct, or an account selecting a node the example never declares, fails
// here rather than under a user's first `--provision`.
func TestExampleManifestRuns(t *testing.T) {
	data, err := os.ReadFile("../../docs/provision.example.json")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := Parse(data)
	if err != nil {
		t.Fatalf("shipped example does not parse: %v", err)
	}
	if _, err := Run(context.Background(), spec, testOptions(t)); err != nil {
		t.Fatalf("shipped example does not run: %v", err)
	}
}

// testOptions points the stores at a temp directory and replaces every call
// that would touch the host with a no-op, so a run is a pure function of the
// spec and the files it reads.
func testOptions(t *testing.T) Options {
	t.Helper()
	dir := t.TempDir()
	return Options{
		NodesPath:     filepath.Join(dir, "nodes.json"),
		AccountsPath:  filepath.Join(dir, "users.json"),
		Now:           func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
		Log:           func(string) {},
		LoadConfig:    func() state.Config { return state.Config{} },
		SaveConfig:    func(state.Config) error { return nil },
		Preflight:     func(context.Context, string) cert.Report { return cert.Report{} },
		EnsureAccount: func(context.Context, string, func(string)) error { return nil },
		Issue:         func(context.Context, string, string, func(string)) error { return nil },
		CheckPort80:   func() error { return nil },
		CertPaths:     func(string) (string, string, bool) { return "c", "k", true },
		InstallTimer:  func(context.Context, func(string)) error { return nil },
		ServiceActive: func(context.Context) bool { return false },
		ServiceDo:     func(context.Context, string) error { return nil },
		ApplyStore:    func(context.Context, state.Config, string, string) error { return nil },
		SubWriteUnit:  func() error { return nil },
		SubDo:         func(context.Context, string) error { return nil },
		SubURL:        func(state.Config, string) string { return "http://test/sub/token" },
		FWApply:       func(context.Context, state.Config, []node.Node, func(string)) error { return nil },
		FWWriteUnit:   func([]node.Node) error { return nil },
		FWUnitAction:  func(context.Context, string) error { return nil },
	}
}

// TestRunKeepsANodeItNeverSaw is a regression guard on the node store rewrite: the
// store is written whole, so a node provision never loaded disappears from the file
// when provision next saves. The node here is one the panel added, which provision
// must leave alone.
//
// It is a guard, not a proof of the lock. A node already on disk at load time
// survives whether or not the cycle is locked, because both read the same file; what
// the lock adds is the case where a write lands between the read and the save, which
// a single-process test cannot schedule deterministically. The lock is recorded as
// verified by inspection in docs/optimization-plan.md rather than claimed as covered
// here.
func TestRunKeepsANodeItNeverSaw(t *testing.T) {
	opt := testOptions(t)
	spec, err := Parse([]byte(`{
		"domain": "example.com",
		"email": "ops@example.com",
		"accounts": [{"name": "alice"}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), spec, opt); err != nil {
		t.Fatal(err)
	}
	before, err := node.Load(opt.NodesPath)
	if err != nil {
		t.Fatal(err)
	}

	// A node the panel adds afterwards. A second provision must not lose it: it is
	// not in the spec, so provision never touches it, but it does write the file.
	if err := before.Add(node.New(state.ProtoTUIC, "panel-added", 8100, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), spec, opt); err != nil {
		t.Fatal(err)
	}

	after, err := node.Load(opt.NodesPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := after.ByID(before.Nodes()[len(before.Nodes())-1].ID); !ok {
		t.Fatal("the node the panel added was lost by the next provision")
	}
	if after.Len() != before.Len() {
		t.Fatalf("provision changed the node count: %d -> %d", before.Len(), after.Len())
	}
}

// TestRunDoesNotOverwriteAConcurrentWrite exercises the "loaded before, saved after"
// shape that makes whole-file stores lose an update: a writer that took its copy
// before provision's save and saves after it erases provision's work.
//
// As with the test above, this is a guard rather than a proof of the lock: the
// writer is driven sequentially, so it is saved after provision has finished rather
// than from inside the read-modify-write window. Closing that window for real means
// making the writer block until it acquires the store lock, which a single-process
// test cannot do without deadlocking the very save it is waiting for.
func TestRunDoesNotOverwriteAConcurrentWrite(t *testing.T) {
	opt := testOptions(t)
	spec, err := Parse([]byte(`{
		"domain": "example.com",
		"email": "ops@example.com",
		"accounts": [{"name": "alice"}]
	}`))
	if err != nil {
		t.Fatal(err)
	}

	const marker = "written by another actor during provision"
	var concurrent *user.Store
	opt.Issue = func(context.Context, string, string, func(string)) error {
		// Another actor's read-modify-write, taken while provision is still inside
		// its own locked group. The pointer is captured here and saved below, which
		// is the "loaded before, saved after" shape.
		if err := withUserLock(opt.AccountsPath, func(s *user.Store) error {
			concurrent = s
			return nil
		}); err != nil {
			return err
		}
		return nil
	}

	if _, err := Run(context.Background(), spec, opt); err != nil {
		t.Fatal(err)
	}
	if concurrent == nil {
		t.Fatal("the concurrent writer never captured the store")
	}

	// The other actor saves now, using the copy it took before provision finished.
	if err := concurrent.Update("alice", func(u *user.User) error {
		u.Remark = marker
		return nil
	}); err != nil {
		t.Fatalf("the concurrent write failed: %v", err)
	}

	store, err := user.Load(opt.AccountsPath)
	if err != nil {
		t.Fatal(err)
	}
	u, ok := store.Find("alice")
	if !ok {
		t.Fatal("account alice disappeared")
	}
	// The account provision wrote must still be there, with the other actor's change
	// applied rather than lost.
	if !u.CredentialsReady() {
		t.Fatal("the account provision created was erased by the concurrent save")
	}
	if u.Remark != marker {
		t.Fatalf("the concurrent write was lost: remark = %q, want %q", u.Remark, marker)
	}
}

// TestRunRejectsAnAccountWhoseNodeIsUnknown keeps the failure path honest: an
// account that selects a node the spec never declares fails, and the failure is
// reported rather than papered over.
func TestRunRejectsAnAccountWhoseNodeIsUnknown(t *testing.T) {
	opt := testOptions(t)
	spec, err := Parse([]byte(`{
		"domain": "example.com",
		"email": "ops@example.com",
		"nodes": [
			{"protocol": "anytls", "name": "only", "port": 8000},
			{"protocol": "tuic", "name": "second", "port": 8002}
		],
		"accounts": [{"name": "dave", "nodes": ["does-not-exist"]}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), spec, opt); err == nil {
		t.Fatal("want an error for an account selecting a node the spec does not declare")
	}
}

func TestParseFillsNodeDefaults(t *testing.T) {
	spec, err := Parse([]byte(`{"domain":"example.com","email":"ops@example.com"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(spec.Nodes) != len(state.Keys) {
		t.Fatalf("got %d nodes, want %d", len(spec.Nodes), len(state.Keys))
	}
	for _, n := range spec.Nodes {
		if n.Port == 0 || n.Name == "" {
			t.Fatalf("node %q was not filled in: %+v", n.Protocol, n)
		}
	}
}

func TestParseRejectsUnknownField(t *testing.T) {
	_, err := Parse([]byte(`{"domain":"example.com","email":"a@b.c","nodse":[]}`))
	if err == nil || !strings.Contains(err.Error(), "nodse") {
		t.Fatalf("want an unknown-field error, got %v", err)
	}
}

func TestParseRealityOnlyNeedsNoDomain(t *testing.T) {
	spec, err := Parse([]byte(`{"nodes":[{"protocol":"vless-reality"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if spec.Nodes[0].Port != 8003 {
		t.Fatalf("default reality port = %d, want 8003", spec.Nodes[0].Port)
	}
}

func TestRunIsIdempotentAndKeepsTokens(t *testing.T) {
	opt := testOptions(t)
	spec, err := Parse([]byte(`{
		"domain": "example.com",
		"email": "ops@example.com",
		"accounts": [{"name": "alice", "quota_gb": 100, "expire_days": 30}]
	}`))
	if err != nil {
		t.Fatal(err)
	}

	first, err := Run(context.Background(), spec, opt)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Nodes) != len(state.Keys) {
		t.Fatalf("first run deployed %d nodes, want %d", len(first.Nodes), len(state.Keys))
	}
	if len(first.Accounts) != 1 || first.Accounts[0].Token == "" {
		t.Fatalf("first run accounts = %+v", first.Accounts)
	}
	token := first.Accounts[0].Token

	second, err := Run(context.Background(), spec, opt)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Nodes) != len(first.Nodes) {
		t.Fatalf("second run has %d nodes, want %d", len(second.Nodes), len(first.Nodes))
	}
	if len(second.Accounts) != 1 || second.Accounts[0].Token != token {
		t.Fatalf("token changed on re-run: %q -> %+v", token, second.Accounts)
	}
}

func TestRunAppliesAccountCredentials(t *testing.T) {
	opt := testOptions(t)
	spec, err := Parse([]byte(`{
		"domain": "example.com",
		"email": "ops@example.com",
		"accounts": [{
			"name": "bob",
			"nodes": ["vmess-ws-tls"],
			"uuid": "11111111-1111-1111-1111-111111111111"
		}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), spec, opt); err != nil {
		t.Fatal(err)
	}

	store, err := user.Load(opt.AccountsPath)
	if err != nil {
		t.Fatal(err)
	}
	u, ok := store.Find("bob")
	if !ok {
		t.Fatal("account bob was not stored")
	}
	if len(u.Nodes) != 1 {
		t.Fatalf("bob selects %d nodes, want 1", len(u.Nodes))
	}
	if got := u.Credentials[u.Nodes[0]].UUID; got != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("uuid = %q, want the supplied value", got)
	}
}

func TestRunRejectsUnknownSelectedNode(t *testing.T) {
	opt := testOptions(t)
	spec, err := Parse([]byte(`{
		"domain": "example.com",
		"email": "ops@example.com",
		"accounts": [{"name": "carol", "nodes": ["does-not-exist"]}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), spec, opt); err == nil {
		t.Fatal("want an error for an unknown node selection")
	}
}
