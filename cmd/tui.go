package cmd

import (
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/EasySBTeam/EasySB/internal/i18n"
	"github.com/EasySBTeam/EasySB/internal/prefs"
	"github.com/EasySBTeam/EasySB/internal/tui"
)

// runTUI is the default mode: the full-screen panel, or one rendered frame when
// --render was given. Remembered interface choices fill in what the command line
// left open, and a v5 deployment is upgraded before the panel reads either store,
// so the render, the menus and the first apply all see the explicit node model.
func runTUI(langFlag, iconsFlag, themeFlag, skinFlag string, render bool, screen string, width, height int) {
	prefs.Load(prefs.Path()).Apply(os.Getenv, os.Setenv)

	applyIcons(iconsFlag)
	applyTheme(themeFlag)
	applySkin(skinFlag)
	lang := i18n.Parse(firstNonEmpty(langFlag, os.Getenv("EASYSB_LANG")))

	migrateStores()

	app := tui.New(resolveVersion(), lang)

	if render {
		fmt.Println(app.SnapshotScreen(screen, width, height))
		return
	}

	program := tea.NewProgram(app)
	if _, err := program.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// applyIcons forwards the icon mode to the TUI. "on" selects the Unicode symbol
// palette, "off" the ASCII fallback; the legacy "nerd" value behaves like "on".
func applyIcons(mode string) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "on", "1", "true", "yes", "symbols", "unicode", "nerd":
		_ = os.Setenv("EASYSB_ICONS", "symbols")
	case "off", "0", "false", "no", "ascii", "plain":
		_ = os.Setenv("EASYSB_ICONS", "ascii")
	}
}

// applyTheme forwards the requested palette to the TUI. An empty or unknown
// value leaves EASYSB_THEME untouched so the TUI keeps auto-detecting.
func applyTheme(mode string) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "auto", "dark", "light":
		_ = os.Setenv("EASYSB_THEME", strings.ToLower(strings.TrimSpace(mode)))
	}
}

// applySkin passes the requested look on to the TUI through the environment,
// the same way --theme does. An unknown name is ignored, so the panel falls back
// to the default skin instead of refusing to start.
func applySkin(name string) {
	if strings.TrimSpace(name) == "" {
		return
	}
	_ = os.Setenv("EASYSB_SKIN", strings.TrimSpace(name))
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
