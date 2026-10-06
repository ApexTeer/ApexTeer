package ipquality

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EasySBTeam/EasySB/internal/toolbox"
)

// testIP is the address every canned answer is about: an RFC 5737 documentation address,
// so a canned body can never be mistaken for a real reading.
const testIP = "203.0.113.7"

// testRevIP is testIP in the reversed form the blocklists and in-addr.arpa use.
const testRevIP = "7.113.0.203"

// route is one canned HTTP answer in a fake client's table.
type route struct {
	// url is matched as a prefix, longest-first by table order.
	url    string
	status int
	body   string
	err    error
	// block makes the answer wait for the request context, which is what a
	// black-holed endpoint looks like.
	block bool
}

// fakeClient answers from a fixed table. An unrouted request fails the test, so a
// database that starts talking to a new host is noticed instead of silently reaching the
// network.
type fakeClient struct {
	t      *testing.T
	routes []route

	mu   sync.Mutex
	seen []string
}

func (c *fakeClient) Do(req *http.Request) (*http.Response, error) {
	target := req.URL.String()
	c.mu.Lock()
	c.seen = append(c.seen, target)
	c.mu.Unlock()

	for _, r := range c.routes {
		if !strings.HasPrefix(target, r.url) {
			continue
		}
		if r.err != nil {
			return nil, r.err
		}
		if r.block {
			<-req.Context().Done()
			return nil, req.Context().Err()
		}
		status := r.status
		if status == 0 {
			status = http.StatusOK
		}
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader(r.body)),
			Request:    req,
		}, nil
	}
	c.t.Errorf("unrouted request: %s", target)
	return nil, fmt.Errorf("no route for %s", target)
}

// requests returns the URLs the fake client answered, in order.
func (c *fakeClient) requests() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.seen...)
}

// asked reports whether any request URL starts with prefix.
func (c *fakeClient) asked(prefix string) bool {
	for _, r := range c.requests() {
		if strings.HasPrefix(r, prefix) {
			return true
		}
	}
	return false
}

// fakeResolver answers from fixed tables: host names it knows, host names it fails on, and
// the PTR names of addresses. Names and addresses it does not know are NXDOMAIN, which is
// exactly what a blocklist answers for an address it does not list, so an unlisted zone
// needs no entry at all.
type fakeResolver struct {
	hosts   map[string][]string
	hostErr map[string]error
	// ptrs is keyed by the address, which is what a PTR query is asked about.
	ptrs   map[string][]string
	ptrErr map[string]error

	mu      sync.Mutex
	seen    []string
	ptrSeen []string
}

func (r *fakeResolver) LookupHost(_ context.Context, host string) ([]string, error) {
	r.mu.Lock()
	r.seen = append(r.seen, host)
	r.mu.Unlock()
	if err, ok := r.hostErr[host]; ok {
		return nil, err
	}
	if addrs, ok := r.hosts[host]; ok {
		return addrs, nil
	}
	return nil, notFoundErr(host)
}

func (r *fakeResolver) LookupAddr(_ context.Context, addr string) ([]string, error) {
	r.mu.Lock()
	r.ptrSeen = append(r.ptrSeen, addr)
	r.mu.Unlock()
	if err, ok := r.ptrErr[addr]; ok {
		return nil, err
	}
	if names, ok := r.ptrs[addr]; ok {
		return names, nil
	}
	return nil, notFoundErr(addr + ".in-addr.arpa")
}

// queries returns the names the fake resolver was asked, in order.
func (r *fakeResolver) queries() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.seen...)
}

// ptrQueries returns the addresses the fake resolver was asked to reverse, in order.
func (r *fakeResolver) ptrQueries() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ptrSeen...)
}

// count returns how often one name was asked, which is how a test notices a query that
// ran twice.
func (r *fakeResolver) count(name string) int {
	n := 0
	for _, q := range r.queries() {
		if q == name {
			n++
		}
	}
	return n
}

// countPTR returns how often one address was reverse-resolved.
func (r *fakeResolver) countPTR(addr string) int {
	n := 0
	for _, q := range r.ptrQueries() {
		if q == addr {
			n++
		}
	}
	return n
}

// notFoundErr is the NXDOMAIN a resolver reports for a name that does not exist.
func notFoundErr(host string) error {
	return &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

// timeoutErr is the failure a resolver reports for a query that never got an answer.
func timeoutErr(host string) error {
	return &net.DNSError{Err: "i/o timeout", Name: host, IsTimeout: true}
}

// logSink collects the progress lines, so a test can assert a run says where it is
// without depending on the panel's logger.
type logSink struct {
	mu    sync.Mutex
	lines []string
}

func (l *logSink) log(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, line)
}

func (l *logSink) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

