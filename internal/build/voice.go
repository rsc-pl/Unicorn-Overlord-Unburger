package build

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"uo-unburger/internal/layeredfs"
	"uo-unburger/internal/nso"
	"uo-unburger/internal/paths"
	"uo-unburger/internal/voice"
)

// VoiceResult describes the shared, Build-ID-gated Japanese-voice ExeFS patch
// that a build may carry alongside the two RomFS editions. The component is
// edition-independent: both text editions share exactly one voice artifact.
type VoiceResult struct {
	Plan           string `json:"plan"`
	BuildID        string `json:"build_id"`
	SourcePath     string `json:"source_path"`
	SourceSHA256   string `json:"source_sha256"`
	ArtifactRel    string `json:"artifact_rel"`
	ArtifactSHA256 string `json:"artifact_sha256"`
	ArtifactSize   int64  `json:"artifact_size"`
	IPSSHA256      string `json:"ips_sha256,omitempty"`
	IPSSize        int64  `json:"ips_size,omitempty"`
	IPSLayer       string `json:"ips_layer,omitempty"`
	IPSInstallRel  string `json:"ips_install_rel,omitempty"`
	IPSRecords     int    `json:"ips_records,omitempty"`
	IPSChanged     int    `json:"ips_changed_bytes,omitempty"`
	ChangedBytes   int    `json:"changed_bytes"`
	Edits          int    `json:"edits"`
	RuntimeStatus  string `json:"runtime_status"`
}

// voiceArtifactRel is the LayeredFS ExeFS replacement path inside the voice
// stage directory. It is re-exported from internal/layeredfs.
const voiceArtifactRel = layeredfs.VoiceExeFSRel

// voiceIPSRel is the optional IPS expression of the same edit, staged inside
// the run for the private package record. It is an Atmosphère exefs_patches
// patch (uncompressed-NSO layer); Eden loads the `exefs/main` replacement, not
// this file.
const voiceIPSRel = "main.ips"

// voicePatchSet is the exefs_patches subdirectory name used in reports and in
// the user-facing IPS stage. Atmosphère only requires some directory below
// /atmosphere/exefs_patches/; the name is fixed here so reports cannot drift.
const voicePatchSet = "uo-unburger-voice"

// voiceIPSLayer names the only layer an Atmosphère exefs_patches IPS may be
// built against.
const voiceIPSLayer = "uncompressed-nso (Atmosphère exefs_patches; Eden does not read .ips)"

const (
	// voiceRuntimeConfirmed is the runtime status recorded for the pinned
	// production plan. It is a machine-readable report value; building or
	// applying the patch never installs or launches anything.
	voiceRuntimeConfirmed = "INTEGRATED_VERIFIED"
	// voiceRuntimeMarker is written next to every voice artifact.
	voiceRuntimeMarker = "RUNTIME_STATUS.txt"
)

// voiceRuntimeText is the marker written next to every voice artifact. It
// describes the artifact's build state and the remaining selector limitation.
// The visible English selector is UcOption.fms data plus UI layout; Japanese
// playback is forced regardless of the setting, and selector removal is not
// implemented.
const voiceRuntimeText = `RUNTIME STATUS: INTEGRATED_VERIFIED

This artifact is the shared Japanese-voice ExeFS patch produced by the build.
It rewrites four same-size .text words and is emitted next to the build record;
the toolkit never installs or launches it.

The in-game voice menu still lists its English entry. Japanese playback is
forced regardless of the selected setting; selector removal is not implemented.
`

