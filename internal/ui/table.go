package ui

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/EasySBTeam/EasySB/internal/theme"
)

// Cell is one table cell: text plus the tone it deserves. The tone travels with the cell
// so a status word can be coloured without the table knowing what the columns mean.
type Cell struct {
	Text string
	Kind Kind
}

// Text builds a plain cell.
func Text(s string) Cell { return Cell{Text: s} }

// Toned builds a cell that carries a tone.
func Toned(s string, k Kind) Cell { return Cell{Text: s, Kind: k} }

// Table lays cells out in columns inside a width of w.
//
// Every width comes from the widest cell in its column, except the last, which takes the
// room that is left. No row is ever wrapped: a table row is one line by contract, so what
// does not fit is truncated. That is what lets a tool report as many rows as it likes
// without the panel planning a layout for it.
func Table(s theme.Style, headers []string, rows [][]Cell, w int) []string {
	if w < 8 {
		return nil
	}
	cols := len(headers)
	for _, row := range rows {
		if len(row) > cols {
			cols = len(row)
		}
	}
	if cols == 0 {
		return nil
	}

	width := tableWidths(headers, rows, cols)
	const gap = 2
	fitColumns(width, w, gap, cols)

	out := make([]string, 0, len(rows)+2)
	if len(headers) > 0 {
		cells := make([]Cell, len(width))
		for i := range cells {
			if i < len(headers) {
				cells[i] = Text(headers[i])
			}
		}
		out = append(out, s.Faint(renderRow(s, cells, width, gap)))
		out = append(out, s.Faint(theme.Truncate(strings.Repeat("─", w), w)))
	}
	for _, row := range rows {
		out = append(out, renderRow(s, row, width, gap))
	}
	return out
}

// tableWidths is the natural width of each column: the widest cell, header included.
func tableWidths(headers []string, rows [][]Cell, cols int) []int {
	width := make([]int, cols)
	for i, h := range headers {
		width[i] = lipgloss.Width(h)
	}
	for _, row := range rows {
		for i, c := range row {
			if i >= cols {
				continue
			}
			if n := lipgloss.Width(c.Text); n > width[i] {
				width[i] = n
			}
		}
	}
	return width
}

// fitColumns gives the last column the slack and shrinks the widest of the others when
// the table still would not fit, so a long value cannot push the frame apart.
func fitColumns(width []int, w, gap, cols int) {
	fixed := gap * (cols - 1)
	for i := 0; i < cols-1; i++ {
		fixed += width[i]
	}
	width[cols-1] = maxInt(4, w-fixed)
	for totalWidth(width)+gap*(cols-1) > w {
		widest, at := 0, -1
		for i := 0; i < cols-1; i++ {
			if width[i] > widest {
				widest, at = width[i], i
			}
		}
		if at < 0 || width[at] <= 6 {
			break
		}
		width[at]--
	}
}

// renderRow renders one row, colouring each cell with the tone it carries. Padding is
// computed from the plain text, because the coloured string is longer than the cell it
// occupies.
func renderRow(s theme.Style, row []Cell, width []int, gap int) string {
	var b strings.Builder
	for i, w := range width {
		if i > 0 {
			b.WriteString(strings.Repeat(" ", gap))
		}
		var cell Cell
		if i < len(row) {
			cell = row[i]
		}
		text := theme.Truncate(cell.Text, w)
		pad := w - lipgloss.Width(text)
		if pad < 0 {
			pad = 0
		}
		if cell.Kind != KindPlain && text != "" {
			text = s.Bold(cell.Kind.Color(s), text)
		}
		b.WriteString(text)
		b.WriteString(strings.Repeat(" ", pad))
	}
	return strings.TrimRight(b.String(), " ")
}

func totalWidth(width []int) int {
	n := 0
	for _, w := range width {
		n += w
	}
	return n
}
