// Package nso implements a minimal, self-contained reader/writer for Nintendo
// Switch NSO0 executable images (the `main` member of an ExeFS).
//
// It supports what this project needs for research and offline validation:
//
//   - Parse an NSO0 header (version, flags, module id, three segments).
//   - Decompress a segment: LZ4 raw-block when the compressed flag is set,
//     stored bytes otherwise.
//   - Verify a segment against the SHA-256 recorded in the header.
//   - Re-encode an image after replacing one or more segments, refreshing
//     segment file offsets, compressed sizes and SHA-256 hashes.
//
// The package is deliberately stdlib-only (crypto/sha256, encoding/binary) and
// has no knowledge of any real game file. All tests use synthetic fixtures.
//
// Header layout used here (all fields little-endian uint32 unless noted):
//
//	0x00 magic "NSO0"      0x04 version        0x08 reserved      0x0C flags
//	0x10 text.fileOffset   0x14 text.memoryOffset
//	0x18 text.decompressedSize 0x1C text.memorySize
//	0x20 rodata.fileOffset 0x24 rodata.memoryOffset
//	0x28 rodata.decompressedSize 0x2C rodata.memorySize
//	0x30 data.fileOffset   0x34 data.memoryOffset
//	0x38 data.decompressedSize 0x3C data.memorySize
//	0x40 moduleID[0x20]
//	0x60 text.compressedSize 0x64 rodata.compressedSize 0x68 data.compressedSize
//	0x6C reserved
//	0xA0 text SHA-256      0xC0 rodata SHA-256      0xE0 data SHA-256
//
// Every other header byte is preserved verbatim by Encode, so an image that is
// re-encoded from unchanged segments is byte-identical to its input.
package nso

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
)

// Magic is the NSO0 file signature.
const Magic = "NSO0"

// HeaderSize is the fixed NSO0 header length (the segment table ends at 0x100).
// It is exported because an Atmosphère exefs_patches IPS is offset relative to
// the uncompressed NSO file, which begins with exactly this header.
const HeaderSize = 0x100

// headerSize is retained for the package's internal call sites.
const headerSize = HeaderSize

// FlagCompressed is the header flag bit that marks the segments LZ4-compressed.
const FlagCompressed = 1

// Name identifies one of the three segments.
type Name string

// The three NSO segments.
const (
	Text   Name = "text"
	Rodata Name = "rodata"
	Data   Name = "data"
)

// segField gives the byte offsets of one segment's header fields.
type segField struct {
	name         Name
	fileOffset   int
	memoryOffset int
	decompressed int
	memorySize   int
	compressedSz int
	hash         int
}

var segFields = []segField{
	{Text, 0x10, 0x14, 0x18, 0x1C, 0x60, 0xA0},
	{Rodata, 0x20, 0x24, 0x28, 0x2C, 0x64, 0xC0},
	{Data, 0x30, 0x34, 0x38, 0x3C, 0x68, 0xE0},
}

// Segment describes one NSO segment as recorded in the header.
type Segment struct {
	Name             Name
	FileOffset       uint32
	MemoryOffset     uint32
	DecompressedSize uint32
	MemorySize       uint32
	CompressedSize   uint32
	Hash             [32]byte
}

// File is a parsed NSO0 image. The original bytes are retained so that
// segments which are not replaced can be copied verbatim.
type File struct {
	Version  uint32
	Flags    uint32
	ModuleID [32]byte
	Text     Segment
	Rodata   Segment
	Data     Segment

	raw []byte
}

// Segment returns the named segment.
func (f *File) Segment(n Name) *Segment {
	switch n {
	case Text:
		return &f.Text
	case Rodata:
		return &f.Rodata
	case Data:
		return &f.Data
	}
	return nil
}

// Compressed reports whether the header marks the segments compressed.
func (f *File) Compressed() bool { return f.Flags&FlagCompressed != 0 }

