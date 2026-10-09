package node

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/EasySBTeam/EasySB/internal/atomicfile"
	"github.com/EasySBTeam/EasySB/internal/state"
)

// The node store is the only source of what the host serves, and it had no test
// file at all. What follows pins the rules the panel, the TUI and the renderer all
// depend on: a node always carries the parameters its protocol needs, a stored
// document is refused rather than half-read, and a read-modify-write cycle under
// the lock does not lose a concurrent change.

func TestNewFillsTheParametersTheProtocolNeeds(t *testing.T) {
	reality := New(state.ProtoVLESSReality, "reality", 8003, nil)
	for _, key := range []string{ParamRealitySNI, ParamRealityPrivate, ParamRealityPublic, ParamRealityShortID} {
		if reality.Param(key) == "" {
			t.Errorf("a Reality node must carry %s; sing-box refuses an inbound with an empty one", key)
		}
	}
	if reality.Param(ParamRealitySNI) != state.DefaultSNI {
		t.Errorf("Reality SNI = %q, want the default %q", reality.Param(ParamRealitySNI), state.DefaultSNI)
	}

	hy2 := New(state.ProtoHysteria2, "hy2", 8001, nil)
	if hy2.Param(ParamHopRange) != state.DefaultHopRange {
		t.Errorf("Hysteria2 hop range = %q, want %q", hy2.Param(ParamHopRange), state.DefaultHopRange)
	}

	// A protocol that authenticates with the account credential alone needs no
	// parameters, and must not be given any it would then have to justify.
	anytls := New(state.ProtoAnyTLS, "anytls", 8000, nil)
	if len(anytls.Params) != 0 {
		t.Errorf("AnyTLS carries parameters it does not use: %v", anytls.Params)
	}
}

func TestNewDoesNotOverwriteAProvidedParameter(t *testing.T) {
	// EnsureParams fills a gap; it must not replace a value the operator chose. The
	// hop range is the one an operator sets by hand.
	provided := New(state.ProtoHysteria2, "hy2", 8001, map[string]string{ParamHopRange: "3000:4000"})
	if got := provided.Param(ParamHopRange); got != "3000:4000" {
		t.Fatalf("EnsureParams replaced a supplied hop range: got %q", got)
	}
}

func TestCoreNameRoundTrips(t *testing.T) {
	// The core user name is what makes per-node accounting possible, and it is also
	// a QueryStats regex pattern, so its shape is load-bearing.
	name := CoreName("token123", "node456")
	if name != "token123@node456" {
		t.Fatalf("CoreName = %q", name)
	}
	token, id, ok := SplitCoreName(name)
	if !ok || token != "token123" || id != "node456" {
		t.Fatalf("SplitCoreName(%q) = %q, %q, %v", name, token, id, ok)
	}
	// A name with no separator, or one at either end, is not a core name.
	for _, bad := range []string{"", "noseparator", "@node", "token@"} {
		if _, _, ok := SplitCoreName(bad); ok {
			t.Errorf("SplitCoreName(%q) reported success", bad)
		}
	}
}

func TestValidateRefusesWhatTheCoreWould(t *testing.T) {
	store := &Store{}
	cases := []struct {
		name string
		node Node
	}{
		{"empty name", Node{ID: "a", Name: "  ", Protocol: state.ProtoAnyTLS, Port: 8000}},
		{"name too long", Node{ID: "a", Name: strings.Repeat("x", 41), Protocol: state.ProtoAnyTLS, Port: 8000}},
		{"control character in the name", Node{ID: "a", Name: "bad\nname", Protocol: state.ProtoAnyTLS, Port: 8000}},
		{"unknown protocol", Node{ID: "a", Name: "n", Protocol: "no-such-protocol", Port: 8000}},
		{"port zero", Node{ID: "a", Name: "n", Protocol: state.ProtoAnyTLS, Port: 0}},
		{"port above the range", Node{ID: "a", Name: "n", Protocol: state.ProtoAnyTLS, Port: 65536}},
		{"negative port", Node{ID: "a", Name: "n", Protocol: state.ProtoAnyTLS, Port: -1}},
	}
	for _, tc := range cases {
		if err := store.Validate(tc.node, ""); err == nil {
			t.Errorf("%s: Validate accepted the node", tc.name)
		}
	}

	valid := Node{ID: "a", Name: "ok", Protocol: state.ProtoAnyTLS, Port: 8000, Enabled: true}
	if err := store.Validate(valid, ""); err != nil {
		t.Fatalf("Validate refused a valid node: %v", err)
	}
}

