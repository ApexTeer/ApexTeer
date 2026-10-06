// Package backtrace reports which mainland carrier a host's traffic comes back
// through, one target at a time.
//
// The question is the one the fused ecs review script
// (github.com/spiritLHLS/ecs) asks in its 三网回程 step: trace to a handful of
// addresses that the three carriers serve in 北京/上海/广州/成都, read the systems
// the last hops belong to, and say whether the return path is 电信, 联通, 移动 or
// something else. It is the reading an overseas VPS owner wants before deciding
// whether a node is worth keeping: a 163 path and a CN2 path are the same
// carrier and a different experience.
//
// Three rules shape the tool, and all three come from the panel's job being to
// tell an operator the truth about a host:
//
//   - The path is measured, not assumed. This package owns its ICMP traceroute
//     rather than shelling out to tracepath or believing a third party's view of
//     the route.
//   - A verdict without evidence is not reported. A carrier name comes from ASN
//     and ISP facts about hops that actually answered; a silent hop, an address
//     ip-api has no entry for, or a batch that was rate limited all end in
//     无法判定 with the reason in Notes, never a guess.
//   - Everything outside the process is injected. The traceroute's socket sits
//     behind the Prober interface and the address lookup behind Lookuper, so the
//     tests run without root, without a network and without waiting out the rate
//     limit.
package backtrace

import (
	"context"
	"fmt"
	"math"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/EasySB-Team/EasySB/internal/toolbox"
)

// ID is the toolbox node id, the i18n key suffix ("toolbox_"+ID) and the --tool
// argument.
const ID = "backtrace"

// Group files this tool under the toolbox's network section.
const Group = "network"

// Carrier is the network a return path was judged to come back through.
type Carrier string

const (
	// CarrierTelecom is 中国电信: 163, CN2 or CTGNet.
	CarrierTelecom Carrier = "telecom"
	// CarrierUnicom is 中国联通: 4837, 9929 or CUG.
	CarrierUnicom Carrier = "unicom"
	// CarrierMobile is 中国移动: CMI or CMIN2.
	CarrierMobile Carrier = "mobile"
	// CarrierInternational is a mainland path that none of the three carries:
	// a foreign transit delivering the last mile, an education or campus
	// network, a small regional provider. The evidence line names the system
	// that was actually seen, so the label never overstates what is known.
	CarrierInternational Carrier = "international"
	// CarrierUnknown is a target the run could not settle. Every unknown verdict
	// carries a reason.
	CarrierUnknown Carrier = "unknown"
)

// Label is the name the report prints for a carrier.
func (c Carrier) Label() string {
	switch c {
	case CarrierTelecom:
		return "电信"
	case CarrierUnicom:
		return "联通"
	case CarrierMobile:
		return "移动"
	case CarrierInternational:
		return "国际多线"
	default:
		return "无法判定"
	}
}

// Column labels of the report table.
const (
	headerTarget = "目标"
	headerLine   = "回程线路"
	headerHop    = "末跳"
	headerRTT    = "时延"
)

// Target is one address the tool traces to.
type Target struct {
	// Name is the city and the carrier, as the reference lists name them.
	Name string
	// IP is the address itself.
	IP net.IP
}

// outcome is everything one target contributed to the report.
type outcome struct {
	target Target
	path   Path
	// err is set when the target could not be probed at all.
	err error
}

// verdict is the judgement for one traced path.
type verdict struct {
	carrier  Carrier
	backbone string
	// hop is the hop the carrier was read from. It is not necessarily the last
	// hop that answered: a silent stretch near the target is skipped, so a note
	// can name where a reading actually came from.
	hop Hop
	// reason explains a CarrierUnknown verdict.
	reason string
	// evidence names the system behind a 国际多线 reading, for Notes.
	evidence string
}

