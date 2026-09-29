// Package unlock reports which well-known services the host's public IP can
// actually use.
//
// Whether ChatGPT, Netflix or 巴哈姆特動畫瘋 work from a VPS depends on where the
// egress IP is registered, not on anything the panel configures. Every probe
// here asks one service that question the way the reference script
// github.com/lmc999/RegionRestrictionCheck does: one to three small HTTP
// requests, then a read of the body. No login, no captcha solving, no extra
// data files.
//
// Two rules shape every probe:
//
//   - A probe never reports StatusUnlocked on partial evidence. Several of
//     these services answer 200 with a geo-block page, so each probe looks for
//     the marker that proves the service works and checks that the page really
//     came from the service; anything it cannot read becomes StatusFailed with
//     a reason, never a hopeful "unlocked".
//   - Every request goes through the injected Client, so unit tests drive the
//     parsers with recorded bodies and never touch the network.
package unlock

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// browserUA is the user agent the panel sends. Most of these services answer a
// bare Go client with a block page, so the probes dress as a desktop Chrome.
const browserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36"

// Groups, in the order the panel shows them. A service belongs to exactly one.
const (
	// GroupMultination covers the services with one global catalogue and
	// per-IP geofencing.
	GroupMultination = "multination"
	// GroupAI covers the assistants that refuse whole countries.
	GroupAI = "ai"
	// GroupGame covers game stores, whose regional signal is the currency.
	GroupGame = "game"
	// GroupChina covers services limited to mainland China.
	GroupChina = "china"
	// GroupTaiwan covers the Taiwan-only catalogue.
	GroupTaiwan = "taiwan"
)

// groupOrder fixes the row order of the panel page.
var groupOrder = []string{GroupMultination, GroupAI, GroupGame, GroupChina, GroupTaiwan}

// Status is a probe's verdict for one service.
type Status string

const (
	// StatusUnlocked means the probe proved the service works from this IP.
	StatusUnlocked Status = "unlocked"
	// StatusPartial means the service works but only in part: Netflix shows
	// originals only, a website answers while its app does not, and so on.
	StatusPartial Status = "partial"
	// StatusBlocked means the service answered and turned this IP away.
	StatusBlocked Status = "blocked"
	// StatusFailed means the probe reached no verdict. Every probe reports this
	// instead of StatusUnlocked when it cannot read the answer.
	StatusFailed Status = "failed"
)

// Reason codes explain a result that is not StatusUnlocked. The panel renders
// them through internal/i18n; Text is the untranslated fallback.
const (
	// ReasonNetwork means the request failed or timed out.
	ReasonNetwork = "network"
	// ReasonHTTP means the service answered with an unexpected status code.
	ReasonHTTP = "http_status"
	// ReasonBody means the response did not carry the marker the probe reads.
	ReasonBody = "unexpected_body"
	// ReasonRegion means the service answered without disclosing a region.
	ReasonRegion = "no_region"
	// ReasonUnknown means the response was readable but not conclusive.
	ReasonUnknown = "unknown"
)

// Service describes one probe in the catalogue. It is a descriptor: the panel
// renders ID, Name and Group and passes ID back to Detector.Check.
type Service struct {
	// ID is the stable key, also the internal/i18n key for the display name.
	ID string
	// Name is the English display name, and the translation fallback.
	Name string
	// Group is one of the Group* constants.
	Group string
}

// Result is one service's verdict.
type Result struct {
	Service string `json:"service"`
	Name    string `json:"name"`
	Group   string `json:"group"`
	Status  Status `json:"status"`
	// Region is the region the service disclosed, as reported by the service
	// itself (usually an ISO 3166-1 alpha-2 code). Steam reports the currency
	// of its store instead. Empty when the service disclosed none.
	Region string `json:"region,omitempty"`
	// Reason is the Reason* code for anything but an unlocked result.
	Reason string `json:"reason,omitempty"`
	// Text is a short English explanation, for the log and for a UI that has
	// no translation for Reason.
	Text string `json:"text,omitempty"`
	// Elapsed is how long the probe took.
	Elapsed time.Duration `json:"elapsed"`
}

