package tui

import (
	"fmt"

	"github.com/EasySBTeam/EasySB/internal/i18n"

	"github.com/EasySBTeam/EasySB/internal/theme"
	"github.com/EasySBTeam/EasySB/internal/ui"
)

// The panel's layout is fixed: the same two boxes, in the same rows, on every page.
//
// The numbers come from the main page, because that is the page an operator sees first and
// the one they asked every other page to match. Before this, a section whose board was taller
// than its room dropped the board entirely and let its menu box grow to fill the screen, so
// every page had its own box sizes and the panel jumped as the operator moved through it.
//
// The slots are:
//
//	┌ 看板 ┐   l.board rows, borders included
//	          l.gap blank rows
//	┌ 菜单 ┐   l.menu rows, borders included
//	· 说明                    l.tail rows: the description line and the key hints
//	┌ 提示 ┐
//
// A page with more content than a slot has room for clips it inside the slot instead of
// growing, and says how much it left out; nothing in the panel scrolls behind a key.
type layout struct {
	// board is the rows of the top box, borders included.
	board int
	// gap is the blank rows between the boxes.
	gap int
	// menu is the rows of the bottom box, borders included.
	menu int
	// tail is the rows of the description line and the key hints.
	tail int
}

// A box needs a border, a title row's worth of content and room to say anything at all.
const (
	minBoardRows = 4
	minMenuRows  = 5
)

// menuSlotCells is the tallest menu the panel lists one entry per line: the hardware group,
// which is six tools and the row that leads back out of it. The fixed slot is sized for that,
// because an entry the box cannot hold would be unreachable — no page of this panel scrolls.
//
// The account detail page lists twelve operations, which is taller on purpose: it is a page of
// actions rather than a list of peers, and dashboard.go lays a page that has outgrown one entry
// per line out in the panel's columns instead of hiding half of it behind the "+N" row. The
// result is the shape of the main menu, which is the one layout an operator already knows.
const menuSlotCells = 7

// menuSlotRows is the height of the bottom box: that menu plus the box's own borders. The main
// menu has fewer entries and leaves the extra rows blank, which is the price of every page
// having the same two boxes.
func (a *App) menuSlotRows(w int) int {
	cells := menuSlotCells
	// On a narrow terminal the main menu is one column too, and it has more entries than the
	// follows whichever of the two needs more rows.
	if colW := (ui.InnerWidth(a.style(), w) - 1) / 2; colW < 16 {
		cells = maxInt(cells, len(a.stack[0].nodes))
	}
	return cells + 2
}

// layoutFor is the panel's fixed layout for a terminal of this width and a body of this many
// rows (the rows under the status strip). The slot heights are the main page's own board and
// menu boxes, so the main page fits them exactly and every other page has to live inside them.
func (a *App) layoutFor(w, h int) layout {
	s := a.style()
	gap := 1
	if s.Met.Compact {
		gap = 0
	}
	// The tail is not what gives way: the description line and the key hints are part of every
	// page. On a short terminal it is the 看板 that shrinks — it is the one slot whose content
	// can say "…还有 N 行" and still be useful — and the entries box only shrinks once the
	// board is already at its floor.
	tail := a.tailRows(h)
	if limit := h - gap - minBoardRows - minMenuRows; tail > limit {
		tail = maxInt(1, limit)
	}
	menu := a.menuSlotRows(w)
	if limit := h - gap - tail - minBoardRows; menu > limit {
		menu = maxInt(minMenuRows, limit)
	}
	// The board never grows past the welcome card it was measured from: a taller terminal
	// leaves the empty rows at the bottom rather than stretching the card, which is the look
	// the panel has always had.
	boardFor := func(gap int) int {
		board := h - gap - menu - tail
		if full := len(a.rootCard(w, heroLevels)); board > full {
			board = full
		}
		if board < minBoardRows {
			board = minBoardRows
		}
		return board
	}
	l := layout{board: boardFor(gap), gap: gap, menu: menu, tail: tail}
	// The blank row between the boxes is only a spacer, so it is the first thing to go when it
	// buys the 看板 a row it can use: a card that fits whole beats a gap.
	if gap > 0 && boardFor(0) > l.board {
		l.board, l.gap = boardFor(0), 0
	}
	return l.fit(h)
}

// fit shrinks the slots until they and the tail fit the body. The board gives way first,
// because a page's entries are what the operator came for, and the bottom box never shrinks
// below its rows plus the way back out.
func (l layout) fit(h int) layout {
	for l.board+l.gap+l.menu+l.tail > h {
		switch {
		case l.board > minBoardRows:
			l.board--
		case l.menu > minMenuRows:
			l.menu--
		case l.tail > 1:
			// Only once both boxes are at their floor: the hint then says the same thing
			// in fewer rows.
			l.tail--
		case l.gap > 0:
			l.gap--
		default:
			// The terminal is smaller than the panel's smallest page. The caller clips
			// to the screen; there is nothing left here to give away.
			return l
		}
	}
	return l
}

