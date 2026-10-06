package tui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/EasySBTeam/EasySB/internal/i18n"
	"github.com/EasySBTeam/EasySB/internal/icons"
	"github.com/EasySBTeam/EasySB/internal/theme"
	"github.com/EasySBTeam/EasySB/internal/ui"
)

// taskFunc is one unit of work the panel runs on its own goroutine: it writes lines
// through the reporter and returns the error that ends the task.
type taskFunc func(ctx context.Context, r *taskReporter) error

// taskReporter is a running task's link back to the screen that started it: Log for a
// line of output, Progress for a download that is still arriving, SetResult for what
// the screen should keep once the task is done. A task never touches the interface
// directly, so every screen sees its output in the order it was produced.
type taskReporter struct {
	log    func(string)
	prog   func(label string, done, total int64)
	steps  func(done, total int, label string)
	result func(any)
}

// Log appends one line to the task's output.
func (r *taskReporter) Log(line string) {
	if r.log != nil {
		r.log(line)
	}
}

// Progress reports a download in flight, which the task screen draws as a bar rather
// than as a line of output: a line would either flood the log or say nothing.
func (r *taskReporter) Progress(label string, done, total int64) {
	if r.prog != nil {
		r.prog(label, done, total)
	}
}

// Steps reports one finished step of a run that can count its work, which the task screen
// draws as a bar with the count and the name of the step. A run that cannot count its steps
// reports nothing, and the screen shows how long it has been going instead.
func (r *taskReporter) Steps(done, total int, label string) {
	if r.steps != nil {
		r.steps(done, total, label)
	}
}

// SetResult hands a value back to the model once the task is over, for the screens
// whose 看板 shows what the task found: a subscription link grid or an unlock report
// is worth more than the last run's log. Values are read back after taskDoneMsg, on
// the render goroutine, so the model guards the handover.
func (r *taskReporter) SetResult(value any) {
	if r.result != nil {
		r.result(value)
	}
}

// progressScrollStep is how many lines one arrow key scrolls the finished log.
// Three matches the feel of a browser wheel.
const progressScrollStep = 3

type logLineMsg string
type logsClosedMsg struct{}
type taskDoneMsg struct{ err error }

// downloadReading is the live state of the download a task is doing. The task
// goroutine writes it and the render goroutine reads it, so the model guards it.
type downloadReading struct {
	label string
	done  int64
	total int64
	live  bool
}

// stepReading is how far a counted run has got: the task goroutine writes it and the render
// goroutine reads it, exactly like the download reading above. live is false for a run that
// cannot count its work, which is what keeps the screen from drawing a bar that would be a
// guess.
type stepReading struct {
	done  int
	total int
	label string
	live  bool
}

type progressModel struct {
	title  string
	fn     taskFunc
	cancel context.CancelFunc
	ch     chan string
	errCh  chan error
	logs   []string
	spin   spinner.Model
	vp     viewport.Model
	done   bool
	err    error
	// noCopy hides the copy key on tasks whose output is a picture, such as the
	// subscription QR codes.
	noCopy bool
	width  int
	height int
	// mu guards dl, which the task goroutine writes while the screen is drawn, and
	// res, which the same goroutine hands over for the section that started it.
	mu    sync.Mutex
	dl    downloadReading
	steps stepReading
	res   any
	// started is when the task began, used to show how long a run that cannot count its
	// steps has been going.
	started time.Time
}

// newProgress builds the model for one task. It returns a pointer because the task
// goroutine records its download readings on the same value the screen draws.
func newProgress(title string, fn taskFunc) *progressModel {
	p := &progressModel{
		title:   title,
		fn:      fn,
		started: time.Now(),
		ch:      make(chan string, 256),
		errCh:   make(chan error, 1),
	}
	p.spin = spinner.New(spinner.WithSpinner(spinner.Line))
	p.vp = viewport.New()
	p.vp.SoftWrap = true
	return p
}

func (p *progressModel) Init() tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	fn := p.fn
	ch := p.ch
	errCh := p.errCh
	go func() {
		r := &taskReporter{
			log:    func(s string) { ch <- s },
			prog:   p.setDownload,
			steps:  p.setSteps,
			result: p.setResult,
		}
		err := fn(ctx, r)
		errCh <- err
		close(ch)
	}()
	return tea.Batch(p.tickCmd(), waitLog(ch))
}

