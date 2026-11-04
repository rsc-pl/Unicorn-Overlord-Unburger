package voice

import "fmt"

// IPS patch support.
//
// The classic IPS format is "PATCH" followed by records and the terminator
// "EOF". A record is a 3-byte big-endian offset, a 2-byte big-endian length and
// either `length` literal bytes, or — when length is 0 — a 2-byte big-endian
// run length and one byte to repeat. The IPS32 extension appends a 3-byte
// big-endian final file size after "EOF", which is how a patch expresses a
// change in image size.
//
// A compressed NSO re-encodes its segments, so the byte difference between the
// stored source and the stored patched image is dominated by the re-compressed
// segment payload (~6 MiB for the Western update `main`). The ExeFS `main`
// replacement is the primary deliverable; BuildIPS exists so the same edit can
// also be expressed as a patch file, and it is validated against the exact
// bytes it was built from.

// BuildIPS encodes the difference between orig and patched as an IPS32 patch.
// A patch produced here is self-contained: it carries the final file size, so
// it both extends and truncates correctly.
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

// ApplyIPS applies an IPS (with the optional IPS32 size record) to orig.
func ApplyIPS(orig, patch []byte) ([]byte, error) {
	if len(patch) < 8 || string(patch[:5]) != "PATCH" {
		return nil, fmt.Errorf("voice: not an IPS patch (bad magic)")
	}
	out := append([]byte(nil), orig...)
	ensure := func(n int) {
		if n > len(out) {
			out = append(out, make([]byte, n-len(out))...)
		}
	}
	i := 5
	for {
		if i+3 <= len(patch) && string(patch[i:i+3]) == "EOF" {
			i += 3
			break
		}
		if i+5 > len(patch) {
			return nil, fmt.Errorf("voice: truncated IPS record at offset %d", i)
		}
		off := int(be24At(patch, i))
		i += 3
		length := int(be16At(patch, i))
		i += 2
		if length == 0 {
			if i+3 > len(patch) {
				return nil, fmt.Errorf("voice: truncated IPS RLE record at offset %d", i)
			}
			rle := int(be16At(patch, i))
			i += 2
			val := patch[i]
			i++
			ensure(off + rle)
			for k := 0; k < rle; k++ {
				out[off+k] = val
			}
			continue
		}
		if i+length > len(patch) {
			return nil, fmt.Errorf("voice: IPS record at %#x overruns the patch", off)
		}
		ensure(off + length)
		copy(out[off:], patch[i:i+length])
		i += length
	}
	if len(patch)-i == 3 {
		size := int(be24At(patch, i))
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
