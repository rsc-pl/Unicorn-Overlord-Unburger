package cpk

import (
	"bytes"
	"sort"
	"testing"
)

type testColumn struct {
	name  string
	vtype byte
}

type testTable struct {
	name    string
	columns []testColumn
	rows    [][]any
}

func testWidth(vtype byte) int {
	switch vtype {
	case utfTypeU8, utfTypeS8:
		return 1
	case utfTypeU16, utfTypeS16:
		return 2
	case utfTypeU32, utfTypeS32, utfTypeStr:
		return 4
	case utfTypeU64, utfTypeS64, utfTypeData:
		return 8
	}
	return 0
}

func appendBE32(dst []byte, v uint32) []byte {
	return append(dst, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

func appendBE64(dst []byte, v uint64) []byte {
	return append(dst, byte(v>>56), byte(v>>48), byte(v>>40), byte(v>>32),
		byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

func appendLE64(dst []byte, v uint64) []byte {
	return append(dst, byte(v), byte(v>>8), byte(v>>16), byte(v>>24),
		byte(v>>32), byte(v>>40), byte(v>>48), byte(v>>56))
}

func padTo(out []byte, n int) []byte {
	for len(out) < n {
		out = append(out, 0)
	}
	return out
}

// encodeTestUTF builds a minimal, self-consistent @UTF packet. It is a test-only
// fixture generator; it exercises the reader rather than reproducing the
// original tool's encoder byte layout.
func encodeTestUTF(t *testing.T, tt testTable) []byte {
	t.Helper()

	var stringsPool []byte
	seen := map[string]uint32{}
	addStr := func(s string) uint32 {
		if off, ok := seen[s]; ok {
			return off
		}
		off := uint32(len(stringsPool))
		seen[s] = off
		stringsPool = append(stringsPool, s...)
		stringsPool = append(stringsPool, 0)
		return off
	}

	var dataPool []byte
	nameOff := addStr(tt.name)
	colNameOff := make([]uint32, len(tt.columns))
	rowWidth := 0
	for i, c := range tt.columns {
		colNameOff[i] = addStr(c.name)
		rowWidth += testWidth(c.vtype)
	}

	rows := make([][]byte, len(tt.rows))
	for ri, row := range tt.rows {
		buf := make([]byte, 0, rowWidth)
		for ci, c := range tt.columns {
			v := row[ci]
			switch c.vtype {
			case utfTypeStr:
				buf = appendBE32(buf, addStr(v.(string)))
			case utfTypeData:
				d := v.([]byte)
				off := len(dataPool)
				dataPool = append(dataPool, d...)
				buf = appendBE32(buf, uint32(off))
				buf = appendBE32(buf, uint32(len(d)))
			default:
				switch c.vtype {
				case utfTypeU32, utfTypeS32:
					buf = appendBE32(buf, uint32(v.(uint64)))
				case utfTypeU64, utfTypeS64:
					buf = appendBE64(buf, v.(uint64))
				case utfTypeU16, utfTypeS16:
					buf = append(buf, byte(v.(uint64)>>8), byte(v.(uint64)))
				case utfTypeU8, utfTypeS8:
					buf = append(buf, byte(v.(uint64)))
				default:
					t.Fatalf("unsupported test column type 0x%02x", c.vtype)
				}
			}
		}
		rows[ri] = buf
	}

	colDefsLen := 5 * len(tt.columns)
	rowsStart := 32 + colDefsLen
	rowsLen := rowWidth * len(tt.rows)
	stringsStart := rowsStart + rowsLen
	dataStart := stringsStart + len(stringsPool)
	total := dataStart + len(dataPool)

	pkt := make([]byte, total)
	copy(pkt[0:4], "@UTF")
	utfByteOrder.PutUint32(pkt[4:8], uint32(total))
	utfByteOrder.PutUint32(pkt[8:12], uint32(rowsStart-8))
	utfByteOrder.PutUint32(pkt[12:16], uint32(stringsStart-8))
	utfByteOrder.PutUint32(pkt[16:20], uint32(dataStart-8))
	utfByteOrder.PutUint32(pkt[20:24], nameOff)
	utfByteOrder.PutUint16(pkt[24:26], uint16(len(tt.columns)))
	utfByteOrder.PutUint16(pkt[26:28], uint16(rowWidth))
	utfByteOrder.PutUint32(pkt[28:32], uint32(len(tt.rows)))

	pos := 32
	for i, c := range tt.columns {
		pkt[pos] = c.vtype
		pos++
		utfByteOrder.PutUint32(pkt[pos:pos+4], colNameOff[i])
		pos += 4
	}
	for _, row := range rows {
		copy(pkt[pos:], row)
		pos += len(row)
	}
	copy(pkt[stringsStart:], stringsPool)
	copy(pkt[dataStart:], dataPool)
	return pkt
}

type testMember struct {
	dir, name string
	stored    []byte
	extract   int64
}

func sumStored(members []testMember) uint64 {
	var n uint64
	for _, m := range members {
		n += uint64(len(m.stored))
	}
	return n
}

func sumExtract(members []testMember) uint64 {
	var n uint64
	for _, m := range members {
		n += uint64(m.extract)
	}
	return n
}

func alignUpTest(v, a int64) int64 {
	if a <= 1 {
		return v
	}
	if r := v % a; r != 0 {
		return v + (a - r)
	}
	return v
}

func buildTestCPK(t *testing.T, members []testMember) []byte {
	t.Helper()
	const (
		tocOffset     = 0x200
		contentOffset = 0x400
		align         = 0x100
	)
	addOffset := int64(tocOffset)

	type placed struct {
		m   testMember
		off int64
		rel int64
	}
	list := make([]placed, len(members))
	cursor := int64(contentOffset)
	for i, m := range members {
		off := alignUpTest(cursor, align)
		list[i] = placed{m: m, off: off, rel: off - addOffset}
		cursor = off + int64(len(m.stored))
	}
	etocOffset := alignUpTest(cursor, align)

	tocRows := make([][]any, len(members))
	for i, p := range list {
		tocRows[i] = []any{p.m.dir, p.m.name, uint64(len(p.m.stored)), uint64(p.m.extract), uint64(p.rel), uint64(i), ""}
	}
	tocPacket := encodeTestUTF(t, testTable{
		name: "TOC ",
		columns: []testColumn{
			{"DirName", utfTypeStr}, {"FileName", utfTypeStr}, {"FileSize", utfTypeU32},
			{"ExtractSize", utfTypeU32}, {"FileOffset", utfTypeU32}, {"ID", utfTypeU32}, {"UserString", utfTypeStr},
		},
		rows: tocRows,
	})
	etocRows := make([][]any, 0, len(members)+1)
	for i, p := range list {
		etocRows = append(etocRows, []any{uint64(1_600_000_000_000_000_000 + int64(i)*2048), p.m.dir})
	}
	// The real archives carry one trailing empty sentinel row; the rebuilt ETOC
	// must keep it last.
	etocRows = append(etocRows, []any{uint64(0), ""})
	etocPacket := encodeTestUTF(t, testTable{
		name: "ETOC",
		columns: []testColumn{
			{"UpdateDateTime", utfTypeU64}, {"LocalDir", utfTypeStr},
		},
		rows: etocRows,
	})
	headerPacket := encodeTestUTF(t, testTable{
		name: "CpkHeader",
		columns: []testColumn{
			{"TocOffset", utfTypeU32}, {"ContentOffset", utfTypeU32}, {"EtocOffset", utfTypeU32},
			{"ItocOffset", utfTypeU32}, {"GtocOffset", utfTypeU32}, {"Files", utfTypeU32}, {"Align", utfTypeU32},
			{"Sorted", utfTypeU32}, {"TocSize", utfTypeU32}, {"EtocSize", utfTypeU32},
			{"ContentSize", utfTypeU32}, {"EnabledPackedSize", utfTypeU32}, {"EnabledDataSize", utfTypeU32},
		},
		rows: [][]any{{
			uint64(tocOffset), uint64(contentOffset), uint64(etocOffset), uint64(0), uint64(0),
			uint64(len(members)), uint64(align), uint64(1),
			uint64(16 + len(tocPacket)), uint64(16 + len(etocPacket)),
			uint64(etocOffset - contentOffset),
			sumExtract(members), sumStored(members),
		}},
	})

	out := []byte("CPK ")
	out = appendLE32(out, 0)
	out = appendLE64(out, uint64(len(headerPacket)))
	out = append(out, headerPacket...)
	if len(out) > tocOffset {
		t.Fatalf("header packet %d bytes exceeds TocOffset %d", len(out), tocOffset)
	}
	out = padTo(out, tocOffset)
	out = append(out, "TOC "...)
	out = appendLE32(out, 0)
	out = appendLE64(out, uint64(len(tocPacket)))
	out = append(out, tocPacket...)
	if len(out) > contentOffset {
		t.Fatalf("TOC packet %d bytes exceeds ContentOffset %d", len(out), contentOffset)
	}
	out = padTo(out, contentOffset)
	for _, p := range list {
		out = padTo(out, int(p.off))
		out = append(out, p.m.stored...)
	}
	out = padTo(out, int(etocOffset))
	out = append(out, "ETOC"...)
	out = appendLE32(out, 0)
	out = appendLE64(out, uint64(len(etocPacket)))
	out = append(out, etocPacket...)
	return out
}

func TestParseUTFValues(t *testing.T) {
	pkt := encodeTestUTF(t, testTable{
		name: "Demo",
		columns: []testColumn{
			{"Name", utfTypeStr}, {"Value", utfTypeU32}, {"Wide", utfTypeU64},
		},
		rows: [][]any{
			{"alpha", uint64(7), uint64(0x1122334455667788)},
			{"beta", uint64(9), uint64(1)},
		},
	})
	tbl, err := parseUTF(pkt)
	if err != nil {
		t.Fatalf("parseUTF: %v", err)
	}
	if tbl.name != "Demo" || tbl.numRows != 2 {
		t.Fatalf("table header wrong: name=%q rows=%d", tbl.name, tbl.numRows)
	}
	if s, _, _ := tbl.stringValue(0, "Name"); s != "alpha" {
		t.Fatalf("row0 Name = %q", s)
	}
	if v, ok, _ := tbl.intValue(1, "Wide"); !ok || v != 1 {
		t.Fatalf("row1 Wide = %d ok=%v", v, ok)
	}
	if _, ok := tbl.value(5, "Name"); ok {
		t.Fatal("out-of-range row should be missing")
	}
}

func TestParseUTFMalformed(t *testing.T) {
	if _, err := parseUTF([]byte("nope")); err == nil {
		t.Fatal("expected error for short packet")
	}
	pkt := encodeTestUTF(t, testTable{name: "T", columns: []testColumn{{"A", utfTypeU32}}, rows: [][]any{{uint64(1)}}})
	pkt[28] = 0xff // inflate row count
	if _, err := parseUTF(pkt); err == nil {
		t.Fatal("expected error for rows region overflow")
	}
}

func TestSetRowUint(t *testing.T) {
	pkt := encodeTestUTF(t, testTable{
		name:    "T",
		columns: []testColumn{{"A", utfTypeU32}, {"B", utfTypeU64}},
		rows:    [][]any{{uint64(1), uint64(2)}},
	})
	tbl, err := parseUTF(pkt)
	if err != nil {
		t.Fatal(err)
	}
	if err := tbl.setRowUint(0, "B", 0xdeadbeefcafebabe); err != nil {
		t.Fatalf("setRowUint: %v", err)
	}
	if err := tbl.setRowUint(0, "A", 0x1234); err != nil {
		t.Fatalf("setRowUint: %v", err)
	}
	re, err := parseUTF(pkt)
	if err != nil {
		t.Fatal(err)
	}
	if v, _, _ := re.intValue(0, "A"); v != 0x1234 {
		t.Fatalf("A = %#x", v)
	}
	if v, _, _ := re.uintValue(0, "B"); v != 0xdeadbeefcafebabe {
		t.Fatalf("B = %#x", v)
	}
	if err := tbl.setRowUint(0, "missing", 1); err == nil {
		t.Fatal("expected error for unknown column")
	}
}

func TestSyntheticCPKRoundTrip(t *testing.T) {
	members := []testMember{
		{dir: "", name: "root.bin", stored: []byte("root payload"), extract: int64(len("root payload"))},
		{dir: "MsgSheet", name: "a.fms", stored: []byte("markup #(2) ##(START) \xe3\x81\x82"), extract: int64(len("markup #(2) ##(START) \xe3\x81\x82"))},
		{dir: "Help", name: "b.bin", stored: []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, extract: 10},
	}
	blob := buildTestCPK(t, members)

	a, err := Open(bytes.NewReader(blob), int64(len(blob)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if len(a.Entries) != len(members) {
		t.Fatalf("entries = %d, want %d", len(a.Entries), len(members))
	}
	if a.Header.Align != 0x100 || a.Header.Files != int64(len(members)) {
		t.Fatalf("header = %+v", a.Header)
	}
	if e := a.Member("MsgSheet/a.fms"); e == nil {
		t.Fatal("member MsgSheet/a.fms not found")
	}
	got, err := a.Data(a.Member("Help/b.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, members[2].stored) {
		t.Fatalf("Data mismatch: %q", got)
	}

	var buf bytes.Buffer
	n, err := a.Rebuild(&buf)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if n != int64(len(blob)) || !bytes.Equal(buf.Bytes(), blob) {
		t.Fatalf("Rebuild not byte-identical: wrote %d bytes, want %d", n, len(blob))
	}
}

func TestSyntheticCPKCompressedMember(t *testing.T) {
	var header [256]byte
	for i := range header {
		header[i] = byte(i * 3)
	}
	const n = 600
	stream := encodeBackRefCRILAYLA(header, 0x5A, n)
	want := append(append([]byte(nil), header[:]...), bytes.Repeat([]byte{0x5A}, n)...)

	blob := buildTestCPK(t, []testMember{
		{dir: "MsgSheet", name: "c.fms", stored: stream, extract: int64(len(want))},
	})
	a, err := Open(bytes.NewReader(blob), int64(len(blob)))
	if err != nil {
		t.Fatal(err)
	}
	e := a.Member("MsgSheet/c.fms")
	if e == nil {
		t.Fatal("member missing")
	}
	if !e.Compressed() {
		t.Fatal("member should be reported compressed")
	}
	got, err := a.Data(e)
	if err != nil {
		t.Fatalf("Data: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("decompressed mismatch: got %d bytes want %d", len(got), len(want))
	}
	var buf bytes.Buffer
	if _, err := a.Rebuild(&buf); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf.Bytes(), blob) {
		t.Fatal("compressed member rebuild not identical")
	}
}

func TestSyntheticCPKReplaceMember(t *testing.T) {
	members := []testMember{
		{dir: "", name: "a.bin", stored: []byte("AAAA"), extract: 4},
		{dir: "", name: "b.bin", stored: []byte("BBBB"), extract: 4},
	}
	blob := buildTestCPK(t, members)
	a, err := Open(bytes.NewReader(blob), int64(len(blob)))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.ReplaceMember("a.bin", []byte("ZZZZ")); err != nil {
		t.Fatalf("ReplaceMember: %v", err)
	}
	if err := a.ReplaceMember("a.bin", []byte("too long")); err == nil {
		t.Fatal("expected rejection of size-changing replacement")
	}
	var buf bytes.Buffer
	if _, err := a.Rebuild(&buf); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	out := buf.Bytes()
	if len(out) != len(blob) {
		t.Fatalf("length changed: %d vs %d", len(out), len(blob))
	}
	diffs := 0
	for i := range out {
		if out[i] != blob[i] {
			diffs++
		}
	}
	if diffs != 4 {
		t.Fatalf("expected exactly 4 changed bytes, got %d", diffs)
	}
	if !bytes.Contains(out, []byte("ZZZZ")) {
		t.Fatal("replacement not written")
	}
}

func TestOpenRejectsMalformed(t *testing.T) {
	if _, err := Open(bytes.NewReader([]byte("short")), 5); err == nil {
		t.Fatal("expected error for tiny input")
	}
	bad := make([]byte, 32)
	copy(bad, "NOPE")
	if _, err := Open(bytes.NewReader(bad), int64(len(bad))); err == nil {
		t.Fatal("expected error for bad magic")
	}
}

// compressedTestMember returns a CRILAYLA stream plus the plaintext it expands to.
func compressedTestMember(seed byte, n int) (stored, plain []byte) {
	var hdr [256]byte
	for i := range hdr {
		hdr[i] = byte(i*int(seed) + int(seed))
	}
	stored = encodeBackRefCRILAYLA(hdr, seed, n)
	plain = append(append([]byte(nil), hdr[:]...), bytes.Repeat([]byte{seed}, n)...)
	return stored, plain
}

func cloneEntryData(t *testing.T, a *Archive) (map[string][]byte, map[string][]byte) {
	t.Helper()
	raw := map[string][]byte{}
	data := map[string][]byte{}
	for _, e := range a.Entries {
		r, err := a.RawData(e)
		if err != nil {
			t.Fatalf("RawData(%s): %v", e.Path(), err)
		}
		d, err := a.Data(e)
		if err != nil {
			t.Fatalf("Data(%s): %v", e.Path(), err)
		}
		raw[e.Path()] = r
		data[e.Path()] = d
	}
	return raw, data
}

// TestRebuildVariableNoOpIdentical covers the no-op path: an archive with no
// staged replacement must rebuild byte-identically (including a compressed member).
func TestRebuildVariableNoOpIdentical(t *testing.T) {
	stored, plain := compressedTestMember(0x41, 400)
	blob := buildTestCPK(t, []testMember{
		{dir: "", name: "a.bin", stored: []byte("hello world"), extract: 11},
		{dir: "MsgSheet", name: "c.fms", stored: stored, extract: int64(len(plain))},
		{dir: "Help", name: "d.bin", stored: []byte{9, 8, 7, 6, 5}, extract: 5},
	})
	a, err := Open(bytes.NewReader(blob), int64(len(blob)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var buf bytes.Buffer
	n, err := a.RebuildVariable(&buf)
	if err != nil {
		t.Fatalf("RebuildVariable: %v", err)
	}
	if n != int64(len(blob)) || !bytes.Equal(buf.Bytes(), blob) {
		t.Fatalf("no-op variable rebuild not byte-identical: wrote %d, want %d", n, len(blob))
	}
}

// TestRebuildVariableResizeLayoutAndPreservation covers shorter and longer single
// member edits, layout recomputation, integrity and untouched-member preservation.
func TestRebuildVariableResizeLayoutAndPreservation(t *testing.T) {
	stored, plain := compressedTestMember(0x41, 400)
	members := []testMember{
		{dir: "", name: "keep.bin", stored: []byte("KEEPERS"), extract: 7},
		{dir: "MsgSheet", name: "c.fms", stored: stored, extract: int64(len(plain))},
		{dir: "Help", name: "tail.bin", stored: []byte{1, 2, 3, 4, 5, 6, 7}, extract: 7},
	}

	cases := []struct {
		name     string
		keep     []byte
		extended []byte // appended to the decompressed c.fms plaintext
	}{
		{"shorter", []byte("K"), nil},
		{"longer", []byte("KEEPERS-EXTENDED-LONGER"), []byte("-TAIL")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			blob := buildTestCPK(t, members)
			a, err := Open(bytes.NewReader(blob), int64(len(blob)))
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			rawBefore, dataBefore := cloneEntryData(t, a)

			newC := append(append([]byte(nil), plain...), tc.extended...)
			if err := a.ReplaceMemberResized("keep.bin", tc.keep, int64(len(tc.keep))); err != nil {
				t.Fatalf("ReplaceMemberResized keep.bin: %v", err)
			}
			if err := a.ReplaceMemberResized("MsgSheet/c.fms", newC, int64(len(newC))); err != nil {
				t.Fatalf("ReplaceMemberResized c.fms: %v", err)
			}
			if err := a.ReplaceMemberResized("missing", []byte("x"), 1); err == nil {
				t.Fatal("expected error for unknown member")
			}
			if _, err := a.Rebuild(&bytes.Buffer{}); err == nil {
				t.Fatal("size-preserving Rebuild must reject a resized archive")
			}

			var buf bytes.Buffer
			n, err := a.RebuildVariable(&buf)
			if err != nil {
				t.Fatalf("RebuildVariable: %v", err)
			}
			out := buf.Bytes()
			if int64(len(out)) != n {
				t.Fatalf("reported %d bytes, wrote %d", n, len(out))
			}

			a2, err := Open(bytes.NewReader(out), int64(len(out)))
			if err != nil {
				t.Fatalf("reopen: %v", err)
			}
			if len(a2.Entries) != len(members) {
				t.Fatalf("reopened entries = %d", len(a2.Entries))
			}

			// Recomputed layout: original offset order, same align, no overlap.
			ordered := make([]*Entry, len(a2.Entries))
			copy(ordered, a2.Entries)
			sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Offset < ordered[j].Offset })
			cursor := a2.ContentOffset()
			for _, e := range ordered {
				if want := alignUpTest(cursor, a2.Header.Align); e.Offset != want {
					t.Fatalf("member %s offset %d, want %d", e.Path(), e.Offset, want)
				}
				if e.Offset < cursor {
					t.Fatalf("member %s overlaps previous payload", e.Path())
				}
				cursor = e.Offset + e.FileSize
			}
			wantEtoc := alignUpTest(cursor, a2.Header.Align)
			if a2.Header.EtocOffset != wantEtoc || a2.EtocOffset() != wantEtoc {
				t.Fatalf("EtocOffset %d/%d, want %d", a2.Header.EtocOffset, a2.EtocOffset(), wantEtoc)
			}

			// Modified members: stored uncompressed, sized as staged.
			keep := a2.Member("keep.bin")
			if keep.Compressed() || keep.FileSize != int64(len(tc.keep)) || keep.ExtractSize != int64(len(tc.keep)) {
				t.Fatalf("keep.bin sizes = %d/%d compressed=%v", keep.FileSize, keep.ExtractSize, keep.Compressed())
			}
			if got, _ := a2.Data(keep); !bytes.Equal(got, tc.keep) {
				t.Fatalf("keep.bin data = %q", got)
			}
			c := a2.Member("MsgSheet/c.fms")
			if c.Compressed() || c.FileSize != int64(len(newC)) || c.ExtractSize != int64(len(newC)) {
				t.Fatalf("c.fms sizes = %d/%d compressed=%v", c.FileSize, c.ExtractSize, c.Compressed())
			}
			if got, _ := a2.Data(c); !bytes.Equal(got, newC) {
				t.Fatalf("c.fms data mismatch")
			}

			// Untouched member preserved byte-for-byte and decompressed-equal.
			tail := a2.Member("Help/tail.bin")
			if r, _ := a2.RawData(tail); !bytes.Equal(r, rawBefore["Help/tail.bin"]) {
				t.Fatal("untouched member RawData changed")
			}
			if d, _ := a2.Data(tail); !bytes.Equal(d, dataBefore["Help/tail.bin"]) {
				t.Fatal("untouched member Data changed")
			}
			// Determinism: a second variable rebuild is identical.
			var again bytes.Buffer
			if _, err := a.RebuildVariable(&again); err != nil {
				t.Fatalf("second RebuildVariable: %v", err)
			}
			if !bytes.Equal(again.Bytes(), out) {
				t.Fatal("variable rebuild not deterministic")
			}
		})
	}
}

// TestAddMemberAppendsTOCAndETOCRows covers staging a new, uncompressed member
// and is the regression test for a container TOC bug: the TOC must stay
// genuinely path-sorted with IDs 0..N-1 and the header must agree, the ETOC must
// gain one file row while keeping its trailing empty sentinel, every existing
// member must keep its bytes and offset, and a second rebuild must be identical.
func TestAddMemberAppendsTOCAndETOCRows(t *testing.T) {
	members := []testMember{
		{dir: "", name: "root.bin", stored: []byte("root payload"), extract: 12},
		{dir: "MsgSheet", name: "a.fms", stored: []byte("AAAA"), extract: 4},
		{dir: "Help", name: "b.bin", stored: []byte{0, 1, 2, 3, 4}, extract: 5},
	}
	blob := buildTestCPK(t, members)
	a, err := Open(bytes.NewReader(blob), int64(len(blob)))
	if err != nil {
		t.Fatal(err)
	}
	rawBefore, _ := cloneEntryData(t, a)
	offBefore := map[string]int64{}
	etocBefore := map[string]struct {
		stamp uint64
		dir   string
	}{}
	for i, e := range a.Entries {
		offBefore[e.Path()] = e.Offset
		stamp, _, _ := a.etocTable.uintValue(i, "UpdateDateTime")
		dir, _, _ := a.etocTable.stringValue(i, "LocalDir")
		etocBefore[e.Path()] = struct {
			stamp uint64
			dir   string
		}{stamp, dir}
	}

	payload := bytes.Repeat([]byte{0xA5, 0x5A, 0x11, 0x22}, 260)
	if err := a.AddMember("GUI", "Opening100.ftx", payload, int64(len(payload))); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	if err := a.AddMember("GUI", "Opening100.ftx", payload, int64(len(payload))); err == nil {
		t.Fatal("duplicate AddMember must be rejected")
	}
	if err := a.AddMember("GUI", "bad", payload, int64(len(payload))+1); err == nil {
		t.Fatal("compressed new member must be rejected")
	}
	if _, err := a.Rebuild(&bytes.Buffer{}); err == nil {
		t.Fatal("size-preserving Rebuild must reject an added member")
	}

	var first bytes.Buffer
	if _, err := a.RebuildVariable(&first); err != nil {
		t.Fatalf("RebuildVariable: %v", err)
	}
	var second bytes.Buffer
	if _, err := a.RebuildVariable(&second); err != nil {
		t.Fatalf("second RebuildVariable: %v", err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("rebuild with an added member is not deterministic")
	}

	out := first.Bytes()
	a2, err := Open(bytes.NewReader(out), int64(len(out)))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if len(a2.Entries) != len(members)+1 {
		t.Fatalf("reopened entries = %d, want %d", len(a2.Entries), len(members)+1)
	}
	if a2.Header.Files != int64(len(members)+1) {
		t.Fatalf("header Files = %d", a2.Header.Files)
	}

	hdrUint := func(name string) uint64 {
		v, _, err := a2.headerTable.uintValue(0, name)
		if err != nil {
			t.Fatalf("header %s: %v", name, err)
		}
		return v
	}
	if got := hdrUint("Sorted"); got != 1 {
		t.Fatalf("header Sorted = %d, want 1", got)
	}
	// The rebuilt TOC must be genuinely path-sorted and IDs must be sequential.
	paths := make([]string, len(a2.Entries))
	for i, e := range a2.Entries {
		paths[i] = e.Path()
		if e.ID != uint64(i) {
			t.Fatalf("TOC row %d (%s) ID = %d, want %d", i, e.Path(), e.ID, i)
		}
	}
	if !sort.StringsAreSorted(paths) {
		t.Fatalf("TOC is not path-sorted: %q", paths)
	}
	if a2.tocTable.numRows != len(paths) {
		t.Fatalf("TOC rows = %d, want %d", a2.tocTable.numRows, len(paths))
	}

	// Header size/count fields agree with the rebuilt packets.
	if got := hdrUint("TocSize"); got != uint64(16+len(a2.tocTable.raw)) {
		t.Fatalf("TocSize = %d, want %d", got, 16+len(a2.tocTable.raw))
	}
	if got := hdrUint("EtocSize"); got != uint64(16+len(a2.etocTable.raw)) {
		t.Fatalf("EtocSize = %d, want %d", got, 16+len(a2.etocTable.raw))
	}
	if got := hdrUint("ContentSize"); got != uint64(a2.Header.EtocOffset-a2.Header.ContentOffset) {
		t.Fatalf("ContentSize = %d", got)
	}
	var wantStored, wantExtract uint64
	for _, e := range a2.Entries {
		wantStored += uint64(e.FileSize)
		wantExtract += uint64(e.ExtractSize)
	}
	if got := hdrUint("EnabledDataSize"); got != wantStored {
		t.Fatalf("EnabledDataSize = %d, want %d", got, wantStored)
	}
	if got := hdrUint("EnabledPackedSize"); got != wantExtract {
		t.Fatalf("EnabledPackedSize = %d, want %d", got, wantExtract)
	}
	if a2.Header.EtocOffset%a2.Header.Align != 0 {
		t.Fatalf("EtocOffset %d is not aligned to %d", a2.Header.EtocOffset, a2.Header.Align)
	}

	// Existing members keep their payload and their source offset.
	for _, e := range a2.Entries {
		if e.Path() == "GUI/Opening100.ftx" {
			continue
		}
		raw, err := a2.RawData(e)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(raw, rawBefore[e.Path()]) {
			t.Fatalf("existing member %q payload changed", e.Path())
		}
		if e.Offset != offBefore[e.Path()] {
			t.Fatalf("existing member %q offset %d, want %d", e.Path(), e.Offset, offBefore[e.Path()])
		}
	}

	// The ETOC has one row per file plus the trailing empty sentinel, and its
	// rows are positionally parallel to the path-sorted TOC.
	if got, want := a2.etocTable.numRows, len(paths)+1; got != want {
		t.Fatalf("ETOC rows = %d, want %d", got, want)
	}
	for i, e := range a2.Entries {
		dir, _, _ := a2.etocTable.stringValue(i, "LocalDir")
		if dir != e.DirName {
			t.Fatalf("ETOC row %d LocalDir = %q, want %q", i, dir, e.DirName)
		}
		if e.Path() != "GUI/Opening100.ftx" {
			before := etocBefore[e.Path()]
			stamp, _, _ := a2.etocTable.uintValue(i, "UpdateDateTime")
			if stamp != before.stamp {
				t.Fatalf("ETOC row %d (%s) timestamp = %d, want %d", i, e.Path(), stamp, before.stamp)
			}
		}
	}
	sentStamp, _, _ := a2.etocTable.uintValue(a2.etocTable.numRows-1, "UpdateDateTime")
	sentDir, _, _ := a2.etocTable.stringValue(a2.etocTable.numRows-1, "LocalDir")
	if sentStamp != 0 || sentDir != "" {
		t.Fatalf("ETOC sentinel = (%d, %q), want (0, \"\")", sentStamp, sentDir)
	}

	added := a2.Member("GUI/Opening100.ftx")
	if added == nil {
		t.Fatal("added member missing")
	}
	if added.Compressed() || added.FileSize != int64(len(payload)) || added.ExtractSize != int64(len(payload)) {
		t.Fatalf("added member sizes = %d/%d compressed=%v", added.FileSize, added.ExtractSize, added.Compressed())
	}
	if got, err := a2.Data(added); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("added member payload mismatch (err %v)", err)
	}
	if added.Offset%a2.Header.Align != 0 {
		t.Fatalf("added member offset %d is not aligned to %d", added.Offset, a2.Header.Align)
	}
	// GUI/Opening100.ftx sorts after Debug and before Help; in this fixture the
	// lower-case "root.bin" sorts last, so GUI is row 0.
	if added.ID != 0 || a2.tocTable.numRows < 1 {
		t.Fatalf("added member ID = %d, want 0 (first path-sorted row)", added.ID)
	}
}
func TestRebuildVariablePreservesCompressedSiblings(t *testing.T) {
	s1, p1 := compressedTestMember(0x22, 700)
	s2, p2 := compressedTestMember(0x77, 300)
	members := []testMember{
		{dir: "", name: "plain", stored: []byte("plain-payload"), extract: 13},
		{dir: "X", name: "one", stored: s1, extract: int64(len(p1))},
		{dir: "X", name: "two", stored: s2, extract: int64(len(p2))},
	}
	blob := buildTestCPK(t, members)
	a, err := Open(bytes.NewReader(blob), int64(len(blob)))
	if err != nil {
		t.Fatal(err)
	}
	rawBefore, dataBefore := cloneEntryData(t, a)
	if err := a.ReplaceMemberResized("plain", []byte("plain-payload-plus"), 17); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := a.RebuildVariable(&buf); err != nil {
		t.Fatalf("RebuildVariable: %v", err)
	}
	a2, err := Open(bytes.NewReader(buf.Bytes()), int64(len(buf.Bytes())))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"X/one", "X/two"} {
		e := a2.Member(name)
		if !e.Compressed() {
			t.Fatalf("%s should still be compressed", name)
		}
		if r, _ := a2.RawData(e); !bytes.Equal(r, rawBefore[name]) {
			t.Fatalf("%s raw bytes changed", name)
		}
		if d, _ := a2.Data(e); !bytes.Equal(d, dataBefore[name]) {
			t.Fatalf("%s decompressed bytes changed", name)
		}
	}
}
