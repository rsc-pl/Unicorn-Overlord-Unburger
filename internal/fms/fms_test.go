package fms

import (
	"bytes"
	"fmt"
	"testing"
)

func buildSheet(slots [][]byte, padding []byte) []byte {
	index := make([]byte, len(slots)*indexStride)
	region := make([]byte, 0)
	for _, s := range slots {
		region = append(region, s...)
		region = append(region, slotTermByte)
	}
	region = append(region, padding...)

	dataSize := len(index) + len(region)
	out := []byte(magic)
	out = appendU32(out, uint32(dataSize))
	out = appendU32(out, headerSize)
	out = appendU32(out, 0)
	out = appendU32(out, 0)
	out = appendU32(out, uint32(len(slots)))
	out = appendU32(out, 3)
	out = appendU32(out, 0)
	out = append(out, index...)
	out = append(out, region...)
	out = append(out, footerMagic...)
	out = appendU32(out, 0)
	out = appendU32(out, 0x10)
	out = appendU32(out, 0)
	return out
}

func TestParseMarshalRoundTrip(t *testing.T) {
	slots := [][]byte{
		[]byte("first"),
		{},                                   // empty slot
		[]byte("こんにちは世界"),                    // multibyte UTF-8
		[]byte("#(2)##(NAME)^1/hero^ %s %d"), // markup-bearing
		{},                                   // another empty slot
		[]byte("last \x00-inner?"),           // placeholder, replaced below
	}
	slots[5] = []byte("last")
	padding := []byte{0, 0, 0, 0, 0}
	blob := buildSheet(slots, padding)

	s, err := Parse(blob)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if s.SlotCount() != len(slots) {
		t.Fatalf("slot count = %d, want %d", s.SlotCount(), len(slots))
	}
	for i := range slots {
		if !bytes.Equal(s.Slot(i), slots[i]) {
			t.Fatalf("slot %d = %q, want %q", i, s.Slot(i), slots[i])
		}
	}

	out, err := s.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !bytes.Equal(out, blob) {
		t.Fatalf("round trip not byte-identical (%d vs %d bytes)", len(out), len(blob))
	}
}

func TestParsePreservesIndexRegion(t *testing.T) {
	blob := buildSheet([][]byte{[]byte("x")}, nil)
	// Corrupt a byte inside the all-zero index region; the writer must keep it.
	blob[headerSize] = 0xAB
	s, err := Parse(blob)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	out, _ := s.Marshal()
	if !bytes.Equal(out, blob) {
		t.Fatal("index region not preserved verbatim")
	}
}

func TestLargeSlotCount(t *testing.T) {
	const n = 5000
	slots := make([][]byte, n)
	for i := range slots {
		if i%3 == 0 {
			continue
		}
		slots[i] = []byte{byte('A' + i%26)}
	}
	blob := buildSheet(slots, nil)
	s, err := Parse(blob)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if s.SlotCount() != n {
		t.Fatalf("slot count = %d", s.SlotCount())
	}
	out, _ := s.Marshal()
	if !bytes.Equal(out, blob) {
		t.Fatal("large sheet round trip not identical")
	}
}

// TestVariableSlotLengthRoundTrip covers single-slot edits that shrink and grow a
// slot; Marshal must serialise the new lengths and Parse must recover them.
func TestVariableSlotLengthRoundTrip(t *testing.T) {
	slots := [][]byte{[]byte("alpha"), []byte("bravo"), []byte("charlie"), {}}
	blob := buildSheet(slots, []byte{0, 0, 0})
	s, err := Parse(blob)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// shorter than the original slot
	if err := s.SetSlot(1, []byte("b")); err != nil {
		t.Fatal(err)
	}
	out, err := s.Marshal()
	if err != nil {
		t.Fatalf("Marshal short: %v", err)
	}
	re, err := Parse(out)
	if err != nil {
		t.Fatalf("Parse short: %v", err)
	}
	if string(re.Slot(1)) != "b" || re.SlotCount() != 4 {
		t.Fatalf("short edit: slot1=%q count=%d", re.Slot(1), re.SlotCount())
	}
	if string(re.Slot(0)) != "alpha" || string(re.Slot(2)) != "charlie" {
		t.Fatal("short edit changed neighbours")
	}
	// longer than the original slot
	if err := re.SetSlot(1, []byte("bravo-extended-much-longer")); err != nil {
		t.Fatal(err)
	}
	out2, err := re.Marshal()
	if err != nil {
		t.Fatalf("Marshal long: %v", err)
	}
	re2, err := Parse(out2)
	if err != nil {
		t.Fatalf("Parse long: %v", err)
	}
	if string(re2.Slot(1)) != "bravo-extended-much-longer" {
		t.Fatalf("long edit: slot1=%q", re2.Slot(1))
	}
	if string(re2.Slot(0)) != "alpha" || string(re2.Slot(2)) != "charlie" || string(re2.Slot(3)) != "" {
		t.Fatal("long edit changed neighbours")
	}
}

