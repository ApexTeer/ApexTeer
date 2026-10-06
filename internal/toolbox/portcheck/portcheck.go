// Package portcheck answers the one question a host's mail story hangs on: can
// this machine be reached on the ports a mail server needs, and is its address
// presentable to the mail world?
//
// It exists because a self-hosted mailbox fails for two reasons that have
// nothing to do with anything the panel configures:
//
//   - The provider blocks the port. Cloud providers filter inbound 25 by
//     default; no amount of local configuration undoes that.
//   - The address has no reverse name (PTR), or its PTR does not resolve back
//     to the same address (forward-confirmed reverse DNS). Large receivers
//     reject such senders, and the only place to fix it is the operator's
//     console for the IP.
//
// Both are read the way an outside tester would read them: every TCP probe
// dials the host's own public IPv4 rather than 127.0.0.1, because the packet
// has to cross the provider's edge for the answer to mean anything. A refused
// connection is therefore a good result, not a bad one — the RST proves the
// packet arrived, which is exactly the question, and port 25 with no MTA
// installed yet can only ever answer with a refusal.
//
// What the tool cannot tell is written down instead of guessed at: a provider
// without NAT hairpin makes every port look filtered from the inside, an IP can
// be reachable and still be blocklisted, and outbound 25 is a separate question.
// Every network surface (HTTP client, dialer, resolver) is injected, so a test
// runs with no socket and no DNS, and every measurement that failed is a note
// explaining why rather than a plausible-looking zero.
package portcheck

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/EasySB-Team/EasySB/internal/toolbox"
)

// DefaultIPURL is the service that answers with the address it saw. It is IPv4
// only on purpose: the mail ports are probed over IPv4, so the address has to
// be the one an IPv4 sender would reach.
const DefaultIPURL = "https://api.ipify.org"

// DefaultDialTimeout bounds one TCP connection attempt. Five seconds is long
// enough for a filter to answer with a RST and short enough that ten silent
// ports do not hold the panel for a minute.
const DefaultDialTimeout = 5 * time.Second

// maxIPBody caps the discovery read: the answer is an address, not a document.
const maxIPBody = 128

// Dialer is the TCP surface the port probes use. *net.Dialer satisfies it; a
// test injects a stub so a run never opens a socket.
type Dialer interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}

// Resolver is the DNS surface the reverse lookup uses. *net.Resolver satisfies
// it; a test injects a stub so a run never asks a real resolver.
type Resolver interface {
	LookupAddr(ctx context.Context, addr string) ([]string, error)
	LookupHost(ctx context.Context, host string) ([]string, error)
}

// Options configures a Check run. The zero value probes for real.
type Options struct {
	// PublicIP skips discovery when set: the panel may already know the host's
	// public address from another toolbox tool, and a discovery endpoint that
	// is unreachable from the host would otherwise block a check that does not
	// need it.
	PublicIP string
	// IPURL overrides the discovery endpoint. Empty means DefaultIPURL.
	IPURL string
	// Client fetches the public IP. Nil means an HTTP client bounded by Timeout.
	Client toolbox.HTTPDoer
	// Dialer opens the probe connections. Nil means a net.Dialer.
	Dialer Dialer
	// Resolver answers the PTR and forward lookups. Nil means the system
	// resolver.
	Resolver Resolver
	// DialTimeout bounds one connection attempt and one DNS query. Zero means
	// DefaultDialTimeout.
	DialTimeout time.Duration
	// Timeout bounds the run as a whole. Zero means toolbox.DefaultTimeout.
	Timeout time.Duration
	// Log receives one progress line per measurement; nil discards them.
	Log func(string)
	// Clock is the source of now for the run's own timing. Nil means time.Now.
	// This tool measures reachability, not speed, so the clock is only used to
	// stamp how long the whole check took.
	Clock func() time.Time
}

// Check measures the mail-relevant ports and the reverse DNS of the host's
// public IPv4, and decides whether a mail server can live on that address.
//
// A non-nil error means nothing could be measured: the public IPv4 was not
// readable, so there was no address to dial and no address to look up. The
// Report is still filled in — every port carries StatusSkipped and the notes
// say why — so a caller that renders the result anyway still shows the
// operator the reason instead of an empty table.
func Check(ctx context.Context, opts Options) (Report, error) {
	start := opts.now()
	rep, err := check(ctx, opts)
	rep.Started, rep.Elapsed = start, opts.now().Sub(start)
	// The timing line belongs after the measurements it describes, and it also
	// tells the operator which dial deadline produced the timeouts.
	rep.Note("检测用时：%s（单个端口超时 %s）。", rep.Elapsed.Round(time.Millisecond), opts.dialTimeout())
	return rep, err
}

