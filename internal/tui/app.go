// Package tui is the panel: the bubbletea model, the menu tree, the fixed layout every
// page is drawn into, the forms and the progress and report screens.
package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/EasySBTeam/EasySB/internal/bbr"
	"github.com/EasySBTeam/EasySB/internal/i18n"
	"github.com/EasySBTeam/EasySB/internal/icons"
	sbnode "github.com/EasySBTeam/EasySB/internal/node"
	"github.com/EasySBTeam/EasySB/internal/prefs"
	"github.com/EasySBTeam/EasySB/internal/subscribe"
	"github.com/EasySBTeam/EasySB/internal/sysinfo"
	"github.com/EasySBTeam/EasySB/internal/theme"
	"github.com/EasySBTeam/EasySB/internal/toolbox"
	"github.com/EasySBTeam/EasySB/internal/toolbox/tools"
	"github.com/EasySBTeam/EasySB/internal/ui"
	"github.com/EasySBTeam/EasySB/internal/update"
	"github.com/EasySBTeam/EasySB/internal/user"
)

type statusMsg sysinfo.Status

// App is the bubbletea model. It holds the navigation stack rather than a single current
// page, so Esc always means "one level back", and it owns the pieces every page needs:
// the language, the icons, the resolved skin and the status bar's reading of the host.
type App struct {
	scriptVersion string
	lang          i18n.Lang
	iconSet       icons.Set
	skin          theme.Skin
	dark          bool
	palette       theme.Palette
	themeAuto     bool
	stack         []*menu
	index         int
	width         int
	height        int
	status        sysinfo.Status
	ready         bool
	sized         bool
	// board is the toolbox 看板's selection: the tool ids whose results the board shows. nil
	// means the panel has never been asked, so tools.BoardDefault() decides (see board.go).
	board map[string]bool

	// scroll is the first row a scrollable screen shows, and scrollMax is how far it can go.
	// The screen measures scrollMax while it draws, because only the drawing knows how many
	// rows the table came out as; the keys then move within that.
	scroll    int
	scrollMax int

	// toolResults is the last outcome of every toolbox entry that has run, keyed by tool
	// id. A tool leaves the process (a probe run, a traceroute, a benchmark), so the panel
	// keeps what the run it started found instead of running it again on every visit.
	toolResults map[string]toolOutcome
	// report is the outcome the report screen is showing, nil when no report is open. A
	// finished tool sets it, so a run ends on its table rather than on its log.
	report   *toolOutcome
	quote    string
	toast    string
	toastErr bool
	// section is the root entry the panel is standing in, empty on the main
	// menu. It is set when a root entry is entered and cleared on the way back.
	section string
	task    *progressModel
	form    *formModel
	links   *linksModel
	// system is the system information screen. It replaces the body of the frame
	// while it is open, so the status strip and the key hints stay in place.
	system *systemModel
	// accounts is the snapshot the account menus render from; it is refreshed
	// when the section is entered and after every task.
	accounts []user.User
	// nodes is the snapshot the node menus render from, refreshed the same way.
	nodes []sbnode.Node
	// bbrVersions is the published kernel list the BBR section renders from, with
	// bbrStatus as the local half of the reading: fetched when the list is opened.
	bbrVersions        []bbr.Release
	bbrVersionsErr     error
	bbrVersionsLoading bool
	bbrStatus          bbr.Status
	// prefsPath is where the interface choices are remembered. It is a field so
	// the tests can point it at a temporary file instead of /etc/sing-box.
	prefsPath string
	// boardPath is where the toolbox 看板 is written down. It is a field for the same
	// reason, and an empty value keeps the board in memory only.
	boardPath string
}

// New builds the application. Interface choices the operator made earlier are
// already in the environment by the time this runs: main applies the preferences
// file only where no flag or exported variable spoke.
func New(scriptVersion string, lang i18n.Lang) *App {
	skin, dark, auto := skinFromEnv()
	a := &App{
		scriptVersion: scriptVersion,
		lang:          lang,
		iconSet:       icons.Detect(),
		themeAuto:     auto,
		stack:         []*menu{buildRoot()},
		quote:         lang.Hitokoto(),
		prefsPath:     prefs.Path(),
		board:         boardFromPrefs(prefs.Load(prefs.Path())),
		boardPath:     boardPath(),
	}
	a.setSkin(skin, dark)
	a.loadBoard()
	return a
}

