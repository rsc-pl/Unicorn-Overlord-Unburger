package cpk

import (
	"bytes"
	"sort"
	"testing"
)

// hardeningTocOffset mirrors the fixed TOC offset used by buildTestCPK.
const hardeningTocOffset = 0x200

// packetBody returns the @UTF body of the packet whose LE wrapper begins at
// wrapOffset (the 12-byte LE-wrapped region that follows a region identifier).
// The returned slice aliases blob, so mutating a parsed table built from it
// mutates the fixture in place.
func packetBody(t *testing.T, blob []byte, wrapOffset int64) []byte {
	t.Helper()
	bodySize := int64(cpkLittleEndian.Uint64(blob[wrapOffset+4 : wrapOffset+packetWrap]))
	if wrapOffset+packetWrap+bodySize > int64(len(blob)) {
		t.Fatalf("packet body at %#x exceeds blob (%d bytes)", wrapOffset, len(blob))
	}
	return blob[wrapOffset+packetWrap : wrapOffset+packetWrap+bodySize]
}

func patchHeaderField(t *testing.T, blob []byte, name string, v uint64) {
	t.Helper()
	// The header packet wrapper follows the 4-byte "CPK " identifier.
	tbl, err := parseUTF(packetBody(t, blob, 4))
	if err != nil {
		t.Fatalf("parse header: %v", err)
	}
	if err := tbl.setRowUint(0, name, v); err != nil {
		t.Fatalf("patch header %s: %v", name, err)
	}
}

func patchTOCField(t *testing.T, blob []byte, row int, name string, v uint64) {
	t.Helper()
	// The TOC packet wrapper follows the 4-byte "TOC " identifier.
	tbl, err := parseUTF(packetBody(t, blob, hardeningTocOffset+4))
	if err != nil {
		t.Fatalf("parse TOC: %v", err)
	}
	if err := tbl.setRowUint(row, name, v); err != nil {
		t.Fatalf("patch TOC %s: %v", name, err)
	}
}

// TestOpenRejectsMalformedMetadata covers bad region identifiers and header/TOC
// offsets and sizes that cannot describe a consistent archive.
func TestOpenRejectsMalformedMetadata(t *testing.T) {
	newBlob := func() []byte {
		return buildTestCPK(t, []testMember{
			{dir: "MsgSheet", name: "a.fms", stored: []byte("payload-a"), extract: 9},
			{dir: "Help", name: "b.bin", stored: []byte{1, 2, 3, 4}, extract: 4},
		})
	}

	t.Run("bad_toc_ident", func(t *testing.T) {
		blob := newBlob()
		i := bytes.Index(blob, []byte(tocMagic))
		if i < 0 {
			t.Fatal("fixture has no TOC marker")
		}
		blob[i] = 'X'
		if _, err := Open(bytes.NewReader(blob), int64(len(blob))); err == nil {
			t.Fatal("expected error for bad TOC ident")
		}
	})

	t.Run("bad_etoc_ident", func(t *testing.T) {
		blob := newBlob()
		i := bytes.Index(blob, []byte(etocMagic))
		if i < 0 {
			t.Fatal("fixture has no ETOC marker")
		}
		blob[i] = 'X'
		if _, err := Open(bytes.NewReader(blob), int64(len(blob))); err == nil {
			t.Fatal("expected error for bad ETOC ident")
		}
	})

	t.Run("toc_offset_zero", func(t *testing.T) {
		blob := newBlob()
		patchHeaderField(t, blob, "TocOffset", 0)
		if _, err := Open(bytes.NewReader(blob), int64(len(blob))); err == nil {
			t.Fatal("expected error for TocOffset 0")
		}
	})

	t.Run("toc_offset_beyond_size", func(t *testing.T) {
		blob := newBlob()
		patchHeaderField(t, blob, "TocOffset", uint64(len(blob)+0x1000))
		if _, err := Open(bytes.NewReader(blob), int64(len(blob))); err == nil {
			t.Fatal("expected error for TocOffset beyond file size")
		}
	})

	t.Run("etoc_before_content", func(t *testing.T) {
		blob := newBlob()
		patchHeaderField(t, blob, "EtocOffset", 0x100)
		if _, err := Open(bytes.NewReader(blob), int64(len(blob))); err == nil {
			t.Fatal("expected error for EtocOffset before content region")
		}
	})

	t.Run("entry_offset_outside_content", func(t *testing.T) {
		blob := newBlob()
		patchTOCField(t, blob, 0, "FileOffset", 0x100000)
		if _, err := Open(bytes.NewReader(blob), int64(len(blob))); err == nil {
			t.Fatal("expected error for entry offset outside the content region")
		}
	})

	t.Run("entry_size_overflows_content", func(t *testing.T) {
		blob := newBlob()
		patchTOCField(t, blob, 0, "FileSize", 0xFFFFFFF0)
		if _, err := Open(bytes.NewReader(blob), int64(len(blob))); err == nil {
			t.Fatal("expected error for entry size overflowing the content region")
		}
	})
}

