// Package build turns a validated translation ledger and a read-only source
// localization CPK into rebuilt CPK archives, one per translation edition.
//
// The package is source-independent and stdlib-only: it never hard-codes a game
// path, the caller supplies the source archive, and the archive is only ever
// read. Both editions are produced from one shared intermediate state (a single
// resolved plan plus a single decompression/parse of each source member), and
// each edition is staged and published in its own directory. Nothing is written
// until every requested edition has been rebuilt and independently re-validated,
// so a failed build never publishes a partial output.
//
// Inputs come from the revision-agnostic importer package, so a later revision
// requires no change here and no revision number is hard-coded.
package build

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"uo-unburger/internal/cpk"
	"uo-unburger/internal/fms"
	"uo-unburger/internal/importer"
	"uo-unburger/internal/layeredfs"
	"uo-unburger/internal/paths"
	"uo-unburger/internal/project"
	"uo-unburger/internal/titlefooter"
	"uo-unburger/internal/voice"
)

// Variant selects the translation editions to build.
type Variant string

const (
	// VariantHonorifics is the canonical edition (source honorifics retained).
	VariantHonorifics Variant = "honorifics"
	// VariantNoHonorifics applies the sparse per-record no-honorifics overrides.
	VariantNoHonorifics Variant = "no-honorifics"
	// VariantBoth builds both editions from the same intermediate state.
	VariantBoth Variant = "both"
)

// ParseVariant maps a CLI word to a Variant.
func ParseVariant(s string) (Variant, error) {
	switch Variant(s) {
	case VariantHonorifics, VariantNoHonorifics, VariantBoth:
		return Variant(s), nil
	}
	return "", fmt.Errorf("build: unknown variant %q (want honorifics|no-honorifics|both)", s)
}

// Editions returns the concrete edition list for a variant.
func (v Variant) Editions() []Variant {
	if v == VariantBoth {
		return []Variant{VariantHonorifics, VariantNoHonorifics}
	}
	return []Variant{v}
}

// Options configures a build.
type Options struct {
	Variant         Variant
	SourcePath      string
	CorpusPath      string
	TranslationsDir string
	OutDir          string
	RunName         string
	TitleID         string
	DisplayVersion  string
	// Expect optionally asserts a stricter source identity (SHA-256, size,
	// member counts, member-list digest). TitleID/DisplayVersion here take
	// precedence over the matching top-level fields.
	Expect SourceExpectation
	// StrictTokens turns advisory control-token mismatches into a hard failure.
	StrictTokens bool
	// DryRun performs every read, rebuild and validation but writes nothing.
	DryRun bool
	// AutoAssets selects the pinned production assets from the checkout: the
	// extracted ExeFS `main` under .local/work/source/exefs/main and the title
	// credit atlas under .local/work/source/romfs/GUI/Opening100.ftx. It makes the
	// Japanese-voice patch and the version footer part of every normal build.
	// The CLI and TUI always set it; tests and library callers can pass explicit
	// Voice/VoiceMain/TitleFooterFTX paths instead.
	AutoAssets bool
	// Root is the checkout root used to resolve AutoAssets. Empty falls back to
	// project.FindRoot (the process working directory).
	Root string
	// Voice requests the shared, Build-ID-gated Japanese-voice ExeFS patch. It is
	// the normal build path through AutoAssets; the explicit fields remain for
	// tests and library callers. The artifact is built from VoiceMain, validated
	// offline and never installed by this package.
	Voice bool
	// VoiceMain is the already-extracted ExeFS `main` (NSO0) the voice patch is
	// built from. Required when Voice is set. Read-only.
	VoiceMain string
	// TitleFooterFTX is the user's own extracted title-screen credit atlas
	// (GUI/Opening100.ftx). When set, the build renders "UNBURGER MOD <Version>"
	// into a copy and adds that copy as a new member of both editions. The input
	// is only read; no game texture is stored in the toolkit or its outputs other
	// than the rebuilt edition archives themselves. AutoAssets sets it for the
	// normal build; the field remains for tests and library callers.
	TitleFooterFTX string
	// Date is the YYYYMMDD stamp used in the default run name; tests pin it.
	Date string
	// Version is the release version read from the checkout root's version.txt
	// (see ReadVersion). It is recorded in every report and is the value the
	// startup label renders as "Unburger Mod V<version>". It is never
	// hard-coded.
	Version string
	// RequireVersion makes an empty Version a hard failure before any source is
	// read or output staged, so a version failure never leaves a partial build.
	// The CLI and TUI set it for a publishing build; a dry run may proceed
	// without it and simply reports no version.
	RequireVersion bool
	// Progress, when set, is called after every completed build stage with the
	// measured stage counters for this run. It reports real stage counts, never
	// an estimated completion percentage. Returning a non-nil error aborts the
	// build; because output is only published in the final stage, an aborted
	// build leaves no partial output.
	Progress func(stage string, done, total int) error

	// voicePlan overrides the shared voice-enforcement plan. It is unexported:
	// production callers always use the single pinned plan, and package tests
	// use a synthetic one.
	voicePlan *voice.Plan
}

