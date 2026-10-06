package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EasySBTeam/EasySB/internal/i18n"
	"github.com/EasySBTeam/EasySB/internal/prefs"
	"github.com/EasySBTeam/EasySB/internal/theme"
)

// send feeds one key through the app the way the runtime does.
func send(a *App, key tea.KeyPressMsg) *App {
	m, _ := a.Update(key)
	return m.(*App)
}

// TestSystemScreenOpensFromMenu walks the real path: the entry is in the root
// menu, Enter opens the screen, and Esc leaves it without stranding the section.
func TestSystemScreenOpensFromMenu(t *testing.T) {
	a := newTestApp(t)
	index := -1
	for i, n := range a.current().nodes {
		if n.id == "system" {
			index = i
		}
	}
	if index < 0 {
		t.Fatal("the root menu has no system entry")
	}
	a.index = index
	a.enter()
	if a.system == nil {
		t.Fatal("Enter on the system entry did not open the screen")
	}
	if a.section != "system" {
		t.Fatalf("section is %q, want system", a.section)
	}
	if !strings.Contains(a.dashboard(), i18n.Chinese.T("panel_appearance")) {
		t.Fatal("the system screen does not show the appearance card")
	}
	send(a, press(tea.KeyEsc))
	if a.system != nil || a.section != "" {
		t.Fatalf("Esc left system=%v section=%q", a.system != nil, a.section)
	}
}

// TestSystemScreenSwitchesLook is the point of the screen: each choice applies to
// the very next frame, in both languages, with the theme locked out of automatic
// detection once it has been chosen by hand.
func TestSystemScreenSwitchesLook(t *testing.T) {
	a := newTestApp(t)
	a.openSystem()

	// A letter picks a skin directly.
	jade := a.skin.ID
	send(a, typeRune('d'))
	if a.skin.ID == jade {
		t.Fatalf("pressing d kept the skin at %q", jade)
	}
	if a.skin.ID != "graphite" {
		t.Fatalf("d selected %q, want the fourth skin graphite", a.skin.ID)
	}

	// Down and Enter walk the list, wrapping past the ends.
	before := a.skin.ID
	send(a, press(tea.KeyDown))
	send(a, press(tea.KeyEnter))
	if a.skin.ID == before {
		t.Fatalf("Enter kept the skin at %q", before)
	}
	if got := a.dashboard(); got == "" {
		t.Fatal("the screen rendered nothing after a skin switch")
	}

	// The palette toggle is manual from now on.
	dark := a.dark
	send(a, typeRune('t'))
	if a.dark == dark {
		t.Fatalf("t did not change the palette")
	}
	if a.themeAuto {
		t.Fatal("a manual palette choice should stop following the terminal")
	}
	send(a, press(tea.KeyUp))
	send(a, press(tea.KeyEnter))

	// The marker set toggles and labels itself correctly.
	set := a.iconSet.ID
	send(a, typeRune('i'))
	if a.iconSet.ID == set {
		t.Fatalf("i did not change the marker set")
	}
	for _, id := range []string{a.iconSet.ID, set} {
		if id != "symbols" && id != "ascii" {
			t.Fatalf("unexpected marker set %q", id)
		}
	}
}

// TestSystemScreenLeavesGlobalKeysAlone keeps the screen from swallowing the
// shortcuts pressed from everywhere else.
func TestSystemScreenLeavesGlobalKeysAlone(t *testing.T) {
	a := newTestApp(t)
	a.openSystem()
	lang := a.lang
	send(a, typeRune('l'))
	if a.lang == lang {
		t.Fatal("l should still switch the language from the system screen")
	}
	if a.system == nil {
		t.Fatal("l closed the system screen")
	}
	if len(a.stack) != 1 {
		t.Fatal("l changed the menu stack")
	}
	// The screen has no cursor to move, so the cursor keys and Enter belong to it, not to
	// the dashboard behind it. Right used to reach the global handler, which ran the
	// highlighted root entry: the menu changed under the screen and Esc then closed onto it.
	for _, key := range []rune{tea.KeyUp, tea.KeyDown, tea.KeyLeft, tea.KeyRight, tea.KeyEnter} {
		send(a, press(key))
		if len(a.stack) != 1 {
			t.Fatalf("key %d changed the menu stack while the system screen was open", key)
		}
		if a.system == nil {
			t.Fatalf("key %d closed the system screen", key)
		}
	}
}

