package tui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"charm.land/bubbletea/v2"

	"uo-unburger/internal/appconfig"
	"uo-unburger/internal/build"
	"uo-unburger/internal/installer"
	"uo-unburger/internal/preflight"
	"uo-unburger/internal/project"
	"uo-unburger/internal/qa"
	"uo-unburger/internal/source"
)

// buildEditions is the edition cycle offered on the Build screen.
var buildEditions = []string{
	string(build.VariantHonorifics),
	string(build.VariantNoHonorifics),
	string(build.VariantBoth),
}

// viewBuild renders the Build screen.
func (m *Model) viewBuild(w, h int) string {
	inner := max(4, w-4)
	if m.build.form.editing {
		return m.theme.stackPanels(w, h, []panelSpec{
			{title: "Edit build options", body: m.build.form.View(inner), focus: true},
			{title: "Keys", body: m.theme.Render(m.theme.Desc, "  tab: next field   enter: apply   esc: done (values kept)"), height: 3},
		})
	}
	options := m.build.options
	options.width = inner
	options.SetRows(m.buildOptionRows())
	optionHeight := max(5, h-14)

	editions := m.build.editions
	editions.width = inner
	lastBody := ""
	if m.build.last != nil {
		res := m.build.last
		lastBody = fmt.Sprintf("  %s — %s\n", statusWord(res.DryRun), res.RunName) +
			m.theme.Render(m.theme.Desc, fmt.Sprintf("  %d records applied · %d no-op",
				res.Plan.AppliedRecords, res.NoOpRecords)) + "\n" +
			editions.View(m.theme, max(2, len(editions.rows)+2), inner)
	} else {
		lastBody = m.theme.Render(m.theme.Muted, "  No run yet in this session. A dry run writes nothing.")
	}
	if m.build.err != "" {
		lastBody = m.theme.Render(m.theme.Bad, "  "+truncate(m.build.err, inner-4))
	}

	actions := m.actions()
	actionBody := ""
	if actions != nil {
		actions.width = inner
		actionBody = actions.View(m.theme, max(1, len(m.actionRows())), inner)
	}
	return m.theme.stackPanels(w, h, []panelSpec{
		{title: "Build options", body: options.View(m.theme, optionHeight, inner), height: optionHeight, focus: m.paneKind() == paneMain},
		{title: "Last run", body: lastBody, height: max(6, h-optionHeight-8)},
		{title: "Actions", body: actionBody, focus: m.paneKind() == paneActions},
	})
}

// buildTargets is the packaging target cycle offered on the Build screen.
var buildTargets = []string{
	string(build.TargetAtmosphere),
	string(build.TargetEmulator),
	string(build.TargetBoth),
}

// buildOptionRows shows the settings a build uses. The edition, packaging
// target and packaging toggle rows are selectors changed with the left/right
// arrow keys. The two translation-table rows report the public files the build
// reads; the advanced reference corpus is advanced-only and never listed here.
func (m *Model) buildOptionRows() []tableRow {
	edition := choose(m.build.form.Value("variant"), m.cfg.Variant)
	sourcePath := choose(m.src.form.Value("cpk"), m.cfg.SourceCPK)
	files := m.translationFiles()
	target := string(m.build.target)
	if target == "" {
		target = string(build.TargetBoth)
	}
	packageWord := "off"
	if m.build.packageOn {
		packageWord = "on"
	}
	return []tableRow{
		{ID: "edition", Cells: []string{"Edition", edition, "← → choose honorifics, no-honorifics or both"}},
		{ID: "target", Cells: []string{"Package target", target, "← → atmosphere, emulator or both (default both)"}},
		{ID: "package", Cells: []string{"Package ZIPs", packageWord, "← → assemble install-ready ZIPs into .local/release"}},
		{ID: "out", Cells: []string{"Output folder", displayOr(m.buildOutDir(), "(default)"), "each run gets its own folder"}},
		{ID: "run", Cells: []string{"Run name", displayOr(m.build.form.Value("run_name"), "(automatic)"), "optional label for this run"}},
		{ID: "source", Cells: []string{"Game archive", displayOr(sourcePath, "(auto: cache, work folder or game files)"), m.sourceStatusWord()}},
		{ID: "honorifics", Cells: []string{"Honorifics table", displayOr(files.honorifics, "(default)"), files.honorificsNote()}},
		{ID: "no-honorifics", Cells: []string{"No-honorifics table", displayOr(files.noHonorifics, "(default)"), files.noHonorificsNote()}},
	}
}

