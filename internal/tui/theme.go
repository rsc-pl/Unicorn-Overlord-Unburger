package tui

import (
	"image/color"
	"strings"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"uo-unburger/internal/appconfig"
)

// Theme carries every style the interface uses.
//
// When NoColor is set (the NO_COLOR convention, a dumb terminal, or a
// non-terminal output) every lipgloss style is the zero style, so rendering
// degrades to plain text with no escape sequences. Box-drawing borders are still
// drawn, and the selected row still carries a "›" marker, so the plain fallback
// stays readable rather than becoming an undifferentiated block of text.
type Theme struct {
	NoColor bool
	Dark    bool

	Title   lipgloss.Style
	Heading lipgloss.Style
	Sub     lipgloss.Style
	Muted   lipgloss.Style
	Accent  lipgloss.Style
	Good    lipgloss.Style
	Warn    lipgloss.Style
	Bad     lipgloss.Style
	Key     lipgloss.Style
	Desc    lipgloss.Style

	// Selected highlights a full table row; RowMarker styles the "›" cursor
	// column so a selected row remains visible even with colour disabled.
	RowSelected lipgloss.Style
	RowMarker   lipgloss.Style

	// Border styles panel frames. BorderFocused marks the pane that has focus.
	Border        lipgloss.Style
	BorderFocused lipgloss.Style

	// Table chrome.
	HeaderCell lipgloss.Style
	Cell       lipgloss.Style

	// Sidebar.
	SideTitle  lipgloss.Style
	SideItem   lipgloss.Style
	SideActive lipgloss.Style
	SideNumber lipgloss.Style
}

// NewTheme builds a theme for the requested mode ("auto", "light" or "dark").
// The env function supplies the environment (os.Getenv by default) so detection
// is testable.
func NewTheme(mode string, env func(string) string) *Theme {
	if env == nil {
		env = func(string) string { return "" }
	}
	if env("NO_COLOR") != "" || strings.EqualFold(env("TERM"), "dumb") {
		return &Theme{NoColor: true, Dark: true}
	}
	dark := mode != appconfig.ThemeLight
	if mode == appconfig.ThemeAuto {
		dark = true
		if v := strings.TrimSpace(env("COLORFGBG")); v != "" {
			// COLORFGBG is "fg;bg"; a bright background means a light terminal.
			parts := strings.Split(v, ";")
			switch last := parts[len(parts)-1]; last {
			case "7", "15", "8":
				dark = false
			case "0", "1", "2", "3", "4", "5", "6", "9":
				dark = true
			}
		}
	}
	selFg, selBg := pick(dark, "231", "16"), pick(dark, "62", "153")
	return &Theme{
		Dark:  dark,
		Title: lipgloss.NewStyle().Bold(true).Foreground(pick(dark, "213", "127")),
		// Heading must stay legible on both light and dark backgrounds; bold
		// with the terminal's own default foreground is the safest choice.
		Heading: lipgloss.NewStyle().Bold(true),
		Sub:     lipgloss.NewStyle().Bold(true).Foreground(pick(dark, "117", "25")),
		Muted:   lipgloss.NewStyle().Foreground(pick(dark, "244", "243")),
		Accent:  lipgloss.NewStyle().Foreground(pick(dark, "81", "26")),
		Good:    lipgloss.NewStyle().Foreground(pick(dark, "84", "28")),
		Warn:    lipgloss.NewStyle().Foreground(pick(dark, "221", "130")),
		Bad:     lipgloss.NewStyle().Foreground(pick(dark, "203", "124")),
		Key:     lipgloss.NewStyle().Bold(true).Foreground(pick(dark, "117", "25")),
		Desc:    lipgloss.NewStyle().Foreground(pick(dark, "245", "242")),

		RowSelected: lipgloss.NewStyle().Bold(true).Foreground(selFg).Background(selBg),
		RowMarker:   lipgloss.NewStyle().Bold(true).Foreground(pick(dark, "213", "127")),
		Border:      lipgloss.NewStyle().Foreground(pick(dark, "240", "250")),
		// The focused frame is a bright, high-contrast colour so focus is obvious
		// on both palettes.
		BorderFocused: lipgloss.NewStyle().Foreground(pick(dark, "81", "26")),
		HeaderCell:    lipgloss.NewStyle().Bold(true).Foreground(pick(dark, "117", "25")),
		Cell:          lipgloss.NewStyle(),
		SideTitle:     lipgloss.NewStyle().Bold(true).Foreground(pick(dark, "117", "25")),
		SideItem:      lipgloss.NewStyle().Foreground(pick(dark, "245", "240")),
		SideActive:    lipgloss.NewStyle().Bold(true).Foreground(selFg).Background(selBg),
		SideNumber:    lipgloss.NewStyle().Foreground(pick(dark, "240", "250")),
	}
}

