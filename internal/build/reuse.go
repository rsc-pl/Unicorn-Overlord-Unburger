package build

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// uniqueRunName returns base when <outDir>/<base> is free, otherwise the first
// free "base-2", "base-3", ... It is deterministic for a given filesystem state.
func uniqueRunName(outDir, base string) string {
	for i := 1; ; i++ {
		cand := base
		if i > 1 {
			cand = fmt.Sprintf("%s-%d", base, i)
		}
		if _, err := os.Lstat(filepath.Join(outDir, cand)); err != nil {
			return cand
		}
	}
}

// reusedRun is a verified published build that this run can adopt instead of
// rebuilding.
type reusedRun struct {
	editions []EditionResult
	voice    *VoiceResult
}

// reusePublishedRun reports whether runDir already holds a complete, verified
// build of exactly this run's identity, and if so returns its editions (and voice
// stage) so the expensive rebuild, re-validation and re-publish can be skipped.
//
// Reuse is deliberately strict: every requested edition must have a report whose
// source digest, dataset identity and version match the run being built, and the
// published CPK must still hash to the digest that report recorded. Any mismatch
// returns nil, leaving publishAll to refuse, so a foreign, stale or unverified
// directory is never silently adopted or overwritten. This is what makes a repeat
// build on the same day succeed without destroying the earlier run.
func reusePublishedRun(result *Result, runDir string, variants []Variant, opts Options) *reusedRun {
	info, err := os.Stat(runDir)
	if err != nil || !info.IsDir() {
		return nil
	}
	// Sweep abandoned staging here too: a repeat build that reuses the run never
	// reaches publishAll, and the run directory must not accumulate stale staging.
	sweepStaleStaging(runDir)
	editions := make([]EditionResult, 0, len(variants))
	for _, v := range variants {
		er := reuseEdition(result, runDir, v)
		if er == nil {
			return nil
		}
		editions = append(editions, *er)
	}
	voice := reuseVoice(result, runDir, opts)
	if opts.Voice && voice == nil {
		return nil
	}
	return &reusedRun{editions: editions, voice: voice}
}

// reuseEdition adopts one verified edition directory, or returns nil when it
// cannot be trusted for this run.
func reuseEdition(result *Result, runDir string, v Variant) *EditionResult {
	dir := filepath.Join(runDir, string(v))
	data, err := os.ReadFile(filepath.Join(dir, "BUILD_REPORT.json"))
	if err != nil {
		return nil
	}
	var er editionReport
	if err := json.Unmarshal(data, &er); err != nil {
		return nil
	}
	if er.Variant != string(v) {
		return nil
	}
	if result.Facts != nil && er.SourceSHA256 != "" && !strings.EqualFold(er.SourceSHA256, result.Facts.SHA256) {
		return nil
	}
	if er.DatasetCanonSHA != result.Dataset.CanonicalSHA256 {
		return nil
	}
	if strings.TrimSpace(er.Version) != result.Version {
		return nil
	}
	// The startup label is forced by the build (it tracks the root version.txt),
	// not by the translation dataset. Requiring it too means a run built before
	// the label existed — or with a different label — is never adopted just
	// because its version string matches.
	if er.StartupLabel != result.StartupLabel {
		return nil
	}
	// The title footer is an input-derived asset: a different atlas (or a build
	// without one) must never reuse a published edition that carries another.
	// The embedded typeface and the typography JSON are part of the identity too,
	// so editing internal/assets/title_footer.json invalidates the cache.
	if er.TitleFooterTag != result.TitleFooterTag ||
		er.TitleFooterMember != result.TitleFooterMember ||
		er.TitleFooterSHA != result.TitleFooterSHA256 {
		return nil
	}
	if er.TitleFooterFontSHA != result.TitleFooterFontSHA256 ||
		er.TitleFooterConfigSHA != result.TitleFooterConfigSHA256 {
		return nil
	}
	if result.TitleID != "" && er.TitleID != "" && er.TitleID != result.TitleID {
		return nil
	}
	cpk := filepath.Join(dir, filepath.FromSlash(artifactRel))
	sum, size, err := hashFileSHA256(cpk)
	if err != nil {
		return nil
	}
	if er.OutputSHA256 != "" && !strings.EqualFold(er.OutputSHA256, sum) {
		return nil
	}
	if er.OutputSize != 0 && er.OutputSize != size {
		return nil
	}
	out := &EditionResult{
		Variant:       v,
		Dir:           dir,
		ArtifactRel:   artifactRel,
		ArtifactPath:  cpk,
		SHA256:        sum,
		Size:          size,
		Files:         er.Files,
		PlannedSlots:  er.PlannedSlots,
		ChangedSlots:  er.ChangedSlots,
		EditedMembers: er.EditedMembers,
		Published:     true,
	}
	// Regenerate the reports from the current resolution so the reused edition and
	// a freshly built one are reported identically.
	out.ReportTxt = renderEditionText(result, out)
	jsonText, err := renderEditionJSON(result, out)
	if err != nil {
		return nil // cannot regenerate an identical report: do not reuse
	}
	out.ReportJSON = jsonText
	return out
}

// reuseVoice adopts a verified shared voice stage when one was requested, or
// returns nil when no stage is present. An unverified stage returns nil so the
// build falls through to a real rebuild.
func reuseVoice(result *Result, runDir string, opts Options) *VoiceResult {
	if !opts.Voice {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(voiceStageDir(runDir), "VOICE_REPORT.json"))
	if err != nil {
		return nil
	}
	var vr VoiceResult
	if err := json.Unmarshal(data, &vr); err != nil {
		return nil
	}
	main := filepath.Join(voiceStageDir(runDir), filepath.FromSlash(voiceArtifactRel))
	sum, size, err := hashFileSHA256(main)
	if err != nil {
		return nil
	}
	if vr.ArtifactSHA256 != "" && !strings.EqualFold(vr.ArtifactSHA256, sum) {
		return nil
	}
	if vr.ArtifactSize != 0 && vr.ArtifactSize != size {
		return nil
	}
	if vr.BuildID == "" {
		return nil
	}
	if vr.ArtifactRel == "" {
		vr.ArtifactRel = voiceArtifactRel
	}
	// A published stage from before the current status carries stale wording.
	// Do not adopt it; rebuild so every report and marker in the run describes
	// the current status.
	if vr.RuntimeStatus != voiceRuntimeConfirmed {
		return nil
	}
	return &vr
}