// buildVoicePatch reads the caller-supplied ExeFS `main` read-only, verifies it
// against the plan's pinned identity and returns the re-encoded NSO plus its IPS
// expression and a report. It never writes anything.
func buildVoicePatch(mainPath string, plan voice.Plan) (patched, ips []byte, vr *VoiceResult, err error) {
	if strings.TrimSpace(mainPath) == "" {
		return nil, nil, nil, fmt.Errorf("build: the voice patch needs an extracted ExeFS main (NSO0)")
	}
	src, err := os.ReadFile(mainPath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("build: reading voice ExeFS main %s: %s", paths.Display(mainPath), paths.Error(err))
	}
	if err := voice.VerifyIdentity(src, plan.Target); err != nil {
		return nil, nil, nil, fmt.Errorf("build: %w", err)
	}
	patched, rep, err := voice.Apply(src, plan)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("build: %w", err)
	}
	if rep.AlreadyApplied {
		return nil, nil, nil, fmt.Errorf("build: the supplied ExeFS main already carries the voice plan; refusing to re-patch")
	}
	// The IPS is for Atmosphère's exefs_patches loader, which patches the
	// *mapped module*: decompressed segments at their memory offsets. Diffing
	// the stored (LZ4-compressed) images instead would restate the whole
	// re-encoded .rodata stream (~6.5 MB) and could never be applied safely.
	patch, err := voice.BuildExefsIPS(src, patched)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("build: %w", err)
	}
	ips = patch.Bytes
	// Self-check with the loader's own semantics: offsets are uncompressed-NSO
	// file offsets, the 0x100-byte header is protected, writes land in the
	// mapped image. The result must be exactly the patched module image.
	want, err := mappedImage(patched)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("build: IPS self-check: %w", err)
	}
	back, err := voice.ApplyExefsIPS(src, ips)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("build: IPS self-check: %w", err)
	}
	if !bytes.Equal(back, want) {
		return nil, nil, nil, fmt.Errorf("build: IPS self-check: the patch does not reproduce the patched mapped image")
	}
	sum := sha256.Sum256(ips)
	vr = &VoiceResult{
		Plan:           rep.Plan,
		BuildID:        rep.ModuleID,
		SourcePath:     paths.Display(mainPath),
		SourceSHA256:   rep.SourceSHA256,
		ArtifactRel:    voiceArtifactRel,
		ArtifactSHA256: rep.OutputSHA256,
		ArtifactSize:   rep.OutputSize,
		IPSSHA256:      hex.EncodeToString(sum[:]),
		IPSSize:        int64(len(ips)),
		IPSLayer:       voiceIPSLayer,
		IPSInstallRel:  layeredfs.ExefsPatchPath(voicePatchSet, rep.ModuleID),
		IPSRecords:     patch.Records,
		IPSChanged:     patch.Changed,
		ChangedBytes:   rep.ChangedBytes,
		Edits:          len(rep.Edits),
		RuntimeStatus:  voiceRuntimeConfirmed,
	}
	return patched, ips, vr, nil
}

// mappedImage returns the loader-visible module image of a stored NSO.
func mappedImage(stored []byte) ([]byte, error) {
	f, err := nso.Parse(stored)
	if err != nil {
		return nil, err
	}
	return f.MappedImage()
}

// renderVoiceText renders the voice artifact's human report.
func renderVoiceText(vr *VoiceResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "uo-unburger voice patch report\n")
	fmt.Fprintf(&b, "status:             %s\n", vr.RuntimeStatus)
	fmt.Fprintf(&b, "plan:               %s (%d edits)\n", vr.Plan, vr.Edits)
	fmt.Fprintf(&b, "build id (gate):    %s\n", vr.BuildID)
	fmt.Fprintf(&b, "source:             %s\n", vr.SourcePath)
	fmt.Fprintf(&b, "source sha256:      %s\n", vr.SourceSHA256)
	fmt.Fprintf(&b, "artifact:           %s\n", vr.ArtifactRel)
	fmt.Fprintf(&b, "artifact sha256:    %s\n", vr.ArtifactSHA256)
	fmt.Fprintf(&b, "artifact size:      %d\n", vr.ArtifactSize)
	fmt.Fprintf(&b, "changed bytes:      %d\n", vr.ChangedBytes)
	if vr.IPSSHA256 != "" {
		fmt.Fprintf(&b, "ips:                %s (%d bytes, %d records, %d payload bytes, sha256 %s)\n",
			voiceIPSRel, vr.IPSSize, vr.IPSRecords, vr.IPSChanged, vr.IPSSHA256)
		fmt.Fprintf(&b, "ips layer:          %s\n", vr.IPSLayer)
		fmt.Fprintf(&b, "ips install path:   %s\n", vr.IPSInstallRel)
	}
	fmt.Fprintf(&b, "placement:          %s\n", layeredfs.AtmospherePath("<title-id>", voiceArtifactRel))
	fmt.Fprintf(&b, "runtime (integrated): %s\n", vr.RuntimeStatus)
	fmt.Fprintf(&b, "voice menu:         English entry still listed (Japanese forced regardless); selector removal not implemented\n")
	return b.String()
}

// voiceStageDir is the shared voice stage directory inside a run.
func voiceStageDir(runDir string) string { return filepath.Join(runDir, "voice") }