func pick(dark bool, darkVal, lightVal string) color.Color {
	if dark {
		return lipgloss.Color(darkVal)
	}
	return lipgloss.Color(lightVal)
}

// Render applies a style unless color is disabled.
func (t *Theme) Render(style lipgloss.Style, text string) string {
	if t == nil || t.NoColor || text == "" {
		return text
	}
	return style.Render(text)
}

// textInputStyles returns plain styles in no-color mode so the widgets emit no
// escape sequences at all.
func (t *Theme) textInputStyles() textinput.Styles {
	if t == nil || t.NoColor {
		return textinput.Styles{}
	}
	st := textinput.DefaultStyles(t.Dark)
	st.Focused.Prompt = t.Accent
	st.Focused.Placeholder = t.Muted
	st.Blurred.Prompt = t.Muted
	st.Blurred.Text = t.Muted
	return st
}

// view builds a tea.View with the shared program settings. The alternate screen
// is used so the terminal is left exactly as it was found on exit.
func (t *Theme) view(content string, mouse bool) tea.View {
	var v tea.View
	v.SetContent(content)
	v.AltScreen = true
	v.WindowTitle = "uo-unburger"
	if mouse {
		// Cell motion reports wheel and click events without the constant motion
		// stream; only enabled when the user asked for it and colour is on.
		v.MouseMode = tea.MouseModeCellMotion
	}
	return v
}

// borderChars is the frame used by panels. Rounded corners are used on every
// terminal (including NO_COLOR) because they are plain characters, not styling.
var borderChars = struct {
	topLeft, topRight, bottomLeft, bottomRight, horizontal, vertical string
}{
	topLeft: "╭", topRight: "╮", bottomLeft: "╰", bottomRight: "╯", horizontal: "─", vertical: "│",
}

// panel draws a framed box of exactly width columns. The title is embedded in
// the top border. A width too small to frame degrades to truncated text so a tiny
// terminal never loses content to a border.
func (t *Theme) panel(title, body string, width int, focused bool) string {
	b := borderChars
	if width < 8 {
		return fitWidth(body, max(1, width))
	}
	inner := width - 4 // "│ " + inner + " │"
	borderStyle := t.Border
	if focused {
		borderStyle = t.BorderFocused
	}

	// Top border: "╭─ Title ─────╮"
	label := " " + string([]rune(title))
	if runeLen(title) == 0 {
		label = ""
	} else {
		label += " "
	}
	label = truncate(strings.ReplaceAll(label, "\n", " "), max(0, inner+1))
	dashCount := inner + 2 - 1 - runeLen(label)
	if dashCount < 0 {
		dashCount = 0
	}
	top := b.topLeft + b.horizontal + t.Render(borderStyle, label) +
		t.Render(borderStyle, strings.Repeat(b.horizontal, dashCount)) + b.topRight

	body = strings.ReplaceAll(body, "\t", "    ")
	var lines []string
	for _, ln := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
		lines = append(lines, t.Render(borderStyle, b.vertical)+" "+padTo(ln, inner)+" "+t.Render(borderStyle, b.vertical))
	}
	if len(lines) == 0 {
		lines = append(lines, t.Render(borderStyle, b.vertical)+" "+strings.Repeat(" ", inner)+" "+t.Render(borderStyle, b.vertical))
	}
	bottom := t.Render(borderStyle, b.bottomLeft+strings.Repeat(b.horizontal, inner+2)+b.bottomRight)
	return fitWidth(top+"\n"+strings.Join(lines, "\n")+"\n"+bottom, width)
}

