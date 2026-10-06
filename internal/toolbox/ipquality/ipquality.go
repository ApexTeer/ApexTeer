// Package ipquality reports what the host's public IPv4 address looks like from the
// outside: which free databases agree on where it is and who announces it, what kind of
// line it sits on, and whether the common DNS blocklists answer for it.
//
// The panel needs this before it can promise much. "Why is this node blocked" usually
// begins with an address that a dozen blocklists already answer for, or one registered
// to a datacenter in a country the operator did not expect. Everything here is somebody
// else's reading, so four rules shape the package:
//
//   - A database that does not answer becomes a failure carrying its reason (timeout,
//     rate limit, HTTP status, missing field). No field is ever filled in from another
//     database, and a failed row carries no values at all.
//   - The line-kind verdict is folded from the databases' own claims, never from a
//     heuristic on their prose. Two databases that disagree produce 不一致, and both
//     sides are printed; there is no majority vote.
//   - A blocklist that answers is a hit, NXDOMAIN is a clean miss, and anything else
//     (timeout, a query the zone refuses to answer for a public resolver) is reported
//     as unreached rather than as either verdict.
//   - Every request goes through toolbox.Options.HTTP() and every DNS query through an
//     injected Resolver, so the tests run without a network.
//
// Only free, keyless endpoints are used. The panel ships to an operator's VPS and must
// not need an account, and a key compiled into the binary would be spent by whoever
// reads it first. That is also why a database whose detection flags need a free key
// (ipapi.is) contributes only what its keyless answer carries, and says so in a note.
package ipquality

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/EasySB-Team/EasySB/internal/toolbox"
)

const (
	// ToolID is the node id, the i18n key suffix and the --tool argument.
	ToolID = "ipquality"
	// Group is the toolbox group the menu files this tool under.
	Group = "ip"
)

// Defaults for one run. A free API deserves a short leash: the whole point of the
// per-request budget is that one silent database cannot hold up the report.
const (
	// DefaultRequestTimeout bounds one HTTP request.
	DefaultRequestTimeout = 10 * time.Second
	// DefaultConcurrency caps how many requests are in flight at once.
	DefaultConcurrency = 4
)

// maxBody caps how much of a response is read. Every endpoint here answers with a few
// kilobytes of JSON or a bare address; a block page that streams endlessly must not be
// able to fill memory on a small VPS.
const maxBody = 256 << 10

// userAgent identifies the panel. Several of these endpoints answer a bare Go client
// with an HTML block page.
const userAgent = "EasySB-ipquality/1.0"

// Tool is the registry entry the panel builds its menu from.
var Tool = toolbox.Tool{ID: ToolID, Group: Group, Run: Run}

// Run is the panel's entry point: it checks the host's own public address using the
// system resolver. A run that could not determine the address still returns a result —
// the notes say why — because "the panel could not ask" is an answer the operator needs
// to see, not an error the caller has to invent a screen for.
func Run(ctx context.Context, opts toolbox.Options) (toolbox.Result, error) {
	return New(Options{Options: opts}).Check(ctx).Result(), nil
}

// Resolver is the DNS surface the tool needs. Two methods, because the two questions are
// different queries for the resolver: *net.Resolver satisfies it, and a test injects a
// table of answers instead.
//
// The reverse-DNS question cannot go through LookupHost: that method asks for A/AAAA
// records, so an "8.8.8.8.in-addr.arpa" name comes back NXDOMAIN even though the address
// has a PTR record (measured against the system resolver). The blocklist question is a
// plain host lookup on the reversed name, which is exactly LookupHost.
type Resolver interface {
	LookupHost(ctx context.Context, host string) ([]string, error)
	LookupAddr(ctx context.Context, addr string) ([]string, error)
}

// defaultResolver is the resolver a real run uses. It is a variable so a test can drive
// the Tool entry point — not just New — without touching the network.
var defaultResolver Resolver = net.DefaultResolver

