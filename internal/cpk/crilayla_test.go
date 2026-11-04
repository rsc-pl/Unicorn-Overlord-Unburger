package cpk

import (
	"bytes"
	"testing"
)

// encodeLiteralCRILAYLA builds a CRILAYLA stream that only uses literal (flag 0)
// tokens. The header field stores the number of bytes following the 256-byte
// initial block, and the decoder emits those bytes from the end of the output
// backwards, so the decoded tail is the reverse of the input list.
func encodeLiteralCRILAYLA(header [256]byte, literals []byte) []byte {
	usz := len(literals)

	var bits []byte
	for _, b := range literals {
		bits = append(bits, 0)
		for j := 7; j >= 0; j-- {
			bits = append(bits, (b>>uint(j))&1)
		}
	}
	return packCRILAYLA(header, usz, bits)
}

// encodeBackRefCRILAYLA builds a stream whose decoded output after the initial
// block is n copies of x, using four literal seed bytes followed by a single
// long back-reference. It exercises the decoder's back-reference path.
func encodeBackRefCRILAYLA(header [256]byte, x byte, n int) []byte {
	if n < 4 {
		panic("encodeBackRefCRILAYLA requires n >= 4")
	}
	var bits []byte
	for i := 0; i < 4; i++ {
		bits = append(bits, 0)
		for j := 7; j >= 0; j-- {
			bits = append(bits, (x>>uint(j))&1)
		}
	}
	bits = append(bits, 1)         // back-reference flag
	bits = appendBits(bits, 0, 13) // offset delta 0
	bits = append(bits, encodeVLE(n-7)...)
	return packCRILAYLA(header, n, bits)
}

// encodeVLE encodes the extra copy length (total minus the decoder's base 3)
// using the decoder's variable-length scheme.
func encodeVLE(extra int) []byte {
	widths := []int{2, 3, 5, 8}
	maxes := []int{3, 7, 31, 255}
	var bits []byte
	i := 0
	for extra > 0 {
		var w, m int
		if i < len(widths) {
			w, m = widths[i], maxes[i]
		} else {
			w, m = 8, 255
		}
		v := extra
		if v > m {
			v = m
		}
		bits = appendBits(bits, v, w)
		extra -= v
		i++
		if extra == 0 && v == m {
			// The decoder continues whenever it reads a full-width value.
			nw := 8
			if i < len(widths) {
				nw = widths[i]
			}
			bits = appendBits(bits, 0, nw)
		}
	}
	return bits
}

func appendBits(dst []byte, v, width int) []byte {
	for j := width - 1; j >= 0; j-- {
		dst = append(dst, byte((v>>uint(j))&1))
	}
	return dst
}

// packCRILAYLA packs a consumed-bit sequence into the trailing read order of a
// CRILAYLA stream (bits are consumed from the highest address, MSB first).
func packCRILAYLA(header [256]byte, usz int, bits []byte) []byte {
	nBytes := (len(bits) + 7) / 8
	packed := make([]byte, nBytes)
	for i, bit := range bits {
		if bit != 0 {
			packed[i/8] |= 1 << uint(7-i%8)
		}
	}
	mem := make([]byte, nBytes)
	for i := range packed {
		mem[i] = packed[nBytes-1-i]
	}

	out := make([]byte, 0, 0x120+len(mem)+0x100)
	out = append(out, "CRILAYLA"...)
	out = appendLE32(out, uint32(usz))
	out = appendLE32(out, 0x10) // header offset
	out = append(out, make([]byte, 16)...)
	out = append(out, header[:]...)
	out = append(out, mem...)
	out = append(out, make([]byte, 0x100)...)
	return out
}

