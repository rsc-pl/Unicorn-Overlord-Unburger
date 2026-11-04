package build

import (
	"encoding/json"
	"fmt"
	"strings"

	"uo-unburger/internal/layeredfs"
	"uo-unburger/internal/paths"
)

// editionReport is the machine-readable per-edition report.
type editionReport struct {
	Variant          string       `json:"variant"`
	Status           string       `json:"status"`
	Translations     string       `json:"translations"`
	Run              string       `json:"run"`
	SourcePath       string       `json:"source_path"`
	SourceSHA256     string       `json:"source_sha256"`
	SourceSize       int64        `json:"source_size"`
	SourceMembers    int          `json:"source_members"`
	SourceFMSMembers int          `json:"source_fms_members"`
	SourceMemberList string       `json:"source_member_list_sha256"`
	TitleID          string       `json:"title_id"`
	DisplayVersion   string       `json:"display_version,omitempty"`
	Version          string       `json:"version,omitempty"`
	StartupLabel     string       `json:"startup_label,omitempty"`
	SourceMode       string       `json:"source_mode"`
	DatasetCanonFile string       `json:"dataset_canonical_file"`
	DatasetCanonSHA  string       `json:"dataset_canonical_sha256"`
	DatasetCanonRows int          `json:"dataset_canonical_rows"`
	DatasetAltFile   string       `json:"dataset_alternate_file,omitempty"`
	DatasetAltSHA    string       `json:"dataset_alternate_sha256,omitempty"`
	DatasetAltRows   int          `json:"dataset_alternate_rows"`
	CanonicalRows    int          `json:"canonical_rows"`
	Overrides        int          `json:"overrides"`
	Applied          int          `json:"applied"`
	Rejected         int          `json:"rejected"`
	Unresolved       int          `json:"unresolved"`
	OfficialVerified int          `json:"official_verified"`
	TokenAllowed     int          `json:"token_allowed_with_exception"`
	TokenMismatch    int          `json:"token_mismatch"`
	TokenMismatchIDs []string     `json:"token_mismatch_ids,omitempty"`
	TokenAdvisory    bool         `json:"token_mismatch_advisory"`
	DiffPass         bool         `json:"diff_pass"`
	EditedMembers    int          `json:"edited_members"`
	PlannedSlots     int          `json:"planned_slots"`
	ChangedSlots     int          `json:"changed_slots"`
	NoOpRecords      int          `json:"no_op_records"`
	Reverts          []string     `json:"no_honorifics_reverts_to_source,omitempty"`
	ArtifactRel      string       `json:"artifact_rel"`
	ArtifactPath     string       `json:"artifact_path"`
	Target           string       `json:"target"`
	PatchComponents  []string     `json:"patch_components"`
	VoiceBuildID     string       `json:"voice_build_id"`
	VoiceStatus      string       `json:"voice_patch_status"`
	VoiceArtifactSHA string       `json:"voice_artifact_sha256,omitempty"`
	VoiceArtifactRel string       `json:"voice_artifact_rel,omitempty"`
	VoiceRuntime     string       `json:"voice_runtime_status,omitempty"`
	OutputSHA256     string       `json:"output_sha256"`
	OutputSize       int64        `json:"output_size"`
	OutputZIP        string       `json:"output_zip,omitempty"`
	Validation       string       `json:"validation_status"`
	Files            []FileResult `json:"files"`
}

// voiceBuildID is UNVERIFIED unless a voice patch was built in this run: the
// planned shared Build-ID-gated Japanese-voice ExeFS patch is experimental and
// not runtime-approved, so no voice Build ID is applied to a normal build.
const voiceBuildID = "UNVERIFIED"

// patchComponents names every component this output carries. The RomFS
// localization replacement is the implemented component; the shared voice
// ExeFS patch is listed only when it was actually built.
func patchComponents(res *Result, er *EditionResult) []string {
	out := []string{"romfs:" + layeredfs.RomFSRel + " (LayeredFS RomFS replacement)"}
	if res.Voice != nil {
		out = append(out, "voice-exefs-patch: "+res.Voice.ArtifactRel+
			" (shared, Build-ID-gated; EXPERIMENTAL "+res.Voice.RuntimeStatus+")")
	} else {
		out = append(out, "voice-exefs-patch: NOT_INCLUDED (experimental, not runtime-approved; use an explicit --voice build)")
	}
	return out
}