// Runner holds the two things a backtrace run needs from outside the process,
// plus the knobs a caller may want to turn. The zero value is what the panel
// uses, and Run is Runner{}.Run.
type Runner struct {
	// Prober traces one target's path. Nil builds the real ICMP traceroute,
	// which needs a raw socket (root, or CAP_NET_RAW).
	Prober Prober
	// NewProber builds that prober lazily. Nil means the real one. A test sets
	// it to prove the no-permission path without a privileged socket; an
	// operator whose panel has no CAP_NET_RAW could point it somewhere else.
	// The config knobs below apply to the real prober only.
	NewProber func() (Prober, error)
	// Lookup resolves hop addresses to ASN and ISP facts. Nil uses ip-api.com's
	// free batch endpoint over the client the toolbox injects.
	Lookup Lookuper
	// Targets is the target table. Nil means DefaultTargets().
	Targets []Target
	// Concurrency is how many targets are traced at once. Default 4.
	Concurrency int
}

// Run is the toolbox entry point: trace the default target table with the
// injected client and return the table the panel draws.
func Run(ctx context.Context, opts toolbox.Options) (toolbox.Result, error) {
	return Runner{}.Run(ctx, opts)
}

// Tool returns this tool's registry entry.
func Tool() toolbox.Tool {
	return toolbox.Tool{ID: ID, Group: Group, Run: Run}
}

// Run traces every target, judges the return path of each, and reports the
// table. The error return is for a run that could not start — a panel without
// the privilege to open a raw socket — and never for a target that answered
// nothing: those are rows with 无法判定 and a note saying why.
//
// The run is bounded by the toolbox timeout, and a run that runs out of budget
// still reports what it measured so far.
func (r Runner) Run(ctx context.Context, opts toolbox.Options) (toolbox.Result, error) {
	opts.Log = toolbox.SafeLog(opts.Log)
	ctx, cancel := context.WithTimeout(ctx, opts.Duration())
	defer cancel()
	targets := r.targets()

	prober, err := r.prober()
	if err != nil {
		// Nothing can be measured, so there is no table to fill: say what is
		// wrong instead of printing twelve unreachable targets.
		return toolbox.Result{}, err
	}
	defer func() { _ = prober.Close() }()

	opts.Logf("三网回程：探测 %d 个目标", len(targets))
	outcomes := r.probeAll(ctx, prober, targets, opts)

	// One lookup covers every hop that answered, so the rate limit is spent once
	// per run instead of once per target.
	facts := make(map[string]Fact)
	var lookupErr error
	if ips := hopIPs(outcomes); len(ips) > 0 {
		opts.Logf("三网回程：查询 %d 个跳地址的归属", len(ips))
		got, err := r.lookup(opts).Lookup(ctx, ips)
		lookupErr = err
		for ip, f := range got {
			facts[ip] = f
		}
	}

	res := toolbox.Result{Headers: []string{headerTarget, headerLine, headerHop, headerRTT}}
	res.Notes = sourceNotes()
	if lookupErr != nil {
		res.Note("依赖地址查询失败，本轮所有目标记为无法判定：%v", lookupErr)
	}

	best := make(map[Carrier]time.Duration)
	undecided := 0
	for _, o := range outcomes {
		v := judge(o, facts)
		res.Rows = append(res.Rows, row(o, v))
		if v.carrier == CarrierUnknown {
			undecided++
		} else {
			best[v.carrier] = minDuration(best[v.carrier], v.hop.RTT)
		}
		res.Notes = append(res.Notes, targetNotes(o, v)...)
	}

	res.Summary = summaryLine(best, undecided)
	opts.Logf("三网回程：%s", res.Summary)
	return res, nil
}

// targets is the table this run walks: the injected one, or the default.
func (r Runner) targets() []Target {
	if r.Targets != nil {
		return r.Targets
	}
	return DefaultTargets()
}

// prober returns the prober to trace with, building the real ICMP one when the
// runner carries neither an implementation nor a constructor.
func (r Runner) prober() (Prober, error) {
	if r.Prober != nil {
		return r.Prober, nil
	}
	if r.NewProber != nil {
		return r.NewProber()
	}
	return newICMPProber(defaultTraceConfig)
}

// lookup returns the address lookup to use, defaulting to ip-api over the
// injected HTTP client. The pacing of that client and the degradation of a
// failed batch are described at apiLookup.
func (r Runner) lookup(opts toolbox.Options) Lookuper {
	if r.Lookup != nil {
		return r.Lookup
	}
	return newAPILookup(opts.HTTP(), opts.Now)
}