func TestValidateRefusesDuplicateNameAndPort(t *testing.T) {
	store := &Store{nodes: []Node{
		{ID: "n1", Name: "first", Protocol: state.ProtoAnyTLS, Port: 8000, Enabled: true},
	}}

	// A second name, same port: two enabled inbounds on one port is a config the
	// core refuses, and one counter name shared by two nodes.
	dup := Node{ID: "n2", Name: "second", Protocol: state.ProtoTUIC, Port: 8000, Enabled: true}
	if err := store.Validate(dup, ""); err == nil {
		t.Error("Validate accepted a second enabled node on a taken port")
	}
	// A duplicate name is refused even when the ports differ.
	sameName := Node{ID: "n3", Name: "first", Protocol: state.ProtoTUIC, Port: 9000, Enabled: true}
	if err := store.Validate(sameName, ""); err == nil {
		t.Error("Validate accepted a duplicate name")
	}
	// Editing the node that owns the name and port is allowed to keep both.
	edited := Node{ID: "n1", Name: "first", Protocol: state.ProtoAnyTLS, Port: 8000, Enabled: true}
	if err := store.Validate(edited, "n1"); err != nil {
		t.Errorf("Validate refused a node keeping its own name and port: %v", err)
	}
	// A disabled node may share a port, because it is not rendered.
	disabled := Node{ID: "n4", Name: "other", Protocol: state.ProtoTUIC, Port: 8000, Enabled: false}
	if err := store.Validate(disabled, ""); err != nil {
		t.Errorf("Validate refused a disabled node sharing a port: %v", err)
	}
}

