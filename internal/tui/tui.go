// Package tui implements the keyboard-first terminal interface for the
// uo-unburger toolkit.
//
// The interface is a pure state machine over Bubble Tea messages: every screen is
// a plain Go value that can be driven from a test with synthetic key and resize
// messages, with no terminal involved. It reuses the same services as the command
// line — the build engine, the translation importer, the static QA analyser, the
// self-check suite and the LayeredFS installer — so the two front ends cannot
// drift apart.
//
// Interaction model:
//
//	↑/↓ (and j/k) move the selection inside the focused table or list
//	←/→          change the value of the selected option or scroll table columns
//	enter        open, activate or confirm the selection
//	esc          dismiss a confirmation or the help overlay, finish editing (the
//	             typed values are kept, nothing is saved), cancel a running task,
//	             otherwise go back one screen (the Overview clears its status)
//	tab/shift+tab move focus between the sidebar, the main table and the actions
//	[ / ]        previous/next section     1…9  jump straight to a section
//	/            filter the focused table    ?    help overlay    q  quit
//
// Number keys are only accelerators; every action is reachable with the arrow
// keys, enter and the plain-language labels on screen.
package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"uo-unburger/internal/appconfig"
	"uo-unburger/internal/build"
	"uo-unburger/internal/importer"
	"uo-unburger/internal/installer"
	"uo-unburger/internal/paths"
	"uo-unburger/internal/preflight"
	"uo-unburger/internal/project"
	"uo-unburger/internal/qa"
)

// Screen identifies one top-level view.
type Screen int

// The screens, in sidebar order.
const (
	ScreenDashboard Screen = iota
	ScreenSources
	ScreenTranslations
	ScreenBuild
	ScreenBuilds
	ScreenInstall
	ScreenDiagnostics
	ScreenVoice
	ScreenSettings
)

// screenTitles are the plain-language section names shown in the sidebar, the
// header and the help overlay. No internal project vocabulary appears here.
var screenTitles = []string{
	"Overview",
	"Game files",
	"Translations",
	"Build",
	"Build history",
	"Install",
	"Diagnostics",
	"Voice",
	"Settings",
}

// screenBlurbs is the one-line explanation under the section name.
var screenBlurbs = []string{
	"status, warnings and what to do next",
	"where the game files, translation files and output folders live",
	"check the translation files and inspect every record",
	"choose the edition and run a build",
	"previous build runs and their reports",
	"install, remove or roll back the game override",
	"static checks and the built-in self-check",
	"Japanese voice patch status (experimental, not runtime-approved)",
	"theme, mouse and where settings are stored",
}

const (
	minUsableWidth  = 48
	minUsableHeight = 12
	// sidebarWidth is the default sidebar frame width; it narrows on small
	// terminals (see Model.dims).
	sidebarWidth = 24
	// headerLines and footerLines are the fixed chrome above and below the body.
	headerLines = 2
	footerLines = 3
)

// paneKind identifies a focusable area within a screen.
type paneKind int

const (
	paneSidebar paneKind = iota
	paneMain
	paneActions
)

// ErrNotTerminal is returned by Run when stdin/stdout are not a terminal.
var ErrNotTerminal = errors.New("tui: a terminal is required on stdin and stdout")

// Options configures the interface.
type Options struct {
	// Config is the loaded project-local configuration.
	Config appconfig.Config
	// ConfigPath is where the configuration is stored (shown in Settings).
	ConfigPath string
	// Input/Output default to stdin/stdout. Both must be terminals for Run.
	Input  io.Reader
	Output io.Writer
	// Version is the toolkit version shown in the header.
	Version string
	// FirstRun requests the guided first-run setup overlay.
	FirstRun bool
	// Getenv overrides environment lookup (tests pin NO_COLOR/TERM).
	Getenv func(string) string
}

// confirmChoice is one keyboard-selectable answer in a multi-choice confirmation.
type confirmChoice struct {
	key   string // single-character accelerator
	label string // human wording
	value string // value handed to pick
}

// confirmState is a modal yes/no, typed-token or multi-choice confirmation.
type confirmState struct {
	title  string
	detail string
	token  string
	typed  string
	// choices turns the dialog into a multi-choice prompt; pick receives the
	// selected value. cancel is invoked when the dialog is dismissed with Escape.
	choices []confirmChoice
	pick    func(value string) tea.Cmd
	cancel  func() tea.Cmd
	run     func() tea.Cmd
}

// Model is the whole interface state.
type Model struct {
	opts Options
	env  func(string) string

	cfg     appconfig.Config
	cfgPath string

	theme     *Theme
	themeMode string

	width, height int
	ready         bool
	screen        Screen
	history       []Screen   // visited screens, newest last (Esc walks back)
	pane          pane       // focus index into the current screen's ring
	kinds         []paneKind // ring for the current screen
	showHelp      bool
	setup         *setupState
	status        string
	statusErr     bool
	confirm       *confirmState
	task          *task
	spin          spinner.Model

	side   *tableModel
	acts   []*tableModel
	src    sourceState
	trans  translationState
	build  buildState
	builds buildsState
	inst   installState
	diag   diagState
	voice  voiceState
	set    settingsState

	// mouseHit records where the focused table was last drawn so click and wheel
	// events can be mapped back to a row.
	mouseHit *mouseHit
}

type pane int

type mouseHit struct {
	table   *tableModel
	originX int
	originY int
	rowY    int // terminal row of the first data row
}

type sourceState struct {
	form  *form
	facts *build.SourceFacts
	err   string
	table *tableModel
}

type translationState struct {
	form    *form
	table   *tableModel
	records []importer.RecordResult
	counts  string
	err     string
	mode    int // 0 all, 1 problems only, 2 overrides only
	// files caches the public translation-table status shown on the normal
	// screens, so the advanced reference corpus is never needed to report it.
	files translationFilesStatus
}

type buildState struct {
	form     *form
	options  *tableModel
	editions *tableModel
	last     *build.Result
	err      string
	report   string
	// target selects the packaging loader layout; packageOn toggles whether a
	// real build also assembles the install-ready ZIPs. Both are cycled with the
	// left/right arrow keys on the Build screen.
	target    build.PackageTarget
	packageOn bool
}

type buildsState struct {
	table  *tableModel
	report string
}

type installState struct {
	form    *form
	table   *tableModel
	preview *installer.Preview
	err     string
}

type diagState struct {
	table    *tableModel
	qa       *qa.Result
	rep      *preflight.Report
	err      string
	advanced bool
	// form holds the advanced-only reference-data field. It is never required
	// and never shown on a normal screen.
	form *form
}

type voiceState struct {
	table *tableModel
}

type settingsState struct {
	form  *form
	table *tableModel
	// dirty records a change made without saving: an edit in the form, or a
	// value toggled with the arrow keys. It drives the "unsaved" note and the
	// hint shown when the screen is left.
	dirty bool
}

// NewModel builds the interface state.
func NewModel(opts Options) *Model {
	env := opts.Getenv
	if env == nil {
		env = os.Getenv
	}
	cfg := opts.Config
	cfg.Normalize()
	m := &Model{
		opts:      opts,
		env:       env,
		cfg:       cfg,
		cfgPath:   opts.ConfigPath,
		themeMode: cfg.Theme,
		theme:     NewTheme(cfg.Theme, env),
		spin:      spinner.New(spinner.WithSpinner(spinner.Line)),
		screen:    ScreenDashboard,
	}
	m.acts = make([]*tableModel, len(screenTitles))
	m.initWidgets()
	m.initForms()
	if opts.FirstRun {
		m.openSetup(true)
	}
	m.setScreen(ScreenDashboard)
	return m
}