// OK reports whether the panel should paint the row as usable.
func (r Result) OK() bool {
	return r.Status == StatusUnlocked || r.Status == StatusPartial
}

// Report is the outcome of a whole run.
type Report struct {
	Started time.Time
	Elapsed time.Duration
	Results []Result
}

// Count returns how many results carry the given status.
func (r Report) Count(s Status) int {
	n := 0
	for _, res := range r.Results {
		if res.Status == s {
			n++
		}
	}
	return n
}

// Client is the HTTP surface the probes use. *http.Client satisfies it; tests
// inject a stub so a run never leaves the process.
type Client interface {
	Do(*http.Request) (*http.Response, error)
}

// Options configures a Detector. The zero value is usable.
type Options struct {
	// Client performs every request. Nil selects a private *http.Client.
	Client Client
	// Timeout bounds one probe as a whole; a multi-step probe shares it between
	// its requests. Default 10s, what the reference script allows per curl call.
	Timeout time.Duration
	// Concurrency is how many probes run at once. Default 6.
	Concurrency int
	// UserAgent is sent with every request. Default a desktop Chrome string,
	// because these services block a bare Go client.
	UserAgent string
	// Progress receives one call per finished probe, so a caller can show how far a run of
	// several services has got. It is called from the probe goroutines and must not block.
	Progress func(done, total int, name string)
}

// Detector runs the probes.
type Detector struct {
	client      Client
	timeout     time.Duration
	concurrency int
	ua          string
	progress    func(done, total int, name string)
}

// New builds a Detector. A nil Options.Client falls back to a private
// *http.Client, which is what the panel uses.
func New(opts Options) *Detector {
	d := &Detector{
		client:      opts.Client,
		timeout:     opts.Timeout,
		concurrency: opts.Concurrency,
		ua:          opts.UserAgent,
		progress:    opts.Progress,
	}
	if d.client == nil {
		d.client = &http.Client{Timeout: 30 * time.Second}
	}
	if d.timeout <= 0 {
		d.timeout = 10 * time.Second
	}
	if d.concurrency <= 0 {
		d.concurrency = 6
	}
	if d.ua == "" {
		d.ua = browserUA
	}
	return d
}

// Check runs the named services concurrently and returns one result per
// service, in catalogue order. With no names it runs the whole catalogue.
// Names that are not in the catalogue are ignored.
func (d *Detector) Check(ctx context.Context, ids ...string) []Result {
	probes := selectProbes(ids)
	if len(probes) == 0 {
		return nil
	}
	results := make([]Result, len(probes))
	sem := make(chan struct{}, d.concurrency)
	var wg sync.WaitGroup
	// The probes run concurrently, so the count of finished probes is what the panel shows:
	// the number is exact and the order is whatever finished first.
	var finished atomic.Int64
	for i, p := range probes {
		wg.Add(1)
		go func(i int, p probe) {
			defer wg.Done()
			// Waiting for a slot is itself a reason to stop: a probe queued behind the
			// concurrency limit used to take its turn on a context that had already
			// been cancelled, so leaving the 服务解锁状态 page kept the run going to the
			// end instead of ending it.
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				results[i] = p.result(StatusFailed, "", ReasonUnknown, ctx.Err().Error())
				return
			}
			defer func() { <-sem }()
			results[i] = d.run(ctx, p)
			if d.progress != nil {
				d.progress(int(finished.Add(1)), len(probes), results[i].Name)
			}
		}(i, p)
	}
	wg.Wait()
	return results
}

// Report runs Check and wraps the results with the timings of the whole run.
func (d *Detector) Report(ctx context.Context, ids ...string) Report {
	start := time.Now()
	results := d.Check(ctx, ids...)
	return Report{Started: start, Elapsed: time.Since(start), Results: results}
}