// setResult records the value the task wants the model to keep. It is called from the
// task goroutine, so it takes the same lock the download reading uses.
func (p *progressModel) setResult(value any) {
	p.mu.Lock()
	p.res = value
	p.mu.Unlock()
}

// taskResult is what the finished task handed over, or nil. It is read on the render
// goroutine once the task is done.
func (p *progressModel) taskResult() any {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.res
}

// setDownload records one reading of a download. It is called from the task
// goroutine, so it never touches anything but the reading itself.
func (p *progressModel) setDownload(label string, done, total int64) {
	p.mu.Lock()
	p.dl = downloadReading{label: label, done: done, total: total, live: true}
	p.mu.Unlock()
}

// reading returns the current download state.
func (p *progressModel) reading() downloadReading {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dl
}

// clearDownload drops the bar: the task is over, so a reading that is no longer
// moving would only be a stale number on a finished screen.
func (p *progressModel) clearDownload() {
	p.mu.Lock()
	p.dl = downloadReading{}
	p.mu.Unlock()
}

// downloadBar draws the download a task is doing: the file, a bar sized to the row and
// the bytes, or the bytes alone when the server announced no length. An empty string
// means nothing is downloading, which is the case for every task that only reads local
// state.
func downloadBar(style theme.Style, ic icons.Set, dl downloadReading, w int) string {
	if !dl.live || w < 24 {
		return ""
	}
	tail := humanBytes(uint64(dl.done))
	if dl.total > 0 {
		tail = fmt.Sprintf("%d%%  %s / %s", dl.done*100/dl.total,
			humanBytes(uint64(dl.done)), humanBytes(uint64(dl.total)))
	}
	head := " " + ic.Download + " "
	barW := w / 3
	if barW < 8 {
		barW = 8
	}
	// The numbers and the bar matter more than the file name, so the label gets
	// whatever is left over once they have their room.
	if labelW := w - lipgloss.Width(head) - lipgloss.Width(tail) - barW - 3; labelW < 8 {
		barW = w - lipgloss.Width(head) - lipgloss.Width(tail) - 11
		if barW < 4 {
			barW = 4
		}
	}
	labelW := w - lipgloss.Width(head) - lipgloss.Width(tail) - barW - 3
	if labelW < 1 {
		labelW = 1
	}
	label := theme.Truncate(dl.label, labelW)
	bar := style.Faint(strings.Repeat("░", barW))
	if dl.total > 0 {
		bar = ui.Meter(style, float64(dl.done)/float64(dl.total), barW)
	}
	line := style.Faint(head) + style.Value(label) + strings.Repeat(" ", labelW-lipgloss.Width(label)) +
		" " + bar + "  " + style.Value(tail)
	return theme.Pad(theme.Truncate(line, w), w)
}

// setSteps records one finished step of a counted run. It is called from the task goroutine,
// so it takes the same lock the download reading uses.
func (p *progressModel) setSteps(done, total int, label string) {
	if total <= 0 {
		return
	}
	if done > total {
		done = total
	}
	p.mu.Lock()
	p.steps = stepReading{done: done, total: total, label: label, live: true}
	p.mu.Unlock()
}

// stepState returns how far the run has got, and whether it can count at all.
func (p *progressModel) stepState() stepReading {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.steps
}

// markComplete fills the bar to its end: a run that has finished should look finished for the
// moment it is still on screen, instead of vanishing at 8/12.
func (p *progressModel) markComplete() {
	p.mu.Lock()
	if p.steps.live && p.steps.total > 0 {
		p.steps.done = p.steps.total
	}
	p.mu.Unlock()
}

// elapsed is how long the task has been running.
func (p *progressModel) elapsed() time.Duration {
	if p.started.IsZero() {
		return 0
	}
	return time.Since(p.started)
}

