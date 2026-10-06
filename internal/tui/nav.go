package tui

import (
	"strings"

	"github.com/EasySB-Team/EasySB/internal/theme"
)

// What is left of the former left-hand navigation lives here. Every page of the
// panel now uses the same two-box frame (the page's 看板 over its entries), and a
// section is left with Esc the way a submenu is, so only two helpers remain:
// sectionID, which says which root entry the panel is inside, and padLines, which
// clips and fills a box.

// sectionID is the root entry the panel is currently inside, or "" on the main
// menu. The section is recorded when a root entry is entered; a stack that was
// assembled another way (tests, restored state) still resolves through the menu
// ids, which repeat their root node id.
func (a *App) sectionID() string {
	if a.section != "" {
		return a.section
	}
	if len(a.stack) < 2 {
		return ""
	}
	for _, n := range a.stack[0].nodes {
		for i := 1; i < len(a.stack); i++ {
			if a.stack[i].id == n.id {
				return n.id
			}
		}
	}
	return ""
}

// padLines clips every line to w columns and fills the column to h rows.
func padLines(lines []string, w, h int) []string {
	out := make([]string, 0, h)
	for _, line := range lines {
		if len(out) == h {
			break
		}
		out = append(out, theme.Pad(theme.Truncate(line, w), w))
	}
	for len(out) < h {
		out = append(out, strings.Repeat(" ", w))
	}
	return out
}
