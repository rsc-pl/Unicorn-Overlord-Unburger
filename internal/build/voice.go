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
	ChangedBytes   int    `json:"changed_bytes"`
	Edits          int    `json:"edits"`
	RuntimeStatus  string `json:"runtime_status"`
}

// voiceArtifactRel is the LayeredFS ExeFS replacement path inside the voice
// stage directory. It is re-exported from internal/layeredfs.
const voiceArtifactRel = layeredfs.VoiceExeFSRel

// voiceIPSRel is the optional IPS expression of the same edit.
const voiceIPSRel = "main.ips"

// voiceNotRuntimeMarker is written next to every experimental voice artifact.
const voiceNotRuntimeMarker = `NOT_RUNTIME_APPROVED

This voice ExeFS patch is experimental and static. It has NOT been installed and
has NOT been run on any console or emulator. The premise that the Western build
selects the US voice registry block at boot, and that repointing it at the
Japanese assets makes Japanese voices play (and English voices unreachable), is
UNPROVEN. Do not ship, distribute or install this artifact before an in-game
acceptance run.
`

// buildVoicePatch reads the caller-supplied ExeFS `main` read-only, verifies it
// against the plan's pinned identity and returns the re-encoded NSO plus its IPS
// expression and a report. It never writes anything.
func buildVoicePatch(mainPath string, plan voice.Plan) (patched, ips []byte, vr *VoiceResult, err error) {
	if strings.TrimSpace(mainPath) == "" {
		return nil, nil, nil, fmt.Errorf("build: the voice patch needs the extracted ExeFS main (--voice-main)")
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
	ips, err = voice.BuildIPS(src, patched)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("build: %w", err)
	}
	back, err := voice.ApplyIPS(src, ips)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("build: IPS self-check: %w", err)
	}
	if !bytes.Equal(back, patched) {
		return nil, nil, nil, fmt.Errorf("build: IPS self-check: the patch does not reproduce the patched image")
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
		ChangedBytes:   rep.ChangedBytes,
		Edits:          len(rep.Edits),
		RuntimeStatus:  rep.RuntimeStatus,
	}
	return patched, ips, vr, nil
}

// renderVoiceText renders the experimental voice artifact's human report.
func renderVoiceText(vr *VoiceResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "uo-unburger voice patch report\n")
	fmt.Fprintf(&b, "status:             EXPERIMENTAL (%s)\n", vr.RuntimeStatus)
	fmt.Fprintf(&b, "plan:               %s (%d edits)\n", vr.Plan, vr.Edits)
	fmt.Fprintf(&b, "build id (gate):    %s\n", vr.BuildID)
	fmt.Fprintf(&b, "source:             %s\n", vr.SourcePath)
	fmt.Fprintf(&b, "source sha256:      %s\n", vr.SourceSHA256)
	fmt.Fprintf(&b, "artifact:           %s\n", vr.ArtifactRel)
	fmt.Fprintf(&b, "artifact sha256:    %s\n", vr.ArtifactSHA256)
	fmt.Fprintf(&b, "artifact size:      %d\n", vr.ArtifactSize)
	fmt.Fprintf(&b, "changed bytes:      %d\n", vr.ChangedBytes)
	if vr.IPSSHA256 != "" {
		fmt.Fprintf(&b, "ips:                %s (%d bytes, sha256 %s)\n", voiceIPSRel, vr.IPSSize, vr.IPSSHA256)
	}
	fmt.Fprintf(&b, "placement:          %s\n", layeredfs.AtmospherePath("<title-id>", voiceArtifactRel))
	fmt.Fprintf(&b, "runtime:            %s (in-game acceptance not performed)\n", vr.RuntimeStatus)
	return b.String()
}

// voiceStageDir is the shared voice stage directory inside a run.
func voiceStageDir(runDir string) string { return filepath.Join(runDir, "voice") }
