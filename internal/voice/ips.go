package voice

import (
	"bytes"
	"fmt"

	"uo-unburger/internal/nso"
)

// IPS patch support.
//
// The classic IPS format is "PATCH" followed by records and the terminator
// "EOF". A record is a 3-byte big-endian offset, a 2-byte big-endian length and
// either `length` literal bytes, or — when length is 0 — a 2-byte big-endian
// run length and one byte to repeat. The IPS32 extension appends a 3-byte
// big-endian final file size after "EOF", which is how a patch expresses a
// change in image size.
//
// There are two different, incompatible layers, and mixing them silently
// produces a patch that either does nothing or corrupts the module:
//
//   - BuildIPS/ApplyIPS are generic: both arguments (or the applied image) are
//     the same byte image the patch is expressed against.
//   - BuildExefsIPS/ApplyExefsIPS are for Atmosphère's `exefs_patches` loader.
//     That loader applies an IPS to the *mapped module* — the decompressed
//     segments at their memory offsets — and its record offsets are relative to
//     the uncompressed NSO file, i.e. `mapped offset + nso.HeaderSize`; the
//     first 0x100 header bytes are protected and never patched.
//     (Atmosphère docs: the patch is made between the uncompressed NSO and the
//     modified uncompressed NSO, never between the compressed files.)
//
// A stored, LZ4-compressed NSO must therefore never be diffed into an
// exefs_patches IPS. This project's encoder re-encodes a changed segment with
// its own match finder, so the whole compressed .rodata stream differs from
// Nintendo's from an early offset on: the stored-file diff for a 13-byte edit
// is ~6.5 MB of literal data. The ExeFS `main` replacement is the primary
// deliverable (and the only form Eden consumes); the IPS is the small,
// Atmosphère-only expression of the same four words.

// BuildIPS encodes the difference between orig and patched as an IPS32 patch.
// Both images must be the same layer; a patch produced here is self-contained
// (it carries the final file size), so it both extends and truncates correctly.
//
// Do not call this with a stored (compressed) NSO to make an exefs_patches
// patch; use BuildExefsIPS.
func BuildIPS(orig, patched []byte) ([]byte, error) {
	if len(patched) > 0xFFFFFF {
		return nil, fmt.Errorf("voice: patched image is %d bytes; IPS offsets are limited to %d", len(patched), 0xFFFFFF)
	}
	out := []byte("PATCH")

	// changed reports whether patched differs from orig at i. A byte beyond the
	// original is "changed" only when it is not the zero the applier would pad;
	// bytes past the end of patched are deletions, expressed by the size record.
	changed := func(i int) bool {
		if i < len(orig) {
			return orig[i] != patched[i]
		}
		return patched[i] != 0
	}

	i := 0
	for i < len(patched) {
		if !changed(i) {
			i++
			continue
		}
		start := i
		for i < len(patched) && changed(i) {
			i++
		}
		if err := appendRun(&out, start, patched[start:i]); err != nil {
			return nil, err
		}
	}

	out = append(out, 'E', 'O', 'F')
	out = be24(out, uint32(len(patched)))
	return out, nil
}

// ExefsIPSPatch is a small IPS built for Atmosphère's exefs_patches loader.
// Bytes is the patch itself; the other fields describe what it contains.
type ExefsIPSPatch struct {
	Bytes   []byte
	Records int
	Changed int
	First   uint32
	Last    uint32
}

