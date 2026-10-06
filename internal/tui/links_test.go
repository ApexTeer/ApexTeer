package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/EasySB-Team/EasySB/internal/i18n"
	"github.com/EasySB-Team/EasySB/internal/icons"
	"github.com/EasySB-Team/EasySB/internal/theme"
)

func sampleLinks() []linkItem {
	return []linkItem{
		{label: "sing-box 订阅", desc: "sing-box 原生配置", value: "https://example.com:8443/singbox/aaaa"},
		{label: "mihomo 订阅", desc: "完整 YAML 配置", value: "https://example.com:8443/mihomo/aaaa"},
		{label: "Base64 订阅", desc: "Base64 分享链接文档", value: "https://example.com:8443/v2ray/aaaa"},
	}
}

func TestLinksViewHidesValues(t *testing.T) {
	l := newLinksModel("订阅链接", sampleLinks())
	view := l.View(100, 40, theme.Dark(), i18n.Chinese, icons.Symbols())
	for _, item := range l.items {
		if strings.Contains(view, item.value) {
			t.Fatalf("view leaked value %q", item.value)
		}
	}
	if strings.Contains(view, "example.com:8443") {
		t.Fatal("the host must not be drawn on the cards")
	}
	if !strings.Contains(view, "完整 YAML 配置") {
		t.Fatal("view should show the card description")
	}
}

func TestLinksArrowsMoveCursor(t *testing.T) {
	l := newLinksModel("t", sampleLinks())
	l.View(100, 40, theme.Dark(), i18n.Chinese, icons.Symbols())

	l.handleKey(press(tea.KeyDown), i18n.Chinese)
	if l.cursor != 2 {
		t.Fatalf("down should move one grid row (clamped to the last item), got %d", l.cursor)
	}
	l.handleKey(press(tea.KeyUp), i18n.Chinese)
	if l.cursor != 0 {
		t.Fatalf("up should return to the first card, got %d", l.cursor)
	}
	l.handleKey(press(tea.KeyRight), i18n.Chinese)
	if l.cursor != 1 {
		t.Fatalf("right should move one card, got %d", l.cursor)
	}
}

func TestLinksGridScrollsToCursor(t *testing.T) {
	items := make([]linkItem, 9)
	for i := range items {
		items[i] = linkItem{label: "c", desc: "d", value: "v"}
	}
	l := newLinksModel("t", items)
	l.cursor = 8
	l.View(60, 20, theme.Dark(), i18n.Chinese, icons.Symbols())
	if l.topRow == 0 {
		t.Fatalf("an off-screen cursor should scroll the grid, topRow=%d", l.topRow)
	}
}

func TestLinksViewFitsPanelHeight(t *testing.T) {
	for _, h := range []int{20, 30, 40} {
		l := newLinksModel("t", sampleLinks())
		view := l.View(100, h, theme.Dark(), i18n.Chinese, icons.Symbols())
		if got := strings.Count(view, "\n") + 1; got != h {
			t.Fatalf("height %d: panel drew %d lines", h, got)
		}
	}
}

func TestLinksNumberKeySelects(t *testing.T) {
	l := newLinksModel("t", sampleLinks())
	l.View(100, 40, theme.Dark(), i18n.Chinese, icons.Symbols())
	cmd, done := l.handleKey(press('2'), i18n.Chinese)
	if done {
		t.Fatal("number key must not close the panel")
	}
	if cmd != nil || l.cursor != 1 || l.copied != -1 {
		t.Fatalf("digit 2 should select card 1 without copying, cursor=%d copied=%d", l.cursor, l.copied)
	}
}

func TestLinksEnterCopiesCursor(t *testing.T) {
	l := newLinksModel("t", sampleLinks())
	l.View(100, 40, theme.Dark(), i18n.Chinese, icons.Symbols())
	l.move(2)
	cmd, done := l.handleKey(press(tea.KeyEnter), i18n.Chinese)
	if done || cmd == nil || l.copied != 2 {
		t.Fatalf("enter should copy the cursor card, copied=%d done=%v", l.copied, done)
	}
}