// run executes one probe under its own timeout. It also contains a panicking
// probe: a broken parser must not take the panel down with it.
func (d *Detector) run(ctx context.Context, p probe) (res Result) {
	start := time.Now()
	cctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	defer func() {
		if r := recover(); r != nil {
			res = p.result(StatusFailed, "", ReasonUnknown, fmt.Sprintf("probe panicked: %v", r))
			res.Elapsed = time.Since(start)
		}
	}()
	res = p.run(cctx, d, p.Service)
	res.Elapsed = time.Since(start)
	return res
}

// Catalogue returns every probe in display order.
func Catalogue() []Service {
	out := make([]Service, 0, len(catalogue))
	for _, p := range catalogue {
		out = append(out, p.Service)
	}
	return out
}

// Groups returns the group identifiers in the order the panel shows them.
func Groups() []string {
	return append([]string(nil), groupOrder...)
}

// Lookup returns the service with the given ID.
func Lookup(id string) (Service, bool) {
	for _, p := range catalogue {
		if p.ID == id {
			return p.Service, true
		}
	}
	return Service{}, false
}

// result builds a Result for s. Reason is dropped for an unlocked result: there
// is nothing to explain.
func (s Service) result(status Status, region, reason, text string) Result {
	if status == StatusUnlocked {
		reason = ""
	}
	return Result{
		Service: s.ID,
		Name:    s.Name,
		Group:   s.Group,
		Status:  status,
		Region:  region,
		Reason:  reason,
		Text:    text,
	}
}

// probe pairs a catalogue entry with the function that checks it.
type probe struct {
	Service
	run func(context.Context, *Detector, Service) Result
}

// selectProbes returns the catalogue entries named by ids, in catalogue order,
// or the whole catalogue when ids is empty.
func selectProbes(ids []string) []probe {
	if len(ids) == 0 {
		return catalogue
	}
	out := make([]probe, 0, len(ids))
	for _, p := range catalogue {
		for _, id := range ids {
			if p.ID == id {
				out = append(out, p)
				break
			}
		}
	}
	return out
}

// request is one HTTP call a probe makes.
type request struct {
	method  string
	url     string
	headers map[string]string
	body    string

	// limit and markers replace the default capped read when a probe has to
	// follow a page further: the read stops at the limit or at the first marker,
	// whichever comes first. A request with no limit keeps the maxBody cap.
	limit   int64
	markers []string
}

// reply is a response with the body already read.
type reply struct {
	status   int
	header   http.Header
	body     string
	finalURL string
	// truncated says the body read stopped at the cap rather than at the end of
	// the document, so a missing marker means "not read" instead of "not there".
	truncated bool
}

// ok reports a 2xx status.
func (r reply) ok() bool { return r.status >= 200 && r.status < 300 }

// has reports whether the body carries the exact marker.
func (r reply) has(marker string) bool { return strings.Contains(r.body, marker) }

// hasFold is has, case-insensitively.
func (r reply) hasFold(marker string) bool {
	return strings.Contains(strings.ToLower(r.body), strings.ToLower(marker))
}

// statusText is the short form of the status used in a result's Text.
func (r reply) statusText() string { return fmt.Sprintf("HTTP %d", r.status) }

// maxBody caps how much of a response a probe keeps, so one broken or hostile
// page cannot fill memory on a small VPS. The cap is set by the deepest marker
// any probe reads: Netflix's region sits about 700 KB into a title page, so a
// new marker belongs below this line and near the top of the document.
const maxBody = 1 << 20

// deepBody is the cap for the one probe that has to follow a page further than
// that: the Prime Video storefront has served a variant whose geo block sits
// past a megabyte, and cutting the read there turned a served country into a
// failed check. The probe names the markers it is looking for, so the extra room
// costs nothing when the marker arrives early — which it did in every reading of
// the current variant, about 170 KB in.
const deepBody = 8 << 20