// TestSystemScreenSurvivesTinyTerminals checks the body stays drawable when there
// is no room for cards at all.
func TestSystemScreenSurvivesTinyTerminals(t *testing.T) {
	for _, size := range [][2]int{{20, 6}, {32, 8}, {40, 10}, {60, 12}, {100, 34}} {
		a := newTestApp(t)
		a.width, a.height = size[0], size[1]
		a.sized = true
		a.openSystem()
		out := a.dashboard()
		if lines := strings.Count(out, "\n") + 1; lines != size[1] {
			t.Fatalf("%dx%d: drew %d lines", size[0], size[1], lines)
		}
	}
}

// TestSystemScreenRemembersChoices is the point of the preferences file: the look
// chosen here is the look the next run starts with, and a palette chosen by hand
// stops following the terminal for good.
func TestSystemScreenRemembersChoices(t *testing.T) {
	path := filepath.Join(t.TempDir(), "easysb-ui.conf")
	t.Setenv(prefs.PathEnv, path)
	for _, name := range []string{prefs.SkinEnv, prefs.ThemeEnv, prefs.IconsEnv, prefs.LangEnv} {
		t.Setenv(name, "")
	}

	a := New("test", i18n.Chinese)
	a.width, a.height, a.sized = 100, 34, true
	a.openSystem()
	send(a, typeRune('d')) // a skin
	send(a, typeRune('t')) // a palette
	send(a, typeRune('i')) // a marker set
	send(a, typeRune('l')) // the language

	saved := prefs.Load(path)
	if saved.Skin != a.skin.ID || saved.Icons != a.iconSet.ID {
		t.Fatalf("saved %+v, wanted skin %q and markers %q", saved, a.skin.ID, a.iconSet.ID)
	}
	if saved.Lang != string(a.lang) {
		t.Fatalf("saved language %q, wanted %q", saved.Lang, a.lang)
	}
	if saved.Theme == "" {
		t.Fatal("a palette chosen by hand should be remembered")
	}

	// A fresh start, the way main does it: preferences fill in what no flag or
	// exported variable answered.
	prefs.Load(path).Apply(os.Getenv, os.Setenv)
	next := New("test", i18n.Parse(os.Getenv(prefs.LangEnv)))
	if next.skin.ID != a.skin.ID {
		t.Fatalf("the next run started on %q, want %q", next.skin.ID, a.skin.ID)
	}
	if next.iconSet.ID != a.iconSet.ID {
		t.Fatalf("the next run used %q markers, want %q", next.iconSet.ID, a.iconSet.ID)
	}
	if next.dark != a.dark {
		t.Fatalf("the next run opened %v, want %v", next.dark, a.dark)
	}
	if next.lang != a.lang {
		t.Fatalf("the next run spoke %q, want %q", next.lang, a.lang)
	}
	if next.themeAuto {
		t.Fatal("a remembered palette must stop the automatic detection")
	}

	// An explicit choice still wins over the remembered one.
	t.Setenv(prefs.SkinEnv, "aurora")
	prefs.Load(path).Apply(os.Getenv, os.Setenv)
	if got := New("test", i18n.Chinese).skin.ID; got != "aurora" {
		t.Fatalf("the environment gave %q, want aurora", got)
	}
}

// TestSkinNotesExist keeps the switcher readable: a skin with no note would show
// as its name alone, which is exactly the information the screen exists to give.
func TestSkinNotesExist(t *testing.T) {
	for _, lang := range []i18n.Lang{i18n.Chinese, i18n.English} {
		for _, sk := range theme.Skins() {
			for _, key := range []string{"skin_" + sk.ID, "skin_" + sk.ID + "_note"} {
				if got := lang.T(key); got == key || got == "" {
					t.Errorf("%s is missing for %s", key, lang.Code())
				}
			}
		}
	}
}
