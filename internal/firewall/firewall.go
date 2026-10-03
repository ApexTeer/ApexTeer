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

	"github.com/MinimaxFlora/EasySB/internal/service"
	"github.com/MinimaxFlora/EasySB/internal/state"
)

// UnitName is the boot service that reapplies the NAT rules.
const UnitName = "easysb-firewall"

const systemdUnitPath = "/etc/systemd/system/easysb-firewall.service"
const openRCUnitPath = "/etc/init.d/easysb-firewall"

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

// Apply adds the port-hopping redirect and opens the enabled protocol ports.
func Apply(ctx context.Context, cfg state.Config, log func(string)) error {
	backend := Detect()
	if backend == None {
		log("no firewall backend detected, skipped")
	} else if cfg.Enabled[state.ProtoHysteria2] {
		start, end, err := hopRange(cfg)
		if err != nil {
			return err
		}
		to := cfg.Ports[state.ProtoHysteria2]
		if backend == IPTables {
			if err := iptablesAdd(ctx, start, end, to); err != nil {
				return err
			}
		} else {
			if err := nftAdd(ctx, start, end, to); err != nil {
				return err
			}
		}
		log("port hopping " + start + ":" + end + " -> " + to)
	}
	OpenPorts(ctx, cfg, log)
	return nil
}

// Remove deletes the port-hopping redirect rules.
// The rule is deleted whenever it might exist: keying this on the protocol
// switch left a live redirect behind when the operator turned Hysteria2 off
// first and then asked to remove the rules, while the panel reported success.
func Remove(ctx context.Context, cfg state.Config) error {
	start, end, err := hopRange(cfg)
	if err != nil {
		return err
	}
	to := cfg.Ports[state.ProtoHysteria2]
	switch Detect() {
	case IPTables:
		return iptablesDelete(ctx, start, end, to)
	case NFTables:
		return nftDelete(ctx, start, end, to)
	}
	return nil
}

func hopRange(cfg state.Config) (string, string, error) {
	raw := cfg.HopRange
	if raw == "" {
		raw = state.DefaultHopRange
	}
	start, end, ok := strings.Cut(raw, ":")
	if !ok || start == "" || end == "" {
		return "", "", errors.New("invalid hop range: " + raw)
	}
	return start, end, nil
}

