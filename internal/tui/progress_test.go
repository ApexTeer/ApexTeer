package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/MinimaxFlora/EasySB/internal/i18n"
	"github.com/MinimaxFlora/EasySB/internal/icons"
	"github.com/MinimaxFlora/EasySB/internal/theme"
)

// testLayout is the layout a task screen is drawn into: the same two slots a page uses, so
// the screen can be tested without an App to measure the main page with.
func testLayout() layout { return layout{board: 6, gap: 1, menu: 8, tail: 4} }

func TestProgressDoneScrolls(t *testing.T) {
	p := newProgress("qr", func(context.Context, *taskReporter) error { return nil })
	p.resize(80, 30, 20)
	for i := 0; i < 200; i++ {
		p.logs = append(p.logs, "line")
	}
	p.refresh()
	p.done = true

	before := p.vp.YOffset()
	if before == 0 {
		t.Fatalf("expected finished task to start scrolled to the bottom, got offset 0")
	}
	if _, closed := p.handleKey(press(tea.KeyUp), i18n.Chinese); closed {
		t.Fatalf("up key should scroll, not close the task")
	}
	if got := p.vp.YOffset(); got >= before {
		t.Fatalf("up key did not scroll: before=%d after=%d", before, got)
	}
	// Enter enters and confirms; it never goes back. A finished task is left with
	// Esc, the same as every other page.
	if _, closed := p.handleKey(press(tea.KeyEnter), i18n.Chinese); closed {
		t.Fatalf("enter must not close a finished task")
	}
	if _, closed := p.handleKey(press(tea.KeyEsc), i18n.Chinese); !closed {
		t.Fatalf("esc should close a finished task")
	}
}

func TestProgressFollowsTailWhileRunning(t *testing.T) {
	p := newProgress("t", func(context.Context, *taskReporter) error { return nil })
	p.resize(80, 20, 20)
	for i := 0; i < 100; i++ {
		p.appendLog("x")
	}
	if !p.vp.AtBottom() {
		t.Fatalf("running task should keep following the log tail")
	}
}

func TestProgressArrowsScrollFinishedLog(t *testing.T) {
	p := newProgress("qr", func(context.Context, *taskReporter) error { return nil })
	p.resize(80, 20, 20)
	for i := 0; i < 100; i++ {
		p.appendLog("line")
	}
	p.done = true

	before := p.vp.YOffset()
	if before == 0 {
		t.Fatalf("expected finished task to start at the bottom")
	}
	if _, closed := p.handleKey(press(tea.KeyUp), i18n.Chinese); closed {
		t.Fatal("arrow up should scroll, not close the task")
	}
	up := p.vp.YOffset()
	if up >= before {
		t.Fatalf("arrow up did not scroll: before=%d after=%d", before, up)
	}
	p.handleKey(press(tea.KeyDown), i18n.Chinese)
	if got := p.vp.YOffset(); got <= up {
		t.Fatalf("arrow down did not scroll back: up=%d down=%d", up, got)
	}
}

func TestProgressCopiesLogToClipboard(t *testing.T) {
	p := newProgress("url", func(context.Context, *taskReporter) error { return nil })
	p.resize(80, 20, 20)
	p.appendLog("sing-box: https://example.com/a")
	p.appendLog("v2rayN: https://example.com/c")
	p.done = true

	cmd, closed := p.handleKey(press('c'), i18n.Chinese)
	if closed {
		t.Fatal("copy must not close the task")
	}
	if cmd == nil {
		t.Fatal("copy must return a clipboard command")
	}
	payload := fmt.Sprint(cmd())
	for _, want := range []string{"https://example.com/a", "https://example.com/c"} {
		if !strings.Contains(payload, want) {
			t.Fatalf("clipboard payload %q is missing %q", payload, want)
		}
	}
}

func TestProgressIgnoresMouseToggleKey(t *testing.T) {
	p := newProgress("t", func(context.Context, *taskReporter) error { return nil })
	p.resize(80, 20, 20)
	if _, closed := p.handleKey(press('m'), i18n.Chinese); closed {
		t.Fatal("a stray key must not close the task")
	}
}

