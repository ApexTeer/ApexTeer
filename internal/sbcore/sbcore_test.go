package sbcore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// minimalConfig is the smallest document the carried core accepts: the schema
// requires an outbound, and nothing else.
const minimalConfig = `{
  "log": { "level": "warn" },
  "outbounds": [ { "type": "direct", "tag": "direct" } ]
}`

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// TestEngineContextKeepsTheCallerCancellation is the point of taking a parent: the
// engine context used to be built on context.Background, so cancelling a deploy
// cancelled nothing and the slowest step of the deploy could not be given up on.
func TestEngineContextKeepsTheCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	engine := engineContext(ctx)

	if engine.Done() == nil {
		t.Fatal("the engine context has no Done channel, so it can never be cancelled")
	}
	cancel()
	select {
	case <-engine.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("cancelling the caller did not reach the engine context")
	}
}

func TestParseRejectsAMissingFile(t *testing.T) {
	if _, err := Parse(filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Fatal("Parse accepted a file that does not exist")
	}
}

func TestParseRejectsAMalformedDocument(t *testing.T) {
	if _, err := Parse(writeConfig(t, "{ this is not json")); err == nil {
		t.Fatal("Parse accepted a malformed document")
	}
}

// TestCheckAcceptsWhatTheNodeWouldRun is the acceptance half of the pair: the
// document the deploy path writes has to be one this engine builds, or the node
// never starts and the panel only hears about it from the service journal.
func TestCheckAcceptsWhatTheNodeWouldRun(t *testing.T) {
	if err := Check(context.Background(), writeConfig(t, minimalConfig)); err != nil {
		t.Fatalf("Check refused a minimal configuration: %v", err)
	}
}

func TestCheckRejectsWhatTheNodeWouldRefuse(t *testing.T) {
	// A protocol whose tag this build does not carry: the core refuses the whole
	// document rather than the one inbound, which is exactly why the deploy path
	// validates before restarting the node.
	if err := Check(context.Background(), writeConfig(t, `{
  "outbounds": [ { "type": "direct", "tag": "direct" } ],
  "inbounds": [ { "type": "no-such-inbound", "tag": "nope" } ]
}`)); err == nil {
		t.Fatal("Check accepted an inbound the core cannot build")
	}
}

func TestVersionIsNeverEmpty(t *testing.T) {
	if got := Version(); got == "" {
		t.Fatal("Version is empty: the dashboard and `core version` would both report nothing")
	}
}