// canned holds one realistic answer per database, all describing the same datacenter
// address. The shapes are the ones the live endpoints return, so a parser that stops
// reading them shows up here.
var canned = map[string]string{
	"ip-api.com": `{"status":"success","country":"United States","countryCode":"US",` +
		`"city":"Los Angeles","isp":"Example Hosting LLC","org":"Example Hosting LLC",` +
		`"as":"AS64500 Example Hosting LLC","asname":"EXAMPLE","proxy":false,"hosting":true,` +
		`"mobile":false,"query":"203.0.113.7"}`,

	"ipinfo.io": `{"input":"203.0.113.7","data":{"ip":"203.0.113.7","city":"Los Angeles",` +
		`"region":"California","country":"US","org":"AS64500 Example Hosting LLC",` +
		`"asn":{"asn":"AS64500","name":"Example Hosting LLC","type":"hosting"},` +
		`"company":{"name":"Example Hosting LLC","type":"hosting"},` +
		`"privacy":{"vpn":false,"proxy":false,"tor":false,"relay":false,"hosting":true,` +
		`"service":"","provider":null},"is_anycast":false,"is_mobile":false,` +
		`"is_anonymous":false,"is_hosting":true}}`,

	"ipapi.is": `{"ip":"203.0.113.7","is_bogon":false,"company":"Example Hosting LLC",` +
		`"asn":"AS64500 Example Hosting LLC","city":"Los Angeles","region":"California",` +
		`"country":"United States","lat":34.05,"lon":-118.24,` +
		`"timezone":"America/Los_Angeles","docs":"https://ipapi.is/free-tier.html"}`,

	"ipwho.is": `{"ip":"203.0.113.7","success":true,"type":"IPv4","country":"United States",` +
		`"country_code":"US","region":"California","city":"Los Angeles",` +
		`"connection":{"asn":64500,"org":"Example Hosting LLC","isp":"Example Hosting LLC",` +
		`"domain":"example.net"}}`,

	"ip.sb": `{"region":"California","organization":"Example Hosting LLC",` +
		`"isp":"Example Hosting LLC","city":"Los Angeles","asn_organization":"Example Hosting LLC",` +
		`"asn":64500,"ip":"203.0.113.7","country":"United States","country_code":"US"}`,

	"ip2location.io": `{"ip":"203.0.113.7","country_code":"US","country_name":"United States of America",` +
		`"region_name":"California","city_name":"Los Angeles","asn":"64500","as":"Example Hosting LLC",` +
		`"is_proxy":false,"message":"Limit to 1,000 queries per day."}`,

	"ipwhois.app": `{"ip":"203.0.113.7","success":true,"country_code":"US","city":"Los Angeles",` +
		`"asn":"AS64500","org":"Example Hosting LLC","isp":"Example Hosting LLC"}`,

	"db-ip.com": `{"ipAddress":"203.0.113.7","continentCode":"NA","continentName":"North America",` +
		`"countryCode":"US","countryName":"United States","stateProvCode":"CA",` +
		`"stateProv":"California","city":"Los Angeles"}`,

	"ipapi.co": `{"ip":"203.0.113.7","city":"Los Angeles","region":"California","country_code":"US",` +
		`"country_name":"United States","asn":"AS64500","org":"Example Hosting LLC"}`,
}

// routes builds the canned table: the caller's overrides first, so an override always wins
// its prefix, then the discovery endpoints and every database in the catalogue.
func routes(t *testing.T, overrides ...route) []route {
	t.Helper()
	out := append([]route(nil), overrides...)
	out = append(out,
		route{url: "https://api.ipify.org", body: testIP},
		route{url: "https://ifconfig.me/ip", body: testIP},
		route{url: "https://checkip.amazonaws.com", body: testIP},
	)
	for _, s := range catalogue {
		// The prefix is the URL up to the address, so a route answers for any
		// address and can never drift from the URL the parser actually asks for.
		prefix, _, ok := strings.Cut(s.url(testIP), testIP)
		if !ok {
			t.Fatalf("database %q does not build a URL carrying the address", s.name)
		}
		out = append(out, route{url: prefix, body: canned[s.name]})
	}
	return out
}

// healthyResolver answers the blocklists and reverse DNS for the canned address: three
// zones list it, one refuses to answer for this resolver, one times out, the rest are
// clean misses (they are absent, so they answer NXDOMAIN).
func healthyResolver() *fakeResolver {
	return &fakeResolver{
		hosts: map[string][]string{
			"mail.example.net":                    {testIP},
			testRevIP + ".zen.spamhaus.org":       {"127.0.0.2"},
			testRevIP + ".bl.spamcop.net":         {"127.0.0.2"},
			testRevIP + ".dnsbl.dronebl.org":      {"127.0.0.8"},
			testRevIP + ".b.barracudacentral.org": {"127.255.255.254"},
		},
		ptrs: map[string][]string{
			testIP: {"mail.example.net"},
		},
		hostErr: map[string]error{
			testRevIP + ".dnsbl-2.uceprotect.net": timeoutErr(testRevIP + ".dnsbl-2.uceprotect.net"),
		},
	}
}

// newTestChecker wires a checker on the fake client and resolver. The per-request timeout
// is short so a deliberately blocking route cannot slow the suite.
func newTestChecker(t *testing.T, client *fakeClient, resolver Resolver, ip string) *Checker {
	t.Helper()
	return New(Options{
		Options:        toolbox.Options{Client: client, Timeout: 10 * time.Second, Log: (&logSink{}).log},
		Resolver:       resolver,
		IP:             ip,
		RequestTimeout: time.Second,
	})
}

// rowOf returns the table row of one database, failing the test when there is none.
func rowOf(t *testing.T, res toolbox.Result, source string) []string {
	t.Helper()
	for _, row := range res.Rows {
		if len(row) > 0 && row[0] == source {
			return row
		}
	}
	t.Fatalf("no row for %q in %v", source, res.Rows)
	return nil
}

// noteContains reports whether any note carries the substring.
func noteContains(res toolbox.Result, want string) bool {
	for _, n := range res.Notes {
		if strings.Contains(n, want) {
			return true
		}
	}
	return false
}

// noteWith returns the first note carrying the substring, or an empty string.
func noteWith(res toolbox.Result, want string) string {
	for _, n := range res.Notes {
		if strings.Contains(n, want) {
			return n
		}
	}
	return ""
}