// readUntil reads at most limit bytes, stopping as soon as one of the markers
// has arrived. Markers are matched against a window that overlaps the previous
// chunk, so a marker split across a read boundary is still found. The second
// result says the read stopped because it ran into the limit rather than because
// the document ended or a marker arrived, which is what a failure text needs to
// tell "not there" from "not read".
func readUntil(r io.Reader, limit int64, markers []string) (string, bool, error) {
	var (
		buf     []byte
		chunk   = make([]byte, 32<<10)
		overlap int
	)
	for _, m := range markers {
		if len(m) > overlap {
			overlap = len(m)
		}
	}
	for int64(len(buf)) < limit {
		n, err := r.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
			window := buf
			if len(window) > n+overlap {
				window = window[len(window)-(n+overlap):]
			}
			for _, m := range markers {
				if bytes.Contains(window, []byte(m)) {
					return string(buf), false, nil
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return string(buf), false, err
		}
	}
	return string(buf), int64(len(buf)) >= limit, nil
}

func (d *Detector) do(ctx context.Context, r request) (reply, error) {
	method := r.method
	if method == "" {
		method = http.MethodGet
	}
	var body io.Reader
	if r.body != "" {
		body = strings.NewReader(r.body)
	}
	req, err := http.NewRequestWithContext(ctx, method, r.url, body)
	if err != nil {
		return reply{}, err
	}
	req.Header.Set("User-Agent", d.ua)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	for k, v := range r.headers {
		req.Header.Set(k, v)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return reply{}, err
	}
	if resp.Body == nil {
		return reply{status: resp.StatusCode, header: resp.Header, finalURL: r.url}, nil
	}
	defer resp.Body.Close()
	var data []byte
	var truncated bool
	if r.limit > 0 {
		text, cut, err := readUntil(resp.Body, r.limit, r.markers)
		if err != nil {
			return reply{}, err
		}
		data, truncated = []byte(text), cut
	} else {
		data, err = io.ReadAll(io.LimitReader(resp.Body, maxBody))
		if err != nil {
			return reply{}, err
		}
		truncated = len(data) >= maxBody
	}
	out := reply{
		status:    resp.StatusCode,
		header:    resp.Header,
		body:      string(data),
		finalURL:  r.url,
		truncated: truncated,
	}
	// A client that follows redirects reports the last hop here; a stub that
	// does not leaves it at the requested URL.
	if resp.Request != nil && resp.Request.URL != nil {
		out.finalURL = resp.Request.URL.String()
	}
	return out, nil
}

func (d *Detector) get(ctx context.Context, url string, headers map[string]string) (reply, error) {
	return d.do(ctx, request{method: http.MethodGet, url: url, headers: headers})
}

// getDeep reads a page past the default cap, stopping at the first marker it was
// told to look for. It is for the one probe that has to follow a storefront to
// its geo block; everything else stays on the capped read.
func (d *Detector) getDeep(ctx context.Context, url string, headers map[string]string, limit int64, markers ...string) (reply, error) {
	return d.do(ctx, request{method: http.MethodGet, url: url, headers: headers, limit: limit, markers: markers})
}

func (d *Detector) post(ctx context.Context, url, contentType, body string, headers map[string]string) (reply, error) {
	h := make(map[string]string, len(headers)+1)
	for k, v := range headers {
		h[k] = v
	}
	if contentType != "" {
		h["Content-Type"] = contentType
	}
	return d.do(ctx, request{method: http.MethodPost, url: url, headers: h, body: body})
}

// firstGroup returns the first capture group of re in s, or "" when re does not
// match.
func firstGroup(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// allGroups returns the first capture group of every match of re in s.
func allGroups(re *regexp.Regexp, s string) []string {
	var out []string
	for _, m := range re.FindAllStringSubmatch(s, -1) {
		if len(m) > 1 {
			out = append(out, m[1])
		}
	}
	return out
}

// cookieHeader renders the cookies a response set, so a probe can carry them
// into its follow-up requests the way curl's cookie jar does. The injected
// Client interface has no jar, so a probe that needs one threads it by hand.
func cookieHeader(h http.Header) string {
	var parts []string
	for _, c := range h.Values("Set-Cookie") {
		name, rest, ok := strings.Cut(c, "=")
		if !ok {
			continue
		}
		value, _, _ := strings.Cut(rest, ";")
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		parts = append(parts, name+"="+strings.TrimSpace(value))
	}
	return strings.Join(parts, "; ")
}
