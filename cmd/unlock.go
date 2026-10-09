package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/EasySBTeam/EasySB/internal/unlock"
)

// runUnlockCheck probes every service in the catalogue and prints one line per
// verdict. It is the headless form of the 服务解锁状态 page, for a host where the
// panel is scripted rather than opened.
func runUnlockCheck() {
	ctx := context.Background()
	report := unlock.New(unlock.Options{}).Report(ctx)
	fmt.Printf("service unlock check · %d services · %s\n\n", len(report.Results), report.Elapsed.Round(time.Millisecond))
	group := ""
	for _, res := range report.Results {
		if res.Group != group {
			group = res.Group
			fmt.Println("[" + unlockGroupName(group) + "]")
		}
		line := fmt.Sprintf("  %-26s %-10s", res.Name, unlockStatusName(res.Status))
		if res.Region != "" {
			line += " " + res.Region
		}
		if res.Text != "" && res.Status != unlock.StatusUnlocked {
			line += "  " + res.Text
		}
		fmt.Println(line)
	}
	fmt.Printf("\nunlocked %d · partial %d · blocked %d · failed %d\n",
		report.Count(unlock.StatusUnlocked), report.Count(unlock.StatusPartial),
		report.Count(unlock.StatusBlocked), report.Count(unlock.StatusFailed))
}

// unlockGroupName names a catalogue group in the report's own language, which is
// English: this output goes to a log or a pipe, not to the bilingual panel.
func unlockGroupName(group string) string {
	switch group {
	case unlock.GroupMultination:
		return "streaming"
	case unlock.GroupAI:
		return "ai"
	case unlock.GroupGame:
		return "game"
	case unlock.GroupChina:
		return "china"
	case unlock.GroupTaiwan:
		return "taiwan"
	}
	return group
}

// unlockStatusName words a verdict for the plain-text report.
func unlockStatusName(status unlock.Status) string {
	switch status {
	case unlock.StatusUnlocked:
		return "unlocked"
	case unlock.StatusPartial:
		return "partial"
	case unlock.StatusBlocked:
		return "blocked"
	}
	return "failed"
}
