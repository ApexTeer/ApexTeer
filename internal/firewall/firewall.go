// Package firewall configures the Hysteria2 port-hopping redirect and opens the
// protocol ports, supporting both iptables and nftables like the legacy shell.
package firewall

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/EasySBTeam/EasySB/internal/atomicfile"
	"github.com/EasySBTeam/EasySB/internal/node"
	"github.com/EasySBTeam/EasySB/internal/service"
	"github.com/EasySBTeam/EasySB/internal/state"
)

// UnitName is the boot service that reapplies the NAT rules.
const UnitName = "easysb-firewall"

const systemdUnitPath = "/etc/systemd/system/easysb-firewall.service"

// Backend identifies the available NAT tooling.
type Backend string

// The NAT backends this host may have. None means neither tool is installed, which the
// panel reports rather than treating as a failure.
const (
	IPTables Backend = "iptables"
	NFTables Backend = "nft"
	None     Backend = "none"
)

// Detect reports which NAT backend is installed, preferring iptables.
func Detect() Backend {
	if _, err := exec.LookPath("iptables"); err == nil {
		return IPTables
	}
	if _, err := exec.LookPath("nft"); err == nil {
		return NFTables
	}
	return None
}

// detectBackend is what this package's own logic consults. It is a variable so a test
// can pin the backend: the rule lifecycle differs per backend, and a test must be able
// to say which one it is exercising rather than depending on what the machine running
// the suite happens to have installed. Detect remains the public answer.
var detectBackend = Detect

// Apply makes the host's port-hopping redirects match nodes and opens the port of
// every enabled node.
//
// It is the entry point for a first deploy and for the panel's enable action. The
// rules it installs are recorded in the ledger, and any rule the ledger holds that is
// no longer wanted is removed, so calling it repeatedly converges on the same rule set
// instead of accumulating duplicates.
func Apply(ctx context.Context, cfg state.Config, nodes []node.Node, log func(string)) error {
	desired, err := desiredRules(nodes)
	if err != nil {
		return err
	}
	if err := sync(ctx, desired, log); err != nil {
		return err
	}
	OpenPorts(ctx, cfg, nodes, log)
	return nil
}

// Sync reconciles the host's redirect rules with what the nodes ask for and opens the
// enabled ports. It is Apply plus OpenPorts for the callers that always want both.
func Sync(ctx context.Context, cfg state.Config, nodes []node.Node, log func(string)) error {
	return Apply(ctx, cfg, nodes, log)
}

// desiredRules is the set of redirects the enabled Hysteria2 nodes require.
func desiredRules(nodes []node.Node) ([]Rule, error) {
	var out []Rule
	for _, n := range enabledNodes(nodes) {
		if n.Protocol != state.ProtoHysteria2 {
			continue
		}
		start, end, err := hopRange(n)
		if err != nil {
			return nil, err
		}
		out = append(out, Rule{Start: start, End: end, Target: fmt.Sprint(n.Port)})
	}
	return out, nil
}

// sync brings the recorded rules in line with desired: every wanted rule is ensured and
// written down, then every recorded rule that is no longer wanted is deleted and
// forgotten.
//
// Deleting from the ledger rather than from the node list is the whole point. A rule
// whose node was removed hours ago is still in the ledger, so it is still reachable;
// before this, that rule was unowned by anything on the host and stayed in PREROUTING
// for good.
func sync(ctx context.Context, desired []Rule, log func(string)) error {
	backend := detectBackend()
	if backend == None {
		log("no firewall backend detected, skipped")
		return nil
	}
	for i := range desired {
		desired[i].Backend = string(backend)
	}

	ledger := LoadLedger(ledgerPath)
	have := ledger.Rules()

	// Delete first, so a rule that moved (same range, new target) never exists twice.
	for _, r := range have {
		if containsRule(desired, r) {
			continue
		}
		if err := deleteRule(ctx, backend, r); err != nil {
			return err
		}
		ledger.Remove(r)
		log("port hopping " + r.Start + ":" + r.End + " -> " + r.Target + " removed")
	}

	for _, r := range desired {
		if err := addRule(ctx, backend, r); err != nil {
			return err
		}
		if !ledger.Has(r) {
			log("port hopping " + r.Start + ":" + r.End + " -> " + r.Target)
		}
		ledger.Add(r)
	}
	return ledger.Save(ledgerPath)
}

