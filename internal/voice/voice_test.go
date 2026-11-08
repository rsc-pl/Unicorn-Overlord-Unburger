package voice

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"testing"

	"uo-unburger/internal/nso"
)

// synthNSO builds a self-contained, format-exact NSO0 image with three
// LZ4-compressed segments. It mirrors the header layout internal/nso parses.
func synthNSO(t *testing.T, rodata []byte, moduleID [32]byte, compressed bool) []byte {
	t.Helper()
	const headerSize = 0x100
	text := append([]byte("TEXT-CODE-"), bytes.Repeat([]byte{0x01, 0x02, 0x03, 0x04}, 32)...)
	data := bytes.Repeat([]byte{0xDE, 0xAD}, 64)
	blobs := map[nso.Name][]byte{nso.Text: text, nso.Rodata: rodata, nso.Data: data}

	payload := map[nso.Name][]byte{}
	for n, b := range blobs {
		if compressed {
			payload[n] = nso.Compress(b)
		} else {
			payload[n] = append([]byte(nil), b...)
		}
	}
	hdr := make([]byte, headerSize)
	copy(hdr[:4], nso.Magic)
	flags := uint32(0)
	if compressed {
		flags = nso.FlagCompressed
	}
	binary.LittleEndian.PutUint32(hdr[0xC:], flags)
	copy(hdr[0x40:0x60], moduleID[:])

	mem := map[nso.Name]uint32{nso.Text: 0, nso.Rodata: 0x1000, nso.Data: 0x2000}
	type field struct{ fileOff, memOff, decomp, memsz, csize, hash int }
	fields := map[nso.Name]field{
		nso.Text:   {0x10, 0x14, 0x18, 0x1C, 0x60, 0xA0},
		nso.Rodata: {0x20, 0x24, 0x28, 0x2C, 0x64, 0xC0},
		nso.Data:   {0x30, 0x34, 0x38, 0x3C, 0x68, 0xE0},
	}
	off := uint32(headerSize)
	out := append([]byte(nil), hdr...)
	for _, n := range []nso.Name{nso.Text, nso.Rodata, nso.Data} {
		f := fields[n]
		p := payload[n]
		binary.LittleEndian.PutUint32(out[f.fileOff:], off)
		binary.LittleEndian.PutUint32(out[f.memOff:], mem[n])
		binary.LittleEndian.PutUint32(out[f.decomp:], uint32(len(blobs[n])))
		binary.LittleEndian.PutUint32(out[f.memsz:], uint32(len(blobs[n])))
		binary.LittleEndian.PutUint32(out[f.csize:], uint32(len(p)))
		sum := sha256.Sum256(blobs[n])
		copy(out[f.hash:f.hash+32], sum[:])
		out = append(out, p...)
		off += uint32(len(p))
	}
	return out
}

// fixturePlan returns a synthetic two-edit plan and the image it belongs to.
func fixturePlan(t *testing.T) ([]byte, Plan) {
	t.Helper()
	rodata := make([]byte, 0x100)
	copy(rodata[0x40:], le64(0xAABBCCDD))
	copy(rodata[0x80:], le64(0x11223344))
	var moduleID [32]byte
	for i := range moduleID {
		moduleID[i] = byte(0xA0 + i)
	}
	img := synthNSO(t, rodata, moduleID, true)
	sum := sha256.Sum256(img)
	plan := Plan{
		Name: "test-plan",
		Target: Identity{
			ModuleID: hex.EncodeToString(moduleID[:]),
			SHA256:   hex.EncodeToString(sum[:]),
			Size:     int64(len(img)),
		},
		Edits: []Edit{
			{Segment: nso.Rodata, VA: 0x1040, From: le64(0xAABBCCDD), To: le64(0x00AABBCC), Desc: "first"},
			{Segment: nso.Rodata, VA: 0x1080, From: le64(0x11223344), To: le64(0x55667788), Desc: "second"},
		},
	}
	return img, plan
}

