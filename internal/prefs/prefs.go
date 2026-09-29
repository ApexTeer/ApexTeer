// Package prefs stores the interface choices the panel would otherwise forget
// between runs: the skin, the palette, the marker set and the language. They live
// next to the node state but in their own file, because they describe this
// terminal, not the node.
package prefs

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/MinimaxFlora/EasySB/internal/sysinfo"
)

const (
	// PathEnv overrides the preferences file path. The tests and a second panel
	// install use it.
	PathEnv = "EASYSB_UI_CONF"

	// SkinEnv selects the skin.
	SkinEnv = "EASYSB_SKIN"
	// ThemeEnv selects the palette.
	ThemeEnv = "EASYSB_THEME"
	// IconsEnv selects the glyph set: the Unicode symbols, or the ASCII fallback.
	IconsEnv = "EASYSB_ICONS"
	// LangEnv selects the interface language. A command line flag sets it before the
	// panel reads it, so a flag still wins over what was remembered.
	LangEnv = "EASYSB_LANG"

	// BoardEnv carries the toolbox 看板's selection, so the entry points that build the panel
	// from flags see the same choice the interface remembers.
	BoardEnv = "EASYSB_BOARD"
)

// Prefs is what the panel remembers. Empty values mean "no choice yet", which
// leaves detection and defaults in charge.
type Prefs struct {
	Skin  string
	Theme string
	Icons string
	Lang  string
	// Board is the toolbox 看板 selection: the tool ids whose results are shown, as a
	// comma-separated list. Empty means the panel has never been asked, so the entries that
	// are worth a board row by default are shown; BoardNone means the operator turned all of
	// them off, which is a choice and not an empty file.
	Board string
}

// binding ties one choice together: its name in the file, the variable it reaches
// the panel through, and how to read and write it. Reading, writing and applying
// all walk this one table, so a key cannot drift apart from its field.
type binding struct {
	key string
	env string
	get func(Prefs) string
	set func(*Prefs, string)
}

var bindings = []binding{
	{"SKIN", SkinEnv, func(p Prefs) string { return p.Skin }, func(p *Prefs, v string) { p.Skin = v }},
	{"THEME", ThemeEnv, func(p Prefs) string { return p.Theme }, func(p *Prefs, v string) { p.Theme = v }},
	{"ICONS", IconsEnv, func(p Prefs) string { return p.Icons }, func(p *Prefs, v string) { p.Icons = v }},
	{"LANG", LangEnv, func(p Prefs) string { return p.Lang }, func(p *Prefs, v string) { p.Lang = v }},
	{"BOARD", BoardEnv, func(p Prefs) string { return p.Board }, func(p *Prefs, v string) { p.Board = v }},
}

// Path returns the preferences file location.
func Path() string {
	if p := strings.TrimSpace(os.Getenv(PathEnv)); p != "" {
		return p
	}
	return filepath.Join(sysinfo.WorkDir, "easysb-ui.conf")
}

// Load reads the preferences file. A missing or unreadable file is not an error:
// it means the panel has never been asked to remember anything.
func Load(path string) Prefs {
	var p Prefs
	data, err := os.ReadFile(path)
	if err != nil {
		return p
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := splitLine(line)
		if !ok {
			continue
		}
		for _, b := range bindings {
			if b.key == key {
				b.set(&p, value)
			}
		}
	}
	return p
}

// Save writes the preferences file atomically, so an interrupted write cannot
// leave a half-written file that the next start would read as no preferences at
// all. The directory is created when missing: the panel can be run before the
// installer has made it.
func (p Prefs) Save(path string) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	var b strings.Builder
	b.WriteString("# EasySB interface preferences\n")
	for _, binding := range bindings {
		value := strings.TrimSpace(binding.get(p))
		if value == "" {
			continue
		}
		// The values come from a closed set in the interface, but the file is on
		// disk and editable, so a stray quote or newline must not be able to turn
		// one setting into two.
		value = strings.NewReplacer("\n", " ", "\r", " ", "\"", "").Replace(value)
		b.WriteString(binding.key + "=\"" + value + "\"\n")
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".easysb-ui-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Apply fills in the variables the caller has not set yet, so remembered choices
// act as defaults rather than overrides. It is called once, from main, after the
// flags have been parsed: a choice made on the command line or exported in the
// shell has already answered those variables and is left alone.
func (p Prefs) Apply(getenv func(string) string, setenv func(string, string) error) {
	for _, binding := range bindings {
		if strings.TrimSpace(getenv(binding.env)) != "" {
			continue
		}
		if value := strings.TrimSpace(binding.get(p)); value != "" {
			_ = setenv(binding.env, value)
		}
	}
}

// splitLine parses one KEY="value" line.
func splitLine(line string) (string, string, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	key, value, ok := strings.Cut(line, "=")
	if !ok {
		return "", "", false
	}
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	value = strings.Trim(value, "\"")
	if key == "" || value == "" {
		return "", "", false
	}
	return key, value, true
}

// BoardNone is what the board selection says when the operator turned every entry off. An
// empty value cannot say it, because an empty value is what a preference that was never set
// looks like, and those two states mean different things: defaults, or nothing at all.
const BoardNone = "none"
