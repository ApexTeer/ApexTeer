package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/EasySBTeam/EasySB/internal/i18n"
	"github.com/EasySBTeam/EasySB/internal/state"
	"github.com/EasySBTeam/EasySB/internal/theme"
)

// hintRows is the height of the pinned hint box, or zero when the terminal is
// too short to spare the rows. Every screen shares this so the box keeps one
// position and size.
func hintRows(h int) int {
	if h >= 14 {
		return 3
	}
	return 0
}

// hintBox renders the key hints as their own box below the frame. hint is the
// already-styled left side; the language code stays pinned to the right.
func (a *App) hintBox(hint string, width int) []string {
	return hintBoxFor(a.palette, a.lang, hint, width)
}

// hintLine pads an already-styled hint to width and pins the language code to
// the right edge, so every screen shows it at the same place.
func (a *App) hintLine(hint string, width int) string {
	return hintLineFor(a.palette, a.lang, hint, width)
}

// hintLineFor is hintLine for callers that only hold a palette and language.
func hintLineFor(pal theme.Palette, lang i18n.Lang, hint string, width int) string {
	right := pal.Label(lang.Code() + " ")
	rightW := lipgloss.Width(right)
	availLeft := width - rightW - 1
	if availLeft < 1 {
		availLeft = 1
	}
	left := " " + theme.Truncate(hint, maxInt(0, availLeft-1))
	gap := width - lipgloss.Width(left) - rightW
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

// hintBoxFor is hintBox for callers that only hold a palette and language.
func hintBoxFor(pal theme.Palette, lang i18n.Lang, hint string, width int) []string {
	box := theme.Box(lang.T("panel_hints"), hintLineFor(pal, lang, hint, width-4), width, pal.Border, pal.Primary)
	return strings.Split(box, "\n")
}

// panelWidth is the shared panel width: the terminal width capped at 100 so
// every screen lines up with the main dashboard. It never exceeds the terminal
// width, so a narrow SSH window keeps both borders on screen.
func panelWidth(w int) int {
	if w <= 0 {
		w = 96
	}
	if w > 100 {
		w = 100
	}
	if w < 8 {
		w = 8
	}
	return w
}

// panelBodyHeight is the number of inner content rows shared by every screen:
// the terminal height minus the borders and the bottom hints (box or line).
func panelBodyHeight(h int) int {
	chrome := hintRows(h)
	if chrome == 0 {
		chrome = 1
	}
	b := h - chrome - 2
	if b < 1 {
		b = 1
	}
	return b
}

// framePanel draws inner body lines inside the shared panel frame and pins the
// hint to the bottom. The body is clipped or padded to the frame's fixed
// height, so the panel is exactly the same size as the main dashboard.
func framePanel(pal theme.Palette, lang i18n.Lang, w, h int, body []string, hint string) string {
	if h < 3 {
		h = 3
	}
	rows := hintRows(h)
	bodyH := panelBodyHeight(h)
	out := make([]string, 0, h)
	out = append(out, theme.TopRule(w, pal.Border))
	for i := 0; i < bodyH; i++ {
		s := ""
		if i < len(body) {
			s = body[i]
		}
		out = append(out, theme.FrameLine(s, w, pal.Border))
	}
	out = append(out, theme.BottomRule(w, pal.Border))
	if rows > 0 {
		out = append(out, hintBoxFor(pal, lang, hint, w)...)
	} else {
		out = append(out, " "+theme.Truncate(hint, maxInt(0, w-2)))
	}
	// Degenerate terminals: never emit more rows than the window can show.
	if len(out) > h {
		out = out[:h]
	}
	return strings.Join(out, "\n")
}

func (a *App) panelValue(v string) string {
	if strings.TrimSpace(v) == "" {
		return a.lang.T("not_set")
	}
	return v
}

// cpuSummary renders the processor core count, e.g. "8 cores".
func (a *App) cpuSummary() string {
	if a.status.CPUCores <= 0 {
		return ""
	}
	return fmt.Sprintf("%d %s", a.status.CPUCores, a.lang.T("unit_cores"))
}

// usageCell formats a used/total pair with its usage percentage, or an empty
// string when the total is unknown.
func usageCell(total, free uint64) string {
	if total == 0 {
		return ""
	}
	if free > total {
		free = total
	}
	used := total - free
	return fmt.Sprintf("%s / %s (%d%%)", humanBytes(used), humanBytes(total), used*100/total)
}

// humanBytes renders a byte count with a binary unit and one decimal.
func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	value := float64(n)
	i := -1
	for value >= unit && i < len(units)-1 {
		value /= unit
		i++
	}
	return fmt.Sprintf("%.1f %s", value, units[i])
}

// humanDuration renders an uptime as "3d 4h 5m", dropping leading units that
// are zero. Anything below a minute reads as "0m".
func humanDuration(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	days := int(d / (24 * time.Hour))
	d -= time.Duration(days) * 24 * time.Hour
	hours := int(d / time.Hour)
	d -= time.Duration(hours) * time.Hour
	mins := int(d / time.Minute)
	var b strings.Builder
	if days > 0 {
		fmt.Fprintf(&b, "%dd ", days)
	}
	if days > 0 || hours > 0 {
		fmt.Fprintf(&b, "%dh ", hours)
	}
	fmt.Fprintf(&b, "%dm", mins)
	return b.String()
}

// portSummary lists the enabled protocol ports for the node card.
func portSummary(cfg state.Config) string {
	var out []string
	for _, k := range state.Keys {
		if cfg.Enabled[k] && cfg.Ports[k] != "" {
			out = append(out, state.Labels[k]+":"+cfg.Ports[k])
		}
	}
	return strings.Join(out, "  ")
}