func containsRule(list []Rule, r Rule) bool {
	for _, x := range list {
		if x.key() == r.key() {
			return true
		}
	}
	return false
}

// Remove deletes every redirect rule this program recorded, whatever the node list now
// says.
//
// It deliberately no longer takes its work from nodes. Keying the deletion on the live
// node list meant a node deleted earlier took the knowledge of its rule with it: the
// rule stayed in PREROUTING with nothing left that could name it. Uninstall and "turn
// port hopping off" both call this, and both want every rule this program created gone.
func Remove(ctx context.Context, _ []node.Node) error {
	return RemoveAll(ctx)
}

// RemoveAll deletes and forgets every recorded rule.
func RemoveAll(ctx context.Context) error {
	backend := detectBackend()
	ledger := LoadLedger(ledgerPath)
	if backend == None {
		// Nothing can be deleted without a tool, but the record must not survive as a
		// claim about rules that are not there.
		return ledger.Save(ledgerPath)
	}
	for _, r := range ledger.Rules() {
		if err := deleteRule(ctx, backend, r); err != nil {
			return err
		}
		ledger.Remove(r)
	}
	return ledger.Save(ledgerPath)
}

// hopRange reads one node's port range, falling back to the compiled default.
func hopRange(n node.Node) (string, string, error) {
	raw := n.Param(node.ParamHopRange)
	if raw == "" {
		raw = state.DefaultHopRange
	}
	start, end, ok := strings.Cut(raw, ":")
	if !ok || start == "" || end == "" {
		return "", "", errors.New("invalid hop range: " + raw)
	}
	return start, end, nil
}

// OpenPorts opens the enabled node ports with ufw or firewalld when present.
func OpenPorts(ctx context.Context, cfg state.Config, nodes []node.Node, log func(string)) {
	var tcp, udp []string
	for _, n := range enabledNodes(nodes) {
		switch n.Protocol {
		case state.ProtoAnyTLS, state.ProtoVLESSReality, state.ProtoVMessWSTLS:
			tcp = append(tcp, fmt.Sprint(n.Port))
		case state.ProtoHysteria2, state.ProtoTUIC:
			udp = append(udp, fmt.Sprint(n.Port))
		}
	}
	if cfg.SubServePort > 0 {
		tcp = append(tcp, fmt.Sprint(cfg.SubServePort))
	}
	if len(tcp) == 0 && len(udp) == 0 {
		return
	}

	var failed []string
	switch {
	case has("ufw"):
		for _, p := range tcp {
			if !run(ctx, "ufw", "allow", p+"/tcp") {
				failed = append(failed, p+"/tcp")
			}
		}
		for _, p := range udp {
			if !run(ctx, "ufw", "allow", p+"/udp") {
				failed = append(failed, p+"/udp")
			}
		}
		if len(failed) == 0 {
			log("ufw rules updated")
		}
	case has("firewall-cmd"):
		for _, p := range tcp {
			if !run(ctx, "firewall-cmd", "--permanent", "--add-port="+p+"/tcp") {
				failed = append(failed, p+"/tcp")
			}
		}
		for _, p := range udp {
			if !run(ctx, "firewall-cmd", "--permanent", "--add-port="+p+"/udp") {
				failed = append(failed, p+"/udp")
			}
		}
		if !run(ctx, "firewall-cmd", "--reload") {
			failed = append(failed, "--reload")
		}
		if len(failed) == 0 {
			log("firewalld rules updated")
		}
	}
	if len(failed) > 0 {
		log("firewall: could not open " + strings.Join(failed, ", "))
	}
}

// enabledNodes filters the list to the nodes that are switched on.
func enabledNodes(nodes []node.Node) []node.Node {
	out := make([]node.Node, 0, len(nodes))
	for _, n := range nodes {
		if n.Enabled {
			out = append(out, n)
		}
	}
	return out
}

// addRule installs a redirect, exactly once.
func addRule(ctx context.Context, backend Backend, r Rule) error {
	switch backend {
	case IPTables:
		return iptablesAdd(ctx, r.Start, r.End, r.Target)
	case NFTables:
		return nftAdd(ctx, r.Start, r.End, r.Target)
	}
	return nil
}

