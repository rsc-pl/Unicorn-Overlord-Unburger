// Package voice builds the shared, Build-ID-gated Japanese-voice ExeFS patch.
//
// The Western build of Unicorn Overlord ships both the Japanese and the US
// voice-asset families in its executable and selects one of them at runtime.
// The two families are registered by ten R_AARCH64_RELATIVE relocation
// descriptors in .rodata (stride 0x18, {u64 addend, u64 target, u64 info}); the
// loader copies each addend (the asset-name VA) into the .data pointer slot
// named by target. The five "US" descriptors are the only byte-level lever:
// repointing them at the five "JP" asset names makes the executable load the
// Japanese family whichever block the runtime selects.
//
// This package is deliberately stdlib-only and free of any real game file: it
// validates the caller-supplied bytes against a pinned identity (whole-file
// SHA-256, exact size and NSO module id / Build ID) and refuses anything else.
// All tests use synthetic NSO fixtures.
//
// SCOPE AND SAFETY
//
//   - The patch is a same-size overwrite of five 8-byte addend fields in the
//     decompressed .rodata image; every other byte is preserved. Re-encoding is
//     delegated to internal/nso, which is byte-stable for untouched segments.
//   - The patch is version-specific: it is only ever applied to the Western
//     v1.05 update ExeFS `main` recorded in WesternUpdate. Any other Build ID is
//     rejected before a single byte is read.
//   - The runtime premise is UNPROVEN. Statically the voice pointer array is
//     filled by the loader and has no direct code reference, and neither the
//     boot default nor a persistent voice-language key was found. A byte-level
//     repoint is therefore a hypothesis that only an in-game acceptance run can
//     confirm. Apply and BuildIPS never install or run anything.
package voice

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"

	"uo-unburger/internal/nso"
)

// Identity pins one exact ExeFS `main` image. Every field is mandatory: an
// image that does not match all three is rejected before any edit.
type Identity struct {
	// ModuleID is the NSO module id (Build ID), 64 lowercase hex characters.
	ModuleID string
	// SHA256 is the whole-file SHA-256, 64 lowercase hex characters.
	SHA256 string
	// Size is the exact stored file size in bytes.
	Size int64
}

// WesternUpdate is the Western v1.05 update ExeFS `main` (NSO0) this project has
// evidence for. It is the only supported patch target.
var WesternUpdate = Identity{
	ModuleID: "c841ffe2717ff03a13990480c51da73f091c04fa000000000000000000000000",
	SHA256:   "69bf80e93ca30b8d474b1b969c558fac3913d78172958c4db27d7726db44236b",
	Size:     11663331,
}

// Edit is a same-size overwrite at a decompressed virtual address inside one
// NSO segment. From is asserted byte-for-byte before To is written; the two
// must have equal length.
type Edit struct {
	Segment nso.Name
	// VA is the virtual address of the field inside the decompressed segment
	// (its memory offset plus the in-segment offset).
	VA   uint32
	From []byte
	To   []byte
	// Desc is a human description used in reports.
	Desc string
}

// Plan is a named, gated, ordered edit set.
type Plan struct {
	Name   string
	Target Identity
	Edits  []Edit
}

func le64(v uint64) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, v)
	return b
}

// jpVoiceRepoints are the five US voice-asset registry addends (decompressed
// .rodata file-VAs) and their JP replacements. The addend is a u64 little-endian
// pointer into .rodata; only its low four bytes differ.
var jpVoiceRepoints = []struct {
	va   uint32
	from uint64
	to   uint64
	desc string
}{
	{0x84F768, 0xAC8010, 0xAE4B9F, "SOUND/VOICE_US.ACB -> SOUND/VOICE_JP.ACB"},
	{0x84F780, 0xAA77E8, 0xABCDC8, "SOUND/VOICE_US.AWB -> SOUND/VOICE_JP.AWB"},
	{0x84F798, 0xAF7489, 0xAAEC42, "SOUND/VOICE_BTL_US.ACB -> SOUND/VOICE_BTL_JP.ACB"},
	{0x84F7B0, 0xAE86F9, 0xAE125E, "SOUND/VOICE_BTL_US.AWB -> SOUND/VOICE_BTL_JP.AWB"},
	{0x84F7C8, 0xACB9BC, 0xAC7FF9, "SOUND/VOICE_ADD_US.ACB -> SOUND/VOICE_ADD_JP.ACB"},
}

// RegistryRepoints returns the JP-enforcement plan for the Western update
// `main`: repoint the five US voice-asset registry entries at the five JP ones.
func RegistryRepoints() Plan {
	p := Plan{Name: "jp-voice-registry", Target: WesternUpdate}
	for _, r := range jpVoiceRepoints {
		p.Edits = append(p.Edits, Edit{
			Segment: nso.Rodata,
			VA:      r.va,
			From:    le64(r.from),
			To:      le64(r.to),
			Desc:    r.desc,
		})
	}
	return p
}

