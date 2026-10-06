// Package speed measures the host's throughput against speedtest.net.
//
// The two entries the toolbox lists here answer the same question with a different
// server list. 就近测速 takes the nearest few servers wherever the host is; 三网测速
// takes one server per mainland Chinese carrier, which is the reading an operator of a
// mainland-facing node is after. They share one package because the measurement path is
// identical — only the choice of servers differs.
//
// Two rules shape it:
//
//   - Choosing what to test is a pure function of the fetched list, so a test drives it
//     with a hand-written list and never the network.
//   - Measuring goes through the runner interface. The panel's runner talks to
//     speedtest.net; a test's runner answers from a table.
package speed

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/EasySBTeam/EasySB/internal/toolbox"
)

// Group is the toolbox group both entries are filed under.
const Group = "network"

// Tool ids. An id is the registry key, the internal/i18n key suffix ("toolbox_"+id) and
// the --tool argument.
const (
	// IDNearby is the generic nearest-server speed test.
	IDNearby = "speed-near"
	// IDCarriers is the mainland carrier speed test.
	IDCarriers = "speed-cn"
)

// DefaultNodes is how many servers 就近测速 tests. The reference implementation
// (融合怪 ecs) tests four, and four also happens to fit the default timeout: one server
// costs roughly half a minute.
const DefaultNodes = 4

// Tools returns this package's toolbox entries, in menu order.
func Tools() []toolbox.Tool {
	return []toolbox.Tool{
		{ID: IDNearby, Group: Group, Run: Nearby},
		{ID: IDCarriers, Group: Group, Run: Carriers},
	}
}

// Nearby measures the nearest DefaultNodes servers of the public speedtest.net list:
// latency, download and upload per server, plus the average over the servers that
// answered.
//
// A server that cannot be measured is skipped with a note and does not fail the run;
// only the server list itself can fail it, because without a list there is nothing to
// measure and no reading to report.
func Nearby(ctx context.Context, opts toolbox.Options) (toolbox.Result, error) {
	return nearby(ctx, opts, newRunner(opts))
}

// Carriers measures one server per mainland China carrier (no match is reported as
// such, never filled in with a neighbouring server).
func Carriers(ctx context.Context, opts toolbox.Options) (toolbox.Result, error) {
	return carriers(ctx, opts, newRunner(opts))
}

// nearby is Nearby with the runner injected, so a test runs the whole tool against a
// table instead of the network.
func nearby(ctx context.Context, opts toolbox.Options, r runner) (toolbox.Result, error) {
	return execute(ctx, opts, r, plan{
		title: "就近测速",
		pick:  func(list []server) []server { return pickNearest(list, DefaultNodes) },
		rule: func([]server) string {
			return fmt.Sprintf("选取规则：本机最近 %d 个节点，逐个测延迟 / 下行 / 上行，总计取均值。", DefaultNodes)
		},
		empty: func(total int) string {
			if total == 0 {
				return "speedtest.net 返回的服务器列表为空，没有可测节点。"
			}
			return fmt.Sprintf("从 %d 个节点里没有选出可测节点。", total)
		},
		nothing: "没有可测节点",
	})
}

// carriers is Carriers with the runner injected.
func carriers(ctx context.Context, opts toolbox.Options, r runner) (toolbox.Result, error) {
	return execute(ctx, opts, r, plan{
		title: "三网测速",
		pick:  pickCarriers,
		rule: func(targets []server) string {
			return "选取规则：节点名或提供商包含 电信 / 联通 / 移动，每个运营商取距离最近的一个；本次 " +
				strings.Join(carrierCounts(targets), " · ") + "。"
		},
		empty: func(total int) string {
			if total == 0 {
				return "没有匹配到三网节点：speedtest.net 返回的服务器列表为空。"
			}
			return fmt.Sprintf("没有匹配到三网节点：列表里的 %d 个节点，名字或提供商都不含 电信 / 联通 / 移动。", total)
		},
		nothing: "没有匹配到三网节点",
		missing: missingCarriers,
	})
}

// plan is the part of a run that differs between the two entries: what to test and how
// to describe the choice.
type plan struct {
	// title heads the summary line and every progress line, e.g. 就近测速.
	title string
	// pick chooses the servers to test from the fetched list.
	pick func([]server) []server
	// rule explains the choice in a note, given what was chosen.
	rule func(targets []server) string
	// empty is the note for a choice that came up with nothing; it is given the size
	// of the list it had to choose from.
	empty func(total int) string
	// nothing is the summary tail for a run with no node to test.
	nothing string
	// missing adds a note about a carrier the choice found no server for. Nil when the
	// plan expects nothing in particular.
	missing func(targets []server) string
}

