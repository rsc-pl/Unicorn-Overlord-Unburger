package tui

import (
	"fmt"
	"os"
	"strings"

	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"uo-unburger/internal/appconfig"
	"uo-unburger/internal/build"
	"uo-unburger/internal/importer"
	"uo-unburger/internal/preflight"
	"uo-unburger/internal/qa"
)

// statusStyle colours a status word consistently across every table. A status
// cell may carry a short qualifier after the word (for example "loaded 17,957"),
// so the exact words are matched first and the leading word after that.
func statusStyle(t *Theme, word string) lipgloss.Style {
	w := strings.ToUpper(strings.TrimSpace(word))
	switch w {
	case "OK", "PASS", "APPLIED", "PUBLISHED", "YES", "ON", "CHECKED OK", "SET":
		return t.Good
	case "FAIL", "REJECTED", "MISSING", "NO", "OFF", "CHECK FAILED", "ERROR", "BLOCKED":
		return t.Bad
	case "UNRESOLVED", "WARN", "DRY RUN", "NOT CHECKED", "NOT SET", "REVIEW":
		return t.Warn
	}
	switch leadingWord(w) {
	case "LOADED", "READY", "FOUND", "DISCOVERED":
		return t.Good
	case "MISSING", "INVALID":
		return t.Bad
	case "AUTO", "OPTIONAL", "NOT":
		return t.Warn
	}
	return t.Muted
}

// leadingWord returns the first word of an upper-cased status cell, so a status
// that appends a count or a qualifier is still recognised.
func leadingWord(w string) string {
	for i := 0; i < len(w); i++ {
		switch w[i] {
		case ' ', '(', ':':
			return w[:i]
		}
	}
	return w
}

// overviewWarnings lists the things that currently block a clean build. The
// advanced reference corpus is never one of them: it is an optional advanced
// cross-check, so a normal build needs only the translation files and the game
// archive.
func (m *Model) overviewWarnings() []string {
	var out []string
	switch {
	case m.cfg.SourceCPK == "" && m.src.form.Value("cpk") == "":
		out = append(out, "No game archive chosen — a build resolves one automatically (cache, work folder or your game files).")
	case m.src.facts == nil && m.src.err == "":
		out = append(out, "The game archive has not been checked in this session yet.")
	case m.src.err != "":
		out = append(out, "The last archive check failed — see Game files.")
	}
	if dir := choose(m.trans.form.Value("translations"), m.cfg.TranslationsDir); dir == "" {
		out = append(out, "No translation folder selected yet — open Translations.")
	} else if files := m.translationFiles(); !files.honorificsPresent {
		out = append(out, "The canonical translation table (honorifics.tsv) was not found in "+display(dir)+".")
	} else if files.err != "" {
		out = append(out, "The translation files in "+display(dir)+" could not be read — open Translations.")
	}
	if m.build.last == nil {
		out = append(out, "No build has been run in this session.")
	}
	if m.inst.form.Value("root") == "" {
		out = append(out, "No install folder selected — nothing can be installed yet.")
	}
	if len(out) == 0 {
		out = append(out, "Nothing is blocking a build. Use Build to run one.")
	}
	return out
}

// overviewReadiness summarises the pipeline as a short checklist. It reports the
// public translation files and the auto-discovered game archive; the private
// reference corpus is deliberately absent.
func (m *Model) overviewReadiness() []tableRow {
	mark := func(ok bool) string {
		if ok {
			return "yes"
		}
		return "no"
	}
	source := choose(m.src.form.Value("cpk"), m.cfg.SourceCPK)
	files := m.translationFiles()
	honorifics := fmt.Sprintf("Honorifics table (%s)", plural(files.canonicalRows, "record"))
	if !files.honorificsPresent {
		honorifics = "Honorifics table"
	}
	noHonorifics := fmt.Sprintf("No-honorifics overrides (%s)", plural(files.overrideRows, "override"))
	if !files.noPresent {
		noHonorifics = "No-honorifics overrides"
	}
	return []tableRow{
		{ID: "source", Cells: []string{"Game archive chosen", mark(source != "")}},
		{ID: "validated", Cells: []string{"Game archive checked", mark(m.src.facts != nil)}},
		{ID: "honorifics", Cells: []string{honorifics, mark(files.loaded && files.honorificsPresent)}},
		{ID: "no-honorifics", Cells: []string{noHonorifics, mark(files.loaded && files.noPresent)}},
		{ID: "checked", Cells: []string{"Translation check run", mark(m.trans.counts != "")}},
		{ID: "build", Cells: []string{"Build or dry run completed", mark(m.build.last != nil)}},
	}
}