func appendLE32(dst []byte, v uint32) []byte {
	return append(dst, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
}

func TestDecompressLiteralVector(t *testing.T) {
	var header [256]byte
	for i := range header {
		header[i] = byte(i)
	}
	literals := []byte("Hello, CRILAYLA!\x00\xff\x80\x7f")
	stream := encodeLiteralCRILAYLA(header, literals)

	got, err := Decompress(stream)
	if err != nil {
		t.Fatalf("Decompress: %v", err)
	}
	reversed := make([]byte, len(literals))
	for i, b := range literals {
		reversed[len(literals)-1-i] = b
	}
	want := append(append([]byte(nil), header[:]...), reversed...)
	if !bytes.Equal(got, want) {
		t.Fatalf("decompressed mismatch:\n got %d bytes\nwant %d bytes", len(got), len(want))
	}
}

func TestDecompressBackReference(t *testing.T) {
	var header [256]byte
	for i := range header {
		header[i] = byte(250 - i)
	}
	const n = 600
	stream := encodeBackRefCRILAYLA(header, 0x41, n)
	if len(stream) >= 0x100+n {
		t.Fatalf("back-reference vector did not compress: %d bytes", len(stream))
	}
	got, err := Decompress(stream)
	if err != nil {
		t.Fatalf("Decompress: %v", err)
	}
	want := append(append([]byte(nil), header[:]...), bytes.Repeat([]byte{0x41}, n)...)
	if !bytes.Equal(got, want) {
		t.Fatalf("back-reference decompress mismatch (got %d bytes)", len(got))
	}
}

func TestDecompressHeaderOnly(t *testing.T) {
	var header [256]byte
	for i := range header {
		header[i] = byte(255 - i)
	}
	stream := encodeLiteralCRILAYLA(header, nil)
	got, err := Decompress(stream)
	if err != nil {
		t.Fatalf("Decompress: %v", err)
	}
	if !bytes.Equal(got, header[:]) {
		t.Fatalf("header-only decompress mismatch")
	}
}

func TestDecompressRejectsForeignData(t *testing.T) {
	if _, err := Decompress([]byte("not a crilayla stream at all")); err == nil {
		t.Fatal("expected error for non-CRILAYLA input")
	}
	if _, err := Decompress([]byte("CRILAYLA")); err == nil {
		t.Fatal("expected error for truncated CRILAYLA input")
	}
}

func TestDecompressTruncatedStream(t *testing.T) {
	var header [256]byte
	// Enough literals that the bit reader needs more bits than the stream holds
	// once the trailing guard region is removed.
	stream := encodeLiteralCRILAYLA(header, bytes.Repeat([]byte{0x5A}, 40))
	truncated := stream[:0x120]
	if _, err := Decompress(truncated); err == nil {
		t.Fatal("expected truncation error")
	}
}

func TestCRIXorMaskRoundTrip(t *testing.T) {
	src := []byte("The quick brown fox jumps over the lazy dog.")
	enc := criXorMask(src)
	if len(enc) != len(src) {
		t.Fatalf("length changed: %d -> %d", len(src), len(enc))
	}
	// The first keystream byte is the low byte of the seed 0x0000655f.
	if enc[0] != src[0]^0x5f {
		t.Fatalf("first keystream byte: got 0x%02x", enc[0]^src[0])
	}
	if bytes.Equal(enc, src) {
		t.Fatal("mask had no effect")
	}
	if back := criXorMask(enc); !bytes.Equal(back, src) {
		t.Fatal("mask is not self-inverse")
	}
}

func TestAddOffset(t *testing.T) {
	const unknown = int64(-1) // 0xFFFFFFFFFFFFFFFF as signed
	cases := []struct {
		toc, content, want int64
	}{
		{0x800, 0x1800, 0x800},
		{0x1800, 0x800, 0x800},
		{unknown, 0x800, 0x800},
		{0x800, unknown, 0x800},
		{unknown, unknown, 0},
	}
	for _, c := range cases {
		if got := addOffset(c.toc, c.content); got != c.want {
			t.Errorf("addOffset(%#x, %#x) = %#x, want %#x", c.toc, c.content, got, c.want)
		}
	}
}

func TestAlignUp(t *testing.T) {
	cases := []struct{ v, a, want int64 }{
		{0, 2048, 0},
		{1, 2048, 2048},
		{2048, 2048, 2048},
		{2049, 2048, 4096},
		{100, 1, 100},
		{100, 0, 100},
	}
	for _, c := range cases {
		if got := alignUp(c.v, c.a); got != c.want {
			t.Errorf("alignUp(%d,%d) = %d, want %d", c.v, c.a, got, c.want)
		}
	}
}
