package ipquality

import (
	"context"
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/EasySBTeam/EasySB/internal/toolbox"
)

func TestZonesList(t *testing.T) {
	got := Zones()
	if len(got) < 10 {
		t.Fatalf("the list holds %d zones, want at least 10", len(got))
	}
	if len(got) != len(zones) {
		t.Errorf("Zones returned %d entries, the table holds %d", len(got), len(zones))
	}
	seen := make(map[string]bool, len(got))
	for _, z := range got {
		if z == "" || !strings.Contains(z, ".") || z != strings.ToLower(z) {
			t.Errorf("zone %q is not a lowercase domain name", z)
		}
		if seen[z] {
			t.Errorf("zone %q is listed twice", z)
		}
		seen[z] = true
	}
	for _, want := range []string{"zen.spamhaus.org", "b.barracudacentral.org", "bl.spamcop.net", "cbl.anti-spam.org.cn"} {
		if !seen[want] {
			t.Errorf("zone %q is missing from the list", want)
		}
	}
	// The caller gets a copy: a panel that sorts the list must not reorder the tool's.
	got[0] = "changed"
	if Zones()[0] == "changed" {
		t.Error("Zones returned the package's own slice")
	}
}

func TestReverseIP(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"203.0.113.7", "7.113.0.203"},
		{"1.2.3.4", "4.3.2.1"},
		{"1.2.3", ""},
		{"1.2.3.4.5", ""},
		{"a.b.c.d", ""},
		{"1.2.3.", ""},
		{"1234.2.3.4", ""},
		{"", ""},
		{"2001:db8::1", ""},
	}
	for _, tc := range cases {
		if got := reverseIP(tc.in); got != tc.want {
			t.Errorf("reverseIP(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestQueryZone covers every way a blocklist can answer: a listing, a clean miss, and the
// three failures that must not be reported as either.
func TestQueryZone(t *testing.T) {
	const zone, name = "zen.spamhaus.org", testRevIP + ".zen.spamhaus.org"
	cases := []struct {
		name    string
		hosts   map[string][]string
		errs    map[string]error
		listed  bool
		answers []string
		errHas  string
	}{
		{
			name:    "listed",
			hosts:   map[string][]string{name: {"127.0.0.2"}},
			listed:  true,
			answers: []string{"127.0.0.2"},
		},
		{
			name: "a clean miss is NXDOMAIN, which the fake resolver answers by default",
		},
		{
			name:   "the zone refuses to answer for this resolver",
			hosts:  map[string][]string{name: {"127.255.255.254"}},
			errHas: "服务拒绝",
		},
		{
			name:   "the refusal can arrive beside other codes",
			hosts:  map[string][]string{name: {"127.0.0.2", "127.255.255.255"}},
			errHas: "服务拒绝",
		},
		{
			// This is the trap a live run fell into on a host whose resolver
			// answers every name: twelve zones, twelve "hits", none of them true.
			name:   "a hijacked answer outside 127.0.0.0/8 is not a listing",
			hosts:  map[string][]string{name: {"198.18.1.21"}},
			errHas: "非 127.0.0.0/8",
		},
		{
			name:    "a listing beside a hijacked answer is still a listing",
			hosts:   map[string][]string{name: {"198.18.1.21", "127.0.0.2"}},
			listed:  true,
			answers: []string{"127.0.0.2"},
		},
		{
			name:   "an unparsable answer is not a listing either",
			hosts:  map[string][]string{name: {"not-an-address"}},
			errHas: "非 127.0.0.0/8",
		},
		{
			name:   "the query timed out",
			errs:   map[string]error{name: timeoutErr(name)},
			errHas: "查询超时",
		},
		{
			name:   "the resolver failed",
			errs:   map[string]error{name: &net.DNSError{Err: "server misbehaving", Name: name}},
			errHas: "server misbehaving",
		},
		{
			name:   "the resolver answered without an address",
			hosts:  map[string][]string{name: {}},
			errHas: "没有返回地址",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &fakeResolver{hosts: tc.hosts, hostErr: tc.errs}
			c := New(Options{Resolver: r, RequestTimeout: time.Second})
			got := c.queryZone(context.Background(), zone, name)
			if got.Zone != zone {
				t.Errorf("zone = %q, want %q", got.Zone, zone)
			}
			if got.Listed != tc.listed {
				t.Errorf("listed = %v, want %v", got.Listed, tc.listed)
			}
			if !reflect.DeepEqual(got.Answers, tc.answers) {
				t.Errorf("answers = %v, want %v", got.Answers, tc.answers)
			}
			if tc.errHas != "" && !strings.Contains(got.Err, tc.errHas) {
				t.Errorf("err = %q, want it to carry %q", got.Err, tc.errHas)
			}
			if tc.errHas == "" && got.Err != "" {
				t.Errorf("unexpected err %q", got.Err)
			}
		})
	}
}

// TestBlocklistsQueryEveryZone drives the whole list and checks the query names, which is
// the part a wrong implementation gets silently wrong: a name that is not the reversed
// address under the zone is answered by some resolvers as a search-domain lookup.
func TestBlocklistsQueryEveryZone(t *testing.T) {
	res := &fakeResolver{}
	c := New(Options{Resolver: res, RequestTimeout: time.Second})
	got := c.blocklists(context.Background(), testIP)

	if len(got) != len(zones) {
		t.Fatalf("got %d results, want one per zone (%d)", len(got), len(zones))
	}
	for i, z := range zones {
		if got[i].Zone != z {
			t.Errorf("result %d is for %q, want %q", i, got[i].Zone, z)
		}
		want := testRevIP + "." + z
		if n := res.count(want); n != 1 {
			t.Errorf("query %q was asked %d times, want exactly 1", want, n)
		}
		if res.count(testIP+"."+z) != 0 {
			t.Errorf("the address was queried unreversed: %s.%s", testIP, z)
		}
	}
	if len(res.queries()) != len(zones) {
		t.Errorf("the resolver saw %d queries, want %d", len(res.queries()), len(zones))
	}
}

func TestBlocklistsRefuseNonIPv4(t *testing.T) {
	res := &fakeResolver{}
	c := New(Options{Resolver: res, RequestTimeout: time.Second})
	got := c.blocklists(context.Background(), "nat")
	if len(got) != len(zones) {
		t.Fatalf("got %d results, want one per zone (%d)", len(got), len(zones))
	}
	for _, b := range got {
		if b.Listed {
			t.Errorf("zone %q reported a listing for a non-address", b.Zone)
		}
		if !strings.Contains(b.Err, "不是 IPv4") {
			t.Errorf("zone %q reports %q, want a note about the address", b.Zone, b.Err)
		}
	}
	if len(res.queries()) != 0 {
		t.Errorf("a non-address produced %d DNS queries", len(res.queries()))
	}
}

// TestReverse covers the PTR and forward-confirmed check, including the case the check
// exists for: a PTR that no longer resolves back to the address.
func TestReverse(t *testing.T) {
	cases := []struct {
		name      string
		hosts     map[string][]string
		hostErr   map[string]error
		ptrs      map[string][]string
		ptrErr    map[string]error
		wantNames []string
		wantFC    bool
		noteHas   string
	}{
		{
			name: "a PTR that resolves back to the address",
			hosts: map[string][]string{
				"mail.example.net": {testIP},
			},
			ptrs:      map[string][]string{testIP: {"mail.example.net"}},
			wantNames: []string{"mail.example.net"},
			wantFC:    true,
			noteHas:   "正向验证一致",
		},
		{
			name: "the first name is stale but a later one confirms",
			hosts: map[string][]string{
				"old.example.net":  {"198.51.100.9"},
				"mail.example.net": {testIP},
			},
			ptrs:      map[string][]string{testIP: {"old.example.net", "mail.example.net"}},
			wantNames: []string{"old.example.net", "mail.example.net"},
			wantFC:    true,
			noteHas:   "正向验证一致",
		},
		{
			name: "a PTR that resolves to a different address",
			hosts: map[string][]string{
				"stale.example.net": {"198.51.100.9"},
			},
			ptrs:      map[string][]string{testIP: {"stale.example.net"}},
			wantNames: []string{"stale.example.net"},
			noteHas:   "正向验证不一致",
		},
		{
			name:      "a PTR that does not resolve forward at all",
			ptrs:      map[string][]string{testIP: {"gone.example.net"}},
			wantNames: []string{"gone.example.net"},
			noteHas:   "正向验证不一致",
		},
		{
			name: "a fully qualified PTR name keeps no trailing dot",
			hosts: map[string][]string{
				"mail.example.net": {testIP},
			},
			ptrs:      map[string][]string{testIP: {"mail.example.net."}},
			wantNames: []string{"mail.example.net"},
			wantFC:    true,
			noteHas:   "正向验证一致",
		},
		{
			name:    "no PTR record",
			noteHas: "无记录",
		},
		{
			name:    "the PTR query timed out",
			ptrErr:  map[string]error{testIP: timeoutErr(testIP)},
			noteHas: "查询失败（查询超时）",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := &fakeResolver{hosts: tc.hosts, hostErr: tc.hostErr, ptrs: tc.ptrs, ptrErr: tc.ptrErr}
			c := New(Options{Resolver: res, RequestTimeout: time.Second})
			names, fc, notes := c.reverse(context.Background(), testIP)
			if !reflect.DeepEqual(names, tc.wantNames) {
				t.Errorf("names = %v, want %v", names, tc.wantNames)
			}
			if fc != tc.wantFC {
				t.Errorf("FCrDNS = %v, want %v", fc, tc.wantFC)
			}
			if len(notes) != 1 || !strings.Contains(notes[0], tc.noteHas) {
				t.Errorf("notes = %v, want one carrying %q", notes, tc.noteHas)
			}
			if n := res.countPTR(testIP); n != 1 {
				t.Errorf("the PTR query was asked %d times, want 1", n)
			}
			// The reverse question is a PTR query for the address, not an
			// A-record lookup for its arpa name: the latter goes to
			// LookupHost and would answer NXDOMAIN for an address that does
			// have a PTR record.
			if res.count(testIP+".in-addr.arpa") != 0 {
				t.Error("the PTR question was asked as a host lookup")
			}
		})
	}
}

func TestReverseRefusesNonIPv4(t *testing.T) {
	res := &fakeResolver{}
	c := New(Options{Resolver: res, RequestTimeout: time.Second})
	names, fc, notes := c.reverse(context.Background(), "2001:db8::1")
	if names != nil || fc {
		t.Errorf("got %v/%v for a non-address", names, fc)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "不是 IPv4") {
		t.Errorf("notes = %v, want a note about the address", notes)
	}
	if len(res.queries()) != 0 {
		t.Errorf("a non-address produced %d DNS queries", len(res.queries()))
	}
}

// TestResultNotesForBlacklists is the wording contract the operator reads: hits name the
// zone and the answer, and unreached zones are listed separately from misses.
func TestResultNotesForBlacklists(t *testing.T) {
	rep := Report{
		IP: testIP,
		Blocklists: []Blocklist{
			{Zone: "zen.spamhaus.org", Listed: true, Answers: []string{"127.0.0.2"}},
			{Zone: "bl.spamcop.net"},
			{Zone: "dnsbl.sorbs.net", Err: "查询超时"},
		},
	}
	res := rep.Result()
	if got := noteWith(res, "黑名单命中"); !strings.Contains(got, "1/3") || !strings.Contains(got, "zen.spamhaus.org(127.0.0.2)") {
		t.Errorf("hit note = %q", got)
	}
	if got := noteWith(res, "黑名单查询未决"); !strings.Contains(got, "dnsbl.sorbs.net") {
		t.Errorf("unreached note = %q", got)
	}
	if noteContains(res, "bl.spamcop.net") {
		t.Error("a clean miss was reported as news")
	}
	if !strings.Contains(res.Summary, "1/3 黑名单命中") {
		t.Errorf("summary = %q, want the hit count", res.Summary)
	}
}

func TestZeroHitNote(t *testing.T) {
	rep := Report{IP: testIP, Blocklists: []Blocklist{{Zone: "bl.spamcop.net"}}}
	if got := noteWith(rep.Result(), "黑名单命中"); !strings.Contains(got, "无") {
		t.Errorf("note = %q, want it to say there were no hits", got)
	}
}

// TestLookupNotes covers what an operator reads when a database or a blocklist did not
// deliver: the failure names the reason, and a promise the answer did not keep is called
// a missing field rather than a failure.
func TestLookupNotes(t *testing.T) {
	rep := Report{
		IP: testIP,
		Lookups: []Lookup{
			{Source: "ip-api.com", Country: "US"},
			{Source: "ip.sb", Fail: fail(FailTimeout, "")},
			{Source: "db-ip.com", Country: "US", Missing: []string{"city"}},
		},
	}
	res := rep.Result()
	if got := noteWith(res, "ip.sb："); got != "ip.sb：查询失败（超时）" {
		t.Errorf("failure note = %q", got)
	}
	if got := noteWith(res, "db-ip.com："); got != "db-ip.com：字段缺失（city）" {
		t.Errorf("missing-field note = %q", got)
	}
	if noteContains(res, "ip-api.com：") {
		t.Error("a database that answered was reported as a problem")
	}
}

func TestErrorHelpers(t *testing.T) {
	if !isTimeout(timeoutErr("x")) {
		t.Error("a timeout error was not recognised")
	}
	if isTimeout(notFoundErr("x")) {
		t.Error("NXDOMAIN was read as a timeout")
	}
	if !isNotFound(notFoundErr("x")) {
		t.Error("NXDOMAIN was not recognised")
	}
	if isNotFound(errors.New("boom")) {
		t.Error("a plain error was read as NXDOMAIN")
	}
	if isNotFound(nil) {
		t.Error("nil was read as NXDOMAIN")
	}
	if got := queryError(context.Canceled); !strings.Contains(got, "已取消") {
		t.Errorf("queryError(cancelled) = %q", got)
	}
	if got := queryError(context.DeadlineExceeded); !strings.Contains(got, "超时") {
		t.Errorf("queryError(deadline) = %q", got)
	}
	if got := queryError(errors.New("boom")); !strings.Contains(got, "查询失败") {
		t.Errorf("queryError(plain) = %q", got)
	}
	if !isIPv4(testIP) || isIPv4("2001:db8::1") || isIPv4("not an ip") {
		t.Error("isIPv4 does not separate IPv4 literals from the rest")
	}
}

func TestHeaderRowIsACopy(t *testing.T) {
	rep := Report{IP: testIP}
	res := rep.Result()
	if !reflect.DeepEqual(res.Headers, headers) {
		t.Fatalf("headers = %v, want %v", res.Headers, headers)
	}
	res.Headers[0] = "changed"
	if headers[0] == "changed" {
		t.Error("the package's own header slice was handed out")
	}
}

// TestStatusFailure pins the status mapping, because a 403 from these endpoints is a key
// or a block page rather than a broken address, and a 429 is a rate limit the operator
// can wait out.
func TestStatusFailure(t *testing.T) {
	cases := []struct {
		code   int
		reason FailReason
		ok     bool
	}{
		{code: 200, ok: true},
		{code: 204, ok: true},
		{code: 301, reason: FailStatus},
		{code: 403, reason: FailRefused},
		{code: 429, reason: FailRateLimit},
		{code: 500, reason: FailStatus},
		{code: 503, reason: FailStatus},
	}
	for _, tc := range cases {
		got := statusFailure(tc.code)
		if tc.ok {
			if got != nil {
				t.Errorf("status %d reported %s, want success", tc.code, got.Text())
			}
			continue
		}
		if got == nil || got.Reason != tc.reason {
			t.Errorf("status %d reported %v, want %q", tc.code, got, tc.reason)
		}
	}
}

func TestTransportFailure(t *testing.T) {
	if got := transportFailure(timeoutErr("x")); got.Reason != FailTimeout {
		t.Errorf("a timeout reported %q", got.Reason)
	}
	if got := transportFailure(context.DeadlineExceeded); got.Reason != FailTimeout {
		t.Errorf("a deadline reported %q", got.Reason)
	}
	if got := transportFailure(errors.New("connection refused")); got.Reason != FailTransport {
		t.Errorf("a transport error reported %q", got.Reason)
	}
}

func TestToolDescriptor(t *testing.T) {
	if Tool.ID != ToolID || ToolID != "ipquality" {
		t.Errorf("Tool.ID = %q, want %q", Tool.ID, ToolID)
	}
	if Tool.Group != Group || Group != "ip" {
		t.Errorf("Tool.Group = %q, want %q", Tool.Group, Group)
	}
	if Tool.Run == nil {
		t.Fatal("Tool.Run is nil")
	}
}

// TestRunToolEntryPoint drives the function the panel calls, with the default resolver
// swapped for the fake one: the Tool entry point must produce the same table New does.
func TestRunToolEntryPoint(t *testing.T) {
	restore := defaultResolver
	defaultResolver = healthyResolver()
	t.Cleanup(func() { defaultResolver = restore })

	res, err := Run(context.Background(), toolbox.Options{
		Client:  &fakeClient{t: t, routes: routes(t)},
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("Run returned %v", err)
	}
	if len(res.Rows) != len(catalogue) {
		t.Fatalf("got %d rows, want one per database (%d)", len(res.Rows), len(catalogue))
	}
	if !strings.Contains(res.Summary, "机房") || !strings.Contains(res.Summary, "3/12 黑名单命中") {
		t.Errorf("summary = %q", res.Summary)
	}
}