// FileResult records one edited member of an edition.
type FileResult struct {
	Member      string
	Slots       []int
	StoredSize  int64
	ExtractSize int64
	// NewMember marks a member that did not exist in the source archive and was
	// staged from scratch (the title-screen footer atlas).
	NewMember bool `json:"new_member,omitempty"`
}

// EditionResult is the outcome for one edition.
type EditionResult struct {
	Variant      Variant
	Dir          string // final directory (empty when dry-run)
	ArtifactRel  string // "romfs/Unicorn_US.CPK"
	ArtifactPath string // absolute-or-relative path of the artifact
	SHA256       string
	Size         int64
	Files        []FileResult
	PlannedSlots int
	ChangedSlots int
	// EditedMembers is the number of edited members; it mirrors len(Files) for
	// an in-memory build and is reconstructed from a published report by
	// LoadRun.
	EditedMembers int
	Published     bool
	ReportTxt     string
	ReportJSON    string
}

// Result is the outcome of a build (or dry-run).
type Result struct {
	Facts          *SourceFacts
	Plan           *Plan
	Resolution     *importer.Resolution
	Translations   string
	RunName        string
	RunDir         string
	DryRun         bool
	TitleID        string
	DisplayVersion string
	// Version is the release version stamped into this output (empty when none
	// was supplied, e.g. a dry run without a root version.txt). StartupLabel is
	// its rendered "Unburger Mod V<version>" form, which is also written into the
	// startup record of MsgSheet/UcSysMsg.fms.
	Version      string
	StartupLabel string
	// SourceMode names where the canonical rows' official English text came
	// from (verified source CPK or reference corpus).
	SourceMode importer.SourceMode
	// TitleFooterTag is the exact footer text rendered into the title-screen
	// credit atlas ("UNBURGER MOD <version>"); empty when no footer was built.
	TitleFooterTag string
	// TitleFooterMember is the CPK member that carries the footer.
	TitleFooterMember string
	// TitleFooterSize is the stored size of the rebuilt atlas member.
	TitleFooterSize int64
	// TitleFooterSHA256 is the SHA-256 of the rebuilt atlas member.
	TitleFooterSHA256 string
	// TitleFooterFontSHA256 and TitleFooterConfigSHA256 pin the embedded
	// typeface and the authoritative typography JSON. They are part of the reuse
	// identity: changing either must invalidate an otherwise identical run.
	TitleFooterFontSHA256   string
	TitleFooterConfigSHA256 string
	// TitleFooterTypography is a human-readable form of the effective config.
	TitleFooterTypography string
	// TitleFooterProof records the edited surface rectangle and byte counts.
	TitleFooterProof *titlefooter.Proof
	// Dataset identifies the translation tables the build resolved.
	Dataset     importer.Dataset
	NoOpRecords int
	Reverts     []string
	Editions    []EditionResult
	// Voice is the shared Japanese-voice ExeFS patch when it was built; nil
	// otherwise.
	Voice      *VoiceResult
	SummaryTxt string
}

type editionMember struct {
	Member string
	File   string
	Slots  []int
	Stored []byte
	// Raw marks a member that is staged verbatim rather than as an FMS slot
	// edit; IsNew marks a member that does not exist in the source archive.
	Raw   bool
	IsNew bool
}