// effectiveVoiceBuildID is the Build ID asserted in the report: the pinned id
// of the built voice patch, or UNVERIFIED when no voice patch was requested.
func effectiveVoiceBuildID(res *Result) string {
	if res.Voice != nil {
		return res.Voice.BuildID
	}
	return voiceBuildID
}

func newEditionReport(res *Result, er *EditionResult) editionReport {
	c := res.Resolution.Counts
	d := res.Dataset
	r := editionReport{
		Variant:          string(er.Variant),
		Status:           statusWord(res.DryRun),
		Translations:     res.Translations,
		Run:              res.RunName,
		SourcePath:       paths.Display(res.Facts.Path),
		SourceSHA256:     res.Facts.SHA256,
		SourceSize:       res.Facts.Size,
		SourceMembers:    res.Facts.Members,
		SourceFMSMembers: res.Facts.FMSMembers,
		SourceMemberList: res.Facts.MemberListSHA256,
		TitleID:          res.TitleID,
		DisplayVersion:   res.DisplayVersion,
		Version:          res.Version,
		StartupLabel:     res.StartupLabel,
		SourceMode:       string(res.SourceMode),
		DatasetCanonFile: d.CanonicalFile,
		DatasetCanonSHA:  d.CanonicalSHA256,
		DatasetCanonRows: d.CanonicalRows,
		DatasetAltFile:   d.AlternateFile,
		DatasetAltSHA:    d.AlternateSHA256,
		DatasetAltRows:   d.AlternateRows,
		CanonicalRows:    c.Canonical,
		Overrides:        c.Overrides,
		Applied:          c.Applied,
		Rejected:         c.Rejected,
		Unresolved:       c.Unresolved,
		OfficialVerified: c.OfficialVerified,
		TokenAllowed:     c.TokenAllowed,
		TokenMismatch:    c.TokenMismatch,
		TokenMismatchIDs: res.Plan.TokenMismatch,
		TokenAdvisory:    true,
		DiffPass:         res.Resolution.DiffPass,
		EditedMembers:    len(er.Files),
		PlannedSlots:     er.PlannedSlots,
		ChangedSlots:     er.ChangedSlots,
		NoOpRecords:      res.NoOpRecords,
		Reverts:          res.Reverts,
		ArtifactRel:      er.ArtifactRel,
		ArtifactPath:     paths.Display(er.ArtifactPath),
		Target:           layeredfs.AtmospherePath(res.TitleID, er.ArtifactRel),
		PatchComponents:  patchComponents(res, er),
		VoiceBuildID:     effectiveVoiceBuildID(res),
		VoiceStatus:      "NOT_INCLUDED (experimental, not runtime-approved)",
		OutputSHA256:     er.SHA256,
		OutputSize:       er.Size,
		Validation:       validationWord(res, er),
		Files:            er.Files,
	}
	if res.Voice != nil {
		r.VoiceStatus = "EXPERIMENTAL (" + res.Voice.RuntimeStatus + ")"
		r.VoiceArtifactSHA = res.Voice.ArtifactSHA256
		r.VoiceArtifactRel = res.Voice.ArtifactRel
		r.VoiceRuntime = res.Voice.RuntimeStatus
	}
	return r
}

// validationWord summarises the machine-checked outcome of one edition.
func validationWord(res *Result, er *EditionResult) string {
	if !res.Resolution.DiffPass {
		return "FAIL (diff drift)"
	}
	if res.Resolution.Counts.Rejected > 0 {
		return "FAIL (rejected records)"
	}
	if res.DryRun {
		return "DRY-RUN OK"
	}
	return "PASS"
}

func statusWord(dryRun bool) string {
	if dryRun {
		return "DRY-RUN"
	}
	return "PUBLISHED"
}