func TestProgressFitsTerminal(t *testing.T) {
	// The task screen is what every action lands on, so it has to fit the terminal
	// at any height the same way the dashboard does: no frame taller than the
	// window, and no line wider than it.
	for _, size := range [][2]int{{40, 6}, {80, 9}, {80, 12}, {100, 16}, {100, 22}, {100, 24}, {120, 40}, {140, 60}} {
		w, h := size[0], size[1]
		p := newProgress("订阅服务", func(context.Context, *taskReporter) error { return nil })
		for i := 0; i < 40; i++ {
			p.appendLog("log line " + strconv.Itoa(i) + " with some length to it")
		}
		for _, done := range []bool{false, true} {
			p.done = done
			out := p.View(w, h, "host  service  node", theme.DefaultSkin().Style(true), i18n.Chinese, icons.Symbols(), testLayout())
			lines := strings.Split(out, "\n")
			if len(lines) != h {
				t.Fatalf("%dx%d done=%v: drew %d lines, want %d", w, h, done, len(lines), h)
			}
			for i, line := range lines {
				if got := lipgloss.Width(line); got > w {
					t.Fatalf("%dx%d done=%v: line %d is %d wide", w, h, done, i, got)
				}
			}
		}
	}
}

func TestProgressNoCopyHidesAndIgnoresCopy(t *testing.T) {
	p := newProgress("qr", func(context.Context, *taskReporter) error { return nil })
	p.noCopy = true
	p.resize(80, 20, 20)
	p.appendLog("QR")
	p.done = true

	if cmd, closed := p.handleKey(press('c'), i18n.Chinese); cmd != nil || closed {
		t.Fatalf("a picture task must ignore copy, cmd=%v closed=%v", cmd != nil, closed)
	}
	view := p.View(80, 20, "", theme.DefaultSkin().Style(true), i18n.Chinese, icons.Symbols(), testLayout())
	if strings.Contains(view, i18n.Chinese.T("task_copy")) {
		t.Fatal("the hint must not offer copy on a picture task")
	}
	if !strings.Contains(view, i18n.Chinese.T("hint_quit")) {
		t.Fatal("the hint should offer Q to quit")
	}
}

// TestProgressDrawsDownloadBar keeps the live download reading inside the frame. The
// bar shares its row with the file name and the byte counts, so a long package name, a
// short terminal and a server that announces no length all have to stay inside the
// card: the screen must not grow a line wider or taller than the window.
func TestProgressDrawsDownloadBar(t *testing.T) {
	cases := []downloadReading{
		{label: "sing-box-1.14.1-linux-amd64.tar.gz", done: 3 << 20, total: 9 << 20, live: true},
		{label: "linux-image-7.2.6-minimaxflora-bbrv3-max_7.2.6-1_amd64.deb", done: 118 << 20, total: 240 << 20, live: true},
		{label: "easysb-linux-amd64", done: 1 << 20, live: true},
	}
	for _, size := range [][2]int{{40, 6}, {80, 12}, {100, 24}, {160, 40}} {
		w, h := size[0], size[1]
		p := newProgress("安装 sing-box", func(context.Context, *taskReporter) error { return nil })
		p.resize(w, h, 15)
		p.appendLog("GET https://github.com/SagerNet/sing-box/releases/download/v1.14.1/sing-box-1.14.1-linux-amd64.tar.gz")
		for _, dl := range cases {
			p.dl = dl
			out := p.View(w, h, "host  service  node", theme.DefaultSkin().Style(true), i18n.Chinese, icons.Symbols(), testLayout())
			lines := strings.Split(out, "\n")
			if len(lines) != h {
				t.Fatalf("%dx%d: drew %d lines, want %d", w, h, len(lines), h)
			}
			for i, line := range lines {
				if got := lipgloss.Width(line); got > w {
					t.Fatalf("%dx%d: line %d is %d wide", w, h, i, got)
				}
			}
			if !strings.Contains(out, humanBytes(uint64(dl.done))) {
				t.Fatalf("%dx%d: the bar should show the bytes that arrived (%s)", w, h, dl.label)
			}
			want := "░"
			if dl.total > 0 {
				want = "█"
				if !strings.Contains(out, "100%") && !strings.Contains(out, "%") {
					t.Fatalf("%dx%d: the bar should show a percentage", w, h)
				}
			}
			if !strings.Contains(out, want) {
				t.Fatalf("%dx%d: the bar should be drawn for %s", w, h, dl.label)
			}
		}
	}
}