// Options configures a Checker. It embeds the panel's toolbox.Options because the HTTP
// client, the progress log and the whole-run deadline belong to the panel.
type Options struct {
	toolbox.Options
	// Resolver answers the blocklist and reverse-DNS queries. Nil uses the system
	// resolver.
	Resolver Resolver
	// IP is the public address to check, when the caller already knows it (the panel
	// reads it for its own header). Empty means discover it.
	IP string
	// RequestTimeout bounds one HTTP request. Zero means DefaultRequestTimeout.
	RequestTimeout time.Duration
	// Concurrency caps how many databases are queried at once. Zero means
	// DefaultConcurrency; the cap keeps the free, rate-limited APIs polite.
	Concurrency int
}

// Checker runs one quality check.
type Checker struct {
	base        toolbox.Options
	resolver    Resolver
	client      toolbox.HTTPDoer
	ip          string
	ipNote      string
	reqTimeout  time.Duration
	concurrency int
}

// New builds a Checker. A caller-supplied address that is not an IPv4 literal is
// rejected here with a note and the address is discovered instead, so a bad value from
// the operator's side degrades to a normal run rather than to an empty report.
func New(opts Options) *Checker {
	c := &Checker{
		base:        opts.Options,
		resolver:    opts.Resolver,
		client:      opts.HTTP(),
		ip:          strings.TrimSpace(opts.IP),
		reqTimeout:  opts.RequestTimeout,
		concurrency: opts.Concurrency,
	}
	if c.resolver == nil {
		c.resolver = defaultResolver
	}
	if c.reqTimeout <= 0 {
		c.reqTimeout = DefaultRequestTimeout
	}
	if c.concurrency <= 0 {
		c.concurrency = DefaultConcurrency
	}
	if c.ip != "" && !isIPv4(c.ip) {
		c.ipNote = fmt.Sprintf("调用方给出的地址 %q 不是 IPv4，改由接口发现", c.ip)
		c.ip = ""
	}
	return c
}

// Check runs the whole report. It never returns an error: every way of failing to read
// something is a note, which is what the operator wants to see anyway.
func (c *Checker) Check(ctx context.Context) Report {
	var rep Report
	ip, source, notes := c.discover(ctx)
	rep.IP, rep.IPSource, rep.IPNotes = ip, source, notes
	if ip == "" {
		c.base.Logf("ipquality: could not determine the public IPv4 address")
		return rep
	}
	c.base.Logf("ipquality: checking %s with %d databases and %d blocklists", ip, len(catalogue), len(zones))

	rep.Lookups = c.lookups(ctx, ip)
	rep.Basis = claimsOf(rep.Lookups)
	rep.Kind = verdict(rep.Basis)
	c.base.Logf("ipquality: line kind %s from %d claims", rep.Kind, len(rep.Basis))

	rep.PTR, rep.FCrDNS, rep.PTRNotes = c.reverse(ctx, ip)
	rep.Blocklists = c.blocklists(ctx, ip)
	if hits := countListed(rep.Blocklists); hits > 0 {
		c.base.Logf("ipquality: %d of %d blocklists answer for %s", hits, len(rep.Blocklists), ip)
	}
	return rep
}

// discover finds the address to check. A caller-supplied address wins; otherwise the
// plain-text endpoints are asked in order, and each one that fails leaves a note naming
// its reason. Nothing is asked about an address that was never determined.
func (c *Checker) discover(ctx context.Context) (ip, source string, notes []string) {
	if c.ip != "" {
		return c.ip, "调用方", nil
	}
	if c.ipNote != "" {
		notes = append(notes, c.ipNote)
	}
	for _, e := range ipEndpoints {
		addr, fail := c.fetchIP(ctx, e.url)
		if fail != nil {
			notes = append(notes, fmt.Sprintf("%s：查询失败（%s）", e.name, fail.Text()))
			continue
		}
		c.base.Logf("ipquality: public IPv4 %s (%s)", addr, e.name)
		return addr, e.name, notes
	}
	notes = append(notes, "无法确定本机公网 IPv4：所有发现接口都没有应答")
	return "", "", notes
}

