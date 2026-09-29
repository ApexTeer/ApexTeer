package theme

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// Corners selects the card corner glyphs: rounded for a soft frame, square for a
// technical one.
type Corners int

// The two corner treatments. Rounded is the zero value, so a skin that says nothing
// about corners gets the softer frame.
const (
	CornersRounded Corners = iota
	CornersSquare
)

// HeaderStyle selects how a card title is drawn.
type HeaderStyle int

const (
	// HeaderBar puts an accent bar and gradient text above the card body.
	HeaderBar HeaderStyle = iota
	// HeaderRule embeds the title in a double top rule.
	HeaderRule
	// HeaderPlain is a bold title over a dim hairline.
	HeaderPlain
)

// Metrics are the geometry knobs of a skin. They are deliberately few: a skin
// may change framing, tinting, spacing and grouping, but not the order of the
// information on a screen.
type Metrics struct {
	Corners    Corners
	CardTint   bool
	CardFrame  bool
	CardHeader HeaderStyle
	// PadX is the number of blank columns between a card frame and its body.
	PadX int
	// Compact drops the blank line between cards.
	Compact bool
	// StripSep is the glyph drawn between status-strip items. Empty means the
	// neutral " │ " every skin uses by default.
	StripSep string
}

// Skin is one selectable look: a palette, the geometry the layout uses, and the grouping
// of the root navigation. The panel ships four, chosen with --skin, so the shapes of the
// interface stay the same while the look can be swapped without touching a screen.
// Palettes come in pairs because the terminal's background is not known until startup.
type Skin struct {
	ID    string
	Name  string
	Note  string
	Dark  Palette
	Light Palette
	Met   Metrics
}

// Style is a skin resolved for one terminal background. It embeds the palette so
// the palette helpers stay reachable as methods.
type Style struct {
	Palette
	Met  Metrics
	ID   string
	Name string
	Dark bool
}

// Style resolves the skin for a dark or light background.
func (s Skin) Style(dark bool) Style {
	pal := s.Light
	if dark {
		pal = s.Dark
	}
	return Style{Palette: pal, Met: s.Met, ID: s.ID, Name: s.Name, Dark: dark}
}

// Skins returns the selectable looks, in the order they are offered.
func Skins() []Skin {
	return []Skin{jadeSkin(), auroraSkin(), emberSkin(), graphiteSkin()}
}

// DefaultSkin is the look used when nothing is configured.
func DefaultSkin() Skin {
	return jadeSkin()
}

// SkinByID resolves a skin from a flag value. Besides the ids it accepts single
// letters in the order Skins returns them, which is what the system screen and
// the prototype comparison hand out.
func SkinByID(id string) (Skin, bool) {
	all := Skins()
	id = strings.ToLower(strings.TrimSpace(id))
	if len(id) == 1 && id[0] >= 'a' && id[0] <= 'z' {
		if i := int(id[0] - 'a'); i < len(all) {
			return all[i], true
		}
		return Skin{}, false
	}
	for _, s := range all {
		if s.ID == id {
			return s, true
		}
	}
	return Skin{}, false
}

// Faint renders tertiary text: present, but never competing with a value.
func (p Palette) Faint(s string) string {
	return lipgloss.NewStyle().Foreground(p.TextFaint).Render(s)
}

// Accented renders a value in the skin's accent colour.
func (p Palette) Accented(s string) string {
	return lipgloss.NewStyle().Foreground(p.Primary).Render(s)
}

