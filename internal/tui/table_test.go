package tui

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"uo-unburger/internal/appconfig"
)

func demoTable() *tableModel {
	tb := newTable("Demo",
		tableColumn{title: "Record", width: 12, min: 8},
		tableColumn{title: "Status", width: 10, min: 8},
		tableColumn{title: "Notes", width: 12, min: 6, flex: true},
	)
	tb.SetRows([]tableRow{
		{ID: "a", Cells: []string{"a.fms:1", "APPLIED", "clean"}},
		{ID: "b", Cells: []string{"b.fms:2", "REJECTED", "missing row"}},
		{ID: "c", Cells: []string{"c.fms:3", "UNRESOLVED", "no corpus entry"}},
	})
	return tb
}

// columnStarts returns the character index where each rendered column starts.
// The layout is deterministic: a two-column marker, then fixed-width cells
// separated by two spaces.
func columnStarts(tb *tableModel, width int) []int {
	widths, start, _ := tb.layout()
	out := []int{}
	pos := 2
	for i := start; i < len(widths); i++ {
		out = append(out, pos)
		pos += widths[i] + 2
	}
	return out
}

// TestTableColumnsAreAlignedAcrossEveryRow is the visual-regression guard for
// the requirement that tables have aligned columns: the header and every data
// row must place the same columns at the same character offsets.
func TestTableColumnsAreAlignedAcrossEveryRow(t *testing.T) {
	th := NewTheme(appconfig.ThemeAuto, func(string) string { return "1" })
	tb := demoTable()
	const width = 60
	out := tb.View(th, 8, width)
	lines := strings.Split(out, "\n")
	// line 0 is the meta line, line 1 the header, line 2 the underline.
	if len(lines) < 3+len(tb.rows) {
		t.Fatalf("table rendered %d lines, want header plus %d rows:\n%s", len(lines), len(tb.rows), out)
	}
	starts := columnStarts(tb, width)
	want := []string{"Record", "Status", "Notes"}
	for i, s := range starts {
		if got := string([]rune(lines[1])[s : s+len(want[i])]); got != want[i] {
			t.Fatalf("header column %d starts at %d with %q, want %q", i, s, got, want[i])
		}
	}
	for r, row := range tb.rows {
		line := []rune(lines[3+r])
		for c, s := range starts {
			cell := row.Cells[c]
			if got := string(line[s : s+len(cell)]); got != cell {
				t.Fatalf("row %d column %d = %q, want %q (line %q)", r, c, got, cell, string(line))
			}
		}
	}
}

// TestTableSelectionMarker: the selected row is marked with "›" and no other
// row is, in both colour and plain modes.
func TestTableSelectionMarker(t *testing.T) {
	for _, tc := range []struct {
		name string
		th   *Theme
	}{
		{"plain", NewTheme(appconfig.ThemeAuto, func(string) string { return "1" })},
		{"colour", NewTheme(appconfig.ThemeDark, noEnv)},
	} {
		tb := demoTable()
		out := tb.View(tc.th, 8, 60)
		lines := strings.Split(out, "\n")
		marked := 0
		for _, ln := range lines {
			if strings.Contains(ln, "›") {
				marked++
			}
		}
		if marked != 1 {
			t.Fatalf("%s: expected exactly one marked row, got %d:\n%s", tc.name, marked, out)
		}
		tb.Move(2)
		out = tb.View(tc.th, 8, 60)
		lines = strings.Split(out, "\n")
		if !strings.Contains(lines[3+2], "›") || strings.Contains(lines[3+0], "›") {
			t.Fatalf("%s: the marker did not follow the cursor:\n%s", tc.name, out)
		}
	}
}