// endpoint is one "what is my address" service. The names are listed rather than parsed
// out of the URL so a note reads the same as the table's 来源 column.
type endpoint struct{ name, url string }

// ipEndpoints are asked in order. Three independent services are here because one of
// them being down must not cost the whole tool its subject; all three answer bare text
// rather than JSON, so a tiny body cannot be mis-parsed.
var ipEndpoints = []endpoint{
	{name: "api.ipify.org", url: "https://api.ipify.org"},
	{name: "ifconfig.me", url: "https://ifconfig.me/ip"},
	{name: "checkip.amazonaws.com", url: "https://checkip.amazonaws.com"},
}

// fetchIP asks one endpoint and returns the address it reports.
func (c *Checker) fetchIP(ctx context.Context, url string) (string, *Failure) {
	body, f := c.get(ctx, url)
	if f != nil {
		return "", f
	}
	fields := strings.Fields(string(body))
	if len(fields) == 0 {
		return "", fail(FailParse, "响应为空")
	}
	if !isIPv4(fields[0]) {
		return "", fail(FailParse, "响应不是 IPv4 地址")
	}
	return fields[0], nil
}

// get performs one request and returns its body. A non-2xx status is a failure before
// the body is examined: a blocked or rate-limited endpoint often answers 200 with an
// HTML error page, and a 403 body is never worth parsing.
func (c *Checker) get(ctx context.Context, url string) ([]byte, *Failure) {
	rctx, cancel := context.WithTimeout(ctx, c.reqTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fail(FailTransport, err.Error())
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, transportFailure(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, transportFailure(err)
	}
	if f := statusFailure(resp.StatusCode); f != nil {
		return nil, f
	}
	return body, nil
}

// statusFailure turns a status code into a reason, or nil for a 2xx.
func statusFailure(code int) *Failure {
	switch {
	case code >= 200 && code < 300:
		return nil
	case code == http.StatusTooManyRequests:
		return fail(FailRateLimit, fmt.Sprintf("HTTP %d", code))
	case code == http.StatusForbidden:
		return fail(FailRefused, fmt.Sprintf("HTTP %d", code))
	}
	return fail(FailStatus, fmt.Sprintf("HTTP %d", code))
}

// transportFailure separates a timeout from any other transport error, because the two
// mean different things to an operator: a silent network versus a refused connection.
func transportFailure(err error) *Failure {
	if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
		return fail(FailTimeout, "")
	}
	return fail(FailTransport, err.Error())
}

// isTimeout reports whether err is a network timeout.
func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// isIPv4 reports whether s is an IPv4 literal. The tool is IPv4-only: the blocklist
// query form and the address the panel expects are both IPv4.
func isIPv4(s string) bool {
	ip := net.ParseIP(s)
	return ip != nil && ip.To4() != nil
}

// Report is one run: the address that was checked and everything that was said about it.
type Report struct {
	// IP is the public IPv4 the run is about. Empty means it could not be determined,
	// and then nothing was asked about anything.
	IP string
	// IPSource names where the address came from: the discovery endpoint that
	// answered, or 调用方 when the caller supplied it.
	IPSource string
	// IPNotes explain the discovery: which endpoints did not answer, or why a
	// caller-supplied address was rejected.
	IPNotes []string
	// Lookups is one entry per database, in catalogue order, failures included.
	Lookups []Lookup
	// Basis is every line-kind claim a database made, with the field it came from.
	Basis []Basis
	// Kind is the verdict folded from Basis.
	Kind Kind
	// PTR holds the address's reverse DNS names; empty when it has none.
	PTR []string
	// FCrDNS says a PTR name resolves forward back to the address.
	FCrDNS bool
	// PTRNotes explain the reverse lookup.
	PTRNotes []string
	// Blocklists is one entry per zone, in list order, hits and misses together.
	Blocklists []Blocklist
}