// execute is the shared body of both entries: fetch the list, let the plan choose
// targets, then measure them one after another.
//
// The servers are measured sequentially on purpose. Parallel tests share the host's
// uplink, so each one would measure the other's traffic rather than the node's speed.
func execute(ctx context.Context, opts toolbox.Options, r runner, p plan) (toolbox.Result, error) {
	res := toolbox.Result{Headers: headers()}

	// The whole run is bounded, not just each node. The library's client has no timeout
	// of its own, so without this a stalled connection to speedtest.net would leave the
	// panel's task hanging with nothing to report.
	rctx, cancel := context.WithTimeout(ctx, opts.Duration())
	defer cancel()

	opts.Logf("%s：正在获取 speedtest.net 服务器列表…", p.title)
	list, err := r.servers(rctx)
	if err != nil {
		return toolbox.Result{}, fmt.Errorf("%s：获取 speedtest.net 服务器列表失败：%w", p.title, err)
	}
	res.Note("数据来源：speedtest.net 公共服务器列表。")

	targets := p.pick(list)
	if len(targets) == 0 {
		res.Note("%s", p.empty(len(list)))
		res.Summary = p.title + "：" + p.nothing
		return res, nil
	}
	// Notes built from list data go through "%s": a server's name is text from
	// speedtest.net and may well contain a percent sign.
	res.Note("%s", p.rule(targets))
	if span, ok := selectionDistances(targets); ok {
		res.Note("已选节点距离本机 %s（距离由 speedtest.net 按本机坐标计算）。", span)
	}
	if p.missing != nil {
		if note := p.missing(targets); note != "" {
			res.Note("%s", note)
		}
	}

	// One server gets an even share of the whole-tool budget: a server that hangs
	// spends its own slice, and the servers after it still get theirs.
	perNode := opts.Duration() / time.Duration(len(targets))

	var (
		got     []measured
		skipped int
		halt    haltReason
	)
	for i, srv := range targets {
		if halt = stopped(ctx, rctx); halt != haltNone {
			break
		}
		opts.Logf("%s：正在测试 %s（%d/%d）…", p.title, nodeName(srv), i+1, len(targets))
		s := measureOne(rctx, r, srv, perNode)
		// The step is counted whether or not the node produced a reading: the bar reports
		// how far the run has got through the list, and a skipped node is still done.
		opts.ReportProgress(i+1, len(targets), nodeLabel(srv))
		if s.err != nil {
			skipped++
			res.Note("跳过 %s：%s。", nodeLabel(srv), s.err)
			continue
		}
		got = append(got, measured{server: srv, sample: s.sample})
	}
	// A run whose last node was cut off by the whole-run deadline has to say so even when
	// the loop had no next iteration to notice it: otherwise the final line blames the
	// nodes for a budget the caller set.
	if halt == haltNone && rctx.Err() != nil && ctx.Err() == nil {
		halt = haltBudget
	}
	switch halt {
	case haltCancelled:
		res.Note("测速被中断：完成 %d/%d 个节点。", len(got), len(targets))
	case haltBudget:
		res.Note("已用完本次总时限 %s：完成 %d/%d 个节点。", budgetText(opts.Duration()), len(got), len(targets))
	}

	if len(got) == 0 {
		switch halt {
		case haltCancelled:
			res.Summary = p.title + "：测速已中断，未测得节点"
		case haltBudget:
			res.Summary = p.title + "：已用完总时限，未测得节点"
		default:
			res.Summary = fmt.Sprintf("%s：%d 个节点全部测速失败", p.title, len(targets))
		}
		return res, nil
	}

	total := average(got)
	for _, m := range got {
		res.Rows = append(res.Rows, row(m.server, m.sample))
	}
	res.Rows = append(res.Rows, totalRow(total))
	res.Note("总计为已测得节点的算术平均（延迟 / 下行 / 上行）；被跳过的节点不计入。")
	if skipped > 0 {
		res.Note("本次跳过 %d 个测速失败的节点。", skipped)
	}
	summary := fmt.Sprintf("%s：下行 %s · 上行 %s · %d 节点", p.title, total.download, total.upload, len(got))
	switch halt {
	case haltCancelled:
		summary += "（已中断）"
	case haltBudget:
		summary += "（超时）"
	}
	res.Summary = summary
	// The board already labels the row with the tool's name, so its line carries the numbers
	// an operator compares: the averages, and how many nodes they came from.
	res.Board = fmt.Sprintf("下行 %s · 上行 %s · %s · %d 节点",
		total.download.String(), total.upload.String(), latencyCell(total.latency), len(got))
	return res, nil
}

// haltReason says why a run stopped before it ran out of servers.
type haltReason string

const (
	// haltNone means the run measured every server it picked.
	haltNone haltReason = ""
	// haltCancelled means the panel's task was cancelled, so the loop stopped where it
	// was and reports what it already measured.
	haltCancelled haltReason = "cancelled"
	// haltBudget means the run used up its whole-tool timeout. It is not the same thing
	// as a cancellation and is reported as a timeout, because what the operator has to
	// change is the timeout or the number of nodes, not their own patience.
	haltBudget haltReason = "budget"
)

