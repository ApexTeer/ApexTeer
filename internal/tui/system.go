package tui

import (
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EasySBTeam/EasySB/internal/icons"
	"github.com/EasySBTeam/EasySB/internal/theme"
	"github.com/EasySBTeam/EasySB/internal/ui"
)

// systemModel is the system information screen. It reports what the panel is
// running on and is the one place where the look can be changed from inside the
// interface: the skin, the palette and the icon set all apply on the next render,
// so the effect of a choice is visible without restarting.
type systemModel struct {
	// cursor is the highlighted skin in the appearance card.
	cursor int
	// note is transient confirmation of the last switch.
	note string
}

func newSystemModel(a *App) *systemModel {
	m := &systemModel{}
	for i, s := range theme.Skins() {
		if s.ID == a.skin.ID {
			m.cursor = i
		}
	}
	return m
}

// handleKey returns handled=false for keys the screen does not own, so the global
// shortcuts (language, refresh, quit) keep working while it is open.
func (m *systemModel) handleKey(msg tea.KeyMsg, a *App) (tea.Cmd, bool) {
	key := msg.String()
	switch key {
	case "esc", "backspace":
		// q is deliberately absent: it quits the panel from every page, so it must
		// reach the global shortcut below rather than closing this screen.
		a.closeSystem()
		return nil, true
	case "up", "k":
		if n := len(theme.Skins()); n > 0 {
			m.cursor = (m.cursor - 1 + n) % n
		}
		return nil, true
	case "down", "j":
		if n := len(theme.Skins()); n > 0 {
			m.cursor = (m.cursor + 1) % n
		}
		return nil, true
	case "enter":
		if sk, ok := m.highlighted(); ok {
			m.applySkin(a, sk)
		}
		return nil, true
	case "t":
		a.setDark(!a.dark)
		a.remember()
		m.note = a.lang.T("theme_"+themeName(a.dark)) + " " + a.lang.T("skin_switched")
		return nil, true
	case "i":
		a.setIcons(nextIconSet(a.iconSet))
		a.remember()
		m.note = a.lang.T("icons_"+a.iconSet.ID) + " " + a.lang.T("skin_switched")
		return nil, true
	}
	// A single letter picks a skin directly, which turns a look comparison into
	// four keystrokes instead of four arrow presses.
	if sk, ok := theme.SkinByID(key); ok && len(key) == 1 {
		m.applySkin(a, sk)
		m.cursor = indexOfSkin(sk.ID)
		return nil, true
	}
	return nil, false
}

func (m *systemModel) applySkin(a *App, sk theme.Skin) {
	a.lockLook()
	a.setSkin(sk, a.dark)
	a.remember()
	m.note = a.lang.T("skin_"+sk.ID) + " " + a.lang.T("skin_switched")
}

func (m *systemModel) highlighted() (theme.Skin, bool) {
	all := theme.Skins()
	if m.cursor < 0 || m.cursor >= len(all) {
		return theme.Skin{}, false
	}
	return all[m.cursor], true
}

func indexOfSkin(id string) int {
	for i, s := range theme.Skins() {
		if s.ID == id {
			return i
		}
	}
	return 0
}

// themeName is the i18n suffix for a background.
func themeName(dark bool) string {
	if dark {
		return "dark"
	}
	return "light"
}

// nextIconSet cycles the marker sets the way the key hint promises.
func nextIconSet(cur icons.Set) icons.Set {
	if cur.ID == "ascii" {
		return icons.Symbols()
	}
	return icons.ASCII()
}

// body lays the screen out as two columns of cards when the width allows it, and
// as one stacked column when it does not. The cards are ordered by how much they
// matter on a short terminal: the switcher is never the thing that gets cut.
func (m *systemModel) body(a *App, w, h int) []string {
	s := a.style()
	if h <= 0 {
		return nil
	}
	const gap = 2
	colW := (w - gap) / 2
	if colW < 34 {
		out := stack([][]string{
			m.appearanceCard(a, w),
			m.terminalCard(a, w),
			m.hostCard(a, w),
			m.buildCard(a, w),
		}, s.Met.Compact)
		return padLines(out, w, h)
	}
	left := stack([][]string{
		m.appearanceCard(a, colW),
		m.terminalCard(a, colW),
	}, s.Met.Compact)
	right := stack([][]string{
		m.hostCard(a, colW),
		m.buildCard(a, colW),
	}, s.Met.Compact)
	return padLines(joinColumns(left, right, colW, gap), w, h)
}

