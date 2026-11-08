package titlefooter

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// buildFTX returns a minimal synthetic FTEX atlas with one 2048x2048 BC7
// surface. It is a test fixture only: the credit strip and every other texel
// start transparent, so the test can prove exactly which blocks Apply writes.
func buildFTX(t *testing.T, width, height int) []byte {
	t.Helper()
	const (
		surfaceTag = 320
		nameBytes  = 64
	)
	tex := surfaceTag + 8 + nameBytes
	payload := tex + 0x100
	blocks := ((width + 3) / 4) * ((height + 3) / 4)
	buf := make([]byte, payload+blocks*blockSize)
	copy(buf[:4], "FTEX")
	copy(buf[surfaceTag:surfaceTag+4], "FTX0")
	copy(buf[tex:tex+4], ".tex")
	words := []uint32{
		bc7Format, 1, uint32(width), uint32(height),
		1, 1, 0x100, uint32(width * height),
	}
	for i, w := range words {
		binary.LittleEndian.PutUint32(buf[tex+4+i*4:], w)
	}
	return buf
}

func TestFooterText(t *testing.T) {
	lines, err := FooterLines(" 0.2.1-test\n")
	if err != nil {
		t.Fatalf("FooterLines: %v", err)
	}
	if len(lines) != 2 || lines[0] != "UNBURGER MOD" || lines[1] != "0.2.1-test" {
		t.Fatalf("lines = %q", lines)
	}
	text, err := FooterText("0.2.1-test")
	if err != nil {
		t.Fatal(err)
	}
	if text != "UNBURGER MOD 0.2.1-test" {
		t.Fatalf("text = %q", text)
	}
	if _, err := FooterText("   "); err != ErrVersionMissing {
		t.Fatalf("empty version error = %v", err)
	}
}