func TestLoadRefusesADocumentItCannotTrust(t *testing.T) {
	dir := t.TempDir()

	// A duplicate id silently collides on one counter name and one port.
	dupID := filepath.Join(dir, "dup.json")
	if err := os.WriteFile(dupID, []byte(`{"version":1,"nodes":[{"id":"x","name":"a","protocol":"anytls","port":8000},{"id":"x","name":"b","protocol":"tuic","port":8001}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dupID); err == nil {
		t.Error("Load accepted a duplicate node id")
	}

	emptyID := filepath.Join(dir, "empty-id.json")
	if err := os.WriteFile(emptyID, []byte(`{"version":1,"nodes":[{"id":"","name":"a","protocol":"anytls","port":8000}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(emptyID); err == nil {
		t.Error("Load accepted an empty node id")
	}

	unknown := filepath.Join(dir, "unknown.json")
	if err := os.WriteFile(unknown, []byte(`{"version":1,"nodes":[{"id":"x","name":"a","protocol":"wireguard","port":8000}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(unknown); err == nil {
		t.Error("Load accepted an unknown protocol")
	}

	truncated := filepath.Join(dir, "truncated.json")
	if err := os.WriteFile(truncated, []byte(`{"version":1,"nodes":[`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(truncated); err == nil {
		t.Error("Load accepted a truncated document")
	}
}

func TestLoadTreatsAMissingOrEmptyFileAsAnEmptyStore(t *testing.T) {
	dir := t.TempDir()
	// A fresh deployment has no node file, and that is not an error: the panel has
	// to be able to start and show an empty list.
	missing, err := Load(filepath.Join(dir, "absent.json"))
	if err != nil {
		t.Fatalf("Load of a missing file: %v", err)
	}
	if missing.Len() != 0 {
		t.Fatalf("a missing file yielded %d nodes", missing.Len())
	}

	blank := filepath.Join(dir, "blank.json")
	if err := os.WriteFile(blank, []byte("  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	empty, err := Load(blank)
	if err != nil {
		t.Fatalf("Load of an empty file: %v", err)
	}
	if empty.Len() != 0 {
		t.Fatalf("an empty file yielded %d nodes", empty.Len())
	}
}

// TestLoadRecoversFromTheBackupWhenTheStoreIsCorrupt covers the recovery path for the
// node store, which is the only record of what the host serves: the ports, the
// protocols and the Reality keypair are not derivable from anything else.
func TestLoadRecoversFromTheBackupWhenTheStoreIsCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "easysb-nodes.json")
	store, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	reality := New(state.ProtoVLESSReality, "reality", 8003, nil)
	if err := store.Add(reality); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// A second save, so a backup exists.
	if err := store.Update(reality.ID, func(n *Node) error { n.Port = 8103; return nil }); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, err := os.Stat(atomicfile.BackupPath(path)); err != nil {
		t.Fatalf("no backup was kept: %v", err)
	}

	// The store is truncated by something outside the program.
	if err := os.WriteFile(path, []byte(`{"version":1,"nodes":[`), 0o600); err != nil {
		t.Fatal(err)
	}

	recovered, err := Load(path)
	if err != nil {
		t.Fatalf("Load did not recover: %v", err)
	}
	got, ok := recovered.ByID(reality.ID)
	if !ok {
		t.Fatal("the recovered store lost the node")
	}
	// The Reality keypair is the part that cannot be regenerated: a new one would
	// invalidate every client that already imported the node.
	if got.Param(ParamRealityPrivate) == "" {
		t.Fatal("the recovered node lost its Reality private key")
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	var quarantined int
	for _, e := range entries {
		if strings.Contains(e.Name(), ".corrupt-") {
			quarantined++
		}
	}
	if quarantined == 0 {
		t.Fatal("the unusable file was not kept for forensics")
	}
}

func TestSaveIsAtomicAndRootOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "easysb-nodes.json")
	store := &Store{path: path}
	if err := store.Add(New(state.ProtoAnyTLS, "anytls", 8000, nil)); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if runtime.GOOS == "windows" {
		// Windows has no Unix permission bits; the mode assertion is what Linux CI runs.
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("node store mode = %04o, want 0600: the parameters include a Reality private key", got)
	}

	// The write goes through a temporary file and a rename, so the directory holds
	// the document and nothing else once the write returns.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "easysb-nodes.json" {
			t.Fatalf("Save left %q behind", e.Name())
		}
	}
}

func TestUpdateAndRemoveReportAnUnknownID(t *testing.T) {
	store := &Store{path: filepath.Join(t.TempDir(), "nodes.json")}
	if err := store.Update("missing", func(*Node) error { return nil }); err == nil {
		t.Error("Update accepted an unknown id")
	}
	if err := store.Remove("missing"); err == nil {
		t.Error("Remove accepted an unknown id")
	}
}

func TestUpdateDoesNotCommitWhenTheEditorFails(t *testing.T) {
	store := &Store{path: filepath.Join(t.TempDir(), "nodes.json")}
	if err := store.Add(New(state.ProtoAnyTLS, "keep", 8000, nil)); err != nil {
		t.Fatal(err)
	}
	id := store.Nodes()[0].ID

	sentinel := errors.New("the editor refused")
	if err := store.Update(id, func(n *Node) error {
		n.Name = "changed"
		n.Port = 9999
		return sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatalf("Update = %v, want the editor's error", err)
	}
	after, _ := store.ByID(id)
	if after.Name != "keep" || after.Port != 8000 {
		t.Fatalf("a failed editor committed its change: %+v", after)
	}
}

// TestLockedKeepsConcurrentWriters is the node-store counterpart of the account
// store's test: the panel, the TUI and the migration are separate processes all
// adding nodes, and the lock is what keeps them from writing a stale list back
// over each other.
func TestLockedKeepsConcurrentWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "easysb-nodes.json")

	const writers = 8
	var wg sync.WaitGroup
	errs := make([]error, writers)
	start := make(chan struct{})
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			store, lock, err := Locked(path)
			if err != nil {
				errs[i] = err
				return
			}
			defer lock.Unlock()
			// A distinct name and port each: names must be unique and two enabled
			// nodes may not share a port, so either collision would make the test
			// pass on the wrong error.
			errs[i] = store.Add(New(state.ProtoAnyTLS, fmt.Sprintf("node-%d", i), 8000+i, nil))
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d: %v", i, err)
		}
	}

	final, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if final.Len() != writers {
		t.Fatalf("store holds %d nodes after %d concurrent writers: a change was lost", final.Len(), writers)
	}
}

func TestCheckSubPort(t *testing.T) {
	node := Node{Enabled: true, Port: 8443}
	if err := CheckSubPort(node, 8443); err == nil {
		t.Error("CheckSubPort accepted a node on the subscription service port")
	}
	if err := CheckSubPort(node, 9000); err != nil {
		t.Errorf("CheckSubPort refused an unrelated port: %v", err)
	}
	// A disabled node is never rendered, so it cannot collide with the listener.
	node.Enabled = false
	if err := CheckSubPort(node, 8443); err != nil {
		t.Errorf("CheckSubPort refused a disabled node: %v", err)
	}
}