func (m *Model) updateBuild(key string) tea.Cmd {
	if key == "left" || key == "right" {
		if row, ok := m.build.options.Selected(); ok {
			switch row.ID {
			case "edition":
				v := nextInCycle(choose(m.build.form.Value("variant"), m.cfg.Variant), buildEditions, key)
				m.build.form.SetValue("variant", v)
				m.cfg.Variant = v
				m.build.options.SetRows(m.buildOptionRows())
				return nil
			case "target":
				cur := string(m.build.target)
				if cur == "" {
					cur = string(build.TargetBoth)
				}
				m.build.target = build.PackageTarget(nextInCycle(cur, buildTargets, key))
				m.build.options.SetRows(m.buildOptionRows())
				return nil
			case "package":
				m.build.packageOn = !m.build.packageOn
				m.build.options.SetRows(m.buildOptionRows())
				return nil
			}
		}
	}
	if m.build.options.HandleKey(key) {
		return nil
	}
	switch key {
	case "e":
		return m.build.form.Begin()
	case "d", "enter":
		return m.runBuild()
	case "b":
		return m.confirmBuild()
	}
	return nil
}

// runBuild validates the local fields and starts a dry-run build (writes
// nothing).
func (m *Model) runBuild() tea.Cmd { return m.startBuild(true) }

// confirmBuild asks before a real build publishes output.
func (m *Model) confirmBuild() tea.Cmd {
	if choose(m.build.form.Value("variant"), m.cfg.Variant) == "" {
		m.setStatus("choose an edition first — press e to edit the build options", true)
		return nil
	}
	m.confirm = &confirmState{
		title:  "Build and publish now?",
		detail: "The build writes new output folders under the output root. The game archive is only ever read.",
		run:    func() tea.Cmd { return m.startBuild(false) },
	}
	return nil
}

// startBuild validates the local fields and starts a build (a dry run when
// dryRun is set, which writes nothing). It resolves the game archive through the
// shared resolver without extracting anything; when no verified archive is
// present yet but a game container and the extractor are available, it asks for
// explicit consent before extracting.
func (m *Model) startBuild(dryRun bool) tea.Cmd {
	vals := m.build.form.All()
	variantWord := vals["variant"]
	if variantWord == "" {
		variantWord = m.cfg.Variant
	}
	if _, err := build.ParseVariant(variantWord); err != nil {
		m.setStatus(err.Error(), true)
		return nil
	}
	m.loadCfgFromFields()
	cfg := m.cfg
	explicit := m.src.form.Value("cpk")

	res, rerr := source.Resolve(context.Background(), source.Options{Explicit: explicit, Config: cfg, AllowExtract: false})
	if rerr != nil {
		// Offer extraction only when a container, the extractor and the keys are
		// all present; the operation itself starts only after the confirmation.
		st := source.Inspect(context.Background(), cfg)
		if st.Pick != nil && st.ToolErr == nil && st.Keys.Required() {
			container := filepath.Base(st.Pick.Path)
			tool := st.Tool.Name
			m.confirm = &confirmState{
				title:  "Prepare the game archive?",
				detail: fmt.Sprintf("No verified game archive is present yet. Extract the resources a build needs from %s with %s? Your container is only read; nothing is installed.", container, tool),
				run:    func() tea.Cmd { return m.launchBuild(dryRun, "", true) },
			}
			return nil
		}
		m.setStatus(rerr.Error(), true)
		return nil
	}
	if res.Provenance != source.ProvenanceFlag && res.Provenance != source.ProvenanceConfigured {
		m.rememberSource(res)
	}
	return m.launchBuild(dryRun, res.Path, false)
}