// stopped classifies the reason the loop must not start another server. The caller's
// context is checked first: a cancellation is the operator's doing, and it should not be
// reported as a timeout.
func stopped(ctx, rctx context.Context) haltReason {
	switch {
	case ctx.Err() != nil:
		return haltCancelled
	case rctx.Err() != nil:
		return haltBudget
	default:
		return haltNone
	}
}

// budgetText renders a budget for a note. Sub-second budgets come up with a deliberately
// tiny Timeout (and in tests), where Round(time.Second) would print a bare "0s".
func budgetText(d time.Duration) string {
	if d < time.Second {
		return d.Round(time.Millisecond).String()
	}
	return d.Round(time.Second).String()
}

// measured pairs a server with the reading it produced.
type measured struct {
	server server
	sample sample
}

// outcome is one server's measurement, as the run needs it: either a reading or the
// reason there is none.
type outcome struct {
	sample sample
	err    error
}

// measureOne runs one server under its share of the budget. A node that ran out of its
// slice says so: "context deadline exceeded" alone reads like a panel-side bug rather
// than a slow server. The whole-run deadline is left to the caller, which reports the
// interruption itself.
func measureOne(ctx context.Context, r runner, srv server, budget time.Duration) outcome {
	nctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	s, err := r.measure(nctx, srv)
	if err != nil {
		if ctx.Err() == nil && errors.Is(nctx.Err(), context.DeadlineExceeded) {
			return outcome{err: fmt.Errorf("超过单节点时限 %s", budgetText(budget))}
		}
		return outcome{err: err}
	}
	return outcome{sample: s}
}

// headers returns a fresh copy of the column labels, so a caller that edits a result's
// headers cannot change what the next run reports.
func headers() []string {
	return []string{"节点", "提供商", "延迟", "下行", "上行"}
}

// row renders one server's measurements. Every rate is printed in Mbps and every
// latency in ms, so two rows can be compared straight down a column.
func row(srv server, s sample) []string {
	return []string{nodeName(srv), srv.sponsor, latencyCell(s.latency), s.download.String(), s.upload.String()}
}

// totalRow is the last line of the table: the same columns, averaged.
func totalRow(s sample) []string {
	return []string{"总计（均值）", "", latencyCell(s.latency), s.download.String(), s.upload.String()}
}

// latencyCell rounds a duration to whole milliseconds. A fraction of a millisecond is
// noise on an internet path, and one number per cell is what keeps the column readable.
func latencyCell(d time.Duration) string {
	return strconv.FormatInt(int64(math.Round(float64(d)/float64(time.Millisecond))), 10) + " ms"
}

// average is the arithmetic mean of what was measured. Averaging the servers that
// answered, rather than naming the best one, keeps the line honest when the list spans
// several regions or a node was skipped.
func average(ms []measured) sample {
	var (
		latency          time.Duration
		download, upload rate
	)
	for _, m := range ms {
		latency += m.sample.latency
		download += m.sample.download
		upload += m.sample.upload
	}
	n := len(ms)
	return sample{
		latency:  latency / time.Duration(n),
		download: download / rate(n),
		upload:   upload / rate(n),
	}
}

// nodeName is the server as the table names it. speedtest.net labels a server with its
// place, so that is the name; the host and the id are fallbacks, because a cell is never
// left empty.
func nodeName(srv server) string {
	switch {
	case srv.name != "":
		return srv.name
	case srv.host != "":
		return srv.host
	case srv.id != "":
		return srv.id
	default:
		return "未知节点"
	}
}

// nodeLabel names a server in a note, with the id speedtest.net knows it by.
func nodeLabel(srv server) string {
	if srv.id == "" || srv.id == nodeName(srv) {
		return nodeName(srv)
	}
	return srv.id + " " + nodeName(srv)
}

// selectionDistances describes how far the chosen servers are, when the list reported
// distances at all. It keeps the "nearest" claim checkable instead of asserted.
func selectionDistances(servers []server) (string, bool) {
	var (
		nearest, furthest float64
		known             int
	)
	for _, srv := range servers {
		if srv.distance <= 0 {
			continue
		}
		if known == 0 || srv.distance < nearest {
			nearest = srv.distance
		}
		if known == 0 || srv.distance > furthest {
			furthest = srv.distance
		}
		known++
	}
	if known == 0 {
		return "", false
	}
	return fmt.Sprintf("%.2f km ～ %.2f km", nearest, furthest), true
}

// missingCarriers names the carriers the selection found no server for. A carrier with
// no node in the list is reported as missing rather than filled in with a server of
// another network: that is the whole point of a 三网 test.
func missingCarriers(targets []server) string {
	var missing []string
	for _, carrier := range carrierNames {
		found := false
		for _, srv := range targets {
			if carrierOf(srv) == carrier {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, carrier)
		}
	}
	if len(missing) == 0 {
		return ""
	}
	return fmt.Sprintf("未匹配到 %s 节点：这次列表里没有该运营商的服务器。", strings.Join(missing, "、"))
}
