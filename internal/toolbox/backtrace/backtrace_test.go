package backtrace

import (
	"context"
	"errors"
	"net"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EasySB-Team/EasySB/internal/toolbox"
)

// fakeProber answers with a canned path per target, so a report test never sends
// a packet and never needs root.
type fakeProber struct {
	paths map[string]Path
	errs  map[string]error
	// block makes every trace wait for the run's deadline, for testing the
	// toolbox timeout.
	block   bool
	mu      sync.Mutex
	traced  []string
	closed  bool
	closeMu sync.Mutex
}

func (p *fakeProber) Trace(ctx context.Context, dst net.IP) (Path, error) {
	p.mu.Lock()
	p.traced = append(p.traced, dst.String())
	p.mu.Unlock()
	if p.block {
		<-ctx.Done()
		return Path{}, ctx.Err()
	}
	if err := p.errs[dst.String()]; err != nil {
		return Path{}, err
	}
	return p.paths[dst.String()], nil
}

func (p *fakeProber) Close() error {
	p.closeMu.Lock()
	p.closed = true
	p.closeMu.Unlock()
	return nil
}

func (p *fakeProber) wasClosed() bool {
	p.closeMu.Lock()
	defer p.closeMu.Unlock()
	return p.closed
}

// fakeLookup answers from a table and remembers what it was asked about.
type fakeLookup struct {
	facts map[string]Fact
	err   error
	asked []string
}

func (l *fakeLookup) Lookup(_ context.Context, ips []net.IP) (map[string]Fact, error) {
	out := make(map[string]Fact, len(ips))
	for _, ip := range ips {
		l.asked = append(l.asked, ip.String())
		if f, ok := l.facts[ip.String()]; ok {
			out[ip.String()] = f
		}
	}
	return out, l.err
}

// Test helpers for the canned report inputs.
func at(name, ip string) Target { return Target{Name: name, IP: net.ParseIP(ip)} }

func answered(ttl int, ip string, rtt time.Duration) Hop {
	return Hop{TTL: ttl, IP: net.ParseIP(ip), RTT: rtt}
}

func silent(ttl int) Hop { return Hop{TTL: ttl} }

func cnFact(asn int, isp, asname string) Fact {
	return Fact{Country: "CN", ASN: asn, ISP: isp, ASName: asname, Known: true}
}