// launchBuild runs the shared build engine in the background. When explicit is
// empty the engine resolves the source itself (allowing extraction when
// allowExtract is set). Progress is the engine's own measured stage counter.
func (m *Model) launchBuild(dryRun bool, explicit string, allowExtract bool) tea.Cmd {
	vals := m.build.form.All()
	variantWord := vals["variant"]
	if variantWord == "" {
		variantWord = m.cfg.Variant
	}
	variant, err := build.ParseVariant(variantWord)
	if err != nil {
		m.setStatus(err.Error(), true)
		return nil
	}
	corpus := m.corpusPath()
	translationsDir := choose(m.trans.form.Value("translations"), m.cfg.TranslationsDir)
	outDir := m.buildOutDir()
	runName := vals["run_name"]
	titleID := choose(m.src.form.Value("title_id"), m.cfg.TitleID)
	displayVersion := m.src.form.Value("display_version")
	cfg := m.cfg
	m.build.err = ""
	root, rootErr := project.FindRoot()
	version := ""
	if rootErr == nil {
		version, _ = build.ReadVersion(root)
	}
	packageOn := m.build.packageOn && !dryRun
	// A packaged release needs a real version from the root version.txt; a plain
	// build or a dry run may proceed without one. The check runs before the
	// build starts, so nothing is written when it fails.
	if packageOn && version == "" {
		msg := build.ErrVersionMissing.Error() + " — supply it before a packaged build, or use a dry run"
		if rootErr != nil {
			msg = build.ErrVersionMissing.Error() + " (" + rootErr.Error() + ")"
		}
		m.setStatus(msg, true)
		return nil
	}
	packageTarget := m.build.target
	if packageTarget == "" {
		packageTarget = build.TargetBoth
	}
	label := "build " + string(variant)
	if dryRun {
		label = "build (dry run) " + string(variant)
	} else if packageOn {
		label = "build + package " + string(variant)
	}
	return m.startTaskAsk(label, func(ctx context.Context, prog progressFunc, ask askFunc) (string, any, error) {
		res, rerr := source.Resolve(ctx, source.Options{Explicit: explicit, Config: cfg, AllowExtract: allowExtract})
		if rerr != nil {
			return "", nil, rerr
		}
		opts := build.Options{
			Variant:         variant,
			SourcePath:      res.Path,
			CorpusPath:      corpus,
			TranslationsDir: translationsDir,
			OutDir:          outDir,
			RunName:         runName,
			TitleID:         titleID,
			DisplayVersion:  displayVersion,
			DryRun:          dryRun,
			AutoAssets:      true,
			Root:            root,
			Version:         version,
			RequireVersion:  packageOn,
		}
		opts.Progress = func(stage string, done, total int) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			prog(stage, done, total)
			return nil
		}
		bres, berr := build.Run(opts)
		if berr != nil {
			return "", nil, berr
		}
		summary := bres.SummaryTxt
		if packageOn {
			if err := ctx.Err(); err != nil {
				return "", nil, err
			}
			pkgOut := ""
			if root != "" {
				pkgOut = project.ReleaseDir(root)
			}
			pkg, perr := build.Package(bres, build.PackageOptions{
				Targets: packageTarget,
				OutDir:  pkgOut,
				Version: version,
				// A collision is resolved once for the whole set: the dialog asks
				// Replace / Keep both / Cancel and the same answer applies to every
				// archive, so a "both" run never publishes a mixed policy.
				OnConflict: func(collisions []string) build.ConflictPolicy {
					return m.askConflictPolicy(ctx, ask, collisions)
				},
			})
			if perr != nil {
				return "", nil, perr
			}
			summary += pkg.SummaryTxt
		}
		return summary, buildOutcome{result: bres, resolution: res}, nil
	})
}

// askConflictPolicy asks the operator how to handle existing package outputs and
// maps the answer to a build.ConflictPolicy. It runs on the background task
// goroutine and never touches the model; it only uses the task's ask function,
// which marshals the question onto the UI goroutine.
func (m *Model) askConflictPolicy(ctx context.Context, ask askFunc, collisions []string) build.ConflictPolicy {
	shown := collisions
	if len(shown) > 6 {
		shown = append(append([]string{}, shown[:6]...), fmt.Sprintf("… and %d more", len(collisions)-6))
	}
	detail := fmt.Sprintf("%d output file(s) in the release folder already exist:\n%s\n\nReplace them, keep both sets under new names, or cancel?",
		len(collisions), strings.Join(shown, "\n"))
	q := taskQuestion{
		Title:  "Output already exists.",
		Detail: detail,
		Choices: []taskChoice{
			{Key: "r", Label: "Replace the existing files", Value: "replace"},
			{Key: "k", Label: "Keep both (new files get a -2, -3 … name)", Value: "keep"},
			{Key: "c", Label: "Cancel (change nothing)", Value: "cancel"},
		},
	}
	v, err := ask(q)
	if err != nil || ctx.Err() != nil {
		return build.ConflictCancel
	}
	switch v {
	case "replace":
		return build.ConflictReplace
	case "keep":
		return build.ConflictKeepBoth
	default:
		return build.ConflictCancel
	}
}