// boardPath is where the toolbox 看板 is written down: the registry owns the path, because
// `--tool` writes to the same file.
func boardPath() string {
	return tools.BoardPath()
}

// loadBoard reads the stored outcomes so the section's 看板 shows the last run of every tool
// the panel has already measured, instead of forgetting them when the panel was closed.
func (a *App) loadBoard() {
	if a.boardPath == "" {
		return
	}
	stored := toolbox.LoadBoard(a.boardPath)
	if len(stored) == 0 {
		return
	}
	if a.toolResults == nil {
		a.toolResults = make(map[string]toolOutcome, len(stored))
	}
	for id, record := range stored {
		outcome := toolOutcome{id: record.ID, when: record.When, result: record.Result}
		if record.Error != "" {
			outcome.err = errors.New(record.Error)
		}
		a.toolResults[id] = outcome
	}
}

// saveBoard writes the outcomes down. A board that cannot be written is a degraded
// convenience, not a failed run: the table is on screen either way, so the error is dropped
// rather than turned into an interruption.
func (a *App) saveBoard() {
	if a.boardPath == "" {
		return
	}
	// The board is merged into the stored one under its lock rather than written
	// whole, so a run `--tool` recorded while the panel was open is kept.
	_ = toolbox.UpdateBoard(a.boardPath, func(board toolbox.Board) {
		for id, outcome := range a.toolResults {
			record := toolbox.Record{ID: id, When: outcome.when, Result: outcome.result}
			if outcome.err != nil {
				record.Error = outcome.err.Error()
			}
			board[id] = record
		}
	})
}

// remember stores the interface choices so the next run starts where this one
// left off. It is called from the actions that represent a decision, never from
// the automatic palette detection, which would look like a decision next time.
func (a *App) remember() {
	if a.prefsPath == "" {
		return
	}
	theme := ""
	if !a.themeAuto {
		if a.dark {
			theme = "dark"
		} else {
			theme = "light"
		}
	}
	_ = prefs.Prefs{
		Skin:  a.skin.ID,
		Theme: theme,
		Icons: a.iconSet.ID,
		Lang:  string(a.lang),
		Board: a.boardStored(),
	}.Save(a.prefsPath)
}

// setSkin resolves the skin for a terminal background and keeps the flat palette
// the older screens use in step with it.
func (a *App) setSkin(skin theme.Skin, dark bool) {
	a.skin = skin
	a.dark = dark
	a.palette = skin.Style(dark).Palette
}

// lockLook records a manual choice: the palette stops following the terminal
// background once the operator has picked one.
func (a *App) lockLook() { a.themeAuto = false }

// setDark switches the palette to a dark or light background.
func (a *App) setDark(dark bool) {
	a.lockLook()
	a.setSkin(a.skin, dark)
}

// setIcons swaps the marker palette.
func (a *App) setIcons(set icons.Set) { a.iconSet = set }

// openSystem and closeSystem enter and leave the system information screen.
func (a *App) openSystem() {
	a.system = newSystemModel(a)
	a.section = "system"
}

func (a *App) closeSystem() {
	a.system = nil
	a.section = ""
}

// style is the current skin resolved for the current background.
func (a *App) style() theme.Style { return a.skin.Style(a.dark) }

// skinFromEnv picks the starting look. EASYSB_SKIN, set by --skin, chooses the
// skin; EASYSB_THEME, set by --theme, forces dark or light. Without either the
// palette follows the terminal background detected on startup.
func skinFromEnv() (theme.Skin, bool, bool) {
	skin, ok := theme.SkinByID(strings.TrimSpace(os.Getenv("EASYSB_SKIN")))
	if !ok {
		skin = theme.DefaultSkin()
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("EASYSB_THEME"))) {
	case "light":
		return skin, false, false
	case "dark":
		return skin, true, false
	default:
		return skin, true, true
	}
}