// Parse validates and decodes an NSO0 header.
func Parse(data []byte) (*File, error) {
	if len(data) < headerSize {
		return nil, fmt.Errorf("nso: image too small (%d bytes, need at least %d)", len(data), headerSize)
	}
	if string(data[:4]) != Magic {
		return nil, fmt.Errorf("nso: bad magic %q", string(data[:4]))
	}
	f := &File{
		Version: binary.LittleEndian.Uint32(data[4:8]),
		Flags:   binary.LittleEndian.Uint32(data[0xC:0x10]),
		raw:     append([]byte(nil), data...),
	}
	copy(f.ModuleID[:], data[0x40:0x60])
	for _, sf := range segFields {
		seg := f.Segment(sf.name)
		seg.FileOffset = binary.LittleEndian.Uint32(data[sf.fileOffset : sf.fileOffset+4])
		seg.MemoryOffset = binary.LittleEndian.Uint32(data[sf.memoryOffset : sf.memoryOffset+4])
		seg.DecompressedSize = binary.LittleEndian.Uint32(data[sf.decompressed : sf.decompressed+4])
		seg.MemorySize = binary.LittleEndian.Uint32(data[sf.memorySize : sf.memorySize+4])
		seg.CompressedSize = binary.LittleEndian.Uint32(data[sf.compressedSz : sf.compressedSz+4])
		copy(seg.Hash[:], data[sf.hash:sf.hash+32])
		if err := f.checkBounds(seg); err != nil {
			return nil, err
		}
	}
	return f, nil
}

func (f *File) checkBounds(s *Segment) error {
	if int64(s.FileOffset)+int64(s.CompressedSize) > int64(len(f.raw)) {
		return fmt.Errorf("nso: %s segment [%d,%d) is outside the image (%d bytes)",
			s.Name, s.FileOffset, uint64(s.FileOffset)+uint64(s.CompressedSize), len(f.raw))
	}
	return nil
}

// rawSegment returns the stored bytes of a segment (compressed or stored).
func (f *File) rawSegment(n Name) ([]byte, *Segment, error) {
	s := f.Segment(n)
	if s == nil {
		return nil, nil, fmt.Errorf("nso: unknown segment %q", n)
	}
	if err := f.checkBounds(s); err != nil {
		return nil, nil, err
	}
	return f.raw[s.FileOffset : uint64(s.FileOffset)+uint64(s.CompressedSize)], s, nil
}

// StoredSegment returns the bytes exactly as stored for a segment (LZ4 payload
// when the image is compressed, raw bytes otherwise). It is the identity test
// used to prove that a re-encode left an untouched segment byte-for-byte
// unchanged.
func (f *File) StoredSegment(n Name) ([]byte, error) {
	payload, _, err := f.rawSegment(n)
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), payload...), nil
}

// Decompress returns the decompressed bytes of a segment.
func (f *File) Decompress(n Name) ([]byte, error) {
	payload, s, err := f.rawSegment(n)
	if err != nil {
		return nil, err
	}
	if !f.Compressed() {
		if uint32(len(payload)) != s.DecompressedSize {
			return nil, fmt.Errorf("nso: %s stored size %d != header size %d",
				n, len(payload), s.DecompressedSize)
		}
		return append([]byte(nil), payload...), nil
	}
	return Decompress(payload, int(s.DecompressedSize))
}

// Verify decompresses every segment and compares it against the header SHA-256.
func (f *File) Verify() error {
	for _, sf := range segFields {
		blob, err := f.Decompress(sf.name)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(blob)
		seg := f.Segment(sf.name)
		if sum != seg.Hash {
			return fmt.Errorf("nso: %s SHA-256 mismatch: header %x, computed %x",
				sf.name, seg.Hash, sum)
		}
	}
	return nil
}

// MappedImage returns the module image a loader maps: the three decompressed
// segments at their memory offsets, with the gaps between them zero-filled.
// The NSO0 header is not part of the mapped image.
//
// This is the image an Atmosphère exefs_patches IPS is applied to, at
// `patch offset - nso.HeaderSize`. A patch that is offset relative to the
// *stored* (compressed) file cannot be used there: the compressed stream is a
// different, much smaller byte sequence.
func (f *File) MappedImage() ([]byte, error) {
	end := 0
	prevEnd := 0
	for _, sf := range segFields {
		s := f.Segment(sf.name)
		if int(s.MemoryOffset) < prevEnd {
			return nil, fmt.Errorf("nso: %s memory offset %#x overlaps the previous segment (ends %#x)",
				sf.name, s.MemoryOffset, prevEnd)
		}
		e := int(s.MemoryOffset) + int(s.DecompressedSize)
		prevEnd = e
		if e > end {
			end = e
		}
	}
	out := make([]byte, end)
	for _, sf := range segFields {
		blob, err := f.Decompress(sf.name)
		if err != nil {
			return nil, err
		}
		copy(out[int(f.Segment(sf.name).MemoryOffset):], blob)
	}
	return out, nil
}

