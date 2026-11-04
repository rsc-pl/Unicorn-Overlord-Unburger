package tui

import (
	"fmt"
	"strings"
	"unicode"

	"charm.land/lipgloss/v2"
)

// tableAlign selects the horizontal alignment of a column's cells.
type tableAlign int

const (
	alignLeft tableAlign = iota
	alignRight
)

// tableColumn describes one column. A flexible column absorbs the space left
// over after the fixed columns; when the table is too wide for the terminal the
// columns fall back to their minimum width and the view scrolls horizontally a
// column at a time (the focused column always stays visible).
type tableColumn struct {
	title string
	width int // fixed width, or the starting width of a flexible column
	min   int // minimum width; defaults to min(width, 8)
	flex  bool
	align tableAlign
}

// tableRow is one selectable row. Cells must line up with the columns; a short
// row is padded with empty cells.
type tableRow struct {
	ID    string
	Cells []string
	Data  any
}

// tableModel is a self-contained, aligned-column table with a header, a
// selectable row, vertical scrolling, an in-place filter and horizontal
// overflow handling.
//
// It exists instead of charm.land/bubbles/v2/table because that component, while
// it does render aligned rows, offers no way to keep a selection marker visible
// without colour, no status-column styling hook, no horizontal overflow, and its
// viewport-offset arithmetic cannot be driven from the outside; the M11B
// requirement is an interactive table with exactly those properties. The rest of
// the interface still uses bubbles/v2 (spinner, textinput) and the table API
// mirrors the bubbles conventions (SetRows, MoveUp/MoveDown, SelectedRow, Focus).
type tableModel struct {
	title    string
	columns  []tableColumn
	rows     []tableRow
	visible  []int
	cursor   int
	offset   int
	height   int
	width    int
	filter   string
	filterOn bool

	emptyMsg   string
	hideMeta   bool
	hideHeader bool
	colFocus   int
	noFilter   bool
	cellStyle  func(row tableRow, col int) lipgloss.Style
}

func newTable(title string, cols ...tableColumn) *tableModel {
	t := &tableModel{title: title, columns: cols, height: 8, width: 80, emptyMsg: "(no rows)"}
	return t
}

// SetRows replaces the rows and re-applies the active filter.
func (m *tableModel) SetRows(rows []tableRow) {
	m.rows = rows
	m.applyFilter()
}

func (m *tableModel) applyFilter() {
	m.visible = m.visible[:0]
	needle := strings.ToLower(strings.TrimSpace(m.filter))
	for i, r := range m.rows {
		if needle == "" || strings.Contains(strings.ToLower(r.ID+" "+strings.Join(r.Cells, " ")), needle) {
			m.visible = append(m.visible, i)
		}
	}
	if m.cursor >= len(m.visible) {
		m.cursor = max(0, len(m.visible)-1)
	}
	m.clampOffset()
}

// chrome reports how many lines the fixed table chrome occupies.
func (m *tableModel) chrome() int {
	n := 0
	if !m.hideMeta {
		n++
	}
	if !m.hideHeader {
		n += 2
	}
	return n
}

func (m *tableModel) maxRows() int { return max(1, m.height-m.chrome()) }

func (m *tableModel) clampOffset() {
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+m.maxRows() {
		m.offset = m.cursor - m.maxRows() + 1
	}
	if m.offset < 0 {
		m.offset = 0
	}
	if m.offset > max(0, len(m.visible)-m.maxRows()) {
		m.offset = max(0, len(m.visible)-m.maxRows())
	}
}

// Move shifts the cursor by delta rows.
func (m *tableModel) Move(delta int) {
	if len(m.visible) == 0 {
		return
	}
	m.cursor += delta
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.visible) {
		m.cursor = len(m.visible) - 1
	}
	m.clampOffset()
}

// Top and Bottom jump to the first and last row.
func (m *tableModel) Top() {
	m.cursor = 0
	m.clampOffset()
}

func (m *tableModel) Bottom() {
	m.cursor = max(0, len(m.visible)-1)
	m.clampOffset()
}

// Selected returns the highlighted row.
func (m *tableModel) Selected() (tableRow, bool) {
	if len(m.visible) == 0 || m.cursor < 0 || m.cursor >= len(m.visible) {
		return tableRow{}, false
	}
	return m.rows[m.visible[m.cursor]], true
}