// viewDashboard renders the Overview screen: status cards, warnings and actions.
func (m *Model) viewDashboard(w, h int) string {
	inner := max(4, w-4)
	readiness := m.renderMiniTable(m.overviewReadiness(), inner)

	var warnings strings.Builder
	for _, line := range m.overviewWarnings() {
		warnings.WriteString("  • " + truncate(line, inner-4))
		warnings.WriteString("\n")
	}
	for _, line := range []string{
		"Game files are opened read-only; a build never touches the archive.",
		"Nothing is written until a build publishes, and install always backs up first.",
	} {
		warnings.WriteString(m.theme.Render(m.theme.Muted, "  "+truncate(line, inner-4)) + "\n")
	}

	transBody := m.theme.Render(m.theme.Muted, "  Not checked yet — open Translations and press enter.")
	if m.trans.counts != "" {
		transBody = "  " + m.theme.Render(m.theme.Good, truncate(m.trans.counts, inner-4))
	} else if m.trans.err != "" {
		transBody = "  " + m.theme.Render(m.theme.Bad, truncate(m.trans.err, inner-4))
	}

	buildBody := m.theme.Render(m.theme.Muted, "  No build in this session yet.")
	if m.build.last != nil {
		res := m.build.last
		buildBody = fmt.Sprintf("  %s — %s\n", statusWord(res.DryRun), res.RunName) +
			m.theme.Render(m.theme.Desc, fmt.Sprintf("  %d editions · %d records applied",
				len(res.Editions), res.Plan.AppliedRecords))
	}

	reports := m.countBuildRuns()

	actions := m.actions()
	actionBody := ""
	if actions != nil {
		actions.width = inner
		actionBody = actions.View(m.theme, max(1, len(m.actionRows())), inner)
	}

	specs := []panelSpec{
		{title: "Ready to build", body: readiness, height: 9},
		{title: "Warnings and blockers", body: strings.TrimRight(warnings.String(), "\n"), height: 6},
		{title: "Translations", body: transBody, height: 3},
		{title: "Builds", body: fmt.Sprintf("  %d run(s) found on disk.\n%s", reports, buildBody), height: 4},
		{title: "Voice patch", body: m.theme.Render(m.theme.Warn, "  Implemented as an experimental, not-runtime-approved artifact."), height: 3},
		{title: "Actions", body: actionBody, focus: m.paneKind() == paneActions},
	}
	return m.theme.stackPanels(w, h, specs)
}

// countBuildRuns counts the folders in the output root.
func (m *Model) countBuildRuns() int {
	entries, err := os.ReadDir(m.buildOutDir())
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			n++
		}
	}
	return n
}