// Run executes a build (or a dry-run) and returns its report. It performs no
// output writes when DryRun is set.
func Run(opts Options) (*Result, error) {
	// A publishing build and any packaged release must carry a real version
	// from the root version.txt; the check runs before anything is read or
	// staged so a missing version leaves no partial output. A dry run may
	// proceed without one and reports no version.
	if opts.RequireVersion && strings.TrimSpace(opts.Version) == "" {
		return nil, ErrVersionMissing
	}
	if opts.AutoAssets {
		if err := resolveAutoAssets(&opts); err != nil {
			return nil, err
		}
	}
	if opts.TranslationsDir == "" {
		opts.TranslationsDir = "translations"
	}
	if opts.OutDir == "" {
		opts.OutDir = filepath.Join(".local", "work", "builds")
	}
	if opts.TitleID == "" {
		opts.TitleID = DefaultTitleID
	}
	expect := opts.Expect
	if expect.TitleID == "" {
		expect.TitleID = opts.TitleID
	}
	if expect.DisplayVersion == "" {
		expect.DisplayVersion = opts.DisplayVersion
	}
	if len(opts.Variant.Editions()) == 0 {
		return nil, fmt.Errorf("build: no variant selected")
	}
	variants := opts.Variant.Editions()
	// stage counts are measured for this run, so a progress callback can report
	// a real fraction of completed stages (never a time estimate).
	total := 5 + 2*len(variants)
	if !opts.DryRun {
		total++
	}
	if opts.Voice {
		total++
	}
	done := 0
	step := func(name string) error {
		done++
		if opts.Progress == nil {
			return nil
		}
		return opts.Progress(name, done, total)
	}

	facts, err := ValidateSource(opts.SourcePath, expect)
	if err != nil {
		return nil, err
	}
	if err := step("validate source"); err != nil {
		return nil, err
	}

	res, err := resolveTranslations(opts)
	if err != nil {
		return nil, fmt.Errorf("build: %w", err)
	}
	if err := step("load and resolve translations"); err != nil {
		return nil, err
	}

	if res.Counts.Rejected > 0 {
		return nil, fmt.Errorf("build: %d record(s) rejected (stale or malformed source hash); refusing to build", res.Counts.Rejected)
	}
	if !res.DiffPass {
		return nil, fmt.Errorf("build: variant diff verification FAILED (drift=%d)", len(res.DiffDrift))
	}
	if opts.StrictTokens && res.Counts.TokenMismatch > 0 {
		return nil, fmt.Errorf("build: %d control-token mismatch(es) with --strict-tokens", res.Counts.TokenMismatch)
	}

	plan, err := DerivePlan(res)
	if err != nil {
		return nil, err
	}
	// The startup label is a build-time override, separate from the translation
	// tables: it forces one record to the rendered version label for both
	// editions. An empty version (a dry run without a root version.txt) carries
	// no label and no edit.
	startupLabel, err := applyStartupLabel(plan, opts.Version)
	if err != nil {
		return nil, err
	}
	if err := step("derive plan"); err != nil {
		return nil, err
	}

	prepared, err := prepareEditions(opts.SourcePath, plan)
	if err != nil {
		return nil, err
	}
	footer, err := prepareTitleFooter(opts)
	if err != nil {
		return nil, err
	}
	if footer != nil {
		member := editionMember{Member: footer.member, Stored: footer.stored, Raw: true, IsNew: true}
		for _, v := range variants {
			prepared.builds[v] = append(prepared.builds[v], member)
		}
	}
	if err := step("prepare editions"); err != nil {
		return nil, err
	}

	// No-op control: a zero-edit rebuild must reproduce the source byte-for-byte,
	// proving the variable repacker is layout-preserving for this archive.
	control, err := rebuildEdition(opts.SourcePath, nil)
	if err != nil {
		return nil, err
	}
	sourceBytes, err := os.ReadFile(opts.SourcePath)
	if err != nil {
		return nil, fmt.Errorf("build: reading source for control comparison: %w", err)
	}
	if !bytes.Equal(control, sourceBytes) {
		return nil, fmt.Errorf("build: no-op control rebuild differs from the source (repacker is not layout-preserving)")
	}
	if err := step("no-op control"); err != nil {
		return nil, err
	}

	runName := opts.RunName
	label := translationsLabel(opts.TranslationsDir)
	if runName == "" {
		runName = defaultRunName(label, opts.Date)
	}
	runDir := filepath.Join(opts.OutDir, runName)

	result := &Result{
		Facts:          facts,
		Plan:           plan,
		Resolution:     res,
		Translations:   label,
		RunName:        runName,
		RunDir:         runDir,
		DryRun:         opts.DryRun,
		TitleID:        expect.TitleID,
		DisplayVersion: expect.DisplayVersion,
		Version:        strings.TrimSpace(opts.Version),
		StartupLabel:   startupLabel,
		SourceMode:     res.SourceMode,
		Dataset:        res.Dataset,
		NoOpRecords:    prepared.noOps,
		Reverts:        prepared.reverts,
	}
	if footer != nil {
		result.TitleFooterTag = footer.text
		result.TitleFooterMember = footer.member
		result.TitleFooterSize = int64(len(footer.stored))
		result.TitleFooterSHA256 = footer.sha256
		result.TitleFooterFontSHA256 = footer.fontSHA256
		result.TitleFooterConfigSHA256 = footer.configSHA
		result.TitleFooterTypography = footer.typography
		proof := footer.proof
		result.TitleFooterProof = &proof
	}

	// A repeat build on the same day reuses a verified, complete run rather than
	// rebuilding it or refusing to publish. The reuse check is strict — the source
	// digest, dataset identity, version and the on-disk CPK digests must all match —
	// so a foreign, stale or unverified directory is left for publishAll to refuse
	// and is never silently overwritten.
	if !opts.DryRun {
		if reused := reusePublishedRun(result, runDir, variants, opts); reused != nil {
			result.Editions = reused.editions
			result.Voice = reused.voice
			result.SummaryTxt = renderSummaryText(result)
			return result, nil
		}
		// The default run name is date-based, so a second build on the same day
		// would collide with the first. When the existing directory cannot be
		// safely reused (a different identity, unverified or foreign data, or an
		// interrupted build), give this build its own run name instead of
		// overwriting it or failing. An explicit --run-name is never changed, so an
		// explicit name still refuses rather than silently writing beside itself.
		if opts.RunName == "" {
			if newName := uniqueRunName(opts.OutDir, runName); newName != runName {
				runName = newName
				runDir = filepath.Join(opts.OutDir, runName)
				result.RunName = runName
				result.RunDir = runDir
			}
		}
	}

	editions := make([]EditionResult, 0, len(variants))
	pending := make([]stagedDir, 0, len(variants))

	if opts.Voice {
		plan := voice.JPConfigV5()
		if opts.voicePlan != nil {
			plan = *opts.voicePlan
		}
		patched, ips, vr, err := buildVoicePatch(opts.VoiceMain, plan)
		if err != nil {
			return nil, err
		}
		vrJSON, err := json.MarshalIndent(vr, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("build: encoding voice report: %w", err)
		}
		result.Voice = vr
		if !opts.DryRun {
			pending = append(pending, stagedDir{dir: voiceStageDir(runDir), files: map[string][]byte{
				voiceArtifactRel:    patched,
				voiceIPSRel:         ips,
				"VOICE_REPORT.txt":  []byte(renderVoiceText(vr)),
				"VOICE_REPORT.json": append(vrJSON, '\n'),
				voiceRuntimeMarker:  []byte(voiceRuntimeText),
			}})
		}
		if err := step("build voice patch"); err != nil {
			return nil, err
		}
	}

	for _, v := range variants {
		members := prepared.builds[v]
		rebuilt, err := rebuildEdition(opts.SourcePath, members)
		if err != nil {
			return nil, err
		}
		if err := step("rebuild " + string(v)); err != nil {
			return nil, err
		}
		if err := validateEdition(opts.SourcePath, rebuilt, members); err != nil {
			return nil, err
		}
		if err := step("validate " + string(v)); err != nil {
			return nil, err
		}
		sum := sha256.Sum256(rebuilt)
		er := EditionResult{
			Variant:      v,
			ArtifactRel:  artifactRel,
			ArtifactPath: filepath.Join(runDir, string(v), artifactRel),
			SHA256:       hex.EncodeToString(sum[:]),
			Size:         int64(len(rebuilt)),
			Files:        fileResults(members),
			PlannedSlots: prepared.planned,
			ChangedSlots: prepared.changed[v],
		}
		er.ReportTxt = renderEditionText(result, &er)
		erJSON, err := renderEditionJSON(result, &er)
		if err != nil {
			return nil, err
		}
		er.ReportJSON = erJSON
		er.EditedMembers = len(er.Files)

		if !opts.DryRun {
			dir := filepath.Join(runDir, string(v))
			pending = append(pending, stagedDir{dir: dir, files: map[string][]byte{
				filepath.ToSlash(artifactRel): rebuilt,
				"BUILD_REPORT.txt":            []byte(er.ReportTxt),
				"BUILD_REPORT.json":           []byte(er.ReportJSON),
			}})
		}
		editions = append(editions, er)
	}

	if !opts.DryRun {
		if err := publishAll(runDir, pending); err != nil {
			return nil, err
		}
		if err := step("publish"); err != nil {
			return nil, err
		}
		for i := range editions {
			editions[i].Published = true
			editions[i].Dir = filepath.Join(runDir, string(editions[i].Variant))
		}
	}

	result.Editions = editions
	result.SummaryTxt = renderSummaryText(result)
	return result, nil
}