// TestReplaceMemberResizedRepeatedLastWins checks that re-staging the same
// member supersedes the earlier payload, that a rejected (negative) staging
// leaves the previous state intact, and that the variable rebuild is
// deterministic.
func TestReplaceMemberResizedRepeatedLastWins(t *testing.T) {
	blob := buildTestCPK(t, []testMember{
		{dir: "", name: "a.bin", stored: []byte("AAAA"), extract: 4},
	})
	a, err := Open(bytes.NewReader(blob), int64(len(blob)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := a.ReplaceMemberResized("a.bin", []byte("XX"), 2); err != nil {
		t.Fatalf("first staging: %v", err)
	}
	if err := a.ReplaceMemberResized("a.bin", []byte("YYYYY"), 5); err != nil {
		t.Fatalf("second staging: %v", err)
	}
	if err := a.ReplaceMemberResized("a.bin", []byte("z"), -1); err == nil {
		t.Fatal("expected error for negative extract size")
	}

	var first, second bytes.Buffer
	if _, err := a.RebuildVariable(&first); err != nil {
		t.Fatalf("RebuildVariable: %v", err)
	}
	if _, err := a.RebuildVariable(&second); err != nil {
		t.Fatalf("second RebuildVariable: %v", err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("repeated resize rebuild is not deterministic")
	}

	a2, err := Open(bytes.NewReader(first.Bytes()), int64(len(first.Bytes())))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	e := a2.Member("a.bin")
	if e == nil {
		t.Fatal("member missing after rebuild")
	}
	if e.Compressed() || e.FileSize != 5 || e.ExtractSize != 5 {
		t.Fatalf("staged sizes = %d/%d compressed=%v, want 5/5 false", e.FileSize, e.ExtractSize, e.Compressed())
	}
	got, err := a2.Data(e)
	if err != nil {
		t.Fatalf("Data: %v", err)
	}
	if !bytes.Equal(got, []byte("YYYYY")) {
		t.Fatalf("member data = %q, want %q", got, "YYYYY")
	}
}

// TestRebuildVariableMultipleResizedMembers resizes three members at once
// (growing one, shrinking two) and leaves a fourth untouched; it verifies the
// recomputed layout, the staged payloads and byte-exact preservation of the
// untouched member, plus determinism.
func TestRebuildVariableMultipleResizedMembers(t *testing.T) {
	s1, p1 := compressedTestMember(0x33, 500)
	s2, p2 := compressedTestMember(0x66, 800)
	members := []testMember{
		{dir: "", name: "plain", stored: []byte("plain"), extract: 5},
		{dir: "X", name: "comp.fms", stored: s1, extract: int64(len(p1))},
		{dir: "Y", name: "tail", stored: []byte("tail-data"), extract: 9},
		{dir: "Z", name: "keep", stored: s2, extract: int64(len(p2))},
	}
	blob := buildTestCPK(t, members)
	a, err := Open(bytes.NewReader(blob), int64(len(blob)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	rawBefore, dataBefore := cloneEntryData(t, a)

	newPlain := bytes.Repeat([]byte("Z"), 40)
	newComp := append([]byte(nil), p1[:120]...)
	newTail := []byte("t")
	for path, payload := range map[string][]byte{
		"plain":      newPlain,
		"X/comp.fms": newComp,
		"Y/tail":     newTail,
	} {
		if err := a.ReplaceMemberResized(path, payload, int64(len(payload))); err != nil {
			t.Fatalf("ReplaceMemberResized(%s): %v", path, err)
		}
	}

	var out bytes.Buffer
	n, err := a.RebuildVariable(&out)
	if err != nil {
		t.Fatalf("RebuildVariable: %v", err)
	}
	blob2 := out.Bytes()
	if int64(len(blob2)) != n {
		t.Fatalf("reported %d bytes, wrote %d", n, len(blob2))
	}
	a2, err := Open(bytes.NewReader(blob2), int64(len(blob2)))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if len(a2.Entries) != len(members) {
		t.Fatalf("reopened entries = %d, want %d", len(a2.Entries), len(members))
	}

	// Recomputed layout stays in original offset order with the same alignment.
	ordered := make([]*Entry, len(a2.Entries))
	copy(ordered, a2.Entries)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Offset < ordered[j].Offset })
	cursor := a2.ContentOffset()
	for _, e := range ordered {
		if want := alignUpTest(cursor, a2.Header.Align); e.Offset != want {
			t.Fatalf("member %s offset %d, want %d", e.Path(), e.Offset, want)
		}
		cursor = e.Offset + e.FileSize
	}
	if want := alignUpTest(cursor, a2.Header.Align); a2.Header.EtocOffset != want {
		t.Fatalf("EtocOffset %d, want %d", a2.Header.EtocOffset, want)
	}

	want := map[string][]byte{"plain": newPlain, "X/comp.fms": newComp, "Y/tail": newTail}
	for path, payload := range want {
		e := a2.Member(path)
		if e.Compressed() || e.FileSize != int64(len(payload)) || e.ExtractSize != int64(len(payload)) {
			t.Fatalf("%s sizes = %d/%d compressed=%v", path, e.FileSize, e.ExtractSize, e.Compressed())
		}
		got, err := a2.Data(e)
		if err != nil {
			t.Fatalf("Data(%s): %v", path, err)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("%s data mismatch", path)
		}
	}

	// The untouched compressed member must survive byte-for-byte.
	keep := a2.Member("Z/keep")
	if !keep.Compressed() {
		t.Fatal("untouched member should remain compressed")
	}
	if r, _ := a2.RawData(keep); !bytes.Equal(r, rawBefore["Z/keep"]) {
		t.Fatal("untouched member raw bytes changed")
	}
	if d, _ := a2.Data(keep); !bytes.Equal(d, dataBefore["Z/keep"]) {
		t.Fatal("untouched member decompressed bytes changed")
	}

	var again bytes.Buffer
	if _, err := a.RebuildVariable(&again); err != nil {
		t.Fatalf("second RebuildVariable: %v", err)
	}
	if !bytes.Equal(again.Bytes(), blob2) {
		t.Fatal("multi-resize variable rebuild is not deterministic")
	}
}