// Run starts the interface. It refuses to start without a terminal so a
// non-interactive invocation can fall back to the printed help instead of
// emitting escape sequences into a pipe.
func Run(opts Options) error {
	in := opts.Input
	if in == nil {
		in = os.Stdin
	}
	out := opts.Output
	if out == nil {
		out = os.Stdout
	}
	if !isTerminalFile(in) || !isTerminalFile(out) {
		return ErrNotTerminal
	}
	m := NewModel(opts)
	p := tea.NewProgram(m, tea.WithInput(in), tea.WithOutput(out))
	if _, err := p.Run(); err != nil {
		if errors.Is(err, tea.ErrInterrupted) {
			return nil
		}
		return err
	}
	return nil
}

// isTerminalFile reports whether v is an *os.File that should be treated as a
// terminal. A character device is required, and /dev/null is excluded because it
// is a character device that cannot be read interactively.
func isTerminalFile(v any) bool {
	f, ok := v.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	if info.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	if f.Name() == os.DevNull {
		return false
	}
	if nullInfo, err := os.Stat(os.DevNull); err == nil && os.SameFile(info, nullInfo) {
		return false
	}
	return true
}

// IsTerminal reports whether v is a file that refers to a character device (a
// terminal). It is used by the command line to decide between the interactive
// interface and the printed help.
func IsTerminal(v any) bool { return isTerminalFile(v) }

// Init requests the terminal background colour so an "auto" theme can follow the
// terminal instead of guessing.
func (m *Model) Init() tea.Cmd {
	return func() tea.Msg { return tea.RequestBackgroundColor() }
}