// buildOutcome carries a finished build together with the source resolution that
// produced it, so a discovered archive is remembered on the UI goroutine.
type buildOutcome struct {
	result     *build.Result
	resolution source.Resolution
}

// rememberSource records a discovered archive in the in-memory configuration and
// the Game files form, and persists it best-effort so a later launch reuses it
// without asking again. A caller-supplied or already-configured path is left
// untouched.
func (m *Model) rememberSource(res source.Resolution) {
	if res.Path == "" || res.Provenance == "" || res.Provenance == source.ProvenanceFlag {
		return
	}
	m.cfg.SourceCPK = res.Path
	if res.Facts != nil {
		m.cfg.SourceSHA256 = res.Facts.SHA256
		m.cfg.SourceSize = res.Facts.Size
		m.cfg.SourceMembers = res.Facts.Members
		m.cfg.SourceFMSMembers = res.Facts.FMSMembers
		m.cfg.SourceMemberListSHA256 = res.Facts.MemberListSHA256
	}
	m.cfg.SourceProvenance = res.Provenance
	if m.src.form != nil {
		m.src.form.SetValue("cpk", res.Path)
	}
	path := m.cfgPath
	if path == "" {
		path = appconfig.Path()
	}
	_, _, _ = source.Remember(path, m.cfg, res)
}

// viewInstall renders the Install screen.
func (m *Model) viewInstall(w, h int) string {
	inner := max(4, w-4)
	if m.inst.form.editing {
		return m.theme.stackPanels(w, h, []panelSpec{
			{title: "Edit install locations", body: m.inst.form.View(inner), focus: true},
			{title: "Keys", body: m.theme.Render(m.theme.Desc, "  tab: next field   enter: apply   esc: done (values kept)"), height: 3},
		})
	}
	table := m.inst.table
	table.width = inner
	tableHeight := max(5, h-16)

	previewBody := m.installPreviewBody(inner)

	actions := m.actions()
	actionBody := ""
	if actions != nil {
		actions.width = inner
		actionBody = actions.View(m.theme, max(1, len(m.actionRows())), inner)
	}
	return m.theme.stackPanels(w, h, []panelSpec{
		{title: "Install preview", body: previewBody, height: max(6, h-tableHeight-9)},
		{title: "Stored backups", body: table.View(m.theme, tableHeight, inner), height: tableHeight, focus: m.paneKind() == paneMain},
		{title: "Actions", body: actionBody, focus: m.paneKind() == paneActions},
	})
}

// installPreviewBody renders the read-only install preview, or an explanation of
// what is still missing.
func (m *Model) installPreviewBody(inner int) string {
	if m.inst.err != "" {
		return m.theme.Render(m.theme.Bad, "  "+truncate(m.inst.err, inner-4)) + "\n" +
			m.theme.Render(m.theme.Muted, "  Only "+installer.ArtifactRel+" inside <root>/contents/<title-id>/ is ever written.")
	}
	p := m.inst.preview
	if p == nil {
		return m.theme.Render(m.theme.Muted,
			"  Enter the install folder below (press e), then choose Preview in the Actions pane.") + "\n" +
			m.theme.Render(m.theme.Muted, "  Only "+installer.ArtifactRel+" inside <root>/contents/<title-id>/ is ever written.")
	}
	existing := "none"
	if p.Existing {
		if p.Identical {
			existing = "identical to the new file"
		} else {
			existing = fmt.Sprintf("%s, checksum %s", humanBytes(p.ExistingSize), shortSHA(p.ExistingSHA256))
		}
	}
	lines := []string{
		fmt.Sprintf("  Action          %s", p.Action),
		fmt.Sprintf("  Target          %s", truncate(display(p.Target), inner-20)),
		fmt.Sprintf("  Source          %s", truncate(display(p.Source), inner-20)),
		fmt.Sprintf("  Existing file   %s", existing),
		fmt.Sprintf("  Backup needed   %s", boolWord(p.BackupRequired)),
		fmt.Sprintf("  Protected       %s", boolWord(p.Protected)),
		fmt.Sprintf("  Confirmation    type %s", p.ConfirmationToken),
	}
	if len(p.UnrelatedFiles) > 0 {
		lines = append(lines, "  Preserved       "+truncate(strings.Join(p.UnrelatedFiles, ", "), inner-20))
	}
	return strings.Join(lines, "\n")
}