func TestLinksEscCloses(t *testing.T) {
	l := newLinksModel("t", sampleLinks())
	l.View(100, 40, theme.Dark(), i18n.Chinese, icons.Symbols())
	if _, done := l.handleKey(press(tea.KeyEsc), i18n.Chinese); !done {
		t.Fatal("esc should close the panel")
	}
}

func TestLinksCopyAllShowsFeedback(t *testing.T) {
	l := newLinksModel("t", sampleLinks())
	l.View(100, 40, theme.Dark(), i18n.Chinese, icons.Symbols())
	cmd, done := l.handleKey(press('c'), i18n.Chinese)
	if done || cmd == nil {
		t.Fatalf("c should copy every link, done=%v cmd=%v", done, cmd)
	}
	if l.status != i18n.Chinese.T("links_copied_all") {
		t.Fatalf("copy-all should surface a visible status, got %q", l.status)
	}
	if !strings.Contains(l.View(100, 40, theme.Dark(), i18n.Chinese, icons.Symbols()), l.status) {
		t.Fatal("the copy-all status should be rendered in the header")
	}
	l.move(1)
	if l.status != "" {
		t.Fatalf("moving should clear the transient status, got %q", l.status)
	}
}

func TestLinksCardSignalsCopyByColor(t *testing.T) {
	lang := i18n.Chinese
	pal := theme.Dark()
	l := newLinksModel("t", sampleLinks())
	l.View(100, 40, pal, lang, icons.Symbols())

	card := func() string { return l.card(l.items[1], 1, 30, pal) }
	idle := card()
	if got := strings.Count(idle, "\n") + 1; got != linkCardHeight {
		t.Fatalf("card should be %d rows, got %d", linkCardHeight, got)
	}
	if !strings.Contains(idle, l.items[1].label) || !strings.Contains(idle, l.items[1].desc) {
		t.Fatalf("card should show its label and description, got %q", idle)
	}

	l.cursor = 1
	selected := card()
	l.copied = 1
	copied := card()
	if selected == idle {
		t.Fatal("the cursor card should render differently from an idle card")
	}
	if copied == selected || copied == idle {
		t.Fatal("a copied card should render unlike both idle and selected cards")
	}
	okCode := pal.Colored(pal.OK, "X")
	okPrefix := okCode[:strings.Index(okCode, "X")]
	if !strings.Contains(copied, okPrefix) {
		t.Fatal("a copied card should use the success color")
	}
	if strings.Contains(idle, okPrefix) {
		t.Fatal("an idle card should not use the success color")
	}
}

func TestLinksStrayKeyIsNoop(t *testing.T) {
	l := newLinksModel("t", sampleLinks())
	if _, done := l.handleKey(press('m'), i18n.Chinese); done {
		t.Fatal("a stray key must not close the panel")
	}
}

func TestGridDims(t *testing.T) {
	cases := []struct {
		width, count, cols int
	}{
		{100, 3, 3},
		{80, 3, 2},
		{60, 3, 2},
		{44, 3, 1},
		{100, 1, 1},
	}
	for _, c := range cases {
		cols, cardW := gridDims(c.width, c.count)
		if cols != c.cols {
			t.Errorf("gridDims(%d,%d) cols=%d, want %d", c.width, c.count, cols, c.cols)
		}
		if cardW < 8 {
			t.Errorf("gridDims(%d,%d) cardW=%d too small", c.width, c.count, cardW)
		}
	}
}

func TestLinksViewFitsWidth(t *testing.T) {
	for _, width := range []int{44, 60, 80, 100, 140} {
		l := newLinksModel("t", sampleLinks())
		view := l.View(width, 40, theme.Dark(), i18n.Chinese, icons.Symbols())
		if width == 100 {
			t.Logf("width 100 layout:\n%s", view)
		}
		for _, line := range strings.Split(view, "\n") {
			if lipgloss.Width(line) > width {
				t.Fatalf("width %d: line wider than viewport: %q", width, line)
			}
		}
	}
}
