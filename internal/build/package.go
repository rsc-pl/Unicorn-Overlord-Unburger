package build

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"uo-unburger/internal/layeredfs"
	"uo-unburger/internal/paths"
	"uo-unburger/internal/project"
)

// Package assembles install-ready ZIP archives from the *already-published*
// output of a build. It never rebuilds a CPK, never reads the game or the
// translation ledger, and never writes into the run's edition directories: it
// only copies the edition CPK and (optionally) the shared voice ExeFS artifact
// into per-edition, per-target archives.
//
// Every archive is deterministic: entries are sorted, every file gets a fixed
// modification time and mode, and compression parameters are fixed, so
// packaging the same inputs twice yields byte-identical ZIPs. A failure never
// leaves a partial archive behind: each ZIP is written to a temporary file in
// the destination directory, verified by reopening it, and the whole set is
// renamed into place only once every archive is ready.

// Archive entry names shared by both loader layouts. The emulator layout
// prefixes them with the single top-level mod directory (see
// layeredfs.EmulatorZipEntry); the Atmosphère layout keeps them at the archive
// root beside atmosphere/.
const (
	// ManifestEntryName is the machine-readable per-archive manifest.
	ManifestEntryName = "manifest.json"
	// InstallReadmeName is the neutral, human-readable install note.
	InstallReadmeName = "README-install.txt"
)

// zipEpoch is the fixed modification time written into every packaged entry so
// the archive bytes do not depend on the packaging clock. ZIP timestamps cannot
// precede 1980, so 2000-01-01 is used rather than the 1980 minimum.
var zipEpoch = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// PackageTarget selects which loader layout an archive is built for.
type PackageTarget string

const (
	// TargetAtmosphere is the Atmosphère (console CFW) layout.
	TargetAtmosphere PackageTarget = "atmosphere"
	// TargetEmulator is the Eden/Yuzu-family emulator mod layout.
	TargetEmulator PackageTarget = "emulator"
	// TargetBoth produces one archive per concrete target.
	TargetBoth PackageTarget = "both"
)

// ParsePackageTarget maps a CLI word to a PackageTarget.
func ParsePackageTarget(s string) (PackageTarget, error) {
	w := PackageTarget(strings.ToLower(strings.TrimSpace(s)))
	switch w {
	case TargetAtmosphere, TargetEmulator, TargetBoth:
		return w, nil
	}
	return "", fmt.Errorf("package: unknown target %q (want atmosphere|emulator|both)", s)
}

// Targets returns the concrete target list (both and the empty default expand
// to two targets; the normal release flow packages both layouts).
func (t PackageTarget) Targets() []PackageTarget {
	switch t {
	case "", TargetBoth:
		return []PackageTarget{TargetAtmosphere, TargetEmulator}
	case TargetAtmosphere:
		return []PackageTarget{TargetAtmosphere}
	default:
		return []PackageTarget{t}
	}
}

// PackageOptions configures a packaging run.
type PackageOptions struct {
	// Variants selects the concrete editions to package (never VariantBoth).
	// Empty means every edition in the build result.
	Variants []Variant
	// Targets selects the loader layout(s). Empty means both (atmosphere and
	// emulator), the normal release flow.
	Targets PackageTarget
	// RunDir is the published build run directory. Empty uses the result's own.
	RunDir string
	// OutDir is the destination for the ZIPs. Empty means the project release
	// directory (.local/release) when a checkout root can be derived from the
	// run directory, and <RunDir>/packages otherwise (library/test use).
	OutDir string
	// Version is the release version stamped into every manifest and used as the
	// release ZIP filename component. Empty falls back to the version recorded by
	// the build run (Result.Version, restored by LoadRun from BUILD_REPORT.json);
	// when neither exists packaging fails, because a release archive must name the
	// actual mod version. It is never hard-coded.
	Version string
	// Release is the neutral release label recorded in manifests and install
	// notes. Empty derives it from the translation dataset identity (never a run
	// name). It does not control the ZIP filename, which always uses Version.
	Release string
	// ModName is rejected when set: an emulator archive always contains exactly
	// one top-level directory named layeredfs.EmulatorModDir, because that is the
	// folder the operator ends up with. It is kept in the API (and the CLI keeps
	// accepting the flag) so a caller that still passes one gets a clear error
	// instead of an archive whose layout disagrees with the documentation.
	ModName string
	// VoiceMain overrides the voice `main` path (default <RunDir>/voice/exefs/main).
	VoiceMain string
	// VoiceIPS overrides the voice IPS path (default <RunDir>/voice/main.ips).
	VoiceIPS string
	// Overwrite replaces existing output files instead of refusing with a
	// *CollisionError. The CLI --force flag sets it; the TUI prompts instead.
	Overwrite bool
	// OnConflict, when set, is asked once per run with the sorted list of
	// destination files that already exist and returns the policy applied to all
	// of them. It is how a UI shows its Replace / Keep both / Cancel prompt without
	// this package depending on any UI. It takes precedence over Overwrite.
	OnConflict func(collisions []string) ConflictPolicy
	// VerifyDeterminism stages every archive twice and refuses to publish unless
	// the two stagings are byte-identical. It costs one extra pass over the
	// inputs and is intended for verification runs.
	VerifyDeterminism bool
}

// PackageFile identifies one file inside a package.
type PackageFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// PackageArtifact is one produced ZIP.
type PackageArtifact struct {
	Variant      Variant
	Target       PackageTarget
	Path         string
	RelPath      string
	SHA256       string
	Size         int64
	Files        []PackageFile
	TargetStatus string
	Manifest     packageManifest
}

// PackageResult is the outcome of a packaging run.
type PackageResult struct {
	RunDir  string
	OutDir  string
	Release string
	Version string
	// StartupLabel is the rendered "Unburger Mod V<version>" label recorded in
	// every manifest and install note. Packaging never rebuilds a CPK, so it
	// records the label but never writes it into the game asset.
	StartupLabel string
	Artifacts    []PackageArtifact
	SummaryTxt   string
	ReportTxt    string
	ReportJSON   string
	ReportPath   string
}

