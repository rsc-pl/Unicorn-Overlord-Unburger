package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"charm.land/bubbletea/v2"

	"uo-unburger/internal/appconfig"
	"uo-unburger/internal/paths"
	"uo-unburger/internal/project"
	"uo-unburger/internal/source"
)

// setupSteps are the guided first-run steps, in order. Keeping the six familiar
// steps preserves the screen's appearance; several of them are now interactive
// (a source choice, an extractor path and an explicit extraction).
var setupSteps = []string{
	"Welcome",
	"Game files",
	"Extractor",
	"Keys",
	"Cache",
	"Ready",
}

// setupChoice is one selectable way to obtain the localization archive.
type setupChoice struct {
	Label        string
	Path         string
	Provenance   string
	NeedsExtract bool
}

// setupInfo is the read-only environment snapshot the flow reports. It is gathered
// when the flow opens and re-gathered on demand (r / after an extraction).
type setupInfo struct {
	State          source.State
	TranslationsOK bool
}

// setupState is the guided first-run flow.
type setupState struct {
	step    int
	info    setupInfo
	choices []setupChoice
	choice  int

	// Extractor path editing on step 2.
	editExtractor  bool
	extractorInput string

	// ready is true when every required resource is present (so a first run skips
	// the flow); completed is the gate that must be met before setup_complete is
	// recorded.
	ready     bool
	completed bool
}

// gatherSetupInfo inspects the project-local environment. It never extracts and
// never writes.
func gatherSetupInfo(cfg appconfig.Config) setupInfo {
	st := source.Inspect(context.Background(), cfg)
	return setupInfo{State: st, TranslationsOK: translationsPresent(st.Root, cfg.TranslationsDir)}
}

// translationsPresent reports whether the translation table exists, resolving a
// relative directory against the checkout root.
func translationsPresent(root, dir string) bool {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		dir = "translations"
	}
	if !filepath.IsAbs(dir) {
		if root == "" {
			return false
		}
		dir = filepath.Join(root, dir)
	}
	if info, err := os.Stat(filepath.Join(dir, "honorifics.tsv")); err == nil && info.Mode().IsRegular() {
		return true
	}
	return false
}

// buildChoices lists the ways the archive can be obtained, verified sources first.
func buildChoices(info setupInfo) []setupChoice {
	var out []setupChoice
	st := info.State
	if st.CacheOK && st.Source != "" {
		out = append(out, setupChoice{Label: "verified cache entry (ready to reuse)", Path: st.Source, Provenance: source.ProvenanceCache})
	}
	for _, c := range st.Workspace {
		if c.Verified {
			out = append(out, setupChoice{Label: "verified archive in the work folder", Path: c.Path, Provenance: source.ProvenanceWorkspace})
		}
	}
	if st.Pick != nil {
		out = append(out, setupChoice{Label: "game container (extraction required)", Path: st.Pick.Path, Provenance: source.ProvenanceExtracted, NeedsExtract: true})
	}
	return out
}

// newSetup starts the guided flow, gathering the environment once. Opening the
// flow also prepares the project-local layout (creating only missing directories,
// never deleting anything).
func newSetup(cfg appconfig.Config) *setupState {
	if root, err := project.FindRoot(); err == nil {
		_, _ = project.NewLayout(root).Ensure()
	}
	info := gatherSetupInfo(cfg)
	s := &setupState{info: info, choices: buildChoices(info)}
	// Prefer an already-verified source; only fall back to a container.
	for i, c := range s.choices {
		if !c.NeedsExtract {
			s.choice = i
			break
		}
	}
	s.evaluate(cfg)
	return s
}

// selected returns the highlighted choice, or nil when there are none.
func (s *setupState) selected() *setupChoice {
	if s.choice >= 0 && s.choice < len(s.choices) {
		return &s.choices[s.choice]
	}
	return nil
}

// evaluate recomputes the readiness gate: setup_complete may only be recorded when
// a verified source archive and the translation files are both present.
func (s *setupState) evaluate(cfg appconfig.Config) {
	sourceVerified := s.info.State.Source != ""
	if c := s.selected(); c != nil && !c.NeedsExtract && c.Path != "" {
		sourceVerified = true
	}
	if p := strings.TrimSpace(cfg.SourceCPK); p != "" {
		if _, err := os.Stat(p); err == nil {
			sourceVerified = true
		}
	}
	s.completed = s.info.State.RootErr == nil && sourceVerified && s.info.TranslationsOK
	s.ready = s.completed
}

