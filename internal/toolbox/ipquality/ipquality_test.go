package ipquality

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/EasySBTeam/EasySB/internal/toolbox"
)

// expectedRows is what a healthy run shows, one entry per database: the country and city
// joined, the ASN, the ISP, the database's own kind claim, and the markers it reported.
var expectedRows = map[string][]string{
	"ip-api.com":     {"ip-api.com", "US · Los Angeles", "AS64500", "Example Hosting LLC", "机房", "hosting"},
	"ipinfo.io":      {"ipinfo.io", "US · Los Angeles", "AS64500", "Example Hosting LLC", "机房", "hosting"},
	"ipapi.is":       {"ipapi.is", "United States · Los Angeles", "AS64500", "Example Hosting LLC", "—", "无"},
	"ipwho.is":       {"ipwho.is", "US · Los Angeles", "AS64500", "Example Hosting LLC", "—", "—"},
	"ip.sb":          {"ip.sb", "US · Los Angeles", "AS64500", "Example Hosting LLC", "—", "—"},
	"ip2location.io": {"ip2location.io", "US · Los Angeles", "AS64500", "Example Hosting LLC", "—", "无"},
	"ipwhois.app":    {"ipwhois.app", "US · Los Angeles", "AS64500", "Example Hosting LLC", "—", "—"},
	"db-ip.com":      {"db-ip.com", "US · Los Angeles", "—", "—", "—", "—"},
	"ipapi.co":       {"ipapi.co", "US · Los Angeles", "AS64500", "Example Hosting LLC", "—", "—"},
}

// TestCheckHealthyRun is the whole-report test: every database answering, a datacenter
// verdict two databases agree on, three blocklist hits, and a reverse name that confirms.
func TestCheckHealthyRun(t *testing.T) {
	client := &fakeClient{t: t, routes: routes(t)}
	res := healthyResolver()
	sink := &logSink{}
	c := New(Options{
		Options:        toolbox.Options{Client: client, Timeout: 10 * time.Second, Log: sink.log},
		Resolver:       res,
		RequestTimeout: 2 * time.Second,
	})
	rep := c.Check(context.Background())

	if rep.IP != testIP {
		t.Fatalf("IP = %q, want %q", rep.IP, testIP)
	}
	if rep.IPSource != "api.ipify.org" {
		t.Errorf("IPSource = %q, want api.ipify.org", rep.IPSource)
	}
	if len(rep.Lookups) != len(catalogue) {
		t.Fatalf("got %d lookups, want one per database (%d)", len(rep.Lookups), len(catalogue))
	}
	for _, l := range rep.Lookups {
		if l.Fail != nil {
			t.Errorf("%s failed: %s", l.Source, l.Fail.Text())
		}
		if len(l.Missing) > 0 {
			t.Errorf("%s is missing %v", l.Source, l.Missing)
		}
	}
	if rep.Kind != KindIDC {
		t.Errorf("Kind = %q, want %q", rep.Kind, KindIDC)
	}
	if len(rep.Basis) != 2 {
		t.Errorf("basis = %+v, want the two claims that report a type", rep.Basis)
	}
	if !rep.FCrDNS || len(rep.PTR) != 1 || rep.PTR[0] != "mail.example.net" {
		t.Errorf("PTR = %v, FCrDNS = %v", rep.PTR, rep.FCrDNS)
	}

	res2 := rep.Result()
	if len(res2.Rows) != len(catalogue) {
		t.Fatalf("got %d rows, want one per database (%d)", len(res2.Rows), len(catalogue))
	}
	for source, want := range expectedRows {
		if got := rowOf(t, res2, source); !reflect.DeepEqual(got, want) {
			t.Errorf("row %s = %v, want %v", source, got, want)
		}
	}
	if res2.Summary != "IP 质量：机房 · 3/12 黑名单命中 · ASN 64500" {
		t.Errorf("summary = %q", res2.Summary)
	}
	for _, want := range []string{
		"检测地址：203.0.113.7（来自 api.ipify.org）",
		"线路判定：机房",
		"ip-api.com 报告 hosting=true",
		"ipinfo.io 报告 type=hosting",
		"反向 DNS（PTR）：mail.example.net（正向验证一致）",
		"黑名单命中（3/12）",
		"zen.spamhaus.org(127.0.0.2)",
		"黑名单查询未决",
	} {
		if !noteContains(res2, want) {
			t.Errorf("no note carries %q; notes are %v", want, res2.Notes)
		}
	}
	if noteContains(res2, "查询失败") {
		t.Errorf("a healthy run reported a failure: %v", res2.Notes)
	}
	if !noteContains(res2, "未提供 IP 类型与代理标记字段的来源：ipapi.is") {
		t.Errorf("the thin-source note does not name ipapi.is: %v", res2.Notes)
	}

	lines := strings.Join(sink.all(), "\n")
	if !strings.Contains(lines, testIP) {
		t.Errorf("the progress log never names the address: %q", lines)
	}
	if !strings.Contains(lines, "idc") {
		t.Errorf("the progress log never names the verdict: %q", lines)
	}
}