// EditResult records one applied (or already-present) edit for the report.
type EditResult struct {
	Segment nso.Name `json:"segment"`
	VA      uint32   `json:"va"`
	Old     string   `json:"old"`
	New     string   `json:"new"`
	Desc    string   `json:"desc,omitempty"`
}

// Report describes the outcome of Apply.
type Report struct {
	Plan           string       `json:"plan"`
	ModuleID       string       `json:"module_id"`
	SourceSHA256   string       `json:"source_sha256"`
	OutputSHA256   string       `json:"output_sha256"`
	OutputSize     int64        `json:"output_size"`
	ChangedBytes   int          `json:"changed_bytes"`
	Edits          []EditResult `json:"edits"`
	AlreadyApplied bool         `json:"already_applied"`
	RuntimeStatus  string       `json:"runtime_status"`
}

// VerifyIdentity checks src against id. It hashes the whole file and compares
// size, SHA-256 and (when src is a valid NSO0) the module id.
func VerifyIdentity(src []byte, id Identity) error {
	if int64(len(src)) != id.Size {
		return fmt.Errorf("voice: image size %d does not match the pinned %d", len(src), id.Size)
	}
	sum := sha256.Sum256(src)
	if got := hex.EncodeToString(sum[:]); !strings.EqualFold(got, id.SHA256) {
		return fmt.Errorf("voice: image SHA-256 %s does not match the pinned %s", got, id.SHA256)
	}
	f, err := nso.Parse(src)
	if err != nil {
		return fmt.Errorf("voice: image is not an NSO0: %w", err)
	}
	return checkModuleID(f, id)
}

func checkModuleID(f *nso.File, id Identity) error {
	if got := hex.EncodeToString(f.ModuleID[:]); !strings.EqualFold(got, id.ModuleID) {
		return fmt.Errorf("voice: module id (Build ID) %s does not match the pinned %s", got, id.ModuleID)
	}
	return nil
}