func check(ctx context.Context, opts Options) (Report, error) {
	rep := Report{Ports: make([]PortResult, 0, len(catalogue))}
	ctx, cancel := context.WithTimeout(ctx, opts.budget())
	defer cancel()

	ip, source, err := opts.publicIP(ctx)
	if err != nil {
		rep.Note("公网 IPv4 未取到：%v。", err)
		rep.Note("没有公网 IP 就没有探测目标：端口一个都没测，PTR 也没查，结论无法确认。")
		for _, p := range catalogue {
			rep.Ports = append(rep.Ports, PortResult{Port: p, Status: StatusSkipped})
		}
		rep.Verdict = VerdictUnknown
		opts.logf("公网 IPv4 未取到，全部跳过：%v", err)
		return rep, err
	}
	rep.PublicIP, rep.IPSource = ip, source
	opts.logf("公网 IPv4 %s（来自 %s），开始探测 %d 个端口", ip, source, len(catalogue))
	rep.Note("公网 IPv4：%s（来源：%s）。", ip, source)
	rep.Note(noteTarget)

	// One port at a time: the log then reads in catalogue order, and a slow
	// port cannot hide behind a fast one. Ten ports with a five second deadline
	// each are still bounded by Options.Timeout.
	for _, p := range catalogue {
		res := opts.probe(ctx, p, ip)
		rep.Ports = append(rep.Ports, res)
		opts.logf("端口 %d（%s）：%s", p.Number, p.Purpose, res.Status.Text())
	}

	rep.RDNS = lookupRDNS(ctx, opts.resolver(), ip, opts.dialTimeout())
	opts.logf("PTR：%s", rep.RDNS.summaryText())

	rep.Note(noteStates)
	rep.Note(noteHairpin)
	rep.Note(noteIntercept)
	rep.Note(noteOtherPorts)
	rep.Note("%s", rep.RDNS.Line(ip))
	rep.decide()
	return rep, nil
}

// Run is the toolbox entry: it maps the panel's Options onto this tool's own and
// renders the report. The error from Check is returned next to the rendered
// result so a panel that shows the error still has the notes explaining it.
//
// toolbox.Options.Scratch goes unused because this tool writes no files.
func Run(ctx context.Context, o toolbox.Options) (toolbox.Result, error) {
	rep, err := Check(ctx, Options{
		Client:      o.HTTP(),
		Timeout:     o.Duration(),
		DialTimeout: DefaultDialTimeout,
		Log:         o.Log,
		Clock:       o.Clock,
	})
	return rep.Result(), err
}

// Tool is the toolbox entry for this check: the id the menu and --tool use, the
// group it is filed under, and how to run it.
func Tool() toolbox.Tool {
	return toolbox.Tool{
		// "ip" is the toolbox group for what the world sees of this host: the mail
		// ports are asked of the public address, alongside the IP quality databases.
		// (The panel's groups are unlock, network, ip and hardware.)
		ID:    "portcheck",
		Group: "ip",
		Run:   Run,
	}
}

// publicIP returns the address to probe: the injected one when there is one,
// otherwise the answer of the discovery endpoint. The second result names where
// the address came from, because the source is what makes an address
// trustworthy enough to publish.
func (o Options) publicIP(ctx context.Context) (string, string, error) {
	if o.PublicIP != "" {
		ip, err := parseIPv4(o.PublicIP)
		if err != nil {
			return "", "", fmt.Errorf("Options.PublicIP: %w", err)
		}
		return ip, "Options.PublicIP", nil
	}
	url := o.IPURL
	if url == "" {
		url = DefaultIPURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", err
	}
	resp, err := o.http().Do(req)
	if err != nil {
		return "", "", fmt.Errorf("%s: %w", url, err)
	}
	if resp.Body == nil {
		return "", "", fmt.Errorf("%s answered without a body", url)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxIPBody))
	if err != nil {
		return "", "", fmt.Errorf("%s: %w", url, err)
	}
	// The status is checked after the read so a proxy's error page is reported
	// as a status problem rather than as a malformed address.
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("%s answered HTTP %d", url, resp.StatusCode)
	}
	ip, err := parseIPv4(string(body))
	if err != nil {
		return "", "", fmt.Errorf("%s: %w", url, err)
	}
	return ip, url, nil
}

// parseIPv4 accepts one textual IPv4 address and returns its canonical form.
// Anything else — an IPv6 answer, an HTML apology, an empty body — is an error
// naming what arrived, because a probe dialling it would measure nothing.
func parseIPv4(s string) (string, error) {
	s = strings.TrimSpace(s)
	parsed := net.ParseIP(s)
	if parsed == nil || parsed.To4() == nil {
		return "", fmt.Errorf("%q is not an IPv4 address", s)
	}
	return parsed.To4().String(), nil
}

// http is the client used for discovery.
func (o Options) http() toolbox.HTTPDoer {
	if o.Client != nil {
		return o.Client
	}
	return &http.Client{Timeout: o.budget()}
}

// dialer is the injected dialer, or a plain one for a real run.
func (o Options) dialer() Dialer {
	if o.Dialer != nil {
		return o.Dialer
	}
	return &net.Dialer{}
}

// resolver is the injected resolver, or the system one.
func (o Options) resolver() Resolver {
	if o.Resolver != nil {
		return o.Resolver
	}
	return net.DefaultResolver
}

func (o Options) dialTimeout() time.Duration {
	if o.DialTimeout > 0 {
		return o.DialTimeout
	}
	return DefaultDialTimeout
}

func (o Options) budget() time.Duration {
	if o.Timeout > 0 {
		return o.Timeout
	}
	return toolbox.DefaultTimeout
}

func (o Options) now() time.Time {
	if o.Clock != nil {
		return o.Clock()
	}
	return time.Now()
}

func (o Options) logf(format string, args ...any) {
	if o.Log != nil {
		o.Log(fmt.Sprintf(format, args...))
	}
}

// isTimeout recognises a deadline as the standard library reports one: the
// context deadline of the dial, the poll deadline underneath it, and the
// net.Error form a wrapped dial error keeps.
func isTimeout(err error) bool {
	var ne net.Error
	return errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, os.ErrDeadlineExceeded) ||
		(errors.As(err, &ne) && ne.Timeout())
}

// isRefused recognises the peer's RST. The errno for it is the one thing that
// differs per platform here, so the value lives in errno_windows.go and
// errno_unix.go.
func isRefused(err error) bool { return errors.Is(err, errnoConnRefused) }
