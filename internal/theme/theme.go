// Package theme is the panel's appearance: four complete skins, each with a dark and a
// light palette, the geometry a skin is allowed to choose, and the small rendering
// helpers every screen draws its boxes and columns with.
package theme

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Palette is one resolved set of colors. The first group predates the skins;
// the second group is what the card-based screens use. TextFaint is tertiary
// text (group headings, units), the two borders separate a resting card from a
// focused one, Surface and SurfaceAlt fill tinted cards, and GradA/GradB are the
// two stops of a header or meter gradient.
type Palette struct {
	Primary color.Color
	Accent  color.Color
	OK      color.Color
	Warn    color.Color
	Err     color.Color
	Text    color.Color
	Muted   color.Color
	Border  color.Color
	SelBg   color.Color
	SelFg   color.Color

	TextFaint    color.Color
	BorderStrong color.Color
	Surface      color.Color
	SurfaceAlt   color.Color
	GradA        color.Color
	GradB        color.Color
	// BarFg is the ink for text sitting on a filled Primary bar, which is how a
	// skin with the HeaderBar card header draws its titles.
	BarFg  color.Color
	IsDark bool
}

// Dark is the default skin's dark palette.
func Dark() Palette { return DefaultSkin().Dark }

// Light is the default skin's light palette. The hues match Dark, but every
// color is darkened so it stays readable on white: the dark palette's pastel
// accents and near-white body text all but disappear there.
func Light() Palette { return DefaultSkin().Light }

// Bold renders s in bold and in the colour the caller picked, for a heading whose role
// colour is decided at the call site rather than taken from the palette.
func (p Palette) Bold(c color.Color, s string) string {
	return lipgloss.NewStyle().Bold(true).Foreground(c).Render(s)
}

// Colored renders s in the colour the caller picked. It is the plain form of Bold, for
// the places a weight change would say something that was not meant.
func (p Palette) Colored(c color.Color, s string) string {
	return lipgloss.NewStyle().Foreground(c).Render(s)
}

// Dim renders secondary text: present, but not competing with the value beside it.
// Faint is the step below it.
func (p Palette) Dim(s string) string {
	return lipgloss.NewStyle().Foreground(p.Muted).Render(s)
}

// Label renders the name of a field, which the accent colour marks as the thing being
// asked for rather than the answer. Its pair is Value.
func (p Palette) Label(s string) string {
	return lipgloss.NewStyle().Foreground(p.Accent).Bold(true).Render(s)
}

// Value renders the reading that belongs to a Label.
func (p Palette) Value(s string) string {
	return lipgloss.NewStyle().Foreground(p.Text).Render(s)
}

// SelectedRow paints one full menu row as the selection cursor. It spans the
// whole row, label and trailing description included, so the highlight is not
// limited to the label column.
func (p Palette) SelectedRow(s string) string {
	return lipgloss.NewStyle().Bold(true).Foreground(p.SelFg).Background(p.SelBg).Render(s)
}

// State renders a status word in the colour of its meaning: a warning, a success, or -
// when it is neither - a failure, which is the reading an unknown state should not get
// by accident.
func (p Palette) State(s string, ok bool, warn bool) string {
	switch {
	case warn:
		return lipgloss.NewStyle().Bold(true).Foreground(p.Warn).Render(s)
	case ok:
		return lipgloss.NewStyle().Bold(true).Foreground(p.OK).Render(s)
	default:
		return lipgloss.NewStyle().Bold(true).Foreground(p.Err).Render(s)
	}
}

// Truncate clips s to at most w display cells, appending an ellipsis. It is
// ANSI-aware, so already-styled text can be clipped without leaving a dangling
// escape sequence or counting it toward the width.
func Truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	return ansi.Truncate(s, w, "…")
}

// Pad right-pads s with spaces to w display cells. Width is counted in cells rather
// than runes, so a Chinese label holds the same column as a Latin one, and already
// styled text is padded without its escape sequences being counted.
func Pad(s string, w int) string {
	if w <= 0 {
		return s
	}
	pad := w - lipgloss.Width(s)
	if pad <= 0 {
		return s
	}
	return s + strings.Repeat(" ", pad)
}

// Box draws a framed card: the title set into the top rule and the content clipped and
// padded inside, both to the same width so the right border stays in one column. It
// returns the frame rather than writing it, so a screen composes it with the rest of a
// page. A width below the frame's own minimum is raised to it rather than drawn wrong.
func Box(title, content string, width int, border color.Color, titleColor color.Color) string {
	if width < 8 {
		width = 8
	}
	title = Truncate(title, width-6)
	tw := lipgloss.Width(title)
	fill := width - 5 - tw
	if fill < 0 {
		fill = 0
	}
	top := lipgloss.NewStyle().Foreground(border).Render("╭─ ") +
		lipgloss.NewStyle().Bold(true).Foreground(titleColor).Render(title) +
		lipgloss.NewStyle().Foreground(border).Render(" "+strings.Repeat("─", fill)+"╮")

	inner := width - 4
	lines := strings.Split(content, "\n")
	var body strings.Builder
	for _, ln := range lines {
		body.WriteString(lipgloss.NewStyle().Foreground(border).Render("│ "))
		body.WriteString(Pad(Truncate(ln, inner), inner))
		body.WriteString(lipgloss.NewStyle().Foreground(border).Render(" │"))
		body.WriteString("\n")
	}
	bottom := lipgloss.NewStyle().Foreground(border).Render("╰" + strings.Repeat("─", width-2) + "╯")
	return top + "\n" + body.String() + bottom
}

// Center places s in the middle of a field of the given width, measuring styled
// text with lipgloss so ANSI sequences do not skew the padding.
func Center(s string, width int) string {
	if width <= 0 {
		return s
	}
	pad := width - lipgloss.Width(s)
	if pad <= 0 {
		return s
	}
	return strings.Repeat(" ", pad/2) + s
}

// TopRule draws the opening border of a panel.
func TopRule(width int, c color.Color) string {
	if width < 2 {
		return ""
	}
	return lipgloss.NewStyle().Foreground(c).Render("╭" + strings.Repeat("─", width-2) + "╮")
}

// BottomRule draws the closing border of a panel.
func BottomRule(width int, c color.Color) string {
	if width < 2 {
		return ""
	}
	return lipgloss.NewStyle().Foreground(c).Render("╰" + strings.Repeat("─", width-2) + "╯")
}

// FrameLine wraps one body row in the panel's side borders, padding it to the
// inner width so the right border stays aligned.
func FrameLine(s string, width int, c color.Color) string {
	inner := width - 4
	if inner < 0 {
		inner = 0
	}
	return lipgloss.NewStyle().Foreground(c).Render("│ ") + Pad(Truncate(s, inner), inner) + lipgloss.NewStyle().Foreground(c).Render(" │")
}