// packageManifest is the machine-readable description written into every ZIP.
// It never carries the ZIP's own digest (that would be self-referential); the
// per-file digests and the validation outcome are recorded instead, and the
// archive's own digest is reported by the caller and in PACKAGE_REPORT.json.
type packageManifest struct {
	Schema          string              `json:"schema"`
	Tool            string              `json:"tool"`
	Game            packageGame         `json:"game"`
	Edition         string              `json:"edition"`
	Dataset         packageDataset      `json:"dataset"`
	Target          packageTargetInfo   `json:"target"`
	TitleFooter     *packageTitleFooter `json:"title_footer,omitempty"`
	Voice           *packageVoice       `json:"voice,omitempty"`
	PatchComponents []string            `json:"patch_components"`
	Release         string              `json:"release"`
	Version         string              `json:"version,omitempty"`
	StartupLabel    string              `json:"startup_label,omitempty"`
	Validation      packageValidation   `json:"validation"`
}

// packageTitleFooter records the build-time title-screen footer baked into the
// RomFS replacement.
type packageTitleFooter struct {
	Text   string `json:"text"`
	Member string `json:"member"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type packageGame struct {
	TitleID        string `json:"title_id"`
	DisplayVersion string `json:"display_version,omitempty"`
	SourceSHA256   string `json:"source_sha256"`
	SourceSize     int64  `json:"source_size"`
}

type packageDataset struct {
	CanonicalSHA256 string `json:"canonical_sha256"`
	CanonicalRows   int    `json:"canonical_rows"`
	ChangedSlots    int    `json:"changed_slots"`
	EditedMembers   int    `json:"edited_members"`
}

type packageTargetInfo struct {
	Kind   string `json:"kind"`
	Status string `json:"status"`
	Layout string `json:"layout"`
}

type packageVoice struct {
	BuildID        string `json:"build_id"`
	ArtifactSHA256 string `json:"artifact_sha256"`
	ArtifactSize   int64  `json:"artifact_size"`
	IPSSHA256      string `json:"ips_sha256,omitempty"`
	IPSSize        int64  `json:"ips_size,omitempty"`
	RuntimeStatus  string `json:"runtime_status"`
	Placement      string `json:"placement"`
}

type packageValidation struct {
	Status        string        `json:"status"`
	Reopened      bool          `json:"reopened"`
	Deterministic bool          `json:"writer_deterministic"`
	Files         []PackageFile `json:"files"`
}

// packageEntry is one file to place inside an archive. Exactly one of Data or
// Src is used: Data for small generated files, Src to stream a large artifact
// from disk without loading it into memory.
type packageEntry struct {
	Name string
	Data []byte
	Src  string
}

// voiceComponent is the resolved shared voice artifact.
type voiceComponent struct {
	MainPath      string
	IPSPath       string
	BuildID       string
	MainSHA256    string
	MainSize      int64
	IPSSHA256     string
	IPSSize       int64
	RuntimeStatus string
}

// Package assembles the requested archives from a build result.
func Package(res *Result, opts PackageOptions) (*PackageResult, error) {
	if res == nil {
		return nil, fmt.Errorf("package: no build result to package")
	}
	runDir := strings.TrimSpace(opts.RunDir)
	if runDir == "" {
		runDir = res.RunDir
	}
	if runDir == "" {
		return nil, fmt.Errorf("package: a published build run directory is required")
	}
	if info, err := os.Stat(runDir); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("package: build run directory %s is not a directory", paths.Display(runDir))
	}
	outDir := strings.TrimSpace(opts.OutDir)
	if outDir == "" {
		// The final ZIPs are user-facing, so they belong in the project release
		// directory. A library/test run whose run directory is outside a
		// checkout falls back to the run's own packages folder.
		if root, ok := project.FindRootFrom(runDir); ok {
			outDir = project.ReleaseDir(root)
		} else {
			outDir = filepath.Join(runDir, "packages")
		}
	}
	targets := opts.Targets
	if targets == "" {
		targets = TargetBoth
	}
	if _, err := ParsePackageTarget(string(targets)); err != nil {
		return nil, err
	}
	targetList := targets.Targets()

	titleID := strings.TrimSpace(res.TitleID)
	if titleID == "" {
		titleID = DefaultTitleID
	}
	if !IsSupportedTitleID(titleID) {
		return nil, fmt.Errorf("package: unsupported TitleID %q", titleID)
	}

	// ModName is no longer used to name emulator archive entries: every emulator
	// archive extracts into exactly one folder, named
	// layeredfs.EmulatorModDir. A non-empty value is refused instead of being
	// silently ignored.
	if opts.ModName != "" {
		return nil, fmt.Errorf("package: --mod-name is not supported: an emulator archive always extracts into the single folder %q", layeredfs.EmulatorModDir)
	}

	// The shared Japanese-voice patch and the title footer are mandatory
	// components of every standard build. Refuse an older, incomplete or
	// hand-built run that lacks either one instead of publishing an archive
	// that silently drops a component the production flow always includes.
	if err := validatePackagingComponents(res); err != nil {
		return nil, err
	}

	// The shared voice patch is part of every normal build, so packaging includes
	// it automatically. There is no opt-in flag and no way to publish a run with
	// a silently dropped voice component.
	voice, err := resolveVoice(res, runDir, opts)
	if err != nil {
		return nil, err
	}

	release := sanitizeName(strings.TrimSpace(opts.Release))
	if release == "" {
		release = releaseLabel(res)
	}
	if release == "" {
		return nil, fmt.Errorf("package: could not derive a release label; pass --release")
	}

	// The ZIP filename carries the real mod version, not a content digest or a
	// random token. Use the same authoritative value that is stamped into the
	// manifests: the caller-supplied version (from the root version.txt) or, for
	// a standalone package run reconstructed by LoadRun, the version recorded in
	// the published build report. Never derive an independent version here.
	version := strings.TrimSpace(opts.Version)
	if version == "" {
		version = strings.TrimSpace(res.Version)
	}
	version, err = filenameVersion(version)
	if err != nil {
		return nil, err
	}

	// Resolve every edition's CPK before writing anything.
	type editionInput struct {
		variant Variant
		cpkPath string
		cpkSHA  string
		cpkSize int64
		changed int
		members int
	}
	var inputs []editionInput
	seen := map[Variant]bool{}
	variants := append([]Variant(nil), opts.Variants...)
	if len(variants) == 0 {
		for _, e := range res.Editions {
			variants = append(variants, e.Variant)
		}
	}
	for _, v := range variants {
		if v == VariantBoth || seen[v] {
			continue
		}
		seen[v] = true
		ei := editionInput{variant: v}
		for _, e := range res.Editions {
			if e.Variant != v {
				continue
			}
			ei.changed = e.ChangedSlots
			ei.members = e.EditedMembers
			if len(e.Files) > 0 {
				ei.members = len(e.Files)
			}
			if e.ArtifactPath != "" {
				ei.cpkPath = e.ArtifactPath
			}
		}
		if ei.cpkPath == "" {
			ei.cpkPath = filepath.Join(runDir, string(v), filepath.FromSlash(layeredfs.RomFSRel))
		}
		sum, size, err := hashFileSHA256(ei.cpkPath)
		if err != nil {
			return nil, fmt.Errorf("package: edition %s: %w", v, err)
		}
		ei.cpkSHA, ei.cpkSize = sum, size
		inputs = append(inputs, ei)
	}
	if len(inputs) == 0 {
		return nil, fmt.Errorf("package: no editions selected")
	}

	result := &PackageResult{RunDir: runDir, OutDir: outDir, Release: release, Version: version}
	result.StartupLabel = FormatTag(result.Version)

	type planned struct {
		art     PackageArtifact
		entries []packageEntry
		payload []PackageFile
	}
	var plans []planned
	for _, ei := range inputs {
		for _, target := range targetList {
			entries, manifest, payload, err := planArchive(res, ei.variant, ei.cpkPath, ei.cpkSHA, ei.cpkSize, ei.changed, ei.members, target, titleID, release, version, voice)
			if err != nil {
				return nil, err
			}
			name := archiveFilename(ei.variant, target, version)
			plans = append(plans, planned{
				art: PackageArtifact{
					Variant:      ei.variant,
					Target:       target,
					Path:         filepath.Join(outDir, name),
					RelPath:      name,
					TargetStatus: manifest.Target.Status,
					Manifest:     manifest,
				},
				entries: entries,
				payload: payload,
			})
		}
	}

	// Resolve the complete destination set once, before anything is written. The
	// existing-output policy — refuse, replace, keep both or cancel — applies to
	// every archive and to the two report files, and is decided a single time, so a
	// "both" run never mixes policies across its outputs.
	zipPaths := make([]string, len(plans))
	for i := range plans {
		zipPaths[i] = plans[i].art.Path
	}
	reportJSONPath := filepath.Join(outDir, "PACKAGE_REPORT.json")
	reportTXTPath := filepath.Join(outDir, "PACKAGE_REPORT.txt")
	destinations, err := resolveOutputConflicts(
		append(append([]string{}, zipPaths...), reportJSONPath, reportTXTPath),
		opts.Overwrite, opts.OnConflict)
	if err != nil {
		return nil, err
	}
	for i := range plans {
		plans[i].art.Path = destinations[i]
		plans[i].art.RelPath = filepath.Base(destinations[i])
	}
	result.ReportPath = destinations[len(plans)]

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, fmt.Errorf("package: creating output directory: %w", err)
	}
	sweepStalePackageTemps(outDir)

	staged := make([]stagedZip, 0, len(plans))
	cleanup := func() {
		for _, s := range staged {
			if s.tmp != "" {
				os.Remove(s.tmp)
			}
		}
	}
	for i := range plans {
		s, err := stageZip(plans[i].art.Path, plans[i].entries)
		if err != nil {
			cleanup()
			return nil, err
		}
		staged = append(staged, s)
		if opts.VerifyDeterminism {
			again, err := stageZip(plans[i].art.Path, plans[i].entries)
			if err != nil {
				cleanup()
				return nil, err
			}
			if again.sha != s.sha {
				os.Remove(again.tmp)
				cleanup()
				return nil, fmt.Errorf("package: %s is not byte-deterministic across two stagings", plans[i].art.RelPath)
			}
			os.Remove(again.tmp)
		}
	}
	// Verify each staged archive by reopening it and re-hashing its contents. The
	// re-opened payload must equal the list embedded in the manifest, so a
	// published archive can never disagree with its own manifest. Every archive is
	// validated before any of them is published.
	for i := range plans {
		s := staged[i]
		files, err := verifyZip(s.tmp)
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("package: verifying %s: %w", plans[i].art.RelPath, err)
		}
		if !payloadMatches(files, plans[i].payload, archiveEntryName(plans[i].art.Target, ManifestEntryName)) {
			cleanup()
			return nil, fmt.Errorf("package: %s does not match its manifest payload list", plans[i].art.RelPath)
		}
		if plans[i].art.Target == TargetEmulator {
			// The emulator layout contract: exactly one top-level directory, the
			// mod folder, holding only the known payload names. Checked on the
			// re-opened archive, before anything is published.
			if err := verifyEmulatorLayout(files); err != nil {
				cleanup()
				return nil, fmt.Errorf("package: %s: %w", plans[i].art.RelPath, err)
			}
		}
		plans[i].art.Files = files
		plans[i].art.SHA256 = s.sha
		plans[i].art.Size = s.size
	}

	for i := range plans {
		result.Artifacts = append(result.Artifacts, plans[i].art)
	}
	result.SummaryTxt = renderPackageSummary(result)
	result.ReportTxt = renderPackageReportText(result)
	reportJSON, err := renderPackageReportJSON(result)
	if err != nil {
		cleanup()
		return nil, err
	}
	result.ReportJSON = reportJSON

	// Stage the two external report files, then publish the whole set at once:
	// temp+rename per file with a whole-set rollback, so a failure never leaves a
	// mixed old/new archive set and never loses a previous valid archive.
	jsonTmp, err := stageFile(reportJSONPath, []byte(result.ReportJSON))
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("package: staging report: %w", err)
	}
	txtTmp, err := stageFile(reportTXTPath, []byte(result.ReportTxt))
	if err != nil {
		cleanup()
		os.Remove(jsonTmp)
		return nil, fmt.Errorf("package: staging report: %w", err)
	}

	items := make([]publishItem, 0, len(plans)+2)
	for i := range plans {
		items = append(items, publishItem{tmp: staged[i].tmp, final: plans[i].art.Path})
	}
	items = append(items,
		publishItem{tmp: jsonTmp, final: reportJSONPath},
		publishItem{tmp: txtTmp, final: reportTXTPath},
	)
	if perr := publishSet(items); perr != nil {
		cleanup()
		os.Remove(jsonTmp)
		os.Remove(txtTmp)
		return nil, fmt.Errorf("package: %w", perr)
	}
	return result, nil
}

// stageFile writes data to a uniquely named temporary file beside dst and returns
// its name. Nothing is visible at dst until publishSet renames it into place.
func stageFile(dst string, data []byte) (string, error) {
	dir := filepath.Dir(dst)
	f, err := os.CreateTemp(dir, "."+filepath.Base(dst)+".tmp-*")
	if err != nil {
		return "", err
	}
	name := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(name)
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return "", err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}

// sweepStalePackageTemps removes abandoned staging files left in an output
// directory by an interrupted packaging run. Only names this package creates are
// considered (a leading "." plus ".tmp" on our archive or report prefixes) and
// only once they are older than staleStagingAge, so an unrelated file is never
// touched and a concurrent run's staging is never disturbed. The sweep is
// best-effort: unique temporary names mean a leftover never blocks a new run.
func sweepStalePackageTemps(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-staleStagingAge)
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, ".") || !strings.Contains(name, ".tmp") {
			continue
		}
		if !strings.HasPrefix(name, ".unicorn-overlord-") && !strings.HasPrefix(name, ".PACKAGE_REPORT") {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, name))
	}
}

// archiveFilename renders the deterministic release ZIP name for one edition and
// loader target. The descriptive prefix and the edition/target distinctions are
// preserved; only the trailing version component varies with the mod version.
// version has already passed filenameVersion validation.
func archiveFilename(variant Variant, target PackageTarget, version string) string {
	return fmt.Sprintf("unicorn-overlord-%s-%s-%s.zip",
		strings.ReplaceAll(string(variant), "_", "-"), target, version)
}

// planArchive builds the entry list, the manifest and the payload file list for
// one (edition, target). The payload list excludes manifest.json, which cannot
// hash itself, and is embedded in the manifest so every archive is
// self-describing.
func planArchive(res *Result, variant Variant, cpkPath, cpkSHA string, cpkSize int64, changed, members int,
	target PackageTarget, titleID, release, version string, voice *voiceComponent) ([]packageEntry, packageManifest, []PackageFile, error) {

	var entries []packageEntry
	var payload []PackageFile
	addData := func(name string, data []byte) {
		entries = append(entries, packageEntry{Name: name, Data: data})
		payload = append(payload, PackageFile{Path: name, Size: int64(len(data)), SHA256: sha256Hex(data)})
	}
	addSrc := func(name, src, sha string, size int64) {
		entries = append(entries, packageEntry{Name: name, Src: src})
		payload = append(payload, PackageFile{Path: name, Size: size, SHA256: sha})
	}

	var cpkName string
	var status string
	switch target {
	case TargetAtmosphere:
		cpkName = layeredfs.AtmospherePath(titleID, layeredfs.RomFSRel)
		status = "VERIFIED_LAYOUT"
	case TargetEmulator:
		// The emulator archive holds exactly one top-level directory, the mod
		// folder itself; every entry — the RomFS override, the optional ExeFS
		// override, the install note and the manifest — lives inside it. The
		// operator extracts the archive into the emulator's mod directory
		// (load/<title id>/) and gets that folder there.
		cpkName = archiveEntryName(target, layeredfs.RomFSRel)
		// The romfs mod folder shape follows Eden's documented layout. The
		// console target keeps the conservative layout-only status because the
		// two loaders use different install paths.
		status = "RUNTIME_VERIFIED"
	default:
		return nil, packageManifest{}, nil, fmt.Errorf("package: unsupported target %q", target)
	}
	addSrc(cpkName, cpkPath, cpkSHA, cpkSize)

	var srcSHA string
	var srcSize int64
	if res.Facts != nil {
		srcSHA, srcSize = res.Facts.SHA256, res.Facts.Size
	}
	manifest := packageManifest{
		Schema: "uo-unburger.package-1",
		Tool:   "uo-unburger",
		Game: packageGame{
			TitleID:        titleID,
			DisplayVersion: res.DisplayVersion,
			SourceSHA256:   srcSHA,
			SourceSize:     srcSize,
		},
		Edition: string(variant),
		Dataset: packageDataset{
			CanonicalSHA256: res.Dataset.CanonicalSHA256,
			CanonicalRows:   res.Dataset.CanonicalRows,
			ChangedSlots:    changed,
			EditedMembers:   members,
		},
		Target:          packageTargetInfo{Kind: string(target), Status: status, Layout: layoutRoot(target, titleID)},
		PatchComponents: []string{"romfs:" + layeredfs.RomFSRel + " (LayeredFS RomFS replacement)"},
		Release:         release,
		Version:         version,
		StartupLabel:    FormatTag(version),
	}
	if res.TitleFooterTag != "" {
		manifest.TitleFooter = &packageTitleFooter{
			Text:   res.TitleFooterTag,
			Member: res.TitleFooterMember,
			Size:   res.TitleFooterSize,
			SHA256: res.TitleFooterSHA256,
		}
		manifest.PatchComponents = append(manifest.PatchComponents,
			"title-footer: "+res.TitleFooterMember+" ("+res.TitleFooterTag+")")
	}

	if voice != nil {
		var voiceName string
		switch target {
		case TargetAtmosphere:
			voiceName = layeredfs.AtmospherePath(titleID, layeredfs.VoiceExeFSRel)
		case TargetEmulator:
			voiceName = archiveEntryName(target, layeredfs.VoiceExeFSRel)
		}
		addSrc(voiceName, voice.MainPath, voice.MainSHA256, voice.MainSize)
		manifest.Voice = &packageVoice{
			BuildID:        voice.BuildID,
			ArtifactSHA256: voice.MainSHA256,
			ArtifactSize:   voice.MainSize,
			IPSSHA256:      voice.IPSSHA256,
			IPSSize:        voice.IPSSize,
			RuntimeStatus:  voice.RuntimeStatus,
			Placement:      voiceName,
		}
		manifest.PatchComponents = append(manifest.PatchComponents,
			"voice-exefs: "+layeredfs.VoiceExeFSRel+" (shared, Build-ID-gated; "+voice.RuntimeStatus+")")
	} else {
		manifest.PatchComponents = append(manifest.PatchComponents, "voice-exefs: NOT_INCLUDED")
	}

	addData(archiveEntryName(target, InstallReadmeName), []byte(renderInstallReadme(target, titleID, variant, release, version, voice)))
	sort.Slice(payload, func(i, j int) bool { return payload[i].Path < payload[j].Path })
	manifest.Validation = packageValidation{Status: "PASS", Reopened: true, Deterministic: true, Files: payload}
	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, packageManifest{}, nil, fmt.Errorf("package: encoding manifest: %w", err)
	}
	entries = append(entries, packageEntry{Name: archiveEntryName(target, ManifestEntryName), Data: append(manifestJSON, '\n')})

	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, manifest, payload, nil
}

// archiveEntryName maps a layout-neutral entry name to the entry name an
// archive of this target uses. The Atmosphère layout keeps the name as-is (its
// entries are relative to the archive root and the RomFS override carries the
// full atmosphere/contents/<title id>/... path); the emulator layout places
// every entry inside the single top-level mod directory, so the whole archive
// extracts into exactly one folder.
func archiveEntryName(target PackageTarget, rel string) string {
	if target == TargetEmulator {
		return layeredfs.EmulatorZipEntry(rel)
	}
	return rel
}

// verifyEmulatorLayout checks a re-opened emulator archive: every entry must sit
// inside the single top-level directory named layeredfs.EmulatorModDir, that
// directory must hold exactly the known payload names, and no load/,
// atmosphere/ or title-id wrapper may appear at the archive root. It is the
// machine check behind "the ZIP root contains exactly one folder".
func verifyEmulatorLayout(files []PackageFile) error {
	const wantDir = layeredfs.EmulatorModDir
	prefix := wantDir + "/"
	allowed := map[string]bool{
		layeredfs.RomFSRel:      true,
		layeredfs.VoiceExeFSRel: true,
		ManifestEntryName:       true,
		InstallReadmeName:       true,
	}
	present := map[string]bool{}
	for _, f := range files {
		if !strings.HasPrefix(f.Path, prefix) {
			return fmt.Errorf("emulator archive entry %q is not inside the single top-level directory %q", f.Path, wantDir)
		}
		rel := strings.TrimPrefix(f.Path, prefix)
		if !allowed[rel] {
			return fmt.Errorf("emulator archive has an unexpected entry %q", f.Path)
		}
		present[rel] = true
	}
	for _, required := range []string{layeredfs.RomFSRel, ManifestEntryName, InstallReadmeName} {
		if !present[required] {
			return fmt.Errorf("emulator archive is missing %s/%s", wantDir, required)
		}
	}
	return nil
}

// sha256Hex returns the lowercase hex SHA-256 of b.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// payloadMatches reports whether the re-opened archive carries exactly the
// planned payload files (everything except the manifest, which cannot hash
// itself). Both slices are sorted by path. manifestName is the manifest's entry
// name for this target, which differs between the Atmosphère and emulator
// layouts.
func payloadMatches(verified, planned []PackageFile, manifestName string) bool {
	got := make([]PackageFile, 0, len(verified))
	for _, f := range verified {
		if f.Path == manifestName {
			continue
		}
		got = append(got, f)
	}
	if len(got) != len(planned) {
		return false
	}
	for i := range got {
		if got[i] != planned[i] {
			return false
		}
	}
	return true
}

func layoutRoot(target PackageTarget, titleID string) string {
	switch target {
	case TargetAtmosphere:
		return layeredfs.AtmosphereRoot + "/" + titleID
	case TargetEmulator:
		// The archive root is the mod folder itself: the layout is the single
		// top-level directory every entry lives in.
		return layeredfs.EmulatorModDir
	}
	return ""
}

// validatePackagingComponents enforces the mandatory build components. Every
// standard build carries the shared Japanese-voice ExeFS patch and the title
// footer, and both are shared by the two text editions. A run produced before
// those components became automatic, or an incomplete/hand-built result, must
// fail packaging instead of producing an archive that omits a mandatory patch.
func validatePackagingComponents(res *Result) error {
	if res.Voice == nil {
		return fmt.Errorf("package: the build is missing the mandatory Japanese-voice ExeFS patch; rebuild the run with the standard build")
	}
	if id := strings.TrimSpace(res.Voice.BuildID); id == "" || strings.EqualFold(id, "UNVERIFIED") {
		return fmt.Errorf("package: the build's voice patch has no verified Build ID; rebuild the run with the standard build")
	}
	if strings.TrimSpace(res.TitleFooterTag) == "" {
		return fmt.Errorf("package: the build is missing the mandatory title footer; rebuild the run with the standard build")
	}
	if strings.TrimSpace(res.TitleFooterMember) == "" || res.TitleFooterSize <= 0 || strings.TrimSpace(res.TitleFooterSHA256) == "" {
		return fmt.Errorf("package: the build's title-footer metadata is incomplete; rebuild the run with the standard build")
	}
	return nil
}

// resolveVoice resolves and verifies the shared voice artifact.
func resolveVoice(res *Result, runDir string, opts PackageOptions) (*voiceComponent, error) {
	mainPath := strings.TrimSpace(opts.VoiceMain)
	if mainPath == "" {
		mainPath = filepath.Join(runDir, "voice", filepath.FromSlash(layeredfs.VoiceExeFSRel))
	}
	sum, size, err := hashFileSHA256(mainPath)
	if err != nil {
		return nil, fmt.Errorf("package: voice artifact: %w", err)
	}
	vc := &voiceComponent{
		MainPath:      mainPath,
		MainSHA256:    sum,
		MainSize:      size,
		BuildID:       "UNVERIFIED",
		RuntimeStatus: voiceRuntimeConfirmed,
	}
	if res != nil && res.Voice != nil {
		v := res.Voice
		if v.ArtifactSHA256 != "" && !strings.EqualFold(v.ArtifactSHA256, sum) {
			return nil, fmt.Errorf("package: voice artifact sha256 %s does not match the build report %s", sum, v.ArtifactSHA256)
		}
		if v.ArtifactSize != 0 && v.ArtifactSize != size {
			return nil, fmt.Errorf("package: voice artifact size %d does not match the build report %d", size, v.ArtifactSize)
		}
		if v.BuildID != "" {
			vc.BuildID = v.BuildID
		}
		if v.RuntimeStatus != "" {
			vc.RuntimeStatus = v.RuntimeStatus
		}
	}
	ipsPath := strings.TrimSpace(opts.VoiceIPS)
	if ipsPath == "" {
		ipsPath = filepath.Join(runDir, "voice", "main.ips")
	}
	if s2, sz2, err := hashFileSHA256(ipsPath); err == nil {
		vc.IPSPath, vc.IPSSHA256, vc.IPSSize = ipsPath, s2, sz2
		if res != nil && res.Voice != nil && res.Voice.IPSSHA256 != "" && !strings.EqualFold(res.Voice.IPSSHA256, s2) {
			return nil, fmt.Errorf("package: voice IPS sha256 %s does not match the build report %s", s2, res.Voice.IPSSHA256)
		}
	}
	return vc, nil
}

// stagedZip is a written-but-not-yet-published archive.
type stagedZip struct {
	tmp   string
	final string
	sha   string
	size  int64
}

// stageZip writes entries to a temporary file next to dst and returns its
// digest and size. Nothing is visible at dst until the caller renames it.
func stageZip(dst string, entries []packageEntry) (stagedZip, error) {
	dir := filepath.Dir(dst)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(dst)+".tmp-*")
	if err != nil {
		return stagedZip{}, fmt.Errorf("package: creating temporary archive: %w", err)
	}
	tmpName := tmp.Name()
	remove := true
	defer func() {
		if remove {
			os.Remove(tmpName)
		}
	}()

	zw := zip.NewWriter(tmp)
	fail := func(err error) (stagedZip, error) {
		zw.Close()
		tmp.Close()
		return stagedZip{}, err
	}
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.Name, Method: zip.Deflate, Modified: zipEpoch}
		h.SetMode(0o644)
		w, err := zw.CreateHeader(h)
		if err != nil {
			return fail(fmt.Errorf("package: writing %s: %w", e.Name, err))
		}
		if e.Data != nil {
			if _, err := w.Write(e.Data); err != nil {
				return fail(fmt.Errorf("package: writing %s: %w", e.Name, err))
			}
			continue
		}
		src, err := os.Open(e.Src)
		if err != nil {
			return fail(fmt.Errorf("package: opening %s: %s", paths.Display(e.Src), paths.Error(err)))
		}
		_, cerr := io.Copy(w, src)
		src.Close()
		if cerr != nil {
			return fail(fmt.Errorf("package: streaming %s: %w", e.Name, cerr))
		}
	}
	if err := zw.Close(); err != nil {
		tmp.Close()
		return stagedZip{}, fmt.Errorf("package: finalising archive: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return stagedZip{}, fmt.Errorf("package: closing archive: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return stagedZip{}, fmt.Errorf("package: setting archive permissions: %w", err)
	}
	sum, size, err := hashFileSHA256(tmpName)
	if err != nil {
		return stagedZip{}, fmt.Errorf("package: hashing archive: %w", err)
	}
	remove = false
	return stagedZip{tmp: tmpName, final: dst, sha: sum, size: size}, nil
}

// verifyZip reopens an archive and returns its files (path, size, sha256),
// sorted. It rejects a path that escapes the tree, a duplicate entry, a
// directory entry or any unreadable member, so the caller can trust an archive
// before publishing it.
func verifyZip(path string) ([]PackageFile, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	seen := map[string]bool{}
	files := make([]PackageFile, 0, len(zr.File))
	for _, f := range zr.File {
		name := f.Name
		if name == "" || strings.HasPrefix(name, "/") || strings.HasPrefix(name, `\`) ||
			strings.Contains(name, "..") || strings.Contains(name, "\\") {
			return nil, fmt.Errorf("unsafe entry %q", name)
		}
		if strings.HasSuffix(name, "/") || f.FileInfo().IsDir() {
			return nil, fmt.Errorf("unexpected directory entry %q", name)
		}
		if seen[name] {
			return nil, fmt.Errorf("duplicate entry %q", name)
		}
		seen[name] = true
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", name, err)
		}
		h := sha256.New()
		n, err := io.Copy(h, rc)
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		if n != int64(f.UncompressedSize64) {
			return nil, fmt.Errorf("%s: read %d bytes, header says %d", name, n, f.UncompressedSize64)
		}
		files = append(files, PackageFile{Path: name, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// releaseLabel derives a neutral release label from the translation dataset
// identity. It is never a batch or run name.
func releaseLabel(res *Result) string {
	if res == nil {
		return ""
	}
	if s := strings.TrimSpace(res.Dataset.CanonicalSHA256); s != "" {
		return shortHex(s, 12)
	}
	es := append([]EditionResult(nil), res.Editions...)
	if len(es) == 0 {
		return ""
	}
	sort.Slice(es, func(i, j int) bool { return es[i].Variant < es[j].Variant })
	h := sha256.New()
	for _, e := range es {
		fmt.Fprintf(h, "%s:%s\n", e.Variant, e.SHA256)
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

func shortHex(s string, n int) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) > n {
		s = s[:n]
	}
	return s
}

// renderInstallReadme renders the neutral, target-specific installation note.
// modName is gone from the signature: the emulator folder name is fixed by
// layeredfs.EmulatorModDir, so there is nothing for a caller to choose.
func renderInstallReadme(target PackageTarget, titleID string, edition Variant, release, version string, voice *voiceComponent) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Unicorn Overlord text mod (edition: %s)\n", edition)
	fmt.Fprintf(&b, "release: %s\n", release)
	if v := strings.TrimSpace(version); v != "" {
		fmt.Fprintf(&b, "version: %s\n", v)
		fmt.Fprintf(&b, "startup label: %s\n", FormatTag(v))
	}
	b.WriteString("\nGenerated from game files you supplied. It contains only modified resources:\n")
	b.WriteString("the localization archive override and the shared Japanese-voice patch.\n\n")
	switch target {
	case TargetAtmosphere:
		b.WriteString("Target: Atmosphère (console CFW) LayeredFS.\n\n")
		b.WriteString("Install:\n")
		b.WriteString("  1. Copy the atmosphere/ folder to the root of your SD card, merging it.\n")
		fmt.Fprintf(&b, "  2. The override lands at atmosphere/contents/%s/romfs/Unicorn_US.CPK.\n", titleID)
		if voice != nil {
			fmt.Fprintf(&b, "     The voice patch lands at atmosphere/contents/%s/exefs/main.\n", titleID)
		}
		b.WriteString("  3. Boot the game.\n\n")
		b.WriteString("Uninstall: delete the <title-id> folder you added under atmosphere/contents and restore\n")
		b.WriteString("any backup you kept. Removing the override restores the original game text.\n\n")
	case TargetEmulator:
		b.WriteString("Target: Eden mod folder (Yuzu/Ryujinx-family layout).\n\n")
		fmt.Fprintf(&b, "Extracting this archive creates exactly one folder, %s/.\n", layeredfs.EmulatorModDir)
		b.WriteString("Its contents are:\n")
		fmt.Fprintf(&b, "  %s/romfs/Unicorn_US.CPK\n", layeredfs.EmulatorModDir)
		if voice != nil {
			fmt.Fprintf(&b, "  %s/exefs/main\n", layeredfs.EmulatorModDir)
		}
		fmt.Fprintf(&b, "  %s/manifest.json\n", layeredfs.EmulatorModDir)
		fmt.Fprintf(&b, "  %s/README-install.txt (this file)\n\n", layeredfs.EmulatorModDir)
		b.WriteString("Install:\n")
		b.WriteString("  1. Open the emulator's mod data location for this title:\n")
		fmt.Fprintf(&b, "       %s/%s/\n", layeredfs.EmulatorRoot, titleID)
		b.WriteString("  2. Extract this archive there, so the mod folder lands in that directory:\n")
		fmt.Fprintf(&b, "       %s/%s/%s/%s\n", layeredfs.EmulatorRoot, titleID, layeredfs.EmulatorModDir, layeredfs.RomFSRel)
		if voice != nil {
			fmt.Fprintf(&b, "       %s/%s/%s/%s\n", layeredfs.EmulatorRoot, titleID, layeredfs.EmulatorModDir, layeredfs.VoiceExeFSRel)
		}
		b.WriteString("  3. Restart the emulator and enable the mod for the game.\n\n")
		b.WriteString("Do not move the folder: the emulator shows the mod under the name\n")
		fmt.Fprintf(&b, "%s.\n", layeredfs.EmulatorModDir)
		b.WriteString("Uninstall: disable or delete the mod folder.\n\n")
	}
	if voice != nil {
		fmt.Fprintf(&b, "Voice patch (%s):\n", voice.RuntimeStatus)
		fmt.Fprintf(&b, "  build id (gate): %s\n", voice.BuildID)
		b.WriteString("  This build rewrites four same-size .text words in the shared Japanese-voice\n")
		b.WriteString("  patch. The in-game voice menu still lists its English entry; Japanese\n")
		b.WriteString("  playback is forced regardless of the setting (selector removal not\n")
		b.WriteString("  implemented).\n\n")
	}
	b.WriteString("This is not a supported release and game support is not claimed.\n")
	return b.String()
}

func renderPackageSummary(res *PackageResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "uo-unburger package\n")
	fmt.Fprintf(&b, "run:                %s\n", paths.Display(res.RunDir))
	fmt.Fprintf(&b, "release:            %s\n", res.Release)
	if res.Version != "" {
		fmt.Fprintf(&b, "version:            %s\n", res.Version)
		fmt.Fprintf(&b, "startup label:      %s\n", res.StartupLabel)
	}
	fmt.Fprintf(&b, "output dir:         %s\n", paths.Display(res.OutDir))
	for _, a := range res.Artifacts {
		fmt.Fprintf(&b, "archive %-14s %-11s %s sha256=%s size=%d files=%d status=%s validation=%s\n",
			a.Variant, a.Target, a.RelPath, a.SHA256, a.Size, len(a.Files), a.TargetStatus, a.Manifest.Validation.Status)
	}
	return b.String()
}

// packageReport is the external, machine-readable packaging report. It lives
// outside the archives, so it can safely carry their own digests.
type packageReport struct {
	Schema       string              `json:"schema"`
	RunDir       string              `json:"run_dir"`
	Release      string              `json:"release"`
	Version      string              `json:"version,omitempty"`
	StartupLabel string              `json:"startup_label,omitempty"`
	OutputDir    string              `json:"output_dir"`
	Archives     []packageReportItem `json:"archives"`
}

type packageReportItem struct {
	Edition    string        `json:"edition"`
	Target     string        `json:"target"`
	Status     string        `json:"target_status"`
	File       string        `json:"file"`
	SHA256     string        `json:"sha256"`
	Size       int64         `json:"size"`
	Validation string        `json:"validation_status"`
	Files      []PackageFile `json:"files"`
}

func renderPackageReportText(res *PackageResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "uo-unburger package report\n")
	fmt.Fprintf(&b, "run:                %s\n", paths.Display(res.RunDir))
	fmt.Fprintf(&b, "release:            %s\n", res.Release)
	if res.Version != "" {
		fmt.Fprintf(&b, "version:            %s\n", res.Version)
		fmt.Fprintf(&b, "startup label:      %s\n", res.StartupLabel)
	}
	for _, a := range res.Artifacts {
		fmt.Fprintf(&b, "  %s target=%s status=%s validation=%s\n", a.RelPath, a.Target, a.TargetStatus, a.Manifest.Validation.Status)
		fmt.Fprintf(&b, "    sha256=%s size=%d\n", a.SHA256, a.Size)
		for _, f := range a.Files {
			fmt.Fprintf(&b, "    %-64s %10d %s\n", f.Path, f.Size, f.SHA256)
		}
	}
	return b.String()
}

func renderPackageReportJSON(res *PackageResult) (string, error) {
	rep := packageReport{
		Schema:       "uo-unburger.package-report-1",
		RunDir:       paths.Display(res.RunDir),
		Release:      res.Release,
		Version:      res.Version,
		StartupLabel: FormatTag(res.Version),
		OutputDir:    paths.Display(res.OutDir),
	}
	for _, a := range res.Artifacts {
		rep.Archives = append(rep.Archives, packageReportItem{
			Edition:    string(a.Variant),
			Target:     string(a.Target),
			Status:     a.TargetStatus,
			File:       filepath.ToSlash(a.RelPath),
			SHA256:     a.SHA256,
			Size:       a.Size,
			Validation: a.Manifest.Validation.Status,
			Files:      a.Files,
		})
	}
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return "", fmt.Errorf("package: encoding package report: %w", err)
	}
	return string(data) + "\n", nil
}

// LoadRun reconstructs the minimal build result a packaging run needs from a
// published run directory: one BUILD_REPORT.json per edition plus the optional
// shared voice stage. It never reads the game or the translations.
func LoadRun(runDir string, variants []Variant) (*Result, error) {
	if strings.TrimSpace(runDir) == "" {
		return nil, fmt.Errorf("package: a build run directory is required")
	}
	if info, err := os.Stat(runDir); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("package: build run directory %s is not a directory", paths.Display(runDir))
	}
	if len(variants) == 0 {
		entries, err := os.ReadDir(runDir)
		if err != nil {
			return nil, fmt.Errorf("package: reading run directory: %w", err)
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			v := Variant(e.Name())
			if _, err := ParseVariant(string(v)); err != nil || v == VariantBoth {
				continue
			}
			if _, err := os.Stat(filepath.Join(runDir, e.Name(), "BUILD_REPORT.json")); err == nil {
				variants = append(variants, v)
			}
		}
		sort.Slice(variants, func(i, j int) bool { return variants[i] < variants[j] })
	}
	if len(variants) == 0 {
		return nil, fmt.Errorf("package: no published editions found under %s", paths.Display(runDir))
	}

	res := &Result{RunDir: runDir}
	for _, v := range variants {
		data, err := os.ReadFile(filepath.Join(runDir, string(v), "BUILD_REPORT.json"))
		if err != nil {
			return nil, fmt.Errorf("package: edition %s has no BUILD_REPORT.json", v)
		}
		var er editionReport
		if err := json.Unmarshal(data, &er); err != nil {
			return nil, fmt.Errorf("package: edition %s report is unreadable: %w", v, err)
		}
		cpk := filepath.Join(runDir, string(v), filepath.FromSlash(layeredfs.RomFSRel))
		sum, size, err := hashFileSHA256(cpk)
		if err != nil {
			return nil, fmt.Errorf("package: edition %s: %w", v, err)
		}
		if er.OutputSHA256 != "" && !strings.EqualFold(er.OutputSHA256, sum) {
			return nil, fmt.Errorf("package: edition %s CPK sha256 %s does not match its report %s", v, sum, er.OutputSHA256)
		}
		res.Editions = append(res.Editions, EditionResult{
			Variant:       v,
			ArtifactRel:   layeredfs.RomFSRel,
			ArtifactPath:  cpk,
			SHA256:        sum,
			Size:          size,
			ChangedSlots:  er.ChangedSlots,
			EditedMembers: er.EditedMembers,
			Published:     true,
		})
		if res.TitleID == "" {
			res.TitleID = er.TitleID
		}
		if res.DisplayVersion == "" {
			res.DisplayVersion = er.DisplayVersion
		}
		if res.Version == "" {
			res.Version = er.Version
			res.StartupLabel = er.StartupLabel
		}
		res.Dataset.CanonicalSHA256 = er.DatasetCanonSHA
		res.Dataset.CanonicalRows = er.DatasetCanonRows
		res.Facts = &SourceFacts{SHA256: er.SourceSHA256, Size: er.SourceSize}
		// Packaging reads the footer metadata back from the per-edition report so
		// a standalone `package` run describes the same components as the build
		// that produced the run. Both editions carry one shared footer.
		if res.TitleFooterTag == "" {
			res.TitleFooterTag = er.TitleFooterTag
			res.TitleFooterMember = er.TitleFooterMember
			res.TitleFooterSize = er.TitleFooterSize
			res.TitleFooterSHA256 = er.TitleFooterSHA
			res.TitleFooterFontSHA256 = er.TitleFooterFontSHA
			res.TitleFooterConfigSHA256 = er.TitleFooterConfigSHA
			res.TitleFooterTypography = er.TitleFooterTypography
		}
	}
	res.Voice = loadVoiceStage(runDir)
	return res, nil
}

// loadVoiceStage reads an optional published voice stage (the shared patch and
// its report) and verifies the artifact digests against it. It returns nil when
// the stage is absent or inconsistent.
func loadVoiceStage(runDir string) *VoiceResult {
	mainPath := filepath.Join(runDir, "voice", filepath.FromSlash(layeredfs.VoiceExeFSRel))
	sum, size, err := hashFileSHA256(mainPath)
	if err != nil {
		return nil
	}
	vr := &VoiceResult{
		ArtifactRel:    layeredfs.VoiceExeFSRel,
		ArtifactSHA256: sum,
		ArtifactSize:   size,
		BuildID:        "UNVERIFIED",
		RuntimeStatus:  voiceRuntimeConfirmed,
	}
	if data, err := os.ReadFile(filepath.Join(runDir, "voice", "VOICE_REPORT.json")); err == nil {
		var reported VoiceResult
		if json.Unmarshal(data, &reported) == nil {
			if reported.ArtifactSHA256 != "" && !strings.EqualFold(reported.ArtifactSHA256, sum) {
				return nil
			}
			if reported.ArtifactSize != 0 && reported.ArtifactSize != size {
				return nil
			}
			vr.BuildID = reported.BuildID
			vr.Plan = reported.Plan
			vr.RuntimeStatus = reported.RuntimeStatus
		}
	}
	if s2, sz2, err := hashFileSHA256(filepath.Join(runDir, "voice", "main.ips")); err == nil {
		vr.IPSSHA256, vr.IPSSize = s2, sz2
	}
	return vr
}