// appearanceCard is the live switcher: every skin with the current one marked,
// then the palette and marker set in force.
func (m *systemModel) appearanceCard(a *App, w int) []string {
	s := a.style()
	inner := ui.InnerWidth(s, w)
	nameW := 0
	skins := theme.Skins()
	for _, sk := range skins {
		if n := len(a.lang.T("skin_" + sk.ID)); n > nameW {
			nameW = n
		}
	}
	var body []string
	for i, sk := range skins {
		name := a.lang.T("skin_" + sk.ID)
		note := a.lang.T("skin_" + sk.ID + "_note")
		mark := "  "
		row := name + strings.Repeat(" ", maxInt(1, nameW-len(name)+2)) + s.Faint(note)
		if sk.ID == a.skin.ID {
			mark = a.iconSet.OK + " "
			row = s.Bold(s.Primary, name) + strings.Repeat(" ", maxInt(1, nameW-len(name)+2)) + s.Faint(note)
		} else if i == m.cursor {
			mark = a.iconSet.Bullet + " "
		}
		row = theme.Truncate(mark+row, inner)
		if i == m.cursor {
			row = s.SelectedRow(theme.Pad(row, inner))
		} else {
			row = theme.Pad(row, inner)
		}
		body = append(body, row)
	}
	body = append(body, "")
	body = append(body, a.lang.T("dev_theme")+"  "+choice(a, a.lang.T("theme_"+themeName(a.dark)), a.themeAuto))
	body = append(body, a.lang.T("dev_icons")+"  "+choice(a, a.lang.T("icons_"+a.iconSet.ID), false))
	body = append(body, a.lang.T("dev_language")+"  "+choice(a, a.lang.T(languageKey(a)), false))
	return ui.Card(s, a.lang.T("panel_appearance"), m.note, body, w)
}

// languageKey is the i18n key naming the language currently in force.
func languageKey(a *App) string {
	if a.lang.Code() == "C" {
		return "lang_cn"
	}
	return "lang_en"
}

// choice renders one "current value" cell where the value is the active choice.
func choice(a *App, value string, auto bool) string {
	s := a.style()
	out := s.Bold(s.Accent, value)
	if auto {
		out += s.Faint(" (" + a.lang.T("theme_auto") + ")")
	}
	return out
}

// terminalCard describes the terminal itself, including a preview row: a glyph
// the font lacks shows up here as a box before it can confuse a card elsewhere.
func (m *systemModel) terminalCard(a *App, w int) []string {
	s := a.style()
	inner := ui.InnerWidth(s, w)
	ic := a.iconSet
	glyphs := []string{
		ic.Host, ic.Core, ic.Rocket, ic.Globe, ic.Account, ic.Subscribe, ic.Service,
		ic.Refresh, ic.Trash, ic.Tool, ic.QR, ic.Link, ic.Download,
		ic.OK, ic.Warn, ic.Err, ic.Info, ic.Running, ic.Stopped,
	}
	// The preview wraps under its label rather than being cut off: a glyph that
	// did not fit could be exactly the one the reader wanted to check.
	label := a.lang.T("term_glyphs")
	indent := strings.Repeat(" ", len(label)+2)
	var preview []string
	perLine := maxInt(6, (inner-len(label)-2+1)/2)
	for i := 0; i < len(glyphs); i += perLine {
		end := i + perLine
		if end > len(glyphs) {
			end = len(glyphs)
		}
		preview = append(preview, strings.Join(glyphs[i:end], " "))
	}
	body := []string{
		kvRow(a, "term_type", withFallback(os.Getenv("TERM"), a.lang.T("not_set"))),
		kvRow(a, "term_size", fmt.Sprintf("%d × %d", a.width, a.height)),
	}
	for i, line := range preview {
		head := indent
		if i == 0 {
			head = s.Faint(label) + "  "
		}
		body = append(body, head+s.Bold(s.Text, line))
	}
	return ui.Card(s, a.lang.T("panel_terminal"), "", body, w)
}