// renderSetup draws the guided flow, reusing the same panel chrome as every other
// overlay.
func (m *Model) renderSetup(w, h int) string {
	st := m.setup
	title := fmt.Sprintf("First-run setup — %d/%d: %s", st.step+1, len(setupSteps), setupSteps[st.step])
	inner := min(max(40, w), 90)
	body := st.body(max(10, inner-4)) + "\n\n" + m.theme.Render(m.theme.Desc, st.footer())
	return m.theme.panelSized(title, body, inner, h, true)
}

// footer states the keys that are actually handled on this step.
func (s *setupState) footer() string {
	if s.editExtractor {
		return "  type the extractor path   enter: apply   esc: cancel"
	}
	switch s.step {
	case 1:
		return "  ← →: choose the game archive   enter: continue   esc: back (finishes from the first step)"
	case 2:
		return "  e: set the extractor path   enter: continue   esc: back"
	case 4:
		return "  c: extract now (asks first)   r: re-check   enter: continue   esc: back"
	case len(setupSteps) - 1:
		return "  enter: finish and save settings   esc: back"
	}
	return "  enter: continue   esc: back (finishes from the first step)"
}

// body renders the current step's text.
func (s *setupState) body(width int) string {
	line := func(key, value string) string {
		return "  " + padTo(key, 18) + truncate(value, max(10, width-20))
	}
	info := s.info
	st := info.State
	if st.RootErr != nil {
		return "  No project root was found:\n\n" +
			line("Reason", paths.Error(st.RootErr)) + "\n" +
			line("Remedy", "run from inside the checkout, or set "+project.RootEnv)
	}
	switch s.step {
	case 0:
		return strings.Join([]string{
			"  This guided setup checks what the toolkit can see and saves working",
			"  settings. Nothing leaves the checkout; key contents are never read.",
			"",
			line("Project root", paths.Display(st.Root)),
			line("Settings file", paths.Display(st.Layout.ConfigFile)),
			line("Work folder", paths.Display(st.Layout.WorkDir)),
			line("Cache folder", paths.Display(st.Layout.CacheDir)),
		}, "\n")
	case 1:
		var b strings.Builder
		if len(st.Discovery.Containers) == 0 {
			b.WriteString("  No game container was found.\n")
			b.WriteString(line("Expected in", paths.Display(st.Layout.RomBase)+" and "+paths.Display(st.Layout.RomUpdate)))
		} else {
			for _, c := range st.Discovery.Containers {
				state := "recognised"
				if !c.Valid() {
					state = "not recognised"
				}
				fmt.Fprintf(&b, "  - %-8s %-15s %s\n", c.Role, state, truncate(filepath.Base(c.Path), width-30))
			}
		}
		b.WriteString("\n  Already-verified archives:\n")
		verified := 0
		for _, c := range st.Workspace {
			if c.Verified {
				verified++
				b.WriteString("  - " + truncate(paths.Display(c.Path), width-6) + "\n")
			}
		}
		if st.CacheOK && st.Source != "" {
			verified++
			b.WriteString("  - " + truncate(paths.Display(st.Source), width-6) + " (cache)\n")
		}
		if verified == 0 {
			b.WriteString("  - none yet\n")
		}
		b.WriteString("\n")
		if c := s.selected(); c != nil {
			b.WriteString(line("Use", c.Label))
			if c.NeedsExtract {
				b.WriteString(line("From", paths.Display(c.Path)))
			} else {
				b.WriteString(line("Archive", paths.Display(c.Path)))
			}
		} else {
			b.WriteString("  No usable archive or container was recognised yet.")
		}
		return strings.TrimRight(b.String(), "\n")
	case 2:
		if s.editExtractor {
			return strings.Join([]string{
				"  Type the path to your extractor (nstool or hactool):",
				"",
				"  " + s.extractorInput + "_",
			}, "\n")
		}
		if st.ToolErr != nil {
			return strings.Join([]string{
				"  No external extractor was found on PATH.",
				"",
				line("Needed", "nstool or hactool"),
				line("Or set", "an explicit path here (press e)"),
				line("Used for", "reading the resources from your own game container"),
			}, "\n")
		}
		return strings.Join([]string{
			"  An external extractor is available.",
			"",
			line("Tool", st.Tool.Name+" "+st.Tool.Version),
			line("Found via", st.Tool.Source),
			line("Game cards", boolWord(st.Tool.SupportsXCI)),
			"",
			"  Press e to override the path if you prefer another build.",
		}, "\n")
	case 3:
		var b strings.Builder
		if st.Keys.ProdKeys {
			b.WriteString("  The required key file was found.\n")
		} else {
			b.WriteString("  The required key file prod.keys was not found.\n")
		}
		b.WriteString("\n" + line("prod.keys", presentWord(st.Keys.ProdKeys)+" (required)"))
		b.WriteString("\n" + line("title.keys", presentWord(st.Keys.TitleKeys)+" (optional)"))
		b.WriteString("\n" + line("Folder", paths.Display(st.Layout.KeysDir)))
		if !st.Keys.ProdKeys {
			b.WriteString("\n\n  Place your own prod.keys in that folder.\n  Key contents are never read or printed.")
		}
		return b.String()
	case 4:
		name := "—"
		if st.Pick != nil {
			name = fmt.Sprintf("%s-%d", strings.ToUpper(st.Pick.TitleID), st.Pick.RawVersion)
		}
		var b strings.Builder
		switch {
		case st.Source != "":
			b.WriteString("  A verified archive is ready to reuse; nothing is extracted.\n\n")
			b.WriteString(line("Entry", name) + "\n")
			b.WriteString(line("Archive", paths.Display(st.Source)))
		case st.CacheErr != "":
			b.WriteString("  The cache entry exists but is damaged.\n\n")
			b.WriteString(line("Entry", name) + "\n" + line("Detail", st.CacheErr))
		default:
			b.WriteString("  No verified archive is present yet.\n\n")
			b.WriteString(line("Entry", name) + "\n")
			if st.Pick != nil {
				b.WriteString("\n  Press c to extract the required resources from\n  " +
					truncate(paths.Display(st.Pick.Path), width-6) + "\n  (a confirmation is asked first).")
			} else {
				b.WriteString("\n  Place your game container under " + paths.Display(st.Layout.RomBase) + " first.")
			}
		}
		return b.String()
	case 5:
		var rows []string
		if s.completed {
			rows = append(rows, "  Everything a build needs is ready. Setup is complete.", "")
		} else {
			rows = append(rows, "  Setup is not complete yet. Missing:", "")
			if st.Source == "" {
				rows = append(rows, "  - a verified game archive (see the Game files and Cache steps)")
			}
			if !info.TranslationsOK {
				rows = append(rows, "  - the translation files (honorifics.tsv) in the translation folder")
			}
			rows = append(rows, "", "  Finishing now saves your choices; setup opens again next launch")
			rows = append(rows, "  until a verified archive and the translations are present.")
		}
		rows = append(rows, "", line("Settings file", paths.Display(st.Layout.ConfigFile)))
		if st.Source != "" {
			rows = append(rows, line("Source archive", paths.Display(st.Source)))
		}
		return strings.Join(rows, "\n")
	}
	return ""
}