// Count reports the number of visible rows and the total row count.
func (m *tableModel) Count() (visible, total int) { return len(m.visible), len(m.rows) }

// SetCursor positions the cursor on a specific row ID when it is visible.
func (m *tableModel) SetCursor(id string) {
	for vi, ri := range m.visible {
		if m.rows[ri].ID == id {
			m.cursor = vi
			m.clampOffset()
			return
		}
	}
}

// HandleKey processes navigation and filtering keys. It reports whether the key
// was consumed.
func (m *tableModel) HandleKey(key string) bool {
	if m.filterOn {
		switch key {
		case "esc":
			m.filterOn = false
			m.filter = ""
			m.applyFilter()
			return true
		case "enter":
			m.filterOn = false
			return true
		case "backspace":
			if m.filter != "" {
				r := []rune(m.filter)
				m.filter = string(r[:len(r)-1])
				m.applyFilter()
			}
			return true
		}
		if r := []rune(key); len(r) == 1 && unicode.IsPrint(r[0]) {
			m.filter += key
			m.applyFilter()
			return true
		}
		return true
	}
	switch key {
	case "j", "down":
		m.Move(1)
	case "k", "up":
		m.Move(-1)
	case "pgdown":
		m.Move(m.maxRows())
	case "pgup":
		m.Move(-m.maxRows())
	case "home", "g":
		m.Top()
	case "end", "G":
		m.Bottom()
	case "/":
		if m.noFilter {
			return false
		}
		m.filterOn = true
	case "left":
		if !m.overflows() {
			return false
		}
		if m.colFocus > 0 {
			m.colFocus--
		}
	case "right":
		if !m.overflows() {
			return false
		}
		if m.colFocus < len(m.columns)-1 {
			m.colFocus++
		}
	default:
		return false
	}
	return true
}

// overflows reports whether the columns do not fit the current width.
func (m *tableModel) overflows() bool {
	if len(m.columns) == 0 {
		return false
	}
	return m.totalWidth(m.desiredWidths(), 0, len(m.columns)) > m.width
}

// desiredWidths is the layout used when the table fits: fixed columns keep their
// width and flexible columns start at their minimum.
func (m *tableModel) desiredWidths() []int {
	ws := make([]int, len(m.columns))
	for i, c := range m.columns {
		w := c.width
		if c.flex && c.min > 0 && w < c.min {
			w = c.min
		}
		if w <= 0 {
			w = 8
		}
		ws[i] = w
	}
	return ws
}

// minWidths is the layout used when the table is too wide: every column shrinks
// to its minimum so as many as possible stay visible.
func (m *tableModel) minWidths() []int {
	ws := make([]int, len(m.columns))
	for i, c := range m.columns {
		minw := c.min
		if minw <= 0 {
			minw = min(c.width, 8)
		}
		if c.width < minw {
			minw = c.width
		}
		if minw < 1 {
			minw = 1
		}
		ws[i] = minw
	}
	return ws
}

// totalWidth is the rendered width of columns [start,end) including the marker
// column and the two-space separators.
func (m *tableModel) totalWidth(ws []int, start, end int) int {
	w := 2
	for i := start; i < end; i++ {
		w += ws[i]
		if i > start {
			w += 2
		}
	}
	return w
}

// layout returns the widths actually used and the visible column window.
func (m *tableModel) layout() (widths []int, start, end int) {
	n := len(m.columns)
	if n == 0 {
		return nil, 0, 0
	}
	desired := m.desiredWidths()
	if m.totalWidth(desired, 0, n) <= m.width {
		widths = make([]int, n)
		copy(widths, desired)
		// Grow flexible columns to fill the table width.
		free := m.width - m.totalWidth(widths, 0, n)
		if free > 0 {
			flex := 0
			for i, c := range m.columns {
				if c.flex {
					flex++
					_ = i
				}
			}
			switch {
			case flex > 0:
				give := free / flex
				for i, c := range m.columns {
					if !c.flex {
						continue
					}
					widths[i] += give
				}
				rest := free - give*flex
				for i := n - 1; i >= 0 && rest > 0; i-- {
					if m.columns[i].flex {
						widths[i] += rest
						rest = 0
					}
				}
			default:
				widths[n-1] += free
			}
		}
		return widths, 0, n
	}

	// Overflow: use the minimum widths and show a window around the focused
	// column.
	widths = m.minWidths()
	if m.colFocus >= n {
		m.colFocus = n - 1
	}
	start = 0
	for start < m.colFocus && m.totalWidth(widths, start, m.colFocus+1) > m.width {
		start++
	}
	end = start + 1
	for end < n && m.totalWidth(widths, start, end+1) <= m.width {
		end++
	}
	return widths, start, end
}