// jadeSkin is the showpiece: rounded frames, tinted card bodies, a filled jade
// bar carrying every card title and a brass second accent. It is the default,
// because a panel that gets screenshotted should look deliberate.
func jadeSkin() Skin {
	return Skin{
		ID:   "jade",
		Name: "Jade",
		Note: "rounded tinted cards, filled jade title bars, brass accents, roomiest",
		Dark: Palette{
			Primary:      lipgloss.Color("#34c59b"),
			Accent:       lipgloss.Color("#d9a95c"),
			OK:           lipgloss.Color("#4ecba4"),
			Warn:         lipgloss.Color("#e4b445"),
			Err:          lipgloss.Color("#ec6b6b"),
			Text:         lipgloss.Color("#e6f0ea"),
			Muted:        lipgloss.Color("#9cb3a8"),
			TextFaint:    lipgloss.Color("#6c857a"),
			Border:       lipgloss.Color("#1f3a33"),
			BorderStrong: lipgloss.Color("#356054"),
			Surface:      lipgloss.Color("#0d1a16"),
			SurfaceAlt:   lipgloss.Color("#12241f"),
			SelBg:        lipgloss.Color("#1b3b32"),
			SelFg:        lipgloss.Color("#f3fbf7"),
			GradA:        lipgloss.Color("#34c59b"),
			GradB:        lipgloss.Color("#d9a95c"),
			BarFg:        lipgloss.Color("#06120e"),
			IsDark:       true,
		},
		Light: Palette{
			Primary:      lipgloss.Color("#0f7a63"),
			Accent:       lipgloss.Color("#96631a"),
			OK:           lipgloss.Color("#0d7a4d"),
			Warn:         lipgloss.Color("#8f6100"),
			Err:          lipgloss.Color("#b3261e"),
			Text:         lipgloss.Color("#14211d"),
			Muted:        lipgloss.Color("#4b6159"),
			TextFaint:    lipgloss.Color("#7a8d85"),
			Border:       lipgloss.Color("#cde0d7"),
			BorderStrong: lipgloss.Color("#9abbaf"),
			Surface:      lipgloss.Color("#f5faf8"),
			SurfaceAlt:   lipgloss.Color("#eaf4ef"),
			SelBg:        lipgloss.Color("#cbe9dd"),
			SelFg:        lipgloss.Color("#0a2b22"),
			GradA:        lipgloss.Color("#0f7a63"),
			GradB:        lipgloss.Color("#a3721c"),
			BarFg:        lipgloss.Color("#ffffff"),
		},
		Met: Metrics{
			Corners:    CornersRounded,
			CardTint:   true,
			CardFrame:  true,
			CardHeader: HeaderBar,
			PadX:       2,
			StripSep:   "┃",
		},
	}
}

func auroraSkin() Skin {
	return Skin{
		ID:   "aurora",
		Name: "Aurora",
		Note: "cool rounded cards, gradient titles, blue-to-violet accent",
		Dark: Palette{
			Primary:      lipgloss.Color("#38bdf8"),
			Accent:       lipgloss.Color("#6366f1"),
			OK:           lipgloss.Color("#34d399"),
			Warn:         lipgloss.Color("#fbbf24"),
			Err:          lipgloss.Color("#f87171"),
			Text:         lipgloss.Color("#e6edf7"),
			Muted:        lipgloss.Color("#93a4bd"),
			TextFaint:    lipgloss.Color("#5f7590"),
			Border:       lipgloss.Color("#1d3050"),
			BorderStrong: lipgloss.Color("#2c4a75"),
			Surface:      lipgloss.Color("#0f1a2b"),
			SurfaceAlt:   lipgloss.Color("#152238"),
			SelBg:        lipgloss.Color("#1c3352"),
			SelFg:        lipgloss.Color("#f8fbff"),
			GradA:        lipgloss.Color("#38bdf8"),
			GradB:        lipgloss.Color("#a78bfa"),
			BarFg:        lipgloss.Color("#0f1a2b"),
			IsDark:       true,
		},
		Light: Palette{
			Primary:      lipgloss.Color("#0369a1"),
			Accent:       lipgloss.Color("#4f46e5"),
			OK:           lipgloss.Color("#047857"),
			Warn:         lipgloss.Color("#b45309"),
			Err:          lipgloss.Color("#b91c1c"),
			Text:         lipgloss.Color("#16233a"),
			Muted:        lipgloss.Color("#4a5b73"),
			TextFaint:    lipgloss.Color("#7c8ba3"),
			Border:       lipgloss.Color("#cbd8ea"),
			BorderStrong: lipgloss.Color("#94a9c9"),
			Surface:      lipgloss.Color("#f2f6fc"),
			SurfaceAlt:   lipgloss.Color("#e8eef8"),
			SelBg:        lipgloss.Color("#cfe4ff"),
			SelFg:        lipgloss.Color("#0b2545"),
			GradA:        lipgloss.Color("#0284c7"),
			GradB:        lipgloss.Color("#6d28d9"),
			BarFg:        lipgloss.Color("#ffffff"),
		},
		Met: Metrics{
			Corners:    CornersRounded,
			CardTint:   true,
			CardFrame:  true,
			CardHeader: HeaderBar,
			PadX:       2,
		},
	}
}