func TestApplyPatchesAndValidates(t *testing.T) {
	img, plan := fixturePlan(t)
	out, rep, err := Apply(img, plan)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if rep.AlreadyApplied {
		t.Fatal("fresh image reported as already applied")
	}
	if rep.ChangedBytes != 8 {
		t.Fatalf("ChangedBytes = %d, want 8", rep.ChangedBytes)
	}
	if len(rep.Edits) != 2 {
		t.Fatalf("edits = %d, want 2", len(rep.Edits))
	}
	if rep.RuntimeStatus != "INTEGRATED_VERIFIED" {
		t.Fatalf("RuntimeStatus = %q", rep.RuntimeStatus)
	}
	if rep.ModuleID != plan.Target.ModuleID {
		t.Fatalf("module id changed: %s", rep.ModuleID)
	}

	f, err := nso.Parse(out)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if err := f.Verify(); err != nil {
		t.Fatalf("re-verify: %v", err)
	}
	if hex.EncodeToString(f.ModuleID[:]) != plan.Target.ModuleID {
		t.Fatal("module id not preserved")
	}
	// The two patched fields carry the new values.
	ro, err := f.Decompress(nso.Rodata)
	if err != nil {
		t.Fatal(err)
	}
	if got := binary.LittleEndian.Uint64(ro[0x40:]); got != 0x00AABBCC {
		t.Fatalf("field 0 = %#x", got)
	}
	if got := binary.LittleEndian.Uint64(ro[0x80:]); got != 0x55667788 {
		t.Fatalf("field 1 = %#x", got)
	}
	// Untouched segments stay identical.
	src, _ := nso.Parse(img)
	for _, n := range []nso.Name{nso.Text, nso.Data} {
		a, _ := src.Decompress(n)
		b, _ := f.Decompress(n)
		if !bytes.Equal(a, b) {
			t.Fatalf("%s changed", n)
		}
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	img, plan := fixturePlan(t)
	first, _, err := Apply(img, plan)
	if err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	second, rep, err := Apply(first, plan)
	if err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	if !rep.AlreadyApplied {
		t.Fatal("second Apply did not report already-applied")
	}
	if !bytes.Equal(first, second) {
		t.Fatal("second Apply changed the image")
	}
}

func TestApplyRejectsCorruptOriginal(t *testing.T) {
	img, plan := fixturePlan(t)
	// Flip one byte of the first field without re-pinning the identity.
	f, _ := nso.Parse(img)
	ro, _ := f.Decompress(nso.Rodata)
	ro[0x40] ^= 0xFF
	bad := synthNSO(t, ro, f.ModuleID, true)
	_, _, err := Apply(bad, plan)
	if err == nil || !strings.Contains(err.Error(), "original-byte assertion") {
		t.Fatalf("want original-byte assertion failure, got %v", err)
	}
}

func TestApplyRejectsWrongModuleID(t *testing.T) {
	img, plan := fixturePlan(t)
	plan.Target.ModuleID = strings.Repeat("00", 32)
	_, _, err := Apply(img, plan)
	if err == nil || !strings.Contains(err.Error(), "module id") {
		t.Fatalf("want module id rejection, got %v", err)
	}
}

func TestApplyRejectsWrongSHA(t *testing.T) {
	img, plan := fixturePlan(t)
	plan.Target.SHA256 = strings.Repeat("11", 32)
	_, _, err := Apply(img, plan)
	if err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("want SHA-256 rejection, got %v", err)
	}
}

func TestApplyRejectsWrongSize(t *testing.T) {
	img, plan := fixturePlan(t)
	plan.Target.Size = int64(len(img)) + 1
	_, _, err := Apply(img, plan)
	if err == nil || !strings.Contains(err.Error(), "size") {
		t.Fatalf("want size rejection, got %v", err)
	}
}

func TestApplyRejectsOutOfBounds(t *testing.T) {
	img, plan := fixturePlan(t)
	plan.Edits[0].VA = 0x4000 // beyond the 0x100-byte rodata segment
	_, _, err := Apply(img, plan)
	if err == nil || !strings.Contains(err.Error(), "outside the segment") {
		t.Fatalf("want bounds rejection, got %v", err)
	}
}

func TestApplyRejectsPartialState(t *testing.T) {
	img, plan := fixturePlan(t)
	// Pre-apply only the second edit.
	f, _ := nso.Parse(img)
	ro, _ := f.Decompress(nso.Rodata)
	copy(ro[0x80:], plan.Edits[1].To)
	partial := synthNSO(t, ro, f.ModuleID, true)
	plan.Target.Size = int64(len(partial))
	sum := sha256.Sum256(partial)
	plan.Target.SHA256 = hex.EncodeToString(sum[:])
	_, _, err := Apply(partial, plan)
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("want ambiguous-state rejection, got %v", err)
	}
}