// artifactRel is the LayeredFS-relative path of an edition's CPK override. It
// is re-exported from internal/layeredfs so every consumer agrees on the path.
const artifactRel = layeredfs.RomFSRel

// resolveTranslations loads the translation ledger and resolves it against the
// user's verified source archive. A reference corpus is optional and, when
// supplied, takes over source-identity validation (advanced mode); otherwise the
// official English slot text is read read-only from the source CPK itself. The
// corpus is never required for a normal build.
func resolveTranslations(opts Options) (*importer.Resolution, error) {
	if opts.CorpusPath != "" {
		return importer.Check(opts.TranslationsDir, opts.CorpusPath)
	}
	return importer.CheckAgainstSource(opts.TranslationsDir, opts.SourcePath)
}

// resolveAutoAssets fills the pinned production inputs from the checkout. The
// voice `main` and the title atlas are extracted game data; they are read-only
// and must already exist under .local/work/source. A missing file is a
// clear pre-build error, never a silently skipped component.
func resolveAutoAssets(opts *Options) error {
	root := strings.TrimSpace(opts.Root)
	if root == "" {
		var err error
		root, err = project.FindRoot()
		if err != nil {
			return fmt.Errorf("build: automatic assets: %w", err)
		}
	}
	source := filepath.Join(project.WorkDir(root), "source")
	voiceMain := filepath.Join(source, "exefs", "main")
	atlas := filepath.Join(source, "romfs", "GUI", "Opening100.ftx")
	for _, asset := range []struct {
		path string
		want string
	}{
		{voiceMain, "the extracted ExeFS main (NSO0)"},
		{atlas, "the extracted GUI/Opening100.ftx credit atlas"},
	} {
		info, err := os.Stat(asset.path)
		if err != nil {
			return fmt.Errorf("build: automatic assets: %s is required at %s", asset.want, paths.Display(asset.path))
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("build: automatic assets: %s at %s is not a regular file", asset.want, paths.Display(asset.path))
		}
	}
	opts.Voice = true
	opts.VoiceMain = voiceMain
	opts.TitleFooterFTX = atlas
	return nil
}

