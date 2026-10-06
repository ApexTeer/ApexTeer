// Package tools is the toolbox registry: the one list of entries the panel's menu, its
// board and the --tool command all read. Adding a tool means adding one entry here, and
// nothing else: the menu, the board and the command line are built from this list.
//
// Entries report stable tokens rather than words ("unlocked", not 解锁), because the
// panel has two languages and the tools have none. The interface turns a token into a
// word; a tool never has to know who is reading.
package tools

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/EasySB-Team/EasySB/internal/i18n"
	"github.com/EasySB-Team/EasySB/internal/sysinfo"
	"github.com/EasySB-Team/EasySB/internal/toolbox"
	"github.com/EasySB-Team/EasySB/internal/toolbox/backtrace"
	"github.com/EasySB-Team/EasySB/internal/toolbox/bench"
	"github.com/EasySB-Team/EasySB/internal/toolbox/hw"
	"github.com/EasySB-Team/EasySB/internal/toolbox/ipquality"
	"github.com/EasySB-Team/EasySB/internal/toolbox/portcheck"
	"github.com/EasySB-Team/EasySB/internal/toolbox/speed"
	"github.com/EasySB-Team/EasySB/internal/unlock"
)

// Group ids, in the order the menu lists them.
const (
	GroupUnlock   = "unlock"
	GroupNetwork  = "network"
	GroupIP       = "ip"
	GroupHardware = "hardware"
)

// Tool ids. They are menu ids, i18n key suffixes ("toolbox_"+id) and --tool arguments.
const (
	UnlockMedia  = "unlock-media"
	UnlockAI     = "unlock-ai"
	UnlockRegion = "unlock-region"
)

// BoardDefault is the board selection a panel that has never been asked shows: the entries
// whose answer an operator wants on the first screen — whether the node unlocks the services
// it is for, how it reaches the three networks, and what the host is. Everything else is one
// key away in its own report, and the 看板 settings page can turn any of them on.
func BoardDefault() []string {
	return []string{UnlockMedia, UnlockAI, UnlockRegion, backtrace.ID, ipquality.ToolID, hw.ToolInfoID}
}

// Groups returns the group ids in menu order.
func Groups() []string {
	return []string{GroupUnlock, GroupNetwork, GroupIP, GroupHardware}
}

// All returns every entry the panel offers, in menu order: the groups appear in the order
// they are listed here, and so does every entry inside a group. Everything a toolbox entry
// is comes from this list — the menu, the board and the --tool command all read it — so a
// new tool is registered here and nowhere else.
func All() []toolbox.Tool {
	entries := make([]toolbox.Tool, 0, 16)
	entries = append(entries, unlockTools()...)
	entries = append(entries, networkTools()...)
	entries = append(entries, ipTools()...)
	entries = append(entries, hardwareTools()...)
	return entries
}

// networkTools is the 网络检测 group: how this host reaches the world, and how fast.
func networkTools() []toolbox.Tool {
	entries := []toolbox.Tool{backtrace.Tool()}
	entries = append(entries, speed.Tools()...)
	return entries
}

// ipTools is the IP 与端口 group: what the world sees when it looks back at this host.
func ipTools() []toolbox.Tool {
	return []toolbox.Tool{ipquality.Tool, portcheck.Tool()}
}

// hardwareTools is the 硬件与性能 group: what the machine is, and what it can do.
func hardwareTools() []toolbox.Tool {
	entries := make([]toolbox.Tool, 0, 8)
	entries = append(entries, hw.Tools()...)
	entries = append(entries, bench.Tools()...)
	entries = append(entries, bench.DisksTool())
	return entries
}

// unlockTools are the three unlock entries: streaming, AI and the regional catalogues. They
// are separate entries rather than one, because "can I watch Netflix" and "does ChatGPT
// answer" are different questions with different answers, and an operator chasing one of
// them should not have to read the other's rows.
func unlockTools() []toolbox.Tool {
	return []toolbox.Tool{
		{ID: UnlockMedia, Group: GroupUnlock, Run: unlockTool(unlock.GroupMultination)},
		{ID: UnlockAI, Group: GroupUnlock, Run: unlockTool(unlock.GroupAI)},
		{ID: UnlockRegion, Group: GroupUnlock, Run: unlockTool(unlock.GroupGame, unlock.GroupChina, unlock.GroupTaiwan)},
	}
}

// InGroup returns the entries of one group, in menu order.
func InGroup(group string) []toolbox.Tool {
	var out []toolbox.Tool
	for _, tool := range All() {
		if tool.Group == group {
			out = append(out, tool)
		}
	}
	return out
}

// Lookup returns one entry by id.
func Lookup(id string) (toolbox.Tool, bool) {
	for _, tool := range All() {
		if tool.ID == id {
			return tool, true
		}
	}
	return toolbox.Tool{}, false
}