func presentWord(b bool) string {
	if b {
		return "present"
	}
	return "missing"
}

// handleSetup processes a keystroke inside the guided setup.
func (m *Model) handleSetup(key string) tea.Cmd {
	st := m.setup
	if m.running() {
		if key == "esc" {
			m.cancelTask()
			m.setStatus("cancelling — nothing is published yet", false)
		}
		return nil
	}
	if st.editExtractor {
		return m.handleExtractorEdit(key)
	}
	switch key {
	case "enter":
		if st.step == len(setupSteps)-1 {
			return m.finishSetup()
		}
		st.step++
		return nil
	case "esc":
		if st.step == 0 {
			return m.finishSetup()
		}
		st.step--
		return nil
	case "left", "right":
		m.cycleSetupChoice(key)
		return nil
	case "e":
		if st.step == 2 {
			st.editExtractor = true
			st.extractorInput = m.cfg.Extractor
		}
		return nil
	case "c":
		if st.step == 4 {
			return m.confirmSetupExtract()
		}
		return nil
	case "r":
		if st.step == 4 {
			m.refreshSetup()
			m.setStatus("re-checked the game files, extractor and cache", false)
		}
		return nil
	case "ctrl+c":
		m.cancelTask()
		return tea.Quit
	}
	return nil
}

// handleExtractorEdit edits the extractor path on the Extractor step.
func (m *Model) handleExtractorEdit(key string) tea.Cmd {
	st := m.setup
	switch key {
	case "esc":
		st.editExtractor = false
		return nil
	case "enter":
		m.cfg.Extractor = strings.TrimSpace(st.extractorInput)
		m.cfg.Normalize()
		st.editExtractor = false
		m.refreshSetup()
		m.setStatus("extractor path set — the extractor was re-detected", false)
		return nil
	case "backspace":
		r := []rune(st.extractorInput)
		if len(r) > 0 {
			st.extractorInput = string(r[:len(r)-1])
		}
		return nil
	}
	if r := []rune(key); len(r) == 1 {
		st.extractorInput += key
	}
	return nil
}