// Result renders the report as the panel's table.
func (r Report) Result() toolbox.Result {
	out := toolbox.Result{Headers: headerRow()}
	if r.IP == "" {
		// Nothing was queried, so there are no rows to show: the notes carry
		// the whole story.
		out.Notes = append(out.Notes, r.IPNotes...)
		out.Summary = "IP 质量：无法确定公网 IP"
		return out
	}
	// The subject comes first: every reading below is about this address, and a panel
	// that shows a table without naming it invites the reader to guess.
	out.Notes = append(out.Notes, fmt.Sprintf("检测地址：%s（来自 %s）", r.IP, r.IPSource))
	out.Notes = append(out.Notes, r.IPNotes...)
	for _, l := range r.Lookups {
		out.Rows = append(out.Rows, l.cells())
	}
	out.Notes = append(out.Notes, r.verdictNote(), r.thinNote())
	out.Notes = append(out.Notes, r.lookupNotes()...)
	out.Notes = append(out.Notes, r.PTRNotes...)
	out.Notes = append(out.Notes, r.blocklistNotes()...)
	out.Summary = r.summary()
	// The board's row already carries the entry's name, so its line starts at the verdict.
	out.Board = strings.TrimPrefix(r.summary(), "IP 质量：")
	return out
}

// headers labels the table columns. One row per database was the shape of the reference
// script too, so an operator can compare two panels line by line.
var headers = []string{"来源", "国家", "ASN", "ISP", "类型", "风险"}

// headerRow copies the column labels, so a caller that edits the result cannot corrupt
// the package's own table.
func headerRow() []string { return append([]string(nil), headers...) }

// summary is the one line for the toolbox board, e.g.
// "IP 质量：机房 · 3/12 黑名单命中 · ASN 64500".
func (r Report) summary() string {
	parts := []string{r.Kind.Label()}
	if total := len(r.Blocklists); total > 0 {
		parts = append(parts, fmt.Sprintf("%d/%d 黑名单命中", countListed(r.Blocklists), total))
	}
	if asn := r.firstASN(); asn != "" {
		parts = append(parts, "ASN "+asn)
	}
	return "IP 质量：" + strings.Join(parts, " · ")
}

// firstASN returns the number of the first ASN any database reported, in catalogue
// order, without the "AS" prefix. The summary has room for one, and the full table shows
// which database each one came from.
func (r Report) firstASN() string {
	for _, l := range r.Lookups {
		if l.ASN != "" {
			return strings.TrimPrefix(l.ASN, "AS")
		}
	}
	return ""
}

// countListed counts the blocklists that answered for the address.
func countListed(zones []Blocklist) int {
	n := 0
	for _, z := range zones {
		if z.Listed {
			n++
		}
	}
	return n
}

// lookupNotes reports the databases that did not deliver: one note per failure, and one
// per answer that arrived without a field it promised.
func (r Report) lookupNotes() []string {
	var out []string
	for _, l := range r.Lookups {
		switch {
		case l.Fail != nil:
			out = append(out, fmt.Sprintf("%s：查询失败（%s）", l.Source, l.Fail.Text()))
		case len(l.Missing) > 0:
			out = append(out, fmt.Sprintf("%s：字段缺失（%s）", l.Source, strings.Join(l.Missing, "、")))
		}
	}
	return out
}