// deleteRule removes every copy of a redirect. Deleting an absent rule succeeds, so the
// caller does not have to know whether it was ever installed.
func deleteRule(ctx context.Context, backend Backend, r Rule) error {
	switch backend {
	case IPTables:
		return iptablesDelete(ctx, r.Start, r.End, r.Target)
	case NFTables:
		return nftDelete(ctx, r.Start, r.End, r.Target)
	}
	return nil
}

// iptablesRule is one complete iptables invocation: the verb, then the rule.
//
// The order matters and is not cosmetic. "iptables -A -t nat ..." is rejected with
// "Bad argument `nat'": the table has to be selected before the command that acts on
// it. The previous form of this code built the arguments by prepending the verb to a
// spec that already began with -t nat, which only ever worked because it was never
// exercised against a real iptables — an isolated test caught it immediately.
func iptablesRule(verb, start, end, to string) []string {
	return []string{"-t", "nat", verb, "PREROUTING", "-p", "udp", "--dport", start + ":" + end,
		"-j", "REDIRECT", "--to-ports", to}
}

func iptablesAdd(ctx context.Context, start, end, to string) error {
	if run(ctx, "iptables", iptablesRule("-C", start, end, to)...) {
		return nil // already present: repeating an apply must not add a second copy
	}
	spec := iptablesRule("-A", start, end, to)
	out, err := execOutput(ctx, "iptables", spec...)
	if err != nil {
		// The tool's own words are the only useful thing to report here: "failed to add
		// redirect rule" alone left an operator with nothing to act on.
		return errors.New("iptables: failed to add redirect rule: " +
			strings.TrimSpace(out) + " (" + strings.Join(spec, " ") + ")")
	}
	return nil
}

func iptablesDelete(ctx context.Context, start, end, to string) error {
	// A loop because the same redirect can legitimately appear more than once (an
	// operator, or an older version, may have added it twice). The break is what keeps
	// a refused delete from spinning: -C still succeeding after a failed -D would
	// otherwise repeat forever.
	for run(ctx, "iptables", iptablesRule("-C", start, end, to)...) {
		if !run(ctx, "iptables", iptablesRule("-D", start, end, to)...) {
			break
		}
	}
	return nil
}

// nftFamilyAndChain finds the NAT prerouting chain by looking at the table rather than
// guessing the chain's name.
//
// The name is not ours to choose: this program's own nftEnsure creates it as
// "prerouting", while iptables-nft creates "PREROUTING". nft is case sensitive, so a
// hardcoded name silently made the delete path do nothing on a host whose chain came
// from iptables-nft: nftList returned nothing, nftHandles returned no handles, and the
// redirect was never removed.
func nftFamilyAndChain(ctx context.Context) (family, chain string, ok bool) {
	out, err := execOutput(ctx, "nft", "list", "tables")
	if err != nil {
		return "", "", false
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line) // "table ip nat" / "table ip6 nat"
		if len(f) != 3 || f[0] != "table" || f[2] != "nat" {
			continue
		}
		listing, err := execOutput(ctx, "nft", "list", "table", f[1], "nat")
		if err != nil {
			continue
		}
		for _, l := range strings.Split(listing, "\n") {
			g := strings.Fields(l)
			if len(g) == 3 && g[0] == "chain" {
				return f[1], g[1], true
			}
		}
	}
	return "", "", false
}

// nftRuleLine is one "udp dport A-B ... redirect to :T # handle H" line.
type nftRuleLine struct {
	Range   string
	Target  string
	Handle  string
	Counter int
}

// parseNFTRules reads the redirect rules out of an nft listing by token name, so it does
// not depend on how many words nft happens to emit before "redirect".
func parseNFTRules(listing string) []nftRuleLine {
	var out []nftRuleLine
	for _, line := range strings.Split(listing, "\n") {
		if !strings.Contains(line, "redirect") {
			continue
		}
		fields := strings.Fields(line)
		var r nftRuleLine
		for i, f := range fields {
			if i+1 >= len(fields) {
				break
			}
			switch f {
			case "dport":
				r.Range = fields[i+1]
			case "to":
				r.Target = strings.TrimPrefix(fields[i+1], ":")
			case "handle":
				r.Handle = fields[i+1]
			case "counter":
				r.Counter = 1 // nft only emits "counter" (and its counts) when it is one
			}
		}
		if r.Range != "" && r.Target != "" {
			out = append(out, r)
		}
	}
	return out
}

