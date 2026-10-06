package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EasySBTeam/EasySB/internal/i18n"
	"github.com/EasySBTeam/EasySB/internal/icons"
	"github.com/EasySBTeam/EasySB/internal/theme"
)

// linkCardHeight is the number of terminal rows one card occupies: two border
// rows plus the single description line.
const linkCardHeight = 3

// linkCardGap is the blank gap between two card columns in cells.
const linkCardGap = 2

// linkCardMin is the narrowest card that still fits a client name plus a copy
// chip. It drives the number of columns chosen for a given width.
const linkCardMin = 26

// linkItem is one copyable entry shown as a card. The URL/value is never
// rendered; only the label and description are visible, and the value is what
// Enter puts on the clipboard.
type linkItem struct {
	label string
	desc  string
	value string
}

// linksModel is a grid of copyable cards, used for subscription endpoints and
// share links. Unlike the task log it never prints the full URL: selecting a
// card and pressing Enter copies its value through OSC52.
type linksModel struct {
	title  string
	items  []linkItem
	cursor int
	copied int
	cols   int
	cardW  int
	// status is transient feedback for copy-all, shown in the header because no
	// single card can carry the cue.
	status string
	// topRow is the first grid row currently on screen; the grid scrolls with
	// the arrow keys to keep the cursor visible.
	topRow int
	// visibleRows is the number of grid rows the last render could fit.
	visibleRows int
}

func newLinksModel(title string, items []linkItem) *linksModel {
	return &linksModel{title: title, items: items, copied: -1}
}

// copy stores text on the system clipboard and remembers which card produced
// it; the copied card turns green on the next render.
func (l *linksModel) copy(index int) tea.Cmd {
	if index < 0 || index >= len(l.items) {
		return nil
	}
	l.copied = index
	l.status = ""
	return tea.SetClipboard(l.items[index].value)
}

// copyAll puts every value on the clipboard, one per line.
func (l *linksModel) copyAll() tea.Cmd {
	values := make([]string, 0, len(l.items))
	for _, item := range l.items {
		values = append(values, item.value)
	}
	l.copied = -1
	return tea.SetClipboard(strings.Join(values, "\n"))
}

// gridDims picks the column count and card width for a viewport.
func gridDims(width, count int) (int, int) {
	cols := 3
	if width < cols*linkCardMin+(cols-1)*linkCardGap {
		cols = 2
	}
	if width < 2*linkCardMin+linkCardGap {
		cols = 1
	}
	if count > 0 && cols > count {
		cols = count
	}
	if cols < 1 {
		cols = 1
	}
	cardW := (width - (cols-1)*linkCardGap) / cols
	if cardW < 8 {
		cardW = 8
	}
	return cols, cardW
}

// handleKey processes a key press. The bool reports whether the panel should
// close.
func (l *linksModel) handleKey(msg tea.KeyPressMsg, lang i18n.Lang) (tea.Cmd, bool) {
	key := strings.ToLower(msg.String())
	switch key {
	case "esc", "backspace":
		// q is deliberately absent: it quits the panel from every page, so it
		// reaches the global shortcut instead of closing this panel.
		return nil, true
	case "enter":
		return l.copy(l.cursor), false
	case "c":
		cmd := l.copyAll()
		l.status = lang.T("links_copied_all")
		return cmd, false
	case "left":
		l.move(-1)
		return nil, false
	case "right":
		l.move(1)
		return nil, false
	case "up", "k":
		l.move(-l.rowStep())
		return nil, false
	case "down", "j":
		l.move(l.rowStep())
		return nil, false
	}
	if n, err := strconv.Atoi(key); err == nil && n >= 1 && n <= len(l.items) {
		l.cursor = n - 1
		l.status = ""
	}
	return nil, false
}

// rowStep is one grid row in cursor steps. It falls back to 1 before the first
// render has measured the column count.
func (l *linksModel) rowStep() int {
	if l.cols < 1 {
		return 1
	}
	return l.cols
}

