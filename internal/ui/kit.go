// Package ui renders the panel's widgets: cards, meters, sparklines and the
// aligned key/value blocks they are built from. Every function takes a resolved
// theme.Style and an exact width, and returns lines that are already clipped and
// padded, so a screen only has to stack them.
//
// The widgets never change the palette they are given and never read the
// terminal: a card that looks wrong on a light background is a palette bug, not
// a widget bug.
package ui

import (
	"image/color"
	"math"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/EasySBTeam/EasySB/internal/theme"
)

// Kind is the semantic tone of a value: how much attention it deserves.
type Kind int

// The tones a value can be drawn in, from the plainest to the most insistent. Plain is
// the zero value, so a value whose tone nobody chose is drawn without emphasis.
const (
	KindPlain Kind = iota
	KindOK
	KindWarn
	KindErr
	KindAccent
)

// Color resolves a kind against a palette.
func (k Kind) Color(s theme.Style) color.Color {
	switch k {
	case KindOK:
		return s.OK
	case KindWarn:
		return s.Warn
	case KindErr:
		return s.Err
	case KindAccent:
		return s.Primary
	default:
		return s.Text
	}
}

// StripItem is one reading on the status strip.
type StripItem struct {
	Icon  string
	Label string
	Value string
	Kind  Kind
}

// Card renders one card of exactly width w: a header carrying the title and an
// optional right-aligned badge, then the body lines. The frame, corner glyphs
// and tinting all come from the skin.
func Card(s theme.Style, title, badge string, body []string, w int) []string {
	if w < 12 {
		w = 12
	}
	padX := s.Met.PadX
	if padX < 0 {
		padX = 0
	}
	switch {
	case s.Met.CardFrame && s.Met.CardHeader == theme.HeaderRule:
		return framed(s, title, badge, body, w, padX, doubleFrame, false)
	case s.Met.CardFrame:
		return framed(s, title, badge, body, w, padX, roundFrame(s.Met.Corners), s.Met.CardHeader == theme.HeaderBar)
	default:
		return frameless(s, title, badge, body, w, padX)
	}
}

// frame is the four corner-and-edge glyphs of a card.
type frame struct {
	tl, tr, bl, br, h, v string
}

func roundFrame(c theme.Corners) frame {
	if c == theme.CornersSquare {
		return frame{"┌", "┐", "└", "┘", "─", "│"}
	}
	return frame{"╭", "╮", "╰", "╯", "─", "│"}
}

var doubleFrame = frame{"╔", "╗", "╚", "╝", "═", "║"}

// framed draws the title into the top border and wraps every body line. bar
// draws that title on a filled accent bar instead of leaving it in the rule.
func framed(s theme.Style, title, badge string, body []string, w, padX int, f frame, bar bool) []string {
	inner := w - 2 - 2*padX
	if inner < 4 {
		inner = 4
	}
	body = append([]string{}, body...)

	out := make([]string, 0, len(body)+2)
	out = append(out, topBorder(s, f, title, badge, w, bar))
	for _, line := range body {
		content := strings.Repeat(" ", padX) + theme.Pad(theme.Truncate(line, inner), inner) + strings.Repeat(" ", padX)
		out = append(out, tintRow(s, s.Colored(s.Border, f.v)+content+s.Colored(s.Border, f.v)))
	}
	out = append(out, tintRow(s, s.Colored(s.Border, f.bl+strings.Repeat(f.h, w-2)+f.br)))
	return out
}

// tintRow paints the card surface behind one finished row. The frame glyphs go
// through the same call so a tinted card is a solid block rather than a striped
// one. Every span inside the row ends with a reset, and a reset drops the
// background with it, so the surface colour is re-armed after each one; without
// that the tint would stop at the first styled value.
func tintRow(s theme.Style, row string) string {
	if !s.Met.CardTint {
		return row
	}
	bg := surfaceSeq(s)
	if bg == "" {
		return row
	}
	row = strings.ReplaceAll(row, "\x1b[0m", "\x1b[0m"+bg)
	row = strings.ReplaceAll(row, "\x1b[m", "\x1b[m"+bg)
	return bg + row + "\x1b[m"
}

