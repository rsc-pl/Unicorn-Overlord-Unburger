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
	if rep.RuntimeStatus != "NOT_RUNTIME_APPROVED" {
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

func TestRegistryRepointsShape(t *testing.T) {
	p := RegistryRepoints()
	if p.Target != WesternUpdate {
		t.Fatalf("plan target = %+v", p.Target)
	}
	if len(p.Edits) != 5 {
		t.Fatalf("edits = %d, want 5", len(p.Edits))
	}
	for _, ed := range p.Edits {
		if ed.Segment != nso.Rodata {
			t.Fatalf("edit segment = %s", ed.Segment)
		}
		if len(ed.From) != 8 || len(ed.To) != 8 {
			t.Fatalf("edit at %#x is not an 8-byte field", ed.VA)
		}
		if ed.VA >= 0x84E000+0x1EBB9A0 {
			t.Fatalf("edit %#x outside the expected .rodata size", ed.VA)
		}
	}
}