func TestVerifyIdentity(t *testing.T) {
	img, plan := fixturePlan(t)
	if err := VerifyIdentity(img, plan.Target); err != nil {
		t.Fatalf("VerifyIdentity: %v", err)
	}
	bad := append([]byte(nil), img...)
	bad[len(bad)-1] ^= 1
	if err := VerifyIdentity(bad, plan.Target); err == nil {
		t.Fatal("VerifyIdentity accepted a modified image")
	}
}

// movWzr reports whether word is the AArch64 encoding of "mov w<rd>, wzr".
func movWzr(word uint32, rd uint32) bool {
	return word == 0x2A1F03E0|rd
}

// synthNSOWithText builds a format-exact compressed NSO whose .text blob is
// caller-supplied. The production plan's .text addresses must be present, so
// the real plan is tested against a synthetic image with the same layout.
func synthNSOWithText(t *testing.T, text, rodata []byte, moduleID [32]byte) []byte {
	t.Helper()
	const headerSize = 0x100
	data := bytes.Repeat([]byte{0xDE, 0xAD}, 64)
	blobs := map[nso.Name][]byte{nso.Text: text, nso.Rodata: rodata, nso.Data: data}
	payload := map[nso.Name][]byte{}
	for n, b := range blobs {
		payload[n] = nso.Compress(b)
	}
	hdr := make([]byte, headerSize)
	copy(hdr[:4], nso.Magic)
	binary.LittleEndian.PutUint32(hdr[0xC:], nso.FlagCompressed)
	copy(hdr[0x40:0x60], moduleID[:])
	mem := map[nso.Name]uint32{nso.Text: 0, nso.Rodata: 0x1000, nso.Data: 0x2000}
	type field struct{ fileOff, memOff, decomp, memsz, csize, hash int }
	fields := map[nso.Name]field{
		nso.Text:   {0x10, 0x14, 0x18, 0x1C, 0x60, 0xA0},
		nso.Rodata: {0x20, 0x24, 0x28, 0x2C, 0x64, 0xC0},
		nso.Data:   {0x30, 0x34, 0x38, 0x3C, 0x68, 0xE0},
	}
	off := uint32(headerSize)
	out := append([]byte(nil), hdr...)
	for _, n := range []nso.Name{nso.Text, nso.Rodata, nso.Data} {
		f := fields[n]
		p := payload[n]
		binary.LittleEndian.PutUint32(out[f.fileOff:], off)
		binary.LittleEndian.PutUint32(out[f.memOff:], mem[n])
		binary.LittleEndian.PutUint32(out[f.decomp:], uint32(len(blobs[n])))
		binary.LittleEndian.PutUint32(out[f.memsz:], uint32(len(blobs[n])))
		binary.LittleEndian.PutUint32(out[f.csize:], uint32(len(p)))
		sum := sha256.Sum256(blobs[n])
		copy(out[f.hash:f.hash+32], sum[:])
		out = append(out, p...)
		off += uint32(len(p))
	}
	return out
}

func TestJPConfigV5Shape(t *testing.T) {
	p := JPConfigV5()
	if p.Name != "jp-voice-config-v5" {
		t.Fatalf("plan name = %q", p.Name)
	}
	if p.Target != WesternUpdate {
		t.Fatalf("plan target = %+v", p.Target)
	}
	if len(p.Edits) != 4 {
		t.Fatalf("edits = %d, want 4", len(p.Edits))
	}
	want := []struct {
		va       uint32
		from, to uint32
		toReg    uint32
	}{
		{0x1A370, 0x1E380008, 0x2A1F03E8, 8}, // init loader: fcvtzs w8 -> mov w8,wzr
		{0x1C884, 0x1E380008, 0x2A1F03E8, 8}, // reload loader: fcvtzs w8 -> mov w8,wzr
		{0x1C970, 0x1E380000, 0x2A1F03E0, 0}, // index getter: fcvtzs w0 -> mov w0,wzr
		{0x1CC34, 0x1E380008, 0x2A1F03E8, 8}, // offset getter: fcvtzs w8 -> mov w8,wzr
	}
	for i, ed := range p.Edits {
		w := want[i]
		if ed.Segment != nso.Text {
			t.Fatalf("edit %d segment = %s, want text", i, ed.Segment)
		}
		if ed.VA != w.va {
			t.Fatalf("edit %d VA = %#x, want %#x", i, ed.VA, w.va)
		}
		if len(ed.From) != 4 || len(ed.To) != 4 {
			t.Fatalf("edit %d is not a 4-byte word", i)
		}
		if got := binary.LittleEndian.Uint32(ed.From); got != w.from {
			t.Fatalf("edit %d from = %#x, want %#x", i, got, w.from)
		}
		if got := binary.LittleEndian.Uint32(ed.To); got != w.to {
			t.Fatalf("edit %d to = %#x, want %#x", i, got, w.to)
		}
		if !movWzr(binary.LittleEndian.Uint32(ed.To), w.toReg) {
			t.Fatalf("edit %d target %#x is not mov w%d,wzr", i, w.to, w.toReg)
		}
		if i > 0 && p.Edits[i-1].VA >= ed.VA {
			t.Fatalf("edits are not in ascending VA order at %d", i)
		}
	}
	// The plan must not carry the two language-tag ubfx edits: bit 6 of the
	// tag word is never set, so both loaders already observe tag 0.
	for _, ed := range p.Edits {
		if ed.VA == 0x1A360 || ed.VA == 0x1C874 {
			t.Fatalf("plan unexpectedly carries a language-tag edit at %#x", ed.VA)
		}
	}
}

