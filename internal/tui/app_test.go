package tui

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/EasySB-Team/EasySB/internal/bbr"
	"github.com/EasySB-Team/EasySB/internal/i18n"
	"github.com/EasySB-Team/EasySB/internal/prefs"
	"github.com/EasySB-Team/EasySB/internal/state"
	"github.com/EasySB-Team/EasySB/internal/sysinfo"
	"github.com/EasySB-Team/EasySB/internal/theme"
	"github.com/EasySB-Team/EasySB/internal/toolbox"
	"github.com/EasySB-Team/EasySB/internal/toolbox/tools"
	"github.com/EasySB-Team/EasySB/internal/ui"
	"github.com/EasySB-Team/EasySB/internal/user"
)

func press(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: code})
}

// ansiSGR matches the colour sequences the panel wraps its text in, so a test can
// look for the text itself.
var ansiSGR = regexp.MustCompile("\x1b\\[[0-9;]*m")

func stripANSI(s string) string { return ansiSGR.ReplaceAllString(s, "") }

func typeRune(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: r, Text: string(r)})
}

func newTestApp(t *testing.T) *App {
	t.Helper()
	// The interface choices and the toolbox board are written to disk, so they go to
	// temporary files instead of the real panel directory.
	t.Setenv(prefs.PathEnv, filepath.Join(t.TempDir(), "easysb-ui.conf"))
	t.Setenv(toolbox.BoardEnv, filepath.Join(t.TempDir(), "easysb-toolbox.json"))
	a := New("test", i18n.Chinese)
	m, _ := a.Update(tea.WindowSizeMsg{Width: 100, Height: 34})
	a = m.(*App)
	m, _ = a.Update(statusMsg(sysinfo.Status{
		Service:     "running",
		CoreVersion: "1.15.0-alpha.6",
		Autostart:   "enabled",
		Domain:      "example.com",
		Deployed:    true,
		Ports:       []sysinfo.PortInfo{{Protocol: "AnyTLS", Port: "8000", Enabled: true}},
	}))
	return m.(*App)
}

func TestDashboardFitsTerminal(t *testing.T) {
	// The inline renderer cannot erase lines that scrolled off the top, so the
	// boxed dashboard must never be taller than the terminal. This exercises the
	// size where the menu has to scroll itself.
	for _, h := range []int{9, 10, 12, 14, 15, 16, 18, 20, 24, 30, 40, 60} {
		a := New("test", i18n.Chinese)
		a.width, a.height = 100, h
		a.sized = true
		a.status = sysinfo.Collect("test")
		a.ready = true
		if lines := strings.Count(a.dashboard(), "\n") + 1; lines > h {
			t.Fatalf("height %d: dashboard drew %d lines", h, lines)
		}
		// The node card is taller than the device card, so exercise it too.
		a.push(buildNode())
		if lines := strings.Count(a.dashboard(), "\n") + 1; lines > h {
			t.Fatalf("height %d: node dashboard drew %d lines", h, lines)
		}
	}
}

func TestDashboardRendersStatus(t *testing.T) {
	a := newTestApp(t)
	v := a.View()
	for _, want := range []string{i18n.Chinese.T("banner_tagline"), "example.com", "1.15.0-alpha.6"} {
		if !strings.Contains(v.Content, want) {
			t.Fatalf("view missing %q", want)
		}
	}
}

func TestRecursiveNavigation(t *testing.T) {
	a := newTestApp(t)

	m, _ := a.Update(press(tea.KeyDown))
	a = m.(*App)
	if got := a.selected().id; got != "node" {
		t.Fatalf("expected node selected, got %s", got)
	}

	m, _ = a.Update(press(tea.KeyEnter))
	a = m.(*App)
	if a.current().id != "node" {
		t.Fatalf("expected node menu, got %s", a.current().id)
	}

	// node menu: deploy, protocols, params -> params is the third row.
	for i := 0; i < 2; i++ {
		m, _ = a.Update(press(tea.KeyDown))
		a = m.(*App)
	}
	m, _ = a.Update(press(tea.KeyEnter))
	a = m.(*App)
	if a.current().id != "params" {
		t.Fatalf("expected params menu, got %s", a.current().id)
	}

	// ports is the second row of the params menu, right below the hop range.
	for i := 0; i < 1; i++ {
		m, _ = a.Update(press(tea.KeyDown))
		a = m.(*App)
	}
	m, _ = a.Update(press(tea.KeyEnter))
	a = m.(*App)
	if a.current().id != "ports" {
		t.Fatalf("expected ports menu, got %s", a.current().id)
	}

	m, _ = a.Update(press(tea.KeyEscape))
	a = m.(*App)
	m, _ = a.Update(press(tea.KeyEscape))
	a = m.(*App)
	if a.current().id != "node" {
		t.Fatalf("expected to return to node menu, got %s", a.current().id)
	}
}

func TestNavRowReturnsToParent(t *testing.T) {
	a := newTestApp(t)
	m, _ := a.Update(press(tea.KeyDown))
	a = m.(*App)
	m, _ = a.Update(press(tea.KeyEnter))
	a = m.(*App)
	if a.current().id != "node" {
		t.Fatalf("expected node menu, got %s", a.current().id)
	}

	// node menu has three nodes plus the trailing navigation row.
	for i := 0; i < 3; i++ {
		m, _ = a.Update(press(tea.KeyDown))
		a = m.(*App)
	}
	if !a.onNavRow() {
		t.Fatalf("expected cursor on nav row")
	}
	if !strings.Contains(a.View().Content, i18n.Chinese.T("nav_back")) {
		t.Fatalf("nav row label missing from view")
	}

	m, _ = a.Update(press(tea.KeyEnter))
	a = m.(*App)
	if a.current().id != "root" {
		t.Fatalf("expected to return to root, got %s", a.current().id)
	}
}

func TestLanguageToggle(t *testing.T) {
	a := newTestApp(t)
	m, _ := a.Update(press('l'))
	a = m.(*App)
	if a.lang != i18n.English {
		t.Fatalf("expected English, got %s", a.lang)
	}
	if !strings.Contains(a.View().Content, "Main menu") {
		t.Fatalf("english menu title missing")
	}
}

