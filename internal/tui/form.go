package tui

import (
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/MinimaxFlora/EasySB/internal/i18n"
	"github.com/MinimaxFlora/EasySB/internal/theme"
)

// dualFieldWidth is how many cells each half of a two-field prompt occupies.
// Six is more than any quota entry needs and keeps the two fields adjacent.
const dualFieldWidth = 4

// formSubmit applies a submitted value. Returning an error keeps the form open
// and shows the message; a non-nil command is dispatched after the form closes.
type formSubmit func(a *App, value string) (tea.Cmd, error)

// dualSubmit applies a submitted pair of field values, used by the quota prompt
// so that "10 GB 500 MB" is two numbers rather than one string to parse.
type dualSubmit func(a *App, first, second string) (tea.Cmd, error)

// formModel is one prompt drawn inside a page's card. It draws only what is inside the
// frame — the title is the card's and the keys are the frame's — so a prompt is the same
// page as the screen it was opened from and the panel never resizes when one opens.
//
// A prompt with hasSecond set draws two small fields with a unit after each, which is
// what the quota entry needs: one free-text box made the operator spell a unit and gave
// the panel a string to guess at.
type formModel struct {
	title     string
	prompt    string
	hint      string
	input     textinput.Model
	second    textinput.Model
	hasSecond bool
	focus     int
	submit    formSubmit
	dual      dualSubmit
	err       string
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

// newDualForm builds a prompt with two numeric fields. Left and right move between
// them and Enter submits from either, so a quota is typed as "10" GB and "500" MB
// instead of one string the panel has to interpret.
func newDualForm(title, prompt, first, second, hint string, submit dualSubmit) *formModel {
	f := newForm(title, prompt, "", hint, nil)
	f.hasSecond = true
	f.dual = submit
	f.input = unitInput(first)
	f.second = unitInput(second)
	f.input.Focus()
	return f
}

func unitInput(value string) textinput.Model {
	in := textinput.New()
	in.Placeholder = ""
	in.CharLimit = 6
	in.SetValue(value)
	in.CursorEnd()
	return in
}

// focusField moves the cursor between the two fields of a dual prompt.
func (f *formModel) focusField(i int) {
	f.focus = i
	if i == 0 {
		f.second.Blur()
		f.input.Focus()
		return
	}
	f.input.Blur()
	f.second.Focus()
}

func (f *formModel) update(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	if f.hasSecond {
		if key, ok := msg.(tea.KeyPressMsg); ok {
			switch key.String() {
			case "left":
				f.focusField(0)
				return nil
			case "right":
				f.focusField(1)
				return nil
			}
		}
		if f.focus == 1 {
			f.second, cmd = f.second.Update(msg)
			return cmd
		}
	}
	f.input, cmd = f.input.Update(msg)
	return cmd
}

// resize sizes the field to the card's inner width, which is the room the card's own frame
// leaves for a body line. The line the field is drawn on holds a leading space, the "> "
// prompt, the field and the cursor's own cell, so the field gets what is left.
func (f *formModel) resize(inner int) {
	if f.hasSecond {
		// Fixed narrow fields. Stretching them to the card width pushed the unit to
		// the far edge and ran the second field off the line entirely, so the two
		// numbers and their units stay together at any card width.
		f.input.SetWidth(dualFieldWidth)
		f.second.SetWidth(dualFieldWidth)
		return
	}
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
	}
	if f.hasSecond {
		out = append(out, " "+f.input.View()+" GB  "+f.second.View()+" MB")
	} else {
		out = append(out, " "+f.input.View())
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