// hostCard is the identity of the machine and the three resources that run out.
func (m *systemModel) hostCard(a *App, w int) []string {
	s := a.style()
	st := a.status
	inner := ui.InnerWidth(s, w)
	var meters []string
	meter := func(labelKey string, total, free uint64) {
		if total == 0 {
			return
		}
		free = minU64(free, total)
		meters = append(meters, ui.MeterLine(s, a.lang.T(labelKey), usageCell(total, free),
			float64(total-free)/float64(total), inner))
	}
	meter("device_memory", st.MemTotal, st.MemAvail)
	meter("device_disk", st.DiskTotal, st.DiskFree)
	left := [][2]string{
		a.kv("device_host", st.Hostname, ui.KindPlain),
		a.kv("device_os", withFallback(st.OS, a.lang.T("state_unknown")), ui.KindPlain),
		a.kv("device_kernel", withFallback(st.Kernel, a.lang.T("state_unknown")), ui.KindPlain),
		a.kv("device_cpu", a.cpuSummary(), ui.KindPlain),
	}
	right := [][2]string{
		a.kv("device_timezone", withFallback(st.Timezone, a.lang.T("not_set")), ui.KindPlain),
		a.kv("device_uptime", withFallback(humanDuration(st.Uptime), "—"), ui.KindPlain),
		a.kv("device_load", withFallback(st.LoadAvg, "—"), ui.KindPlain),
		a.kv("device_local_ipv4", withFallback(st.LocalIPv4, "—"), ui.KindPlain),
	}
	body := make([]string, 0, len(meters)+2)
	if len(meters) > 0 {
		body = append(body, meters...)
		body = append(body, "")
	}
	body = append(body, ui.TwoCol(s, left, right, inner)...)
	return ui.Card(s, a.lang.T("panel_device"), "", body, w)
}

// buildCard is what the panel is made of: which core, which script, and whether
// the service is up.
func (m *systemModel) buildCard(a *App, w int) []string {
	s := a.style()
	svcText, svcKind := a.serviceState()
	nodeText, nodeKind := a.nodeState()
	coreText, coreKind := a.coreSummary()
	left := [][2]string{
		a.kv("ver_script", a.scriptVersion, ui.KindOK),
		a.kv("ver_core", coreText, coreKind),
	}
	right := [][2]string{
		a.kv("ov_service", svcText, svcKind),
		a.kv("ov_node", nodeText, nodeKind),
	}
	return ui.Card(s, a.lang.T("panel_build"), "", ui.TwoCol(s, left, right, ui.InnerWidth(s, w)), w)
}

// kvRow is a label/value pair without the tone colouring of a status row.
func kvRow(a *App, labelKey, value string) string {
	s := a.style()
	return s.Faint(a.lang.T(labelKey)) + "  " + s.Bold(s.Text, value)
}

// stack joins cards vertically, one blank row apart unless the skin is compact.
func stack(cards [][]string, compact bool) []string {
	var out []string
	for _, card := range cards {
		if len(card) == 0 {
			continue
		}
		if len(out) > 0 && !compact {
			out = append(out, "")
		}
		out = append(out, card...)
	}
	return out
}

// joinColumns lays two blocks side by side, padding the shorter one.
func joinColumns(left, right []string, colW, gap int) []string {
	rows := maxInt(len(left), len(right))
	out := make([]string, 0, rows)
	pad := strings.Repeat(" ", maxInt(0, gap))
	for i := 0; i < rows; i++ {
		l, r := "", ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		out = append(out, theme.Pad(l, colW)+pad+r)
	}
	return out
}
