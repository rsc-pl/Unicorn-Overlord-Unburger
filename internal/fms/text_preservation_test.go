package fms

import (
	"bytes"
	"strings"
	"testing"
)

// markupSlots is a synthetic sheet exercising every control-code shape the real
// message sheets use: italics, colour, waits (both sigils), key/placeholder
// tokens, an author-inserted line break, bare sigils and an unbalanced token.
func markupSlots() [][]byte {
	return [][]byte{
		[]byte("#iItalic#/i"),                        // italic pair
		[]byte("#c(255,0,0)red#/c"),                  // colour pair
		[]byte("Wait#w(15)then continue"),            // hash wait
		[]byte("Wait@w(30)then continue"),            // at wait (JP-source form)
		[]byte("Hello#FN(Alain)#/FN"),                // name/key token
		[]byte("##(BUTTON)Confirm"),                  // button placeholder
		[]byte("Line one\nLine two\nLine three"),     // authored breaks
		[]byte("タブ\tと CRLF\r\nの混在"),                  // raw tab and CRLF
		[]byte("#most#/i"),                           // malformed western markup
		[]byte("bare # and bare @ and #w( trailing"), // bare sigils, unterminated token
		{}, // empty slot
	}
}

// TestMarkupAndControlCodesRoundTrip: every control-code shape survives a
// read/write round trip byte-for-byte, and a SetSlot rewrite of one slot leaves
// every other slot untouched.
func TestMarkupAndControlCodesRoundTrip(t *testing.T) {
	slots := markupSlots()
	blob := buildSheet(slots, []byte{0, 0, 0, 0})

	s, err := Parse(blob)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if s.SlotCount() != len(slots) {
		t.Fatalf("slot count = %d, want %d", s.SlotCount(), len(slots))
	}
	for i, want := range slots {
		if !bytes.Equal(s.Slot(i), want) {
			t.Fatalf("slot %d = %q, want %q", i, s.Slot(i), want)
		}
	}
	out, err := s.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !bytes.Equal(out, blob) {
		t.Fatal("markup round trip is not byte-identical")
	}
}

// TestEditPreservesMarkupInNeighbours rewrites one markup slot and proves the
// control codes of the neighbouring slots are preserved exactly.
func TestEditPreservesMarkupInNeighbours(t *testing.T) {
	slots := markupSlots()
	blob := buildSheet(slots, nil)
	s, err := Parse(blob)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	edited := []byte("#iLonger rewrite#/i#line two\n#FN(Alain)#/FN")
	if err := s.SetSlot(0, edited); err != nil {
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
	if !bytes.Equal(re.Slot(0), edited) {
		t.Fatalf("edited slot = %q, want %q", re.Slot(0), edited)
	}
	for i := 1; i < len(slots); i++ {
		if !bytes.Equal(re.Slot(i), slots[i]) {
			t.Fatalf("neighbour slot %d changed: %q", i, re.Slot(i))
		}
	}
}

// TestLongAndMultilineSlotsPreserved covers a very long single slot and a slot
// with many lines (the shapes the UI display gate cares about), including a
// multibyte payload at both ends.
func TestLongAndMultilineSlotsPreserved(t *testing.T) {
	long := []byte("始" + strings.Repeat("x", 6000) + "終")
	var b strings.Builder
	for i := 0; i < 64; i++ {
		b.WriteString("line ")
		b.WriteString(strings.Repeat("y", i))
		b.WriteString("\n")
	}
	multi := []byte(strings.TrimRight(b.String(), "\n") + "#w(10)\n#i尾#/i")
	slots := [][]byte{long, multi, []byte("short")}

	blob := buildSheet(slots, nil)
	s, err := Parse(blob)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for i, want := range slots {
		if !bytes.Equal(s.Slot(i), want) {
			t.Fatalf("slot %d mismatch (len %d vs %d)", i, len(s.Slot(i)), len(want))
		}
	}
	out, err := s.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	re, err := Parse(out)
	if err != nil {
		t.Fatalf("reparse: %v", err)
	}
	if !bytes.Equal(re.Slot(0), long) || !bytes.Equal(re.Slot(1), multi) {
		t.Fatal("long/multiline slots did not survive the round trip")
	}
}

// TestNoImplicitNormalization: raw bytes are never rewritten. A UTF-8 BOM, a
// no-break space, CRLF and a lone invalid byte all round-trip unchanged.
func TestNoImplicitNormalization(t *testing.T) {
	raw := [][]byte{
		{0xEF, 0xBB, 0xBF, 'B', 'O', 'M'},
		[]byte("nbsp\u00a0here"),
		[]byte("crlf\r\nlf\n"),
		{0xC3, 0x28}, // invalid UTF-8 sequence
		[]byte("   trailing spaces   "),
	}
	blob := buildSheet(raw, nil)
	s, err := Parse(blob)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for i, want := range raw {
		if !bytes.Equal(s.Slot(i), want) {
			t.Fatalf("slot %d = % x, want % x", i, s.Slot(i), want)
		}
	}
	out, err := s.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !bytes.Equal(out, blob) {
		t.Fatal("raw bytes were normalized during the round trip")
	}
}
