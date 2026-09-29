package unlock

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// route is one canned answer in a fake client's table.
type route struct {
	method string // empty matches any method
	url    string // matched as a prefix
	status int
	body   string
	// seq replaces body per call: the first call gets seq[0], the second seq[1], and
	// the last entry repeats for every call after it. A probe that asks again is
	// tested with this.
	seq    []string
	header http.Header
	final  string // the URL the client "landed on" after redirects
	err    error
}

// fakeClient answers from a fixed routing table. An unrouted request is
// reported as a test failure, so a probe that starts talking to a new host is
// noticed instead of silently going to the network.
type fakeClient struct {
	t      *testing.T
	routes []route

	mu    sync.Mutex
	seen  []string
	calls map[int]int
}

func (c *fakeClient) Do(req *http.Request) (*http.Response, error) {
	target := req.URL.String()
	c.mu.Lock()
	c.seen = append(c.seen, req.Method+" "+target)
	c.mu.Unlock()

	for idx, r := range c.routes {
		if r.method != "" && r.method != req.Method {
			continue
		}
		if !strings.HasPrefix(target, r.url) {
			continue
		}
		if r.err != nil {
			return nil, r.err
		}
		body := r.body
		if len(r.seq) > 0 {
			c.mu.Lock()
			if c.calls == nil {
				c.calls = make(map[int]int)
			}
			n := c.calls[idx]
			c.calls[idx]++
			c.mu.Unlock()
			if n >= len(r.seq) {
				n = len(r.seq) - 1
			}
			body = r.seq[n]
		}
		final := r.final
		if final == "" {
			final = target
		}
		u, err := url.Parse(final)
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: r.status,
			Header:     r.header,
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    &http.Request{Method: req.Method, URL: u, Header: req.Header},
		}, nil
	}
	c.t.Errorf("unrouted request: %s %s", req.Method, target)
	return nil, fmt.Errorf("no route for %s %s", req.Method, target)
}

// requests returns the requests the fake client answered, in order.
func (c *fakeClient) requests() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.seen...)
}

// runProbe runs one catalogue entry against a fake client.
func runProbe(t *testing.T, id string, routes ...route) Result {
	t.Helper()
	probes := selectProbes([]string{id})
	if len(probes) != 1 {
		t.Fatalf("service %q is not in the catalogue", id)
	}
	d := New(Options{Client: &fakeClient{t: t, routes: routes}, Timeout: 5 * time.Second})
	return d.run(context.Background(), probes[0])
}

// alwaysClient answers every request with the same status and body.
type alwaysClient struct {
	status int
	body   string
}

func (c alwaysClient) Do(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: c.status,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(c.body)),
		Request:    &http.Request{Method: req.Method, URL: req.URL, Header: req.Header},
	}, nil
}

// errorClient fails every request at the transport level.
type errorClient struct{ err error }

func (c errorClient) Do(*http.Request) (*http.Response, error) { return nil, c.err }

// blockingClient waits for the probe's context to expire, which is what a
// black-holed host looks like.
type blockingClient struct{}

func (blockingClient) Do(req *http.Request) (*http.Response, error) {
	<-req.Context().Done()
	return nil, req.Context().Err()
}

func TestCatalogueIsWellFormed(t *testing.T) {
	services := Catalogue()
	if len(services) == 0 {
		t.Fatal("the catalogue is empty")
	}
	if len(services) != len(catalogue) {
		t.Fatalf("Catalogue returned %d services, the table holds %d", len(services), len(catalogue))
	}

	groups := Groups()
	position := make(map[string]int, len(groups))
	for i, g := range groups {
		position[g] = i
	}

	seen := make(map[string]bool, len(services))
	lastGroup := -1
	for _, s := range services {
		if s.ID == "" || s.Name == "" {
			t.Errorf("service %+v has an empty ID or Name", s)
		}
		if seen[s.ID] {
			t.Errorf("duplicate service ID %q", s.ID)
		}
		seen[s.ID] = true

		at, ok := position[s.Group]
		if !ok {
			t.Errorf("service %q has unknown group %q", s.ID, s.Group)
			continue
		}
		if at < lastGroup {
			t.Errorf("service %q breaks the group order: %q comes after a later group", s.ID, s.Group)
		}
		lastGroup = at

		if got, ok := Lookup(s.ID); !ok || got != s {
			t.Errorf("Lookup(%q) = %+v, %v; want %+v", s.ID, got, ok, s)
		}
	}
	if _, ok := Lookup("no-such-service"); ok {
		t.Error("Lookup found a service that is not in the catalogue")
	}
}