// Encode re-emits an NSO0 image. Segments present in replace are re-compressed
// (or stored verbatim when the compressed flag is clear); all other segments
// are copied byte-for-byte from the source image. Offsets, compressed sizes and
// per-segment SHA-256 hashes are refreshed and every other header byte is
// preserved. Segment order in the output is always text, rodata, data.
func (f *File) Encode(replace map[Name][]byte) ([]byte, error) {
	order := []Name{Text, Rodata, Data}
	blobs := make(map[Name][]byte, 3)
	payload := make(map[Name][]byte, 3)
	sizes := make(map[Name]uint32, 3)

	for _, n := range order {
		if b, ok := replace[n]; ok {
			blobs[n] = b
			sizes[n] = uint32(len(b))
			if f.Compressed() {
				payload[n] = Compress(b)
			} else {
				payload[n] = append([]byte(nil), b...)
			}
			continue
		}
		raw, seg, err := f.rawSegment(n)
		if err != nil {
			return nil, err
		}
		blob, err := f.Decompress(n)
		if err != nil {
			return nil, err
		}
		blobs[n] = blob
		sizes[n] = seg.DecompressedSize
		payload[n] = append([]byte(nil), raw...)
	}

	// Payloads stay contiguous, starting at the source's first segment offset
	// (0x100 for the images this project handles) so unrelated layout bytes are
	// preserved. The header itself is copied verbatim, then patched.
	out := make([]byte, headerSize)
	copy(out, f.raw[:headerSize])
	start := f.Text.FileOffset
	if start < headerSize {
		start = headerSize
	}
	if start > headerSize {
		out = append(out, make([]byte, start-headerSize)...)
	}
	off := start
	for _, n := range order {
		p := payload[n]
		sf := fieldFor(n)
		binary.LittleEndian.PutUint32(out[sf.fileOffset:], off)
		binary.LittleEndian.PutUint32(out[sf.decompressed:], sizes[n])
		binary.LittleEndian.PutUint32(out[sf.compressedSz:], uint32(len(p)))
		sum := sha256.Sum256(blobs[n])
		copy(out[sf.hash:sf.hash+32], sum[:])
		out = append(out, p...)
		off += uint32(len(p))
	}
	if uint32(len(out)) != off {
		return nil, fmt.Errorf("nso: internal size mismatch (%d != %d)", len(out), off)
	}
	return out, nil
}

func fieldFor(n Name) segField {
	for _, sf := range segFields {
		if sf.name == n {
			return sf
		}
	}
	return segField{}
}

// ErrCorrupt is wrapped by Decompress for malformed LZ4 input.
var ErrCorrupt = errors.New("nso: corrupt LZ4 block")