func (m *Model) updateInstall(key string) tea.Cmd {
	if m.inst.table.HandleKey(key) {
		return nil
	}
	switch key {
	case "e":
		return m.inst.form.Begin()
	case "r":
		m.refreshBackups()
		m.setStatus("backup list refreshed", false)
	case "p", "enter":
		m.previewInstall()
	case "i":
		return m.confirmInstall()
	case "u":
		return m.confirmUninstall()
	case "o":
		return m.confirmRollback()
	}
	return nil
}

// confirmInstall previews and asks for the typed confirmation token.
func (m *Model) confirmInstall() tea.Cmd {
	m.previewInstall()
	if m.inst.preview == nil {
		return nil
	}
	token := m.inst.preview.ConfirmationToken
	m.confirm = &confirmState{
		title:  "Install the override?",
		detail: "The existing file (if any) is backed up first, then replaced atomically. Unrelated files are preserved.",
		token:  token,
		run:    func() tea.Cmd { return m.startInstall(installActionInstall) },
	}
	return nil
}

func (m *Model) confirmUninstall() tea.Cmd {
	token := choose(m.inst.form.Value("title_id"), m.cfg.TitleID)
	m.confirm = &confirmState{
		title:  "Remove the override?",
		detail: "The installed file is backed up before it is removed, so the change can be rolled back.",
		token:  token,
		run:    func() tea.Cmd { return m.startInstall(installActionUninstall) },
	}
	return nil
}

func (m *Model) confirmRollback() tea.Cmd {
	it, ok := m.inst.table.Selected()
	if !ok {
		m.setStatus("select a stored backup first", true)
		return nil
	}
	m.confirm = &confirmState{
		title:  "Roll back to this backup?",
		detail: "The current file is backed up first, so the rollback itself can be undone.",
		run:    func() tea.Cmd { return m.startInstall(installActionRollback, it.ID) },
	}
	return nil
}

type installAction int

const (
	installActionInstall installAction = iota
	installActionUninstall
	installActionRollback
)

// previewInstall computes the read-only preview used by the confirmations.
func (m *Model) previewInstall() {
	opts, err := m.installerOptions()
	if err != nil {
		m.inst.preview = nil
		m.inst.err = installerMessage(err)
		return
	}
	preview, err := installer.PreviewInstall(m.sourceArchive(), opts)
	if err != nil {
		m.inst.preview = nil
		m.inst.err = installerMessage(err)
		return
	}
	m.inst.preview = preview
	m.inst.err = ""
}

func installerMessage(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, installer.ErrNoRoot):
		return "Set the install folder in this screen first (press e to edit the locations)."
	case errors.Is(err, installer.ErrProtected):
		return err.Error()
	case errors.Is(err, installer.ErrUnsupportedTitleID):
		return err.Error()
	}
	return err.Error()
}

// startInstall runs the installer in the background.
func (m *Model) startInstall(action installAction, backupPath ...string) tea.Cmd {
	path := ""
	if len(backupPath) > 0 {
		path = backupPath[0]
	}
	opts, err := m.installerOptions()
	if err != nil {
		m.setStatus(installerMessage(err), true)
		return nil
	}
	m.inst.form.End()
	label := map[installAction]string{
		installActionInstall:   "install",
		installActionUninstall: "uninstall",
		installActionRollback:  "rollback",
	}[action]
	return m.startTask(label, func(ctx context.Context, prog func(string, int, int)) (string, any, error) {
		prog("reading the game archive", 1, 4)
		if err := ctx.Err(); err != nil {
			return "", nil, err
		}
		switch action {
		case installActionInstall:
			res, err := installer.Install(m.sourceArchive(), opts)
			if err != nil {
				return "", nil, err
			}
			prog("installed", 2, 2)
			return installSummary(res), installResult{res: res}, nil
		case installActionUninstall:
			res, err := installer.Uninstall(opts)
			if err != nil {
				return "", nil, err
			}
			prog("removed", 2, 2)
			return installSummary(res), installResult{res: res}, nil
		default:
			res, err := installer.Rollback(opts, path)
			if err != nil {
				return "", nil, err
			}
			prog("restored", 2, 2)
			return installSummary(res), installResult{res: res}, nil
		}
	})
}

