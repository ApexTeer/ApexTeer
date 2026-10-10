// Package firewall's ledger records the port-hopping redirect rules this program has
// installed.
//
// It exists because a rule cannot be found from the node store once its node is gone:
// deleting a Hysteria2 node removed its entry, and the redirect that node needed was
// then unowned by anything on the host. A rule that is written down when it is added
// can always be removed later, whatever happened to the node that asked for it.
//
// Only rules EasySB itself added are ever recorded, so anything another service put in
// PREROUTING is invisible here and can never be removed through this file.
package firewall

import (
	"encoding/json"
	"os"
	"sort"
	"time"

	"github.com/EasySBTeam/EasySB/internal/atomicfile"
	"github.com/EasySBTeam/EasySB/internal/sysinfo"
)

// ledgerPath is a variable so a test can point it at a temporary file. Writing the
// real one would need /etc, which a build host does not have.
var ledgerPath = sysinfo.FirewallLedger

// Rule is one port-hopping redirect this program installed.
//
// The fields are exactly the ones needed to delete it again: a NAT REDIRECT on a UDP
// destination range aimed at one local port. They are also the identity used to tell
// whether a desired rule is already recorded, so recording twice is a no-op.
type Rule struct {
	Backend string `json:"backend"`
	Start   string `json:"start"`
	End     string `json:"end"`
	Target  string `json:"target"`
	AddedAt string `json:"addedAt,omitempty"`
}

// key is the identity of a rule: backend plus the exact match specification. Two rules
// with the same key are the same rule as far as add and delete are concerned.
func (r Rule) key() string {
	return r.Backend + "|" + r.Start + "|" + r.End + "|" + r.Target
}

// fileFormat is the on-disk document.
type fileFormat struct {
	Version int    `json:"version"`
	Rules   []Rule `json:"rules"`
}

// ledgerVersion is bumped only for an incompatible layout change.
const ledgerVersion = 1

// LoadLedger reads the recorded rules. A missing or unreadable file yields an empty
// ledger rather than an error: the worst case is that an old rule is not cleaned up
// automatically, and refusing to run the firewall at all would be far worse.
func LoadLedger(path string) Ledger {
	l := Ledger{}
	data, err := os.ReadFile(path)
	if err != nil {
		return l
	}
	var doc fileFormat
	if err := json.Unmarshal(data, &doc); err != nil {
		return l
	}
	for _, r := range doc.Rules {
		if r.Start == "" || r.End == "" || r.Target == "" {
			continue
		}
		l.rules = append(l.rules, r)
	}
	return l
}

// Ledger is the in-memory set of recorded rules.
type Ledger struct {
	rules []Rule
}

// Rules returns the recorded rules, sorted for a stable report.
func (l Ledger) Rules() []Rule {
	out := append([]Rule(nil), l.rules...)
	sort.Slice(out, func(i, j int) bool { return out[i].key() < out[j].key() })
	return out
}

// Has reports whether the rule is already recorded.
func (l Ledger) Has(r Rule) bool {
	for _, x := range l.rules {
		if x.key() == r.key() {
			return true
		}
	}
	return false
}

// Add records a rule. Recording an already-present rule is a no-op, which is what makes
// running Apply twice leave the file unchanged.
func (l *Ledger) Add(r Rule) {
	if l.Has(r) {
		return
	}
	if r.AddedAt == "" {
		r.AddedAt = time.Now().UTC().Format(time.RFC3339)
	}
	l.rules = append(l.rules, r)
}

// Remove drops a rule from the record.
func (l *Ledger) Remove(r Rule) {
	kept := l.rules[:0]
	for _, x := range l.rules {
		if x.key() != r.key() {
			kept = append(kept, x)
		}
	}
	l.rules = kept
}

// Save writes the ledger, creating the parent directory. It goes through atomicfile for
// the same reason every other state file does: a truncated ledger is a set of rules
// that can no longer be cleaned up.
func (l Ledger) Save(path string) error {
	doc := fileFormat{Version: ledgerVersion, Rules: l.Rules()}
	if doc.Rules == nil {
		doc.Rules = []Rule{}
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(path, append(data, '\n'), 0o600)
}