// TestTableOverflowShowsAColumnWindow: when the columns do not fit, the table must
// keep the focused column visible, report it and never emit a line wider than the
// terminal.
func TestTableOverflowShowsAColumnWindow(t *testing.T) {
	th := NewTheme(appconfig.ThemeAuto, func(string) string { return "1" })
	tb := demoTable()
	out := tb.View(th, 8, 20)
	if !strings.Contains(out, "columns") {
		t.Fatalf("a narrow table must report its column window:\n%s", out)
	}
	for _, ln := range strings.Split(out, "\n") {
		if n := maxRunes(ln); n > 20 {
			t.Fatalf("line exceeds the table width: %q", ln)
		}
	}
	// Move the column focus and check the reported window changes.
	before := out
	for i := 0; i < 3; i++ {
		tb.HandleKey("right")
	}
	after := tb.View(th, 8, 20)
	if after == before {
		t.Fatalf("left/right must move the visible column window:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if tb.colFocus == 0 {
		t.Fatalf("the column focus should have advanced")
	}
}

// TestTableScrollingAndFiltering covers the vertical navigation contract.
func TestTableScrollingAndFiltering(t *testing.T) {
	tb := newTable("Many",
		tableColumn{title: "N", width: 8, min: 4, flex: true},
	)
	rows := make([]tableRow, 0, 30)
	for i := 0; i < 30; i++ {
		rows = append(rows, tableRow{ID: fmt.Sprintf("%02d", i), Cells: []string{fmt.Sprintf("row %02d", i)}})
	}
	tb.SetRows(rows)
	_ = tb.View(nil, 8, 40)
	if tb.maxRows() != 5 {
		t.Fatalf("maxRows at height 8 = %d, want 5", tb.maxRows())
	}
	tb.Move(29)
	if row, _ := tb.Selected(); row.ID != "29" {
		t.Fatalf("Moving to the end selected %q", row.ID)
	}
	if tb.offset == 0 {
		t.Fatalf("scrolling to the end must move the offset")
	}
	tb.Top()
	if row, _ := tb.Selected(); row.ID != "00" || tb.offset != 0 {
		t.Fatalf("Top = %q offset %d", row.ID, tb.offset)
	}
	tb.HandleKey("pgdown")
	if row, _ := tb.Selected(); row.ID != "05" {
		t.Fatalf("pgdown = %q, want 05", row.ID)
	}
	tb.HandleKey("/")
	tb.HandleKey("2")
	tb.HandleKey("9")
	if visible, total := tb.Count(); visible != 1 || total != 30 {
		t.Fatalf("filter = %d/%d rows, want 1/30", visible, total)
	}
	tb.HandleKey("esc")
	if visible, _ := tb.Count(); visible != 30 {
		t.Fatalf("esc should clear the filter, got %d rows", visible)
	}
}

// TestTableCellTruncationKeepsWidth: a cell wider than its column is truncated
// with an ellipsis at that cell only; the row keeps its exact width.
func TestTableCellTruncationKeepsWidth(t *testing.T) {
	th := NewTheme(appconfig.ThemeAuto, func(string) string { return "1" })
	tb := newTable("Long",
		tableColumn{title: "A", width: 10, min: 10},
		tableColumn{title: "B", width: 10, min: 10},
	)
	tb.SetRows([]tableRow{{ID: "x", Cells: []string{strings.Repeat("a", 40), "short"}}})
	out := tb.View(th, 5, 30)
	for i, ln := range strings.Split(out, "\n") {
		if n := maxRunes(ln); n > 30 {
			t.Fatalf("line %d is %d columns wide: %q", i, n, ln)
		}
	}
	if !strings.Contains(out, "…") {
		t.Fatalf("a truncated cell should show an ellipsis:\n%s", out)
	}
	if !strings.Contains(out, "short") {
		t.Fatalf("the second column must still be rendered:\n%s", out)
	}
}

// TestTableCellStatusStylingUsesColourButNotInPlainMode checks the status column
// styling hook on both palettes.
func TestTableCellStatusStylingUsesColourButNotInPlainMode(t *testing.T) {
	tb := newTable("Status", tableColumn{title: "S", width: 10, min: 10})
	tb.SetRows([]tableRow{{ID: "a", Cells: []string{"REJECTED"}}})
	dark := NewTheme(appconfig.ThemeDark, noEnv)
	tb.cellStyle = func(row tableRow, col int) lipgloss.Style {
		return statusStyle(dark, row.Cells[col])
	}
	coloured := tb.View(dark, 5, 20)
	if !strings.Contains(coloured, "\x1b[") {
		t.Fatalf("colour mode should style the status cell")
	}
	plain := NewTheme(appconfig.ThemeAuto, func(string) string { return "1" })
	plainOut := tb.View(plain, 5, 20)
	if strings.Contains(plainOut, "\x1b[") {
		t.Fatalf("plain mode must not emit escape sequences: %q", plainOut)
	}
	if !strings.Contains(plainOut, "REJECTED") {
		t.Fatalf("plain mode must keep the status text: %q", plainOut)
	}
}

// TestTableHandlesShortRowsAndEmptyState: a short row is padded and an empty
// table explains itself instead of crashing.
func TestTableHandlesShortRowsAndEmptyState(t *testing.T) {
	th := NewTheme(appconfig.ThemeAuto, func(string) string { return "1" })
	tb := newTable("Sparse", tableColumn{title: "A", width: 6, min: 6}, tableColumn{title: "B", width: 6, min: 6})
	tb.SetRows([]tableRow{{ID: "short", Cells: []string{"one"}}})
	if out := tb.View(th, 5, 20); !strings.Contains(out, "one") {
		t.Fatalf("a short row must still render: %q", out)
	}
	tb.SetRows(nil)
	if out := tb.View(th, 5, 20); !strings.Contains(out, "no rows") {
		t.Fatalf("an empty table must explain itself: %q", out)
	}
	if _, ok := tb.Selected(); ok {
		t.Fatalf("an empty table has no selection")
	}
}

// TestStyledWidthMath covers the ANSI helpers the layout depends on: a styled line
// must be measured and padded by its *visible* width, never by its byte length.
func TestStyledWidthMath(t *testing.T) {
	th := NewTheme(appconfig.ThemeDark, noEnv)
	styled := th.Render(th.Muted, "hello")
	if visLen(styled) != 5 {
		t.Fatalf("visLen(%q) = %d, want 5", styled, visLen(styled))
	}
	if got := padStyled(styled, 9); visLen(got) != 9 {
		t.Fatalf("padStyled produced %d visible columns, want 9: %q", visLen(got), got)
	}
	if got := padStyled(styled, 3); visLen(got) != 3 {
		t.Fatalf("padStyled truncated to %d visible columns, want 3: %q", visLen(got), got)
	}
	if stripANSI(styled) != "hello" {
		t.Fatalf("stripANSI(%q) = %q", styled, stripANSI(styled))
	}
	bold := th.Render(th.Title, "uo-unburger")
	if visLen(bold) != 11 {
		t.Fatalf("visLen(%q) = %d, want 11", bold, visLen(bold))
	}
	box := strings.Repeat("─", 7)
	if got := padTo(box, 7); got != box {
		t.Fatalf("padTo changed a box-drawing line: %q", got)
	}
}

// TestStyledTruncationKeepsTheEllipsis: a styled line cut to the terminal width
// must end with the ellipsis (the same as a plain line), so the reader knows text
// was shortened.
func TestStyledTruncationKeepsTheEllipsis(t *testing.T) {
	th := NewTheme(appconfig.ThemeDark, noEnv)
	styled := th.Render(th.Desc, "  Showing: all records   (← → to change) more text")
	got := truncatePlainStyled(styled, 20)
	if visLen(got) != 20 {
		t.Fatalf("truncated width = %d, want 20: %q", visLen(got), got)
	}
	if !strings.HasSuffix(stripANSI(got), "…") {
		t.Fatalf("a truncated styled line must keep the ellipsis: %q", stripANSI(got))
	}
	plain := truncate("  Showing: all records   (← → to change) more text", 20)
	if stripANSI(got) != plain {
		t.Fatalf("styled truncation %q differs from plain %q", stripANSI(got), plain)
	}
	if same := truncatePlainStyled(styled, 200); same != styled {
		t.Fatalf("a line that fits must be returned unchanged")
	}
}

// TestSelectionHighlightIsVisibleInBothPalettes: light and dark palettes must
// both paint the selected row with a background *and* keep the "›" marker, so the
// highlight can never become invisible text.
func TestSelectionHighlightIsVisibleInBothPalettes(t *testing.T) {
	for _, mode := range []string{appconfig.ThemeDark, appconfig.ThemeLight} {
		th := NewTheme(mode, noEnv)
		if th.RowSelected.GetBackground() == nil {
			t.Fatalf("%s: the selected row must carry a background colour", mode)
		}
		if th.RowSelected.GetForeground() == nil {
			t.Fatalf("%s: the selected row must carry a foreground colour", mode)
		}
		tb := demoTable()
		out := tb.View(th, 8, 60)
		var sel string
		for _, ln := range strings.Split(out, "\n") {
			if strings.Contains(ln, "›") {
				sel = ln
			}
		}
		if sel == "" || !strings.Contains(sel, "\x1b[") {
			t.Fatalf("%s: the selected row is not styled and marked: %q", mode, sel)
		}
	}
}
