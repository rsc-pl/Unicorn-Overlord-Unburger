package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"charm.land/bubbletea/v2"

	"uo-unburger/internal/build"
	"uo-unburger/internal/importer"
)

// translation record view modes, chosen with the left/right arrow keys.
var translationModes = []string{"all records", "problems only", "overrides only"}

// viewTranslations renders the Translations screen.
func (m *Model) viewTranslations(w, h int) string {
	inner := max(4, w-4)
	if m.trans.form.editing {
		return m.theme.stackPanels(w, h, []panelSpec{
			{title: "Edit locations", body: m.trans.form.View(inner), focus: true},
			{title: "Keys", body: m.theme.Render(m.theme.Desc, "  tab: next field   enter: apply and run the check   esc: done (values kept)"), height: 3},
		})
	}
	table := m.trans.table
	table.width = inner
	table.SetRows(m.translationRows())
	tableHeight := max(5, h-14)

	var detail strings.Builder
	if row, ok := table.Selected(); ok {
		if rec, ok := row.Data.(importer.RecordResult); ok {
			detail.WriteString(fmt.Sprintf("  Record    %s\n", rec.RecordID))
			detail.WriteString(fmt.Sprintf("  Status    %s\n", rec.Status))
			notes := "none"
			if rec.TokenFlag != "" {
				notes = string(rec.TokenFlag)
			}
			detail.WriteString(fmt.Sprintf("  Token     %s\n", notes))
			errs := "none"
			if len(rec.Errors) > 0 {
				errs = strings.Join(rec.Errors, "; ")
			}
			detail.WriteString("  Errors    " + truncate(errs, inner-14))
		}
	} else {
		detail.WriteString(m.theme.Render(m.theme.Muted, "  No record selected."))
	}

	actions := m.actions()
	actionBody := ""
	if actions != nil {
		actions.width = inner
		actionBody = actions.View(m.theme, max(1, len(m.actionRows())), inner)
	}
	return m.theme.stackPanels(w, h, []panelSpec{
		{title: "Translation files", body: m.translationSummary(inner), height: 6},
		{title: "Records", body: table.View(m.theme, tableHeight, inner), height: tableHeight, focus: m.paneKind() == paneMain},
		{title: "Selected record", body: strings.TrimRight(detail.String(), "\n"), height: 5},
		{title: "Actions", body: actionBody, focus: m.paneKind() == paneActions},
	})
}

// translationSummary reports the public translation files and the last check.
// The advanced reference corpus is never shown here; it is advanced-only.
func (m *Model) translationSummary(inner int) string {
	files := m.translationFiles()
	rows := [][3]string{
		{"Honorifics", displayOr(files.honorifics, "(default)"), files.honorificsWord()},
		{"No-honorifics", displayOr(files.noHonorifics, "(default)"), files.noHonorificsWord()},
		{"Game archive", displayOr(choose(m.src.form.Value("cpk"), m.cfg.SourceCPK), "(auto: cache, work folder or game files)"), m.sourceStatusWord()},
	}
	labelW := 15
	var b strings.Builder
	for _, r := range rows {
		status := r[2]
		b.WriteString("  " + m.theme.Render(m.theme.Desc, padTo(r[0], labelW)))
		b.WriteString(truncate(r[1], max(10, inner-labelW-4-len(status))) + "  ")
		b.WriteString(m.theme.Render(statusStyle(m.theme, status), status))
		b.WriteString("\n")
	}
	b.WriteString(m.theme.Render(m.theme.Desc, "  Showing: "+translationModes[m.trans.mode]+"   (← → to change)") + "\n")
	switch {
	case m.trans.counts != "":
		b.WriteString("  " + m.theme.Render(m.theme.Good, truncate(m.trans.counts, inner-4)))
	case m.trans.err != "":
		b.WriteString("  " + m.theme.Render(m.theme.Bad, truncate(m.trans.err, inner-4)))
	default:
		b.WriteString(m.theme.Render(m.theme.Muted, "  Not checked yet — press enter to run the check."))
	}
	return b.String()
}

// translationRows applies the current view mode to the resolved records.
func (m *Model) translationRows() []tableRow {
	rows := make([]tableRow, 0, len(m.trans.records))
	for _, r := range m.trans.records {
		switch m.trans.mode {
		case 1:
			if !recordHasProblem(r) {
				continue
			}
		case 2:
			if r.NoHonorifics == r.Canonical {
				continue
			}
		}
		notes := ""
		if r.TokenFlag != "" {
			notes += "token: " + string(r.TokenFlag)
		}
		if len(r.Errors) > 0 {
			if notes != "" {
				notes += "  "
			}
			notes += fmt.Sprintf("%d error(s)", len(r.Errors))
		}
		rows = append(rows, tableRow{
			ID:    r.RecordID,
			Cells: []string{r.RecordID, string(r.Status), notes},
			Data:  r,
		})
	}
	return rows
}