// thinNote lists the databases whose keyless answer carries no line-kind or
// proxy/VPN/Tor field. Without it a dash in the table reads as "nothing found", when it
// really means "this database does not report that".
func (r Report) thinNote() string {
	var names []string
	for _, s := range catalogue {
		if !s.kind && !s.flags {
			names = append(names, s.name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return "未提供 IP 类型与代理标记字段的来源：" + strings.Join(names, "、") +
		"（免费无 key 接口不含这些字段，不代表已核查为安全）"
}

// verdictNote names who claimed what. A mixed verdict prints every claim instead of
// hiding the disagreement behind one word.
func (r Report) verdictNote() string {
	if len(r.Basis) == 0 {
		return "线路判定：未知（没有数据源报告 IP 类型）"
	}
	if r.Kind == KindMixed {
		parts := make([]string, 0, len(r.Basis))
		for _, b := range r.Basis {
			parts = append(parts, fmt.Sprintf("%s：%s（%s）", b.Source, b.Kind.Label(), b.Claim))
		}
		return "线路判定：不一致（" + strings.Join(parts, "；") + "）"
	}
	parts := make([]string, 0, len(r.Basis))
	for _, b := range r.Basis {
		parts = append(parts, fmt.Sprintf("%s 报告 %s", b.Source, b.Claim))
	}
	return fmt.Sprintf("线路判定：%s（%s）", r.Kind.Label(), strings.Join(parts, "；"))
}

// blocklistNotes lists the zones that answered for the address, and the ones that reached
// no verdict. A zone that resolved cleanly as NXDOMAIN is neither. Zones that failed for
// the same reason are folded into one line, because a resolver problem makes all of them
// fail at once and twelve identical sentences are not a report.
func (r Report) blocklistNotes() []string {
	var hits []string
	byReason := make(map[string][]string)
	var reasons []string
	for _, z := range r.Blocklists {
		switch {
		case z.Listed:
			hits = append(hits, fmt.Sprintf("%s(%s)", z.Zone, strings.Join(z.Answers, " ")))
		case z.Err != "":
			if _, seen := byReason[z.Err]; !seen {
				reasons = append(reasons, z.Err)
			}
			byReason[z.Err] = append(byReason[z.Err], z.Zone)
		}
	}
	var out []string
	if len(hits) == 0 {
		out = append(out, fmt.Sprintf("黑名单命中：无（已查询 %d 个域）", len(r.Blocklists)))
	} else {
		out = append(out, fmt.Sprintf("黑名单命中（%d/%d）：%s", len(hits), len(r.Blocklists), strings.Join(hits, "、")))
	}
	for _, reason := range reasons {
		zones := byReason[reason]
		if len(zones) == 1 {
			out = append(out, fmt.Sprintf("黑名单查询未决：%s（%s）", zones[0], reason))
			continue
		}
		out = append(out, fmt.Sprintf("黑名单查询未决（%d 个域，%s）：%s",
			len(zones), reason, strings.Join(zones, "、")))
	}
	return out
}

// lookups queries every database concurrently, one goroutine per entry, and returns the
// answers in catalogue order. A per-request timeout is applied inside, so a single
// silent endpoint costs at most RequestTimeout.
func (c *Checker) lookups(ctx context.Context, ip string) []Lookup {
	out := make([]Lookup, len(catalogue))
	sem := make(chan struct{}, c.concurrency)
	var wg sync.WaitGroup
	for i := range catalogue {
		wg.Add(1)
		go func(i int, s *source) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i] = c.lookup(ctx, s, ip)
		}(i, &catalogue[i])
	}
	wg.Wait()
	return out
}

// lookup asks one database. A panicking parser is contained here: a database that
// changes its shape must not take the panel down with it.
func (c *Checker) lookup(ctx context.Context, s *source, ip string) (out Lookup) {
	out = Lookup{Source: s.name}
	defer func() {
		if r := recover(); r != nil {
			out = Lookup{Source: s.name, Fail: fail(FailParse, fmt.Sprintf("解析器崩溃：%v", r))}
		}
	}()
	body, f := c.get(ctx, s.url(ip))
	if f != nil {
		return Lookup{Source: s.name, Fail: f}
	}
	out = s.parse(body)
	out.Source = s.name
	return out
}