func TestRootMenuHasNoNav(t *testing.T) {
	a := newTestApp(t)
	if a.hasNavRow() {
		t.Fatalf("root menu should not show a navigation row")
	}
	// The root entries are the panel's map: every screen has to be reachable from
	// here, and from the navigation grouping as well, or it is hidden behind a
	// scroll nobody knows about.
	want := []string{"toolbox", "node", "domain", "subscribe", "users", "service", "system", "bbr", "script-update", "uninstall"}
	got := map[string]bool{}
	for _, n := range a.current().nodes {
		got[n.id] = true
	}
	for _, id := range want {
		if !got[id] {
			t.Errorf("root menu is missing %q", id)
		}
	}
	if len(a.current().nodes) != len(want) {
		t.Errorf("root has %d entries, expected %d", len(a.current().nodes), len(want))
	}
	if !got["uninstall"] {
		t.Fatalf("uninstall should be in the root menu")
	}
	view := a.View().Content
	for _, want := range []string{i18n.Chinese.T("menu_script_update"), i18n.Chinese.T("menu_uninstall")} {
		if !strings.Contains(view, want) {
			t.Fatalf("root menu missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "[0]") {
		t.Fatalf("root menu should not render a [0] row")
	}
}

// The 工具箱 section stands where 内核管理 used to: the core is compiled into the panel, so
// what an operator needs there is a measurement of the machine rather than a menu of cores.
// Its entries come from the registry, so the menu cannot drift from what the panel can run.
func TestToolboxMenuEntries(t *testing.T) {
	a := newTestApp(t)
	a.push(buildToolbox())
	groups := tools.Groups()
	if got, want := len(a.current().nodes), len(groups)+1; got != want {
		t.Fatalf("toolbox menu has %d entries, want %d groups and the board settings", got, len(groups))
	}
	for i, group := range groups {
		if got := a.current().nodes[i].id; got != "toolbox-"+group {
			t.Fatalf("toolbox entry %d = %s, want toolbox-%s", i, got, group)
		}
	}
	// The board is the page's own summary, so what belongs on it is the last entry of the
	// section rather than the property of any one tool.
	if got := a.current().nodes[len(groups)].id; got != "board-settings" {
		t.Fatalf("last toolbox entry = %s, want board-settings", got)
	}
	if !a.hasNavRow() {
		t.Fatalf("toolbox menu should show a navigation row")
	}
	view := a.View().Content
	for _, label := range []string{i18n.Chinese.T("toolbox_group_unlock"), i18n.Chinese.T("toolbox_group_hardware"), i18n.Chinese.T("nav_back")} {
		if !strings.Contains(view, label) {
			t.Fatalf("toolbox view missing %q: %s", label, view)
		}
	}
	m, _ := a.Update(press('0'))
	a = m.(*App)
	if !a.onNavRow() {
		t.Fatalf("digit 0 should select the navigation row in a submenu")
	}
	if a.current().id != "toolbox" {
		t.Fatalf("expected toolbox submenu, got %s", a.current().id)
	}
}

// Every registered tool gets one entry of its own, in registry order: the point of the
// toolbox is that each measurement is reachable on its own.
func TestToolboxGroupEntriesFollowTheRegistry(t *testing.T) {
	a := newTestApp(t)
	// The panel enters the section first; a stack assembled without it would be a stack the
	// panel never builds, and the page would be drawn as the main menu.
	if !a.enterSection("toolbox") {
		t.Fatal("the toolbox section should be reachable")
	}
	for _, group := range tools.Groups() {
		a.push(buildToolGroup(group))
		entries := tools.InGroup(group)
		if got := len(a.current().nodes); got != len(entries) {
			t.Fatalf("group %s has %d entries, want %d", group, got, len(entries))
		}
		view := a.View().Content
		for i, tool := range entries {
			if got := a.current().nodes[i].id; got != "tool-"+tool.ID {
				t.Fatalf("group %s entry %d = %s, want tool-%s", group, i, got, tool.ID)
			}
			label := i18n.Chinese.T("toolbox_" + tool.ID)
			if strings.HasPrefix(label, "toolbox_") {
				t.Fatalf("tool %s has no name in the table", tool.ID)
			}
			if !strings.Contains(view, label) {
				t.Fatalf("group %s view missing %q: %s", group, label, view)
			}
		}
		a.pop()
	}
}

// Every menu in the panel has to fit the box the layout gives it. The boxes are fixed at the
// main menu's size and no page scrolls, so a menu with more entries than rows would hide the
// last ones with no way to reach them — which is the failure this test exists to catch.
func TestEveryMenuFitsItsBox(t *testing.T) {
	a := newTestApp(t)
	l := a.bodyLayout()
	room := boxRows(l.menu)
	if room < 5 {
		t.Fatalf("the menu slot holds %d rows, too few for the panel's menus", room)
	}
	var walk func(m *menu, path string, depth int)
	walk = func(m *menu, path string, depth int) {
		rows := len(a.menuCells(m.nodes, 0, ui.InnerWidth(a.style(), a.frameWidth())))
		if depth > 1 {
			rows++ // the row that leads back out
		}
		if rows > room {
			t.Errorf("%s draws %d rows of entries, its box holds %d", path, rows, room)
		}
		for _, n := range m.nodes {
			if n.sub != nil {
				walk(n.sub, path+"/"+n.id, depth+1)
			}
		}
	}
	walk(a.stack[0], "main", 1)
}

// Every page draws its boxes in the same rows, at the same sizes. This is the test for the
// complaint that started the fixed layout: moving from the main menu into a section used to
// resize the boxes, and a page whose 看板 was taller than its room dropped the 看板 entirely.
func TestEveryPageKeepsTheSameBoxes(t *testing.T) {
	boxes := func(view string) [][2]int {
		var out [][2]int
		open := -1
		for i, line := range strings.Split(view, "\n") {
			switch {
			case open < 0 && strings.Contains(line, "╭"):
				open = i
			case open >= 0 && strings.Contains(line, "╰"):
				out = append(out, [2]int{open, i})
				open = -1
			}
		}
		return out
	}

	a := newTestApp(t)
	main := boxes(a.View().Content)
	if len(main) != 3 {
		t.Fatalf("the main page should draw the 看板, the menu and the hints, got %d boxes", len(main))
	}

	pages := map[string]string{"主菜单": a.View().Content}
	a.enterSection("toolbox")
	pages["工具箱"] = a.View().Content
	a.push(buildToolGroup(tools.GroupHardware))
	pages["硬件与性能"] = a.View().Content
	a.pop()
	a.pop()
	a.enterSection("users")
	pages["账号管理"] = a.View().Content
	a.pop()

	for name, view := range pages {
		got := boxes(view)
		if len(got) != len(main) {
			t.Errorf("%s draws %d boxes, the main page draws %d", name, len(got), len(main))
			continue
		}
		for i := range got {
			if got[i] != main[i] {
				t.Errorf("%s draws box %d at rows %v, the main page draws it at %v", name, i, got[i], main[i])
			}
		}
	}
}

// A running task and a finished report use the two slots as one box: same rows, same place, so
// the screen does not change shape when the work starts or ends either.
func TestTaskAndReportFillBothSlots(t *testing.T) {
	a := newTestApp(t)
	l := a.bodyLayout()
	want := [2]int{2, 2 + l.span() - 1}
	boxes := func(view string) [][2]int {
		var out [][2]int
		open := -1
		for i, line := range strings.Split(view, "\n") {
			switch {
			case open < 0 && strings.Contains(line, "╭"):
				open = i
			case open >= 0 && strings.Contains(line, "╰"):
				out = append(out, [2]int{open, i})
				open = -1
			}
		}
		return out
	}

	p := newProgress("三网回程", func(context.Context, *taskReporter) error { return nil })
	p.resize(a.width, a.height, l.span())
	a.task = p
	running := boxes(a.View().Content)
	if len(running) != 2 || running[0] != want {
		t.Fatalf("the running screen should fill both slots at %v, got %v", want, running)
	}

	outcome := previewToolOutcome()
	a.task = nil
	a.report = &outcome
	report := boxes(a.View().Content)
	if len(report) != 2 || report[0] != want {
		t.Fatalf("the report should fill both slots at %v, got %v", want, report)
	}
}

// A finished tool leaves a report on the screen and a line on the board: the run is what
// the operator asked for, so the panel shows its table rather than its log.
func TestToolboxReportAndBoard(t *testing.T) {
	a := newTestApp(t)
	outcome := toolOutcome{
		id: "unlock-ai",
		result: toolbox.Result{
			Headers: []string{"service", "status", "region"},
			Rows: [][]string{
				{"ChatGPT", "unlocked", "US"},
				{"Claude", "unknown", "US"},
			},
			Notes:   []string{"Claude: claude.ai answered a Cloudflare challenge"},
			Summary: "unlocked 1 · blocked 0 · unknown 1 (2)",
		},
		when: time.Now(),
	}
	a.toolResults = map[string]toolOutcome{outcome.id: outcome}
	a.report = &outcome

	view := a.View().Content
	for _, want := range []string{
		i18n.Chinese.T("toolbox_verdict_unlocked"),
		i18n.Chinese.T("toolbox_verdict_unknown"),
		i18n.Chinese.T("toolbox_col_region"),
		"US",
		i18n.Chinese.T("toolbox_rerun"),
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("report missing %q:\n%s", want, view)
		}
	}
	// A verdict the panel defined is worded in the interface language; the numbers and
	// names a tool reports are left as they came.
	if strings.Contains(view, "unlocked") {
		t.Errorf("the report leaked a raw verdict token:\n%s", view)
	}

	// Esc closes the report; the board then shows what the run found.
	m, _ := a.Update(press(tea.KeyEsc))
	a = m.(*App)
	if a.report != nil {
		t.Fatal("esc should close the report")
	}
	board := a.toolboxBody(88, 6)
	joined := strings.Join(board, "\n")
	if !strings.Contains(joined, i18n.Chinese.T("toolbox_unlock-ai")) || !strings.Contains(joined, "未知 1") {
		t.Fatalf("board does not report the last run:\n%s", joined)
	}
	// The board words the verdicts like the report does: a summary that stayed in tokens
	// would put `unknown 1` next to a table that says 未知.
	for _, token := range []string{"unlocked", "blocked", "unknown"} {
		if strings.Contains(joined, token) {
			t.Errorf("the board leaked the raw token %q:\n%s", token, joined)
		}
	}
	// The head counts the entries the board shows, so an operator can tell a board with one
	// result on it from one that is still waiting for its first run.
	if !strings.Contains(joined, i18n.Chinese.Format("toolbox_board_head", 1, len(tools.BoardDefault()))) {
		t.Fatalf("board does not count what it shows:\n%s", joined)
	}
	// An entry that is on the board and has not run says so, rather than leaving a gap.
	if !strings.Contains(joined, i18n.Chinese.T("toolbox_undetected")) {
		t.Fatalf("board does not mark what has not run:\n%s", joined)
	}
}

// Before anything runs the board shows the entries it is meant to show and marks them as not
// run: an operator reading it knows what will appear there, and that the host has not answered
// yet. Only a board with nothing selected falls back to an invitation.
func TestToolboxBoardBeforeAnyRun(t *testing.T) {
	a := newTestApp(t)
	board := strings.Join(a.toolboxBody(88, 6), "\n")
	if !strings.Contains(board, i18n.Chinese.Format("toolbox_board_head", 0, len(tools.BoardDefault()))) {
		t.Fatalf("board head should count the entries it shows:\n%s", board)
	}
	for _, id := range tools.BoardDefault() {
		if !strings.Contains(board, i18n.Chinese.T("toolbox_"+id)) {
			t.Fatalf("board is missing the default entry %s:\n%s", id, board)
		}
	}
	a.setBoardAll(false)
	board = strings.Join(a.toolboxBody(88, 6), "\n")
	if !strings.Contains(board, i18n.Chinese.T("toolbox_board_none")) {
		t.Fatalf("a board with nothing selected should say so:\n%s", board)
	}
}

// The board settings page is where the board's contents are chosen, and the choice is written
// down with the rest of the interface preferences.
func TestBoardSettingsChooseWhatTheBoardShows(t *testing.T) {
	a := newTestApp(t)
	a.push(buildToolbox())
	// Walking into the settings entry has to leave the toolbox menu behind.
	a.push(a.buildBoardSettings())
	ids := tools.InGroup(tools.GroupHardware)
	if on, total := a.boardGroupState(tools.GroupHardware); total != len(ids) {
		t.Fatalf("hardware group counts %d of %d entries", on, total)
	}
	a.push(a.buildBoardGroup(tools.GroupHardware))
	before := a.boardSelected(ids[0].ID)
	m, _ := a.Update(press(tea.KeyEnter))
	a = m.(*App)
	if a.boardSelected(ids[0].ID) == before {
		t.Fatalf("enter left %s at %v", ids[0].ID, before)
	}
	// The row shows the state it is in, so the page can be read without remembering it.
	want := "[x] "
	if before {
		want = "[ ] "
	}
	if !strings.Contains(a.View().Content, want+i18n.Chinese.T("toolbox_"+ids[0].ID)) {
		t.Fatalf("the row does not show %q:\n%s", want+ids[0].ID, a.View().Content)
	}
	// The choice survives a restart: it is read back from the preferences file.
	stored := boardFromPrefs(prefs.Load(a.prefsPath))
	if stored[ids[0].ID] == before {
		t.Fatalf("the choice was not written down: %v", stored)
	}
	// Turning it back is the same key.
	m, _ = a.Update(press(tea.KeyEnter))
	a = m.(*App)
	if a.boardSelected(ids[0].ID) != before {
		t.Fatalf("enter should have put %s back to %v", ids[0].ID, before)
	}
	// The two whole-board rows are choices too, and the board follows them.
	a.setBoardAll(true)
	if on, total := a.boardGroupState(tools.GroupHardware); on != total {
		t.Fatalf("select all left the hardware group at %d/%d", on, total)
	}
	a.setBoardAll(false)
	// Nothing is selected, so a default entry has to be off as well.
	if a.boardSelected(tools.BoardDefault()[0]) {
		t.Fatal("clear all should have turned every entry off")
	}
}

func TestNumberedMenuAndDigitSelection(t *testing.T) {
	a := newTestApp(t)
	if !a.View().AltScreen {
		t.Fatalf("dashboard should render fullscreen so the screen is cleared")
	}
	if !strings.Contains(a.View().Content, i18n.Chinese.T("svc_title")) {
		t.Fatalf("service entry should render its label")
	}
	m, _ := a.Update(press('5'))
	a = m.(*App)
	if got := a.selected().id; got != "users" {
		t.Fatalf("digit 5 should select accounts, got %s", got)
	}
	m, _ = a.Update(press('6'))
	a = m.(*App)
	if got := a.selected().id; got != "service" {
		t.Fatalf("digit 6 should select service, got %s", got)
	}
	m, _ = a.Update(press(tea.KeyEnter))
	a = m.(*App)
	m, _ = a.Update(press('0'))
	a = m.(*App)
	if !a.onNavRow() {
		t.Fatalf("digit 0 should select the navigation row")
	}
}

func TestFormSubmit(t *testing.T) {
	a := newTestApp(t)
	var got string
	a.openForm("UUID", "enter value", "abc", "", func(_ *App, v string) (tea.Cmd, error) {
		got = v
		return nil, nil
	})

	for _, r := range "def" {
		m, _ := a.Update(typeRune(r))
		a = m.(*App)
	}
	m, _ := a.Update(press(tea.KeyEnter))
	a = m.(*App)

	if got != "abcdef" {
		t.Fatalf("submitted %q, want %q", got, "abcdef")
	}
	if a.form != nil {
		t.Fatal("form should close after successful submit")
	}
}

func TestFormValidationKeepsOpen(t *testing.T) {
	a := newTestApp(t)
	a.openForm("title", "prompt", "", "", func(_ *App, _ string) (tea.Cmd, error) {
		return nil, errors.New("bad value")
	})

	m, _ := a.Update(press(tea.KeyEnter))
	a = m.(*App)
	if a.form == nil {
		t.Fatal("form closed despite validation error")
	}
	if a.form.err != "bad value" {
		t.Fatalf("form error = %q", a.form.err)
	}
	if !strings.Contains(a.View().Content, "bad value") {
		t.Fatal("view should render the form error")
	}

	m, _ = a.Update(press(tea.KeyEscape))
	a = m.(*App)
	if a.form != nil {
		t.Fatal("esc should close the form")
	}
}

func TestDashboardPanelsAndIcons(t *testing.T) {
	a := newTestApp(t)
	a.height = 52
	view := a.View().Content
	if !strings.Contains(view, i18n.Chinese.T("panel_hints")) {
		t.Fatalf("view missing the hint box:\n%s", view)
	}
	if !strings.Contains(view, i18n.Chinese.T("menu_main")) {
		t.Fatalf("view missing the main menu card:\n%s", view)
	}
	// The host card belongs to the system screen now: the main menu keeps its
	// width for the entries and their two columns.
	for _, gone := range []string{i18n.Chinese.T("panel_device"), i18n.Chinese.T("panel_accounts")} {
		if strings.Contains(view, gone) {
			t.Fatalf("main menu should not show %q:\n%s", gone, view)
		}
	}
	if strings.Contains(view, "[1]") {
		t.Fatalf("menu should use icons instead of bracketed numbers:\n%s", view)
	}

	a.openSystem()
	if got := a.View().Content; !strings.Contains(got, i18n.Chinese.T("panel_device")) {
		t.Fatalf("system screen should show the host card:\n%s", got)
	}
	a.closeSystem()

	// The node card lives inside node management instead of the main menu.
	a.push(buildNode())
	nodeView := a.View().Content
	if !strings.Contains(nodeView, i18n.Chinese.T("panel_node")) {
		t.Fatalf("node menu missing the node card:\n%s", nodeView)
	}
	if strings.Contains(nodeView, i18n.Chinese.T("panel_device")) {
		t.Fatalf("node menu should not show the device card:\n%s", nodeView)
	}
}

func TestDashboardShowsLogoAndMenuDescriptions(t *testing.T) {
	a := New("test", i18n.Chinese)
	a.width, a.height = 100, 48
	a.sized = true
	a.status = sysinfo.Collect("test")
	a.ready = true

	view := stripANSI(a.dashboard())
	if !strings.Contains(view, "██████") {
		t.Fatalf("tall dashboard should show the block-letter wordmark:\n%s", view)
	}
	// The wordmark and the vitals share one card, in that order, above the menu.
	lines := strings.Split(view, "\n")
	at := func(s string) int {
		for i, line := range lines {
			if strings.Contains(line, s) {
				return i
			}
		}
		return -1
	}
	mark, vitals, menu := at("██████"), at(i18n.Chinese.T("status_autostart")), at(i18n.Chinese.T("menu_main"))
	if mark < 0 || vitals < 0 || menu < 0 {
		t.Fatalf("wordmark %d, vitals %d, menu %d — one of them is missing:\n%s", mark, vitals, menu, view)
	}
	if !(mark < vitals && vitals < menu) {
		t.Fatalf("expected wordmark(%d) then vitals(%d) then menu(%d):\n%s", mark, vitals, menu, view)
	}
	// The vitals are inside the wordmark's card: a rule is the only thing
	// separating the two halves, and the card's own border is not one.
	rule := -1
	for i := mark; i < vitals; i++ {
		inner := strings.Trim(strings.TrimSpace(strings.Trim(lines[i], "│")), "─═")
		if inner == "" && strings.ContainsAny(lines[i], "─═") {
			rule = i
			break
		}
	}
	if rule < 0 {
		t.Fatalf("no rule between the wordmark and the vitals:\n%s", view)
	}
	// The entries live in their own box under the 看板: that is the frame every
	// page of the panel shares — 看板 on top, entries below — and it is what the
	// lines between the two halves have to prove.
	split := -1
	for i := vitals; i < menu; i++ {
		if strings.ContainsAny(lines[i], "╭╰") {
			split = i
			break
		}
	}
	if split < 0 {
		t.Fatalf("the menu is not in a box of its own after the 看板:\n%s", view)
	}
	// The hints are part of the fixed tail: the menu box closes, one description line, and
	// then the hint box — the same three rows on every page, which is what the layout
	// reserves for them. They are not pinned to the bottom of the terminal, so the gap the
	// user asked to close stays closed.
	hint := at(i18n.Chinese.T("panel_hints"))
	last := at("[ 10 ]")
	if hint < 0 || last < 0 {
		t.Fatalf("hints %d, last entry %d:\n%s", hint, last, view)
	}
	menuBottom := -1
	for i := last; i < len(lines); i++ {
		if strings.Contains(lines[i], "╰") {
			menuBottom = i
			break
		}
	}
	if menuBottom < 0 {
		t.Fatalf("the menu box has no bottom border:\n%s", view)
	}
	if hint-menuBottom != 2 {
		t.Fatalf("hints at line %d, menu box ends at %d — the tail is not the one row the layout reserves:\n%s",
			hint, menuBottom, view)
	}
	if hint+3 >= len(lines)-1 {
		t.Fatalf("hints at line %d of %d lines — they are pinned to the bottom:\n%s", hint, len(lines), view)
	}
}

// TestEverySectionHasItsOwnPanel guards the frame the panel is built around: every
// second-level page shows a 看板 of its own in the top box and its entries in the
// bottom one, so moving between pages swaps those two contents and nothing else.
func TestEverySectionHasItsOwnPanel(t *testing.T) {
	ids := []string{"toolbox", "node", "domain", "subscribe", "users", "service", "bbr", "script-update", "uninstall"}
	seen := make(map[string]string, len(ids))
	for _, id := range ids {
		a := newTestApp(t)
		a.width, a.height = 100, 40
		a.section = id
		title, rows := a.sectionPanel(a.width, 6)
		if strings.TrimSpace(title) == "" {
			t.Errorf("section %q has no 看板 title", id)
		}
		if len(rows) == 0 {
			t.Errorf("section %q has no 看板 rows", id)
		}
		if first, ok := seen[title]; ok {
			t.Errorf("sections %q and %q share the 看板 title %q", first, id, title)
		}
		seen[title] = id

		// The rendered page is two boxes: the 看板 and, under it, the entries.
		view := stripANSI(a.dashboard())
		boxes := strings.Count(view, "╭")
		if boxes < 2 {
			t.Errorf("section %q renders %d boxes, want the 看板 and the entries:\n%s", id, boxes, view)
		}
		if !strings.Contains(view, title) {
			t.Errorf("section %q does not render its 看板 title %q:\n%s", id, title, view)
		}
	}
}

func TestDashboardFitsNarrowWidths(t *testing.T) {
	// Long English labels used to spill past the right border on small
	// terminals because the two-column and menu rows never clamped the label.
	for _, lang := range []i18n.Lang{i18n.Chinese, i18n.English} {
		for _, w := range []int{24, 32, 40, 60, 80} {
			a := New("test", lang)
			a.width, a.height = w, 40
			a.sized = true
			a.status = sysinfo.Collect("test")
			a.ready = true
			for _, line := range strings.Split(a.dashboard(), "\n") {
				if got := lipgloss.Width(line); got > w {
					t.Fatalf("lang %s width %d: line is %d cells: %q", lang, w, got, line)
				}
			}
		}
	}
}

func TestFitsNarrowWidthsEveryScreen(t *testing.T) {
	// The main menu gained a second column and a section of its own, so every
	// screen is checked at the widths where columns start to collapse.
	for _, lang := range []i18n.Lang{i18n.Chinese, i18n.English} {
		for _, screen := range []string{"", "bbr", "bbr-qdisc", "system"} {
			for _, w := range []int{40, 60, 76, 100} {
				a := New("test", lang)
				a.sized = true
				a.status = sysinfo.Collect("test")
				a.ready = true
				for _, line := range strings.Split(a.SnapshotScreen(screen, w, 40), "\n") {
					if got := lipgloss.Width(line); got > w {
						t.Fatalf("lang %s screen %q width %d: line is %d cells: %q", lang, screen, w, got, line)
					}
				}
			}
		}
	}
}

func TestBBRMenuShape(t *testing.T) {
	a := newTestApp(t)
	if n := len(a.current().nodes); n != 10 {
		t.Fatalf("the main menu has %d entries, want 10", n)
	}
	// The tenth entry opens a section of its own.
	var bbrNode *node
	for _, n := range a.current().nodes {
		if n.id == "bbr" {
			bbrNode = n
		}
	}
	if bbrNode == nil || bbrNode.sub == nil {
		t.Fatal("the main menu has no BBR section")
	}
	// Two columns of five fill the card: the second column starts at the entries
	// after the fifth, and the last entry stays reachable on the same screen.
	view := a.SnapshotScreen("", 100, 40)
	for _, want := range []string{i18n.Chinese.T("toolbox_title"), i18n.Chinese.T("svc_title"), i18n.Chinese.T("menu_uninstall")} {
		if !strings.Contains(view, want) {
			t.Fatalf("main menu missing %q:\n%s", want, view)
		}
	}

	qdisc := a.SnapshotScreen("bbr-qdisc", 100, 40)
	for _, q := range []string{"fq", "fq_codel", "fq_pie", "cake"} {
		if !strings.Contains(qdisc, q) {
			t.Fatalf("queue discipline menu missing %q:\n%s", q, qdisc)
		}
	}
}

// The board is what makes a run worth repeating: a panel that forgot every result when it was
// closed would make an operator re-run a traceroute, a speed test and three benchmarks to see
// what yesterday's reading was.
func TestToolboxBoardSurvivesARestart(t *testing.T) {
	t.Setenv(prefs.PathEnv, filepath.Join(t.TempDir(), "easysb-ui.conf"))
	boardFile := filepath.Join(t.TempDir(), "easysb-toolbox.json")
	t.Setenv(toolbox.BoardEnv, boardFile)

	a := New("test", i18n.Chinese)
	outcome := previewToolOutcome()
	a.toolResults = map[string]toolOutcome{outcome.id: outcome}
	a.saveBoard()
	if _, err := os.Stat(boardFile); err != nil {
		t.Fatalf("the run should have been written down: %v", err)
	}

	restarted := New("test", i18n.Chinese)
	got, ok := restarted.toolResults["unlock-media"]
	if !ok {
		t.Fatalf("the board did not survive the restart: %v", restarted.toolResults)
	}
	if got.result.Summary != outcome.result.Summary || len(got.result.Rows) != len(outcome.result.Rows) {
		t.Errorf("the stored table came back different: %+v", got.result)
	}
	if !got.when.Equal(outcome.when) {
		t.Errorf("the stored time came back as %v, want %v", got.when, outcome.when)
	}

	// And the section draws it, so a restarted panel opens on what was measured.
	m, _ := restarted.Update(tea.WindowSizeMsg{Width: 100, Height: 34})
	restarted = m.(*App)
	if !restarted.enterSection("toolbox") {
		t.Fatal("the toolbox section should be reachable")
	}
	view := ansiSGR.ReplaceAllString(restarted.View().Content, "")
	// The board words the summary like the report does, so a stored English token must not
	// come back as one.
	if !strings.Contains(view, "解锁 6 · 不解锁 1 (7)") {
		t.Fatalf("the board should show the stored summary in words:\n%s", view)
	}

	// A file that cannot be parsed is an empty board, never a panel that will not start.
	if err := os.WriteFile(boardFile, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if broken := New("test", i18n.Chinese); len(broken.toolResults) != 0 {
		t.Errorf("a corrupt board should load as empty, got %d records", len(broken.toolResults))
	}
}

func TestMainMenuArrowKeysFollowTheColumns(t *testing.T) {
	a := newTestApp(t)
	if a.menuColumns() != 2 {
		t.Fatalf("the main menu should be drawn in two columns at %d columns wide", a.width)
	}
	half := len(a.current().nodes) / 2

	// Down walks the left column and stays in it: the entries below the fold are
	// the ones in the other column, so a step across would look like a jump.
	for i := 0; i < half-1; i++ {
		m, _ := a.Update(press(tea.KeyDown))
		a = m.(*App)
	}
	if a.index != half-1 {
		t.Fatalf("down from the top of the left column landed on %d, want %d", a.index, half-1)
	}
	// One more down wraps inside the column instead of crossing to the other one.
	m, _ := a.Update(press(tea.KeyDown))
	a = m.(*App)
	if a.index != 0 {
		t.Fatalf("down at the bottom of the left column landed on %d, want 0", a.index)
	}
	m, _ = a.Update(press(tea.KeyUp))
	a = m.(*App)
	if a.index != half-1 {
		t.Fatalf("up at the top of the left column landed on %d, want %d", a.index, half-1)
	}

	// Right moves to the same row of the right column, left comes back.
	m, _ = a.Update(press(tea.KeyRight))
	a = m.(*App)
	if want := 2*half - 1; a.index != want {
		t.Fatalf("right from the last row landed on %d, want %d", a.index, want)
	}
	m, _ = a.Update(press(tea.KeyLeft))
	a = m.(*App)
	if a.index != half-1 {
		t.Fatalf("left landed on %d, want %d", a.index, half-1)
	}

	// Enter still opens the highlighted entry from either column.
	m, _ = a.Update(press(tea.KeyEnter))
	a = m.(*App)
	if a.current().id == "root" {
		t.Fatal("enter should have left the main menu")
	}
}

func TestMainMenuNarrowFallsBackToSingleColumn(t *testing.T) {
	a := newTestApp(t)
	// Two columns need about 37 columns of card before each of them is too narrow
	// to hold a label.
	m, _ := a.Update(tea.WindowSizeMsg{Width: 36, Height: 34})
	a = m.(*App)
	if a.menuColumns() != 1 {
		t.Fatal("a 36-column terminal cannot hold two columns")
	}
	m, _ = a.Update(press(tea.KeyDown))
	a = m.(*App)
	if a.index != 1 {
		t.Fatalf("down moved to %d, want 1", a.index)
	}
	// With one column there is no column to step to, so right must not open the entry.
	m, _ = a.Update(press(tea.KeyRight))
	a = m.(*App)
	if a.current().id != "root" {
		t.Fatal("right must not enter a screen; only Enter does")
	}
}

// A page inside a section falls back to the panel's two columns once its entries no longer fit
// one per line (see entryRows), and then the arrows have to walk those columns the way they do
// on the main menu. The key handler used to disagree with the renderer here: menuColumns still
// reported one column, so right ran the highlighted entry — on the service page that is a
// right press starting, stopping or restarting the service — and left went back a level.
func TestSectionPageArrowKeysFollowItsColumns(t *testing.T) {
	a := newTestApp(t)
	if !a.enterSection("service") {
		t.Fatal("the service section should be reachable")
	}
	// The page has more entries than its box holds, which is what makes it two columns.
	if total := a.itemCount(); total <= boxRows(a.bodyLayout().menu) {
		t.Fatalf("the service page has %d entries and no longer falls back to columns", total)
	}
	if a.menuColumns() != 2 {
		t.Fatalf("the service page is drawn in two columns but menuColumns says %d", a.menuColumns())
	}
	half := a.colHalf()

	m, _ := a.Update(press(tea.KeyRight))
	a = m.(*App)
	if a.current().id != "service" {
		t.Fatalf("right left the page for %q instead of moving a column", a.current().id)
	}
	if a.index != half {
		t.Fatalf("right landed on %d, want the right column's first row %d", a.index, half)
	}
	m, _ = a.Update(press(tea.KeyLeft))
	a = m.(*App)
	if a.index != 0 {
		t.Fatalf("left landed on %d, want the left column's first row 0", a.index)
	}
	if a.current().id != "service" {
		t.Fatalf("left went back to %q instead of moving a column", a.current().id)
	}
	// Enter is still what runs the highlighted entry.
	m, _ = a.Update(press(tea.KeyEnter))
	a = m.(*App)
	if a.task == nil {
		t.Fatal("enter should have started the highlighted action")
	}
}

func TestBBRVersionListShowsPublishedKernels(t *testing.T) {
	a := newTestApp(t)
	a.push(buildBBR())
	a.section = "bbr"
	a.bbrVersionsLoading = true
	a.push(a.bbrVersionsMenu())

	// While the fetch is in flight the screen says so instead of looking empty.
	loading := a.Snapshot(100, 34)
	if !strings.Contains(loading, i18n.Chinese.T("bbr_versions_loading")) {
		t.Fatalf("the loading row is missing:\n%s", loading)
	}

	// A finished fetch lists every published kernel and marks the one this machine
	// already runs.
	a.applyBBRVersions(bbrVersionsMsg{
		list: []bbr.Release{
			{Tag: "x86_64-9.9.9", Version: "9.9.9", Profile: bbr.Standard},
			{Tag: "x86_64-9.9.9-max", Version: "9.9.9", Profile: bbr.Max},
			{Tag: "x86_64-9.9.8", Version: "9.9.8", Profile: bbr.Standard},
		},
		status: bbr.Status{
			Running: "9.9.8-minimaxflora-bbrv3",
			Kernels: []string{"linux-image-9.9.8-minimaxflora-bbrv3"},
		},
	})
	view := a.Snapshot(100, 34)
	for _, want := range []string{"9.9.9", "9.9.8", i18n.Chinese.T("bbr_versions_running"), i18n.Chinese.T("bbr_versions_latest")} {
		if !strings.Contains(view, want) {
			t.Fatalf("the version list is missing %q:\n%s", want, view)
		}
	}
	nodes := a.current().nodes
	if len(nodes) != 3 {
		t.Fatalf("the list has %d rows, want one per published kernel", len(nodes))
	}
	if nodes[0].action == nil {
		t.Fatal("a version row must install that version when entered")
	}

	// A failed fetch explains itself rather than rendering an empty list.
	a.applyBBRVersions(bbrVersionsMsg{err: errors.New("network unreachable")})
	view = a.Snapshot(100, 34)
	if !strings.Contains(view, i18n.Chinese.T("bbr_versions_failed")) {
		t.Fatalf("the failure row is missing:\n%s", view)
	}
}

func TestNoScreenCapturesTheMouse(t *testing.T) {
	a := newTestApp(t)
	if got := a.View().MouseMode; got != tea.MouseModeNone {
		t.Fatalf("dashboard should not capture the mouse, got %v", got)
	}
	p := newProgress("qr", func(context.Context, *taskReporter) error { return nil })
	p.resize(a.width, a.height, a.bodyLayout().span())
	a.task = p
	if got := a.View().MouseMode; got != tea.MouseModeNone {
		t.Fatalf("task screen should not capture the mouse, got %v", got)
	}
}

// Q quits the panel from every page, upper or lower case. There is one key for leaving, and
// it is not the key that walks back: Esc is the way back, on every page that has one.
func TestQQuitsFromEveryScreen(t *testing.T) {
	a := newTestApp(t)
	a.links = newLinksModel("t", sampleLinks())
	if _, cmd := a.Update(press('Q')); cmd == nil {
		t.Fatal("upper-case Q should quit from the link panel")
	}

	a.links = nil
	p := newProgress("t", func(context.Context, *taskReporter) error { return nil })
	a.task = p
	if _, cmd := a.Update(press('Q')); cmd == nil {
		t.Fatal("upper-case Q should quit from the task panel")
	}

	// Lower-case q quits too, on any page and at any depth.
	a.task = nil
	a.links = newLinksModel("t", sampleLinks())
	if _, cmd := a.Update(press('q')); cmd == nil {
		t.Fatal("lower-case q should quit from the link panel")
	}
	a.links = nil
	a.push(buildToolGroup(tools.GroupUnlock))
	a.push(buildToolGroup(tools.GroupUnlock))
	if _, cmd := a.Update(press('q')); cmd == nil {
		t.Fatal("lower-case q should quit from a third-level page")
	}

	// Esc is what closes a subpage, and it does not quit the panel.
	a.links = newLinksModel("t", sampleLinks())
	m, cmd := a.Update(press(tea.KeyEsc))
	if cmd != nil {
		t.Fatal("esc should not quit the panel")
	}
	if m.(*App).links != nil {
		t.Fatal("esc should close the link panel")
	}
}

// TestFormKeepsThePanelFrame pins the page that used to break the fixed layout. A prompt drew
// its own full-height panel: no status strip, and one card stretching from the first row of the
// window to the last, so opening the domain prompt moved the whole frame and the keys box. The
// form screen was also the one page --render could not draw, so nothing checked it. A prompt now
// uses the rows a task does: the strip, one card in the space of the page's two boxes, and the
// keys box in the tail -- the same rows the dashboard uses.
func TestFormKeepsThePanelFrame(t *testing.T) {
	for _, size := range [][2]int{{60, 20}, {80, 24}, {100, 33}, {100, 40}, {160, 50}} {
		w, h := size[0], size[1]
		a := New("test", i18n.Chinese)
		a.width, a.height = w, h
		a.sized = true
		a.status = sysinfo.Collect("test")
		a.ready = true

		l := a.bodyLayout()
		root := strings.Split(stripANSI(a.dashboard()), "\n")
		a.openForm(a.lang.T("domain_issue"), a.lang.T("domain_prompt"), "example.com", "", nil)
		lines := strings.Split(stripANSI(a.View().Content), "\n")

		if len(lines) != h {
			t.Fatalf("%dx%d: the form drew %d lines", w, h, len(lines))
		}
		// The status strip and the blank row under it belong to the frame, so they are byte
		// for byte what the page the prompt was opened from has.
		if lines[0] != root[0] || lines[1] != root[1] {
			t.Fatalf("%dx%d: the form does not keep the frame's top:\nform: %q\nroot: %q", w, h, lines[0], root[0])
		}
		// The card takes the rows the page's two boxes would, and the keys box closes the page
		// in the tail, which is the row every other page puts it on.
		for _, row := range []int{2, 2 + l.span() - 1, 2 + l.span() + l.tail - 1} {
			form, want := []rune(lines[row]), []rune(root[row])
			if form[0] != want[0] {
				t.Fatalf("%dx%d: the form's row %d starts with %q, the page's with %q", w, h, row, form[0], want[0])
			}
		}
	}
}

func TestEveryScreenUsesOneFixedFrame(t *testing.T) { // The whole point of the layout: the dashboard and every subpage render at
	// exactly the same size, so moving between them never resizes the panel and
	// the hint box never moves.
	lines := func(s string) int { return strings.Count(s, "\n") + 1 }
	screens := func(a *App) map[string]string {
		out := map[string]string{"dashboard": a.dashboard()}
		a.links = newLinksModel("t", sampleLinks())
		out["links"] = a.View().Content
		a.links = nil
		p := newProgress("t", func(context.Context, *taskReporter) error { return nil })
		a.task = p
		out["task"] = a.View().Content
		// A download in flight shares the card with the log, so the bar has to fit
		// every size the frame is promised at.
		p.setDownload("linux-image-7.2.6-minimaxflora-bbrv3-max_7.2.6-1_amd64.deb", 118<<20, 240<<20)
		out["task-downloading"] = a.View().Content
		a.task = nil
		a.openForm("t", "p", "", "", nil)
		out["form"] = a.View().Content
		a.form = nil
		a.openSystem()
		out["system"] = a.View().Content
		a.system = nil

		// Every page under the main menu, and the subpage each one hangs further
		// down: the two boxes have to hold whatever a section puts in them.
		for _, n := range buildRoot().nodes {
			if n.sub == nil {
				continue
			}
			a.stack = a.stack[:1]
			a.section = n.id
			a.push(n.sub)
			out["page:"+n.id] = a.View().Content
			for _, sub := range n.sub.nodes {
				if sub.sub == nil {
					continue
				}
				a.push(sub.sub)
				out["page:"+n.id+"/"+sub.id] = a.View().Content
				break
			}
		}
		a.stack = a.stack[:1]
		a.section = ""

		// The account screens carry the longest values in the panel: names,
		// quotas and subscription URLs.
		account := user.New("a-very-long-account-name-for-layout", state.Keys, time.Unix(0, 0))
		account.QuotaBytes = 1 << 40
		account.UsedBytes = 1 << 30
		account.ExpireAt = time.Unix(0, 0).Add(48 * time.Hour)
		a.accounts = []user.User{account}
		a.push(a.usersMenu())
		out["accounts"] = a.View().Content
		a.push(a.userListMenu())
		out["account-list"] = a.View().Content
		a.push(a.userMenu(account.Token))
		out["account-detail"] = a.View().Content
		a.push(a.userProtocolsMenu(account.Token))
		out["account-protocols"] = a.View().Content
		return out
	}
	// Every skin has to hold the same frame: a skin that pads, tints or frames
	// differently must not change the size of a single screen.
	skins := theme.Skins()
	status := sysinfo.Collect("test")

	for _, skin := range skins {
		for _, dark := range []bool{true, false} {
			for _, w := range []int{20, 32, 60, 100, 140} {
				for _, h := range []int{6, 8, 10, 12, 14, 20, 34, 60} {
					a := New("test", i18n.Chinese)
					a.setSkin(skin, dark)
					a.width, a.height = w, h
					a.sized = true
					a.status = status
					a.ready = true

					limit := panelWidth(w)
					for name, content := range screens(a) {
						if got := lines(content); got != h {
							t.Fatalf("%s dark=%v %dx%d %s: drew %d lines", skin.ID, dark, w, h, name, got)
						}
						for _, line := range strings.Split(content, "\n") {
							if got := lipgloss.Width(line); got > limit {
								t.Fatalf("%s dark=%v %dx%d %s: line is %d cells (limit %d): %q", skin.ID, dark, w, h, name, got, limit, line)
							}
						}
					}
				}
			}
		}
	}
}

func TestLogEndpointNeedsHost(t *testing.T) {
	var logged []string
	logEndpoint(state.Config{}, func(s string) { logged = append(logged, s) }, i18n.Chinese)
	if len(logged) == 0 || !strings.Contains(logged[0], i18n.Chinese.T("sub_need_domain")) {
		t.Fatalf("expected a need-domain hint, got %v", logged)
	}
}

// TestBBRPanelShowsPendingReboot keeps the one thing an operator has to do after
// installing a kernel on the page: the new kernel only runs after a reboot, and a
// panel that reports it installed without saying so invites a bug report.
func TestBBRPanelShowsPendingReboot(t *testing.T) {
	newApp := func() *App {
		a := New("test", i18n.Chinese)
		a.width, a.height = 100, 34
		a.sized = true
		a.status = sysinfo.Collect("test")
		a.ready = true
		a.section = "bbr"
		a.push(buildBBR())
		return a
	}

	pending := newApp()
	pending.bbrStatus = bbr.Status{
		Running:    "6.12.48+deb13-amd64",
		Congestion: "cubic",
		Kernels:    []string{"linux-image-7.2.7-minimaxflora-bbrv3"},
	}
	if view := pending.View().Content; !strings.Contains(view, i18n.Chinese.T("bbr_reboot_pending")) {
		t.Fatalf("a kernel waiting for a reboot should say so:\n%s", view)
	}

	running := newApp()
	running.bbrStatus = bbr.Status{
		Running:    "7.2.7-minimaxflora-bbrv3",
		Congestion: "bbr",
		Kernels:    []string{"linux-image-7.2.7-minimaxflora-bbrv3"},
	}
	if view := running.View().Content; strings.Contains(view, i18n.Chinese.T("bbr_reboot_pending")) {
		t.Fatalf("a kernel that is already running should not ask for a reboot:\n%s", view)
	}
}

// TestSectionPanelRefreshesAfterTask keeps the 看板 of a section that reads the
// machine in step with the task that just changed it.
func TestSectionPanelRefreshesAfterTask(t *testing.T) {
	a := New("test", i18n.Chinese)
	a.section = "bbr"
	if a.sectionRefresh() == nil {
		t.Fatal("a task that finished in the BBR section should re-read its panel")
	}
	a.section = "domain"
	if a.sectionRefresh() != nil {
		t.Fatal("a section whose panel only reads the status strip needs no re-read")
	}
}

// TestRootEntriesOpenPages covers the frame the operator is left in after a root
// entry: every entry of the main menu opens a page of its own, so the top box is that
// page's 看板 and Esc walks back to the main menu. An entry that used to run its
// action in place showed this section's 看板 above the main menu and made Esc quit.
func TestRootEntriesOpenPages(t *testing.T) {
	// The index of a root entry, looked up from the menu itself so the test does not
	// depend on anything besides the entries the panel builds.
	rootIndex := func(id string) int {
		for i, n := range buildRoot().nodes {
			if n.id == id {
				return i
			}
		}
		return -1
	}
	for _, id := range []string{"script-update", "uninstall"} {
		a := New("test", i18n.Chinese)
		a.width, a.height = 100, 33
		a.sized = true
		a.status = sysinfo.Collect("test")
		a.ready = true
		a.index = rootIndex(id)

		m, _ := a.Update(press(tea.KeyEnter))
		a = m.(*App)
		if len(a.stack) != 2 {
			t.Fatalf("%s: Enter should open a page, stack is %d deep", id, len(a.stack))
		}
		if a.sectionID() != id {
			t.Fatalf("%s: the page should carry its own section, got %q", id, a.sectionID())
		}
		// The page is the same two boxes as every other: its 看板 on top, its own
		// entries below, titled with the page.
		view := a.View().Content
		title, rows := a.sectionPanel(a.frameWidth(), 6)
		if title == "" || len(rows) == 0 {
			t.Fatalf("%s: the page has no 看板", id)
		}
		if !strings.Contains(stripANSI(view), title) {
			t.Fatalf("%s: the page should draw the %q 看板:\n%s", id, title, view)
		}
		if got := strings.Count(stripANSI(view), i18n.Chinese.T("menu_main")); got != 0 {
			t.Fatalf("%s: the main menu should not be on screen inside a page", id)
		}
		if !strings.Contains(stripANSI(view), i18n.Chinese.T("nav_back")) {
			t.Fatalf("%s: the page should offer the way back:\n%s", id, view)
		}

		// Esc leaves the page instead of quitting the panel.
		m, cmd := a.Update(press(tea.KeyEscape))
		a = m.(*App)
		if cmd != nil {
			t.Fatalf("%s: Esc on a page should step back, not quit", id)
		}
		if len(a.stack) != 1 || a.sectionID() != "" {
			t.Fatalf("%s: Esc should return to the main menu, stack=%d section=%q", id, len(a.stack), a.sectionID())
		}
	}
}

// TestRootRunInPlaceLeavesNoSection pins the invariant behind that frame: an entry
// that runs an action without opening a page must not claim a section.
func TestRootRunInPlaceLeavesNoSection(t *testing.T) {
	a := New("test", i18n.Chinese)
	a.width, a.height = 100, 33
	a.sized = true
	a.status = sysinfo.Collect("test")
	a.ready = true
	for i, n := range buildRoot().nodes {
		if n.sub != nil || n.action == nil {
			continue
		}
		a.section = ""
		a.stack = a.stack[:1]
		a.index = i
		if n.id == "system" {
			continue // the system screen is a page of its own and says so
		}
		m, _ := a.Update(press(tea.KeyEnter))
		a = m.(*App)
		if a.section != "" {
			t.Fatalf("%s runs in place and must not set a section, got %q", n.id, a.section)
		}
	}
}

func TestLogEndpointWarnsWithoutCertificate(t *testing.T) {
	cfg := state.Default()
	// A server IP is enough for a host, but it is not a domain with a
	// certificate, so the endpoint will speak plain HTTP.
	cfg.ServerIP = "203.0.113.10"
	var logged []string
	logEndpoint(cfg, func(s string) { logged = append(logged, s) }, i18n.Chinese)
	joined := strings.Join(logged, "\n")
	if !strings.Contains(joined, i18n.Chinese.T("sub_plaintext_warning")) {
		t.Fatalf("expected a plaintext warning, got %v", logged)
	}
	if !strings.Contains(joined, "http://203.0.113.10:8443/sub/") {
		t.Fatalf("endpoint URL should match the plain listener: %v", logged)
	}
}

func TestAccountScreensRenderAccount(t *testing.T) {
	a := New("test", i18n.Chinese)
	a.width, a.height = 100, 40
	a.sized = true
	a.status = sysinfo.Collect("test")
	a.ready = true

	account := user.New("alice", state.Keys, time.Now())
	account.QuotaBytes = 1 << 40
	account.UsedBytes = 1 << 30
	a.accounts = []user.User{account}

	a.push(a.usersMenu())
	a.push(a.userListMenu())
	view := a.View().Content
	if !strings.Contains(view, "alice") {
		t.Fatalf("account list should show the account name:\n%s", view)
	}
	if !strings.Contains(view, i18n.Chinese.T("user_status_active")) {
		t.Fatalf("account list should show the status:\n%s", view)
	}

	a.push(a.userMenu(account.Token))
	view = a.View().Content
	for _, want := range []string{
		"alice",
		i18n.Chinese.T("user_quota"),
		i18n.Chinese.T("user_protocols"),
		i18n.Chinese.T("user_sub"),
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("account detail missing %q:\n%s", want, view)
		}
	}
}

func TestValidHopRange(t *testing.T) {
	cases := map[string]bool{
		"2080:3000": true,
		"1:2":       true,
		"3000:2080": false,
		"0:100":     false,
		"100:70000": false,
		"abc":       false,
		"":          false,
	}
	for in, want := range cases {
		if got := validHopRange(in); got != want {
			t.Errorf("validHopRange(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestMenuCursorKeepsUniformWidth(t *testing.T) {
	// The selection bar spans the full inner width so it stays one size as the
	// cursor moves through rows with different length descriptions.
	a := New("test", i18n.Chinese)
	a.width, a.height = 100, 34
	a.sized = true
	a.status = sysinfo.Collect("test")
	a.ready = true

	inner := a.width - 4
	// A row inside a section lists one entry per line and its bar spans the row, so the
	// cursor keeps one size while it moves through entries of different lengths.
	a.push(buildSubscribe())
	labelCol := a.menuLabelColumn()
	for i, n := range a.current().nodes {
		bar := a.menuRow(true, i, n, inner, labelCol)
		if got := lipgloss.Width(bar); got != inner {
			t.Fatalf("row %d bar width = %d, want %d", i, got, inner)
		}
	}
}

// A page inside a section shows what its entries do: the label and the description sit in the
// same cell, and the row the cursor is on repeats its description in full under the box, which
// is where a description too wide for a column can be read to the end.
func TestSubmenuShowsDescriptions(t *testing.T) {
	a := New("test", i18n.Chinese)
	a.width, a.height = 100, 46
	a.sized = true
	a.status = sysinfo.Collect("test")
	a.ready = true

	a.push(buildSubscribe())
	view := a.View().Content
	for _, key := range []string{"desc_sub_url", "desc_sub_qr", "desc_sub_links", "desc_sub_svc_install"} {
		desc := i18n.Chinese.T(key)
		if !strings.Contains(view, descPrefix(desc)) {
			t.Fatalf("subscription submenu missing description %q (looked for %q):\n%s", key, descPrefix(desc), view)
		}
	}
	// The hovered entry's description is repeated in full on the line under the box.
	if first := i18n.Chinese.T("desc_sub_url"); !strings.Contains(view, first) {
		t.Fatalf("the hovered entry's description is not shown in full:\n%s", view)
	}
}

// descPrefix is the part of a description a menu column is guaranteed to have room for: a
// column is half the frame, and a Chinese description is two columns per character.
func descPrefix(desc string) string {
	runes := []rune(desc)
	if len(runes) > 6 {
		runes = runes[:6]
	}
	return string(runes)
}

func TestPaletteFollowsTerminalBackground(t *testing.T) {
	a := New("test", i18n.Chinese)
	if !a.themeAuto {
		t.Fatal("the built-in theme should auto-detect the terminal background")
	}
	m, _ := a.Update(tea.BackgroundColorMsg{Color: color.White})
	a = m.(*App)
	if a.palette.Primary != theme.Light().Primary {
		t.Fatal("a light terminal background should select the light palette")
	}
	m, _ = a.Update(tea.BackgroundColorMsg{Color: color.Black})
	a = m.(*App)
	if a.palette.Primary != theme.Dark().Primary {
		t.Fatal("a dark terminal background should select the dark palette")
	}
}

func TestThemeEnvOverrideWins(t *testing.T) {
	t.Setenv("EASYSB_THEME", "light")
	a := New("test", i18n.Chinese)
	if a.themeAuto {
		t.Fatal("a forced theme should disable background auto-detection")
	}
	if a.palette.Primary != theme.Light().Primary {
		t.Fatal("EASYSB_THEME=light should start on the light palette")
	}
	m, _ := a.Update(tea.BackgroundColorMsg{Color: color.Black})
	a = m.(*App)
	if a.palette.Primary != theme.Light().Primary {
		t.Fatal("a forced theme must ignore the detected background")
	}
}

// The core the panel carries has to say whether this build counts per-account traffic:
// that decides whether the traffic columns exist at all. The answer is a build tag, so it
// comes from the binary rather than from anything stored on the host.
func TestCoreStatsLabel(t *testing.T) {
	cases := []struct {
		name  string
		stats bool
		want  string
	}{
		{"counters compiled in", true, "带流量统计"},
		{"no counters in this build", false, "无流量统计"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestApp(t)
			a.status.StatsCapable = tc.stats
			if got := a.coreStatsLabel(); got != tc.want {
				t.Fatalf("coreStatsLabel = %q, want %q", got, tc.want)
			}
			// The wordmark card and the version line name it too, so the answer is
			// visible without opening a section.
			summary, _ := a.coreSummary()
			if !strings.Contains(summary, tc.want) {
				t.Fatalf("coreSummary = %q, want it to name %q", summary, tc.want)
			}
		})
	}
}

// A report taller than its box is scrolled with the arrow keys: the table is read in full
// rather than cut off with a count of what did not fit, which is what a detection page needs —
// the last rows of a system or disk report are the ones an operator came for.
func TestReportScrollsTallTables(t *testing.T) {
	a := newTestApp(t)
	rows := make([][]string, 0, 30)
	for i := 0; i < 30; i++ {
		rows = append(rows, []string{fmt.Sprintf("项目 %d", i+1), fmt.Sprintf("值 %d", i+1)})
	}
	outcome := toolOutcome{
		id:     "hw-info",
		when:   time.Now(),
		result: toolbox.Result{Headers: []string{"项目", "值"}, Rows: rows, Summary: "短结论"},
	}
	a.toolResults = map[string]toolOutcome{"hw-info": outcome}
	a.report = &outcome

	first := ansiSGR.ReplaceAllString(a.View().Content, "")
	if a.scrollMax == 0 {
		t.Fatalf("a report of %d rows should be scrollable:\n%s", len(rows), first)
	}
	if !strings.Contains(first, i18n.Chinese.T("task_scroll")) {
		t.Fatalf("a scrollable report should say so in its hints:\n%s", first)
	}
	if !strings.Contains(first, i18n.Chinese.Format("box_scroll_at", 1, a.scrollMax+boxRows(a.bodyLayout().span()))) {
		t.Fatalf("the report should say which line it starts at:\n%s", first)
	}
	m, _ := a.Update(press(tea.KeyDown))
	a = m.(*App)
	if a.scroll != 1 {
		t.Fatalf("down moved the report to %d, want 1", a.scroll)
	}
	second := ansiSGR.ReplaceAllString(a.View().Content, "")
	if second == first {
		t.Fatalf("down did not move the visible rows:\n%s", second)
	}
	// The end key lands on the last window, and Esc leaves the screen from the top.
	m, _ = a.Update(press(tea.KeyEnd))
	a = m.(*App)
	if a.scroll != a.scrollMax {
		t.Fatalf("end left the report at %d, want %d", a.scroll, a.scrollMax)
	}
	m, _ = a.Update(press(tea.KeyEsc))
	a = m.(*App)
	if a.report != nil || a.scroll != 0 || a.scrollMax != 0 {
		t.Fatalf("esc should close the report and drop its scroll: %v %d %d", a.report != nil, a.scroll, a.scrollMax)
	}
}