func nftAdd(ctx context.Context, start, end, to string) error {
	family, chain, ok := nftFamilyAndChain(ctx)
	if !ok {
		if err := nftEnsure(ctx); err != nil {
			return err
		}
		family, chain, ok = nftFamilyAndChain(ctx)
		if !ok {
			return errors.New("nft: no nat prerouting chain could be prepared")
		}
	}
	// Idempotent: a matching rule means there is nothing to add.
	listing, _ := execOutput(ctx, "nft", "-a", "list", "table", family, "nat")
	for _, r := range parseNFTRules(listing) {
		if r.Range == start+"-"+end && r.Target == to {
			return nil
		}
	}
	if !run(ctx, "nft", "add", "rule", family, "nat", chain,
		"udp", "dport", start+"-"+end, "redirect", "to", ":"+to) {
		return errors.New("nft: failed to add redirect rule")
	}
	return nil
}

func nftDelete(ctx context.Context, start, end, to string) error {
	family, _, ok := nftFamilyAndChain(ctx)
	if !ok {
		return nil // no table, therefore no rule of ours to delete
	}
	// Delete by handle, matched on the exact range and target. Repeated until no match
	// remains, so duplicates left by an older version are cleared too.
	for {
		listing, _ := execOutput(ctx, "nft", "-a", "list", "table", family, "nat")
		handle := ""
		for _, r := range parseNFTRules(listing) {
			if r.Range == start+"-"+end && r.Target == to && r.Handle != "" {
				handle = r.Handle
				break
			}
		}
		if handle == "" {
			return nil
		}
		if !run(ctx, "nft", "delete", "rule", family, "nat", chainOf(listing), "handle", handle) {
			return nil // cannot delete: stop rather than spin
		}
	}
}

// chainOf returns the first chain name in a table listing, which is the chain the rules
// in that listing belong to.
func chainOf(listing string) string {
	for _, l := range strings.Split(listing, "\n") {
		f := strings.Fields(l)
		if len(f) >= 2 && f[0] == "chain" {
			return f[1]
		}
	}
	return ""
}

func nftEnsure(ctx context.Context) error {
	if !nftHas(ctx, "table", "ip", "nat") {
		if !run(ctx, "nft", "add", "table", "ip", "nat") {
			return errors.New("nft: could not create table ip nat")
		}
	}
	if !nftHas(ctx, "chain", "ip", "nat", "prerouting") {
		if !run(ctx, "nft", "add", "chain", "ip", "nat", "prerouting", "{",
			"type", "nat", "hook", "prerouting", "priority", "dstnat", ";", "}") {
			return errors.New("nft: could not create chain ip nat prerouting")
		}
	}
	return nil
}

func nftHas(ctx context.Context, args ...string) bool {
	full := append([]string{"list"}, args...)
	return run(ctx, "nft", full...)
}

// Inventory reports the redirect rules present on the host that look like the ones this
// program installs: a UDP destination range redirected to a single local port.
//
// It is for reporting and for PruneOrphans. A single-port redirect is not reported,
// because that is not a shape this program creates.
func Inventory(ctx context.Context) []Rule {
	switch detectBackend() {
	case IPTables:
		out, err := execOutput(ctx, "iptables", "-t", "nat", "-S", "PREROUTING")
		if err != nil {
			return nil
		}
		var rules []Rule
		for _, line := range strings.Split(out, "\n") {
			f := strings.Fields(line)
			if len(f) == 0 || f[0] != "-A" {
				continue
			}
			var start, end, to string
			for i, tok := range f {
				if i+1 >= len(f) {
					break
				}
				switch tok {
				case "--dport":
					start, end, _ = strings.Cut(f[i+1], ":")
				case "--to-ports":
					to = f[i+1]
				}
			}
			if start != "" && end != "" && to != "" && start != end {
				rules = append(rules, Rule{Backend: string(IPTables), Start: start, End: end, Target: to})
			}
		}
		return rules
	case NFTables:
		family, _, ok := nftFamilyAndChain(ctx)
		if !ok {
			return nil
		}
		listing, err := execOutput(ctx, "nft", "-a", "list", "table", family, "nat")
		if err != nil {
			return nil
		}
		var rules []Rule
		for _, r := range parseNFTRules(listing) {
			start, end, ok := strings.Cut(r.Range, "-")
			if !ok || start == "" || end == "" {
				continue
			}
			rules = append(rules, Rule{Backend: string(NFTables), Start: start, End: end, Target: r.Target})
		}
		return rules
	}
	return nil
}