// TestProgressBarClearsWhenTaskEnds keeps a finished task from leaving a reading that is
// no longer moving on screen: the bar belongs to the moment, not to the result.
func TestProgressBarClearsWhenTaskEnds(t *testing.T) {
	p := newProgress("t", func(context.Context, *taskReporter) error { return nil })
	p.resize(100, 24, 20)
	p.setDownload("sing-box-1.14.1-linux-amd64.tar.gz", 1, 2)
	if got := p.reading(); !got.live {
		t.Fatal("a reading should be live when it arrives")
	}
	p.handle(taskDoneMsg{})
	if got := p.reading(); got.live {
		t.Fatalf("the bar should be dropped when the task ends, got %+v", got)
	}
}

// A run that can count its steps gets a bar with the count and the step it just finished. One
// that cannot gets how long it has been going and what it is doing, because a percentage
// nobody measured would be an invented number.
func TestTaskScreenShowsCountedAndUncountedRuns(t *testing.T) {
	style := theme.DefaultSkin().Style(true)
	draw := func(p *progressModel) string {
		p.resize(100, 32, testLayout().span())
		view := p.View(100, 32, "host", style, i18n.Chinese, icons.Symbols(), testLayout())
		return ansiSGR.ReplaceAllString(view, "")
	}

	counted := newProgress("三网回程", func(context.Context, *taskReporter) error { return nil })
	counted.setSteps(2, 4, "Mogadishu")
	view := draw(counted)
	if !strings.Contains(view, "2/4") || !strings.Contains(view, "Mogadishu") {
		t.Fatalf("a counted run should show its count and its step:\n%s", view)
	}

	uncounted := newProgress("CPU 跑分", func(context.Context, *taskReporter) error { return nil })
	uncounted.appendLog("bench/cpu: single core, 3 workloads")
	view = draw(uncounted)
	if !strings.Contains(view, i18n.Chinese.T("task_elapsed")) {
		t.Fatalf("a run that cannot count should show elapsed time:\n%s", view)
	}
	if !strings.Contains(view, "single core") {
		t.Fatalf("a run that cannot count should show what it is doing:\n%s", view)
	}

	// A finished run fills its bar: the screen is swapped for the report a moment later, and
	// a bar left at 2/4 would look like the run was cut short.
	counted.markComplete()
	if got := counted.stepState(); got.done != got.total || got.total != 4 {
		t.Fatalf("a finished run should fill its bar, got %d/%d", got.done, got.total)
	}
}

// A tool's progress callback reaches the screen through the reporter the panel hands it.
func TestTaskReporterCarriesSteps(t *testing.T) {
	p := newProgress("解锁检测", func(context.Context, *taskReporter) error { return nil })
	r := &taskReporter{steps: p.setSteps}
	r.Steps(1, 3, "Netflix")
	r.Steps(2, 3, "Disney+")
	got := p.stepState()
	if !got.live || got.done != 2 || got.total != 3 || got.label != "Disney+" {
		t.Fatalf("steps did not reach the screen: %+v", got)
	}
	// A tool run with nowhere to report changes nothing.
	quiet := &taskReporter{}
	quiet.Steps(3, 3, "Claude")
	if st := p.stepState(); st.done != 2 || st.label != "Disney+" {
		t.Fatalf("a reporter with no listener changed the reading: %+v", st)
	}
}
