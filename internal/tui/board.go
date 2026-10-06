package tui

import (
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EasySB-Team/EasySB/internal/prefs"
	"github.com/EasySB-Team/EasySB/internal/toolbox/tools"
)

// The toolbox 看板 shows the entries the operator picked, and remembers the pick. This is the
// one screen whose content is a choice rather than a measurement, so it is kept as a set of
// tool ids and saved with the rest of the interface preferences.
//
// Three states share one field, and they mean different things:
//
//	nil        nobody has been asked, so BoardDefault() decides
//	anything   exactly those ids are shown (an empty map means the board shows nothing)
//
// The stored form spells the last one out ("none"), because an empty value in the
// preferences file is what a setting that was never made looks like.

// boardFromPrefs reads the stored selection. A missing or unreadable preference is not an
// error: it means the panel has never been asked, which is the default selection.
func boardFromPrefs(p prefs.Prefs) map[string]bool {
	value := strings.TrimSpace(p.Board)
	if value == "" {
		return nil
	}
	selected := map[string]bool{}
	if value == prefs.BoardNone {
		return selected
	}
	for _, id := range strings.Split(value, ",") {
		if id = strings.TrimSpace(id); id != "" {
			selected[id] = true
		}
	}
	return selected
}

// boardStored is the selection in the form the preferences file keeps.
func (a *App) boardStored() string {
	if a.board == nil {
		return ""
	}
	if len(a.board) == 0 {
		return prefs.BoardNone
	}
	ids := a.boardIDs()
	return strings.Join(ids, ",")
}

// boardIDs is the selected tool ids, in the order the toolbox lists them, so the stored value
// reads like the menu instead of changing order because a map was walked. An id the registry
// no longer knows — a tool that was removed — is kept at the end: dropping it silently would
// rewrite a choice the operator made.
func (a *App) boardIDs() []string {
	ids := make([]string, 0, len(a.board))
	seen := make(map[string]bool, len(a.board))
	for _, tool := range tools.All() {
		if a.boardSelected(tool.ID) {
			ids = append(ids, tool.ID)
			seen[tool.ID] = true
		}
	}
	extra := make([]string, 0, len(a.board))
	for id := range a.board {
		if a.board[id] && !seen[id] {
			extra = append(extra, id)
		}
	}
	sort.Strings(extra)
	return append(ids, extra...)
}

// boardSelected answers whether an entry is on the board. A panel that has never been asked
// shows the entries that are worth it by default.
func (a *App) boardSelected(id string) bool {
	if a.board == nil {
		for _, def := range tools.BoardDefault() {
			if def == id {
				return true
			}
		}
		return false
	}
	return a.board[id]
}

// boardAny reports whether every entry of a group is off, on, or mixed, which is what the
// settings page draws beside a group's name.
func (a *App) boardGroupState(group string) (on, total int) {
	for _, tool := range tools.InGroup(group) {
		total++
		if a.boardSelected(tool.ID) {
			on++
		}
	}
	return on, total
}

// toggleBoard turns one entry on or off and remembers the answer. Turning the first entry on
// or off materialises the default selection, so a single click never silently discards the
// rest of it.
func (a *App) toggleBoard(id string) tea.Cmd {
	if a.board == nil {
		a.board = map[string]bool{}
		for _, def := range tools.BoardDefault() {
			a.board[def] = true
		}
	}
	if a.board[id] {
		delete(a.board, id)
	} else {
		a.board[id] = true
	}
	a.remember()
	return nil
}

// setBoardAll turns every entry on or off at once, which is what the 全选 / 全不选 rows of the
// settings page do.
func (a *App) setBoardAll(on bool) tea.Cmd {
	selected := map[string]bool{}
	if on {
		for _, tool := range tools.All() {
			selected[tool.ID] = true
		}
	}
	a.board = selected
	a.remember()
	return nil
}

// boardMark is the checkbox in front of a settings row.
func (a *App) boardMark(on bool) string {
	if on {
		return "[x] "
	}
	return "[ ] "
}