// preparedState is the shared intermediate produced from the source archive.
type preparedState struct {
	builds  map[Variant][]editionMember
	changed map[Variant]int
	planned int
	noOps   int
	// reverts lists record IDs whose no-honorifics value equals the source slot
	// text while the canonical (honorifics) value differs: the no-honorifics
	// edition leaves the original English there.
	reverts []string
}

// prepareEditions decompresses every targeted member exactly once, then marshals
// each edition from that same shared source member bytes. Parsing per edition
// keeps the two editions fully independent even when one edition leaves a slot
// at its original value.
func prepareEditions(sourcePath string, plan *Plan) (*preparedState, error) {
	a, err := cpk.OpenFile(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("build: %w", err)
	}
	defer a.Close()

	st := &preparedState{
		builds: map[Variant][]editionMember{
			VariantHonorifics:   {},
			VariantNoHonorifics: {},
		},
		changed: map[Variant]int{},
	}

	for _, member := range plan.Order {
		fp := plan.Files[member]
		e := a.Member(member)
		if e == nil {
			return nil, fmt.Errorf("build: unknown record target %q: no such member in the source CPK", member)
		}
		data, err := a.Data(e)
		if err != nil {
			return nil, fmt.Errorf("build: %s: %w", member, err)
		}
		sheet, err := fms.Parse(data)
		if err != nil {
			return nil, fmt.Errorf("build: %s: %w", member, err)
		}

		honEdits := map[int][]byte{}
		noHonEdits := map[int][]byte{}
		slots := make([]int, 0, len(fp.Edits))
		for slot, ed := range fp.Edits {
			if slot >= sheet.SlotCount() {
				return nil, fmt.Errorf("build: unknown record target %s: slot %d outside member %s (slot count %d)",
					ed.RecordID, slot, member, sheet.SlotCount())
			}
			slots = append(slots, slot)
			honEdits[slot] = ed.Honorifics
			noHonEdits[slot] = ed.NoHonorifics
			cur := sheet.Slot(slot)
			if !bytes.Equal(cur, ed.Honorifics) {
				st.changed[VariantHonorifics]++
			}
			if !bytes.Equal(cur, ed.NoHonorifics) {
				st.changed[VariantNoHonorifics]++
			}
			if bytes.Equal(cur, ed.Honorifics) && bytes.Equal(cur, ed.NoHonorifics) {
				st.noOps++
			}
			if !bytes.Equal(cur, ed.Honorifics) && bytes.Equal(cur, ed.NoHonorifics) {
				st.reverts = append(st.reverts, ed.RecordID)
			}
		}
		sort.Ints(slots)
		st.planned += len(slots)
		if len(slots) == 0 {
			continue
		}

		honBytes, err := marshalEdition(data, honEdits)
		if err != nil {
			return nil, fmt.Errorf("build: %s: %w", member, err)
		}
		noHonBytes, err := marshalEdition(data, noHonEdits)
		if err != nil {
			return nil, fmt.Errorf("build: %s: %w", member, err)
		}
		// A member is only staged for an edition when its marshalled bytes
		// actually differ from the source member.
		if !bytes.Equal(honBytes, data) {
			st.builds[VariantHonorifics] = append(st.builds[VariantHonorifics], editionMember{
				Member: member, File: fp.File, Slots: slots, Stored: honBytes,
			})
		}
		if !bytes.Equal(noHonBytes, data) {
			st.builds[VariantNoHonorifics] = append(st.builds[VariantNoHonorifics], editionMember{
				Member: member, File: fp.File, Slots: slots, Stored: noHonBytes,
			})
		}
	}
	sortRecordIDs(st.reverts)
	return st, nil
}

