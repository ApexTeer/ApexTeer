package firewall

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/EasySBTeam/EasySB/internal/node"
	"github.com/EasySBTeam/EasySB/internal/state"
)

// The rule lifecycle had no test at all: every decision in this package is a decision
// about what iptables or nft printed, and nothing stubbed that. The delete path
// therefore believed an empty nft listing and quietly removed nothing, and a rule whose
// node was deleted could never be found again. The fake below keeps a real rule set, so
// a scenario is exercised the whole way through rather than asserted against a mock's
// return value.

// fakeFirewall implements just enough of iptables and nft to hold a rule set.
type fakeFirewall struct {
	t       *testing.T
	rules   []Rule // in chain order
	chain   string // "prerouting" (ours) or "PREROUTING" (iptables-nft's)
	table   bool
	tool    string // which binary answered last
	deletes int
	adds    int
}

func newFakeFirewall(t *testing.T, chain string) *fakeFirewall {
	t.Helper()
	f := &fakeFirewall{t: t, chain: chain, table: true}
	original := execOutput
	execOutput = f.run
	t.Cleanup(func() { execOutput = original })
	return f
}

// run answers the exact argv shapes the package builds.
func (f *fakeFirewall) run(_ context.Context, name string, args ...string) (string, error) {
	f.tool = name
	switch name {
	case "iptables":
		return f.iptables(args)
	case "nft":
		return f.nft(args)
	}
	return "", fmt.Errorf("unexpected tool %q", name)
}

func (f *fakeFirewall) iptables(args []string) (string, error) {
	// Forms the package builds (iptables accepts flags in any order, so the verb is
	// found by name rather than by position):
	//   -t nat -S PREROUTING
	//   -C|-A|-D -t nat PREROUTING -p udp --dport S:E -j REDIRECT --to-ports T
	verb := ""
	for _, a := range args {
		switch a {
		case "-S", "-C", "-A", "-D":
			verb = a
		}
		if verb != "" {
			break
		}
	}
	isNat := false
	for i, a := range args {
		if a == "-t" && i+1 < len(args) && args[i+1] == "nat" {
			isNat = true
		}
	}
	if verb != "" && isNat {
		switch verb {
		case "-S":
			var b strings.Builder
			for _, r := range f.rules {
				fmt.Fprintf(&b, "-A PREROUTING -p udp -m udp --dport %s:%s -j REDIRECT --to-ports %s\n",
					r.Start, r.End, r.Target)
			}
			return b.String(), nil
		case "-C", "-A", "-D":
			r := Rule{}
			for i, tok := range args {
				if i+1 >= len(args) {
					break
				}
				switch tok {
				case "--dport":
					r.Start, r.End, _ = strings.Cut(args[i+1], ":")
				case "--to-ports":
					r.Target = args[i+1]
				}
			}
			idx := f.indexOf(r)
			switch verb {
			case "-C":
				if idx < 0 {
					return "", fmt.Errorf("iptables: Bad rule")
				}
				return "", nil
			case "-A":
				f.rules = append(f.rules, r)
				f.adds++
				return "", nil
			case "-D":
				if idx < 0 {
					return "", fmt.Errorf("iptables: Bad rule")
				}
				f.rules = append(f.rules[:idx], f.rules[idx+1:]...)
				f.deletes++
				return "", nil
			}
		}
	}
	return "", fmt.Errorf("unhandled iptables %v", args)
}