// span is the rows one box takes when a running task or a report merges the two slots: the
// same space, used as one box.
func (l layout) span() int {
	return l.board + l.gap + l.menu
}

// tailRows is the rows the fixed tail needs: the description line and the key hints. The hint
// block's own height comes from the screen height, exactly as it did before the layout was
// fixed, so a short terminal keeps the one-line hint it always had.
func (a *App) tailRows(h int) int {
	if h < 3 {
		return 0
	}
	return 1 + hintHeight(h-1)
}

// boxAt draws a titled box that is exactly height rows tall, the way the panel's slots need
// it. It is a package function rather than a method because the task screen draws its own box
// with the same rules, without an App to hand.
func boxAt(s theme.Style, title, badge string, rows []string, w, height int) []string {
	if height < 3 {
		height = 3
	}
	body := rows
	room := boxRows(height)
	if len(body) > room {
		body = body[:room]
	}
	for len(body) < room {
		body = append(body, "")
	}
	card := ui.Card(s, title, badge, body, w)
	if len(card) > height {
		card = card[:height]
	}
	return card
}

// clipLines fits lines into row rows and, when it had to drop some, ends on a line that says
// how many: a screen whose content did not fit says so instead of quietly showing less.
func clipLines(s theme.Style, lines []string, row int) []string {
	if row <= 0 || len(lines) <= row {
		return lines
	}
	if row == 1 {
		return []string{s.Faint("…")}
	}
	out := make([]string, 0, row)
	out = append(out, lines[:row-1]...)
	out = append(out, s.Faint(fmt.Sprintf("… %d", len(lines)-row+1)))
	return out
}

// boxAt draws a titled box that is exactly height rows tall. The content is clipped to the
// room inside the box and the rest of the rows are left blank, so a box never changes size:
// an entry list with four items and one with nine both fill their slot.
func (a *App) boxAt(title string, rows []string, w, height int) []string {
	inner := boxRows(height)
	return boxAt(a.style(), title, "", a.clipRows(rows, inner), w, height)
}

// clipRows fits rows into n rows, ending with a line that says how many were left out. A
// measurement that does not fit is therefore counted on screen rather than hidden behind a
// key: the panel has no scrolling pages, so a silent cut would be a silent lie.
func (a *App) clipRows(rows []string, n int) []string {
	if n <= 0 || len(rows) <= n {
		return rows
	}
	if n == 1 {
		return []string{a.lang.Format("box_rows_more", len(rows))}
	}
	kept := make([]string, 0, n)
	kept = append(kept, rows[:n-1]...)
	kept = append(kept, a.style().Faint(a.lang.Format("box_rows_more", len(rows)-n+1)))
	return kept
}

// blankRows returns n blank lines, which is how the gap between the boxes is drawn.
func blankRows(n int) []string {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, "")
	}
	return out
}

// tailLinesPadded renders the fixed tail and pads it to the rows the layout reserved. The tail
// itself is built for the whole body height, so its hint block is the same one every page has
// always drawn.
func (a *App) tailLinesPadded(w, h, rows int) []string {
	tail := a.tailLines(w, h)
	for len(tail) < rows {
		tail = append(tail, "")
	}
	if len(tail) > rows {
		tail = tail[:rows]
	}
	return tail
}

// boxRows is how much content a box of this height can hold.
func boxRows(height int) int {
	if height < 3 {
		return 1
	}
	return height - 2
}

// menuRowsFor is the entry rows of any menu, in the two columns the panel uses once the frame
// is wide enough. It is the same calculation for the main menu and for every page under it,
// which is what keeps the two boxes the same in every page.

// keyTail renders the panel's fixed tail: the description line and the key hints, in the rows
// the layout reserves for them. An empty description still takes its row, so the hint box
// lands in the same place on every page.
func keyTail(pal theme.Palette, lang i18n.Lang, desc, hint string, width, rows int) []string {
	if rows <= 0 {
		return nil
	}
	out := make([]string, 0, rows)
	if desc != "" {
		out = append(out, pal.Faint("· "+theme.Truncate(desc, maxInt(0, width-2))))
	} else {
		out = append(out, "")
	}
	switch {
	case rows >= 4:
		out = append(out, hintBoxFor(pal, lang, hint, width)...)
	case rows >= 2:
		out = append(out, hintLineFor(pal, lang, hint, width))
	}
	for len(out) < rows {
		out = append(out, "")
	}
	if len(out) > rows {
		out = out[:rows]
	}
	return out
}
