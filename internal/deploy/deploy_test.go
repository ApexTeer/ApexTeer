package deploy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/MinimaxFlora/EasySB/internal/cert"
	"github.com/MinimaxFlora/EasySB/internal/sbcore"
	"github.com/MinimaxFlora/EasySB/internal/state"
	"github.com/MinimaxFlora/EasySB/internal/user"
)

// writeInto points the deployment's config paths at a temporary directory for one test.
func writeInto(t *testing.T, dir string) {
	t.Helper()
	originalDir, originalPath := configDir, configPath
	t.Cleanup(func() { configDir, configPath = originalDir, originalPath })
	configDir = dir
	configPath = filepath.Join(dir, "config.json")
}

// testAccount returns one account that selected every protocol, with credentials.
func testAccount(t *testing.T) user.User {
	t.Helper()
	account := user.New("alice", state.Keys, time.Now())
	account.EnsureCredentials()
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

// taggedProtocols are the protocols whose code sits behind a build tag: Hysteria2 and
// TUIC need with_quic, and the Reality inbound needs with_utls. The release tag set
// (release/TAGS) carries both, so their documents are checked in
// deploy_release_test.go; the tests here run in every build.
func taggedProtocols() map[string]bool {
	return map[string]bool{
		state.ProtoAnyTLS:     true,
		state.ProtoVMessWSTLS: true,
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
	cfg.Enabled = taggedProtocols()

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
	cfg.Enabled = taggedProtocols()
	document, err := ServerConfig(cfg, nil)
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
	cfg.Enabled = map[string]bool{state.ProtoVLESSReality: true}
	cfg.RealityPriv, cfg.RealityPub = "", ""
	if _, err := ServerConfig(cfg, nil); !errors.Is(err, ErrNoRealityKey) {
		t.Fatalf("ServerConfig without a keypair = %v, want %v", err, ErrNoRealityKey)
	}
}

// TestWriteServerConfigKeepsCredentialsRootOnly covers the one file the panel writes
// that carries every account's credentials: the uuid and password of each enabled
// protocol. The account store is 0600 and this document is the same secret in another
// shape, so a group or world readable mode here hands every local user of the host a set
// of working credentials.
func TestWriteServerConfigKeepsCredentialsRootOnly(t *testing.T) {
	const domain = "example.com"
	installTestCertificate(t, t.TempDir(), domain)

	dir := t.TempDir()
	writeInto(t, dir)

	cfg := state.Default()
	cfg.Domain = domain
	cfg.Enabled = taggedProtocols()

	document, err := WriteServerConfig(cfg, []user.User{testAccount(t)})
	if err != nil {
		t.Fatalf("WriteServerConfig: %v", err)
	}
	path := filepath.Join(dir, "config.json")
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the written config: %v", err)
	}
	if string(written) != string(document) {
		t.Fatal("the file on disk is not the document that was returned")
	}

	// WriteFile leaves the mode of a file that already exists alone, so a config.json
	// that an earlier version left at 0644 has to be brought back rather than skipped.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("widen the mode: %v", err)
	}
	if _, err := WriteServerConfig(cfg, []user.User{testAccount(t)}); err != nil {
		t.Fatalf("WriteServerConfig over an existing file: %v", err)
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

// TestApplyLeavesAnUndeployedNodeAlone pins the early return: a host that has accounts
// but no node yet must not be made to fail on a configuration there is no certificate
// for.
func TestApplyLeavesAnUndeployedNodeAlone(t *testing.T) {
	writeInto(t, t.TempDir())

	cfg := state.Default()
	cfg.NodeDeployed = false

	if err := Apply(context.Background(), cfg, []user.User{testAccount(t)}); err != nil {
		t.Fatalf("Apply on a node that was never deployed = %v, want nil", err)
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
	cfg.Enabled = taggedProtocols()
	cfg.NodeDeployed = true

	err := Apply(context.Background(), cfg, []user.User{testAccount(t)})
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("Apply = %v, want an error that wraps %v", err, ErrRejected)
	}
	if !strings.Contains(err.Error(), "no-such-inbound") {
		t.Fatalf("Apply dropped the core's own message: %v", err)
	}
}
