package nso

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"math/rand"
	"testing"
)

// buildFixture assembles a synthetic but format-exact NSO0 image from the three
// segment blobs. When compressed is true each segment is LZ4-compressed with the
// package Compress, otherwise it is stored verbatim. All offsets, sizes and
// hashes are written the way a real image records them.
func buildFixture(t *testing.T, text, rodata, data []byte, compressed bool) []byte {
	t.Helper()
	blobs := map[Name][]byte{Text: text, Rodata: rodata, Data: data}
	payload := map[Name][]byte{}
	for n, b := range blobs {
		if compressed {
			payload[n] = Compress(b)
		} else {
			payload[n] = b
		}
	}
	hdr := make([]byte, headerSize)
	copy(hdr[:4], Magic)
	binary.LittleEndian.PutUint32(hdr[4:], 0)
	flags := uint32(0)
	if compressed {
		flags = FlagCompressed
	}
	binary.LittleEndian.PutUint32(hdr[0xC:], flags)
	copy(hdr[0x40:0x60], bytes.Repeat([]byte{0xAB}, 32))

	mem := map[Name]uint32{Text: 0, Rodata: 0x1000, Data: 0x2000}
	off := uint32(headerSize)
	out := append([]byte(nil), hdr...)
	for _, n := range []Name{Text, Rodata, Data} {
		sf := fieldFor(n)
		p := payload[n]
		binary.LittleEndian.PutUint32(out[sf.fileOffset:], off)
		binary.LittleEndian.PutUint32(out[sf.memoryOffset:], mem[n])
		binary.LittleEndian.PutUint32(out[sf.decompressed:], uint32(len(blobs[n])))
		binary.LittleEndian.PutUint32(out[sf.compressedSz:], uint32(len(p)))
		sum := sha256.Sum256(blobs[n])
		copy(out[sf.hash:sf.hash+32], sum[:])
		out = append(out, p...)
		off += uint32(len(p))
	}
	return out
}

func syntheticSegments() (text, rodata, data []byte) {
	text = append([]byte("TEXT-CODE-"), bytes.Repeat([]byte{0x00, 0x01, 0x02, 0x03}, 64)...)
	rodata = append([]byte("VOICE_JP\000VOICE_US\000"), bytes.Repeat([]byte("lorem ipsum dolor sit amet "), 40)...)
	data = bytes.Repeat([]byte{0xDE, 0xAD, 0xBE, 0xEF}, 100)
	return
}

