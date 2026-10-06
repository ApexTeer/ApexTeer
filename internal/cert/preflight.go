package cert

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/EasySB-Team/EasySB/internal/netutil"
)

// Report is what can be checked about a domain before spending an ACME attempt on
// it. Every field is a fact rather than a verdict: a domain behind a CDN resolves
// to addresses that are not this server and is still issuable, so the caller
// decides what to do with the combination.
type Report struct {
	Resolved []string
	PublicIP string
	DNSFail  error
}

// Mismatch reports whether the domain resolves somewhere else.
// means the panel could not work out this server's address, which is not a
// mismatch, only an unknown.
func (r Report) Mismatch() bool {
	if r.PublicIP == "" || len(r.Resolved) == 0 {
		return false
	}
	for _, ip := range r.Resolved {
		if ip == r.PublicIP {
			return false
		}
	}
	return true
}

// Others are the resolved addresses that are not this server. A domain with one of
// these is not necessarily broken: Let's Encrypt validates a challenge against
// every address a domain resolves to, so a stale record pointing at a host that no
// longer exists fails the whole order with a connection error on that address, while
// the address of this server passes. It is worth saying out loud before the attempt.
func (r Report) Others() []string {
	if r.PublicIP == "" {
		return nil
	}
	var others []string
	for _, ip := range r.Resolved {
		if ip != r.PublicIP {
			others = append(others, ip)
		}
	}
	return others
}

// Preflight gathers the facts an operator needs before issuing: where the domain
// currently points, and whether this host can be reached at all. Both are cheap,
// and both turn a two-minute wait into a specific sentence when they are wrong.
func Preflight(ctx context.Context, domain string) Report {
	var rep Report
	dctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if ips, err := net.DefaultResolver.LookupHost(dctx, domain); err != nil {
		rep.DNSFail = err
	} else {
		rep.Resolved = ips
	}
	if ip, err := netutil.PublicIP(dctx); err == nil {
		rep.PublicIP = ip
	}
	return rep
}

// CheckPort80 reports why the standalone challenge cannot bind port 80. It is
// called after the core is stopped, because the core is what usually holds it.
func CheckPort80() error {
	ln, err := net.Listen("tcp", ":80")
	if err != nil {
		return fmt.Errorf("%w (%s)", err, port80Holder())
	}
	return ln.Close()
}

// port80Holder names the process holding port 80 when ss can tell us. It is
// best-effort: an empty result just means the error message stays shorter.
func port80Holder() string {
	if _, err := exec.LookPath("ss"); err != nil {
		return "no ss to name the holder"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ss", "-ltnpH", "sport = :80").Output()
	if err != nil {
		return ""
	}
	line := strings.Join(strings.Fields(string(out)), " ")
	if len(line) > 120 {
		line = line[:120]
	}
	return line
}

// secondaryValidation matches the address Let's Encrypt names when a remote
// perspective fails to reach the domain:
//
//	During secondary validation: 203.0.113.9: Fetching …: Connection refused
//
// An address may arrive bracketed, the form net.JoinHostPort produces for IPv6; an
// address without brackets is indistinguishable from the separator before the port
// that follows it, so only the bracketed form can be read back whole.
var secondaryValidation = regexp.MustCompile(`During secondary validation: (?:\[([0-9a-fA-F:.]+)\]|([0-9a-fA-F.:]+?)):`)

// StrayAddress returns the address a failed secondary validation could not reach.
// Combined with Report.Others it explains the failure: the address in the error is
// the stale record, and removing it is the fix. The CA's own wording survives into
// the error lego returns, so this reads the same text it always did.
func StrayAddress(issueErr error) string {
	if issueErr == nil {
		return ""
	}
	m := secondaryValidation.FindStringSubmatch(issueErr.Error())
	if len(m) < 3 {
		return ""
	}
	for _, addr := range m[1:] {
		if addr != "" {
			return addr
		}
	}
	return ""
}