// Init is the one command bubbletea runs at startup: read the host for the status bar,
// and ask the terminal for its background when the palette is set to follow it.
func (a *App) Init() tea.Cmd {
	cmds := []tea.Cmd{collectStatus(a.scriptVersion)}
	if a.themeAuto {
		cmds = append(cmds, requestBackground())
	}
	return tea.Batch(cmds...)
}

// requestBackground asks the terminal for its background color; the reply
// drives the light/dark palette choice.
func requestBackground() tea.Cmd {
	return func() tea.Msg { return tea.RequestBackgroundColor() }
}

// Snapshot renders the current screen headlessly, which is what --render prints.
func (a *App) Snapshot(width, height int) string {
	a.width, a.height = width, height
	a.sized = true
	a.status = sysinfo.Collect(a.scriptVersion)
	a.ready = true
	if a.form != nil {
		// A form owns the screen while it is open, exactly as in View.
		return a.formScreen()
	}
	if a.task != nil {
		// A task owns the screen while it runs, so a rendered frame is the task's.
		return a.task.View(a.width, a.height, a.statusStrip(a.frameWidth()), a.style(), a.lang, a.iconSet, a.bodyLayout())
	}
	if a.report != nil {
		// A report owns the screen once a tool has finished, so a rendered frame
		// is the report's, exactly as in the running panel.
		return a.reportScreen()
	}
	return a.dashboard()
}

// SnapshotScreen is Snapshot for one named screen, so a layout can be inspected
// without walking the menus. Any root entry that has a submenu can be named, on top of
// the screens that open their own model. Unknown names fall back to the dashboard.
func (a *App) SnapshotScreen(screen string, width, height int) string {
	if screen != "system" && a.enterSection(screen) {
		// The BBR 看板 reads the local state when the section is entered. A
		// rendered screen has no event loop to deliver that reading, so it takes
		// one here; otherwise it would only ever draw the placeholder.
		if screen == "bbr" {
			a.bbrStatus = bbrStatusNow()
		}
		return a.Snapshot(width, height)
	}
	switch screen {
	case "node":
		a.loadNodes()
		a.push(a.nodeMenu())
		a.section = "node"
	case "node-list":
		a.loadNodes()
		a.push(a.nodeMenu())
		a.push(a.nodeListMenu())
		a.section = "node"
	case "users":
		a.loadAccounts()
		a.loadNodes()
		a.push(a.usersMenu())
		a.section = "users"
	case "user-list":
		a.loadAccounts()
		a.loadNodes()
		a.push(a.usersMenu())
		a.push(a.userListMenu())
		a.section = "users"
	case "params":
		a.push(a.nodeMenu())
		a.push(buildParams())
		a.section = "node"
	case "system":
		a.openSystem()
	case "form":
		// The form screen is where every prompt lands, and its frame is the one page
		// that used to be missing from the renderer: a rendered frame shows the domain
		// prompt, which is the form the operator meets first.
		a.openForm(a.lang.T("domain_issue"), a.lang.T("domain_prompt"), "example.com", "", nil)
	case "bbr-qdisc":
		a.push(buildBBR())
		a.section = "bbr"
		a.push(buildQdisc())
	case "bbr-versions":
		a.push(buildBBR())
		a.section = "bbr"
		a.bbrStatus = bbrStatusNow()
		// The list is fetched from the network when it is opened. A rendered
		// screen shows the layout, so it takes a sample list instead of an empty
		// one, which would only ever draw the loading placeholder.
		a.bbrVersions = previewReleases()
		a.push(a.bbrVersionsMenu())
	case "task":
		// The task screen is where every action lands, and its download bar only
		// exists while a download is in flight, so a rendered frame takes a sample
		// reading rather than an idle one.
		p := newProgress(a.lang.T("task_running"), func(context.Context, *taskReporter) error { return nil })
		for _, line := range previewTaskLog(a.lang) {
			p.appendLog(line)
		}
		p.setDownload(previewDownload(a.scriptVersion))
		p.resize(a.width, a.height, a.bodyLayout().span())
		a.task = p
	case "toolbox-report":
		// The report screen is the one a finished tool leaves behind: entering the
		// section and a group stands the panel where the run was started from, and
		// the sample run is recorded so the board behind it has a line too.
		a.enterSection("toolbox")
		a.push(buildToolGroup(tools.GroupUnlock))
		outcome := previewToolOutcome()
		a.toolResults = map[string]toolOutcome{outcome.id: outcome}
		a.report = &outcome
	}
	return a.Snapshot(width, height)
}

