package fms

import (
	"bytes"
	"testing"
)

// TestParseTruncatedSlotIndex inflates the slot count so the slot index region
// would extend past the footer; Parse must reject it rather than read past the
// data region.
func TestParseTruncatedSlotIndex(t *testing.T) {
	blob := buildSheet([][]byte{[]byte("one"), []byte("two")}, nil)
	le.PutUint32(blob[20:24], 1<<20)
	if _, err := Parse(blob); err == nil {
		t.Fatal("expected error for slot index exceeding the data region")
	}
}

// TestParseDataSizeMismatch covers both an inflated and a shrunken data-size
// header field.
func TestParseDataSizeMismatch(t *testing.T) {
	tooLarge := buildSheet([][]byte{[]byte("x")}, nil)
	le.PutUint32(tooLarge[4:8], le.Uint32(tooLarge[4:8])+8)
	if _, err := Parse(tooLarge); err == nil {
		t.Fatal("expected error for over-large data size")
	}

	tooSmall := buildSheet([][]byte{[]byte("x")}, nil)
	le.PutUint32(tooSmall[4:8], le.Uint32(tooSmall[4:8])-1)
	if _, err := Parse(tooSmall); err == nil {
		t.Fatal("expected error for under-sized data size")
	}
}

// TestEmbeddedNULTerminatesSlot documents that the NUL byte is the only slot
// terminator: an embedded NUL ends the slot, the remainder becomes trailing
// padding, and the byte image still round-trips.
func TestEmbeddedNULTerminatesSlot(t *testing.T) {
	blob := buildSheet([][]byte{[]byte("alpha\x00omega")}, nil)
	s, err := Parse(blob)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if s.SlotCount() != 1 {
		t.Fatalf("slot count = %d, want 1", s.SlotCount())
	}
	if !bytes.Equal(s.Slot(0), []byte("alpha")) {
		t.Fatalf("slot 0 = %q, want %q", s.Slot(0), "alpha")
	}
	out, err := s.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !bytes.Equal(out, blob) {
		t.Fatal("byte image with an embedded NUL did not round-trip")
	}
}

// TestSetSlotEmbeddedNULReparseShape shows the same rule on the write path: a
// slot value containing a NUL is serialised, and on reparse it is split at that
// NUL. Callers must therefore never embed a NUL in a slot value.
func TestSetSlotEmbeddedNULReparseShape(t *testing.T) {
	blob := buildSheet([][]byte{[]byte("base")}, nil)
	s, err := Parse(blob)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := s.SetSlot(0, []byte("x\x00y")); err != nil {
		t.Fatalf("SetSlot: %v", err)
	}
	out, err := s.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	re, err := Parse(out)
	if err != nil {
		t.Fatalf("reparse: %v", err)
	}
	if re.SlotCount() != 1 || !bytes.Equal(re.Slot(0), []byte("x")) {
		t.Fatalf("reparsed slot = %q count=%d, want %q count=1", re.Slot(0), re.SlotCount(), "x")
	}
}

// TestMultibyteUTF8Boundaries proves the format is byte-oriented: 2-, 3- and
// 4-byte UTF-8 sequences (including a deliberately truncated lead byte) are
// preserved exactly. Because no UTF-8 continuation byte is 0x00, byte-level
// terminator scanning never splits a valid sequence.
func TestMultibyteUTF8Boundaries(t *testing.T) {
	slots := [][]byte{
		[]byte("café"),           // 2-byte é
		[]byte("あいう"),            // 3-byte sequences
		[]byte("😀"),              // 4-byte sequence
		{0xE3, 0x81},             // truncated 3-byte lead, preserved raw
		[]byte("mixed 混合 😀 あ"),   // mixed widths
		{0x7F, 0x80, 0xBF, 0xC0}, // boundary bytes around the lead range
	}
	blob := buildSheet(slots, []byte{0, 0})
	s, err := Parse(blob)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if s.SlotCount() != len(slots) {
		t.Fatalf("slot count = %d, want %d", s.SlotCount(), len(slots))
	}
	for i := range slots {
		if !bytes.Equal(s.Slot(i), slots[i]) {
			t.Fatalf("slot %d = % x, want % x", i, s.Slot(i), slots[i])
		}
	}
	out, err := s.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !bytes.Equal(out, blob) {
		t.Fatal("multibyte slot round trip not byte-identical")
	}
}