// panelSized draws a framed box exactly width columns wide and height lines tall.
// Missing body lines are blank and extra lines are dropped, so the sidebar and the
// main pane keep a shared, stable frame while the terminal is resized.
func (t *Theme) panelSized(title, body string, width, height int, focused bool) string {
	if height < 3 || width < 8 {
		return fitHeight(fitWidth(body, max(1, width)), max(1, height))
	}
	inner := width - 4
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	bodyLines := height - 2
	if len(lines) < bodyLines {
		blank := strings.Repeat(" ", inner)
		for len(lines) < bodyLines {
			lines = append(lines, blank)
		}
	} else if len(lines) > bodyLines {
		lines = lines[:bodyLines]
	}
	var b strings.Builder
	b.WriteString(t.panelTop(title, inner, focused))
	b.WriteString("\n")
	for i, ln := range lines {
		b.WriteString(t.panelLine(ln, inner, focused))
		if i != len(lines)-1 {
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")
	b.WriteString(t.Render(t.panelBorderStyle(focused), borderChars.bottomLeft+strings.Repeat(borderChars.horizontal, inner+2)+borderChars.bottomRight))
	return fitHeight(b.String(), height)
}

// panelAuto frames a body and sizes the frame to the content.
func (t *Theme) panelAuto(title, body string, width int, focused bool) string {
	height := len(strings.Split(strings.TrimRight(body, "\n"), "\n")) + 2
	return t.panelSized(title, body, width, height, focused)
}

func (t *Theme) panelBorderStyle(focused bool) lipgloss.Style {
	if focused {
		return t.BorderFocused
	}
	return t.Border
}

func (t *Theme) panelTop(title string, inner int, focused bool) string {
	b := borderChars
	label := ""
	if runeLen(title) > 0 {
		label = " " + title + " "
	}
	label = padTo(label, min(runeLen(label), inner+1))
	dashCount := inner + 2 - 1 - runeLen(label)
	if dashCount < 0 {
		dashCount = 0
	}
	style := t.panelBorderStyle(focused)
	return b.topLeft + b.horizontal + t.Render(style, label) +
		t.Render(style, strings.Repeat(b.horizontal, dashCount)) + b.topRight
}

func (t *Theme) panelLine(ln string, inner int, focused bool) string {
	style := t.panelBorderStyle(focused)
	return t.Render(style, borderChars.vertical) + " " + padStyled(ln, inner) + " " + t.Render(style, borderChars.vertical)
}

// panelSpec is one framed block in a vertical stack.
type panelSpec struct {
	title  string
	body   string
	focus  bool
	height int // fixed line count; 0 means it shares the leftover space
}

// stackPanels lays panels out vertically inside exactly height lines, giving the
// flexible panels the leftover space (each at least three lines: two borders and
// one content line). It is how every screen keeps a consistent frame at any
// terminal size.
func (t *Theme) stackPanels(width, height int, specs []panelSpec) string {
	if len(specs) == 0 {
		return ""
	}
	fixed, flex := 0, 0
	for _, s := range specs {
		if s.height > 0 {
			fixed += s.height
		} else {
			flex++
		}
	}
	remaining := height - fixed
	heights := make([]int, len(specs))
	left := flex
	for i, s := range specs {
		if s.height > 0 {
			heights[i] = s.height
			continue
		}
		if left <= 0 {
			heights[i] = 3
			continue
		}
		share := remaining / left
		if share < 3 {
			share = 3
		}
		heights[i] = share
		remaining -= share
		left--
	}
	// If the fixed panels already exceed the budget, shrink from the bottom up.
	total := 0
	for _, h := range heights {
		total += h
	}
	for i := len(heights) - 1; i >= 0 && total > height; i-- {
		excess := total - height
		reduce := min(excess, heights[i]-3)
		if reduce < 0 {
			reduce = 0
		}
		heights[i] -= reduce
		total -= reduce
	}
	parts := make([]string, 0, len(specs))
	for i, s := range specs {
		if total > height {
			break
		}
		parts = append(parts, t.panelSized(s.title, s.body, width, heights[i], s.focus))
	}
	return fitHeight(lipgloss.JoinVertical(lipgloss.Left, parts...), height)
}