// previewTaskLog is the sample output of a rendered task screen.
func previewTaskLog(l i18n.Lang) []string {
	return []string{
		"$ systemctl restart " + sysinfo.ServiceName,
		"write " + sysinfo.ConfigJSON,
		"config ok: " + sysinfo.ConfigJSON,
		l.T("preview_deployed"),
	}
}

// previewDownload is the sample download reading of a rendered task screen. The name
// comes from the same helper the updater downloads with, so the sample cannot drift
// from the published asset shape.
func previewDownload(version string) (string, int64, int64) {
	name, _ := update.PackageFileName(version, "amd64")
	return name, 12 << 20, 29 << 20
}

// previewToolOutcome is the sample a rendered report screen shows. A report exists only
// after a tool has run, and a rendered frame has no event loop to run one, so the screen is
// drawn from a table of the shape the unlock entries produce.
func previewToolOutcome() toolOutcome {
	return toolOutcome{
		id:   "unlock-media",
		when: time.Date(2026, 9, 26, 7, 42, 11, 0, time.UTC),
		result: toolbox.Result{
			Headers: []string{"service", "status", "region"},
			Rows: [][]string{
				{"Netflix", "unlocked", "US"},
				{"Disney+", "unlocked", "US"},
				{"YouTube Premium", "unlocked", "US"},
				{"Amazon Prime Video", "unlocked", "US"},
				{"DAZN", "unlocked", "SC"},
				{"Spotify", "blocked", "—"},
				{"TikTok", "unlocked", "SC"},
			},
			Notes: []string{
				"Spotify: Spotify refuses registration from this IP (status 320): You seem to be using a proxy service.",
			},
			Summary: "unlocked 6 · blocked 1 (7)",
		},
	}
}

// enterSection pushes the submenu of a root entry by id, so a page can be rendered by
// name. It reports whether the entry exists and has a submenu to stand in.
func (a *App) enterSection(id string) bool {
	for _, n := range buildRoot().nodes {
		if n.id != id || n.sub == nil {
			continue
		}
		a.push(n.sub)
		a.section = id
		return true
	}
	return false
}

func collectStatus(version string) tea.Cmd {
	return func() tea.Msg {
		return statusMsg(sysinfo.Collect(version))
	}
}

func quit() tea.Cmd {
	return func() tea.Msg { return tea.Quit() }
}

func (a *App) current() *menu { return a.stack[len(a.stack)-1] }

func (a *App) push(m *menu) {
	a.stack = append(a.stack, m)
	a.index = 0
}

func (a *App) pop() {
	if len(a.stack) > 1 {
		a.stack = a.stack[:len(a.stack)-1]
		a.index = 0
	}
	if len(a.stack) == 1 {
		a.section = ""
	}
}

// moveRows moves the cursor one row. On the main menu the entries are drawn in
// two columns, so a row is one column slot: moving by entry there would walk the
// cursor sideways into the other column halfway down the list.
func (a *App) moveRows(d int) {
	n := a.itemCount()
	if n == 0 {
		return
	}
	if a.menuColumns() < 2 {
		a.index = (a.index + d + n) % n
		return
	}
	start, height := a.columnSpan(a.index)
	a.index = start + (a.index-start+d+height)%height
}

// moveColumn hops to the same row of the neighbouring column, staying in the last
// row when the column it lands on is shorter.
func (a *App) moveColumn(d int) {
	half := a.colHalf()
	if a.index < half {
		if d < 0 {
			return
		}
		height := a.itemCount() - half
		a.index = half + min(a.index, height-1)
		return
	}
	if d > 0 {
		return
	}
	a.index = min(a.index-half, half-1)
}

// colHalf is the number of rows in the left column of the main menu; the left
// column takes the extra row when the count is odd.
func (a *App) colHalf() int {
	return (a.itemCount() + 1) / 2
}