// installResult carries an installer outcome back to the UI goroutine.
type installResult struct {
	res *installer.Result
}

func installSummary(res *installer.Result) string {
	msg := fmt.Sprintf("%s: %s", res.Action, display(res.Target))
	if res.BackupPath != "" {
		msg += "  backup " + display(res.BackupPath)
	}
	if len(res.Unrelated) > 0 {
		msg += fmt.Sprintf("  preserved %d unrelated file(s)", len(res.Unrelated))
	}
	return msg
}

// sourceArchive is the source path used by the installer: the source archive
// chosen on Game files, or the artifact of the most recent published build.
func (m *Model) sourceArchive() string {
	if p := m.src.form.Value("cpk"); p != "" {
		return p
	}
	if m.build.last != nil {
		for _, e := range m.build.last.Editions {
			if e.Published {
				return e.ArtifactPath
			}
		}
	}
	return m.cfg.SourceCPK
}

// viewDiagnostics renders the Diagnostics screen. The advanced toggle reveals the
// optional reference-corpus field and the raw self-check output; the normal view
// never asks for reference data.
func (m *Model) viewDiagnostics(w, h int) string {
	inner := max(4, w-4)
	if m.diag.form.editing {
		return m.theme.stackPanels(w, h, []panelSpec{
			{title: "Advanced — reference data", body: m.diag.form.View(inner), focus: true},
			{title: "Keys", body: m.theme.Render(m.theme.Desc, "  enter: apply   esc: done (values kept)"), height: 3},
		})
	}
	table := m.diag.table
	table.width = inner
	// The advanced view adds the optional reference-data status and the raw
	// self-check text, so it trades a little table height for summary lines.
	tableHeight := max(5, h-14)
	if m.diag.advanced {
		tableHeight = max(5, h-18)
	}

	actions := m.actions()
	actionBody := ""
	if actions != nil {
		actions.width = inner
		actionBody = actions.View(m.theme, max(1, len(m.actionRows())), inner)
	}
	return m.theme.stackPanels(w, h, []panelSpec{
		{title: "Summary", body: strings.TrimRight(m.diagSummary(inner), "\n"), height: max(5, h-tableHeight-9)},
		{title: "Self-check results", body: table.View(m.theme, tableHeight, inner), height: tableHeight, focus: m.paneKind() == paneMain},
		{title: "Actions", body: actionBody, focus: m.paneKind() == paneActions},
	})
}

// diagSummary builds the Diagnostics summary body. The reference corpus is
// described only in the advanced view and is always marked as optional.
func (m *Model) diagSummary(inner int) string {
	summary := m.theme.Render(m.theme.Desc,
		"  Details: "+m.detailsWord()+"   (← → or a to change)") + "\n"
	switch {
	case m.diag.qa != nil:
		c := m.diag.qa.Counts
		summary += fmt.Sprintf("  %d records checked · %d rejected · %d token mismatches · %d over-length lines\n",
			c.Records, c.Rejected, c.TokenMismatch, c.OverLongSingleLine)
	case m.diag.rep != nil:
		summary += fmt.Sprintf("  Self-check %s — %d of %d checks passed\n", passWord(m.diag.rep.OK), passedChecks(m.diag.rep), len(m.diag.rep.Checks))
	default:
		summary += m.theme.Render(m.theme.Muted, "  No check has been run yet. Choose one in the Actions pane.") + "\n"
	}
	if m.diag.advanced {
		summary += m.theme.Render(m.theme.Muted, "  Advanced: static signals only — no rendered-fit claim is made.") + "\n"
		summary += m.theme.Render(m.theme.Muted,
			"  Reference data (optional cross-check): "+truncate(displayOr(m.corpusPath(), "(not set — not required)"), max(10, inner-40))) + "\n"
		if m.diag.rep != nil {
			summary += indent(truncateLines(m.diag.rep.Text(), 6), "  ") + "\n"
		}
	}
	if m.diag.err != "" {
		summary += m.theme.Render(m.theme.Bad, "  "+truncate(m.diag.err, inner-4)) + "\n"
	}
	return summary
}