// concurrency is how many targets are traced at once. Four keeps the worst case
// (twelve targets, every hop silent, three probes per hop) inside the toolbox's
// five-minute budget, and the prober shares one raw socket across all of them.
func (r Runner) concurrency() int {
	if r.Concurrency > 0 {
		return r.Concurrency
	}
	return 4
}

// probeAll traces every target, at most concurrency at a time, and keeps the
// results in table order. One target's failure never stops the others: a path
// that could not be measured is a row that says so.
func (r Runner) probeAll(ctx context.Context, p Prober, targets []Target, opts toolbox.Options) []outcome {
	out := make([]outcome, len(targets))
	sem := make(chan struct{}, r.concurrency())
	var wg sync.WaitGroup
	// The targets are traced concurrently, so the count of finished targets is what the panel
	// shows: the number is exact and the order is whatever finished first.
	var finished atomic.Int64
	for i, t := range targets {
		wg.Add(1)
		go func(i int, t Target) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := ctx.Err(); err != nil {
				out[i] = outcome{target: t, err: err}
				return
			}
			opts.Logf("三网回程：%s %s", t.Name, t.IP)
			path, err := p.Trace(ctx, t.IP)
			out[i] = outcome{target: t, path: path, err: err}
			opts.ReportProgress(int(finished.Add(1)), len(targets), t.Name)
		}(i, t)
	}
	wg.Wait()
	return out
}