// columnSpan is the first index and the row count of the column holding i.
func (a *App) columnSpan(i int) (start, height int) {
	half := a.colHalf()
	if i < half {
		return 0, half
	}
	return half, a.itemCount() - half
}

// menuColumns reports how many columns the page the operator stands in is drawn in. It has to
// agree with the renderer, because the arrows move the cursor through whichever layout is on
// screen: the main menu is two columns once its card is wide enough, and a page inside a
// section is two columns only when its entries no longer fit one per line and entryRows falls
// back to the panel's columns. Reading one column while the page drew two is what made right
// run the highlighted entry and left leave the page on those menus.
func (a *App) menuColumns() int {
	if len(a.current().nodes) == 0 {
		return 1
	}
	if (ui.InnerWidth(a.style(), a.frameWidth())-1)/2 < 16 {
		return 1
	}
	if a.sectionID() == "" {
		return 2
	}
	if a.itemCount() > boxRows(a.bodyLayout().menu) {
		return 2
	}
	return 1
}

// itemCount is the number of selectable rows: menu nodes plus the trailing
// navigation row ("back") when the current menu is not the root.
func (a *App) itemCount() int {
	n := len(a.current().nodes)
	if n == 0 {
		return 1
	}
	if a.hasNavRow() {
		return n + 1
	}
	return n
}

// hasNavRow reports whether the current menu shows the trailing navigation
// row. The root menu has none: quit with Q/Esc.
func (a *App) hasNavRow() bool {
	return len(a.stack) > 1
}

// onNavRow reports whether the cursor sits on the trailing navigation row.
func (a *App) onNavRow() bool {
	return a.hasNavRow() && a.index >= len(a.current().nodes)
}

// navLabel is the label of the trailing navigation row.
func (a *App) navLabel() string {
	return a.lang.T("nav_back")
}

func (a *App) selected() *node {
	nodes := a.current().nodes
	if len(nodes) == 0 || a.onNavRow() {
		return nil
	}
	if a.index >= len(nodes) {
		a.index = len(nodes) - 1
	}
	return nodes[a.index]
}

func (a *App) setToast(msg string, warn bool) {
	a.toast = msg
	a.toastErr = warn
}

// taskToast is a message a finished task wants shown in the status bar. A task runs
// on its own goroutine and never touches the interface, so it hands the message back
// through the reporter and the render goroutine sets it.
type taskToast struct {
	msg  string
	warn bool
}

func (a *App) enter() tea.Cmd {
	if a.onNavRow() {
		if len(a.stack) > 1 {
			a.pop()
			return nil
		}
		return quit()
	}
	n := a.selected()
	if n == nil {
		return nil
	}
	// Entering a root entry is what decides the current section, which is what gives a
	// page its 看板. Only an entry that opens a page counts: an entry that runs an
	// action in place would otherwise leave the frame showing that section's 看板 above
	// a menu it does not own, with Esc quitting the panel instead of walking back.
	if a.current().id == "root" && n.sub != nil {
		a.section = n.id
	}
	if n.sub != nil {
		a.push(n.sub)
		return a.sectionRefresh()
	}
	if n.action != nil {
		return n.action(a)
	}
	return nil
}

func (a *App) startTask(title string, fn taskFunc) tea.Cmd {
	p := newProgress(title, fn)
	p.resize(a.width, a.height, a.bodyLayout().span())
	a.task = p
	return p.Init()
}

// startTaskQR is startTask for the subscription QR codes: the log is a picture,
// so the copy key is hidden.
func (a *App) startTaskQR(title string, fn taskFunc) tea.Cmd {
	p := newProgress(title, fn)
	p.noCopy = true
	p.resize(a.width, a.height, a.bodyLayout().span())
	a.task = p
	return p.Init()
}

// subscriptionTitle is the card title for a subscription endpoint, e.g.
// "sing-box 订阅".
func subscriptionTitle(lang i18n.Lang, client subscribe.Client) string {
	switch client {
	case subscribe.ClientSingBox:
		return lang.T("links_sub_singbox")
	case subscribe.ClientMihomo:
		return lang.T("links_sub_mihomo")
	default:
		return lang.T("links_sub_v2ray")
	}
}