func (m *Model) detailsWord() string {
	if m.diag.advanced {
		return "advanced"
	}
	return "summary"
}

func (m *Model) updateDiagnostics(key string) tea.Cmd {
	switch key {
	case "left", "right", "a":
		m.diag.advanced = !m.diag.advanced
		if !m.diag.advanced {
			m.diag.form.End()
		}
		m.refreshActions()
		return nil
	case "e":
		// The reference-data field is advanced-only.
		if m.diag.advanced {
			return m.diag.form.Begin()
		}
		return nil
	}
	if m.diag.table.HandleKey(key) {
		return nil
	}
	switch key {
	case "q", "enter":
		return m.runQA()
	case "s":
		return m.confirmSelfCheck()
	}
	return nil
}

// runQA runs the shared static analyser in the background. It needs only the
// translation files and, optionally, the game archive; the reference corpus is
// advanced-only and never required.
func (m *Model) runQA() tea.Cmd {
	corpus := m.corpusPath()
	source := m.sourceArchive()
	if corpus == "" && source == "" {
		m.setStatus("no game archive found yet — open Game files to validate one", true)
		return nil
	}
	scope, err := qa.ParseScope(choose(m.cfg.Variant, string(qa.ScopeBoth)))
	if err != nil {
		scope = qa.ScopeBoth
	}
	opts := qa.Options{
		Scope:           scope,
		CorpusPath:      corpus,
		SourcePath:      source,
		TranslationsDir: choose(m.trans.form.Value("translations"), m.cfg.TranslationsDir),
	}
	return m.startTask("translation check (static)", func(ctx context.Context, prog func(string, int, int)) (string, any, error) {
		prog("analysing records", 1, 2)
		res, err := qa.Run(opts)
		if err != nil {
			return "", nil, err
		}
		if err := ctx.Err(); err != nil {
			return "", nil, err
		}
		prog("done", 2, 2)
		return res.SummaryTxt, res, nil
	})
}

// applyQA stores QA results on the UI goroutine.
func (m *Model) applyQA(res *qa.Result) {
	m.diag.qa = res
	m.diag.err = ""
	m.refreshActions()
}

// confirmSelfCheck asks before the self-check suite runs.
func (m *Model) confirmSelfCheck() tea.Cmd {
	m.confirm = &confirmState{
		title:  "Run the self-check?",
		detail: "It validates the game archive, proves that wrong hashes and unsupported versions are rejected, builds both editions twice into a scratch folder and compares them byte-for-byte. Nothing outside that scratch folder is written.",
		run:    func() tea.Cmd { return m.startPreflight() },
	}
	return nil
}

// startPreflight runs the self-check suite in the background.
func (m *Model) startPreflight() tea.Cmd {
	opts := preflight.Options{
		SourcePath:      m.sourceArchive(),
		CorpusPath:      m.corpusPath(),
		TranslationsDir: choose(m.trans.form.Value("translations"), m.cfg.TranslationsDir),
		TitleID:         choose(m.src.form.Value("title_id"), m.cfg.TitleID),
		DisplayVersion:  m.src.form.Value("display_version"),
		Runs:            2,
	}
	variant, err := build.ParseVariant(choose(m.build.form.Value("variant"), m.cfg.Variant))
	if err != nil {
		variant = build.VariantBoth
	}
	opts.Variant = variant
	return m.startTask("self-check", func(ctx context.Context, prog func(string, int, int)) (string, any, error) {
		opts.Progress = func(stage string, done, total int) { prog(stage, done, total) }
		rep, err := preflight.Run(ctx, opts)
		if err != nil {
			return "", nil, err
		}
		return fmt.Sprintf("self-check %s — %d of %d checks passed", passWord(rep.OK), passedChecks(rep), len(rep.Checks)), rep, nil
	})
}

func passedChecks(rep *preflight.Report) int {
	n := 0
	for _, c := range rep.Checks {
		if c.OK {
			n++
		}
	}
	return n
}