// stepBar draws a counted run: a bar that fills, the step count, and what is being worked on
// right now. An empty string means the run cannot count its steps — a benchmark, a transfer in
// flight — and that screen keeps the spinner and the elapsed time it already shows.
func stepBar(style theme.Style, st stepReading, lang i18n.Lang, w int) string {
	if !st.live || st.total <= 0 || w < 24 {
		return ""
	}
	tail := fmt.Sprintf("%d/%d", st.done, st.total)
	barW := w / 3
	if barW < 8 {
		barW = 8
	}
	label := st.label
	head := " " + lang.T("task_progress") + " "
	// The count and the bar matter more than the label, so the label gets whatever is
	// left once they have their room.
	labelW := w - lipgloss.Width(head) - lipgloss.Width(tail) - barW - 4
	if labelW < 1 {
		barW = w - lipgloss.Width(head) - lipgloss.Width(tail) - 5
		if barW < 4 {
			return theme.Pad(theme.Truncate(head+tail, w), w)
		}
		labelW = 1
	}
	label = theme.Truncate(label, labelW)
	bar := ui.Meter(style, float64(st.done)/float64(st.total), barW)
	line := style.Faint(head) + bar + "  " + style.Value(tail) + "  " + style.Value(label)
	return theme.Pad(theme.Truncate(line, w), w)
}

// elapsedLine is what a run that cannot count its steps shows instead of a bar: how long it
// has been going and the last thing the tool said. It is a reading, not a progress bar,
// because a percentage nobody measured would be a made-up number.
func elapsedLine(style theme.Style, d time.Duration, last string, lang i18n.Lang, w int) string {
	if w < 24 {
		return ""
	}
	head := " " + lang.T("task_elapsed") + " " + d.Round(time.Second).String() + "  "
	last = theme.Truncate(strings.TrimSpace(last), maxInt(1, w-lipgloss.Width(head)))
	return theme.Pad(theme.Truncate(style.Faint(head)+style.Faint(last), w), w)
}

func (p *progressModel) tickCmd() tea.Cmd {
	return func() tea.Msg { return p.spin.Tick() }
}

func waitLog(ch chan string) tea.Cmd {
	return func() tea.Msg {
		line, ok := <-ch
		if !ok {
			return logsClosedMsg{}
		}
		return logLineMsg(line)
	}
}

func waitErr(ch chan error) tea.Cmd {
	return func() tea.Msg { return taskDoneMsg{err: <-ch} }
}

func (p *progressModel) handle(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case spinner.TickMsg:
		var cmd tea.Cmd
		p.spin, cmd = p.spin.Update(m)
		return cmd
	case logLineMsg:
		p.appendLog(string(m))
		return waitLog(p.ch)
	case logsClosedMsg:
		return waitErr(p.errCh)
	case taskDoneMsg:
		p.done = true
		p.err = m.err
		p.clearDownload()
		if m.err != nil {
			p.appendLog("✗ " + m.err.Error())
		}
		p.refresh()
		return nil
	}
	return nil
}

func (p *progressModel) handleKey(msg tea.KeyPressMsg, lang i18n.Lang) (tea.Cmd, bool) {
	key := strings.ToLower(msg.String())
	if p.done {
		switch key {
		case "esc", "backspace":
			// q is deliberately absent: it quits the panel from every page, so it
			// reaches the global shortcut instead of dismissing this screen.
			return nil, true
		case "c":
			if p.noCopy {
				break
			}
			text := strings.Join(p.logs, "\n")
			p.appendLog("✓ " + lang.T("copied"))
			return tea.SetClipboard(text), false
		case "up", "k":
			p.vp.ScrollUp(progressScrollStep)
			return nil, false
		case "down", "j":
			p.vp.ScrollDown(progressScrollStep)
			return nil, false
		}
		var cmd tea.Cmd
		p.vp, cmd = p.vp.Update(msg)
		return cmd, false
	}
	if key == "esc" || key == "ctrl+c" {
		if p.cancel != nil {
			p.cancel()
		}
		p.appendLog("✗ " + lang.T("cancelled"))
		return nil, false
	}
	var cmd tea.Cmd
	p.vp, cmd = p.vp.Update(msg)
	return cmd, false
}

func (p *progressModel) resize(w, h int, span int) {
	w = panelWidth(w)
	p.width, p.height = w, h
	inner := w - 4
	if inner < 10 {
		inner = 10
	}
	// The log sits inside the box the task fills, so the viewport is what is left of
	// that box once its borders and the live reading's row are taken.
	vh := boxRows(span) - 1
	if vh < 1 {
		vh = 1
	}
	p.vp.SetWidth(inner)
	p.vp.SetHeight(vh)
	p.refresh()
}

