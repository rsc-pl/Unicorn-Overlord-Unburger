package cpk

import (
	"bytes"
	"strings"
	"testing"
)

func corruptionFixture(t *testing.T) []byte {
	t.Helper()
	return buildTestCPK(t, []testMember{
		{dir: "MsgSheet", name: "a.fms", stored: []byte("payload-a"), extract: 9},
		{dir: "Help", name: "b.bin", stored: []byte{1, 2, 3, 4}, extract: 4},
		{dir: "", name: "c.bin", stored: []byte("tail"), extract: 4},
	})
}

// TestOpenRejectsEveryTruncation cuts the archive at every boundary an attacker
// or an interrupted copy could produce. Open must always return an error and
// never read past the available bytes.
func TestOpenRejectsEveryTruncation(t *testing.T) {
	blob := corruptionFixture(t)
	cuts := []int{0, 1, 8, 15, 16, 4, 20, 32, 0x1ff, 0x200, 0x204, 0x300, 0x400, 0x401, len(blob) - 17, len(blob) - 1}
	for _, cut := range cuts {
		if cut < 0 || cut > len(blob) {
			continue
		}
		truncated := blob[:cut]
		if _, err := Open(bytes.NewReader(truncated), int64(len(truncated))); err == nil {
			t.Fatalf("truncation to %d bytes was accepted", cut)
		}
	}
}

// TestOpenRejectsCorruptPacketBodySize zeroes and inflates the header packet
// wrapper, which must be refused before any body read.
func TestOpenRejectsCorruptPacketBodySize(t *testing.T) {
	for _, size := range []uint64{0, 1, 3, 1 << 40} {
		blob := corruptionFixture(t)
		cpkLittleEndian.PutUint64(blob[8:16], size)
		if _, err := Open(bytes.NewReader(blob), int64(len(blob))); err == nil {
			t.Fatalf("header packet body size %d was accepted", size)
		}
	}
}

// TestOpenRejectsGarbagePacketBody replaces the (XOR-masked) header body with
// bytes that cannot parse as a UTF table.
func TestOpenRejectsGarbagePacketBody(t *testing.T) {
	blob := corruptionFixture(t)
	for i := 16; i < 64 && i < len(blob); i++ {
		blob[i] = 0x5A
	}
	if _, err := Open(bytes.NewReader(blob), int64(len(blob))); err == nil {
		t.Fatal("a garbage header body was accepted")
	}
}

// TestDataRejectsTruncatedCompressedPayload shrinks a compressed member's stored
// size so the CRILAYLA stream is cut short; Data must report a precise error for
// the member instead of returning a partial or wrong buffer.
func TestDataRejectsTruncatedCompressedPayload(t *testing.T) {
	stored, plain := compressedTestMember(0x41, 400)
	blob := buildTestCPK(t, []testMember{
		{dir: "MsgSheet", name: "comp.fms", stored: stored, extract: int64(len(plain))},
		{dir: "", name: "tail.bin", stored: []byte("tail"), extract: 4},
	})

	// Sanity: the intact fixture decompresses.
	a, err := Open(bytes.NewReader(blob), int64(len(blob)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got, err := a.Data(a.Member("MsgSheet/comp.fms")); err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("intact member: len=%d err=%v", len(got), err)
	}

	// Truncated stored size (still inside the content region).
	truncated := append([]byte(nil), blob...)
	patchTOCField(t, truncated, 0, "FileSize", uint64(len(stored)/2))
	ta, err := Open(bytes.NewReader(truncated), int64(len(truncated)))
	if err != nil {
		t.Fatalf("Open truncated-size archive: %v", err)
	}
	_, err = ta.Data(ta.Member("MsgSheet/comp.fms"))
	if err == nil {
		t.Fatal("a truncated compressed payload must not decompress silently")
	}
	if !strings.Contains(err.Error(), "comp.fms") {
		t.Fatalf("error must name the member: %v", err)
	}

	// Extract size inflated beyond what the stream produces.
	inflated := append([]byte(nil), blob...)
	patchTOCField(t, inflated, 0, "ExtractSize", uint64(len(plain)+64))
	ia, err := Open(bytes.NewReader(inflated), int64(len(inflated)))
	if err != nil {
		t.Fatalf("Open inflated-extract archive: %v", err)
	}
	if _, err := ia.Data(ia.Member("MsgSheet/comp.fms")); err == nil {
		t.Fatal("an inflated extract size must be detected")
	}
}

// TestDuplicateMemberPathsAreDeterministic documents the rule for a TOC with two
// identical paths (not produced by the real archives): Member returns the first
// entry in offset order and a resized replacement is staged on that first entry
// only. The behaviour is deterministic, but the duplicate is not rejected, so
// callers must not rely on it.
func TestDuplicateMemberPathsAreDeterministic(t *testing.T) {
	blob := buildTestCPK(t, []testMember{
		{dir: "", name: "dup.bin", stored: []byte("first"), extract: 5},
		{dir: "", name: "dup.bin", stored: []byte("second"), extract: 6},
	})
	a, err := Open(bytes.NewReader(blob), int64(len(blob)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if len(a.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(a.Entries))
	}
	if first := a.Member("dup.bin"); first != a.Entries[0] {
		t.Fatal("Member must return the first entry with the path")
	}
	if err := a.ReplaceMemberResized("dup.bin", []byte("replaced"), 8); err != nil {
		t.Fatalf("ReplaceMemberResized: %v", err)
	}
	var out bytes.Buffer
	if _, err := a.RebuildVariable(&out); err != nil {
		t.Fatalf("RebuildVariable: %v", err)
	}
	b, err := Open(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if len(b.Entries) != 2 {
		t.Fatalf("rebuilt entries = %d, want 2", len(b.Entries))
	}
	d0, err := b.Data(b.Entries[0])
	if err != nil {
		t.Fatalf("entry 0 data: %v", err)
	}
	if !bytes.Equal(d0, []byte("replaced")) {
		t.Fatalf("first duplicate = %q, want the staged payload", d0)
	}
	d1, err := b.Data(b.Entries[1])
	if err != nil {
		t.Fatalf("entry 1 data: %v", err)
	}
	if !bytes.Equal(d1, []byte("second")) {
		t.Fatalf("second duplicate changed: %q", d1)
	}
}