// PruneOrphans removes redirect rules that look like this program's port hopping but
// that nothing needs any more, and that this program never recorded.
//
// It is what cleans up a rule left by a version that had no ledger. It is deliberately
// conservative: a rule is only removed when its target port is not one of the nodes
// currently enabled AND nothing is listening on that port, so a rule another service
// owns is left alone even if it happens to use the same range shape.
func PruneOrphans(ctx context.Context, nodes []node.Node, listening func(string) bool, log func(string)) ([]Rule, error) {
	desired, err := desiredRules(nodes)
	if err != nil {
		return nil, err
	}
	backend := detectBackend()
	// The backend is part of a rule's identity, so the desired set has to carry it
	// before it can be compared with what Inventory reports. Without this every wanted
	// rule looked like an orphan and was deleted.
	for i := range desired {
		desired[i].Backend = string(backend)
	}
	ledger := LoadLedger(ledgerPath)
	var removed []Rule
	for _, r := range Inventory(ctx) {
		if r.Backend != string(backend) {
			continue
		}
		if containsRule(desired, r) || ledger.Has(r) {
			continue // wanted, or already ours and handled by sync
		}
		if listening != nil && listening(r.Target) {
			continue // something is serving that port: not ours to judge
		}
		if err := deleteRule(ctx, backend, r); err != nil {
			return removed, err
		}
		removed = append(removed, r)
		if log != nil {
			log("port hopping " + r.Start + ":" + r.End + " -> " + r.Target + " (orphan) removed")
		}
	}
	return removed, nil
}

// WriteUnit installs a boot service that reapplies the NAT rules.
func WriteUnit(nodes []node.Node) error {
	hop := false
	for _, n := range enabledNodes(nodes) {
		if n.Protocol == state.ProtoHysteria2 {
			hop = true
			break
		}
	}
	if !hop {
		return nil
	}
	exe, err := service.PanelExecutable()
	if err != nil {
		return err
	}
	unit := fmt.Sprintf(`[Unit]
Description=EasySB Hysteria2 port-hopping firewall rules
After=network-online.target
Wants=network-online.target
Before=sing-box.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=%s --apply-firewall

[Install]
WantedBy=multi-user.target
`, exe)
	// Atomic: the port-hopping rules are applied by this unit at boot, so a unit left
	// truncated by a crash would silently drop the hop rules.
	if err := atomicfile.Write(systemdUnitPath, []byte(unit), 0o644); err != nil {
		return err
	}
	return service.DaemonReload()
}

// RemoveUnit deletes the boot service.
func RemoveUnit() error {
	if err := os.Remove(systemdUnitPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return service.DaemonReload()
}

// runUnitAction runs the init system's own tool for one unit action. It is a variable
// so that a test can drive UnitAction's failure path without an init system to fail for
// real: what such a test is about is that the failure reaches the caller.
var runUnitAction = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	return cmd.CombinedOutput()
}

// UnitAction executes a lifecycle action ("enable" or "disable") on the unit.
func UnitAction(ctx context.Context, action string) error {
	out, err := runUnitAction(ctx, "systemctl", action, UnitName+".service")
	if err != nil {
		return unitActionError(action, out, err)
	}
	return nil
}

// unitActionError words a failed unit action the way internal/service words one: the
// tool's own message when it printed one, and the exit status otherwise. The failure has
// to reach the caller, because an enable that did not take leaves a host whose
// port-hopping rules are gone after the next reboot while the panel said they were
// installed.
func unitActionError(action string, out []byte, err error) error {
	msg := strings.TrimSpace(string(out))
	if msg == "" {
		msg = err.Error()
	}
	return errors.New(action + " " + UnitName + ": " + msg)
}

func has(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func run(ctx context.Context, name string, args ...string) bool {
	_, err := execOutput(ctx, name, args...)
	return err == nil
}

// execOutput runs a firewall tool. It is a variable because every rule decision in this
// package is a decision about what a tool printed, and until now none of it could be
// tested without root and a real ruleset: the delete path believed an empty nft listing
// and quietly did nothing, which is exactly the kind of defect a test would have caught.
var execOutput = func(ctx context.Context, name string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	return string(out), err
}