func TestCheckOrderAndFilter(t *testing.T) {
	d := New(Options{Client: alwaysClient{status: 200}})

	// Requested out of order: the results still follow the catalogue.
	results := d.Check(context.Background(), "steam", "netflix")
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if results[0].Service != "netflix" || results[1].Service != "steam" {
		t.Errorf("results came back as %q, %q; want catalogue order", results[0].Service, results[1].Service)
	}

	// Duplicates collapse, unknown names are ignored.
	results = d.Check(context.Background(), "steam", "steam", "nope")
	if len(results) != 1 || results[0].Service != "steam" {
		t.Errorf("got %+v, want the single steam result", results)
	}

	if got := d.Check(context.Background(), "nope"); got != nil {
		t.Errorf("Check with no known names returned %+v, want nil", got)
	}
}

func TestCheckFillsDescriptorFields(t *testing.T) {
	d := New(Options{Client: alwaysClient{status: 200}})
	for _, r := range d.Check(context.Background()) {
		want, ok := Lookup(r.Service)
		if !ok {
			t.Fatalf("result names unknown service %q", r.Service)
		}
		if r.Name != want.Name || r.Group != want.Group {
			t.Errorf("result %q carries %q/%q, want %q/%q", r.Service, r.Name, r.Group, want.Name, want.Group)
		}
		if r.Elapsed < 0 {
			t.Errorf("result %q reports a negative duration", r.Service)
		}
	}
}

// TestInconclusiveNeverUnlocks is the contract that matters most: a host that
// cannot reach anything, answers everything with an empty page, or only ever
// times out must never produce a usable verdict.
func TestInconclusiveNeverUnlocks(t *testing.T) {
	cases := []struct {
		name   string
		client Client
		opts   Options
	}{
		{name: "transport error", client: errorClient{err: errors.New("connection refused")}},
		{name: "empty 200", client: alwaysClient{status: 200}},
		{name: "error status", client: alwaysClient{status: 503, body: "upstream is down"}},
		{name: "timeout", client: blockingClient{}, opts: Options{Timeout: 30 * time.Millisecond}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := tc.opts
			opts.Client = tc.client
			results := New(opts).Check(context.Background())
			if len(results) != len(catalogue) {
				t.Fatalf("got %d results, want one per catalogue entry (%d)", len(results), len(catalogue))
			}
			for _, r := range results {
				if r.OK() {
					t.Errorf("%s reported %q on an inconclusive run", r.Service, r.Status)
				}
				if r.Status != StatusFailed {
					t.Errorf("%s reported %q, want %q", r.Service, r.Status, StatusFailed)
				}
				if r.Reason == "" {
					t.Errorf("%s reported a failure with no reason", r.Service)
				}
				if r.Text == "" {
					t.Errorf("%s reported a failure with no text", r.Service)
				}
			}
		})
	}
}

func TestRunRecoversFromPanic(t *testing.T) {
	d := New(Options{Client: alwaysClient{status: 200}})
	res := d.run(context.Background(), probe{
		Service: Service{ID: "boom", Name: "Boom", Group: GroupAI},
		run:     func(context.Context, *Detector, Service) Result { panic("kaboom") },
	})
	if res.Status != StatusFailed {
		t.Errorf("a panicking probe reported %q, want %q", res.Status, StatusFailed)
	}
	if !strings.Contains(res.Text, "kaboom") {
		t.Errorf("the panic was not reported in Text: %q", res.Text)
	}
}

func TestReport(t *testing.T) {
	d := New(Options{Client: alwaysClient{status: 200}})
	rep := d.Report(context.Background(), "steam", "reddit")
	if len(rep.Results) != 2 {
		t.Fatalf("got %d results, want 2", len(rep.Results))
	}
	if rep.Started.IsZero() {
		t.Error("Report did not record its start")
	}
	if rep.Count(StatusFailed) != 2 || rep.Count(StatusUnlocked) != 0 {
		t.Errorf("counts are %d failed / %d unlocked, want 2/0",
			rep.Count(StatusFailed), rep.Count(StatusUnlocked))
	}
}

func TestResultOK(t *testing.T) {
	cases := map[Status]bool{
		StatusUnlocked: true,
		StatusPartial:  true,
		StatusBlocked:  false,
		StatusFailed:   false,
	}
	for status, want := range cases {
		if got := (Result{Status: status}).OK(); got != want {
			t.Errorf("Result{Status: %q}.OK() = %v, want %v", status, got, want)
		}
	}
}