// cycleSetupChoice moves the highlighted source choice.
func (m *Model) cycleSetupChoice(key string) {
	st := m.setup
	if len(st.choices) == 0 {
		return
	}
	if key == "left" {
		st.choice = (st.choice - 1 + len(st.choices)) % len(st.choices)
	} else {
		st.choice = (st.choice + 1) % len(st.choices)
	}
	st.evaluate(m.cfg)
}

// openSetup opens the guided flow. When skipWhenReady is true (a first run) and
// every required resource is already present, the flow is skipped entirely and a
// status line records why.
func (m *Model) openSetup(skipWhenReady bool) {
	st := newSetup(m.cfg)
	if skipWhenReady && st.ready {
		m.cfg.SetupComplete = true
		m.refreshSettings()
		path := m.cfgPath
		if path == "" {
			path = appconfig.Path()
		}
		if path != "" {
			_ = m.cfg.Save(path)
		}
		m.setStatus("setup not needed — a verified game archive and the translation files were found", false)
		return
	}
	m.setup = st
}

// refreshSetup re-gathers the environment after a change (extractor path set,
// extraction finished, manual re-check).
func (m *Model) refreshSetup() {
	if m.setup == nil {
		return
	}
	m.setup.info = gatherSetupInfo(m.cfg)
	m.setup.choices = buildChoices(m.setup.info)
	if m.setup.choice >= len(m.setup.choices) {
		m.setup.choice = 0
	}
	for i, c := range m.setup.choices {
		if !c.NeedsExtract {
			m.setup.choice = i
			break
		}
	}
	m.setup.evaluate(m.cfg)
}

// confirmSetupExtract asks before the extractor runs, then starts it.
func (m *Model) confirmSetupExtract() tea.Cmd {
	st := m.setup.info.State
	if st.Source != "" {
		m.setStatus("a verified game archive is already available — nothing to extract", false)
		return nil
	}
	switch {
	case st.Pick == nil:
		m.setStatus("no game container was found to extract from", true)
		return nil
	case st.ToolErr != nil:
		m.setStatus("no extractor is available — set its path on the Extractor step", true)
		return nil
	case !st.Keys.Required():
		m.setStatus("the required key file prod.keys was not found — see the Keys step", true)
		return nil
	}
	m.confirm = &confirmState{
		title:  "Prepare the game archive now?",
		detail: "Extract the resources a build needs from " + filepath.Base(st.Pick.Path) + " with " + st.Tool.Name + "? Your container is only read; nothing is installed.",
		run:    func() tea.Cmd { return m.setupExtract() },
	}
	return nil
}

// setupResolved carries a finished setup extraction back to the UI goroutine.
type setupResolved struct {
	res source.Resolution
}

// setupExtract runs the extractor for the guided flow and commits the cache entry.
func (m *Model) setupExtract() tea.Cmd {
	cfg := m.cfg
	return m.startTask("prepare game archive", func(ctx context.Context, prog func(string, int, int)) (string, any, error) {
		prog("extracting the required resources", 1, 2)
		res, err := source.Resolve(ctx, source.Options{Config: cfg, AllowExtract: true})
		if err != nil {
			return "", nil, err
		}
		if err := ctx.Err(); err != nil {
			return "", nil, err
		}
		prog("done", 2, 2)
		return "game archive prepared from your game container", setupResolved{res: res}, nil
	})
}

// finishSetup records the chosen archive and the extractor path, and records
// setup_complete only when a verified source and the translation files are both
// present, then saves the project-local settings.
func (m *Model) finishSetup() tea.Cmd {
	st := m.setup
	m.setup = nil
	if st == nil {
		return nil
	}
	if c := st.selected(); c != nil && !c.NeedsExtract && c.Path != "" {
		m.cfg.SourceCPK = c.Path
		if m.src.form != nil {
			m.src.form.SetValue("cpk", c.Path)
		}
	} else if st.info.State.Source != "" && strings.TrimSpace(m.cfg.SourceCPK) == "" {
		m.cfg.SourceCPK = st.info.State.Source
		if m.src.form != nil {
			m.src.form.SetValue("cpk", st.info.State.Source)
		}
	}
	st.evaluate(m.cfg)
	m.cfg.SetupComplete = st.completed
	m.refreshSettings()
	if st.completed {
		m.setStatus("setup complete — the game archive and the translation files are ready", false)
	} else {
		var missing []string
		if st.info.State.Source == "" {
			missing = append(missing, "a verified game archive")
		}
		if !st.info.TranslationsOK {
			missing = append(missing, "the translation files")
		}
		if len(missing) == 0 {
			missing = append(missing, "nothing recorded yet — re-check the game files")
		}
		m.setStatus("setup is not complete yet — still required: "+strings.Join(missing, " and "), true)
	}
	return m.saveSettings()
}