// hopIPs returns every address that answered, deduplicated and sorted. One
// lookup call carries them all, and a hop two targets share is asked about once.
func hopIPs(outcomes []outcome) []net.IP {
	seen := make(map[string]net.IP)
	for _, o := range outcomes {
		for _, h := range o.path.Hops {
			if h.IP == nil {
				continue
			}
			seen[h.IP.String()] = h.IP
		}
	}
	out := make([]net.IP, 0, len(seen))
	for _, ip := range seen {
		out = append(out, ip)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// judge reads a path from the destination side.
//
// The network nearest the target is the one that answers for the return path, so
// the first hop that can be named decides and nothing further back is consulted:
// a 电信 hop behind a CERNET last mile does not make the path 电信. Hops nothing
// is known about, and hops outside the mainland, are skipped but counted, which
// is what keeps the reason on an unsettled target honest — "no data at all" and
// "inside China but no name for it" are different answers.
func judge(o outcome, facts map[string]Fact) verdict {
	if o.err != nil {
		return verdict{carrier: CarrierUnknown, reason: fmt.Sprintf("探测失败（%v）", o.err)}
	}
	var (
		reason      string // why the hop nearest the target settled nothing
		usable      bool   // at least one hop had an answer from the lookup
		sawMainland bool
		answered    bool
	)
	for i := len(o.path.Hops) - 1; i >= 0; i-- {
		h := o.path.Hops[i]
		if h.IP == nil {
			continue
		}
		answered = true
		f, have := facts[h.IP.String()]
		n := nameFact(f, have)
		if n.usable {
			usable = true
		}
		if n.known {
			return verdict{carrier: n.carrier, backbone: n.backbone, hop: h, evidence: n.evidence}
		}
		if reason == "" && n.reason != "" {
			reason = n.reason
		}
		if n.cn {
			sawMainland = true
		}
	}
	switch {
	case !answered:
		return verdict{carrier: CarrierUnknown, reason: "24 跳内没有任何应答"}
	case !usable:
		if reason == "" {
			reason = "ip-api 没有返回这些地址的数据"
		}
		return verdict{carrier: CarrierUnknown, reason: reason}
	case !sawMainland:
		return verdict{carrier: CarrierUnknown, reason: "路径未进入中国大陆"}
	case reason == "":
		return verdict{carrier: CarrierUnknown, reason: "路径上没有可判定归属的跳"}
	default:
		return verdict{carrier: CarrierUnknown, reason: reason}
	}
}

// row renders one target's line: the target, the carrier judged for it, the last
// hop that answered and that hop's latency.
func row(o outcome, v verdict) []string {
	last, _ := o.path.LastHop()
	return []string{targetCell(o.target), lineCell(v), hopCell(last), formatMS(last.RTT)}
}

// targetCell names a target with its address, so a row can be checked against
// the table without a second lookup.
func targetCell(t Target) string {
	if t.IP == nil {
		return t.Name
	}
	return t.Name + " " + t.IP.String()
}

// lineCell renders the 回程线路 column: the carrier, plus the backbone that named
// it when the AS number did.
func lineCell(v verdict) string {
	if v.carrier == CarrierUnknown {
		return CarrierUnknown.Label()
	}
	if v.backbone == "" {
		return v.carrier.Label()
	}
	return fmt.Sprintf("%s(%s)", v.carrier.Label(), v.backbone)
}

// hopCell renders the 末跳 column. A path nothing answered on has no last hop,
// which is not the same as a latency of zero.
func hopCell(h Hop) string {
	if h.IP == nil {
		return "无应答"
	}
	return h.IP.String()
}

// formatMS renders a latency the way the report prints it: whole milliseconds
// when the reading lands on one, one decimal otherwise, and a dash when there
// was no reading at all.
func formatMS(d time.Duration) string {
	if d <= 0 {
		return "—"
	}
	ms := d.Seconds() * 1000
	if math.Abs(ms-math.Round(ms)) < 0.05 {
		return fmt.Sprintf("%.0fms", ms)
	}
	return fmt.Sprintf("%.1fms", ms)
}

// summaryLine is the one line the toolbox board shows: the fastest last-hop
// latency per carrier the run settled, then how many targets it could not
// settle, because a carrier with one slow city is still that carrier.
func summaryLine(best map[Carrier]time.Duration, undecided int) string {
	var parts []string
	for _, c := range []Carrier{CarrierTelecom, CarrierUnicom, CarrierMobile, CarrierInternational} {
		if d, ok := best[c]; ok {
			parts = append(parts, c.Label()+" "+formatMS(d))
		}
	}
	switch {
	case len(parts) == 0 && undecided == 0:
		return "没有可判定的目标"
	case len(parts) == 0:
		return fmt.Sprintf("%d 个目标全部无法判定", undecided)
	}
	line := strings.Join(parts, " · ")
	if undecided > 0 {
		line += fmt.Sprintf(" · %d 个目标未判定", undecided)
	}
	return line
}

// minDuration keeps the smaller of two latencies, treating zero as "none yet".
func minDuration(cur, d time.Duration) time.Duration {
	if cur == 0 || (d > 0 && d < cur) {
		return d
	}
	return cur
}

// sourceNotes are the lines that say where the numbers come from, printed before
// anything about an individual target.
func sourceNotes() []string {
	return []string{
		targetSourceNote,
		"线路判定：ip-api.com/batch（免费接口，45 次/分钟限流，一批最多 100 个 IP）；被限流或查询失败时，涉及的地址按无法判定处理，不猜",
		"Summary 中每家的时延取该家各城市末跳时延的最小值",
	}
}

// targetNotes explains one row: why it could not be judged, what settled a
// reading that is not a carrier's own name, and what an unanswered target means
// for the 末跳 column.
func targetNotes(o outcome, v verdict) []string {
	id := targetCell(o.target)
	var out []string
	switch {
	case o.err != nil:
		out = append(out, fmt.Sprintf("目标 %s：探测失败（%v）", id, o.err))
	case v.carrier == CarrierUnknown:
		out = append(out, fmt.Sprintf("目标 %s：无法判定（%s）", id, v.reason))
	case v.carrier == CarrierInternational:
		out = append(out, fmt.Sprintf("目标 %s：末段网络 %s（%s），非三大运营商，因此记为国际多线",
			id, v.evidence, hopCell(v.hop)))
	}
	if o.err == nil && !o.path.Reached {
		out = append(out, fmt.Sprintf("目标 %s：%d 跳内目标未应答，末跳一列是最后一台应答的路由器",
			id, defaultTraceConfig.maxHops))
	}
	return out
}