func renderEditionJSON(res *Result, er *EditionResult) (string, error) {
	data, err := json.MarshalIndent(newEditionReport(res, er), "", "  ")
	if err != nil {
		return "", fmt.Errorf("build: encoding edition report: %w", err)
	}
	return string(data) + "\n", nil
}

func renderEditionText(res *Result, er *EditionResult) string {
	r := newEditionReport(res, er)
	var b strings.Builder
	fmt.Fprintf(&b, "uo-unburger build report\n")
	fmt.Fprintf(&b, "variant:            %s\n", r.Variant)
	fmt.Fprintf(&b, "status:             %s\n", r.Status)
	fmt.Fprintf(&b, "translations:       %s\n", r.Translations)
	fmt.Fprintf(&b, "run:                %s\n", r.Run)
	fmt.Fprintf(&b, "source:             %s\n", r.SourcePath)
	fmt.Fprintf(&b, "source sha256:      %s\n", r.SourceSHA256)
	fmt.Fprintf(&b, "source size:        %d\n", r.SourceSize)
	fmt.Fprintf(&b, "source members:     %d (fms %d)\n", r.SourceMembers, r.SourceFMSMembers)
	fmt.Fprintf(&b, "source members sha256: %s\n", r.SourceMemberList)
	fmt.Fprintf(&b, "source identity:    %s\n", r.SourceMode)
	fmt.Fprintf(&b, "title id:           %s\n", r.TitleID)
	if r.DisplayVersion != "" {
		fmt.Fprintf(&b, "display version:    %s\n", r.DisplayVersion)
	} else {
		fmt.Fprintf(&b, "display version:    UNASSERTED\n")
	}
	if r.Version != "" {
		fmt.Fprintf(&b, "version:            %s\n", r.Version)
		fmt.Fprintf(&b, "startup label:      %s\n", r.StartupLabel)
	} else {
		fmt.Fprintf(&b, "version:            UNASSIGNED (no root %s)\n", VersionFileName)
	}
	fmt.Fprintf(&b, "dataset:            %s rows=%d sha256=%s\n", r.DatasetCanonFile, r.DatasetCanonRows, r.DatasetCanonSHA)
	if r.DatasetAltFile != "" {
		fmt.Fprintf(&b, "dataset (alt):      %s rows=%d sha256=%s\n", r.DatasetAltFile, r.DatasetAltRows, r.DatasetAltSHA)
	}
	fmt.Fprintf(&b, "canonical rows:     %d\n", r.CanonicalRows)
	fmt.Fprintf(&b, "overrides:          %d\n", r.Overrides)
	fmt.Fprintf(&b, "applied:            %d\n", r.Applied)
	fmt.Fprintf(&b, "rejected:           %d\n", r.Rejected)
	fmt.Fprintf(&b, "unresolved:         %d\n", r.Unresolved)
	fmt.Fprintf(&b, "official en verified: %d\n", r.OfficialVerified)
	fmt.Fprintf(&b, "token flags:        allowed_with_exception=%d token_mismatch=%d (advisory)\n", r.TokenAllowed, r.TokenMismatch)
	fmt.Fprintf(&b, "diff verification:  %s\n", diffWord(r.DiffPass))
	fmt.Fprintf(&b, "edited members:     %d\n", r.EditedMembers)
	fmt.Fprintf(&b, "planned edits:      %d\n", r.PlannedSlots)
	fmt.Fprintf(&b, "changed slots:      %d\n", r.ChangedSlots)
	fmt.Fprintf(&b, "no-op records:      %d\n", r.NoOpRecords)
	if len(r.Reverts) > 0 {
		fmt.Fprintf(&b, "no-honorifics reverts to source En: %d\n", len(r.Reverts))
		for _, id := range r.Reverts {
			fmt.Fprintf(&b, "  %s\n", id)
		}
	}
	fmt.Fprintf(&b, "artifact:           %s\n", r.ArtifactRel)
	fmt.Fprintf(&b, "target:             %s\n", r.Target)
	fmt.Fprintf(&b, "voice build id:     %s\n", r.VoiceBuildID)
	fmt.Fprintf(&b, "voice patch:        %s\n", r.VoiceStatus)
	if r.VoiceArtifactSHA != "" {
		fmt.Fprintf(&b, "voice artifact:     %s sha256=%s (shared)\n", r.VoiceArtifactRel, r.VoiceArtifactSHA)
	}
	fmt.Fprintf(&b, "patch components:\n")
	for _, c := range r.PatchComponents {
		fmt.Fprintf(&b, "  %s\n", c)
	}
	fmt.Fprintf(&b, "output sha256:      %s\n", r.OutputSHA256)
	fmt.Fprintf(&b, "output size:        %d\n", r.OutputSize)
	fmt.Fprintf(&b, "validation:         %s\n", r.Validation)
	if len(r.TokenMismatchIDs) > 0 {
		fmt.Fprintf(&b, "token mismatch ids: %s\n", strings.Join(r.TokenMismatchIDs, " "))
	}
	if len(r.Files) > 0 {
		fmt.Fprintf(&b, "members:\n")
		for _, f := range r.Files {
			fmt.Fprintf(&b, "  %-28s slots=%d stored=%d\n", f.Member, len(f.Slots), f.StoredSize)
		}
	}
	return b.String()
}