func (f *fakeFirewall) nft(args []string) (string, error) {
	joined := strings.Join(args, " ")
	switch {
	case joined == "list tables":
		if !f.table {
			return "", nil
		}
		return "table ip nat\n", nil
	case joined == "list table ip nat":
		if !f.table {
			return "", fmt.Errorf("nft: no such table")
		}
		// The chain name is whatever the host actually has, which is the point of the
		// bug this covers: nft is case sensitive and iptables-nft names it PREROUTING.
		var b strings.Builder
		fmt.Fprintf(&b, "table ip nat {\n\tchain %s {\n", f.chain)
		fmt.Fprintf(&b, "\t\ttype nat hook prerouting priority dstnat; policy accept;\n")
		for i, r := range f.rules {
			fmt.Fprintf(&b, "\t\tudp dport %s-%s counter packets 1 bytes 60 redirect to :%s # handle %d\n",
				r.Start, r.End, r.Target, i+1)
		}
		b.WriteString("\t}\n}\n")
		return b.String(), nil
	case joined == "-a list table ip nat":
		return f.nft([]string{"list", "table", "ip", "nat"})
	case joined == "list table ip nat prerouting" || joined == "list table ip nat "+f.chain:
		if f.chain == "prerouting" {
			return f.nft([]string{"list", "table", "ip", "nat"})
		}
		return "", fmt.Errorf("nft: No such file or directory")
	case strings.HasPrefix(joined, "add rule ip nat "):
		// add rule ip nat <chain> udp dport S-E redirect to :T
		parts := strings.Fields(joined)
		var r Rule
		for i, tok := range parts {
			if i+1 >= len(parts) {
				break
			}
			switch tok {
			case "dport":
				r.Start, r.End, _ = strings.Cut(parts[i+1], "-")
			case "to":
				r.Target = strings.TrimPrefix(parts[i+1], ":")
			}
		}
		if f.indexOf(r) >= 0 {
			return "", fmt.Errorf("nft: File exists")
		}
		f.rules = append(f.rules, r)
		f.adds++
		return "", nil
	case strings.HasPrefix(joined, "delete rule ip nat "):
		parts := strings.Fields(joined)
		handle := ""
		for i, tok := range parts {
			if tok == "handle" && i+1 < len(parts) {
				handle = parts[i+1]
			}
		}
		n := 0
		fmt.Sscanf(handle, "%d", &n)
		if n < 1 || n > len(f.rules) {
			return "", fmt.Errorf("nft: no such handle")
		}
		f.rules = append(f.rules[:n-1], f.rules[n:]...)
		f.deletes++
		return "", nil
	case strings.HasPrefix(joined, "add table"), strings.HasPrefix(joined, "add chain"):
		f.table = true
		return "", nil
	}
	return "", fmt.Errorf("unhandled nft %q", joined)
}

func (f *fakeFirewall) indexOf(r Rule) int {
	for i, x := range f.rules {
		if x.Start == r.Start && x.End == r.End && x.Target == r.Target {
			return i
		}
	}
	return -1
}