// View renders the table inside a box of the given height and width.
func (m *tableModel) View(t *Theme, height, width int) string {
	if t == nil {
		t = &Theme{NoColor: true}
	}
	m.height = max(4, height)
	m.width = max(12, width)
	m.clampOffset()

	widths, start, end := m.layout()
	visible, total := m.Count()

	var b strings.Builder
	if !m.hideMeta {
		// Context line: the column window comes first because it is the most
		// important thing to know when the table is too wide.
		var meta []string
		if end-start < len(m.columns) {
			meta = append(meta, fmt.Sprintf("columns %d–%d of %d (←/→)", start+1, end, len(m.columns)))
		}
		if total == 0 {
			meta = append(meta, "no rows")
		} else {
			meta = append(meta, fmt.Sprintf("row %d/%d", min(m.cursor+1, visible), max(visible, 1)))
		}
		if m.filter != "" {
			meta = append(meta, fmt.Sprintf("filter %q", m.filter))
		}
		if m.filterOn {
			meta = append(meta, "type to filter · enter applies · esc clears")
		} else if !m.noFilter && total > 0 {
			meta = append(meta, "/ filters")
		}
		b.WriteString(t.Render(t.Muted, padTo("  "+strings.Join(meta, "  ·  "), m.width)))
		b.WriteString("\n")
	}

	if !m.hideHeader {
		header := overflowMarker(start > 0, end < len(m.columns))
		for i := start; i < end; i++ {
			if i > start {
				header += "  "
			}
			header += padTo(m.columns[i].title, widths[i])
		}
		b.WriteString(t.Render(t.HeaderCell, padTo(header, m.width)))
		b.WriteString("\n")
		b.WriteString(t.Render(t.Border, strings.Repeat("─", m.width)))
		b.WriteString("\n")
	}

	if visible == 0 {
		b.WriteString(t.Render(t.Muted, padTo("  "+truncate(m.emptyMsg, max(4, m.width-2)), m.width)))
		return b.String()
	}

	last := min(len(m.visible), m.offset+m.maxRows())
	for vi := m.offset; vi < last; vi++ {
		row := m.rows[m.visible[vi]]
		selected := vi == m.cursor
		var line strings.Builder
		if selected {
			line.WriteString(t.Render(t.RowMarker, "› "))
		} else {
			line.WriteString("  ")
		}
		if selected || t.NoColor {
			// Render the selected row as one plain, padded string so the marker
			// (plain fallback) and the highlight (colour) both span the row.
			var plain strings.Builder
			for i := start; i < end; i++ {
				if i > start {
					plain.WriteString("  ")
				}
				plain.WriteString(m.padCell(row, i, widths[i]))
			}
			text := padTo(plain.String(), max(0, m.width-2))
			if selected {
				line.WriteString(t.Render(t.RowSelected, text))
			} else {
				line.WriteString(text)
			}
		} else {
			for i := start; i < end; i++ {
				if i > start {
					line.WriteString("  ")
				}
				style := t.Cell
				if m.cellStyle != nil {
					style = m.cellStyle(row, i)
				}
				line.WriteString(t.Render(style, m.padCell(row, i, widths[i])))
			}
		}
		b.WriteString(line.String())
		if vi != last-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// overflowMarker is the two-column gutter that shows whether columns are hidden
// to the left, the right or both.
func overflowMarker(left, right bool) string {
	switch {
	case left && right:
		return "‹›"
	case left:
		return "‹ "
	case right:
		return " ›"
	}
	return "  "
}

// padCell pads a row cell to the column width using the column's alignment.
func (m *tableModel) padCell(row tableRow, i, width int) string {
	if i < len(m.columns) && m.columns[i].align == alignRight {
		return padLeft(m.cell(row, i), width)
	}
	return padTo(m.cell(row, i), width)
}

// cell returns a row cell, tolerating short rows.
func (m *tableModel) cell(row tableRow, i int) string {
	if i < 0 || i >= len(row.Cells) {
		return ""
	}
	return row.Cells[i]
}
