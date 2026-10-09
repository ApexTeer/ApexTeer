package provision

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/EasySBTeam/EasySB/internal/cert"
	"github.com/EasySBTeam/EasySB/internal/state"
	"github.com/EasySBTeam/EasySB/internal/user"
)

// TestExampleManifestParses keeps the shipped example honest: a field renamed
// in the struct without updating docs/provision.example.json fails here.
func TestExampleManifestParses(t *testing.T) {
	data, err := os.ReadFile("../../docs/provision.example.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(data); err != nil {
		t.Fatalf("shipped example does not parse: %v", err)
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