// TestJPConfigV5ForcesJapaneseVoiceState proves the consumer-side invariant:
// the derived window, cache index and cue offset are zero for every possible
// engine float, including the 1.0 that the settings setter writes back.
func TestJPConfigV5ForcesJapaneseVoiceState(t *testing.T) {
	plan := JPConfigV5()
	const textLen = 0x1CD40
	text := bytes.Repeat([]byte{0x1F, 0x20, 0x03, 0xD5}, textLen/4) // nop
	for _, ed := range plan.Edits {
		copy(text[ed.VA:], ed.From)
	}
	// Only the four plan words are staged; the tag sources, the literal float
	// address setup and the cue-offset conditional are not otherwise present in
	// the synthetic buffer and are outside the plan.
	rodata := bytes.Repeat([]byte{0xAA}, 0x200)
	var moduleID [32]byte
	for i := range moduleID {
		moduleID[i] = byte(0x40 + i)
	}
	img := synthNSOWithText(t, text, rodata, moduleID)
	sum := sha256.Sum256(img)
	plan.Target = Identity{
		ModuleID: hex.EncodeToString(moduleID[:]),
		SHA256:   hex.EncodeToString(sum[:]),
		Size:     int64(len(img)),
	}
	out, rep, err := Apply(img, plan)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if rep.ChangedBytes != 16 || len(rep.Edits) != 4 {
		t.Fatalf("report changed=%d edits=%d, want 16/4", rep.ChangedBytes, len(rep.Edits))
	}
	f, err := nso.Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	patched, err := f.Decompress(nso.Text)
	if err != nil {
		t.Fatal(err)
	}
	word := func(va uint32) uint32 { return binary.LittleEndian.Uint32(patched[va:]) }

	// Whatever float a writer leaves in engine+0x2DF0C (0.0 Japanese, 1.0
	// English, or noise), every forced conversion reports zero.
	for _, engine := range []float32{0, 1, 7, -1, 1.0 / 3} {
		for _, c := range []struct {
			name string
			va   uint32
			reg  uint32
		}{
			{"init window", 0x1A370, 8},
			{"reload window", 0x1C884, 8},
			{"index getter", 0x1C970, 0},
			{"cue offset", 0x1CC34, 8},
		} {
			derived := int32(engine) // fcvtzs truncates toward zero
			if movWzr(word(c.va), c.reg) {
				derived = 0
			}
			if derived != 0 {
				t.Fatalf("%s = %d for engine float %v, want 0", c.name, derived, engine)
			}
		}
	}

	// Exactly the four planned words differ; .rodata is untouched.
	diff := 0
	for i := range text {
		if text[i] != patched[i] {
			diff++
		}
	}
	if diff != 16 {
		t.Fatalf(".text changed by %d bytes, want 16", diff)
	}
	ro, err := f.Decompress(nso.Rodata)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ro, rodata) {
		t.Fatal(".rodata changed")
	}

	again, _, err := Apply(img, plan)
	if err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	if !bytes.Equal(out, again) {
		t.Fatal("two Apply calls produced different images")
	}
	third, rep3, err := Apply(out, plan)
	if err != nil {
		t.Fatalf("idempotent Apply: %v", err)
	}
	if !rep3.AlreadyApplied || !bytes.Equal(out, third) {
		t.Fatal("patched image is not idempotent")
	}
}