// TestCheckSourceFailures is the honesty contract: a database that did not answer gets a
// row that carries no values at all, its reason in the risk column, and a note; the other
// databases keep their own answers.
func TestCheckSourceFailures(t *testing.T) {
	cases := []struct {
		name     string
		override route
		source   string
		// row is the whole expected row, for the failures that still carry part
		// of an answer.
		row      []string
		failCell string
		prefix   bool
		note     string
	}{
		{
			name:     "a 503",
			override: route{url: "https://api.ip.sb/geoip/", status: 503, body: "upstream is down"},
			source:   "ip.sb",
			failCell: "查询失败（HTTP 503）",
			note:     "ip.sb：查询失败（HTTP 503）",
		},
		{
			name:     "a 429",
			override: route{url: "https://ipwho.is/", status: 429, body: "slow down"},
			source:   "ipwho.is",
			failCell: "查询失败（限流 · HTTP 429）",
			note:     "ipwho.is：查询失败（限流 · HTTP 429）",
		},
		{
			name:     "a 403",
			override: route{url: "https://api.db-ip.com/", status: 403, body: "forbidden"},
			source:   "db-ip.com",
			failCell: "查询失败（服务拒绝 · HTTP 403）",
			note:     "db-ip.com：查询失败（服务拒绝 · HTTP 403）",
		},
		{
			name:     "a request that never answers",
			override: route{url: "https://ipapi.co/", block: true},
			source:   "ipapi.co",
			failCell: "查询失败（超时）",
			note:     "ipapi.co：查询失败（超时）",
		},
		{
			name:     "a connection that is refused",
			override: route{url: "https://ipwhois.app/", err: errors.New("connection refused")},
			source:   "ipwhois.app",
			failCell: "查询失败（网络错误 · connection refused）",
			note:     "ipwhois.app：查询失败（网络错误 · connection refused）",
		},
		{
			name:     "a block page instead of JSON",
			override: route{url: "https://api.ipapi.is/", body: "<html><body>blocked</body></html>"},
			source:   "ipapi.is",
			failCell: "查询失败（响应无法解析",
			prefix:   true,
			note:     "ipapi.is：查询失败（响应无法解析",
		},
		{
			name: "a database that declines the address",
			override: route{url: "http://ip-api.com/json/",
				body: `{"status":"fail","message":"reserved range"}`},
			source:   "ip-api.com",
			failCell: "查询失败（服务拒绝 · reserved range）",
			note:     "ip-api.com：查询失败（服务拒绝 · reserved range）",
		},
		{
			name: "a database that lost a promised field keeps the rest of its answer",
			override: route{url: "http://ip-api.com/json/",
				body: `{"status":"success","countryCode":"NL","city":"Amsterdam","isp":"Example BV","hosting":true}`},
			source: "ip-api.com",
			row:    []string{"ip-api.com", "NL · Amsterdam", "—", "Example BV", "机房", "hosting"},
			note:   "ip-api.com：字段缺失（asn）",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &fakeClient{t: t, routes: routes(t, tc.override)}
			rep := newTestChecker(t, client, healthyResolver(), "").Check(context.Background())
			res := rep.Result()
			row := rowOf(t, res, tc.source)

			if tc.row != nil {
				if !reflect.DeepEqual(row, tc.row) {
					t.Errorf("row = %v, want %v", row, tc.row)
				}
			} else {
				if row[5] != tc.failCell && !(tc.prefix && strings.HasPrefix(row[5], tc.failCell)) {
					t.Errorf("risk cell = %q, want %q", row[5], tc.failCell)
				}
				for i := 1; i <= 4; i++ {
					if row[i] != dash {
						t.Errorf("a failed row borrowed a value: cell %d = %q", i, row[i])
					}
				}
			}
			if !noteContains(res, tc.note) {
				t.Errorf("no note carries %q; notes are %v", tc.note, res.Notes)
			}
			// The databases that answered are untouched by one neighbour's
			// failure, and no value from them was borrowed.
			for source, want := range expectedRows {
				if source == tc.source {
					continue
				}
				if got := rowOf(t, res, source); !reflect.DeepEqual(got, want) {
					t.Errorf("row %s = %v, want %v", source, got, want)
				}
			}
		})
	}
}