func renderSummaryText(res *Result) string {
	c := res.Resolution.Counts
	var b strings.Builder
	fmt.Fprintf(&b, "uo-unburger build %s\n", statusWord(res.DryRun))
	fmt.Fprintf(&b, "translations:       %s\n", res.Translations)
	fmt.Fprintf(&b, "run:                %s\n", res.RunName)
	if !res.DryRun {
		fmt.Fprintf(&b, "output dir:         %s\n", paths.Display(res.RunDir))
	}
	fmt.Fprintf(&b, "source sha256:      %s\n", res.Facts.SHA256)
	fmt.Fprintf(&b, "source size:        %d\n", res.Facts.Size)
	fmt.Fprintf(&b, "source members:     %d (fms %d)\n", res.Facts.Members, res.Facts.FMSMembers)
	fmt.Fprintf(&b, "source identity:    %s\n", res.SourceMode)
	if res.Dataset.CanonicalFile != "" {
		fmt.Fprintf(&b, "dataset:            %s rows=%d sha256=%s\n", res.Dataset.CanonicalFile, res.Dataset.CanonicalRows, res.Dataset.CanonicalSHA256)
		if res.Dataset.AlternatePresent {
			fmt.Fprintf(&b, "dataset (alt):      %s rows=%d sha256=%s\n", res.Dataset.AlternateFile, res.Dataset.AlternateRows, res.Dataset.AlternateSHA256)
		}
	}
	fmt.Fprintf(&b, "canonical rows:     %d\n", c.Canonical)
	fmt.Fprintf(&b, "overrides:          %d\n", c.Overrides)
	fmt.Fprintf(&b, "applied:            %d\n", c.Applied)
	fmt.Fprintf(&b, "rejected:           %d\n", c.Rejected)
	fmt.Fprintf(&b, "unresolved:         %d\n", c.Unresolved)
	fmt.Fprintf(&b, "token flags:        allowed_with_exception=%d token_mismatch=%d\n", c.TokenAllowed, c.TokenMismatch)
	fmt.Fprintf(&b, "diff verification:  %s\n", diffWord(res.Resolution.DiffPass))
	fmt.Fprintf(&b, "no-op records:      %d\n", res.NoOpRecords)
	for _, e := range res.Editions {
		fmt.Fprintf(&b, "edition %-14s sha256=%s size=%d members=%d planned=%d changed=%d\n",
			e.Variant, e.SHA256, e.Size, len(e.Files), e.PlannedSlots, e.ChangedSlots)
	}
	if res.Voice != nil {
		fmt.Fprintf(&b, "voice patch (shared) %s rel=%s sha256=%s edits=%d changed=%d runtime=%s\n",
			res.Voice.Plan, res.Voice.ArtifactRel, res.Voice.ArtifactSHA256,
			res.Voice.Edits, res.Voice.ChangedBytes, res.Voice.RuntimeStatus)
	}
	return b.String()
}

func diffWord(ok bool) string {
	if ok {
		return "PASS"
	}
	return "FAIL"
}