func TestCompressDecompressRoundtrip(t *testing.T) {
	cases := map[string][]byte{
		"empty":     {},
		"one":       {0x42},
		"five":      {1, 2, 3, 4, 5},
		"all-same":  bytes.Repeat([]byte{0x7F}, 4096),
		"text":      []byte("the quick brown fox jumps over the lazy dog. the quick brown fox jumps over the lazy dog."),
		"zeros":     make([]byte, 70000),
		"long-same": bytes.Repeat([]byte{0x41}, 200000),
	}
	// deterministic pseudo-random data
	rnd := rand.New(rand.NewSource(20261017))
	random := make([]byte, 65536)
	for i := range random {
		random[i] = byte(rnd.Intn(256))
	}
	cases["random"] = random

	for name, in := range cases {
		blocks := [][]byte{Compress(in)}
		// also add a second identical half to exercise long matches/offsets
		half := append(append([]byte(nil), in...), in...)
		blocks = append(blocks, Compress(half))
		for i, blk := range blocks {
			src := in
			if i == 1 {
				src = half
			}
			got, err := Decompress(blk, len(src))
			if err != nil {
				t.Fatalf("%s[%d]: Decompress: %v", name, i, err)
			}
			if !bytes.Equal(got, src) {
				t.Fatalf("%s[%d]: roundtrip mismatch (%d bytes)", name, i, len(src))
			}
		}
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	if _, err := Parse([]byte("NSO0")); err == nil {
		t.Fatal("want error for truncated header")
	}
	short := make([]byte, headerSize)
	copy(short, "ABCD")
	if _, err := Parse(short); err == nil {
		t.Fatal("want error for bad magic")
	}
	// valid header but segment pointing outside the image
	bad := make([]byte, headerSize)
	copy(bad, Magic)
	binary.LittleEndian.PutUint32(bad[0x10:], headerSize)
	binary.LittleEndian.PutUint32(bad[0x60:], 4096)
	if _, err := Parse(bad); err == nil {
		t.Fatal("want error for out-of-range segment")
	}
}

func TestParseAndVerify(t *testing.T) {
	text, rodata, data := syntheticSegments()
	for _, compressed := range []bool{true, false} {
		img := buildFixture(t, text, rodata, data, compressed)
		f, err := Parse(img)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if f.Compressed() != compressed {
			t.Fatalf("Compressed()=%v want %v", f.Compressed(), compressed)
		}
		if f.Segment(Rodata).DecompressedSize != uint32(len(rodata)) {
			t.Fatalf("rodata size %d want %d", f.Segment(Rodata).DecompressedSize, len(rodata))
		}
		if err := f.Verify(); err != nil {
			t.Fatalf("Verify: %v", err)
		}
		for n, want := range map[Name][]byte{Text: text, Rodata: rodata, Data: data} {
			got, err := f.Decompress(n)
			if err != nil {
				t.Fatalf("Decompress(%s): %v", n, err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("Decompress(%s) mismatch", n)
			}
		}
	}
}

func TestMappedImagePlacesSegmentsAtMemoryOffsets(t *testing.T) {
	text, rodata, data := syntheticSegments()
	for _, compressed := range []bool{true, false} {
		img := buildFixture(t, text, rodata, data, compressed)
		f, err := Parse(img)
		if err != nil {
			t.Fatal(err)
		}
		got, err := f.MappedImage()
		if err != nil {
			t.Fatalf("MappedImage: %v", err)
		}
		// The mapped image is the segments at their memory offsets, no header.
		if want := len(data) + 0x2000; len(got) != want {
			t.Fatalf("mapped size %d want %d", len(got), want)
		}
		if !bytes.Equal(got[:len(text)], text) {
			t.Fatal("text is not at mapped offset 0")
		}
		if !bytes.Equal(got[0x1000:0x1000+len(rodata)], rodata) {
			t.Fatal("rodata is not at its memory offset")
		}
		if !bytes.Equal(got[0x2000:0x2000+len(data)], data) {
			t.Fatal("data is not at its memory offset")
		}
		// Gaps between segments are zero-filled.
		for _, gap := range [][2]int{{len(text), 0x1000}, {0x1000 + len(rodata), 0x2000}} {
			for i := gap[0]; i < gap[1]; i++ {
				if got[i] != 0 {
					t.Fatalf("gap byte %#x is %#x, want 0", i, got[i])
				}
			}
		}
		// The header is not part of the mapped image.
		if bytes.Equal(got[:4], img[:4]) && string(got[:4]) == Magic {
			t.Fatal("mapped image starts with the NSO header")
		}
	}
}

func TestVerifyDetectsBadHash(t *testing.T) {
	text, rodata, data := syntheticSegments()
	img := buildFixture(t, text, rodata, data, true)
	f, err := Parse(img)
	if err != nil {
		t.Fatal(err)
	}
	f.Rodata.Hash[0] ^= 0xFF
	if err := f.Verify(); err == nil {
		t.Fatal("want hash mismatch error")
	}
}

func TestEncodePreservesUntouchedSegments(t *testing.T) {
	text, rodata, data := syntheticSegments()
	img := buildFixture(t, text, rodata, data, true)
	f, err := Parse(img)
	if err != nil {
		t.Fatal(err)
	}
	// Replace the rodata segment with a same-size edited copy.
	edited := append([]byte(nil), rodata...)
	copy(edited, []byte("VOICE_JP\000VOICE_JP\000"))
	out, err := f.Encode(map[Name][]byte{Rodata: edited})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	g, err := Parse(out)
	if err != nil {
		t.Fatalf("Parse(encoded): %v", err)
	}
	if err := g.Verify(); err != nil {
		t.Fatalf("Verify(encoded): %v", err)
	}
	// text and data payloads must be byte-identical to the source
	for _, n := range []Name{Text, Data} {
		orig, err := f.StoredSegment(n)
		if err != nil {
			t.Fatal(err)
		}
		now, err := g.StoredSegment(n)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(orig, now) {
			t.Fatalf("%s compressed payload changed", n)
		}
		oa, _ := f.Decompress(n)
		na, _ := g.Decompress(n)
		if !bytes.Equal(oa, na) {
			t.Fatalf("%s decompressed content changed", n)
		}
	}
	got, err := g.Decompress(Rodata)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, edited) {
		t.Fatal("rodata replacement not applied")
	}
	if g.Text.FileOffset != f.Text.FileOffset {
		t.Fatalf("text offset changed: %d -> %d", f.Text.FileOffset, g.Text.FileOffset)
	}
	if g.Rodata.FileOffset != f.Rodata.FileOffset {
		t.Fatalf("rodata offset changed: %d -> %d", f.Rodata.FileOffset, g.Rodata.FileOffset)
	}
	// data moved because rodata's compressed size changed
	if g.Data.FileOffset != g.Rodata.FileOffset+g.Rodata.CompressedSize {
		t.Fatalf("data offset is not contiguous with rodata")
	}
	// module id and every non-updated header field preserved
	if g.ModuleID != f.ModuleID {
		t.Fatal("module id changed")
	}
	if !bytes.Equal(out[:0x10], img[:0x10]) || !bytes.Equal(out[0x40:0x60], img[0x40:0x60]) {
		t.Fatal("unrelated header bytes changed")
	}
}

func TestEncodeIdentityIsByteStable(t *testing.T) {
	text, rodata, data := syntheticSegments()
	img := buildFixture(t, text, rodata, data, true)
	f, err := Parse(img)
	if err != nil {
		t.Fatal(err)
	}
	out, err := f.Encode(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, img) {
		t.Fatal("re-encoding an unmodified image is not byte-identical")
	}
}

func TestEncodeUncompressedImage(t *testing.T) {
	text, rodata, data := syntheticSegments()
	img := buildFixture(t, text, rodata, data, false)
	f, err := Parse(img)
	if err != nil {
		t.Fatal(err)
	}
	edited := append([]byte(nil), rodata...)
	edited[0] ^= 0xFF
	out, err := f.Encode(map[Name][]byte{Rodata: edited})
	if err != nil {
		t.Fatal(err)
	}
	g, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if g.Compressed() {
		t.Fatal("compressed flag set on an uncompressed re-encode")
	}
	if err := g.Verify(); err != nil {
		t.Fatal(err)
	}
	got, _ := g.Decompress(Rodata)
	if !bytes.Equal(got, edited) {
		t.Fatal("uncompressed replacement not applied")
	}
}

func TestDecompressRejectsCorruptLZ4(t *testing.T) {
	good := Compress(bytes.Repeat([]byte{0xAA}, 1000))
	// wrong declared size
	if _, err := Decompress(good, 999); err == nil {
		t.Fatal("want size mismatch error")
	}
	// offset past the start of the output
	bad := []byte{0x00, 0x10, 0x00, 0xFF}
	if _, err := Decompress(bad, 16); err == nil {
		t.Fatal("want bad-offset error")
	}
	// truncated literal run
	trunc := []byte{0xF0, 0xFF}
	if _, err := Decompress(trunc, 300); err == nil {
		t.Fatal("want truncated-literal error")
	}
}