// BuildExefsIPS encodes orig -> patched as a classic IPS patch for Atmosphère's
// exefs_patches loader, from the two *stored* NSO images.
//
// Only bytes that differ in the mapped module image are emitted: the compressed
// streams are decompressed first, and the 0x100-byte NSO header (which changes
// when a segment is re-compressed) is excluded because the loader protects it.
// For the Japanese-voice plan this is four records and 16 payload bytes instead
// of a ~6.5 MB re-compressed-segment restatement.
func BuildExefsIPS(orig, patched []byte) (ExefsIPSPatch, error) {
	fo, err := nso.Parse(orig)
	if err != nil {
		return ExefsIPSPatch{}, fmt.Errorf("voice: parsing the original NSO: %w", err)
	}
	fp, err := nso.Parse(patched)
	if err != nil {
		return ExefsIPSPatch{}, fmt.Errorf("voice: parsing the patched NSO: %w", err)
	}
	if !bytes.Equal(fo.ModuleID[:], fp.ModuleID[:]) {
		return ExefsIPSPatch{}, fmt.Errorf("voice: the two images have different module ids (%x vs %x)",
			fo.ModuleID, fp.ModuleID)
	}
	mappedOrig, err := fo.MappedImage()
	if err != nil {
		return ExefsIPSPatch{}, fmt.Errorf("voice: original mapped image: %w", err)
	}
	mappedPatched, err := fp.MappedImage()
	if err != nil {
		return ExefsIPSPatch{}, fmt.Errorf("voice: patched mapped image: %w", err)
	}
	if len(mappedOrig) != len(mappedPatched) {
		return ExefsIPSPatch{}, fmt.Errorf("voice: mapped image size changed (%d -> %d); an ExeFS IPS is for same-size edits only",
			len(mappedOrig), len(mappedPatched))
	}

	out := []byte("PATCH")
	res := ExefsIPSPatch{}
	first, last := -1, -1
	i := 0
	for i < len(mappedPatched) {
		if mappedOrig[i] == mappedPatched[i] {
			i++
			continue
		}
		start := i
		for i < len(mappedPatched) && mappedOrig[i] != mappedPatched[i] {
			i++
		}
		// Exefs IPS offsets are relative to the uncompressed NSO file, which
		// starts with the 0x100-byte header the loader protects.
		if err := appendRun(&out, nso.HeaderSize+start, mappedPatched[start:i]); err != nil {
			return ExefsIPSPatch{}, err
		}
		res.Records++
		res.Changed += i - start
		if first < 0 {
			first = start
		}
		last = i
	}
	if res.Records == 0 {
		return ExefsIPSPatch{}, fmt.Errorf("voice: the images are identical; refusing to emit an empty ExeFS IPS")
	}
	out = append(out, 'E', 'O', 'F')
	res.Bytes = out
	res.First = uint32(nso.HeaderSize + first)
	res.Last = uint32(nso.HeaderSize + last)
	return res, nil
}

// ApplyExefsIPS applies patch to orig exactly the way Atmosphère's loader does
// and returns the resulting *mapped module image* (header excluded): record
// offsets are uncompressed-NSO file offsets, the 0x100-byte header is skipped,
// and writes are clipped to the mapped size.
func ApplyExefsIPS(orig, patch []byte) ([]byte, error) {
	f, err := nso.Parse(orig)
	if err != nil {
		return nil, fmt.Errorf("voice: parsing the NSO to patch: %w", err)
	}
	mapped, err := f.MappedImage()
	if err != nil {
		return nil, fmt.Errorf("voice: building the mapped image: %w", err)
	}
	recs, _, err := parseIPS(patch)
	if err != nil {
		return nil, err
	}
	for _, r := range recs {
		off, data := r.Offset, r.data()
		size := len(data)
		if off < nso.HeaderSize {
			if off+size <= nso.HeaderSize {
				continue
			}
			cut := nso.HeaderSize - off
			off += cut
			data = data[cut:]
			size -= cut
		}
		o := off - nso.HeaderSize
		if o >= len(mapped) {
			continue
		}
		if o+size > len(mapped) {
			size = len(mapped) - o
			data = data[:size]
		}
		copy(mapped[o:o+size], data)
	}
	return mapped, nil
}

// ipsRecord is one decoded IPS record.
type ipsRecord struct {
	// Offset is the position the record writes at, in the image the patch is
	// expressed against.
	Offset int
	// Literal holds the record's bytes; Value is used when RLE is true.
	Literal []byte
	Value   byte
	RLE     bool
	// Length is the number of bytes the record writes.
	Length int
}

func (r ipsRecord) data() []byte {
	if !r.RLE {
		return r.Literal
	}
	b := make([]byte, r.Length)
	for i := range b {
		b[i] = r.Value
	}
	return b
}