// renderMiniTable renders a small aligned two-column list inside a panel: the
// label column is fixed so the values line up, and the value is coloured by its
// status word.
func (m *Model) renderMiniTable(rows []tableRow, width int) string {
	labelW := max(12, min(30, width-10))
	var b strings.Builder
	for _, r := range rows {
		label := m.cellOf(r, 0)
		value := m.cellOf(r, 1)
		b.WriteString("  " + padTo(label, labelW))
		b.WriteString(m.theme.Render(statusStyle(m.theme, value), value))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m *Model) cellOf(r tableRow, i int) string {
	if i >= 0 && i < len(r.Cells) {
		return r.Cells[i]
	}
	return ""
}

// sourceStatusWord reports the archive check state.
func (m *Model) sourceStatusWord() string {
	switch {
	case m.src.facts != nil:
		return "checked OK"
	case m.src.err != "":
		return "check failed"
	}
	if choose(m.src.form.Value("cpk"), m.cfg.SourceCPK) == "" {
		return "auto"
	}
	return "not checked"
}

// sourceRows builds the Game files table. It reports the public translation
// files (honorifics and the optional sparse no-honorifics table) rather than a
// reference-data requirement; the private JP/EN corpus is an advanced
// cross-check and never appears here.
func (m *Model) sourceRows() []tableRow {
	setWord := func(v string) string {
		if strings.TrimSpace(v) == "" {
			return "not set"
		}
		return "set"
	}
	files := m.translationFiles()
	source := choose(m.src.form.Value("cpk"), m.cfg.SourceCPK)
	rows := []tableRow{
		{ID: "source", Cells: []string{"Game archive", displayOr(source, "(auto: cache, work folder or game files)"), m.sourceStatusWord()}},
		{ID: "honorifics", Cells: []string{"Honorifics table", displayOr(files.honorifics, "(default)"), files.honorificsWord()}},
		{ID: "no-honorifics", Cells: []string{"No-honorifics table", displayOr(files.noHonorifics, "(default)"), files.noHonorificsWord()}},
		{ID: "translations", Cells: []string{"Translation folder", displayOr(choose(m.trans.form.Value("translations"), m.cfg.TranslationsDir), "(default)"), setWord(choose(m.trans.form.Value("translations"), m.cfg.TranslationsDir))}},
		{ID: "output", Cells: []string{"Build output folder", displayOr(m.buildOutDir(), "(default)"), "set"}},
		{ID: "title", Cells: []string{"Title ID", choose(m.src.form.Value("title_id"), m.cfg.TitleID), setWord(choose(m.src.form.Value("title_id"), m.cfg.TitleID))}},
		{ID: "variant", Cells: []string{"Default edition", choose(m.build.form.Value("variant"), m.cfg.Variant), "set"}},
	}
	return rows
}

// viewSources renders the Game files screen.
func (m *Model) viewSources(w, h int) string {
	inner := max(4, w-4)
	if m.src.form.editing {
		return m.theme.stackPanels(w, h, []panelSpec{
			{title: "Edit locations", body: m.src.form.View(inner), focus: true},
			{title: "Keys", body: m.theme.Render(m.theme.Desc, "  tab: next field   enter: apply   esc: done (values kept)"), height: 3},
		})
	}
	table := m.src.table
	table.width = inner
	tableHeight := max(5, h-14)
	rows := m.sourceRows()
	table.SetRows(rows)

	detail := ""
	if f := m.src.facts; f != nil {
		detail = fmt.Sprintf("  Path      %s\n  Size      %s\n  Checksum  %s\n  Members   %d (%d game files)",
			truncate(display(f.Path), inner-14), humanBytes(f.Size), f.SHA256, f.Members, f.FMSMembers)
	} else if m.src.err != "" {
		detail = m.theme.Render(m.theme.Bad, "  "+truncate(m.src.err, inner-4))
	} else {
		detail = m.theme.Render(m.theme.Muted, "  Not checked yet — press enter or v to validate (read-only).")
	}

	actions := m.actions()
	actionBody := ""
	if actions != nil {
		actions.width = inner
		actionBody = actions.View(m.theme, max(1, len(m.actionRows())), inner)
	}
	return m.theme.stackPanels(w, h, []panelSpec{
		{title: "Game files and folders", body: table.View(m.theme, tableHeight, inner), height: tableHeight, focus: m.paneKind() == paneMain},
		{title: "Archive details", body: detail, height: 6},
		{title: "Actions", body: actionBody, focus: m.paneKind() == paneActions},
	})
}

func (m *Model) updateSources(key string) tea.Cmd {
	if m.src.table.HandleKey(key) {
		return nil
	}
	switch key {
	case "e":
		return m.src.form.Begin()
	case "v", "enter":
		return m.validateSource()
	}
	return nil
}

// viewSettings renders the Settings screen.
func (m *Model) viewSettings(w, h int) string {
	inner := max(4, w-4)
	if m.set.form.editing {
		return m.theme.stackPanels(w, h, []panelSpec{
			{title: "Edit settings", body: m.set.form.View(inner), focus: true},
			{title: "Keys", body: m.theme.Render(m.theme.Desc, "  enter: apply and save   esc: done (values kept, not saved)"), height: 3},
		})
	}
	m.refreshSettings()
	table := m.set.table
	table.width = inner
	tableHeight := max(5, h-14)

	actions := m.actions()
	actionBody := ""
	if actions != nil {
		actions.width = inner
		actionBody = actions.View(m.theme, max(1, len(m.actionRows())), inner)
	}
	hint := "  ↑↓ choose a setting   ←→ change it   enter or the Actions pane saves."
	if m.settingsDirty() {
		hint = "  Unsaved changes — press enter (or Save settings) to store them; esc keeps them here."
	}
	if m.cfg.Mouse && !m.mouseEnabled() {
		hint = "  Mouse is on in the settings file but inactive while colour is off."
	}
	return m.theme.stackPanels(w, h, []panelSpec{
		{title: "Settings", body: table.View(m.theme, tableHeight, inner), height: tableHeight, focus: m.paneKind() == paneMain},
		{title: "Notes", body: m.theme.Render(m.theme.Desc, hint) + "\n" +
			m.theme.Render(m.theme.Muted, "  Settings are stored outside the repository; nothing about your machine is written into it."), height: 4},
		{title: "Actions", body: actionBody, focus: m.paneKind() == paneActions},
	})
}

func (m *Model) updateSettings(key string) tea.Cmd {
	if m.set.table.HandleKey(key) {
		return nil
	}
	switch key {
	case "e":
		return m.set.form.Begin()
	case "enter", "s":
		return m.saveSettings()
	case "left", "right":
		return m.toggleSetting(key)
	}
	return nil
}

// toggleSetting changes the selected option with the arrow keys.
func (m *Model) toggleSetting(key string) tea.Cmd {
	row, ok := m.set.table.Selected()
	if !ok {
		return nil
	}
	switch row.ID {
	case "theme":
		m.themeMode = nextInCycle(m.themeMode, []string{appconfig.ThemeAuto, appconfig.ThemeLight, appconfig.ThemeDark}, key)
		m.cfg.Theme = m.themeMode
		m.theme = NewTheme(m.themeMode, m.env)
		m.retheme()
	case "mouse":
		if !m.cfg.Mouse && m.theme.NoColor {
			m.setStatus("mouse support is unavailable while colour is off", true)
			return nil
		}
		m.cfg.Mouse = !m.cfg.Mouse
		if m.cfg.Mouse && !m.mouseEnabled() {
			m.setStatus("mouse requested, but it stays off with NO_COLOR or TERM=dumb", false)
		} else {
			m.setStatus("mouse support "+boolWord(m.cfg.Mouse), false)
		}
	}
	m.set.dirty = true
	m.refreshSettings()
	m.refreshActions()
	return nil
}

// nextInCycle returns the next value in a cycle after current, forwards for
// "right" and backwards for "left".
func nextInCycle(current string, values []string, key string) string {
	idx := 0
	for i, v := range values {
		if v == current {
			idx = i
		}
	}
	if key == "left" {
		idx = (idx - 1 + len(values)) % len(values)
	} else {
		idx = (idx + 1) % len(values)
	}
	return values[idx]
}

// applyTaskData stores a finished task's structured result on the UI goroutine.
func (m *Model) applyTaskData(data any) {
	switch d := data.(type) {
	case *build.SourceFacts:
		m.src.facts = d
		m.src.err = ""
		m.refreshActions()
	case *build.Result:
		m.build.last = d
		m.build.err = ""
		m.build.report = d.SummaryTxt
		m.build.editions.SetRows(buildRows(d))
		m.refreshActions()
	case buildOutcome:
		m.build.last = d.result
		m.build.err = ""
		m.build.report = d.result.SummaryTxt
		m.build.editions.SetRows(buildRows(d.result))
		m.rememberSource(d.resolution)
		m.refreshActions()
	case *preflight.Report:
		m.diag.rep = d
		rows := make([]tableRow, 0, len(d.Checks))
		for _, c := range d.Checks {
			state := "fail"
			if c.OK {
				state = "pass"
			}
			rows = append(rows, tableRow{ID: c.Name, Cells: []string{c.Name, state, c.Detail}})
		}
		m.diag.table.SetRows(rows)
		m.refreshActions()
	case *importer.Resolution:
		m.applyResolution(d)
	case *qa.Result:
		m.applyQA(d)
	case setupResolved:
		m.rememberSource(d.res)
		if d.res.Path != "" {
			m.cfg.SourceCPK = d.res.Path
			if m.src.form != nil {
				m.src.form.SetValue("cpk", d.res.Path)
			}
		}
		m.refreshSetup()
		m.refreshActions()
	case installResult:
		if d.res != nil {
			m.inst.err = ""
			m.refreshBackups()
			m.previewInstall()
		}
	}
}

// voiceItems is the read-only voice-patch status shown on the Voice screen. The
// patch implementation exists and can be generated by an explicit --voice build,
// but it is experimental and has not been runtime-approved: no console or
// emulator acceptance run has been performed.
func voiceItems() []tableRow {
	return []tableRow{
		{ID: "status", Cells: []string{"Patch status", "experimental", "implemented and generatable with an explicit --voice build"}},
		{ID: "assets", Cells: []string{"Japanese voice assets", "present", "present in the western build (voice families confirmed)"}},
		{ID: "gate", Cells: []string{"Runtime validation", "required", "an in-game acceptance run is required; the patch is not runtime-approved"}},
		{ID: "selector", Cells: []string{"In-game voice setting", "unknown", "no persistent setting or menu string has been located"}},
		{ID: "runtime", Cells: []string{"Runtime acceptance", "not run", "no console or emulator test has been performed"}},
		{ID: "note", Cells: []string{"Never installed", "ok", "the toolkit builds the artifact but never installs or runs it"}},
		{ID: "text", Cells: []string{"Text editions", "ok", "the text override and the voice patch are independent; one shared patch is built separately"}},
	}
}

// viewVoice renders the read-only voice-patch status.
func (m *Model) viewVoice(w, h int) string {
	inner := max(4, w-4)
	m.voice.table.width = inner
	m.voice.table.SetRows(voiceItems())
	body := m.voice.table.View(m.theme, max(5, h-8), inner)
	return m.theme.stackPanels(w, h, []panelSpec{
		{title: "Japanese voice patch — experimental", body: body, focus: m.paneKind() == paneMain},
		{title: "Notice", body: m.theme.Render(m.theme.Warn,
			"  The patch can be generated with --voice, but it is NOT runtime-approved:\n"+
				"  it has not been installed or run on a console or emulator."), height: 4,
		},
	})
}