// titleFooterBuild is the rebuilt title-screen atlas staged into both editions.
type titleFooterBuild struct {
	text       string
	member     string
	stored     []byte
	sha256     string
	fontSHA256 string
	configSHA  string
	typography string
	proof      titlefooter.Proof
}

// prepareTitleFooter renders the version footer into the user's own extracted
// title-screen credit atlas. It is a no-op unless Options.TitleFooterFTX is set;
// when it is set a real version is mandatory, because the footer text is the
// release version and is never invented.
func prepareTitleFooter(opts Options) (*titleFooterBuild, error) {
	path := strings.TrimSpace(opts.TitleFooterFTX)
	if path == "" {
		return nil, nil
	}
	version := strings.TrimSpace(opts.Version)
	if version == "" {
		return nil, fmt.Errorf("build: a title footer needs a version from the checkout root %s", VersionFileName)
	}
	cfg, err := titlefooter.EmbeddedConfig()
	if err != nil {
		return nil, fmt.Errorf("build: title footer typography (%s): %w", "internal/assets/title_footer.json", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("build: reading title footer atlas: %w", err)
	}
	out, proof, err := titlefooter.ApplyWithConfig(data, version, cfg)
	if err != nil {
		return nil, fmt.Errorf("build: title footer: %w", err)
	}
	text, err := titlefooter.FooterText(version)
	if err != nil {
		return nil, fmt.Errorf("build: title footer: %w", err)
	}
	sum := sha256.Sum256(out)
	fontSHA := titlefooter.FontSHA256()
	configSum := sha256.Sum256(titlefooter.ConfigJSON())
	return &titleFooterBuild{
		text:       text,
		member:     titlefooter.MemberPath,
		stored:     out,
		sha256:     hex.EncodeToString(sum[:]),
		fontSHA256: fontSHA,
		configSHA:  hex.EncodeToString(configSum[:]),
		typography: fmt.Sprintf("font_size=%g outline_size=%g line_spacing=%g offset_x=%g offset_y=%g",
			cfg.FontSize, cfg.OutlineSize, cfg.LineSpacing, cfg.OffsetX, cfg.OffsetY),
		proof: proof,
	}, nil
}

// marshalEdition parses a fresh sheet from the shared source bytes, applies the
// edition's slot values and marshals it.
func marshalEdition(sourceData []byte, edits map[int][]byte) ([]byte, error) {
	sheet, err := fms.Parse(sourceData)
	if err != nil {
		return nil, err
	}
	if err := applyEdits(sheet, edits); err != nil {
		return nil, err
	}
	return sheet.Marshal()
}

func applyEdits(s *fms.Sheet, edits map[int][]byte) error {
	slots := make([]int, 0, len(edits))
	for slot := range edits {
		slots = append(slots, slot)
	}
	sort.Ints(slots)
	for _, slot := range slots {
		if err := s.SetSlot(slot, edits[slot]); err != nil {
			return err
		}
	}
	return nil
}

// rebuildEdition stages members in a fresh read-only view of the source archive
// and writes a complete rebuilt archive to memory. With no members staged the
// output is the no-op control.
func rebuildEdition(sourcePath string, members []editionMember) ([]byte, error) {
	a, err := cpk.OpenFile(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("build: %w", err)
	}
	defer a.Close()
	for _, m := range members {
		if m.IsNew {
			dir, name := splitMember(m.Member)
			if err := a.AddMember(dir, name, m.Stored, int64(len(m.Stored))); err != nil {
				return nil, fmt.Errorf("build: %w", err)
			}
			continue
		}
		if err := a.ReplaceMemberResized(m.Member, m.Stored, int64(len(m.Stored))); err != nil {
			return nil, fmt.Errorf("build: %w", err)
		}
	}
	var buf bytes.Buffer
	if _, err := a.RebuildVariable(&buf); err != nil {
		return nil, fmt.Errorf("build: %w", err)
	}
	return buf.Bytes(), nil
}

// validateEdition independently re-opens the rebuilt bytes and checks that every
// untouched member is byte-identical, every edited member reparses, its changed
// slots hold the intended text and its other slots are unchanged.
func validateEdition(sourcePath string, rebuilt []byte, members []editionMember) error {
	src, err := cpk.OpenFile(sourcePath)
	if err != nil {
		return fmt.Errorf("build: validation: %w", err)
	}
	defer src.Close()
	dst, err := cpk.Open(bytes.NewReader(rebuilt), int64(len(rebuilt)))
	if err != nil {
		return fmt.Errorf("build: validation: rebuilt archive does not parse: %w", err)
	}
	edited := map[string]map[int]bool{}
	newCount := 0
	for _, m := range members {
		if m.IsNew {
			newCount++
			continue
		}
		slots := map[int]bool{}
		for _, s := range m.Slots {
			slots[s] = true
		}
		edited[m.Member] = slots
	}
	if len(dst.Entries) != len(src.Entries)+newCount {
		return fmt.Errorf("build: validation: member count changed (%d -> %d, %d new staged)", len(src.Entries), len(dst.Entries), newCount)
	}
	changedMembers := 0
	for _, se := range src.Entries {
		de := dst.Member(se.Path())
		if de == nil {
			return fmt.Errorf("build: validation: member %q missing from the rebuilt archive", se.Path())
		}
		if _, isEdited := edited[se.Path()]; !isEdited {
			sr, err := src.RawData(se)
			if err != nil {
				return err
			}
			dr, err := dst.RawData(de)
			if err != nil {
				return err
			}
			if !bytes.Equal(sr, dr) {
				return fmt.Errorf("build: validation: untouched member %q changed", se.Path())
			}
			continue
		}
		changedMembers++
		sd, err := src.Data(se)
		if err != nil {
			return err
		}
		dd, err := dst.Data(de)
		if err != nil {
			return fmt.Errorf("build: validation: edited member %q: %w", se.Path(), err)
		}
		sSheet, err := fms.Parse(sd)
		if err != nil {
			return err
		}
		dSheet, err := fms.Parse(dd)
		if err != nil {
			return fmt.Errorf("build: validation: edited member %q does not reparse: %w", se.Path(), err)
		}
		if sSheet.SlotCount() != dSheet.SlotCount() {
			return fmt.Errorf("build: validation: %q slot count changed", se.Path())
		}
		want := map[int][]byte{}
		for _, m := range members {
			if m.Member != se.Path() {
				continue
			}
			parsed, err := fms.Parse(m.Stored)
			if err != nil {
				return fmt.Errorf("build: validation: %q staged bytes: %w", m.Member, err)
			}
			for _, s := range m.Slots {
				want[s] = parsed.Slot(s)
			}
		}
		for i := 0; i < dSheet.SlotCount(); i++ {
			exp := sSheet.Slot(i)
			if v, ok := want[i]; ok {
				exp = v
			}
			if !bytes.Equal(dSheet.Slot(i), exp) {
				return fmt.Errorf("build: validation: %s slot %d does not match the intended edit", se.Path(), i)
			}
		}
	}
	// Verify every member staged from scratch (the title footer) round-trips
	// byte-for-byte as an uncompressed member.
	for _, m := range members {
		if !m.IsNew {
			continue
		}
		de := dst.Member(m.Member)
		if de == nil {
			return fmt.Errorf("build: validation: new member %q missing from the rebuilt archive", m.Member)
		}
		raw, err := dst.RawData(de)
		if err != nil {
			return fmt.Errorf("build: validation: new member %q: %w", m.Member, err)
		}
		if !bytes.Equal(raw, m.Stored) {
			return fmt.Errorf("build: validation: new member %q payload changed", m.Member)
		}
		if de.Compressed() || de.FileSize != int64(len(m.Stored)) || de.ExtractSize != int64(len(m.Stored)) {
			return fmt.Errorf("build: validation: new member %q sizes = %d/%d compressed=%v",
				m.Member, de.FileSize, de.ExtractSize, de.Compressed())
		}
	}
	if changedMembers != len(members)-newCount {
		return fmt.Errorf("build: validation: %d members changed, expected %d", changedMembers, len(members)-newCount)
	}
	return nil
}

// splitMember splits a CPK member path into its directory and file name.
func splitMember(member string) (dir, name string) {
	if i := strings.LastIndexByte(member, '/'); i >= 0 {
		return member[:i], member[i+1:]
	}
	return "", member
}

func fileResults(members []editionMember) []FileResult {
	out := make([]FileResult, 0, len(members))
	for _, m := range members {
		n := int64(len(m.Stored))
		out = append(out, FileResult{Member: m.Member, Slots: append([]int(nil), m.Slots...), StoredSize: n, ExtractSize: n, NewMember: m.IsNew})
	}
	return out
}

// translationsLabel resolves a human label for the translation directory. It is
// derived from the directory name alone: no revision number, tracker file or
// tracker note is consulted, so a generated report can never leak an internal
// revision identifier.
func translationsLabel(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "translations"
	}
	base := filepath.Base(filepath.Clean(dir))
	if base == "." || base == string(filepath.Separator) || base == "" {
		return "translations"
	}
	return base
}

func defaultRunName(label, date string) string {
	if date == "" {
		date = time.Now().Format("20060102")
	}
	label = sanitizeName(label)
	if label == "" {
		label = "translations"
	}
	return label + "-" + date
}

// sortRecordIDs sorts "<file>:<slot>" ids by file name then numeric slot.
func sortRecordIDs(ids []string) {
	sort.Slice(ids, func(i, j int) bool {
		fi, si, _ := splitRecordID(ids[i])
		fj, sj, _ := splitRecordID(ids[j])
		if fi != fj {
			return fi < fj
		}
		return si < sj
	})
}

func sanitizeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return strings.Trim(b.String(), "_")
}