// parseIPS decodes every record and returns the bytes after "EOF" (the optional
// IPS32 size record, which not every applier honours).
func parseIPS(patch []byte) ([]ipsRecord, []byte, error) {
	if len(patch) < 8 || string(patch[:5]) != "PATCH" {
		return nil, nil, fmt.Errorf("voice: not an IPS patch (bad magic)")
	}
	var recs []ipsRecord
	i := 5
	for {
		if i+3 <= len(patch) && string(patch[i:i+3]) == "EOF" {
			i += 3
			break
		}
		if i+5 > len(patch) {
			return nil, nil, fmt.Errorf("voice: truncated IPS record at offset %d", i)
		}
		off := int(be24At(patch, i))
		i += 3
		length := int(be16At(patch, i))
		i += 2
		if length == 0 {
			if i+3 > len(patch) {
				return nil, nil, fmt.Errorf("voice: truncated IPS RLE record at offset %d", i)
			}
			rle := int(be16At(patch, i))
			i += 2
			recs = append(recs, ipsRecord{Offset: off, Value: patch[i], RLE: true, Length: rle})
			i++
			continue
		}
		if i+length > len(patch) {
			return nil, nil, fmt.Errorf("voice: IPS record at %#x overruns the patch", off)
		}
		recs = append(recs, ipsRecord{Offset: off, Literal: patch[i : i+length], Length: length})
		i += length
	}
	return recs, patch[i:], nil
}

// appendRun writes one contiguous changed run, splitting on the 0xFFFF record
// limit and switching to an RLE record for a long run of one repeated byte.
func appendRun(out *[]byte, start int, run []byte) error {
	for pos := 0; pos < len(run); {
		if start+pos > 0xFFFFFF {
			return fmt.Errorf("voice: IPS offset %d exceeds the 24-bit limit", start+pos)
		}
		chunk := run[pos:]
		if len(chunk) > 0xFFFF {
			chunk = chunk[:0xFFFF]
		}
		if n := repeatRun(chunk); n >= 8 {
			*out = be24(*out, uint32(start+pos))
			*out = append(*out, 0, 0) // RLE marker
			*out = be16(*out, uint16(n))
			*out = append(*out, chunk[0])
			pos += n
			continue
		}
		*out = be24(*out, uint32(start+pos))
		*out = be16(*out, uint16(len(chunk)))
		*out = append(*out, chunk...)
		pos += len(chunk)
	}
	return nil
}

// repeatRun returns the length of the leading run of one repeated byte, capped
// at the RLE record limit.
func repeatRun(b []byte) int {
	if len(b) == 0 {
		return 0
	}
	n := 1
	for n < len(b) && n < 0xFFFF && b[n] == b[0] {
		n++
	}
	return n
}

// ApplyIPS applies a generic IPS (with the optional IPS32 size record) to orig.
// The patch must have been built against the same layer as orig; an exefs_patches
// patch must go through ApplyExefsIPS instead.
func ApplyIPS(orig, patch []byte) ([]byte, error) {
	recs, trailer, err := parseIPS(patch)
	if err != nil {
		return nil, err
	}
	out := append([]byte(nil), orig...)
	ensure := func(n int) {
		if n > len(out) {
			out = append(out, make([]byte, n-len(out))...)
		}
	}
	for _, r := range recs {
		ensure(r.Offset + r.Length)
		copy(out[r.Offset:r.Offset+r.Length], r.data())
	}
	if len(trailer) == 3 {
		size := int(be24At(trailer, 0))
		if size < len(out) {
			out = out[:size]
		} else if size > len(out) {
			out = append(out, make([]byte, size-len(out))...)
		}
	}
	return out, nil
}

func be24(dst []byte, v uint32) []byte {
	return append(dst, byte(v>>16), byte(v>>8), byte(v))
}

func be16(dst []byte, v uint16) []byte {
	return append(dst, byte(v>>8), byte(v))
}

func be24At(b []byte, i int) uint32 {
	return uint32(b[i])<<16 | uint32(b[i+1])<<8 | uint32(b[i+2])
}

func be16At(b []byte, i int) uint16 {
	return uint16(b[i])<<8 | uint16(b[i+1])
}
