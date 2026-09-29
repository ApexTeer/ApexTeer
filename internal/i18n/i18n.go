// Package i18n holds the panel's bilingual string table. Every user-facing string
// lives here once, with its Chinese and English forms side by side, so a screen that
// words itself from this table cannot end up translated in one language only.
package i18n

import (
	"fmt"
	"math/rand"
)

// Lang is the interface language. The values are the codes the state file and the
// command line use.
type Lang string

// The two interface languages. Chinese is the default: an unrecognised or empty value
// resolves to it rather than to English.
const (
	Chinese Lang = "C"
	English Lang = "E"
)

// Parse reads a language from a flag or an environment variable, falling back to
// Chinese for anything it does not recognise.
func Parse(s string) Lang {
	switch s {
	case "E", "e", "en", "EN", "english":
		return English
	default:
		return Chinese
	}
}

// Name is the language's own name, so the choice reads correctly whichever language is
// active when it is shown.
func (l Lang) Name() string {
	if l == English {
		return "English"
	}
	return "简体中文"
}

// Code is the compact label for the language, for the places the full name does not fit.
func (l Lang) Code() string {
	if l == English {
		return "EN"
	}
	return "中文"
}

// Toggle returns the other language, which is how one key switches between them.
func (l Lang) Toggle() Lang {
	if l == English {
		return Chinese
	}
	return English
}

var (
	zhTable = map[string]string{}
	enTable = map[string]string{}
)

func init() {
	for _, e := range table {
		zhTable[e.key] = e.cn
		enTable[e.key] = e.en
	}
}

// T looks a key up in the active language. An unknown key is returned as itself, so a
// string that was never added shows up in the interface as its own key name - visible
// on the screen that needs it, rather than silently blank.
func (l Lang) T(key string) string {
	m := zhTable
	if l == English {
		m = enTable
	}
	if v, ok := m[key]; ok {
		return v
	}
	return key
}

// Format looks a key up and substitutes it as fmt.Sprintf would. The table entry is the
// format string, so a placeholder whose argument count no longer matches shows up in
// the interface rather than failing at build time.
func (l Lang) Format(key string, args ...any) string {
	return fmt.Sprintf(l.T(key), args...)
}

// Hitokoto returns one of the welcome card's lines at random, from the list belonging
// to the active language.
func (l Lang) Hitokoto() string {
	quotes := hitokotoCN
	if l == English {
		quotes = hitokotoEN
	}
	if len(quotes) == 0 {
		return ""
	}
	return quotes[rand.Intn(len(quotes))]
}
