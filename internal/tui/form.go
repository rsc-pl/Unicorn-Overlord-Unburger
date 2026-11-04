package tui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbletea/v2"

	"uo-unburger/internal/paths"
)

// fieldSpec describes one editable text field.
type fieldSpec struct {
	key         string
	label       string
	placeholder string
	width       int
}

// field is one editable value. It wraps the shared bubbles text input so the
// editing behaviour (cursor, word keys, paste) matches the rest of the bubble
// ecosystem.
type field struct {
	key   string
	label string
	input textinput.Model
}

// form is a vertical stack of fields with its own focus ring.
type form struct {
	fields  []*field
	focus   int
	editing bool
	theme   *Theme
}

func newForm(theme *Theme, specs []fieldSpec) *form {
	f := &form{theme: theme}
	for _, spec := range specs {
		in := textinput.New()
		in.Prompt = "› "
		in.Placeholder = spec.placeholder
		w := spec.width
		if w <= 0 {
			w = 48
		}
		in.SetWidth(w)
		in.SetStyles(theme.textInputStyles())
		f.fields = append(f.fields, &field{key: spec.key, label: spec.label, input: in})
	}
	return f
}

// Begin enters edit mode and focuses the first field.
func (f *form) Begin() tea.Cmd {
	f.editing = true
	f.focus = 0
	return f.fields[0].input.Focus()
}

// End blurs every field and leaves edit mode.
func (f *form) End() {
	for _, fl := range f.fields {
		fl.input.Blur()
	}
	f.editing = false
}

// Next moves focus forward (wrapping).
func (f *form) Next() {
	if len(f.fields) == 0 {
		return
	}
	f.fields[f.focus].input.Blur()
	f.focus = (f.focus + 1) % len(f.fields)
	f.fields[f.focus].input.Focus()
}

// Prev moves focus backward (wrapping).
func (f *form) Prev() {
	if len(f.fields) == 0 {
		return
	}
	f.fields[f.focus].input.Blur()
	f.focus = (f.focus - 1 + len(f.fields)) % len(f.fields)
	f.fields[f.focus].input.Focus()
}

// Update feeds a key to the focused field, or moves focus for tab/shift+tab.
func (f *form) Update(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "tab", "down":
		f.Next()
		return nil
	case "shift+tab", "up":
		f.Prev()
		return nil
	}
	if len(f.fields) == 0 {
		return nil
	}
	var cmd tea.Cmd
	f.fields[f.focus].input, cmd = f.fields[f.focus].input.Update(msg)
	return cmd
}

// SetValues fills the fields from a key/value map.
func (f *form) SetValues(vals map[string]string) {
	for _, fl := range f.fields {
		if v, ok := vals[fl.key]; ok {
			fl.input.SetValue(v)
		}
	}
}

// All returns the current values.
func (f *form) All() map[string]string {
	out := make(map[string]string, len(f.fields))
	for _, fl := range f.fields {
		out[fl.key] = strings.TrimSpace(fl.input.Value())
	}
	return out
}

// Value returns one field value.
func (f *form) Value(key string) string {
	for _, fl := range f.fields {
		if fl.key == key {
			return strings.TrimSpace(fl.input.Value())
		}
	}
	return ""
}

// SetValue sets one field value.
func (f *form) SetValue(key, value string) {
	for _, fl := range f.fields {
		if fl.key == key {
			fl.input.SetValue(value)
			return
		}
	}
}

// View renders the form. In edit mode the focused field is rendered through the
// text input; otherwise every value is shown statically.
func (f *form) View(width int) string {
	var b strings.Builder
	for i, fl := range f.fields {
		label := fl.label
		if i == f.focus && f.editing {
			label = "› " + label
		} else {
			label = "  " + label
		}
		b.WriteString(f.theme.Render(f.theme.Desc, label))
		b.WriteString("\n")
		if i == f.focus && f.editing {
			b.WriteString("    " + fl.input.View())
		} else {
			val := fl.input.Value()
			if val == "" {
				b.WriteString("    " + f.theme.Render(f.theme.Muted, "(not set)"))
			} else {
				b.WriteString("    " + f.theme.Render(f.theme.Heading, truncate(paths.Display(val), max(16, width-6))))
			}
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