// Apply verifies src against plan.Target and returns a re-encoded NSO image with
// every edit applied. It is idempotent: an image in which every edit is already
// present (and whose module id matches) is returned unchanged.
//
// Every edit's From bytes are asserted before anything is written; a mismatch,
// an out-of-bounds address, a wrong module id, a wrong SHA-256 or a wrong size
// aborts with an error and no partial result.
func Apply(src []byte, plan Plan) ([]byte, *Report, error) {
	f, err := nso.Parse(src)
	if err != nil {
		return nil, nil, fmt.Errorf("voice: %w", err)
	}
	if err := checkModuleID(f, plan.Target); err != nil {
		return nil, nil, err
	}

	// Decompress every segment any edit touches, once.
	blobs := map[nso.Name][]byte{}
	load := func(n nso.Name) ([]byte, error) {
		if b, ok := blobs[n]; ok {
			return b, nil
		}
		b, err := f.Decompress(n)
		if err != nil {
			return nil, fmt.Errorf("voice: decompressing %s: %w", n, err)
		}
		blobs[n] = b
		return b, nil
	}
	for _, ed := range plan.Edits {
		if len(ed.From) != len(ed.To) || len(ed.From) == 0 {
			return nil, nil, fmt.Errorf("voice: edit %s@%#x is not a same-size overwrite", ed.Segment, ed.VA)
		}
		if _, err := load(ed.Segment); err != nil {
			return nil, nil, err
		}
	}

	report := &Report{
		Plan:          plan.Name,
		ModuleID:      hex.EncodeToString(f.ModuleID[:]),
		SourceSHA256:  "",
		RuntimeStatus: "NOT_RUNTIME_APPROVED",
	}

	// Classify the current state of every edit.
	already, missing := 0, 0
	for _, ed := range plan.Edits {
		blob := blobs[ed.Segment]
		off, err := offsetOf(ed, len(blob), f.Segment(ed.Segment).MemoryOffset)
		if err != nil {
			return nil, nil, err
		}
		cur := blob[off : off+len(ed.From)]
		switch {
		case bytes.Equal(cur, ed.To):
			already++
		case bytes.Equal(cur, ed.From):
			missing++
		default:
			return nil, nil, fmt.Errorf("voice: edit %s@%#x original-byte assertion failed: found %x, expected %x",
				ed.Segment, ed.VA, cur, ed.From)
		}
	}
	if already == len(plan.Edits) {
		if err := f.Verify(); err != nil {
			return nil, nil, fmt.Errorf("voice: already-applied image: %w", err)
		}
		sum := sha256.Sum256(src)
		report.SourceSHA256 = hex.EncodeToString(sum[:])
		report.OutputSHA256 = report.SourceSHA256
		report.OutputSize = int64(len(src))
		report.AlreadyApplied = true
		for _, ed := range plan.Edits {
			report.Edits = append(report.Edits, EditResult{ed.Segment, ed.VA,
				hex.EncodeToString(ed.From), hex.EncodeToString(ed.To), ed.Desc})
		}
		return append([]byte(nil), src...), report, nil
	}
	if missing != len(plan.Edits) {
		return nil, nil, fmt.Errorf("voice: image partially carries the plan (%d of %d edits present); refusing an ambiguous patch",
			already, len(plan.Edits))
	}

	// Not yet applied: the image must be exactly the pinned original.
	if err := VerifyIdentity(src, plan.Target); err != nil {
		return nil, nil, err
	}
	if err := f.Verify(); err != nil {
		return nil, nil, fmt.Errorf("voice: source segment verification: %w", err)
	}
	sum := sha256.Sum256(src)
	report.SourceSHA256 = hex.EncodeToString(sum[:])

	// Build patched segments.
	patched := map[nso.Name][]byte{}
	ranges := map[nso.Name][][2]int{}
	for _, ed := range plan.Edits {
		blob := blobs[ed.Segment]
		off, err := offsetOf(ed, len(blob), f.Segment(ed.Segment).MemoryOffset)
		if err != nil {
			return nil, nil, err
		}
		if _, ok := patched[ed.Segment]; !ok {
			patched[ed.Segment] = append([]byte(nil), blob...)
		}
		copy(patched[ed.Segment][off:], ed.To)
		ranges[ed.Segment] = append(ranges[ed.Segment], [2]int{off, len(ed.From)})
		report.Edits = append(report.Edits, EditResult{ed.Segment, ed.VA,
			hex.EncodeToString(ed.From), hex.EncodeToString(ed.To), ed.Desc})
	}

	out, err := f.Encode(patched)
	if err != nil {
		return nil, nil, fmt.Errorf("voice: re-encoding NSO: %w", err)
	}

	// Independent re-parse and validation of the produced image.
	g, err := nso.Parse(out)
	if err != nil {
		return nil, nil, fmt.Errorf("voice: re-parsing the patched image: %w", err)
	}
	if err := g.Verify(); err != nil {
		return nil, nil, fmt.Errorf("voice: patched image segment verification: %w", err)
	}
	for _, n := range []nso.Name{nso.Text, nso.Rodata, nso.Data} {
		before, err := f.Decompress(n)
		if err != nil {
			return nil, nil, err
		}
		after, err := g.Decompress(n)
		if err != nil {
			return nil, nil, err
		}
		if len(before) != len(after) {
			return nil, nil, fmt.Errorf("voice: %s decompressed size changed (%d -> %d)", n, len(before), len(after))
		}
		changed, err := changedOutside(before, after, ranges[n])
		if err != nil {
			return nil, nil, err
		}
		report.ChangedBytes += changed
	}
	// Untouched stored payloads must be byte-identical to the source.
	for _, n := range []nso.Name{nso.Text, nso.Data} {
		a, _ := f.StoredSegment(n)
		b, _ := g.StoredSegment(n)
		if !bytes.Equal(a, b) {
			return nil, nil, fmt.Errorf("voice: untouched %s stored payload changed", n)
		}
	}

	sumOut := sha256.Sum256(out)
	report.OutputSHA256 = hex.EncodeToString(sumOut[:])
	report.OutputSize = int64(len(out))
	return out, report, nil
}

func offsetOf(ed Edit, blobLen int, memBase uint32) (int, error) {
	if ed.VA < memBase {
		return 0, fmt.Errorf("voice: edit %s@%#x is below the segment base %#x", ed.Segment, ed.VA, memBase)
	}
	off := int(ed.VA - memBase)
	if off+len(ed.From) > blobLen {
		return 0, fmt.Errorf("voice: edit %s@%#x is outside the segment (%d bytes)", ed.Segment, ed.VA, blobLen)
	}
	return off, nil
}

// changedOutside counts differing bytes and returns an error if any of them
// falls outside the planned ranges.
func changedOutside(before, after []byte, ranges [][2]int) (int, error) {
	changed := 0
	for i := range before {
		if before[i] == after[i] {
			continue
		}
		changed++
		inside := false
		for _, r := range ranges {
			if i >= r[0] && i < r[0]+r[1] {
				inside = true
				break
			}
		}
		if !inside {
			return changed, fmt.Errorf("voice: byte %d changed outside every planned range", i)
		}
	}
	return changed, nil
}