func TestRunVerdicts(t *testing.T) {
	beijing := []Target{at("北京电信", "219.141.140.10")}
	transit := Fact{Country: "US", ISP: "Example Transit", ASN: 64500, ASName: "EXAMPLE-TRANSIT", Known: true}

	tests := []struct {
		name        string
		targets     []Target
		paths       map[string]Path
		probeErr    map[string]error
		facts       map[string]Fact
		lookupErr   error
		wantRows    [][]string
		wantSummary string
		wantNotes   []string
	}{
		{
			name: "three carriers, one city each",
			targets: []Target{
				at("北京电信", "219.141.140.10"),
				at("北京联通", "202.106.195.68"),
				at("北京移动", "221.179.155.161"),
			},
			paths: map[string]Path{
				"219.141.140.10": {Reached: true, Hops: []Hop{answered(1, "203.0.113.9", 30*time.Millisecond), answered(2, "219.141.140.10", 12*time.Millisecond)}},
				"202.106.195.68": {Reached: true, Hops: []Hop{answered(1, "203.0.113.9", 30*time.Millisecond), answered(2, "202.106.195.68", 15*time.Millisecond)}},
				"221.179.155.161": {Reached: true, Hops: []Hop{
					answered(1, "203.0.113.9", 30*time.Millisecond),
					answered(2, "221.179.155.161", 18*time.Millisecond)}},
			},
			facts: map[string]Fact{
				"203.0.113.9":     transit,
				"219.141.140.10":  cnFact(4134, "Chinanet", "CHINANET-BACKBONE"),
				"202.106.195.68":  cnFact(4837, "China Unicom", "CHINA169-BACKBONE"),
				"221.179.155.161": cnFact(9808, "China Mobile", "CMNET"),
			},
			wantRows: [][]string{
				{"北京电信 219.141.140.10", "电信(163)", "219.141.140.10", "12ms"},
				{"北京联通 202.106.195.68", "联通(4837)", "202.106.195.68", "15ms"},
				{"北京移动 221.179.155.161", "移动(CMI)", "221.179.155.161", "18ms"},
			},
			wantSummary: "电信 12ms · 联通 15ms · 移动 18ms",
		},
		{
			name:    "the target never answers",
			targets: beijing,
			paths: map[string]Path{
				"219.141.140.10": {Hops: []Hop{
					answered(1, "198.51.100.1", 5*time.Millisecond),
					silent(2), silent(3)}},
			},
			facts:       map[string]Fact{"198.51.100.1": cnFact(4134, "Chinanet", "CHINANET-BACKBONE")},
			wantRows:    [][]string{{"北京电信 219.141.140.10", "电信(163)", "198.51.100.1", "5ms"}},
			wantSummary: "电信 5ms",
			wantNotes:   []string{"目标未应答", "末跳"},
		},
		{
			name:    "the lookup was rate limited",
			targets: beijing,
			paths: map[string]Path{
				"219.141.140.10": {Reached: true, Hops: []Hop{answered(1, "219.141.140.10", 12*time.Millisecond)}},
			},
			facts:       map[string]Fact{"219.141.140.10": {Reason: "ip-api 限流（HTTP 429），本轮无法判定这些地址"}},
			wantRows:    [][]string{{"北京电信 219.141.140.10", "无法判定", "219.141.140.10", "12ms"}},
			wantSummary: "1 个目标全部无法判定",
			wantNotes:   []string{"无法判定（ip-api 限流"},
		},
		{
			name:    "the lookup could not run at all",
			targets: beijing,
			paths: map[string]Path{
				"219.141.140.10": {Reached: true, Hops: []Hop{answered(1, "219.141.140.10", 12*time.Millisecond)}},
			},
			lookupErr:   errors.New("ip-api 查询失败：i/o timeout"),
			wantRows:    [][]string{{"北京电信 219.141.140.10", "无法判定", "219.141.140.10", "12ms"}},
			wantSummary: "1 个目标全部无法判定",
			wantNotes:   []string{"依赖地址查询失败"},
		},
		{
			name:    "the operator is unknown",
			targets: beijing,
			paths: map[string]Path{
				"219.141.140.10": {Reached: true, Hops: []Hop{answered(1, "219.141.140.10", 12*time.Millisecond)}},
			},
			facts:       map[string]Fact{"219.141.140.10": {Country: "CN", Known: true}},
			wantRows:    [][]string{{"北京电信 219.141.140.10", "无法判定", "219.141.140.10", "12ms"}},
			wantSummary: "1 个目标全部无法判定",
			wantNotes:   []string{"没有 ISP/ASN 信息"},
		},
		{
			name:    "a mainland network that is not one of the three",
			targets: beijing,
			paths: map[string]Path{
				"219.141.140.10": {Reached: true, Hops: []Hop{answered(1, "219.141.140.10", 12*time.Millisecond)}},
			},
			facts: map[string]Fact{
				"219.141.140.10": cnFact(4538, "China Education and Research Network Center", "CHINA EDUCATION AND RESEARCH NETWORK"),
			},
			wantRows:    [][]string{{"北京电信 219.141.140.10", "国际多线(AS4538)", "219.141.140.10", "12ms"}},
			wantSummary: "国际多线 12ms",
			wantNotes:   []string{"AS4538", "非三大运营商"},
		},
		{
			name:    "the path never reaches the mainland",
			targets: beijing,
			paths: map[string]Path{
				"219.141.140.10": {Reached: true, Hops: []Hop{
					answered(1, "203.0.113.9", 30*time.Millisecond),
					answered(2, "219.141.140.10", 12*time.Millisecond)}},
			},
			facts:       map[string]Fact{"203.0.113.9": transit, "219.141.140.10": transit},
			wantRows:    [][]string{{"北京电信 219.141.140.10", "无法判定", "219.141.140.10", "12ms"}},
			wantSummary: "1 个目标全部无法判定",
			wantNotes:   []string{"路径未进入中国大陆"},
		},
		{
			name:    "nothing answered at all",
			targets: beijing,
			paths: map[string]Path{
				"219.141.140.10": {Hops: []Hop{silent(1), silent(2)}},
			},
			wantRows:    [][]string{{"北京电信 219.141.140.10", "无法判定", "无应答", "—"}},
			wantSummary: "1 个目标全部无法判定",
			wantNotes:   []string{"24 跳内没有任何应答"},
		},
		{
			name:        "the probe failed",
			targets:     beijing,
			probeErr:    map[string]error{"219.141.140.10": errors.New("reading the ICMP reply: socket died")},
			wantRows:    [][]string{{"北京电信 219.141.140.10", "无法判定", "无应答", "—"}},
			wantSummary: "1 个目标全部无法判定",
			wantNotes:   []string{"探测失败（reading the ICMP reply"},
		},
		{
			name: "one broken target does not stop the others",
			targets: []Target{
				at("北京电信", "219.141.140.10"),
				at("上海联通", "210.22.97.1"),
			},
			paths: map[string]Path{
				"219.141.140.10": {Reached: true, Hops: []Hop{answered(1, "219.141.140.10", 40*time.Millisecond)}},
				"210.22.97.1":    {Reached: true, Hops: []Hop{answered(1, "210.22.97.1", 9*time.Millisecond)}},
			},
			probeErr: map[string]error{"219.141.140.10": errors.New("send failed")},
			facts: map[string]Fact{
				"210.22.97.1": cnFact(9929, "China Unicom", "CUII"),
			},
			wantRows: [][]string{
				{"北京电信 219.141.140.10", "无法判定", "无应答", "—"},
				{"上海联通 210.22.97.1", "联通(9929)", "210.22.97.1", "9ms"},
			},
			wantSummary: "联通 9ms · 1 个目标未判定",
			wantNotes:   []string{"探测失败"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prober := &fakeProber{paths: tt.paths, errs: tt.probeErr}
			if prober.paths == nil {
				prober.paths = map[string]Path{}
			}
			lookup := &fakeLookup{facts: tt.facts, err: tt.lookupErr}
			runner := Runner{Prober: prober, Lookup: lookup, Targets: tt.targets}

			var logs []string
			res, err := runner.Run(context.Background(), toolbox.Options{Log: func(s string) { logs = append(logs, s) }})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if !reflect.DeepEqual(res.Headers, []string{"目标", "回程线路", "末跳", "时延"}) {
				t.Errorf("headers = %v", res.Headers)
			}
			if !reflect.DeepEqual(res.Rows, tt.wantRows) {
				t.Errorf("rows = %v, want %v", res.Rows, tt.wantRows)
			}
			if res.Summary != tt.wantSummary {
				t.Errorf("summary = %q, want %q", res.Summary, tt.wantSummary)
			}
			if len(res.Notes) < 3 || res.Notes[0] != targetSourceNote {
				t.Errorf("notes must open with the target source line, got %v", res.Notes)
			}
			notes := strings.Join(res.Notes, "\n")
			for _, want := range tt.wantNotes {
				if !strings.Contains(notes, want) {
					t.Errorf("notes = %v, want one containing %q", res.Notes, want)
				}
			}
			if !prober.wasClosed() {
				t.Error("the prober was not closed after the run")
			}
			if len(logs) == 0 {
				t.Error("no progress was reported")
			}
		})
	}
}

