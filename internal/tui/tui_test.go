package tui

import (
	"context"
	"strings"
	"testing"

	"charm.land/bubbletea/v2"

	"uo-unburger/internal/appconfig"
	"uo-unburger/internal/importer"
)

// press builds a key message from a human-readable key name, mirroring the
// keystrokes the interface matches on.
func press(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "pgup":
		return tea.KeyPressMsg{Code: tea.KeyPgUp}
	case "pgdown":
		return tea.KeyPressMsg{Code: tea.KeyPgDown}
	case "home":
		return tea.KeyPressMsg{Code: tea.KeyHome}
	case "end":
		return tea.KeyPressMsg{Code: tea.KeyEnd}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	default:
		r := []rune(s)
		if len(r) == 1 {
			return tea.KeyPressMsg{Code: r[0], Text: s}
		}
		return tea.KeyPressMsg{Code: tea.KeyExtended, Text: s}
	}
}

func noEnv(string) string { return "" }

func testModel(t *testing.T) *Model {
	t.Helper()
	// Isolate the checkout root so a unit test never discovers (or builds from)
	// the real project's private archives.
	t.Setenv("UO_UNBURGER_ROOT", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := NewModel(Options{
		Config:     appconfig.Default(),
		ConfigPath: "/nonexistent/config.json",
		Version:    "test",
		Getenv:     noEnv,
	})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return m
}

// step applies one key and returns the resulting model.
func step(t *testing.T, m *Model, keys ...string) *Model {
	t.Helper()
	for _, k := range keys {
		next, _ := m.Update(press(k))
		got, ok := next.(*Model)
		if !ok {
			t.Fatalf("Update returned %T, want *Model", next)
		}
		m = got
	}
	return m
}

func view(m *Model) string { return m.View().Content }

func TestThemeNoColorFallback(t *testing.T) {
	env := func(k string) string {
		if k == "NO_COLOR" {
			return "1"
		}
		return ""
	}
	th := NewTheme(appconfig.ThemeAuto, env)
	if !th.NoColor {
		t.Fatalf("NO_COLOR must disable styling")
	}
	m := NewModel(Options{Config: appconfig.Default(), Getenv: env})
	m.Update(tea.WindowSizeMsg{Width: 90, Height: 28})
	got := view(m)
	if strings.Contains(got, "\x1b[") {
		t.Fatalf("no-color output must not contain escape sequences: %q", got)
	}
	if !strings.Contains(got, "Overview") {
		t.Fatalf("no-color output must still be usable: %q", got)
	}
	if !strings.Contains(got, "›") {
		t.Fatalf("the plain fallback must still mark the selected row: %q", got)
	}
}

func TestThemeDumbTerminalDisablesColor(t *testing.T) {
	th := NewTheme(appconfig.ThemeAuto, func(k string) string {
		if k == "TERM" {
			return "dumb"
		}
		return ""
	})
	if !th.NoColor {
		t.Fatalf("TERM=dumb must disable styling")
	}
}

func TestThemeLightDarkSelection(t *testing.T) {
	if th := NewTheme(appconfig.ThemeLight, noEnv); th.Dark {
		t.Fatalf("explicit light theme must be light")
	}
	if th := NewTheme(appconfig.ThemeDark, noEnv); !th.Dark {
		t.Fatalf("explicit dark theme must be dark")
	}
	th := NewTheme(appconfig.ThemeAuto, func(k string) string {
		if k == "COLORFGBG" {
			return "0;15"
		}
		return ""
	})
	if th.Dark {
		t.Fatalf("a bright COLORFGBG background means a light palette")
	}
}

func TestMouseIsOffByDefaultAndWithNoColor(t *testing.T) {
	m := testModel(t)
	if m.mouseEnabled() {
		t.Fatalf("mouse reporting must be off unless the user enables it")
	}
	m.cfg.Mouse = true
	if !m.mouseEnabled() {
		t.Fatalf("mouse reporting should be available once enabled with colour on")
	}
	if v := m.View(); v.MouseMode != tea.MouseModeCellMotion {
		t.Fatalf("the view must request cell-motion mouse mode, got %v", v.MouseMode)
	}

	plain := colorFreeModel(t, 100, 30)
	plain.cfg.Mouse = true
	if plain.mouseEnabled() {
		t.Fatalf("mouse reporting must stay off with NO_COLOR")
	}
	plain.cfg.Mouse = true
	m.theme = NewTheme(appconfig.ThemeAuto, func(k string) string {
		if k == "TERM" {
			return "dumb"
		}
		return ""
	})
	if m.mouseEnabled() {
		t.Fatalf("mouse reporting must stay off on a dumb terminal")
	}
}

func TestScreenSwitchingKeys(t *testing.T) {
	m := testModel(t)
	if m.screen != ScreenDashboard {
		t.Fatalf("expected the Overview to start, got %v", m.screen)
	}
	m = step(t, m, "]")
	if m.screen != ScreenSources {
		t.Fatalf("] should advance one section, got %v", m.screen)
	}
	m = step(t, m, "[")
	if m.screen != ScreenDashboard {
		t.Fatalf("[ should go back one section, got %v", m.screen)
	}
	m = step(t, m, "[")
	if m.screen != ScreenSettings {
		t.Fatalf("[ should wrap to the last section, got %v", m.screen)
	}
	m = step(t, m, "4")
	if m.screen != ScreenBuild {
		t.Fatalf("digit shortcut failed, got %v", m.screen)
	}
	m = step(t, m, "9")
	if m.screen != ScreenSettings {
		t.Fatalf("digit shortcut to the last section failed, got %v", m.screen)
	}
}

// TestSidebarSelectsScreens: the sidebar is a normal list driven by the arrows
// and enter.
func TestSidebarSelectsScreens(t *testing.T) {
	m := testModel(t)
	m.pane = 0
	m = step(t, m, "down")
	if row, ok := m.side.Selected(); !ok || row.ID != "1" {
		t.Fatalf("down in the sidebar should move to Game files, got %+v", row)
	}
	m = step(t, m, "enter")
	if m.screen != ScreenSources {
		t.Fatalf("enter should open the selected section, got %v", m.screen)
	}
}

func TestEveryScreenRendersAndHasState(t *testing.T) {
	m := testModel(t)
	for i := range screenTitles {
		m.setScreen(Screen(i))
		got := view(m)
		if !strings.Contains(got, screenTitles[i]) {
			t.Fatalf("screen %d (%s) does not render its title:\n%s", i, screenTitles[i], got)
		}
		if strings.TrimSpace(got) == "" {
			t.Fatalf("screen %d rendered nothing", i)
		}
	}
}

func TestResizeAndSmallScreenFallback(t *testing.T) {
	m := testModel(t)
	m.Update(tea.WindowSizeMsg{Width: 30, Height: 6})
	got := view(m)
	if !strings.Contains(got, "Terminal too small") {
		t.Fatalf("a small terminal must show the compact fallback:\n%s", got)
	}
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	got = view(m)
	if strings.Contains(got, "Terminal too small") {
		t.Fatalf("a large terminal must show the full interface:\n%s", got)
	}
	if !strings.Contains(got, "Sections") {
		t.Fatalf("full interface should show the sidebar:\n%s", got)
	}
	// Extreme sizes must not panic.
	for _, size := range []tea.WindowSizeMsg{{Width: 0, Height: 0}, {Width: 1, Height: 1}, {Width: 500, Height: 3}} {
		m.Update(size)
		_ = view(m)
	}
}

func TestHelpOverlayOpensAndCloses(t *testing.T) {
	m := testModel(t)
	m = step(t, m, "?")
	if !strings.Contains(view(m), "Move around") {
		t.Fatalf("help overlay missing:\n%s", view(m))
	}
	if !strings.Contains(view(m), "tab / shift+tab") {
		t.Fatalf("help overlay must document pane focus:\n%s", view(m))
	}
	m = step(t, m, "x")
	if strings.Contains(view(m), "Move around") {
		t.Fatalf("any key must close the help overlay")
	}
}

func TestQuitKeys(t *testing.T) {
	m := testModel(t)
	_, cmd := m.Update(press("q"))
	if cmd == nil {
		t.Fatalf("q must request a quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("q must produce tea.QuitMsg")
	}
	m2 := testModel(t)
	_, cmd = m2.Update(press("ctrl+c"))
	if cmd == nil {
		t.Fatalf("ctrl+c must request a quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("ctrl+c must produce tea.QuitMsg")
	}
}

func TestSourcesFormEditingAndCancellation(t *testing.T) {
	m := testModel(t)
	m.setScreen(ScreenSources)
	m = step(t, m, "e")
	if !m.src.form.editing {
		t.Fatalf("e must enter edit mode")
	}
	m = step(t, m, "/", "t", "m", "p", "/", "a", ".", "c", "p", "k")
	if got := m.src.form.Value("cpk"); got != "/tmp/a.cpk" {
		t.Fatalf("typed value = %q", got)
	}
	m = step(t, m, "tab")
	if m.src.form.focus != 1 {
		t.Fatalf("tab must advance the field focus, got %d", m.src.form.focus)
	}
	m = step(t, m, "esc")
	if m.src.form.editing {
		t.Fatalf("esc must leave edit mode")
	}
}

func TestValidateSourceRefusesWithoutAPath(t *testing.T) {
	m := testModel(t)
	m.setScreen(ScreenSources)
	m = step(t, m, "v")
	if m.task != nil {
		t.Fatalf("no task may start without a game archive path")
	}
	if !strings.Contains(m.status, "archive") {
		t.Fatalf("expected a status hint, got %q", m.status)
	}
}

func TestBuildRefusesWithoutInputs(t *testing.T) {
	m := testModel(t)
	m.setScreen(ScreenBuild)
	m = step(t, m, "d")
	if m.task != nil {
		t.Fatalf("a build must not start without an archive and reference data")
	}
	if m.status == "" {
		t.Fatalf("expected an explanatory status line")
	}
}

// TestTranslationsRefusesWithoutAGameArchive: with neither a configured/validated
// game archive nor a reference corpus, the check refuses with a hint that names the
// game archive only. The advanced reference data is never required and must not be
// reported as a blocker.
func TestTranslationsRefusesWithoutAGameArchive(t *testing.T) {
	m := testModel(t)
	m.setScreen(ScreenTranslations)
	m = step(t, m, "enter")
	if m.task != nil {
		t.Fatalf("the check must not start without a game archive")
	}
	if !strings.Contains(m.status, "game archive") {
		t.Fatalf("expected a game-archive hint, got %q", m.status)
	}
	if strings.Contains(strings.ToLower(m.status), "reference data") {
		t.Fatalf("the reference data must never be reported as a blocker, got %q", m.status)
	}
}

func TestInstallPreviewRequiresAnExplicitRoot(t *testing.T) {
	m := testModel(t)
	m.setScreen(ScreenInstall)
	m = step(t, m, "p")
	if m.inst.preview != nil {
		t.Fatalf("no preview without an install folder")
	}
	if !strings.Contains(strings.ToLower(m.inst.err), "install folder") {
		t.Fatalf("expected an install-folder hint, got %q", m.inst.err)
	}
}

func TestInstallConfirmationsUseTypedToken(t *testing.T) {
	m := testModel(t)
	m.setScreen(ScreenInstall)
	m.inst.form.SetValue("root", t.TempDir())
	m = step(t, m, "u")
	if m.confirm == nil {
		t.Fatalf("uninstall must ask for confirmation")
	}
	if m.confirm.token != appconfig.Default().TitleID {
		t.Fatalf("confirmation token = %q", m.confirm.token)
	}
	m = step(t, m, "x", "y", "z", "enter")
	if m.confirm == nil {
		t.Fatalf("a wrong typed token must not confirm")
	}
	for _, r := range m.confirm.token {
		m = step(t, m, string(r))
	}
	m = step(t, m, "enter")
	if m.confirm != nil {
		t.Fatalf("the exact token must confirm")
	}
	if m.task == nil || m.task.label != "uninstall" {
		t.Fatalf("expected an uninstall task, got %+v", m.task)
	}
	m.cancelTask()
}

func TestInstallScreenPassesProtectedRootAndRefusesWithoutSource(t *testing.T) {
	m := testModel(t)
	root := t.TempDir()
	m.setScreen(ScreenInstall)
	m.inst.form.SetValue("root", root)
	m.inst.form.SetValue("active_root", root)

	opts, err := m.installerOptions()
	if err != nil {
		t.Fatalf("installerOptions: %v", err)
	}
	if opts.Root != root || opts.TitleID != appconfig.Default().TitleID {
		t.Fatalf("unexpected options: %+v", opts)
	}
	if len(opts.ProtectedRoots) != 1 || opts.ProtectedRoots[0] != root {
		t.Fatalf("protected root not passed through: %+v", opts.ProtectedRoots)
	}

	// Without a validated source the screen must fail closed: no preview, no
	// confirmation, no writes.
	m = step(t, m, "i")
	if m.inst.preview != nil {
		t.Fatalf("a preview must not be produced without a source archive")
	}
	if m.confirm != nil {
		t.Fatalf("a confirmation must not be offered without a preview")
	}
	if m.task != nil {
		t.Fatalf("no install task may start without a valid source")
	}
}

func TestConfirmDialogPlainYesNo(t *testing.T) {
	m := testModel(t)
	ran := false
	m.confirm = &confirmState{title: "sure?", run: func() tea.Cmd { ran = true; return nil }}
	m = step(t, m, "n")
	if ran || m.confirm != nil {
		t.Fatalf("n must dismiss the confirmation without running it")
	}
	m.confirm = &confirmState{title: "sure?", run: func() tea.Cmd { ran = true; return nil }}
	m = step(t, m, "y")
	if !ran || m.confirm != nil {
		t.Fatalf("y must run the confirmation")
	}
}

func TestProgressLineReportsMeasuredStagesOnly(t *testing.T) {
	m := testModel(t)
	m.task = &task{label: "build both", stage: "rebuild honorifics", done: 3, total: 9}
	line := m.progressLine()
	if !strings.Contains(line, "3/9 stages") {
		t.Fatalf("progress line must show measured stage counts: %q", line)
	}
	if strings.Contains(line, "%") {
		t.Fatalf("progress line must not invent a percentage: %q", line)
	}
	m.task = &task{label: "installing", done: 0, total: 0}
	if strings.Contains(m.progressLine(), "%") {
		t.Fatalf("an unmeasurable task must not show a percentage")
	}
}

func TestTaskLifecycleAppliesStructuredResult(t *testing.T) {
	m := testModel(t)
	m.setScreen(ScreenDiagnostics)
	cmd := m.startTask("unit task", func(ctx context.Context, prog func(string, int, int)) (string, any, error) {
		prog("half", 1, 2)
		return "done", nil, nil
	})
	if cmd == nil || m.task == nil {
		t.Fatalf("startTask must return a wait command")
	}
	var sawProgress bool
	for i := 0; i < 4; i++ {
		msg := cmd()
		if msg == nil {
			break
		}
		next, c := m.Update(msg)
		m = next.(*Model)
		switch msg.(type) {
		case taskProgressMsg:
			sawProgress = true
			if !strings.Contains(m.progressLine(), "1/2 stages") {
				t.Fatalf("progress not applied: %q", m.progressLine())
			}
		case taskDoneMsg:
			if m.task == nil || !m.task.doneFlag {
				t.Fatalf("task should be marked done")
			}
			cmd = c
			i = 99
		}
		if c != nil && i != 99 {
			cmd = c
		}
	}
	if !sawProgress {
		t.Fatalf("progress message was never delivered")
	}
	if m.running() {
		t.Fatalf("task should no longer be running")
	}
}

func TestSecondTaskIsRefusedWhileOneRuns(t *testing.T) {
	m := testModel(t)
	block := make(chan struct{})
	first := m.startTask("first", func(ctx context.Context, prog func(string, int, int)) (string, any, error) {
		<-block
		return "", nil, nil
	})
	if first == nil || m.task == nil {
		t.Fatalf("first task should start")
	}
	if cmd := m.startTask("second", func(context.Context, func(string, int, int)) (string, any, error) {
		return "", nil, nil
	}); cmd != nil {
		t.Fatalf("a concurrent task must be refused")
	}
	if !strings.Contains(m.status, "already running") {
		t.Fatalf("expected a refusal message, got %q", m.status)
	}
	close(block)
	for {
		if msg := first(); msg == nil {
			break
		}
	}
}

func TestCancellationRequestsContextCancel(t *testing.T) {
	m := testModel(t)
	started := make(chan struct{})
	released := make(chan struct{})
	cmd := m.startTask("long task", func(ctx context.Context, prog func(string, int, int)) (string, any, error) {
		close(started)
		<-ctx.Done()
		close(released)
		return "", nil, ctx.Err()
	})
	<-started
	m.cancelTask()
	<-released
	if !m.task.cancelled {
		t.Fatalf("the task should be marked cancelled")
	}
	if _, cmd2 := m.Update(press("esc")); cmd2 != nil {
		// esc during a running task only requests cancellation
	}
	_ = cmd
}

func TestSettingsRememberPaths(t *testing.T) {
	m := testModel(t)
	m.setScreen(ScreenSources)
	m.src.form.SetValue("cpk", "/data/Unicorn_US.CPK")
	m.diag.form.SetValue("corpus", "/data/corpus.jsonl")
	m.build.form.SetValue("variant", "no-honorifics")
	m.loadCfgFromFields()
	if m.cfg.SourceCPK != "/data/Unicorn_US.CPK" {
		t.Fatalf("source not remembered: %q", m.cfg.SourceCPK)
	}
	if m.cfg.Corpus != "/data/corpus.jsonl" {
		t.Fatalf("corpus not remembered: %q", m.cfg.Corpus)
	}
	if m.cfg.Variant != "no-honorifics" {
		t.Fatalf("variant not remembered: %q", m.cfg.Variant)
	}
}

// TestAnEmptyAdvancedCorpusFieldKeepsTheConfiguredCorpus: the reference corpus is
// advanced-only, so an empty advanced field must never wipe a configured value.
func TestAnEmptyAdvancedCorpusFieldKeepsTheConfiguredCorpus(t *testing.T) {
	m := testModel(t)
	m.cfg.Corpus = "/data/kept.jsonl"
	m.diag.form.SetValue("corpus", "")
	m.loadCfgFromFields()
	if m.cfg.Corpus != "/data/kept.jsonl" {
		t.Fatalf("an empty advanced field cleared the corpus: %q", m.cfg.Corpus)
	}
}

func TestFormTabCyclesFields(t *testing.T) {
	th := NewTheme(appconfig.ThemeAuto, noEnv)
	f := newForm(th, []fieldSpec{{key: "a", label: "A"}, {key: "b", label: "B"}})
	if f.editing {
		t.Fatalf("a fresh form is not editing")
	}
	f.Begin()
	if !f.editing || f.focus != 0 {
		t.Fatalf("Begin must focus the first field")
	}
	f.Update(press("tab"))
	if f.focus != 1 {
		t.Fatalf("tab focus = %d", f.focus)
	}
	f.Update(press("tab"))
	if f.focus != 0 {
		t.Fatalf("tab must wrap")
	}
	f.Update(press("shift+tab"))
	if f.focus != 1 {
		t.Fatalf("shift+tab focus = %d", f.focus)
	}
	f.End()
	if f.editing {
		t.Fatalf("End must leave edit mode")
	}
}

func TestVoiceScreenStatesExperimentalStatus(t *testing.T) {
	m := testModel(t)
	m.setScreen(ScreenVoice)
	got := view(m)
	if !strings.Contains(got, "experimental") {
		t.Fatalf("voice screen must state the experimental status:\n%s", got)
	}
	if !strings.Contains(got, "runtime-approved") {
		t.Fatalf("voice screen must preserve the not-runtime-approved boundary:\n%s", got)
	}
	foundImplemented := false
	for _, it := range voiceItems() {
		if len(it.Cells) >= 3 && strings.Contains(it.Cells[2], "implemented") {
			foundImplemented = true
		}
		if len(it.Cells) >= 3 && strings.Contains(it.Cells[2], "not implemented") {
			t.Fatalf("voice screen must not claim the patch is unimplemented: %+v", it)
		}
	}
	if !foundImplemented {
		t.Fatalf("voice screen must state the patch implementation exists")
	}
}

func TestStatusClearsOnEscape(t *testing.T) {
	m := testModel(t)
	m.setStatus("something happened", false)
	m = step(t, m, "esc")
	if m.status != "" {
		t.Fatalf("esc must clear the status line")
	}
}

// ---- interaction (arrow keys, enter, tab, resize) -------------------------

// seedRecords installs a deterministic set of resolved records on the
// Translations screen so table navigation can be tested without running the
// importer.
func seedRecords(m *Model) {
	m.trans.records = []importer.RecordResult{
		{RecordID: "a.fms:1", Status: importer.StatusApplied},
		{RecordID: "b.fms:2", Status: importer.StatusUnresolved},
		{RecordID: "c.fms:3", Status: importer.StatusRejected},
		{RecordID: "d.fms:4", Status: importer.StatusApplied, NoHonorifics: "x", Canonical: "y"},
	}
	m.trans.table.SetRows(m.translationRows())
}

func TestArrowKeysMoveTableSelectionAndEnterActivates(t *testing.T) {
	m := testModel(t)
	m.setScreen(ScreenTranslations)
	seedRecords(m)
	m.pane = 1 // main table

	if row, _ := m.trans.table.Selected(); row.ID != "a.fms:1" {
		t.Fatalf("expected the first record selected, got %q", row.ID)
	}
	m = step(t, m, "down", "down")
	if row, _ := m.trans.table.Selected(); row.ID != "c.fms:3" {
		t.Fatalf("down must move the selection, got %q", row.ID)
	}
	m = step(t, m, "up")
	if row, _ := m.trans.table.Selected(); row.ID != "b.fms:2" {
		t.Fatalf("up must move the selection back, got %q", row.ID)
	}
	m = step(t, m, "j", "j")
	if row, _ := m.trans.table.Selected(); row.ID != "d.fms:4" {
		t.Fatalf("j must move the selection, got %q", row.ID)
	}
	m = step(t, m, "home")
	if row, _ := m.trans.table.Selected(); row.ID != "a.fms:1" {
		t.Fatalf("home must jump to the first row, got %q", row.ID)
	}
	m = step(t, m, "end")
	if row, _ := m.trans.table.Selected(); row.ID != "d.fms:4" {
		t.Fatalf("end must jump to the last row, got %q", row.ID)
	}

	// Enter activates the screen's primary action: with no game archive available
	// the check refuses with an explanatory status instead of starting work. The
	// advanced reference data is never required and never cited as the reason.
	m = step(t, m, "enter")
	if !strings.Contains(m.status, "game archive") {
		t.Fatalf("enter must activate the primary action, status=%q", m.status)
	}
}

func TestFilterAndViewModeOnTranslations(t *testing.T) {
	m := testModel(t)
	m.setScreen(ScreenTranslations)
	seedRecords(m)
	m.pane = 1

	m = step(t, m, "left")
	if m.trans.mode != len(translationModes)-1 {
		t.Fatalf("left must wrap the view mode, got %d", m.trans.mode)
	}
	m = step(t, m, "right")
	if m.trans.mode != 0 {
		t.Fatalf("right must advance the view mode, got %d", m.trans.mode)
	}
	m = step(t, m, "right")
	if m.trans.mode != 1 {
		t.Fatalf("right must reach problems-only, got %d", m.trans.mode)
	}
	if visible, _ := m.trans.table.Count(); visible != 2 {
		t.Fatalf("problems-only should show 2 rows, got %d", visible)
	}
	m.trans.mode = 2
	m.trans.table.SetRows(m.translationRows())
	if visible, _ := m.trans.table.Count(); visible != 1 {
		t.Fatalf("overrides-only should show 1 row, got %d", visible)
	}
	m.trans.mode = 0
	m.trans.table.SetRows(m.translationRows())

	m = step(t, m, "/", "b", ".")
	if visible, total := m.trans.table.Count(); visible != 1 || total != 4 {
		t.Fatalf("filter should narrow to 1 of 4 rows, got %d/%d", visible, total)
	}
	m = step(t, m, "esc")
	if visible, _ := m.trans.table.Count(); visible != 4 {
		t.Fatalf("esc must clear the filter, got %d rows", visible)
	}
}

func TestTabCyclesPaneFocus(t *testing.T) {
	m := testModel(t)
	m.setScreen(ScreenTranslations)
	if m.paneKind() != paneMain {
		t.Fatalf("a screen should open with the main table focused, got %v", m.paneKind())
	}
	m = step(t, m, "tab")
	if m.paneKind() != paneActions {
		t.Fatalf("tab should focus the actions pane, got %v", m.paneKind())
	}
	m = step(t, m, "tab")
	if m.paneKind() != paneSidebar {
		t.Fatalf("tab should focus the sidebar, got %v", m.paneKind())
	}
	m = step(t, m, "tab")
	if m.paneKind() != paneMain {
		t.Fatalf("tab should wrap to the main table, got %v", m.paneKind())
	}
	m = step(t, m, "shift+tab")
	if m.paneKind() != paneSidebar {
		t.Fatalf("shift+tab should focus the sidebar, got %v", m.paneKind())
	}
	// The footer reflects the focused pane.
	if hint := m.footerHints(); !strings.Contains(hint, "section") {
		t.Fatalf("sidebar hint should mention sections, got %q", hint)
	}
	if got := view(m); !strings.Contains(got, "tab: pane") && !strings.Contains(got, "tab: main pane") {
		t.Fatalf("the footer should show contextual tab hints:\n%s", got)
	}
}

func TestOverviewActionsAreKeyboardDriven(t *testing.T) {
	m := testModel(t)
	if m.paneKind() != paneActions {
		t.Fatalf("the Overview should open with its actions focused, got %v", m.paneKind())
	}
	m = step(t, m, "down")
	row, ok := m.actions().Selected()
	if !ok {
		t.Fatalf("the actions pane must have a selection")
	}
	if row.ID != "goto-translations" {
		t.Fatalf("down should select the second action, got %q", row.ID)
	}
	m = step(t, m, "enter")
	if m.screen != ScreenTranslations {
		t.Fatalf("enter on an action should open it, got %v", m.screen)
	}
	// The letter shown in the actions pane is a real accelerator.
	m2 := testModel(t)
	m2 = step(t, m2, "b")
	if m2.screen != ScreenBuild {
		t.Fatalf("the g/t/b/d accelerators must work, got %v", m2.screen)
	}
}

func TestBuildEditionSelectorAndActions(t *testing.T) {
	m := testModel(t)
	m.setScreen(ScreenBuild)
	m.pane = 1
	m.build.options.SetCursor("edition")
	before := m.build.form.Value("variant")
	m = step(t, m, "right")
	if m.build.form.Value("variant") == before {
		t.Fatalf("right on the edition row must change the edition")
	}
	m = step(t, m, "left")
	if m.build.form.Value("variant") != before {
		t.Fatalf("left must cycle the edition back")
	}
	// The real build asks for confirmation first.
	m = step(t, m, "b")
	if m.confirm == nil {
		t.Fatalf("b must open a confirmation")
	}
	if m.confirm.token != "" {
		t.Fatalf("a plain build confirmation should not need a typed token")
	}
}

func TestSettingsToggleWithArrowKeys(t *testing.T) {
	m := testModel(t)
	m.setScreen(ScreenSettings)
	m.pane = 1
	m.set.table.SetCursor("theme")
	if m.cfg.Theme == "light" {
		m.cfg.Theme = "dark"
	}
	start := m.cfg.Theme
	m = step(t, m, "right")
	if m.cfg.Theme == start {
		t.Fatalf("right must change the theme")
	}
	m.set.table.SetCursor("mouse")
	was := m.cfg.Mouse
	m = step(t, m, "right")
	if m.cfg.Mouse == was {
		t.Fatalf("right must toggle mouse support")
	}
}

func TestResizeMidSessionKeepsLayout(t *testing.T) {
	m := colorFreeModel(t, 100, 30)
	m.setScreen(ScreenTranslations)
	seedRecords(m)
	for _, size := range []tea.WindowSizeMsg{{Width: 132, Height: 40}, {Width: 80, Height: 24}, {Width: 40, Height: 10}, {Width: 100, Height: 30}} {
		m.Update(size)
		got := view(m)
		if strings.TrimSpace(got) == "" {
			t.Fatalf("%dx%d rendered nothing", size.Width, size.Height)
		}
		if n := maxRunes(got); n > size.Width {
			var bad string
			for _, ln := range strings.Split(got, "\n") {
				if len([]rune(ln)) > size.Width {
					bad = ln
					break
				}
			}
			t.Fatalf("%dx%d rendered a %d-column line: %q", size.Width, size.Height, n, bad)
		}
	}
	// After shrinking and growing again the full layout is back.
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if got := view(m); !strings.Contains(got, "Sections") || !strings.Contains(got, "Records") {
		t.Fatalf("the layout did not recover after a resize:\n%s", got)
	}
}

// TestFocusIsVisibleOnTheFocusedPane: the focused pane's frame is drawn in the
// accent colour and the others in the quiet idle colour, so focus is always
// visible (and in the plain fallback the footer states the focused pane).
func TestFocusIsVisibleOnTheFocusedPane(t *testing.T) {
	const accent = "\x1b[38;5;81m" // dark palette focus
	const idle = "\x1b[38;5;240m"  // dark palette idle

	m := testModel(t)
	m.setScreen(ScreenTranslations)
	if m.paneKind() != paneMain {
		t.Fatalf("the main table should be focused, got %v", m.paneKind())
	}
	out := view(m)
	records := lineContaining(out, "Records")
	actions := lineContaining(out, "Actions")
	if !strings.Contains(records, accent) {
		t.Fatalf("the focused table frame should use the accent colour: %q", records)
	}
	if strings.Contains(actions, accent) {
		t.Fatalf("an unfocused pane must not use the focus colour: %q", actions)
	}
	if !strings.Contains(actions, idle) {
		t.Fatalf("an unfocused pane should use the idle border colour: %q", actions)
	}

	m = step(t, m, "tab")
	out = view(m)
	if !strings.Contains(lineContaining(out, "Actions"), accent) {
		t.Fatalf("after tab the actions frame should be focused: %q", lineContaining(out, "Actions"))
	}
	m = step(t, m, "tab")
	out = view(m)
	if !strings.Contains(lineContaining(out, "Sections"), accent) {
		t.Fatalf("after two tabs the sidebar should be focused: %q", lineContaining(out, "Sections"))
	}
}

func lineContaining(block, needle string) string {
	for _, ln := range strings.Split(block, "\n") {
		if strings.Contains(ln, needle) {
			return ln
		}
	}
	return ""
}