// TestVariableMultiSlotEdits edits several slots (including emptying one) in a
// single sheet and checks every untouched slot survives verbatim.
func TestVariableMultiSlotEdits(t *testing.T) {
	original := [][]byte{
		[]byte("zero"), []byte("one"), {}, []byte("three"), []byte("four"),
		[]byte("five"), {}, []byte("seven"),
	}
	blob := buildSheet(original, []byte{7, 7})
	s, err := Parse(blob)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := s.SetSlot(0, []byte("ZERO-EXPANDED")); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSlot(2, []byte("two")); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSlot(4, []byte("f")); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSlot(6, []byte("six-reintroduced")); err != nil {
		t.Fatal(err)
	}
	out, err := s.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	re, err := Parse(out)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := map[int]string{0: "ZERO-EXPANDED", 2: "two", 4: "f", 6: "six-reintroduced"}
	if re.SlotCount() != len(original) {
		t.Fatalf("slot count = %d", re.SlotCount())
	}
	for i := range original {
		if w, ok := want[i]; ok {
			if string(re.Slot(i)) != w {
				t.Fatalf("slot %d = %q, want %q", i, re.Slot(i), w)
			}
			continue
		}
		if !bytes.Equal(re.Slot(i), original[i]) {
			t.Fatalf("untouched slot %d = %q, want %q", i, re.Slot(i), original[i])
		}
	}
}

// TestLargeSparseVariableRebuild mirrors the real UcSysMsg sheet scale (1,451
// slots, sparse) to prove variable-length serialisation at size and that editing
// one slot leaves all neighbours byte-equal.
func TestLargeSparseVariableRebuild(t *testing.T) {
	const n = 1451
	slots := make([][]byte, n)
	for i := range slots {
		if i%97 == 0 {
			slots[i] = []byte(fmt.Sprintf("row-%04d", i))
		}
	}
	blob := buildSheet(slots, []byte{0, 0, 0, 0})
	s, err := Parse(blob)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	before := make([][]byte, n)
	for i := range before {
		before[i] = append([]byte(nil), s.Slot(i)...)
	}
	if err := s.SetSlot(1312, []byte("replacement-label")); err != nil {
		t.Fatal(err)
	}
	out, err := s.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	re, err := Parse(out)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if re.SlotCount() != n {
		t.Fatalf("slot count = %d, want %d", re.SlotCount(), n)
	}
	if string(re.Slot(1312)) != "replacement-label" {
		t.Fatalf("edited slot = %q", re.Slot(1312))
	}
	for i := range before {
		if i == 1312 {
			continue
		}
		if !bytes.Equal(re.Slot(i), before[i]) {
			t.Fatalf("neighbour slot %d changed: %q -> %q", i, before[i], re.Slot(i))
		}
	}
}

func TestSetSlot(t *testing.T) {
	slots := [][]byte{[]byte("one"), []byte("two"), []byte("three")}
	blob := buildSheet(slots, nil)
	s, err := Parse(blob)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSlot(1, []byte("TWO-EXTENDED")); err != nil {
		t.Fatalf("SetSlot: %v", err)
	}
	if err := s.SetSlot(9, []byte("x")); err == nil {
		t.Fatal("expected out-of-range error")
	}
	out, err := s.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	re, err := Parse(out)
	if err != nil {
		t.Fatalf("reparse: %v", err)
	}
	if !bytes.Equal(re.Slot(1), []byte("TWO-EXTENDED")) {
		t.Fatal("modified slot not persisted")
	}
	if re.SlotCount() != 3 {
		t.Fatal("slot count changed")
	}
}

func TestParseMalformed(t *testing.T) {
	valid := buildSheet([][]byte{[]byte("ok")}, nil)

	cases := []struct {
		name string
		data []byte
	}{
		{"short input", valid[:10]},
		{"bad magic", append([]byte("NOPE"), valid[4:]...)},
		{"missing footer", valid[:len(valid)-16]},
	}
	for _, c := range cases {
		if _, err := Parse(c.data); err == nil {
			t.Errorf("%s: expected error", c.name)
		}
	}

	// Data size mismatch.
	bad := append([]byte(nil), valid...)
	bad[4] = bad[4] + 1
	if _, err := Parse(bad); err == nil {
		t.Error("expected data-size mismatch error")
	}

	// Unsupported data start.
	bad2 := append([]byte(nil), valid...)
	le.PutUint32(bad2[8:12], 0x24)
	if _, err := Parse(bad2); err == nil {
		t.Error("expected unsupported data-start error")
	}
}

func TestParseMissingNULTerminator(t *testing.T) {
	// One slot without its terminator, footer still present.
	region := []byte("unterminated")
	dataSize := indexStride + len(region)
	blob := []byte(magic)
	blob = appendU32(blob, uint32(dataSize))
	blob = appendU32(blob, headerSize)
	blob = appendU32(blob, 0)
	blob = appendU32(blob, 0)
	blob = appendU32(blob, 1)
	blob = appendU32(blob, 3)
	blob = appendU32(blob, 0)
	blob = append(blob, make([]byte, indexStride)...)
	blob = append(blob, region...)
	blob = append(blob, footerMagic...)
	blob = appendU32(blob, 0)
	blob = appendU32(blob, 0x10)
	blob = appendU32(blob, 0)

	if _, err := Parse(blob); err == nil {
		t.Fatal("expected error for missing NUL terminator")
	}
}