func recordHasProblem(r importer.RecordResult) bool {
	return r.Status == importer.StatusRejected || r.Status == importer.StatusUnresolved || len(r.Errors) > 0
}

func (m *Model) updateTranslations(key string) tea.Cmd {
	switch key {
	case "left", "right":
		if key == "left" {
			m.trans.mode = (m.trans.mode - 1 + len(translationModes)) % len(translationModes)
		} else {
			m.trans.mode = (m.trans.mode + 1) % len(translationModes)
		}
		m.trans.table.SetRows(m.translationRows())
		return nil
	}
	if m.trans.table.HandleKey(key) {
		return nil
	}
	switch key {
	case "e":
		return m.trans.form.Begin()
	case "enter":
		return m.runImport()
	}
	return nil
}

// runImport performs the shared read-only translation check. It needs only the
// translation files and, for source verification, the game archive: the private
// reference corpus is an advanced option and is never required.
func (m *Model) runImport() tea.Cmd {
	vals := m.trans.form.All()
	corpus := m.corpusPath()
	source := m.sourceArchive()
	if corpus == "" && source == "" {
		m.setStatus("no game archive found yet — open Game files to validate one (a build resolves it automatically)", true)
		return nil
	}
	dir := vals["translations"]
	if dir == "" {
		dir = m.cfg.TranslationsDir
	}
	m.trans.form.End()
	m.loadCfgFromFields()
	return m.startTask("translation check", func(ctx context.Context, prog func(string, int, int)) (string, any, error) {
		prog("reading the translation files", 1, 2)
		var res *importer.Resolution
		var err error
		if corpus != "" {
			res, err = importer.Check(dir, corpus)
		} else {
			res, err = importer.CheckAgainstSource(dir, source)
		}
		if err != nil {
			return "", nil, err
		}
		if err := ctx.Err(); err != nil {
			return "", nil, err
		}
		prog("resolved", 2, 2)
		return fmt.Sprintf("applied %d · overrides %d · rejected %d · unresolved %d · token mismatch %d · differences %s",
			res.Counts.Applied, res.Counts.OverridesApplied,
			res.Counts.Rejected, res.Counts.Unresolved, res.Counts.TokenMismatch, passWord(res.DiffPass)), res, nil
	})
}

// applyResolution fills the record list and the summary line.
func (m *Model) applyResolution(res *importer.Resolution) {
	m.trans.err = ""
	m.trans.records = res.Records
	m.trans.counts = fmt.Sprintf("applied %d · overrides %d · rejected %d · unresolved %d · differences %s",
		res.Counts.Applied, res.Counts.OverridesApplied,
		res.Counts.Rejected, res.Counts.Unresolved, passWord(res.DiffPass))
	m.trans.table.SetRows(m.translationRows())
	m.refreshActions()
}

// viewBuilds renders the Build history screen.
func (m *Model) viewBuilds(w, h int) string {
	inner := max(4, w-4)
	table := m.builds.table
	table.width = inner
	tableHeight := max(5, h-16)

	report := m.theme.Render(m.theme.Muted, "  Select a run and press enter to read its report.")
	if m.builds.report != "" {
		report = truncateLines(m.builds.report, max(3, h-20))
	}
	actions := m.actions()
	actionBody := ""
	if actions != nil {
		actions.width = inner
		actionBody = actions.View(m.theme, max(1, len(m.actionRows())), inner)
	}
	return m.theme.stackPanels(w, h, []panelSpec{
		{title: "Build runs in " + truncate(display(m.buildOutDir()), inner-16), body: table.View(m.theme, tableHeight, inner), height: tableHeight, focus: m.paneKind() == paneMain},
		{title: "Report", body: report, height: max(5, h-tableHeight-9)},
		{title: "Actions", body: actionBody, focus: m.paneKind() == paneActions},
	})
}

func (m *Model) updateBuilds(key string) tea.Cmd {
	if m.builds.table.HandleKey(key) {
		return nil
	}
	switch key {
	case "r":
		m.scanBuilds()
		m.setStatus("build history refreshed", false)
	case "enter":
		m.openBuildReport()
	}
	return nil
}

// openBuildReport reads the selected run's text report, read-only.
func (m *Model) openBuildReport() {
	it, ok := m.builds.table.Selected()
	if !ok {
		return
	}
	outDir := m.buildOutDir()
	var last string
	for _, v := range []string{string(build.VariantHonorifics), string(build.VariantNoHonorifics)} {
		p := filepath.Join(outDir, it.ID, v, "BUILD_REPORT.txt")
		if data, err := os.ReadFile(p); err == nil {
			last = string(data)
		}
	}
	if last == "" {
		m.builds.report = "(no build report found for this run)"
		m.setStatus("no build report found", true)
		return
	}
	m.builds.report = last
}

// truncateLines keeps at most limit lines of a text block.
func truncateLines(s string, limit int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= limit {
		return strings.TrimRight(s, "\n")
	}
	return strings.Join(lines[:limit], "\n") + "\n…"
}