// taskHintRows is how many rows the hint bar takes, matching the dashboard: a
// boxed hint on a roomy terminal, one line when there is almost no room.

func (p *progressModel) appendLog(line string) {
	p.logs = append(p.logs, line)
	p.refresh()
}

func (p *progressModel) refresh() {
	follow := p.vp.AtBottom()
	p.vp.SetContent(strings.Join(p.logs, "\n"))
	if follow {
		p.vp.GotoBottom()
	}
}

// body is the card's content: the live download bar on top while something is
// arriving, then the log. The bar takes the first row and the log gives up its last
// one, so the frame keeps its size and the screen does not jump when a download starts
// or finishes.
func (p *progressModel) body(style theme.Style, ic icons.Set, lang i18n.Lang, width int, room int) []string {
	lines := strings.Split(p.vp.View(), "\n")
	inner := ui.InnerWidth(style, width)
	// A counted run gets its bar first: it is the line the operator watches. One that
	// cannot count gets the elapsed time and the last thing the tool said.
	live := stepBar(style, p.stepState(), lang, inner)
	if live == "" {
		liveness := downloadBar(style, ic, p.reading(), inner)
		if liveness == "" && !p.done {
			liveness = elapsedLine(style, p.elapsed(), p.lastLog(), lang, inner)
		}
		live = liveness
	}
	if live == "" {
		return clipLines(style, lines, room)
	}
	if len(lines) < 2 {
		// A one-row card has no room for both, and the live reading is the more useful
		// half of it.
		return []string{live}
	}
	body := make([]string, 0, len(lines))
	body = append(body, live)
	return clipLines(style, append(body, lines...), room)
}

// lastLog is the most recent progress line the tool printed, which is what a run that cannot
// count its steps is currently doing.
func (p *progressModel) lastLog() string {
	if len(p.logs) == 0 {
		return ""
	}
	return p.logs[len(p.logs)-1]
}

// View draws the task the way the dashboard draws everything else: the live
// status strip on top, the log in a card titled with the task, and the keys on the
// bottom bar. The task screen is where the subscription service, the kernel
// install and the deployment all end up, so it is the one screen that has to look
// like the rest of the panel rather than like a raw console.
func (p *progressModel) View(w, h int, strip string, style theme.Style, lang i18n.Lang, ic icons.Set, l layout) string {
	width := panelWidth(w)
	if width != p.width || h != p.height {
		p.resize(width, h, l.span())
	}
	pal := style.Palette

	badge := pal.Colored(pal.Primary, p.spin.View()+" "+lang.T("task_running"))
	if p.done {
		if p.err != nil {
			badge = pal.State(ic.Err+" "+lang.T("task_failed"), false, false)
		} else {
			badge = pal.State(ic.OK+" "+lang.T("task_done"), true, false)
		}
	}

	// A running task fills the space a page's two boxes would have used: one box, the same
	// rows, in the same place. The log lives inside it, and the keys stay where every other
	// page puts them.
	lines := make([]string, 0, h)
	if strip != "" {
		lines = append(lines, strip, "")
	}
	body := p.body(style, ic, lang, ui.InnerWidth(style, width), boxRows(l.span()))
	lines = append(lines, boxAt(style, p.title, badge, body, width, l.span())...)
	lines = append(lines, p.hintTail(pal, lang, width, l.tail)...)
	return ui.Fit(lines, width, h)
}

// hintTail renders the task's keys in the rows the layout reserves for the tail, so the hint
// box sits in the same place as it does on a menu page.
func (p *progressModel) hintTail(pal theme.Palette, lang i18n.Lang, width, rows int) []string {
	var hint string
	if p.done {
		parts := []string{lang.T("task_scroll")}
		if !p.noCopy {
			parts = append(parts, lang.T("task_copy"))
		}
		parts = append(parts, lang.T("hint_back"), lang.T("hint_quit"))
		hint = strings.Join(parts, "  ")
	} else {
		hint = lang.T("hint_back") + "  " + lang.T("cancelled")
	}
	return keyTail(pal, lang, "", hint, width, rows)
}