// surfaceSeq is the SGR sequence that opens the card's surface background, taken
// from the renderer itself so the terminal colour profile matches the one used
// for every other colour in the panel.
func surfaceSeq(s theme.Style) string {
	probe := lipgloss.NewStyle().Background(s.Surface).Render("x")
	if i := strings.Index(probe, "x"); i > 0 {
		return probe[:i]
	}
	return ""
}

// topBorder is the first row of a framed card: "╭─ Title ─────╮" through
// "╔══ Title ═══╗". The right shoulder carries the badge when there is one.
// topBorder builds the top rule, embedding the title and the optional badge. The
// surface tint is painted per piece rather than around the finished row: a filled
// title bar carries its own background, and wrapping it afterwards would let the
// bar's reset swallow the tint of everything after it.
func topBorder(s theme.Style, f frame, title, badge string, w int, bar bool) string {
	right := ""
	if badge != "" {
		right = " " + badge + " "
	}
	rightW := lipgloss.Width(right)
	head := ""
	if title != "" {
		head = " " + title + " "
	}
	headW := lipgloss.Width(head)
	// Two corners, one leading rule and one rule column on each side of the text.
	fill := w - 3 - headW - rightW
	if fill < 0 {
		head = theme.Truncate(head, maxInt(0, headW+fill))
		headW = lipgloss.Width(head)
		fill = w - 3 - headW - rightW
	}
	if fill < 0 {
		fill = 0
	}
	rule := onSurface(s, lipgloss.NewStyle().Foreground(s.Border), f.h)
	line := onSurface(s, lipgloss.NewStyle().Foreground(s.Border), f.tl) + rule
	if head != "" {
		if bar {
			line += lipgloss.NewStyle().Bold(true).Foreground(s.BarFg).Background(s.Primary).Render(head)
		} else {
			line += onSurface(s, lipgloss.NewStyle().Bold(true).Foreground(s.Text), head)
		}
	}
	line += strings.Repeat(rule, fill)
	if right != "" {
		line += onSurface(s, lipgloss.NewStyle(), right)
	}
	// Pad any rounding gap so the closing corner lands in the last column.
	if gap := w - lipgloss.Width(line) - 1; gap > 0 {
		line += strings.Repeat(rule, gap)
	}
	return line + onSurface(s, lipgloss.NewStyle().Foreground(s.Border), f.tr)
}

// onSurface paints the card tint behind one piece of a row.
func onSurface(s theme.Style, style lipgloss.Style, text string) string {
	if text == "" {
		return ""
	}
	if s.Met.CardTint {
		style = style.Background(s.Surface)
	}
	return style.Render(text)
}

// frameless is the flat skin: a bold title, a hairline, then the body.
func frameless(s theme.Style, title, badge string, body []string, w, padX int) []string {
	pad := strings.Repeat(" ", padX)
	head := s.Bold(s.Primary, title)
	if badge != "" {
		gap := w - lipgloss.Width(head) - lipgloss.Width(badge) - 2*padX
		if gap < 1 {
			gap = 1
		}
		head += strings.Repeat(" ", gap) + badge
	}
	out := []string{pad + head, pad + s.Colored(s.Border, strings.Repeat("─", maxInt(1, w-2*padX)))}
	inner := w - 2*padX
	for _, line := range body {
		out = append(out, pad+theme.Pad(theme.Truncate(line, inner), inner))
	}
	return out
}

// InnerWidth is the width a card body has once the frame and the body padding
// are taken off. Screens use it so their key/value blocks line up with the card
// edges instead of overflowing them.
func InnerWidth(s theme.Style, w int) int {
	padX := s.Met.PadX
	if padX < 0 {
		padX = 0
	}
	inner := w - 2*padX
	if s.Met.CardFrame {
		inner -= 2
	}
	if inner < 4 {
		inner = 4
	}
	return inner
}

// Rule is a dim hairline, or the card's double rule, of exactly width w.
func Rule(s theme.Style, w int) string {
	if w <= 0 {
		return ""
	}
	glyph := "─"
	if s.Met.CardHeader == theme.HeaderRule && s.Met.CardFrame {
		glyph = "═"
	}
	return s.Colored(s.Border, strings.Repeat(glyph, w))
}