// Update is the message loop.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		m.resize()
		return m, nil
	case tea.BackgroundColorMsg:
		if m.themeMode == appconfig.ThemeAuto && !m.theme.NoColor {
			mode := appconfig.ThemeDark
			if !msg.IsDark() {
				mode = appconfig.ThemeLight
			}
			m.theme = NewTheme(mode, m.env)
			m.retheme()
		}
		return m, nil
	case spinner.TickMsg:
		if !m.running() {
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	case taskProgressMsg:
		if m.task != nil && msg.id == m.task.id {
			m.task.stage = msg.stage
			m.task.done = msg.done
			m.task.total = msg.total
			return m, waitForTask(m.task.ch)
		}
		return m, nil
	case taskAskMsg:
		if m.task != nil && msg.id == m.task.id {
			m.presentTaskQuestion(msg.question)
			return m, waitForTask(m.task.ch)
		}
		return m, nil
	case taskDoneMsg:
		if m.task != nil && msg.id == m.task.id {
			m.task.doneFlag = true
			m.task.report = msg.report
			m.task.err = msg.err
			m.task.cancel = nil
			if msg.err != nil && !isCancelled(msg.err) {
				m.setStatus(fmt.Sprintf("%s failed: %s", m.task.label, paths.Error(msg.err)), true)
			} else if isCancelled(msg.err) {
				m.setStatus(m.task.label+" cancelled — nothing was published", false)
			} else {
				m.setStatus(m.task.label+" finished", false)
			}
			m.applyTaskData(msg.data)
			m.afterTask()
		}
		return m, nil
	case settingsSavedMsg:
		if msg.err != nil {
			m.setStatus("saving settings: "+paths.Error(msg.err), true)
		} else {
			if msg.path != "" {
				m.cfgPath = msg.path
			}
			m.set.dirty = false
			m.setStatus("settings saved", false)
		}
		m.refreshSettings()
		return m, nil
	case tea.InterruptMsg:
		m.cancelTask()
		return m, tea.Quit
	case tea.QuitMsg:
		m.cancelTask()
		return m, tea.Quit
	case tea.MouseWheelMsg:
		return m, m.handleMouseWheel(msg)
	case tea.MouseClickMsg:
		return m, m.handleMouseClick(msg)
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// View renders the interface.
func (m *Model) View() tea.View {
	return m.theme.view(m.render(), m.mouseEnabled())
}

// mouseEnabled reports whether terminal mouse reporting is requested. It is off
// when the user has not enabled it, and always off with NO_COLOR or TERM=dumb,
// because reporting captures the terminal's own mouse handling (wheel and click)
// and can interfere with selecting and copying text.
func (m *Model) mouseEnabled() bool {
	if m.theme == nil || m.theme.NoColor {
		return false
	}
	if strings.EqualFold(m.env("TERM"), "dumb") {
		return false
	}
	return m.cfg.Mouse
}

// handleKey routes a keystroke.
func (m *Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	if m.confirm != nil {
		return m, m.handleConfirm(key)
	}
	if m.setup != nil {
		return m, m.handleSetup(key)
	}
	if m.showHelp {
		m.showHelp = false
		if key == "ctrl+c" {
			m.cancelTask()
			return m, tea.Quit
		}
		return m, nil
	}
	if key == "ctrl+c" {
		m.cancelTask()
		return m, tea.Quit
	}
	if f := m.activeForm(); f != nil && f.editing {
		return m.updateEditing(msg)
	}
	// While a table is filtering, every key belongs to the filter (so typing "q"
	// filters instead of quitting).
	if t := m.currentTable(); t != nil && t.filterOn {
		if key == "ctrl+c" {
			m.cancelTask()
			return m, tea.Quit
		}
		t.HandleKey(key)
		return m, nil
	}
	if m.running() {
		switch key {
		case "esc":
			m.cancelTask()
			m.setStatus("cancelling — nothing is published until the final stage", false)
			return m, nil
		case "q":
			m.confirm = &confirmState{
				title:  "A task is still running",
				detail: "Cancelling is safe: a build publishes only in its final stage, so no partial output is written.",
				run:    func() tea.Cmd { m.cancelTask(); return tea.Quit },
			}
			return m, nil
		}
	}

	switch key {
	case "q":
		return m, tea.Quit
	case "?":
		m.showHelp = true
		return m, nil
	case "esc":
		return m, m.escapeFallback()
	case "tab":
		m.cyclePane(1)
		return m, nil
	case "shift+tab":
		m.cyclePane(-1)
		return m, nil
	case "[":
		m.setScreen(m.screen - 1)
		return m, nil
	case "]":
		m.setScreen(m.screen + 1)
		return m, nil
	}
	if r := []rune(key); len(r) == 1 && r[0] >= '1' && r[0] <= '9' {
		idx := int(r[0] - '1')
		if idx < len(screenTitles) {
			m.setScreen(Screen(idx))
		}
		return m, nil
	}

	switch m.paneKind() {
	case paneSidebar:
		return m, m.updateSidebar(key)
	case paneActions:
		return m, m.updateActions(key)
	default:
		return m, m.updateMain(key)
	}
}

// escapeFallback implements the last level of the Escape hierarchy, reached only
// when no confirmation, guided setup, help overlay, active edit or running task
// claimed the key (each of those handles Escape before this point):
//
//   - a finished task result is dismissed so its message stops covering the screen;
//   - the Overview clears its status line (Escape never quits);
//   - every other screen returns to the screen it was opened from, falling back to
//     the Overview when there is no history.
func (m *Model) escapeFallback() tea.Cmd {
	if m.task != nil && m.task.doneFlag {
		m.task = nil
		m.status = ""
		m.statusErr = false
		m.refreshActions()
		return nil
	}
	if m.screen == ScreenDashboard {
		m.status = ""
		m.statusErr = false
		m.refreshActions()
		return nil
	}
	m.goBack()
	return nil
}

// paneRing returns the focusable areas of the current screen, in tab order.
func (m *Model) paneRing() []paneKind {
	switch m.screen {
	case ScreenDashboard:
		return []paneKind{paneSidebar, paneActions}
	case ScreenVoice:
		return []paneKind{paneSidebar, paneMain}
	default:
		return []paneKind{paneSidebar, paneMain, paneActions}
	}
}

// paneKind returns the kind of the currently focused pane.
func (m *Model) paneKind() paneKind {
	if m.pane < 0 || int(m.pane) >= len(m.kinds) {
		return paneMain
	}
	return m.kinds[m.pane]
}

// cyclePane moves focus within the ring.
func (m *Model) cyclePane(delta int) {
	if len(m.kinds) == 0 {
		m.kinds = m.paneRing()
	}
	m.pane = pane((int(m.pane) + delta + len(m.kinds)) % len(m.kinds))
}

// updateSidebar handles navigation inside the section list.
func (m *Model) updateSidebar(key string) tea.Cmd {
	if m.side.HandleKey(key) {
		return nil
	}
	switch key {
	case "enter":
		if row, ok := m.side.Selected(); ok {
			if n, err := strconv.Atoi(row.ID); err == nil {
				m.setScreen(Screen(n))
			}
		}
	}
	return nil
}

// updateActions activates the selected action of the current screen. The letter
// shown in the actions pane is a real accelerator: it works from anywhere on the
// screen, and the arrow keys plus enter always work too.
func (m *Model) updateActions(key string) tea.Cmd {
	acts := m.actions()
	if acts == nil {
		return nil
	}
	if acts.HandleKey(key) {
		return nil
	}
	if key == "enter" {
		if row, ok := acts.Selected(); ok {
			return m.runAction(row.ID)
		}
	}
	if r := []rune(key); len(r) == 1 {
		for _, row := range acts.rows {
			if len(row.Cells) > 0 && row.Cells[0] == key {
				return m.runAction(row.ID)
			}
		}
	}
	return nil
}

// handleConfirm processes a keystroke inside the modal confirmation.
func (m *Model) handleConfirm(key string) tea.Cmd {
	c := m.confirm
	if len(c.choices) > 0 {
		if key == "esc" {
			m.confirm = nil
			if c.cancel != nil {
				return c.cancel()
			}
			return nil
		}
		for _, ch := range c.choices {
			if key == ch.key {
				m.confirm = nil
				if c.pick != nil {
					return c.pick(ch.value)
				}
				return nil
			}
		}
		return nil
	}
	if c.token != "" {
		switch key {
		case "esc":
			m.confirm = nil
			return nil
		case "enter":
			if strings.TrimSpace(c.typed) == c.token {
				m.confirm = nil
				return c.run()
			}
			c.typed = ""
			return nil
		case "backspace":
			r := []rune(c.typed)
			if len(r) > 0 {
				c.typed = string(r[:len(r)-1])
			}
			return nil
		}
		if r := []rune(key); len(r) == 1 {
			c.typed += key
		}
		return nil
	}
	switch key {
	case "y", "enter":
		m.confirm = nil
		return c.run()
	default:
		m.confirm = nil
	}
	return nil
}

// updateEditing sends keystrokes to a focused form.
func (m *Model) updateEditing(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	f := m.activeForm()
	switch key {
	case "esc":
		f.End()
		return m, nil
	case "enter":
		if len(f.fields) == 1 || f.focus == len(f.fields)-1 {
			f.End()
			return m, m.submitScreen()
		}
		f.Next()
		return m, nil
	}
	return m, f.Update(msg)
}

// submitScreen runs the primary action of a screen whose form was committed with
// Enter.
func (m *Model) submitScreen() tea.Cmd {
	switch m.screen {
	case ScreenSources:
		return m.validateSource()
	case ScreenTranslations:
		return m.runImport()
	case ScreenBuild:
		return m.runBuild()
	case ScreenInstall:
		m.previewInstall()
		return nil
	case ScreenSettings:
		return m.saveSettings()
	case ScreenDiagnostics:
		// The advanced reference-data field keeps its value; nothing runs.
		return nil
	}
	return nil
}

// activeForm returns the focused form of the current screen, if any.
func (m *Model) activeForm() *form {
	switch m.screen {
	case ScreenSources:
		return m.src.form
	case ScreenTranslations:
		return m.trans.form
	case ScreenBuild:
		return m.build.form
	case ScreenInstall:
		return m.inst.form
	case ScreenDiagnostics:
		return m.diag.form
	case ScreenSettings:
		return m.set.form
	}
	return nil
}

// setScreen switches screens, wrapping around, and focuses the main pane. The
// screen it was on is pushed onto the history stack so Escape can return to it.
func (m *Model) setScreen(s Screen) { m.navigate(s, true) }

// goBack returns to the previously visited screen, or the Overview when the
// history stack is empty. It never records a new history entry.
func (m *Model) goBack() {
	if n := len(m.history); n > 0 {
		prev := m.history[n-1]
		m.history = m.history[:n-1]
		m.navigate(prev, false)
		return
	}
	if m.screen != ScreenDashboard {
		m.navigate(ScreenDashboard, false)
	}
}

// navigate switches screens. When record is true the current screen is pushed
// onto the history stack first. Leaving the Settings screen with unsaved changes
// keeps the edited values and leaves a hint on the destination screen, so a
// choice is never lost silently.
func (m *Model) navigate(s Screen, record bool) {
	leavingSettings := m.screen == ScreenSettings && m.settingsDirty()
	if s < 0 {
		s = Screen(len(screenTitles) - 1)
	}
	if int(s) >= len(screenTitles) {
		s = ScreenDashboard
	}
	if record && s != m.screen {
		m.history = append(m.history, m.screen)
	}
	m.screen = s
	m.status = ""
	m.statusErr = false
	m.kinds = m.paneRing()
	// Focus the main table so the arrow keys work immediately; the sidebar is one
	// shift+tab away and the actions pane one tab away.
	m.pane = 0
	if len(m.kinds) > 1 {
		m.pane = 1
	}
	m.side.SetCursor(strconv.Itoa(int(s)))
	m.ensure(s)
	m.syncTables()
	m.refreshActions()
	if leavingSettings && m.screen != ScreenSettings {
		m.setStatus("unsaved settings kept — open Settings and press enter to save them", false)
	}
}

// syncTables refreshes the current screen's tables so the arrow keys act on real
// data even before the first render.
func (m *Model) syncTables() {
	switch m.screen {
	case ScreenSources:
		setRowsKeepingSelection(m.src.table, m.sourceRows())
	case ScreenTranslations:
		if len(m.trans.records) > 0 {
			setRowsKeepingSelection(m.trans.table, m.translationRows())
		}
	case ScreenBuild:
		setRowsKeepingSelection(m.build.options, m.buildOptionRows())
		if m.build.last != nil {
			setRowsKeepingSelection(m.build.editions, buildRows(m.build.last))
		}
	case ScreenBuilds:
		m.scanBuilds()
	case ScreenInstall:
		m.refreshBackups()
	case ScreenSettings:
		m.refreshSettings()
	}
}

// setRowsKeepingSelection replaces rows while keeping the cursor on the same row
// ID where possible.
func setRowsKeepingSelection(t *tableModel, rows []tableRow) {
	if t == nil {
		return
	}
	sel := ""
	if row, ok := t.Selected(); ok {
		sel = row.ID
	}
	t.SetRows(rows)
	if sel != "" {
		t.SetCursor(sel)
	}
}

// ensure lazily initialises per-screen state.
func (m *Model) ensure(s Screen) {
	switch s {
	case ScreenBuilds:
		m.scanBuilds()
	}
}

// initForms builds every screen form up front. Forms are cross-referenced (the
// build screen reads the archive and reference-data fields), so they must always
// exist.
func (m *Model) initForms() {
	m.src.form = newForm(m.theme, []fieldSpec{
		{key: "cpk", label: "Game archive (read-only)", placeholder: "/path/to/Unicorn_US.CPK", width: 56},
		{key: "title_id", label: "Title ID", placeholder: build.DefaultTitleID, width: 24},
		{key: "display_version", label: "Display version", placeholder: "1.00 or 1.05", width: 12},
	})
	m.src.form.SetValues(map[string]string{
		"cpk":      m.cfg.SourceCPK,
		"title_id": m.cfg.TitleID,
	})

	m.trans.form = newForm(m.theme, []fieldSpec{
		{key: "translations", label: "Translation folder", placeholder: "translations", width: 40},
	})
	m.trans.form.SetValues(map[string]string{
		"translations": m.cfg.TranslationsDir,
	})

	m.build.form = newForm(m.theme, []fieldSpec{
		{key: "variant", label: "Edition (honorifics|no-honorifics|both)", placeholder: "both", width: 20},
		{key: "out_dir", label: "Output folder", placeholder: ".local/work/builds", width: 40},
		{key: "run_name", label: "Run name (optional)", placeholder: "(automatic)", width: 30},
	})
	m.build.form.SetValues(map[string]string{
		"variant": m.cfg.Variant,
		"out_dir": m.cfg.OutDir,
	})
	// Packaging is on by default (the normal release flow), targeting both the
	// Atmosphère and emulator layouts; the release ZIPs land in .local/release.
	// Both are cycled with the left/right arrow keys on the Build screen.
	m.build.target = build.TargetBoth
	m.build.packageOn = true

	m.inst.form = newForm(m.theme, []fieldSpec{
		{key: "root", label: "Install folder (explicit)", placeholder: "/media/sd/atmosphere", width: 56},
		{key: "title_id", label: "Title ID", placeholder: build.DefaultTitleID, width: 24},
		{key: "active_root", label: "Game folder to protect (optional)", placeholder: "(none)", width: 56},
	})
	m.inst.form.SetValues(map[string]string{"title_id": m.cfg.TitleID})

	m.set.form = newForm(m.theme, []fieldSpec{
		{key: "theme", label: "Theme (auto|light|dark)", placeholder: "auto", width: 10},
	})
	m.set.form.SetValues(map[string]string{"theme": m.cfg.Theme})

	// The reference JP/EN corpus is an advanced cross-check only: it lives behind
	// the Diagnostics advanced toggle and is never required by a normal build,
	// check or source acquisition.
	m.diag.form = newForm(m.theme, []fieldSpec{
		{key: "corpus", label: "Reference data (JP/EN corpus, optional)", placeholder: "/path/to/corpus.jsonl", width: 56},
	})
	m.diag.form.SetValues(map[string]string{"corpus": m.cfg.Corpus})
}

// initWidgets creates the sidebar, the per-screen tables and the per-screen
// actions tables.
func (m *Model) initWidgets() {
	m.side = newTable("Sections",
		tableColumn{title: "Section", width: 16, min: 8, flex: true},
		tableColumn{title: "#", width: 2, min: 2, align: alignRight},
	)
	m.side.hideMeta = true
	m.side.noFilter = true
	rows := make([]tableRow, 0, len(screenTitles))
	for i, name := range screenTitles {
		rows = append(rows, tableRow{ID: strconv.Itoa(i), Cells: []string{name, strconv.Itoa(i + 1)}})
	}
	m.side.SetRows(rows)

	m.src.table = newTable("Game files",
		tableColumn{title: "Item", width: 18, min: 14},
		tableColumn{title: "Location", width: 30, min: 16, flex: true},
		tableColumn{title: "Status", width: 12, min: 9},
	)
	m.src.table.cellStyle = m.statusStyler(2)
	m.src.table.emptyMsg = "(nothing configured yet)"

	m.trans.table = newTable("Records",
		tableColumn{title: "Record", width: 34, min: 20},
		tableColumn{title: "Status", width: 12, min: 9},
		tableColumn{title: "Notes", width: 24, min: 10, flex: true},
	)
	m.trans.table.cellStyle = m.statusStyler(1)
	m.trans.table.emptyMsg = "(run the check to list the records)"

	m.build.options = newTable("Build options",
		tableColumn{title: "Option", width: 14, min: 12},
		tableColumn{title: "Value", width: 24, min: 12, flex: true},
		tableColumn{title: "Notes", width: 24, min: 10, flex: true},
	)
	m.build.editions = newTable("Editions",
		tableColumn{title: "Edition", width: 14, min: 12},
		tableColumn{title: "Size", width: 10, min: 8, align: alignRight},
		tableColumn{title: "Checksum", width: 13, min: 13},
		tableColumn{title: "Slots", width: 13, min: 9, align: alignRight},
		tableColumn{title: "State", width: 10, min: 6, flex: true},
	)
	m.build.editions.hideMeta = true
	m.build.editions.hideHeader = true

	m.builds.table = newTable("Build runs",
		tableColumn{title: "Run", width: 26, min: 14, flex: true},
		tableColumn{title: "Editions", width: 22, min: 10},
		tableColumn{title: "Newest", width: 10, min: 8, align: alignRight},
	)
	m.builds.table.emptyMsg = "(no build runs found in the output folder)"

	m.inst.table = newTable("Stored backups",
		tableColumn{title: "Backup", width: 26, min: 14},
		tableColumn{title: "Size", width: 10, min: 8, align: alignRight},
		tableColumn{title: "Checksum", width: 13, min: 13},
		tableColumn{title: "Created", width: 17, min: 12},
	)
	m.inst.table.emptyMsg = "(no stored backups yet)"

	m.diag.table = newTable("Self-check results",
		tableColumn{title: "Check", width: 26, min: 16},
		tableColumn{title: "Result", width: 8, min: 6},
		tableColumn{title: "Detail", width: 30, min: 12, flex: true},
	)
	m.diag.table.cellStyle = m.statusStyler(1)
	m.diag.table.emptyMsg = "(no self-check has been run yet)"

	m.voice.table = newTable("Voice patch",
		tableColumn{title: "Item", width: 26, min: 16},
		tableColumn{title: "Status", width: 12, min: 8},
		tableColumn{title: "Detail", width: 30, min: 12, flex: true},
	)
	m.voice.table.cellStyle = m.statusStyler(1)
	m.voice.table.noFilter = true
	m.voice.table.SetRows(voiceItems())

	m.set.table = newTable("Settings",
		tableColumn{title: "Setting", width: 14, min: 12},
		tableColumn{title: "Value", width: 20, min: 10},
		tableColumn{title: "Description", width: 30, min: 12, flex: true},
	)
	m.set.table.noFilter = true

	for i := range screenTitles {
		a := newTable("Actions",
			tableColumn{title: "Key", width: 6, min: 6},
			tableColumn{title: "Action", width: 24, min: 10, flex: true},
			tableColumn{title: "", width: 24, min: 10, flex: true},
		)
		a.hideMeta = true
		a.hideHeader = true
		a.noFilter = true
		m.acts[i] = a
	}
}

// statusStyler colours the given column by its status word.
func (m *Model) statusStyler(col int) func(tableRow, int) lipgloss.Style {
	return func(row tableRow, c int) lipgloss.Style {
		if c == col && c < len(row.Cells) {
			return statusStyle(m.theme, row.Cells[c])
		}
		return m.theme.Cell
	}
}

// retheme rebuilds widget styles after a theme change.
func (m *Model) retheme() {
	for _, f := range []*form{m.src.form, m.trans.form, m.build.form, m.inst.form, m.set.form, m.diag.form} {
		if f == nil {
			continue
		}
		f.theme = m.theme
		for _, fl := range f.fields {
			fl.input.SetStyles(m.theme.textInputStyles())
		}
	}
	m.spin.Style = m.theme.Accent
}

// validateSource runs the shared read-only archive gate in the background.
func (m *Model) validateSource() tea.Cmd {
	src := m.src.form.Value("cpk")
	if src == "" {
		m.setStatus("choose the game archive first — press e to edit the locations", true)
		return nil
	}
	exp := build.SourceExpectation{
		TitleID:        m.src.form.Value("title_id"),
		DisplayVersion: m.src.form.Value("display_version"),
	}
	m.src.form.End()
	m.loadCfgFromFields()
	return m.startTask("archive check", func(ctx context.Context, prog func(string, int, int)) (string, any, error) {
		facts, err := build.ValidateSource(src, exp)
		if err != nil {
			return "", nil, err
		}
		prog("checked", 1, 1)
		return fmt.Sprintf("archive OK: %d members (%d game files), %s, checksum %s",
			facts.Members, facts.FMSMembers, humanBytes(facts.Size), facts.SHA256), facts, nil
	})
}

// loadCfgFromFields copies the edited fields back into the persisted settings.
func (m *Model) loadCfgFromFields() {
	if m.src.form != nil {
		m.cfg.SourceCPK = m.src.form.Value("cpk")
		if id := m.src.form.Value("title_id"); id != "" {
			m.cfg.TitleID = id
		}
	}
	if m.trans.form != nil {
		m.cfg.TranslationsDir = m.trans.form.Value("translations")
	}
	// The reference corpus is advanced-only. A non-empty value is remembered; an
	// empty advanced field never clears an already-configured corpus.
	if m.diag.form != nil {
		if c := m.diag.form.Value("corpus"); c != "" {
			m.cfg.Corpus = c
		}
	}
	if m.build.form != nil {
		if v := m.build.form.Value("variant"); v != "" {
			m.cfg.Variant = v
		}
		if o := m.build.form.Value("out_dir"); o != "" {
			m.cfg.OutDir = o
		}
	}
	if m.set.form != nil {
		if t := m.set.form.Value("theme"); t != "" {
			m.themeMode = t
			m.cfg.Theme = t
		}
	}
	m.cfg.Normalize()
}

// settingsDirty reports whether the Settings screen holds a change that has not
// been saved yet: a value typed into the form, or an option toggled with the arrow
// keys. The edited values are never discarded, only remembered until saved.
func (m *Model) settingsDirty() bool {
	if m.set.dirty {
		return true
	}
	if m.set.form == nil {
		return false
	}
	return m.set.form.Value("theme") != m.cfg.Theme
}

type settingsSavedMsg struct {
	path string
	err  error
}

// saveSettings persists the settings atomically.
func (m *Model) saveSettings() tea.Cmd {
	m.loadCfgFromFields()
	path := m.cfgPath
	if path == "" {
		path = appconfig.Path()
	}
	cfg := m.cfg
	return func() tea.Msg {
		if err := cfg.Save(path); err != nil {
			return settingsSavedMsg{err: err}
		}
		return settingsSavedMsg{path: path}
	}
}

// actions returns the actions table of the current screen.
func (m *Model) actions() *tableModel {
	if int(m.screen) < len(m.acts) {
		return m.acts[m.screen]
	}
	return nil
}

// refreshActions refreshes the action list of the current screen.
func (m *Model) refreshActions() {
	acts := m.actions()
	if acts == nil {
		return
	}
	sel := ""
	if row, ok := acts.Selected(); ok {
		sel = row.ID
	}
	acts.SetRows(m.actionRows())
	if sel != "" {
		acts.SetCursor(sel)
	}
}

// actionRow describes one entry of the actions pane.
type actionRow struct {
	id    string
	cells []string
}

// actionRows builds the current screen's action list in plain language.
func (m *Model) actionRows() []tableRow {
	row := func(id, key, label, note string) tableRow {
		return tableRow{ID: id, Cells: []string{key, label, note}}
	}
	var out []tableRow
	switch m.screen {
	case ScreenDashboard:
		out = []tableRow{
			row("goto-sources", "g", "Open Game files", "see the paths and validate the archive"),
			row("goto-translations", "t", "Open Translations", "check the translation files"),
			row("goto-build", "b", "Open Build", "choose an edition and build"),
			row("goto-diagnostics", "d", "Open Diagnostics", "run the static checks"),
			row("setup", "w", "Run first-run setup", "re-check game files, extractor, keys and cache"),
			row("help", "?", "Show help", "all keys and every screen action"),
		}
	case ScreenSources:
		out = []tableRow{
			row("validate", "enter", "Validate the archive", "read-only hash and member check"),
			row("edit", "e", "Edit locations", "type the paths to use"),
			row("goto-translations", "t", "Open Translations", "check the translation files"),
		}
	case ScreenTranslations:
		out = []tableRow{
			row("run", "enter", "Run the check", "read-only dry run, nothing is written"),
			row("edit", "e", "Edit locations", "translation folder and game archive"),
			row("goto-build", "b", "Open Build", "build once the check passes"),
		}
	case ScreenBuild:
		rowDry, rowReal := "dry", "build"
		_ = rowDry
		out = []tableRow{
			row("dry", "d", "Dry run", "validate and report; writes nothing, needs no version"),
			row("build", "b", "Build and publish", "writes a new run and, by default, the release ZIPs"),
			row("edit", "e", "Edit build options", "edition, output folder, run name"),
			row("goto-builds", "h", "Open Build history", "inspect earlier runs"),
		}
		_ = rowReal
	case ScreenBuilds:
		out = []tableRow{
			row("open", "enter", "Read the report", "show BUILD_REPORT for the selected run"),
			row("rescan", "r", "Rescan outputs", "look for new runs on disk"),
			row("goto-build", "b", "Open Build", "start a new run"),
		}
	case ScreenInstall:
		out = []tableRow{
			row("preview", "p", "Preview", "show exactly what would change"),
			row("install", "i", "Install override", "requires a typed confirmation"),
			row("uninstall", "u", "Remove override", "backs the file up first"),
			row("rollback", "o", "Roll back selected", "restore a stored backup"),
			row("edit", "e", "Edit locations", "root folder and title"),
		}
	case ScreenDiagnostics:
		out = []tableRow{
			row("qa", "enter", "Run the translation check", "static signals; no fit claim"),
			row("selfcheck", "s", "Run the self-check", "builds twice into a scratch folder"),
			row("advanced", "a", "Toggle advanced details", "show raw check output"),
		}
		if m.diag.advanced {
			out[2].Cells[2] = "hide raw check output"
			out = append(out, row("edit-corpus", "e", "Edit reference data", "advanced: optional JP/EN corpus, never required"))
		}
	case ScreenVoice:
		out = []tableRow{
			row("help", "?", "Show help", "explains the research-only status"),
		}
	case ScreenSettings:
		out = []tableRow{
			row("save", "enter", "Save settings", "store them in the checkout's .local folder"),
			row("edit", "e", "Edit values", "type a value for the selected setting"),
			row("setup", "w", "Run first-run setup", "re-inspect game files, extractor, keys and cache"),
		}
	}
	return out
}

// runAction dispatches an actions-pane row.
func (m *Model) runAction(id string) tea.Cmd {
	switch id {
	case "goto-sources":
		m.setScreen(ScreenSources)
	case "goto-translations":
		m.setScreen(ScreenTranslations)
	case "goto-build":
		m.setScreen(ScreenBuild)
	case "goto-builds":
		m.setScreen(ScreenBuilds)
	case "goto-diagnostics":
		m.setScreen(ScreenDiagnostics)
	case "help":
		m.showHelp = true
	case "setup":
		m.openSetup(false)
	case "validate":
		return m.validateSource()
	case "edit":
		return m.beginEdit()
	case "run":
		return m.runImport()
	case "dry":
		return m.runBuild()
	case "build":
		return m.confirmBuild()
	case "open":
		m.openBuildReport()
	case "rescan":
		m.scanBuilds()
		m.setStatus("build history refreshed", false)
	case "preview":
		m.previewInstall()
	case "install":
		return m.confirmInstall()
	case "uninstall":
		return m.confirmUninstall()
	case "rollback":
		return m.confirmRollback()
	case "qa":
		return m.runQA()
	case "selfcheck":
		return m.confirmSelfCheck()
	case "advanced":
		m.diag.advanced = !m.diag.advanced
		m.refreshActions()
	case "edit-corpus":
		if m.diag.advanced {
			return m.diag.form.Begin()
		}
	case "save":
		return m.saveSettings()
	}
	return nil
}

// beginEdit focuses the current screen's form.
func (m *Model) beginEdit() tea.Cmd {
	f := m.activeForm()
	if f == nil {
		return nil
	}
	return f.Begin()
}

// updateMain routes a keystroke to the current screen's primary pane.
func (m *Model) updateMain(key string) tea.Cmd {
	switch m.screen {
	case ScreenDashboard:
		// The Overview has no primary table; the actions pane is the way in.
		return nil
	case ScreenSources:
		return m.updateSources(key)
	case ScreenTranslations:
		return m.updateTranslations(key)
	case ScreenBuild:
		return m.updateBuild(key)
	case ScreenBuilds:
		return m.updateBuilds(key)
	case ScreenInstall:
		return m.updateInstall(key)
	case ScreenDiagnostics:
		return m.updateDiagnostics(key)
	case ScreenVoice:
		m.voice.table.HandleKey(key)
	case ScreenSettings:
		return m.updateSettings(key)
	}
	return nil
}

// resize propagates the terminal size to every widget.
func (m *Model) resize() {
	_, _, bodyH := m.dims()
	for _, t := range m.allTables() {
		if t == nil {
			continue
		}
		t.height = max(1, bodyH-2)
	}
}

// dims returns the sidebar width, the main pane width and the body height.
func (m *Model) dims() (sideW, mainW, bodyH int) {
	sideW = sidebarWidth
	switch {
	case m.width >= 84:
		sideW = sidebarWidth
	case m.width >= 66:
		sideW = 21
	default:
		sideW = 18
	}
	bodyH = max(3, m.height-headerLines-footerLines)
	mainW = max(20, m.width-sideW-1)
	return sideW, mainW, bodyH
}

// allTables returns every table widget so a resize reaches all of them.
func (m *Model) allTables() []*tableModel {
	out := []*tableModel{m.side, m.src.table, m.trans.table, m.build.options, m.build.editions, m.builds.table, m.inst.table, m.diag.table, m.voice.table, m.set.table}
	out = append(out, m.acts...)
	return out
}

// setStatus records a transient status line.
func (m *Model) setStatus(msg string, isErr bool) {
	m.status = msg
	m.statusErr = isErr
}

// render composes the whole screen.
func (m *Model) render() string {
	if !m.ready || m.width == 0 || m.height == 0 {
		return "uo-unburger: starting…"
	}
	if m.width < minUsableWidth || m.height < minUsableHeight {
		return m.renderCompact()
	}
	sideW, mainW, bodyH := m.dims()

	var body string
	switch {
	case m.confirm != nil:
		body = fitHeight(m.renderConfirm(min(mainW, m.width-2)), bodyH)
	case m.setup != nil:
		body = fitHeight(m.renderSetup(min(mainW, m.width-2), bodyH), bodyH)
	case m.showHelp:
		body = fitHeight(m.renderHelp(m.width-2, bodyH), bodyH)
	default:
		sidebar := m.theme.panelSized("Sections", m.sidebarBody(sideW-4), sideW, bodyH, m.paneKind() == paneSidebar)
		main := m.renderMain(mainW, bodyH)
		body = lipgloss.JoinHorizontal(lipgloss.Top, sidebar, " ", main)
	}
	out := strings.Join([]string{m.renderHeader(), body, m.renderFooter()}, "\n")
	return fitHeight(out, m.height)
}

func (m *Model) headerText() string {
	version := m.opts.Version
	if version == "" {
		version = "dev"
	}
	return fmt.Sprintf("uo-unburger %s", version)
}

// renderHeader draws the two-line title bar.
func (m *Model) renderHeader() string {
	leftPlain := "uo-unburger " + versionWord(m.opts.Version)
	rightPlain := "Unicorn Overlord translation toolkit"
	gap := m.width - runeLen(leftPlain) - runeLen(rightPlain)
	if gap < 1 {
		return m.theme.Render(m.theme.Title, truncate(leftPlain, m.width))
	}
	line1 := m.theme.Render(m.theme.Title, leftPlain) + strings.Repeat(" ", gap) + m.theme.Render(m.theme.Muted, rightPlain)
	crumb := m.theme.Render(m.theme.Accent, screenTitles[m.screen])
	sub := m.theme.Render(m.theme.Muted, "  ·  "+screenBlurbs[m.screen])
	return line1 + "\n" + truncatePlainStyled(crumb+sub, m.width)
}

// truncatePlainStyled truncates a styled line to at most width visible columns.
// It carries escape sequences along instead of cutting them, so a colour code is
// never split and the visible width stays predictable.
func truncatePlainStyled(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if visLen(s) <= width {
		return s
	}
	plain := truncate(stripANSI(s), width)
	visible := runeLen(plain)
	addEllipsis := strings.HasSuffix(plain, "…")
	copyN := visible
	if addEllipsis {
		copyN--
	}
	var b strings.Builder
	styled := false
	wrote := 0
	for i := 0; i < len(s) && wrote < copyN; {
		if s[i] == 0x1b {
			styled = true
			j := scanEscape(s, i)
			b.WriteString(s[i:j])
			i = j
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		b.WriteString(string(r))
		i += size
		wrote++
	}
	if addEllipsis {
		b.WriteString("…")
	}
	if !styled {
		return b.String()
	}
	return b.String() + "\x1b[0m"
}

// stripANSI removes CSI escape sequences (terminal colours) from s.
func stripANSI(s string) string {
	if !strings.ContainsRune(s, 0x1b) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			j := scanEscape(s, i)
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// scanEscape returns the index just past the escape sequence that starts at i.
// A CSI sequence ends at the first byte in the range 0x40–0x7E (the terminal's
// final-byte range), which includes the lowercase 'm' that ends an SGR colour.
func scanEscape(s string, i int) int {
	if i+1 < len(s) && s[i+1] == '[' {
		j := i + 2
		for j < len(s) && !(s[j] >= 0x40 && s[j] <= 0x7e) {
			j++
		}
		if j < len(s) {
			j++
		}
		return j
	}
	return i + 1
}

// visLen is the visible width of a styled string in terminal columns.
func visLen(s string) int { return runeLen(stripANSI(s)) }

// padStyled right-pads (or truncates) a possibly styled string to exactly width
// visible columns. Plain padTo cannot be used on styled content: it would count
// escape bytes as columns and truncate the text away.
func padStyled(s string, width int) string {
	if width <= 0 {
		return ""
	}
	n := visLen(s)
	switch {
	case n > width:
		return truncatePlainStyled(s, width)
	case n == width:
		return s
	}
	return s + strings.Repeat(" ", width-n)
}

// versionWord is the version shown in the header.
func versionWord(v string) string {
	if v == "" {
		return "dev"
	}
	return v
}

// renderFooter draws the contextual key hints and the status/progress line.
func (m *Model) renderFooter() string {
	statusPlain := ""
	statusStyle := m.theme.Good
	switch {
	case m.running():
		statusPlain = "  " + m.progressLine()
		statusStyle = m.theme.Warn
	case m.status != "":
		statusPlain = "  " + truncate(m.status, max(10, m.width-4))
		if m.statusErr {
			statusStyle = m.theme.Bad
		}
	}
	status := m.theme.Render(statusStyle, padTo(statusPlain, m.width))
	hints := m.theme.Render(m.theme.Desc, padTo("  "+m.footerHints(), m.width))
	rule := m.theme.Render(m.theme.Border, strings.Repeat("─", m.width))
	return status + "\n" + rule + "\n" + hints
}

// footerHints returns the keys that matter right now, in the order they matter.
// Every line states behaviour that is really implemented: the running-task,
// editing and filter states each show their own keys before the pane hints.
func (m *Model) footerHints() string {
	if m.running() {
		return "esc: cancel the running task   ↑↓: browse   ?: help   q: quit"
	}
	if f := m.activeForm(); f != nil && f.editing {
		return "tab/↑↓: next field   enter: next field, or apply on the last   esc: done (values kept)"
	}
	if t := m.currentTable(); t != nil && t.filterOn {
		return "type to filter   enter: keep the filter   esc: clear it"
	}
	switch m.paneKind() {
	case paneSidebar:
		return "↑↓: choose a section   enter: open   tab: pane   esc: back   ?: help   q: quit"
	case paneActions:
		return "↑↓: choose an action   enter: run   tab: pane   esc: back   ?: help   q: quit"
	default:
		return "↑↓: choose   ←→: change   enter: open   esc: back   tab: pane   /: filter   ?: help   q: quit"
	}
}

// currentTable returns the table of the currently focused pane, if any.
func (m *Model) currentTable() *tableModel {
	switch m.paneKind() {
	case paneSidebar:
		return m.side
	case paneActions:
		return m.actions()
	}
	switch m.screen {
	case ScreenSources:
		return m.src.table
	case ScreenTranslations:
		return m.trans.table
	case ScreenBuild:
		return m.build.options
	case ScreenBuilds:
		return m.builds.table
	case ScreenInstall:
		return m.inst.table
	case ScreenDiagnostics:
		return m.diag.table
	case ScreenVoice:
		return m.voice.table
	case ScreenSettings:
		return m.set.table
	}
	return nil
}

// sidebarBody renders the section list body. The section column is sized to the
// frame so a section name is never pushed into the horizontal-scroll path, and the
// number stays a subtle suffix rather than the primary affordance.
func (m *Model) sidebarBody(inner int) string {
	m.side.width = inner
	width := max(6, inner-6) // marker (2) + gap (2) + number column (2)
	m.side.columns[0].width = width
	m.side.columns[0].min = width
	m.side.columns[0].flex = false
	m.side.SetCursor(strconv.Itoa(int(m.screen)))
	return m.side.View(m.theme, len(screenTitles)+2, inner)
}

// renderMain draws the body of the current screen.
func (m *Model) renderMain(w, h int) string {
	m.mouseHit = nil
	var content string
	switch m.screen {
	case ScreenDashboard:
		content = m.viewDashboard(w, h)
	case ScreenSources:
		content = m.viewSources(w, h)
	case ScreenTranslations:
		content = m.viewTranslations(w, h)
	case ScreenBuild:
		content = m.viewBuild(w, h)
	case ScreenBuilds:
		content = m.viewBuilds(w, h)
	case ScreenInstall:
		content = m.viewInstall(w, h)
	case ScreenDiagnostics:
		content = m.viewDiagnostics(w, h)
	case ScreenVoice:
		content = m.viewVoice(w, h)
	case ScreenSettings:
		content = m.viewSettings(w, h)
	}
	return fitHeight(content, h)
}

func (m *Model) renderCompact() string {
	w := max(1, m.width)
	reason := "Terminal too small"
	if w < runeLen(reason) {
		reason = "too small"
	}
	lines := []string{m.theme.Render(m.theme.Title, truncate("uo-unburger", w))}
	lines = append(lines, wrap(reason, w)...)
	if m.width > 0 && m.height > 0 {
		lines = append(lines, wrap(fmt.Sprintf("%dx%d; need %dx%d",
			m.width, m.height, minUsableWidth, minUsableHeight), w)...)
	}
	lines = append(lines, wrap("resize, or press q to quit", w)...)
	if m.showHelp {
		lines = append(lines, wrap("↑↓ move   ←→ change   tab pane   enter open   esc back   ? close   q quit", w)...)
	}
	if m.setup != nil {
		lines = append(lines, wrap("guided setup is waiting — enlarge the terminal to use it, or press esc to skip", w)...)
	}
	if m.running() {
		lines = append(lines, wrap("working… esc cancels", w)...)
	}
	if m.confirm != nil {
		lines = append(lines, wrap("confirmation pending — "+m.confirm.title, w)...)
	}
	return fitWidth(strings.Join(lines, "\n"), m.width)
}

// renderHelp draws the help overlay.
func (m *Model) renderHelp(w, h int) string {
	var b strings.Builder
	b.WriteString(m.theme.Render(m.theme.Sub, "Move around"))
	b.WriteString("\n")
	rows := [][2]string{
		{"↑ ↓ or j k", "move the selection in the focused table"},
		{"← →", "change the selected option, or scroll table columns"},
		{"enter", "open the selected row, run the action, or confirm a dialog"},
		{"tab / shift+tab", "move focus: sidebar ↔ table ↔ actions"},
		{"[ / ]", "previous / next section"},
		{"1 … 9", "jump to a section (a shortcut, never required)"},
		{"/", "filter the focused table (enter keeps it, esc clears it)"},
		{"pgup pgdn home end", "fast scroll inside the focused table"},
		{"?", "this help overlay"},
		{"q / ctrl+c", "quit"},
	}
	for _, r := range rows {
		b.WriteString("  " + m.theme.Render(m.theme.Key, padTo(r[0], 22)))
		b.WriteString(m.theme.Render(m.theme.Desc, r[1]))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(m.theme.Render(m.theme.Sub, "What esc does, in order"))
	b.WriteString("\n")
	for _, line := range []string{
		"dismiss a confirmation, or close this help overlay;",
		"finish editing — the values you typed are kept, nothing is saved yet;",
		"cancel a running task (a build publishes only in its final stage, so no partial output is written);",
		"otherwise go back one screen. The Overview clears its status line; esc never quits.",
	} {
		b.WriteString("  " + m.theme.Render(m.theme.Desc, truncate(line, max(10, w-4))) + "\n")
	}
	b.WriteString("\n")
	b.WriteString(m.theme.Render(m.theme.Sub, "On the "+screenTitles[m.screen]+" screen"))
	b.WriteString("\n")
	for _, r := range m.helpActions() {
		b.WriteString("  " + m.theme.Render(m.theme.Key, padTo(r[0], 22)))
		b.WriteString(m.theme.Render(m.theme.Desc, r[1]))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(m.theme.Render(m.theme.Sub, "Safety"))
	b.WriteString("\n")
	for _, line := range []string{
		"Game files are opened read-only: hashed and parsed, never modified.",
		"A build publishes atomically into its own folder; a failure leaves no partial output.",
		"A packaged build needs a non-empty root version.txt; without it it stops before writing.",
		"Packaged release ZIPs land in .local/release (default target: both layouts).",
		"The installer writes only the exact override file under the root you type, and backs up first.",
		"No game support is claimed and no runtime acceptance has been verified.",
	} {
		b.WriteString("  " + m.theme.Render(m.theme.Muted, truncate(line, max(10, w-4))) + "\n")
	}
	return m.theme.panelSized("Help", strings.TrimRight(b.String(), "\n"), min(max(40, w), 100), h, true)
}

// helpActions lists the current screen's actions for the help overlay.
func (m *Model) helpActions() [][2]string {
	var out [][2]string
	for _, r := range m.actionRows() {
		if len(r.Cells) >= 3 {
			out = append(out, [2]string{r.Cells[0], r.Cells[1] + " — " + r.Cells[2]})
		}
	}
	return out
}

func (m *Model) renderConfirm(w int) string {
	c := m.confirm
	var b strings.Builder
	b.WriteString(m.theme.Render(m.theme.Warn, "Please confirm"))
	b.WriteString("\n\n")
	b.WriteString(m.theme.Render(m.theme.Heading, c.title))
	b.WriteString("\n\n")
	if c.detail != "" {
		for _, ln := range wrap(c.detail, max(10, w-6)) {
			b.WriteString(ln)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	if len(c.choices) > 0 {
		for _, ch := range c.choices {
			b.WriteString(fmt.Sprintf("  %s   %s\n", m.theme.Render(m.theme.Key, ch.key), ch.label))
		}
		b.WriteString("\n")
		b.WriteString(m.theme.Render(m.theme.Desc, "press a key to choose   esc: cancel"))
	} else if c.token != "" {
		b.WriteString(fmt.Sprintf("Type %s to confirm: %s_", m.theme.Render(m.theme.Key, c.token), c.typed))
		b.WriteString("\n\n")
		b.WriteString(m.theme.Render(m.theme.Desc, "enter: confirm   esc: cancel"))
	} else {
		b.WriteString(m.theme.Render(m.theme.Desc, "enter / y: confirm   esc / n: cancel"))
	}
	return m.theme.panelAuto("Confirm", strings.TrimRight(b.String(), "\n"), min(max(40, w), 72), true)
}

// afterTask refreshes whatever the finished task may have changed.
func (m *Model) afterTask() {
	if m.task == nil {
		return
	}
	switch {
	case strings.HasPrefix(m.task.label, "build "):
		m.scanBuilds()
	case strings.HasPrefix(m.task.label, "install"), strings.HasPrefix(m.task.label, "uninstall"),
		strings.HasPrefix(m.task.label, "rollback"):
		m.refreshBackups()
	}
	m.refreshActions()
}

// ---- mouse ----------------------------------------------------------------

func (m *Model) handleMouseWheel(msg tea.MouseWheelMsg) tea.Cmd {
	if m.mouseHit == nil || m.mouseHit.table == nil {
		return nil
	}
	switch msg.Button {
	case tea.MouseWheelUp:
		m.mouseHit.table.Move(-3)
	case tea.MouseWheelDown:
		m.mouseHit.table.Move(3)
	}
	return nil
}

func (m *Model) handleMouseClick(msg tea.MouseClickMsg) tea.Cmd {
	hit := m.mouseHit
	if hit == nil || hit.table == nil {
		return nil
	}
	if msg.Button != tea.MouseLeft {
		return nil
	}
	if msg.X < hit.originX || msg.X >= hit.originX+hit.table.width {
		return nil
	}
	row := msg.Y - hit.rowY
	if row < 0 || row >= hit.table.maxRows() {
		return nil
	}
	hit.table.cursor = hit.table.offset + row
	hit.table.clampOffset()
	return nil
}

// ---- shared helpers --------------------------------------------------------

// display shortens a path for display without changing it.
func display(p string) string {
	if p == "" {
		return ""
	}
	return paths.Display(p)
}

func displayOr(p, fallback string) string {
	if strings.TrimSpace(p) == "" {
		return fallback
	}
	return display(p)
}

func boolWord(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func paletteWord(dark bool) string {
	if dark {
		return "dark"
	}
	return "light"
}

func statusWord(dryRun bool) string {
	if dryRun {
		return "dry run complete"
	}
	return "build published"
}

func passWord(ok bool) string {
	if ok {
		return "PASS"
	}
	return "FAIL"
}

func shortSHA(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[:12]
}

func choose(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// fitHeight truncates a block to at most h lines.
func fitHeight(s string, h int) string {
	if h <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= h {
		return s
	}
	return strings.Join(lines[:h], "\n")
}

// sortStrings is a tiny wrapper so screens do not import sort directly.
func sortStrings(v []string) { sort.Strings(v) }

// buildRows lists the artifact paths of a finished build, newest edition first.
func buildRows(res *build.Result) []tableRow {
	items := make([]tableRow, 0, len(res.Editions))
	for _, e := range res.Editions {
		items = append(items, tableRow{
			ID: string(e.Variant),
			Cells: []string{
				string(e.Variant),
				humanBytes(e.Size),
				shortSHA(e.SHA256),
				fmt.Sprintf("%d/%d", e.ChangedSlots, e.PlannedSlots),
				editionStateWord(e),
			},
			Data: e,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}

func editionStateWord(e build.EditionResult) string {
	if e.Published {
		return "published"
	}
	return "dry run"
}

// scanBuilds lists the build output directory read-only.
func (m *Model) scanBuilds() {
	outDir := m.buildOutDir()
	entries, err := os.ReadDir(outDir)
	if err != nil {
		m.builds.table.SetRows(nil)
		return
	}
	var items []tableRow
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		run := filepath.Join(outDir, e.Name())
		var editions []string
		var newest string
		for _, v := range []string{string(build.VariantHonorifics), string(build.VariantNoHonorifics)} {
			artifact := filepath.Join(run, v, filepath.FromSlash(installer.ArtifactRel))
			if info, err := os.Stat(artifact); err == nil {
				editions = append(editions, v)
				newest = humanBytes(info.Size())
			}
		}
		sortStrings(editions)
		state := "no artifact"
		if len(editions) > 0 {
			state = strings.Join(editions, " · ")
		}
		items = append(items, tableRow{
			ID:    e.Name(),
			Cells: []string{e.Name(), state, newest},
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID > items[j].ID })
	m.builds.table.SetRows(items)
}

// buildOutDir resolves the build output root from the fields or config.
func (m *Model) buildOutDir() string {
	if o := m.build.form.Value("out_dir"); o != "" {
		return o
	}
	return m.cfg.OutDir
}

// refreshBackups lists stored installer backups.
func (m *Model) refreshBackups() {
	opts, err := m.installerOptions()
	if err != nil {
		m.inst.table.SetRows(nil)
		return
	}
	backups, err := installer.Backups(opts)
	if err != nil {
		m.inst.table.SetRows(nil)
		return
	}
	items := make([]tableRow, 0, len(backups))
	for _, b := range backups {
		when := "—"
		if !b.Time.IsZero() {
			when = b.Time.Format("2006-01-02 15:04")
		}
		items = append(items, tableRow{
			ID:    b.Path,
			Cells: []string{filepath.Base(b.Path), humanBytes(b.Size), shortSHA(b.SHA256), when},
		})
	}
	m.inst.table.SetRows(items)
}

// installerOptions builds installer options from the Install screen fields.
func (m *Model) installerOptions() (installer.Options, error) {
	vals := m.inst.form.All()
	if vals["root"] == "" {
		return installer.Options{}, installer.ErrNoRoot
	}
	titleID := vals["title_id"]
	if titleID == "" {
		titleID = m.cfg.TitleID
	}
	var protected []string
	if vals["active_root"] != "" {
		protected = append(protected, vals["active_root"])
	}
	return installer.Options{
		Root:           vals["root"],
		TitleID:        titleID,
		ProtectedRoots: protected,
	}, nil
}

// settingsRows builds the Settings table.
func (m *Model) refreshSettings() {
	if m.set.table == nil {
		return
	}
	cfgPath := m.cfgPath
	if cfgPath == "" {
		cfgPath = appconfig.Path()
	}
	state := "(not created yet)"
	if cfgPath != "" {
		if _, err := os.Stat(cfgPath); err == nil {
			state = "(exists)"
		}
	}
	mouse := "off — keyboard only; the terminal keeps its own mouse handling"
	if m.cfg.Mouse {
		if m.mouseEnabled() {
			mouse = "on — wheel scrolls, click selects a row"
		} else {
			mouse = "on, but disabled while colour is off"
		}
	}
	color := "on"
	if m.theme.NoColor {
		color = "off (NO_COLOR or TERM=dumb)"
	}
	rows := []tableRow{
		{ID: "theme", Cells: []string{"Theme", m.cfg.Theme, "auto follows the terminal background"}},
		{ID: "mouse", Cells: []string{"Mouse", boolWord(m.cfg.Mouse), mouse}},
		{ID: "config", Cells: []string{"Settings file", display(cfgPath), state}},
	}
	rootCell, cacheCell := "(not found)", "(unknown)"
	if root, err := project.FindRoot(); err == nil {
		rootCell = display(root)
		cacheCell = display(project.NewLayout(root).CacheDir)
	}
	setupCell := "not run"
	if m.cfg.SetupComplete {
		setupCell = "complete"
	}
	rows = append(rows,
		tableRow{ID: "project", Cells: []string{"Project root", rootCell, "found by walking up to go.mod + translations"}},
		tableRow{ID: "cache", Cells: []string{"Extraction cache", cacheCell, "verified entries are reused; damaged ones are quarantined"}},
		tableRow{ID: "setup", Cells: []string{"First-run setup", setupCell, "re-run it from the Actions pane"}},
		tableRow{ID: "palette", Cells: []string{"Palette", paletteWord(m.theme.Dark), "chosen from the terminal background"}},
		tableRow{ID: "color", Cells: []string{"Colour", color, "NO_COLOR and TERM=dumb always force plain text"}},
	)
	m.set.table.SetRows(rows)
}
