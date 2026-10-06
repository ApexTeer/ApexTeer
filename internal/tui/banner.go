package tui

import (
	"charm.land/lipgloss/v2"

	"github.com/EasySB-Team/EasySB/internal/theme"
)

// logoGlyphs holds the "ANSI Shadow" block letters used for the EasySB banner.
// Every glyph is six rows tall and eight columns wide so the letters tile.
var logoGlyphs = map[rune][6]string{
	'E': {
		"███████╗",
		"██╔════╝",
		"█████╗  ",
		"██╔══╝  ",
		"███████╗",
		"╚══════╝",
	},
	'A': {
		" █████╗ ",
		"██╔══██╗",
		"███████║",
		"██╔══██║",
		"██║  ██║",
		"╚═╝  ╚═╝",
	},
	'S': {
		"███████╗",
		"██╔════╝",
		"███████╗",
		"╚════██║",
		"███████║",
		"╚══════╝",
	},
	'Y': {
		"██╗   ██╗",
		"╚██╗ ██╔╝",
		" ╚████╔╝ ",
		"  ╚██╔╝  ",
		"   ██║   ",
		"   ╚═╝   ",
	},
	'B': {
		"██████╗ ",
		"██╔══██╗",
		"██████╔╝",
		"██╔══██╗",
		"██████╔╝",
		"╚═════╝ ",
	},
}

// logoLines renders the block-letter EasySB wordmark centered in the panel. It
// returns nil when the terminal is too narrow to hold it.
func (a *App) logoLines(inner int) []string {
	rows := make([]string, 6)
	for _, ch := range "EASYSB" {
		glyph, ok := logoGlyphs[ch]
		if !ok {
			return nil
		}
		for r := 0; r < 6; r++ {
			rows[r] += glyph[r]
		}
	}
	if lipgloss.Width(rows[0]) > inner {
		return nil
	}
	out := make([]string, len(rows))
	for r, row := range rows {
		out[r] = theme.Center(a.palette.Bold(a.palette.Primary, row), inner)
	}
	return out
}

// taglineLine renders the centered product tagline. The caller draws a
// full-width rule beneath it to separate the header from the dashboard.
func (a *App) taglineLine(inner int) string {
	return theme.Center(a.palette.Bold(a.palette.Primary, theme.Truncate(a.lang.T("banner_tagline"), inner)), inner)
}

// quoteLine renders the frozen daily quote framed by full-width quotation marks.
func (a *App) quoteLine(inner int) string {
	text := theme.Truncate("“"+a.quote+"”", inner)
	return theme.Center(a.palette.Dim(text), inner)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