// BoardPath is where the toolbox 看板 is written down. The registry owns the path because
// both readers of the board — the panel and `--tool` — have to agree on which file it is; the
// environment override exists for the tests and for a second panel on the same host, the way
// the interface preferences file has its own.
func BoardPath() string {
	if path := strings.TrimSpace(os.Getenv(toolbox.BoardEnv)); path != "" {
		return path
	}
	return filepath.Join(sysinfo.WorkDir, "easysb-toolbox.json")
}

// Record stores one finished run in the board, so a run started from the command line shows
// up in the panel's 看板 too. A board that cannot be written is a degraded convenience, not a
// failed run: the caller has the table either way, so the error is returned rather than
// turned into an interruption.
func Record(id string, result toolbox.Result, runErr error) error {
	path := BoardPath()
	record := toolbox.Record{ID: id, When: time.Now(), Result: result}
	if runErr != nil {
		record.Error = runErr.Error()
	}
	return toolbox.UpdateBoard(path, func(board toolbox.Board) {
		board[id] = record
	})
}

// Verdict words one of the verdict tokens this package defines. The registry owns the
// tokens, so it owns their wording too: the panel's table and the command line then say the
// same thing about the same run, in whichever language they were asked for.
func Verdict(lang i18n.Lang, token string) string {
	switch token {
	case "unlocked":
		return lang.T("toolbox_verdict_unlocked")
	case "blocked":
		return lang.T("toolbox_verdict_blocked")
	case "unknown":
		return lang.T("toolbox_verdict_unknown")
	}
	return token
}

// IsVerdict reports whether a token is one of the verdicts this package defines.
func IsVerdict(token string) bool {
	switch token {
	case "unlocked", "blocked", "unknown":
		return true
	}
	return false
}

// unlockTool adapts the unlock catalogue to a toolbox entry: one row per service of the
// given catalogue groups, with the verdict as a token the interface words and the region
// the service reported. A verdict that is not a plain yes carries its reason as a note, so
// the table stays one line per service while nothing is hidden.
func unlockTool(groups ...string) func(context.Context, toolbox.Options) (toolbox.Result, error) {
	wanted := make(map[string]bool, len(groups))
	for _, group := range groups {
		wanted[group] = true
	}
	return func(ctx context.Context, opts toolbox.Options) (toolbox.Result, error) {
		var ids []string
		for _, service := range unlock.Catalogue() {
			if wanted[service.Group] {
				ids = append(ids, service.ID)
			}
		}
		detector := unlock.New(unlock.Options{
			Client:  opts.HTTP(),
			Timeout: opts.Duration(),
			// The run is counted so the panel can show a bar: one step per service, named
			// after the service that just finished.
			Progress: func(done, total int, name string) {
				opts.ReportProgress(done, total, name)
			},
		})
		report := detector.Report(ctx, ids...)

		result := toolbox.Result{Headers: []string{"service", "status", "region"}}
		counts := map[unlock.Status]int{}
		for _, one := range report.Results {
			counts[one.Status]++
			region := one.Region
			if region == "" {
				region = "—"
			}
			result.Rows = append(result.Rows, []string{one.Name, verdictToken(one.Status), region})
			if text := detail(one); text != "" {
				result.Note("%s: %s", one.Name, text)
			}
		}
		result.Summary = summary(counts, len(report.Results))
		return result, nil
	}
}

// verdictToken is the stable word the interface localises. The four states the probe
// distinguishes collapse to what an operator asks — can this be used here? — and the
// difference between "refused" and "could not be read" survives, because reporting a
// service as refused when the answer was unreadable would be a guess.
func verdictToken(status unlock.Status) string {
	switch status {
	case unlock.StatusUnlocked:
		return "unlocked"
	case unlock.StatusPartial, unlock.StatusBlocked:
		return "blocked"
	default:
		return "unknown"
	}
}

// detail is the one-line explanation a verdict needs, or "" when it needs none.
func detail(one unlock.Result) string {
	switch one.Status {
	case unlock.StatusUnlocked:
		return ""
	case unlock.StatusFailed:
		if one.Text != "" {
			return one.Text
		}
		return one.Reason
	default:
		return one.Text
	}
}

// summary is the board line: how many services answered which way.
func summary(counts map[unlock.Status]int, total int) string {
	parts := []string{
		plural("unlocked", counts[unlock.StatusUnlocked]),
		plural("blocked", counts[unlock.StatusBlocked]+counts[unlock.StatusPartial]),
	}
	if failed := counts[unlock.StatusFailed]; failed > 0 {
		parts = append(parts, plural("unknown", failed))
	}
	return strings.Join(parts, " · ") + " (" + strconv.Itoa(total) + ")"
}

func plural(token string, n int) string {
	return token + " " + strconv.Itoa(n)
}
