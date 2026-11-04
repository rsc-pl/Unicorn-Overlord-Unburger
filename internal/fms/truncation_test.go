package fms

import (
	"bytes"
	"testing"
)

// TestParseRejectsEveryTruncation cuts an FMSB sheet at every byte boundary an
// interrupted write or a partially copied member could produce. Parse must
// return an error for every cut and never read past the data it was given.
func TestParseRejectsEveryTruncation(t *testing.T) {
	blob := buildSheet([][]byte{[]byte("first"), {}, []byte("third")}, []byte{0, 0, 0})
	for cut := 0; cut < len(blob); cut++ {
		if _, err := Parse(blob[:cut]); err == nil {
			t.Fatalf("truncation to %d bytes was accepted", cut)
		}
	}
}

// TestParseRejectsHeaderOnly: exactly the fixed header, no footer, is refused.
func TestParseRejectsHeaderOnly(t *testing.T) {
	blob := buildSheet([][]byte{[]byte("x")}, nil)
	if _, err := Parse(blob[:headerSize]); err == nil {
		t.Fatal("header-only input was accepted")
	}
}

// TestParseRejectsMissingFinalTerminator builds a sheet whose last slot has no
// terminating NUL before the footer; Parse must refuse it rather than merge the
// footer into the slot.
func TestParseRejectsMissingFinalTerminator(t *testing.T) {
	index := make([]byte, indexStride)
	region := []byte("one\x00two") // second slot deliberately unterminated
	dataSize := len(index) + len(region)
	blob := []byte(magic)
	blob = appendU32(blob, uint32(dataSize))
	blob = appendU32(blob, headerSize)
	blob = appendU32(blob, 0)
	blob = appendU32(blob, 0)
	blob = appendU32(blob, 2)
	blob = appendU32(blob, 3)
	blob = appendU32(blob, 0)
	blob = append(blob, index...)
	blob = append(blob, region...)
	blob = append(blob, footerMagic...)
	blob = appendU32(blob, 0)
	blob = appendU32(blob, 0)
	blob = appendU32(blob, 0)

	if _, err := Parse(blob); err == nil {
		t.Fatal("an unterminated final slot was accepted")
	}
}

// TestParseRejectsUnsupportedDataStart: only the canonical 0x20 data start is
// supported, and Marshal refuses a sheet that was mutated to another value.
func TestParseRejectsUnsupportedDataStart(t *testing.T) {
	blob := buildSheet([][]byte{[]byte("x")}, nil)
	patch := append([]byte(nil), blob...)
	le.PutUint32(patch[8:12], 0x40)
	if _, err := Parse(patch); err == nil {
		t.Fatal("unsupported data start was accepted")
	}

	s, err := Parse(blob)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	s.DataStart = 0x40
	if _, err := s.Marshal(); err == nil {
		t.Fatal("Marshal accepted an unsupported data start")
	}
}

// TestParseRejectsEmptyAndGarbage: zero-length, sub-header, bad-magic and
// footer-removed inputs are all refused.
func TestParseRejectsEmptyAndGarbage(t *testing.T) {
	cases := map[string][]byte{
		"empty":      {},
		"one_byte":   {0x00},
		"short":      bytes.Repeat([]byte{0xAA}, headerSize+15),
		"bad_magic":  append([]byte("XXXX"), bytes.Repeat([]byte{0}, headerSize+16-4)...),
		"bad_footer": append(append([]byte(nil), buildSheet([][]byte{[]byte("x")}, nil)[:len(buildSheet([][]byte{[]byte("x")}, nil))-16]...), []byte("XXXX")...),
	}
	for name, blob := range cases {
		if _, err := Parse(blob); err == nil {
			t.Fatalf("%s: malformed input was accepted", name)
		}
	}
}

// TestSetSlotRejectsInvalidIndex: the write path refuses negative and
// out-of-range indexes instead of panicking or growing the sheet.
func TestSetSlotRejectsInvalidIndex(t *testing.T) {
	blob := buildSheet([][]byte{[]byte("a"), []byte("b")}, nil)
	s, err := Parse(blob)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, i := range []int{-1, -100, 2, 1 << 20} {
		if err := s.SetSlot(i, []byte("x")); err == nil {
			t.Fatalf("SetSlot(%d) was accepted", i)
		}
	}
	if s.SlotCount() != 2 {
		t.Fatalf("failed SetSlot changed the slot count to %d", s.SlotCount())
	}
}