// OpenPorts opens the enabled protocol ports with ufw or firewalld when present.
func OpenPorts(ctx context.Context, cfg state.Config, log func(string)) {
	var tcp, udp []string
	if cfg.Enabled[state.ProtoAnyTLS] {
		tcp = append(tcp, cfg.Ports[state.ProtoAnyTLS])
	}
	if cfg.Enabled[state.ProtoTUIC] {
		udp = append(udp, cfg.Ports[state.ProtoTUIC])
	}
	if cfg.Enabled[state.ProtoVLESSReality] {
		tcp = append(tcp, cfg.Ports[state.ProtoVLESSReality])
	}
	if cfg.Enabled[state.ProtoVMessWSTLS] {
		tcp = append(tcp, cfg.Ports[state.ProtoVMessWSTLS])
	}
	if cfg.Enabled[state.ProtoHysteria2] {
		udp = append(udp, cfg.Ports[state.ProtoHysteria2])
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

func iptablesAdd(ctx context.Context, start, end, to string) error {
	if run(ctx, "iptables", "-t", "nat", "-C", "PREROUTING", "-p", "udp", "--dport", start+":"+end, "-j", "REDIRECT", "--to-ports", to) {
		return nil
	}
	if !run(ctx, "iptables", "-t", "nat", "-A", "PREROUTING", "-p", "udp", "--dport", start+":"+end, "-j", "REDIRECT", "--to-ports", to) {
		return errors.New("iptables: failed to add redirect rule")
	}
	return nil
}

func iptablesDelete(ctx context.Context, start, end, to string) error {
	for run(ctx, "iptables", "-t", "nat", "-C", "PREROUTING", "-p", "udp", "--dport", start+":"+end, "-j", "REDIRECT", "--to-ports", to) {
		if !run(ctx, "iptables", "-t", "nat", "-D", "PREROUTING", "-p", "udp", "--dport", start+":"+end, "-j", "REDIRECT", "--to-ports", to) {
			break
		}
	}
	return nil
}

func nftAdd(ctx context.Context, start, end, to string) error {
	nftEnsure(ctx)
	spec := fmt.Sprintf("udp dport %s-%s redirect to :%s", start, end, to)
	if strings.Contains(nftList(ctx), spec) {
		return nil
	}
	if !run(ctx, "nft", "add", "rule", "ip", "nat", "prerouting", "udp", "dport", start+"-"+end, "redirect", "to", ":"+to) {
		return errors.New("nft: failed to add redirect rule")
	}
	return nil
}

func nftDelete(ctx context.Context, start, end, to string) error {
	spec := fmt.Sprintf("udp dport %s-%s redirect to :%s", start, end, to)
	for _, handle := range nftHandles(ctx, spec) {
		run(ctx, "nft", "delete", "rule", "ip", "nat", "prerouting", "handle", handle)
	}
	return nil
}

func nftEnsure(ctx context.Context) {
	if !nftHas(ctx, "table", "ip", "nat") {
		run(ctx, "nft", "add", "table", "ip", "nat")
	}
	if !nftHas(ctx, "chain", "ip", "nat", "prerouting") {
		run(ctx, "nft", "add", "chain", "ip", "nat", "prerouting", "{", "type", "nat", "hook", "prerouting", "priority", "dstnat", ";", "}")
	}
}

func nftHas(ctx context.Context, args ...string) bool {
	full := append([]string{"list"}, args...)
	return run(ctx, "nft", full...)
}

func nftList(ctx context.Context) string {
	out, _ := output(ctx, "nft", "-a", "list", "chain", "ip", "nat", "prerouting")
	return out
}

func nftHandles(ctx context.Context, spec string) []string {
	var handles []string
	for _, line := range strings.Split(nftList(ctx), "\n") {
		if strings.Contains(line, spec) {
			fields := strings.Fields(line)
			if len(fields) > 0 {
				handles = append(handles, fields[len(fields)-1])
			}
		}
	}
	return handles
}

// WriteUnit installs a boot service that reapplies the NAT rules.
func WriteUnit(cfg state.Config) error {
	if !cfg.Enabled[state.ProtoHysteria2] {
		return nil
	}
	exe, err := service.PanelExecutable()
	if err != nil {
		return err
	}
	if service.Detect() == service.OpenRC {
		unit := fmt.Sprintf(`#!/sbin/openrc-run
name="%s"
description="EasySB Hysteria2 port-hopping firewall rules"
depend() { after net; before sing-box; }

start() {
  ebegin "Applying EasySB port-hopping rules"
  %s --apply-firewall >/dev/null 2>&1
  eend $?
}
`, UnitName, exe)
		if err := os.WriteFile(openRCUnitPath, []byte(unit), 0o755); err != nil {
			return err
		}
		return nil
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
	if err := os.WriteFile(systemdUnitPath, []byte(unit), 0o644); err != nil {
		return err
	}
	return service.DaemonReload()
}

// RemoveUnit deletes the boot service.
func RemoveUnit() error {
	if service.Detect() == service.OpenRC {
		if err := os.Remove(openRCUnitPath); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
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
	name, args := "systemctl", []string{action, UnitName + ".service"}
	if service.Detect() == service.OpenRC {
		name, args = "rc-update", []string{mapAction(action), UnitName, "default"}
	}
	out, err := runUnitAction(ctx, name, args...)
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

func mapAction(action string) string {
	if action == "disable" {
		return "del"
	}
	return "add"
}

func has(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func run(ctx context.Context, name string, args ...string) bool {
	_, err := output(ctx, name, args...)
	return err == nil
}

func output(ctx context.Context, name string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	return string(out), err
}