func TestRunAsksOnceAboutSharedHops(t *testing.T) {
	// Two targets whose paths share a hop: the lookup is asked about the union,
	// once, because the endpoint is rate limited and a shared hop is one address.
	prober := &fakeProber{paths: map[string]Path{
		"219.141.140.10": {Reached: true, Hops: []Hop{answered(1, "203.0.113.9", time.Millisecond), answered(2, "219.141.140.10", time.Millisecond)}},
		"202.106.195.68": {Reached: true, Hops: []Hop{answered(1, "203.0.113.9", time.Millisecond), answered(2, "202.106.195.68", time.Millisecond)}},
	}}
	lookup := &fakeLookup{facts: map[string]Fact{
		"203.0.113.9":    {Country: "US", Known: true, ISP: "Transit", ASN: 64500},
		"219.141.140.10": cnFact(4134, "Chinanet", "CHINANET"),
		"202.106.195.68": cnFact(4837, "China Unicom", "CHINA169"),
	}}
	runner := Runner{Prober: prober, Lookup: lookup, Targets: []Target{
		at("北京电信", "219.141.140.10"), at("北京联通", "202.106.195.68")}}

	if _, err := runner.Run(context.Background(), toolbox.Options{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := []string{"202.106.195.68", "203.0.113.9", "219.141.140.10"}
	if !reflect.DeepEqual(lookup.asked, want) {
		t.Errorf("lookup was asked about %v, want %v", lookup.asked, want)
	}
}

func TestRunWithoutPermissionReturnsAClearError(t *testing.T) {
	prober := &fakeProber{}
	runner := Runner{
		NewProber: func() (Prober, error) { return nil, socketError(os.ErrPermission) },
		Lookup:    &fakeLookup{},
		Targets:   []Target{at("北京电信", "219.141.140.10")},
		// Prober stays nil so NewProber is what the run calls.
	}
	res, err := runner.Run(context.Background(), toolbox.Options{})
	if err == nil {
		t.Fatal("Run returned no error for a run that cannot open a socket")
	}
	if !strings.Contains(err.Error(), "root") || !strings.Contains(err.Error(), "CAP_NET_RAW") {
		t.Errorf("error = %q, want it to name root and CAP_NET_RAW", err)
	}
	if len(res.Rows) != 0 || res.Summary != "" {
		t.Errorf("result = %+v, want nothing reported when nothing could be measured", res)
	}
	if prober.wasClosed() {
		t.Error("the unused prober was closed")
	}
}

func TestRunUsesTheInjectedClient(t *testing.T) {
	// The whole chain with a fake HTTP client instead of a fake Lookup: the
	// toolbox's injected client is what reaches ip-api.
	prober := &fakeProber{paths: map[string]Path{
		"219.141.140.10": {Reached: true, Hops: []Hop{answered(1, "219.141.140.10", 12*time.Millisecond)}},
	}}
	client := successClient(t, "AS4134", "CHINANET-BACKBONE")
	runner := Runner{Prober: prober, Targets: []Target{at("北京电信", "219.141.140.10")}}

	res, err := runner.Run(context.Background(), toolbox.Options{Client: client, Timeout: time.Minute})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := [][]string{{"北京电信 219.141.140.10", "电信(163)", "219.141.140.10", "12ms"}}
	if !reflect.DeepEqual(res.Rows, want) {
		t.Errorf("rows = %v, want %v", res.Rows, want)
	}
	if len(client.calls) != 1 {
		t.Errorf("requests = %v, want one batch call", client.calls)
	}
}

func TestRunReportsCancellationAsUnfinishedTargets(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	prober := &fakeProber{paths: map[string]Path{}}
	runner := Runner{Prober: prober, Lookup: &fakeLookup{}, Targets: []Target{at("北京电信", "219.141.140.10")}}

	res, err := runner.Run(ctx, toolbox.Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Rows) != 1 || res.Rows[0][1] != "无法判定" {
		t.Errorf("rows = %v, want one undecided row", res.Rows)
	}
	if !strings.Contains(strings.Join(res.Notes, "\n"), "探测失败") {
		t.Errorf("notes = %v, want the cancellation explained", res.Notes)
	}
}

func TestRunRespectsTheToolboxTimeout(t *testing.T) {
	prober := &fakeProber{block: true}
	runner := Runner{Prober: prober, Lookup: &fakeLookup{}, Targets: []Target{at("北京电信", "219.141.140.10")}}

	start := time.Now()
	res, err := runner.Run(context.Background(), toolbox.Options{Timeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("run took %v, want it bounded by the toolbox timeout", elapsed)
	}
	if len(res.Rows) != 1 || res.Rows[0][1] != "无法判定" {
		t.Errorf("rows = %v, want one undecided row", res.Rows)
	}
	if !strings.Contains(strings.Join(res.Notes, "\n"), "探测失败") {
		t.Errorf("notes = %v, want the budget explained", res.Notes)
	}
}

func TestDefaultTargets(t *testing.T) {
	targets := DefaultTargets()
	if len(targets) != 12 {
		t.Fatalf("targets = %d, want 12 (four cities times three carriers)", len(targets))
	}
	cities := map[string]bool{"北京": false, "上海": false, "广州": false, "成都": false}
	carriers := map[string]bool{"电信": false, "联通": false, "移动": false}
	seen := map[string]bool{}
	for _, target := range targets {
		if target.IP == nil || target.IP.To4() == nil {
			t.Fatalf("%s has no IPv4 address", target.Name)
		}
		if seen[target.IP.String()] {
			t.Errorf("%s is listed twice", target.IP)
		}
		seen[target.IP.String()] = true
		matched := false
		for city := range cities {
			for carrier := range carriers {
				if target.Name == city+carrier {
					cities[city], carriers[carrier] = true, true
					matched = true
				}
			}
		}
		if !matched {
			t.Errorf("unexpected target name %q", target.Name)
		}
	}
	for city, ok := range cities {
		if !ok {
			t.Errorf("no target for %s", city)
		}
	}
	for carrier, ok := range carriers {
		if !ok {
			t.Errorf("no target for %s", carrier)
		}
	}
	// The table is a copy: a caller must not be able to edit the package's.
	targets[0].Name = "changed"
	if DefaultTargets()[0].Name != "北京电信" {
		t.Error("DefaultTargets handed out the package's own table")
	}
}

func TestToolEntry(t *testing.T) {
	tool := Tool()
	if tool.ID != ID || tool.ID == "" {
		t.Errorf("Tool().ID = %q, want %q", tool.ID, ID)
	}
	if tool.Group != Group {
		t.Errorf("Tool().Group = %q, want %q", tool.Group, Group)
	}
	if tool.Run == nil {
		t.Fatal("Tool().Run is nil")
	}
}

func TestFormatMS(t *testing.T) {
	tests := map[time.Duration]string{
		0:                        "—",
		12 * time.Millisecond:    "12ms",
		12500 * time.Microsecond: "12.5ms",
		999 * time.Microsecond:   "1ms",
	}
	for in, want := range tests {
		if got := formatMS(in); got != want {
			t.Errorf("formatMS(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestSummaryLine(t *testing.T) {
	tests := []struct {
		name      string
		best      map[Carrier]time.Duration
		undecided int
		want      string
	}{
		{
			name: "the three carriers",
			best: map[Carrier]time.Duration{
				CarrierTelecom: 12 * time.Millisecond,
				CarrierUnicom:  15 * time.Millisecond,
				CarrierMobile:  18 * time.Millisecond,
			},
			want: "电信 12ms · 联通 15ms · 移动 18ms",
		},
		{
			name:      "nothing settled",
			best:      map[Carrier]time.Duration{},
			undecided: 2,
			want:      "2 个目标全部无法判定",
		},
		{
			name:      "partly settled",
			best:      map[Carrier]time.Duration{CarrierTelecom: 12 * time.Millisecond},
			undecided: 2,
			want:      "电信 12ms · 2 个目标未判定",
		},
		{
			name: "international only",
			best: map[Carrier]time.Duration{CarrierInternational: 30 * time.Millisecond},
			want: "国际多线 30ms",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := summaryLine(tt.best, tt.undecided); got != tt.want {
				t.Errorf("summaryLine = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestJudgeReadsFromTheDestinationSide(t *testing.T) {
	// The hop nearest the target decides: a 电信 hop behind a last mile that is
	// demonstrably not a carrier's does not make the path 电信.
	o := outcome{
		target: at("北京电信", "219.141.140.10"),
		path: Path{Reached: true, Hops: []Hop{
			answered(1, "203.0.113.9", 30*time.Millisecond),
			{TTL: 2}, // nothing answered
			answered(3, "219.141.140.10", 12*time.Millisecond),
		}},
	}
	facts := map[string]Fact{
		"203.0.113.9":    cnFact(4134, "Chinanet", "CHINANET-BACKBONE"),
		"219.141.140.10": cnFact(4538, "CERNET", "CHINA EDUCATION AND RESEARCH NETWORK"),
	}
	v := judge(o, facts)
	if v.carrier != CarrierInternational || v.backbone != "AS4538" {
		t.Errorf("verdict = %q/%q, want 国际多线/AS4538", v.carrier, v.backbone)
	}
	if v.hop.IP.String() != "219.141.140.10" {
		t.Errorf("verdict hop = %v, want the hop the reading came from", v.hop.IP)
	}
	if !strings.Contains(v.evidence, "AS4538") {
		t.Errorf("evidence = %q, want the system that was seen", v.evidence)
	}
}

func TestJudgeNamesMainlandPathsThatSettleNothing(t *testing.T) {
	o := outcome{
		target: at("北京电信", "219.141.140.10"),
		path:   Path{Hops: []Hop{answered(1, "203.0.113.9", time.Millisecond)}},
	}
	// Data about a mainland hop, but nothing that names a network.
	v := judge(o, map[string]Fact{"203.0.113.9": {Country: "CN", Known: true}})
	if v.carrier != CarrierUnknown {
		t.Errorf("carrier = %q, want 无法判定", v.carrier)
	}
	if !strings.Contains(v.reason, "没有 ISP/ASN") {
		t.Errorf("reason = %q, want it to name the missing evidence", v.reason)
	}
}