// openForm shows a single-value text prompt over the dashboard.
func (a *App) openForm(title, prompt, initial, hint string, submit formSubmit) {
	f := newForm(title, prompt, initial, hint, submit)
	f.resize(ui.InnerWidth(a.style(), a.frameWidth()))
	a.form = f
}

// openDualForm shows a two-field prompt: a number, a unit, a number, a unit.
func (a *App) openDualForm(title, prompt, first, second, hint string, submit dualSubmit) {
	f := newDualForm(title, prompt, first, second, hint, submit)
	f.resize(ui.InnerWidth(a.style(), a.frameWidth()))
	a.form = f
}

func (a *App) handleFormKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	f := a.form
	if f == nil {
		return a, nil
	}
	switch strings.ToLower(msg.String()) {
	case "ctrl+c":
		return a, quit()
	case "esc":
		a.form = nil
		return a, nil
	case "enter":
		var cmd tea.Cmd
		var err error
		switch {
		case f.dual != nil:
			cmd, err = f.dual(a, f.input.Value(), f.second.Value())
		case f.submit != nil:
			cmd, err = f.submit(a, f.input.Value())
		}
		if err != nil {
			f.err = err.Error()
			return a, nil
		}
		if a.form == f {
			a.form = nil
		}
		return a, tea.Batch(cmd, collectStatus(a.scriptVersion))
	default:
		return a, f.update(msg)
	}
}

// Update is the model's single entry point for everything that happens: key presses,
// resizes, the terminal's answer about its background, and the results the actions
// reported back. It returns the model because a message may replace it.
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		// Follow the terminal background unless --theme pinned a palette.
		if a.themeAuto {
			a.setSkin(a.skin, msg.IsDark())
		}
		return a, nil
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		a.sized = true
		if a.task != nil {
			a.task.resize(msg.Width, msg.Height, a.bodyLayout().span())
		}
		if a.form != nil {
			a.form.resize(ui.InnerWidth(a.style(), a.frameWidth()))
		}
		return a, nil
	case statusMsg:
		a.status = sysinfo.Status(msg)
		a.ready = true
		return a, nil
	case bbrVersionsMsg:
		a.applyBBRVersions(msg)
		return a, nil
	case bbrStatusMsg:
		a.bbrStatus = msg.status
		return a, nil
	}

	if a.form != nil {
		if key, ok := msg.(tea.KeyPressMsg); ok {
			return a.handleFormKey(key)
		}
		return a, a.form.update(msg)
	}

	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return a.handleKey(msg)
	case spinner.TickMsg:
		if a.task != nil {
			return a, a.task.handle(msg)
		}
	case logLineMsg, logsClosedMsg:
		if a.task != nil {
			return a, a.task.handle(msg)
		}
	case taskDoneMsg:
		if a.task != nil {
			cmd := a.task.handle(msg)
			if a.task.done {
				// The account file is what the account menus render from, so the
				// snapshot is refreshed as soon as a task finishes; a section whose
				// 看板 reads the machine (BBR) takes its reading again too, or the
				// page would keep showing what it said before the task ran.
				a.refreshAccountMenus()
				a.refreshNodeMenus()
				a.adoptTaskResult()
				return a, tea.Batch(cmd, collectStatus(a.scriptVersion), a.sectionRefresh())
			}
			return a, tea.Batch(cmd, collectStatus(a.scriptVersion))
		}
	}
	return a, nil
}