func TestApplyConfinesEditAndDecodesWithinTolerance(t *testing.T) {
	src := buildFTX(t, 2048, 2048)
	out, proof, err := Apply(src, "0.2.1-test")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if proof.Surface != 0 || proof.X != footerX || proof.Y != footerY || proof.W != footerW || proof.H != footerH {
		t.Fatalf("proof region = %+v", proof)
	}
	if proof.Blocks != 510 || proof.ChangedBlocks == 0 || proof.ChangedBytes == 0 || !proof.OutsideUnchanged {
		t.Fatalf("proof = %+v", proof)
	}
	if proof.MaxChannelError > maxEncodedChannelError {
		t.Fatalf("max channel error = %d, want <= %d", proof.MaxChannelError, maxEncodedChannelError)
	}

	atlas, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	tex := atlas.Textures[0]
	widthBlocks := blocksWide(tex.Width)
	allowed := func(off int) bool {
		for by := footerY / 4; by < (footerY+footerH)/4; by++ {
			for bx := footerX / 4; bx < (footerX+footerW)/4; bx++ {
				start := tex.PayloadOffset + addrBlockLinear(bx, by, widthBlocks, blockHeightBlocks)
				if off >= start && off < start+blockSize {
					return true
				}
			}
		}
		return false
	}
	for i := range src {
		if src[i] == out[i] {
			continue
		}
		if i < tex.PayloadOffset || i >= tex.PayloadOffset+tex.PayloadSize || !allowed(i) {
			t.Fatalf("byte %d outside the credit strip changed", i)
		}
	}

	// Every written block must decode to within the documented tolerance of the
	// intended strip, and the mark must exercise white fill, dark outline and
	// antialiased outline alpha.
	strip, scale, err := renderStrip("0.2.1-test", *mustEmbeddedConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	if scale < 20 {
		t.Fatalf("font size = %d px, want a legible mark at least 20 px tall", scale)
	}
	seenWhite, seenDark, seenAA := 0, 0, 0
	maxDelta := 0
	for by := footerY / 4; by < (footerY+footerH)/4; by++ {
		for bx := footerX / 4; bx < (footerX+footerW)/4; bx++ {
			pos := tex.PayloadOffset + addrBlockLinear(bx, by, widthBlocks, blockHeightBlocks)
			var got [16]pixel
			decodeBC7Block(out[pos:pos+blockSize], &got)
			for i := 0; i < 4; i++ {
				for j := 0; j < 4; j++ {
					ax := bx*4 + j - footerX
					ay := by*4 + i - footerY
					want := strip[ay*stripWidth+ax]
					g := got[i*4+j]
					diffs := []int{absDiff(g.A, want.A)}
					if want.A != 0 {
						diffs = append(diffs, absDiff(g.R, want.R), absDiff(g.G, want.G), absDiff(g.B, want.B))
					}
					for _, d := range diffs {
						if d > maxDelta {
							maxDelta = d
						}
					}
					if want == white {
						seenWhite++
					}
					if want.A == 255 && want.R == footerDark.R && want.G == footerDark.G && want.B == footerDark.B {
						seenDark++
					}
					if want.A > 0 && want.A < 255 {
						seenAA++
					}
				}
			}
		}
	}
	if maxDelta > maxEncodedChannelError {
		t.Fatalf("decoded channel error = %d, want <= %d", maxDelta, maxEncodedChannelError)
	}
	if seenWhite == 0 || seenDark == 0 || seenAA == 0 {
		t.Fatalf("mark coverage: white=%d dark=%d antialiased=%d", seenWhite, seenDark, seenAA)
	}
}

// TestParseRejectsTruncatedSurfaceTag is the regression for a malformed FTEX
// whose final "FTX0" marker sits inside the last seven bytes of the file. The
// parser used to slice data[off+8:] past the end of the buffer and panic;
// malformed input must return a validation error instead.
func TestParseRejectsTruncatedSurfaceTag(t *testing.T) {
	for _, data := range [][]byte{
		append([]byte("FTEX"), []byte("FTX0")...),
		append([]byte("FTEX123"), []byte("FTX0")...),
	} {
		if _, err := Parse(data); err == nil {
			t.Fatalf("Parse accepted a truncated surface tag: %x", data)
		}
	}
}

// TestApplyRejectsUncoveredGOBLayout is the regression for a surface whose
// declared dimensions need more block-linear GOB padding than the file holds.
// The old naive payload-size check accepted it and Apply then indexed past the
// buffer; it must now fail as a validation error.
func TestApplyRejectsUncoveredGOBLayout(t *testing.T) {
	src := buildFTX(t, 2000, 864)
	if _, _, err := Apply(src, "0.2.1-test"); err == nil {
		t.Fatal("a surface whose GOB layout exceeds its payload must be rejected")
	}
}

func TestApplyRejectsUneditableInput(t *testing.T) {
	if _, _, err := Apply([]byte("not a texture at all"), "0.2.1-test"); err != ErrNotFTX {
		t.Fatalf("non-FTX error = %v", err)
	}
	src := buildFTX(t, 2048, 2048)
	if _, _, err := Apply(src, ""); err != ErrVersionMissing {
		t.Fatalf("empty version error = %v", err)
	}
	short := buildFTX(t, 2048, 2048)
	short = short[:len(short)-16]
	if _, _, err := Apply(short, "1.0"); err == nil {
		t.Fatal("truncated payload must be rejected")
	}
}

func TestApplyVersionChangesFooterBytes(t *testing.T) {
	src := buildFTX(t, 2048, 2048)
	a, _, err := Apply(src, "0.2.1-test")
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := Apply(src, "0.2.1-test-2")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("a different version must change the rendered footer")
	}
}

func TestBC7Mode6RoundTripsBinaryArt(t *testing.T) {
	patterns := []func(i int) pixel{
		func(int) pixel { return transparent },
		func(int) pixel { return white },
		func(i int) pixel {
			if i%2 == 0 {
				return white
			}
			return transparent
		},
		func(i int) pixel {
			// The first texel is white, which forces the encoder to swap the
			// endpoints so the fix-up index stays in 0..7.
			if i == 0 {
				return white
			}
			return transparent
		},
		func(i int) pixel {
			if i == 0 {
				return transparent
			}
			return white
		},
	}
	for n, pattern := range patterns {
		var pixels [16]pixel
		for i := range pixels {
			pixels[i] = pattern(i)
		}
		block := encodeBC7Mode6(pixels)
		var got [16]pixel
		decodeBC7Mode6(block[:], &got)
		if got != pixels {
			t.Fatalf("pattern %d did not round-trip: got %v want %v", n, got, pixels)
		}
	}
}
