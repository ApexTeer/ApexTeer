package tui

import (
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/MinimaxFlora/EasySB/internal/i18n"
	"github.com/MinimaxFlora/EasySB/internal/theme"
)

// formSubmit applies a submitted value. Returning an error keeps the form open
// and shows the message; a non-nil command is dispatched after the form closes.
type formSubmit func(a *App, value string) (tea.Cmd, error)

// formModel is one prompt drawn inside a page's card. It draws only what is inside the
// frame — the title is the card's and the keys are the frame's — so a prompt is the same
// page as the screen it was opened from and the panel never resizes when one opens.
type formModel struct {
	title  string
	prompt string
	hint   string
	input  textinput.Model
	submit formSubmit
	err    string
}

func newForm(title, prompt, initial, hint string, submit formSubmit) *formModel {
	in := textinput.New()
	in.Placeholder = ""
	in.CharLimit = 256
	in.SetValue(initial)
	in.CursorEnd()
	in.Focus()
	return &formModel{
		title:  title,
		prompt: prompt,
		hint:   hint,
		input:  in,
		submit: submit,
	}
}

func (f *formModel) update(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	f.input, cmd = f.input.Update(msg)
	return cmd
}

// resize sizes the field to the card's inner width, which is the room the card's own frame
// leaves for a body line. The line the field is drawn on holds a leading space, the "> "
// prompt, the field and the cursor's own cell, so the field gets what is left.
func (f *formModel) resize(inner int) {
	width := inner - 4
	if width < 8 {
		width = 8
	}
	f.input.SetWidth(width)
}

// body is the card's content: what the field asks for, the field itself and, when the
// last submit was rejected, the message that says why. inner is the room the card's frame
// leaves, so the field is never wider than the line it is drawn on.
func (f *formModel) body(pal theme.Palette, inner int) []string {
	f.resize(inner)
	out := []string{
		" " + pal.Value(theme.Truncate(f.prompt, maxInt(0, inner-1))),
		" " + f.input.View(),
	}
	if f.err != "" {
		out = append(out, "", " "+pal.Colored(pal.Err, f.err))
	}
	return out
}

// hintLine is the line the frame draws under the card: what the field expects, then the
// keys that close the form.
func (f *formModel) hintLine(lang i18n.Lang) string {
	keys := lang.T("form_confirm") + "  " + lang.T("form_cancel")
	if f.hint != "" {
		return f.hint + "    " + keys
	}
	return keys
}