func (a *App) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	raw := msg.String()
	key := strings.ToLower(raw)

	// One key leaves the panel, from every page and at every depth: q (either case) and
	// ctrl+c. It is checked before any screen gets the key, so no page can interpret it as
	// "go back" — Esc is the way back, and the hints say so on every page that has one.
	if raw == "Q" || key == "q" || key == "ctrl+c" {
		return a, quit()
	}

	if a.task != nil {
		cmd, done := a.task.handleKey(msg, a.lang)
		if done {
			a.task = nil
		}
		return a, cmd
	}

	if a.links != nil {
		cmd, done := a.links.handleKey(msg, a.lang)
		if done {
			a.links = nil
		}
		return a, cmd
	}

	// The report screen is a dead end with one way forward: read it, run it again, or go
	// back. Nothing else it could do would be clearer than that.
	if a.report != nil {
		switch key {
		case "esc", "backspace":
			// Esc is the way back; Enter is not, because Enter means "enter or
			// confirm" everywhere else and a report has nothing to enter.
			a.report = nil
			a.scroll, a.scrollMax = 0, 0
		case "r":
			return a, a.rerunReport()
		case "up", "k":
			a.scrollBy(-1)
		case "down", "j":
			a.scrollBy(1)
		case "pgup":
			a.scrollBy(-a.screenRows())
		// Paging is PageDown or the space bar. Right stays out of it: the arrows move a
		// cursor, and this screen has none, so a key that runs or pages on one page must
		// not do a third thing on another.
		case "pgdown", " ":
			a.scrollBy(a.screenRows())
		case "home":
			a.scroll = 0
		case "end":
			a.scroll = a.scrollMax
		}
		return a, nil
	}

	if a.toast != "" {
		a.toast = ""
	}

	// The system screen owns a few keys of its own and lets the panel-wide shortcuts
	// (language and refresh) fall through. It has no cursor of its own to move, so no
	// other key may touch the menu behind it: Enter and the arrows used to reach the
	// dashboard's cursor, so pressing right on the system screen silently opened a
	// menu the operator could not see, and Esc then closed the screen onto it.
	if a.system != nil {
		if cmd, handled := a.system.handleKey(msg, a); handled {
			return a, cmd
		}
		switch key {
		case "l":
			a.lang = a.lang.Toggle()
			a.quote = a.lang.Hitokoto()
			a.remember()
		case "r":
			return a, collectStatus(a.scriptVersion)
		}
		return a, nil
	}

	switch key {
	case "esc", "backspace":
		if len(a.stack) > 1 {
			a.pop()
		} else {
			return a, quit()
		}
	case "up", "k":
		a.moveRows(-1)
	case "down", "j":
		a.moveRows(1)
	case "0", "1", "2", "3", "4", "5", "6", "7", "8", "9":
		// Shell-style numeric selection: pick the row, Enter runs it. 0 targets
		// the trailing navigation row, which only exists in submenus.
		if key == "0" {
			if a.hasNavRow() {
				a.index = len(a.current().nodes)
			}
		} else if n := int(key[0] - '0'); n <= len(a.current().nodes) {
			a.index = n - 1
		}
	case "left":
		// The arrows only ever move the cursor. On a page drawn in two columns left and
		// right step between them; on a one-column page there is no column to step to and
		// the key does nothing. No cursor key opens an entry or leaves a page: Enter enters
		// and confirms, Esc goes back, and nothing else does either.
		if a.menuColumns() > 1 {
			a.moveColumn(-1)
		}
	case "right":
		if a.menuColumns() > 1 {
			a.moveColumn(1)
		}
	case "enter":
		return a, a.enter()
	case "l":
		a.lang = a.lang.Toggle()
		a.quote = a.lang.Hitokoto()
		a.remember()
	case "r":
		return a, collectStatus(a.scriptVersion)
	}
	return a, nil
}

// View draws the current frame. Which screen is drawn depends on the mode the model is
// in - a form, a running task, a finished report, or the menu tree - and each of those
// draws into the same fixed layout, so the frame does not change shape as work starts
// and ends. Nothing is drawn until the terminal has reported its size.
func (a *App) View() tea.View {
	if !a.sized {
		// Wait for the first size report before drawing.
		return tea.NewView("")
	}
	var content string
	switch {
	case a.form != nil:
		content = a.formScreen()
	case a.task != nil:
		content = a.task.View(a.width, a.height, a.statusStrip(a.frameWidth()), a.style(), a.lang, a.iconSet, a.bodyLayout())
	case a.links != nil:
		content = a.links.View(a.width, a.height, a.palette, a.lang, a.iconSet)
	case a.report != nil:
		content = a.reportScreen()
	default:
		content = a.dashboard()
	}
	v := tea.NewView(content)
	// Fullscreen keeps the terminal clean: the shell prompt and command above
	// are hidden while the dashboard runs, and the inline renderer's stale-frame
	// stacking cannot happen.
	v.AltScreen = true
	return v
}