func TestUnlockedDropsReason(t *testing.T) {
	s := Service{ID: "x", Name: "X", Group: GroupAI}
	if got := s.result(StatusUnlocked, "US", ReasonBody, "ignored"); got.Reason != "" {
		t.Errorf("an unlocked result kept reason %q", got.Reason)
	}
	if got := s.result(StatusFailed, "", ReasonBody, "text"); got.Reason != ReasonBody {
		t.Errorf("a failed result lost its reason: %+v", got)
	}
}

func TestReplyHelpers(t *testing.T) {
	r := reply{status: 200, body: "Hello WORLD"}
	if !r.ok() {
		t.Error("200 is not ok")
	}
	if !r.has("Hello") || r.has("hello") {
		t.Error("has is not case-sensitive")
	}
	if !r.hasFold("hello world") {
		t.Error("hasFold is not case-insensitive")
	}
	if got := r.statusText(); got != "HTTP 200" {
		t.Errorf("statusText = %q", got)
	}
	if (reply{status: 302}).ok() {
		t.Error("302 counted as ok")
	}
}

func TestCookieHeader(t *testing.T) {
	h := http.Header{}
	h.Add("Set-Cookie", "BAHAID=abc123; Path=/; HttpOnly")
	h.Add("Set-Cookie", "other=x")
	if got, want := cookieHeader(h), "BAHAID=abc123; other=x"; got != want {
		t.Errorf("cookieHeader = %q, want %q", got, want)
	}
	if got := cookieHeader(http.Header{}); got != "" {
		t.Errorf("cookieHeader on no cookies = %q", got)
	}
}

func TestFirstGroup(t *testing.T) {
	re := netflixID
	cases := []struct {
		in   string
		want string
	}{
		{`{"id":"SG"}`, "SG"},
		{`{"id": "SG"}`, ""},
		{`nothing here`, ""},
	}
	for _, tc := range cases {
		if got := firstGroup(re, tc.in); got != tc.want {
			t.Errorf("firstGroup(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNetflixRegion(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "requestCountry",
			body: `{"requestCountry":{"id":"sg"}}`,
			want: "SG",
		},
		{
			name: "requestCountry inside an escaped JavaScript string",
			body: `{"x":"\"requestCountry\":{\"supportedLocales\":[\"en\",\"es\"],\"id\":\"us\",\"countryName\":\"United\\x20States\"}"}`,
			want: "US",
		},
		{
			name: "id before countryName wins",
			body: `{"id":"US","x":1},{"id":"SG","countryName":"Singapore"}`,
			want: "SG",
		},
		{
			name: "no country name",
			body: `{"id":"SG"}`,
			want: "",
		},
		{
			name: "nothing at all",
			body: "<html>Oh no!</html>",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := netflixRegion(tc.body); got != tc.want {
				t.Errorf("netflixRegion(%q) = %q, want %q", tc.body, got, tc.want)
			}
		})
	}
}

// gateClient holds every request until release is closed, and counts the calls that got
// through. A test uses it to tell how many probes actually started: without the count, a
// run that stopped early and a run that never started look the same.
type gateClient struct {
	release chan struct{}
	started chan struct{}
	once    sync.Once
	calls   atomic.Int64
}

func (c *gateClient) Do(req *http.Request) (*http.Response, error) {
	c.calls.Add(1)
	c.once.Do(func() { close(c.started) })
	<-c.release
	return &http.Response{
		StatusCode: http.StatusForbidden,
		Status:     "403 Forbidden",
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    req,
	}, nil
}

// TestCheckStopsQueuedProbesWhenCancelled covers the wait for a concurrency slot. A probe
// queued behind the limit used to take its turn on a context that was already cancelled,
// so leaving the 服务解锁状态 page kept the run going to the end instead of ending it.
//
// The shape makes it deterministic rather than timing dependent: the one probe that holds
// the slot is parked inside its request, so no queued probe can acquire the slot until
// after the context has been cancelled.
func TestCheckStopsQueuedProbesWhenCancelled(t *testing.T) {
	probes := Catalogue()
	if len(probes) < 2 {
		t.Fatalf("the catalogue holds %d services; this test needs more than one", len(probes))
	}
	client := &gateClient{release: make(chan struct{}), started: make(chan struct{})}
	d := New(Options{Client: client, Concurrency: 1, Timeout: 5 * time.Second})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		d.Check(ctx)
		close(done)
	}()

	<-client.started // one probe is in flight, the rest are queued
	cancel()
	close(client.release)

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Check did not return after the context was cancelled")
	}
	if got := client.calls.Load(); got >= int64(len(probes)) {
		t.Fatalf("%d requests for %d probes: the queued probes ran on a cancelled context", got, len(probes))
	}
}