// Meter renders a gradient bar of exactly w cells.
func Meter(s theme.Style, ratio float64, w int) string {
	if w <= 0 {
		return ""
	}
	if math.IsNaN(ratio) {
		ratio = 0
	}
	filled := int(math.Round(clamp01(ratio) * float64(w)))
	stops := lipgloss.Blend1D(w, s.GradA, s.GradB)
	var b strings.Builder
	for i := 0; i < w; i++ {
		if i < filled {
			b.WriteString(lipgloss.NewStyle().Foreground(stops[i]).Render("█"))
			continue
		}
		b.WriteString(s.Faint("░"))
	}
	return b.String()
}

// MeterLine renders "label  ███░░░  42%" on one row of exactly w columns.
func MeterLine(s theme.Style, label, value string, ratio float64, w int) string {
	if w < 12 {
		return theme.Truncate(label+" "+value, maxInt(0, w))
	}
	labelW := lipgloss.Width(label)
	valueW := lipgloss.Width(value)
	barW := w - labelW - valueW - 3
	if barW < 4 {
		barW = 4
	}
	return s.Faint(label) + " " + Meter(s, ratio, barW) + " " + s.Value(value)
}

// KV renders aligned key/value rows. Keys are dim so the values carry the eye.
func KV(s theme.Style, rows [][2]string, w int) []string {
	labelW := 0
	for _, r := range rows {
		if n := lipgloss.Width(r[0]); n > labelW {
			labelW = n
		}
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		key := s.Faint(r[0]) + strings.Repeat(" ", labelW-lipgloss.Width(r[0])+2)
		value := theme.Truncate(r[1], maxInt(0, w-labelW-2))
		out = append(out, key+value)
	}
	return out
}

// TwoCol lays two key/value blocks side by side, which is how a card shows twice
// as much on a wide terminal without becoming a wall of text.
func TwoCol(s theme.Style, left, right [][2]string, w int) []string {
	gutter := 2
	colW := (w - gutter) / 2
	leftLines := KV(s, left, colW)
	rightLines := KV(s, right, colW)
	n := len(leftLines)
	if len(rightLines) > n {
		n = len(rightLines)
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		l, r := "", ""
		if i < len(leftLines) {
			l = leftLines[i]
		}
		if i < len(rightLines) {
			r = rightLines[i]
		}
		out = append(out, theme.Pad(l, colW)+strings.Repeat(" ", gutter)+theme.Pad(r, colW))
	}
	return out
}

// Strip renders one row of readings, dropping trailing items when the terminal
// is too narrow for all of them.
func Strip(s theme.Style, items []StripItem, w int) string {
	if w <= 0 || len(items) == 0 {
		return ""
	}
	sep := s.Faint(" │ ")
	if s.Met.StripSep != "" {
		sep = s.Colored(s.BorderStrong, " "+s.Met.StripSep+" ")
	}
	var parts []string
	used := 0
	for _, it := range items {
		piece := ""
		if it.Icon != "" {
			piece += it.Icon + " "
		}
		piece += s.Faint(it.Label)
		if it.Value != "" {
			piece += " " + s.Bold(it.Kind.Color(s), it.Value)
		}
		add := lipgloss.Width(piece)
		if used > 0 {
			add += lipgloss.Width(sep)
		}
		if used+add > w {
			break
		}
		parts = append(parts, piece)
		used += add
	}
	if len(parts) == 0 {
		return s.Faint(theme.Truncate(items[0].Label, w))
	}
	line := strings.Join(parts, sep)
	if rest := w - lipgloss.Width(line); rest > 0 {
		line += strings.Repeat(" ", rest)
	}
	return line
}

// Fit normalises a block to exactly h lines of at most w columns, padding the
// bottom with blanks. Every screen ends with Fit so its frame never resizes.
func Fit(lines []string, w, h int) string {
	if h <= 0 {
		return ""
	}
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
	return strings.Join(out, "\n")
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