// formScreen draws a prompt in the panel's fixed frame: the same status strip, the same
// box in the rows a page's two boxes would use, and the same keys box underneath. A prompt
// is therefore the same page as the one it was opened from, and opening one never moves the
// frame.
func (a *App) formScreen() string {
	w, h := a.frameWidth(), a.height
	if h <= 0 {
		h = 24
	}
	l := a.bodyLayout()
	out := []string{a.statusStrip(w), ""}
	out = append(out, a.boxAt(a.form.title, a.form.body(a.palette, ui.InnerWidth(a.style(), w)), w, l.span())...)
	out = append(out, keyTail(a.palette, a.lang, "", a.form.hintLine(a.lang), w, l.tail)...)
	return ui.Fit(out, w, h)
}

// frameWidth is the shared panel width: the terminal width capped at 100 so
// every screen lines up with the main dashboard.
func (a *App) frameWidth() int {
	return panelWidth(a.width)
}

// menuLabelColumn returns the column at which menu descriptions start so they all
// line up behind the widest numbered label.
func (a *App) menuLabelColumn() int {
	width := 0
	for i, n := range a.current().nodes {
		if w := lipgloss.Width(a.numberedLabel(i, n)); w > width {
			width = w
		}
	}
	if a.hasNavRow() {
		if w := lipgloss.Width(a.numberedLabel(len(a.current().nodes), nil)); w > width {
			width = w
		}
	}
	return width + 3
}

// numberTag is the bracket in front of every menu entry: entries are picked by
// number as well as by cursor, which is how the entries stay countable once the
// list is longer than a screen.
func (a *App) numberTag(i int) string {
	return fmt.Sprintf("[ %d ] ", i+1)
}

// numberedLabel is one entry as it is drawn in a menu: its number, then its name.
// A nil node is the trailing navigation row, which is numbered like the rest.
func (a *App) numberedLabel(i int, n *node) string {
	name := a.navLabel()
	if n != nil {
		name = n.label(a.lang)
	}
	return a.numberTag(i) + name
}

func (a *App) renderToast(w int) string {
	icon := a.iconSet.Info
	col := a.palette.Primary
	if a.toastErr {
		icon = a.iconSet.Warn
		col = a.palette.Warn
	}
	return " " + a.palette.Colored(col, icon+" "+theme.Truncate(a.toast, w-4))
}

// dashboardHint is the key list shown in the pinned hint box on the main menu.
func (a *App) dashboardHint() string {
	if a.system != nil {
		return strings.Join([]string{
			a.lang.T("hint_system"),
			a.lang.T("hint_back"),
			a.lang.T("hint_lang"),
			a.lang.T("hint_quit"),
		}, "  ")
	}
	hints := []string{a.lang.T("hint_navigate")}
	if a.menuColumns() > 1 {
		hints = append(hints, a.lang.T("hint_columns"))
	}
	return strings.Join(append(hints,
		a.lang.T("hint_enter"),
		a.lang.T("hint_back"),
		a.lang.T("hint_lang"),
		a.lang.T("hint_quit"),
	), "  ")
}

// scrollBy moves a scrollable screen, staying inside the rows it drew. A table taller than its
// box is read with the arrow keys instead of being cut off with a count of what was left out.
func (a *App) scrollBy(delta int) {
	a.scroll += delta
	if a.scroll < 0 {
		a.scroll = 0
	}
	if a.scroll > a.scrollMax {
		a.scroll = a.scrollMax
	}
}

// screenRows is one page of a scrollable screen: the rows its box shows, which is how far
// PageUp and PageDown move.
func (a *App) screenRows() int {
	rows := boxRows(a.bodyLayout().span())
	if rows < 1 {
		rows = 1
	}
	return rows
}

// windowRows is the slice of rows a scrollable screen shows from an offset. It never grows the
// box: what is past the last row is reached with the arrow keys, and the hint bar says so.
func windowRows(rows []string, offset, n int) []string {
	if n <= 0 || len(rows) <= n {
		return rows
	}
	if offset > len(rows)-n {
		offset = len(rows) - n
	}
	if offset < 0 {
		offset = 0
	}
	return rows[offset : offset+n]
}