func emberSkin() Skin {
	return Skin{
		ID:   "ember",
		Name: "Ember",
		Note: "warm layered panels, double rules, orange-to-pink accent",
		Dark: Palette{
			Primary:      lipgloss.Color("#fb923c"),
			Accent:       lipgloss.Color("#f472b6"),
			OK:           lipgloss.Color("#4ade80"),
			Warn:         lipgloss.Color("#facc15"),
			Err:          lipgloss.Color("#fb7185"),
			Text:         lipgloss.Color("#f5ede7"),
			Muted:        lipgloss.Color("#b7a294"),
			TextFaint:    lipgloss.Color("#8a7466"),
			Border:       lipgloss.Color("#3d2e25"),
			BorderStrong: lipgloss.Color("#6b4a35"),
			Surface:      lipgloss.Color("#221a15"),
			SurfaceAlt:   lipgloss.Color("#2c231c"),
			SelBg:        lipgloss.Color("#4a2f1f"),
			SelFg:        lipgloss.Color("#fff6ee"),
			GradA:        lipgloss.Color("#fb923c"),
			GradB:        lipgloss.Color("#f472b6"),
			BarFg:        lipgloss.Color("#221a15"),
			IsDark:       true,
		},
		Light: Palette{
			Primary:      lipgloss.Color("#c2410c"),
			Accent:       lipgloss.Color("#be185d"),
			OK:           lipgloss.Color("#15803d"),
			Warn:         lipgloss.Color("#a16207"),
			Err:          lipgloss.Color("#be123c"),
			Text:         lipgloss.Color("#33241a"),
			Muted:        lipgloss.Color("#6b5546"),
			TextFaint:    lipgloss.Color("#937f70"),
			Border:       lipgloss.Color("#e3cdbc"),
			BorderStrong: lipgloss.Color("#c39a7c"),
			Surface:      lipgloss.Color("#fdf5ef"),
			SurfaceAlt:   lipgloss.Color("#f7e9de"),
			SelBg:        lipgloss.Color("#fce4d0"),
			SelFg:        lipgloss.Color("#4a2610"),
			GradA:        lipgloss.Color("#ea580c"),
			GradB:        lipgloss.Color("#db2777"),
			BarFg:        lipgloss.Color("#ffffff"),
		},
		Met: Metrics{
			Corners:    CornersSquare,
			CardTint:   true,
			CardFrame:  true,
			CardHeader: HeaderRule,
			PadX:       2,
		},
	}
}

func graphiteSkin() Skin {
	return Skin{
		ID:   "graphite",
		Name: "Graphite",
		Note: "flat monochrome, hairline rules, one teal accent, densest",
		Dark: Palette{
			Primary:      lipgloss.Color("#2dd4bf"),
			Accent:       lipgloss.Color("#22d3ee"),
			OK:           lipgloss.Color("#3fb950"),
			Warn:         lipgloss.Color("#d29922"),
			Err:          lipgloss.Color("#f85149"),
			Text:         lipgloss.Color("#d7dee8"),
			Muted:        lipgloss.Color("#8b949e"),
			TextFaint:    lipgloss.Color("#6b7480"),
			Border:       lipgloss.Color("#30363d"),
			BorderStrong: lipgloss.Color("#4b5563"),
			Surface:      lipgloss.Color("#0d1117"),
			SurfaceAlt:   lipgloss.Color("#161b22"),
			SelBg:        lipgloss.Color("#21323a"),
			SelFg:        lipgloss.Color("#eaf7f5"),
			GradA:        lipgloss.Color("#2dd4bf"),
			GradB:        lipgloss.Color("#22d3ee"),
			BarFg:        lipgloss.Color("#0d1117"),
			IsDark:       true,
		},
		Light: Palette{
			Primary:      lipgloss.Color("#0f766e"),
			Accent:       lipgloss.Color("#0e7490"),
			OK:           lipgloss.Color("#1a7f37"),
			Warn:         lipgloss.Color("#9a6700"),
			Err:          lipgloss.Color("#cf222e"),
			Text:         lipgloss.Color("#1f2328"),
			Muted:        lipgloss.Color("#57606a"),
			TextFaint:    lipgloss.Color("#7d8590"),
			Border:       lipgloss.Color("#d0d7de"),
			BorderStrong: lipgloss.Color("#8c959f"),
			Surface:      lipgloss.Color("#ffffff"),
			SurfaceAlt:   lipgloss.Color("#f6f8fa"),
			SelBg:        lipgloss.Color("#d5f0ec"),
			SelFg:        lipgloss.Color("#083b36"),
			GradA:        lipgloss.Color("#0d9488"),
			GradB:        lipgloss.Color("#0891b2"),
			BarFg:        lipgloss.Color("#ffffff"),
		},
		Met: Metrics{
			Corners:    CornersSquare,
			CardTint:   false,
			CardFrame:  false,
			CardHeader: HeaderPlain,
			PadX:       1,
			Compact:    true,
		},
	}
}

// PaletteOf keeps the historical entry points working: the default skin's
// palettes are what Dark() and Light() always returned.