// Decompress decodes an LZ4 raw block (the format used by NSO segments) and
// checks that the result is exactly dsize bytes.
func Decompress(src []byte, dsize int) ([]byte, error) {
	if dsize < 0 {
		return nil, fmt.Errorf("nso: negative decompressed size %d", dsize)
	}
	dst := make([]byte, 0, dsize)
	i, n := 0, len(src)
	for i < n {
		tok := src[i]
		i++
		ll := int(tok >> 4)
		if ll == 15 {
			for {
				if i >= n {
					return nil, fmt.Errorf("%w: truncated literal length", ErrCorrupt)
				}
				b := src[i]
				i++
				ll += int(b)
				if b != 255 {
					break
				}
			}
		}
		if ll > 0 {
			if i+ll > n {
				return nil, fmt.Errorf("%w: literal run overruns input", ErrCorrupt)
			}
			dst = append(dst, src[i:i+ll]...)
			i += ll
		}
		if i >= n {
			break // last sequence: literals only
		}
		if i+2 > n {
			return nil, fmt.Errorf("%w: truncated match offset", ErrCorrupt)
		}
		off := int(src[i]) | int(src[i+1])<<8
		i += 2
		ml := int(tok & 0xF)
		if ml == 15 {
			for {
				if i >= n {
					return nil, fmt.Errorf("%w: truncated match length", ErrCorrupt)
				}
				b := src[i]
				i++
				ml += int(b)
				if b != 255 {
					break
				}
			}
		}
		ml += 4
		if off == 0 || off > len(dst) {
			return nil, fmt.Errorf("%w: bad match offset %d at %d", ErrCorrupt, off, len(dst))
		}
		start := len(dst) - off
		if off >= ml {
			dst = append(dst, dst[start:start+ml]...)
		} else {
			for k := 0; k < ml; k++ {
				dst = append(dst, dst[start+k%off])
			}
		}
		if len(dst) > dsize {
			return nil, fmt.Errorf("%w: output overruns declared size %d", ErrCorrupt, dsize)
		}
	}
	if len(dst) != dsize {
		return nil, fmt.Errorf("%w: size mismatch %d != %d", ErrCorrupt, len(dst), dsize)
	}
	return dst, nil
}

// match4 reports whether the four bytes at a and b are equal.
func match4(src []byte, a, b int) bool {
	return src[a] == src[b] && src[a+1] == src[b+1] && src[a+2] == src[b+2] && src[a+3] == src[b+3]
}

// Compress encodes src as a single LZ4 raw block using a greedy hash-table
// matcher. It always produces a block that Decompress accepts and that decodes
// back to src byte-for-byte (unit-tested), which is what the offline validation
// requires; compression ratio is secondary.
func Compress(src []byte) []byte {
	const (
		minMatch  = 4
		maxOffset = 65535
	)
	out := make([]byte, 0, len(src)+len(src)/255+16)
	// table maps a 4-byte sequence hash to the most recent position.
	table := make([]int, 1<<16)
	for i := range table {
		table[i] = -1
	}
	hash := func(i int) uint32 {
		v := uint32(src[i]) | uint32(src[i+1])<<8 | uint32(src[i+2])<<16 | uint32(src[i+3])<<24
		return (v * 2654435761) >> 16
	}
	emitLiterals := func(lit []byte) {
		ll := len(lit)
		if ll < 15 {
			out = append(out, byte(ll<<4))
		} else {
			out = append(out, 0xF0)
			rem := ll - 15
			for rem >= 255 {
				out = append(out, 255)
				rem -= 255
			}
			out = append(out, byte(rem))
		}
		out = append(out, lit...)
	}

	anchor := 0
	i := 0
	// The last 5 bytes must always be literals; the last match must start at
	// least 12 bytes before the end.
	limit := len(src) - 12
	for i <= limit {
		h := hash(i)
		cand := table[h]
		table[h] = i
		if cand >= 0 && i-cand <= maxOffset && match4(src, cand, i) {
			// extend the match
			ml := 4
			for i+ml < len(src)-5 && src[cand+ml] == src[i+ml] {
				ml++
			}
			// emit token + literals + offset
			lit := src[anchor:i]
			ll := len(lit)
			matchCode := ml - 4
			var tok byte
			switch {
			case ll < 15:
				tok = byte(ll << 4)
			default:
				tok = 0xF0
			}
			if matchCode < 15 {
				tok |= byte(matchCode)
			} else {
				tok |= 0x0F
			}
			out = append(out, tok)
			if ll >= 15 {
				rem := ll - 15
				for rem >= 255 {
					out = append(out, 255)
					rem -= 255
				}
				out = append(out, byte(rem))
			}
			out = append(out, lit...)
			off := i - cand
			out = append(out, byte(off), byte(off>>8))
			if matchCode >= 15 {
				rem := matchCode - 15
				for rem >= 255 {
					out = append(out, 255)
					rem -= 255
				}
				out = append(out, byte(rem))
			}
			i += ml
			anchor = i
			continue
		}
		i++
	}
	// final literal-only sequence
	emitLiterals(src[anchor:])
	return out
}