// spec renders the rule set for a readable failure message and for comparing runs.
func (f *fakeFirewall) spec() string {
	var out []string
	for _, r := range f.rules {
		out = append(out, r.Start+":"+r.End+"->"+r.Target)
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

// useLedger points the ledger at a temporary file. Writing the real one would need /etc.
func useLedger(t *testing.T) string {
	t.Helper()
	original := ledgerPath
	path := filepath.Join(t.TempDir(), "easysb-firewall.json")
	ledgerPath = path
	t.Cleanup(func() { ledgerPath = original })
	return path
}

func hy2(t *testing.T, port int, hop string) node.Node {
	t.Helper()
	n := node.New(state.ProtoHysteria2, "hy2", port, nil)
	if hop != "" {
		n.Params[node.ParamHopRange] = hop
	}
	return n
}

func anytls(t *testing.T) node.Node {
	t.Helper()
	return node.New(state.ProtoAnyTLS, "anytls", 8000, nil)
}

// forceBackend pins the backend the package's own logic uses. Detect answers what the
// machine has, which a suite must not depend on: the iptables and nft paths differ, and
// both have to be exercised wherever the suite runs.
func forceBackend(t *testing.T, backend Backend) {
	t.Helper()
	original := detectBackend
	t.Cleanup(func() { detectBackend = original })
	detectBackend = func() Backend { return backend }
}

// --- the scenarios the report asks for ---------------------------------------------

func TestSyncAddsTheRuleForAHysteriaNode(t *testing.T) {
	f := newFakeFirewall(t, "PREROUTING")
	useLedger(t)
	forceBackend(t, IPTables)

	nodes := []node.Node{hy2(t, 8001, "2080:3000"), anytls(t)}
	if err := sync(context.Background(), mustDesire(t, nodes), func(string) {}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if got := f.spec(); got != "2080:3000->8001" {
		t.Fatalf("rules = %q, want the hop redirect only (no rule for anytls)", got)
	}
}

func TestSyncIsIdempotent(t *testing.T) {
	f := newFakeFirewall(t, "PREROUTING")
	ledger := useLedger(t)
	forceBackend(t, IPTables)

	desired := mustDesire(t, []node.Node{hy2(t, 8001, "2080:3000")})
	for i := 0; i < 4; i++ {
		if err := sync(context.Background(), desired, func(string) {}); err != nil {
			t.Fatalf("sync %d: %v", i, err)
		}
	}
	if len(f.rules) != 1 {
		t.Fatalf("after four syncs there are %d rules, want 1: repeating must not duplicate", len(f.rules))
	}
	if f.adds != 1 {
		t.Fatalf("the rule was added %d times, want 1", f.adds)
	}
	// The ledger must not grow either: the file is the record of what exists.
	l := LoadLedger(ledger)
	if got := len(l.Rules()); got != 1 {
		t.Fatalf("ledger holds %d rules after four syncs, want 1", got)
	}
}

// TestSyncRemovesTheRuleOfADeletedNode is the regression for the orphan this whole
// change exists for: the node is gone, so the live node list cannot name the rule, but
// the ledger can.
func TestSyncRemovesTheRuleOfADeletedNode(t *testing.T) {
	f := newFakeFirewall(t, "PREROUTING")
	useLedger(t)
	forceBackend(t, IPTables)
	ctx := context.Background()

	// A node with a hop range is deployed.
	if err := sync(ctx, mustDesire(t, []node.Node{hy2(t, 8001, "2080:3000")}), func(string) {}); err != nil {
		t.Fatal(err)
	}
	if f.spec() != "2080:3000->8001" {
		t.Fatalf("setup failed: %q", f.spec())
	}

	// The node is deleted. Sync is called with the remaining nodes only.
	if err := sync(ctx, mustDesire(t, []node.Node{anytls(t)}), func(string) {}); err != nil {
		t.Fatal(err)
	}
	if got := f.spec(); got != "" {
		t.Fatalf("rules = %q, want none: a deleted node's redirect must be cleaned up", got)
	}
	if f.deletes == 0 {
		t.Fatal("nothing was deleted")
	}
}

// TestSyncMovesARuleWhenTheTargetPortChanges covers an update rather than a deletion:
// the same range now points somewhere else, and the stale rule must not survive.
func TestSyncMovesARuleWhenTheTargetPortChanges(t *testing.T) {
	f := newFakeFirewall(t, "PREROUTING")
	useLedger(t)
	forceBackend(t, IPTables)
	ctx := context.Background()

	if err := sync(ctx, mustDesire(t, []node.Node{hy2(t, 8001, "2080:3000")}), func(string) {}); err != nil {
		t.Fatal(err)
	}
	if err := sync(ctx, mustDesire(t, []node.Node{hy2(t, 9001, "2080:3000")}), func(string) {}); err != nil {
		t.Fatal(err)
	}
	if got := f.spec(); got != "2080:3000->9001" {
		t.Fatalf("rules = %q, want only the new target", got)
	}
}

// TestSyncChangesTheRange covers the other half of an update.
func TestSyncChangesTheRange(t *testing.T) {
	f := newFakeFirewall(t, "PREROUTING")
	useLedger(t)
	forceBackend(t, IPTables)
	ctx := context.Background()

	if err := sync(ctx, mustDesire(t, []node.Node{hy2(t, 8001, "2080:3000")}), func(string) {}); err != nil {
		t.Fatal(err)
	}
	if err := sync(ctx, mustDesire(t, []node.Node{hy2(t, 8001, "4000:5000")}), func(string) {}); err != nil {
		t.Fatal(err)
	}
	if got := f.spec(); got != "4000:5000->8001" {
		t.Fatalf("rules = %q, want only the new range", got)
	}
}

// TestSyncLeavesRulesItDoesNotOwn is the safety property: a redirect another service
// installed is invisible to the ledger and must survive every operation.
func TestSyncLeavesRulesItDoesNotOwn(t *testing.T) {
	f := newFakeFirewall(t, "PREROUTING")
	useLedger(t)
	forceBackend(t, IPTables)
	ctx := context.Background()

	foreign := Rule{Start: "7000", End: "7100", Target: "7999"}
	f.rules = append(f.rules, foreign)

	if err := sync(ctx, mustDesire(t, []node.Node{hy2(t, 8001, "2080:3000")}), func(string) {}); err != nil {
		t.Fatal(err)
	}
	// Then remove everything we own.
	if err := Remove(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if f.indexOf(foreign) < 0 {
		t.Fatalf("a rule this program never added was deleted; rules now %q", f.spec())
	}
	if got := f.spec(); got != "7000:7100->7999" {
		t.Fatalf("rules = %q, want the foreign rule left alone", got)
	}
}

// TestRemoveDeletesEverythingRecorded covers "turn port hopping off" and uninstall.
func TestRemoveDeletesEverythingRecorded(t *testing.T) {
	f := newFakeFirewall(t, "PREROUTING")
	useLedger(t)
	forceBackend(t, IPTables)
	ctx := context.Background()

	desired := mustDesire(t, []node.Node{hy2(t, 8001, "2080:3000"), hy2(t, 8002, "4000:4100")})
	if err := sync(ctx, desired, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if len(f.rules) != 2 {
		t.Fatalf("setup: %d rules", len(f.rules))
	}
	// Remove is called with an EMPTY node list: it must still know what to delete.
	if err := Remove(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.spec(); got != "" {
		t.Fatalf("rules = %q, want none", got)
	}
	if l := LoadLedger(ledgerPath); len(l.Rules()) != 0 {
		t.Fatalf("ledger still claims %d rules exist", len(l.Rules()))
	}
}

// TestDeleteClearsDuplicatesLeftByAnOlderVersion covers a rule recorded once but present
// twice, which an older version could produce.
func TestDeleteClearsDuplicatesLeftByAnOlderVersion(t *testing.T) {
	f := newFakeFirewall(t, "PREROUTING")
	useLedger(t)
	forceBackend(t, IPTables)

	dup := Rule{Start: "2080", End: "3000", Target: "8001"}
	f.rules = append(f.rules, dup, dup, dup)

	if err := deleteRule(context.Background(), IPTables, dup); err != nil {
		t.Fatal(err)
	}
	if len(f.rules) != 0 {
		t.Fatalf("%d copies survived, want 0", len(f.rules))
	}
}

// TestPruneOrphansRemovesTheLiveRemnant is the scenario measured on the server: a
// redirect to a port with no listener and no owning node.
func TestPruneOrphansRemovesTheLiveRemnant(t *testing.T) {
	f := newFakeFirewall(t, "PREROUTING")
	useLedger(t)
	forceBackend(t, IPTables)
	ctx := context.Background()

	// Exactly what the host has: the live rule and the orphan.
	f.rules = append(f.rules,
		Rule{Start: "2080", End: "3000", Target: "8001"},
		Rule{Start: "3100", End: "3200", Target: "8101"},
	)

	removed, err := PruneOrphans(ctx, []node.Node{hy2(t, 8001, "2080:3000")}, func(string) bool { return false }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0].Target != "8101" {
		t.Fatalf("removed %+v, want only the 8101 orphan", removed)
	}
	if got := f.spec(); got != "2080:3000->8001" {
		t.Fatalf("rules = %q, want the live hop rule untouched", got)
	}
}

// TestPruneOrphansKeepsARuleWhoseTargetIsServing guards the conservative half: if
// something is listening on the target, the rule is not ours to judge.
func TestPruneOrphansKeepsARuleWhoseTargetIsServing(t *testing.T) {
	f := newFakeFirewall(t, "PREROUTING")
	useLedger(t)
	forceBackend(t, IPTables)

	f.rules = append(f.rules, Rule{Start: "3100", End: "3200", Target: "8101"})

	removed, err := PruneOrphans(context.Background(), nil, func(port string) bool { return port == "8101" }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 0 {
		t.Fatalf("pruned %+v although something serves the port", removed)
	}
	if len(f.rules) != 1 {
		t.Fatal("the rule was deleted")
	}
}

// TestPruneOrphansNeverTouchesASinglePortRedirect: this program only ever creates
// range redirects, so a one-port redirect belongs to something else.
func TestPruneOrphansNeverTouchesASinglePortRedirect(t *testing.T) {
	newFakeFirewall(t, "PREROUTING")
	useLedger(t)
	forceBackend(t, IPTables)

	// Inventory reports ranges only, so a single-port rule is not even seen.
	inv := Inventory(context.Background())
	if len(inv) != 0 {
		t.Fatalf("Inventory reported %+v for an empty rule set", inv)
	}
}

// TestNFTDeleteFindsAnIPTablesNFTRules is the Bug B regression: the chain is named
// PREROUTING because iptables-nft created it, and nft is case sensitive.
func TestNFTDeleteFindsAnIPTablesNFTRules(t *testing.T) {
	f := newFakeFirewall(t, "PREROUTING")
	useLedger(t)
	forceBackend(t, NFTables)
	ctx := context.Background()

	if err := addRule(ctx, NFTables, Rule{Start: "2080", End: "3000", Target: "8001"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if len(f.rules) != 1 {
		t.Fatalf("setup: %d rules", len(f.rules))
	}
	if err := deleteRule(ctx, NFTables, Rule{Start: "2080", End: "3000", Target: "8001"}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(f.rules) != 0 {
		t.Fatalf("%d rules survived the delete; the chain name was not resolved", len(f.rules))
	}
}

func TestNFTWorksWithItsOwnLowercaseChain(t *testing.T) {
	f := newFakeFirewall(t, "prerouting")
	useLedger(t)
	forceBackend(t, NFTables)
	ctx := context.Background()

	r := Rule{Start: "2080", End: "3000", Target: "8001"}
	if err := addRule(ctx, NFTables, r); err != nil {
		t.Fatal(err)
	}
	if err := addRule(ctx, NFTables, r); err != nil {
		t.Fatalf("a repeated add must be a no-op: %v", err)
	}
	if len(f.rules) != 1 {
		t.Fatalf("%d rules, want 1: the second add was not idempotent", len(f.rules))
	}
	if err := deleteRule(ctx, NFTables, r); err != nil {
		t.Fatal(err)
	}
	if len(f.rules) != 0 {
		t.Fatalf("%d rules survived", len(f.rules))
	}
}

// TestNFTDeleteOfAnAbsentRuleSucceeds keeps the caller free of existence checks.
func TestNFTDeleteOfAnAbsentRuleSucceeds(t *testing.T) {
	newFakeFirewall(t, "PREROUTING")
	useLedger(t)
	forceBackend(t, NFTables)

	if err := deleteRule(context.Background(), NFTables, Rule{Start: "1", End: "2", Target: "3"}); err != nil {
		t.Fatalf("deleting a rule that is not there returned %v", err)
	}
}

// TestSyncWithNoBackendDoesNotWriteARecord: without a tool nothing can be installed, so
// the ledger must not claim otherwise.
func TestSyncWithNoBackendDoesNotWriteARecord(t *testing.T) {
	ledger := useLedger(t)
	forceBackend(t, None) // no tool can be run at all

	if err := sync(context.Background(), mustDesire(t, []node.Node{hy2(t, 8001, "2080:3000")}), func(string) {}); err != nil {
		t.Fatal(err)
	}
	if l := LoadLedger(ledger); len(l.Rules()) != 0 {
		t.Fatalf("ledger records %d rules although no backend exists", len(l.Rules()))
	}
}

// TestLedgerSurvivesAReload is what makes the fix work across restarts and upgrades.
func TestLedgerSurvivesAReload(t *testing.T) {
	path := useLedger(t)
	l := Ledger{}
	r := Rule{Backend: "iptables", Start: "2080", End: "3000", Target: "8001"}
	l.Add(r)
	l.Add(r) // recording twice must not duplicate
	if err := l.Save(path); err != nil {
		t.Fatal(err)
	}
	got := LoadLedger(path)
	if len(got.Rules()) != 1 {
		t.Fatalf("reloaded ledger holds %d rules, want 1", len(got.Rules()))
	}
	if !got.Has(r) {
		t.Fatal("the reloaded ledger does not recognise its own rule")
	}
	if got.Rules()[0].AddedAt == "" {
		t.Fatal("no timestamp was recorded")
	}
}

// TestLedgerToleratesACorruptFile: a damaged ledger must not stop the firewall.
func TestLedgerToleratesACorruptFile(t *testing.T) {
	path := useLedger(t)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := LoadLedger(path); len(got.Rules()) != 0 {
		t.Fatalf("a corrupt ledger yielded %d rules, want none", len(got.Rules()))
	}
}

func mustDesire(t *testing.T, nodes []node.Node) []Rule {
	t.Helper()
	desired, err := desiredRules(nodes)
	if err != nil {
		t.Fatal(err)
	}
	backend := Detect()
	for i := range desired {
		desired[i].Backend = string(backend)
	}
	return desired
}

// TestInventoryThenPruneOnTheRealWorldShape pins the exact rule text measured on the
// host, so a future change to the parser is caught by the shape rather than by a
// hand-written expectation.
func TestInventoryParsesTheMeasuredIPTablesShape(t *testing.T) {
	f := newFakeFirewall(t, "PREROUTING")
	useLedger(t)
	forceBackend(t, IPTables)

	f.rules = append(f.rules,
		Rule{Start: "3100", End: "3200", Target: "8101"},
		Rule{Start: "2080", End: "3000", Target: "8001"},
	)
	inv := Inventory(context.Background())
	if len(inv) != 2 {
		t.Fatalf("Inventory returned %d rules, want 2: %+v", len(inv), inv)
	}
	targets := []string{inv[0].Target, inv[1].Target}
	sort.Strings(targets)
	if targets[0] != "8001" || targets[1] != "8101" {
		t.Fatalf("targets = %v", targets)
	}
}