// TestCheckKindVerdicts drives the line-kind rule end to end, including the case where the
// databases disagree: the report must say so rather than pick a winner.
func TestCheckKindVerdicts(t *testing.T) {
	const (
		apiMobile = `{"status":"success","countryCode":"US","city":"Denver","isp":"Example Wireless",` +
			`"as":"AS64502 Example Wireless","proxy":false,"hosting":false,"mobile":true}`
		apiSilent = `{"status":"success","countryCode":"US","city":"Denver","isp":"Example Telecom",` +
			`"as":"AS64503 Example Telecom","proxy":false,"hosting":false,"mobile":false}`
		infoResidential = `{"data":{"city":"Los Angeles","country":"US","org":"AS64500 Example Telecom",` +
			`"asn":{"asn":"AS64500","name":"Example Telecom","type":"isp"},` +
			`"company":{"name":"Example Telecom","type":"isp"},"privacy":{"hosting":false},"is_mobile":false}}`
		infoSilent = `{"data":{"city":"Los Angeles","country":"US","org":"AS64500 Example Hosting LLC",` +
			`"asn":{"asn":"AS64500","name":"Example Hosting LLC"},"company":{"name":"Example Hosting LLC"},` +
			`"privacy":{"hosting":false},"is_mobile":false}}`
	)
	cases := []struct {
		name      string
		overrides []route
		want      Kind
		notes     []string
	}{
		{
			name:  "both databases say hosting",
			want:  KindIDC,
			notes: []string{"线路判定：机房", "ip-api.com 报告 hosting=true", "ipinfo.io 报告 type=hosting"},
		},
		{
			name: "a mobile range against a datacenter is a disagreement",
			overrides: []route{
				{url: "http://ip-api.com/json/", body: apiMobile},
			},
			want:  KindMixed,
			notes: []string{"线路判定：不一致", "ip-api.com：移动网络（mobile=true）", "ipinfo.io：机房（type=hosting）"},
		},
		{
			name: "a residential claim against a datacenter is a disagreement too",
			overrides: []route{
				{url: "https://ipinfo.io/widget/demo/", body: infoResidential},
			},
			want:  KindMixed,
			notes: []string{"线路判定：不一致", "ipinfo.io：家宽（type=isp）"},
		},
		{
			name: "one database silent leaves the other's claim in charge",
			overrides: []route{
				{url: "http://ip-api.com/json/", body: apiSilent},
			},
			want:  KindIDC,
			notes: []string{"线路判定：机房（ipinfo.io 报告 type=hosting）"},
		},
		{
			name: "no database says anything",
			overrides: []route{
				{url: "http://ip-api.com/json/", body: apiSilent},
				{url: "https://ipinfo.io/widget/demo/", body: infoSilent},
			},
			want:  KindUnknown,
			notes: []string{"线路判定：未知", "IP 质量：未知"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &fakeClient{t: t, routes: routes(t, tc.overrides...)}
			rep := newTestChecker(t, client, healthyResolver(), "").Check(context.Background())
			if rep.Kind != tc.want {
				t.Errorf("Kind = %q, want %q", rep.Kind, tc.want)
			}
			res := rep.Result()
			for _, want := range tc.notes {
				if !noteContains(res, want) && !strings.Contains(res.Summary, want) {
					t.Errorf("nothing carries %q; notes are %v", want, res.Notes)
				}
			}
		})
	}
}