// move shifts the cursor by d cards and clamps it to the item range.
func (l *linksModel) move(d int) {
	if len(l.items) == 0 {
		return
	}
	l.status = ""
	l.cursor += d
	if l.cursor < 0 {
		l.cursor = 0
	}
	if l.cursor >= len(l.items) {
		l.cursor = len(l.items) - 1
	}
}

// card renders one card. The value is deliberately absent from the content:
// only the description is drawn and the frame color is the copy state.
func (l *linksModel) card(item linkItem, index, width int, pal theme.Palette) string {
	inner := width - 4
	desc := pal.Dim(theme.Truncate(item.desc, inner))

	border := pal.Border
	title := pal.Text
	switch {
	case l.copied == index:
		border, title = pal.OK, pal.OK
	case l.cursor == index:
		border, title = pal.Primary, pal.Primary
	}
	return theme.Box(item.label, desc, width, border, title)
}

// gridLines renders the visible grid rows and records how many fit. Each row
// block is linkCardHeight lines tall, with one blank line between rows.
func (l *linksModel) gridLines(cols, cardW, gridH int, pal theme.Palette) []string {
	if len(l.items) == 0 {
		l.topRow, l.visibleRows = 0, 0
		return nil
	}
	rows := (len(l.items) + cols - 1) / cols
	visible := (gridH + 1) / (linkCardHeight + 1)
	if visible < 1 {
		visible = 1
	}
	cursorRow := l.cursor / cols
	if cursorRow < l.topRow {
		l.topRow = cursorRow
	}
	if cursorRow >= l.topRow+visible {
		l.topRow = cursorRow - visible + 1
	}
	if l.topRow > rows-visible {
		l.topRow = rows - visible
	}
	if l.topRow < 0 {
		l.topRow = 0
	}
	l.visibleRows = visible
	if visible > rows {
		l.visibleRows = rows
	}

	end := l.topRow + visible
	if end > rows {
		end = rows
	}
	var out []string
	for row := l.topRow; row < end; row++ {
		if row > l.topRow {
			out = append(out, "")
		}
		cards := make([]string, cols)
		for col := 0; col < cols; col++ {
			index := row*cols + col
			if index < len(l.items) {
				cards[col] = l.card(l.items[index], index, cardW, pal)
			} else {
				cards[col] = blankCard(cardW)
			}
		}
		cardLines := make([][]string, cols)
		for col := range cards {
			cardLines[col] = strings.Split(cards[col], "\n")
		}
		for line := 0; line < linkCardHeight; line++ {
			parts := make([]string, 0, cols)
			for col := 0; col < cols; col++ {
				parts = append(parts, cardLines[col][line])
			}
			out = append(out, strings.Join(parts, strings.Repeat(" ", linkCardGap)))
		}
	}
	return out
}

// render lays the cards out in a grid sized to the shared panel frame.
func (l *linksModel) render(width, height int, pal theme.Palette, lang i18n.Lang, ic icons.Set) string {
	cols, cardW := gridDims(width-4, len(l.items))
	l.cols, l.cardW = cols, cardW

	bodyH := panelBodyHeight(height)
	if bodyH < 1 {
		bodyH = 1
	}

	grid := l.gridLines(cols, cardW, bodyH-2, pal)

	header := pal.Bold(pal.Primary, " "+ic.Link+" "+theme.Truncate(l.title, width-6))
	if l.status != "" {
		header += "  " + pal.Colored(pal.OK, ic.OK+" "+l.status)
	}
	rows := 0
	if len(l.items) > 0 {
		rows = (len(l.items) + cols - 1) / cols
	}
	if rows > 1 {
		header += pal.Dim(fmt.Sprintf("  %d/%d", l.topRow+1, rows))
	}

	body := make([]string, 0, len(grid)+2)
	body = append(body, header, "")
	body = append(body, grid...)

	return framePanel(pal, lang, width, height, body, pal.Dim(lang.T("links_hint")))
}

// blankCard keeps empty grid cells the same width as a real card.
func blankCard(width int) string {
	line := strings.Repeat(" ", width)
	return strings.Join([]string{line, line, line}, "\n")
}

func (l *linksModel) View(w, h int, pal theme.Palette, lang i18n.Lang, ic icons.Set) string {
	return l.render(panelWidth(w), h, pal, lang, ic)
}