// TestDiscoverIP covers the address discovery and its fallbacks.
func TestDiscoverIP(t *testing.T) {
	cases := []struct {
		name       string
		overrides  []route
		ip         string
		wantIP     string
		wantSource string
		wantRows   int
		nummary    string
		noteHas    string
		noRequest  string
	}{
		{
			name:       "the first endpoint answers",
			wantSource: "api.ipify.org",
		},
		{
			name:       "the first endpoint is down",
			overrides:  []route{{url: "https://api.ipify.org", status: 503, body: "down"}},
			wantSource: "ifconfig.me",
			noteHas:    "api.ipify.org：查询失败（HTTP 503）",
		},
		{
			name: "only the last endpoint answers",
			overrides: []route{
				{url: "https://api.ipify.org", err: errors.New("connection reset")},
				{url: "https://ifconfig.me/ip", status: 500, body: "oops"},
			},
			wantSource: "checkip.amazonaws.com",
			noteHas:    "ifconfig.me：查询失败（HTTP 500）",
		},
		{
			name:       "an endpoint answering with HTML is not an address",
			overrides:  []route{{url: "https://api.ipify.org", body: "<html>hello</html>"}},
			wantSource: "ifconfig.me",
			noteHas:    "api.ipify.org：查询失败（响应无法解析",
		},
		{
			name: "no endpoint answers",
			overrides: []route{
				{url: "https://api.ipify.org", block: true},
				{url: "https://ifconfig.me/ip", status: 503, body: "down"},
				{url: "https://checkip.amazonaws.com", status: 403, body: "forbidden"},
			},
			nummary: "IP 质量：无法确定公网 IP",
			noteHas: "无法确定本机公网 IPv4",
		},
		{
			name:       "the caller already knows the address",
			ip:         "198.51.100.9",
			wantIP:     "198.51.100.9",
			wantSource: "调用方",
			noRequest:  "https://api.ipify.org",
		},
		{
			name:       "a caller-supplied non-address degrades to discovery",
			ip:         "::1",
			wantSource: "api.ipify.org",
			noteHas:    "调用方给出的地址",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &fakeClient{t: t, routes: routes(t, tc.overrides...)}
			res := healthyResolver()
			rep := newTestChecker(t, client, res, tc.ip).Check(context.Background())

			wantIP := tc.wantIP
			if wantIP == "" && tc.nummary == "" {
				wantIP = testIP
			}
			if rep.IP != wantIP {
				t.Fatalf("IP = %q, want %q (notes %v)", rep.IP, wantIP, rep.IPNotes)
			}
			if tc.wantSource != "" && rep.IPSource != tc.wantSource {
				t.Errorf("IPSource = %q, want %q", rep.IPSource, tc.wantSource)
			}
			if tc.noteHas != "" && !noteContains(rep.Result(), tc.noteHas) {
				t.Errorf("no note carries %q; notes are %v", tc.noteHas, rep.Result().Notes)
			}
			if tc.noRequest != "" && client.asked(tc.noRequest) {
				t.Errorf("a known address still hit %s", tc.noRequest)
			}
			out := rep.Result()
			if wantIP == "" {
				if out.Summary != tc.nummary {
					t.Errorf("summary = %q, want %q", out.Summary, tc.nummary)
				}
				if len(out.Rows) != 0 {
					t.Errorf("an undetermined address produced %d rows", len(out.Rows))
				}
				if client.asked("http://ip-api.com") {
					t.Error("databases were queried about an address that was never determined")
				}
				if len(res.queries()) != 0 {
					t.Errorf("blocklists were queried about an unknown address: %v", res.queries())
				}
				return
			}
			if len(out.Rows) != len(catalogue) {
				t.Errorf("got %d rows, want one per database (%d)", len(out.Rows), len(catalogue))
			}
		})
	}
}

// TestCallerAddressSkipsDiscovery checks the fallback path in the other direction: an
// address from the caller means the discovery endpoints are not asked at all.
func TestCallerAddressSkipsDiscovery(t *testing.T) {
	client := &fakeClient{t: t, routes: routes(t)}
	rep := newTestChecker(t, client, healthyResolver(), "198.51.100.9").Check(context.Background())
	if rep.IP != "198.51.100.9" || rep.IPSource != "调用方" {
		t.Fatalf("report is about %q from %q", rep.IP, rep.IPSource)
	}
	for _, e := range ipEndpoints {
		if client.asked(e.url) {
			t.Errorf("%s was asked even though the caller supplied the address", e.name)
		}
	}
	if len(rep.IPNotes) != 0 {
		t.Errorf("a caller-supplied address produced notes: %v", rep.IPNotes)
	}
}

// TestCheckRespectsContext checks that a cancelled run does not hang: every request shares
// the caller's context.
func TestCheckRespectsContext(t *testing.T) {
	client := &fakeClient{t: t, routes: []route{
		{url: "https://api.ipify.org", block: true},
		{url: "https://ifconfig.me/ip", block: true},
		{url: "https://checkip.amazonaws.com", block: true},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan Report, 1)
	go func() { done <- newTestChecker(t, client, healthyResolver(), "").Check(ctx) }()
	select {
	case rep := <-done:
		if rep.IP != "" {
			t.Errorf("a cancelled run reported IP %q", rep.IP)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Check did not return after its context was cancelled")
	}
}

// TestUnknownAddressResultShape pins the shape the panel draws when there is nothing to
// tabulate: the columns are still there, so the notes are not rendered as cells.
func TestUnknownAddressResultShape(t *testing.T) {
	rep := Report{IPNotes: []string{"api.ipify.org：查询失败（超时）"}}
	res := rep.Result()
	if !reflect.DeepEqual(res.Headers, headers) {
		t.Errorf("headers = %v, want %v", res.Headers, headers)
	}
	if len(res.Rows) != 0 {
		t.Errorf("rows = %v, want none", res.Rows)
	}
	if !reflect.DeepEqual(res.Notes, rep.IPNotes) {
		t.Errorf("notes = %v, want %v", res.Notes, rep.IPNotes)
	}
	if res.Summary != "IP 质量：无法确定公网 IP" {
		t.Errorf("summary = %q", res.Summary)
	}
}
