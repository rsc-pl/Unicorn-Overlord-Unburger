package titlefooter

import (
	"encoding/binary"
	"testing"
)

// decodeBC7Mode4Reference is an independent transliteration of the published
// mode-4 decoder, written against the 64-bit chunk layout rather than the
// bit-reader used by the production decoder. The two must agree on every block
// the encoder produces; this guards against a layout mistake that would only
// show up on hardware.
func decodeBC7Mode4Reference(block []byte, out *[16]pixel) bool {
	*out = [16]pixel{}
	if len(block) < 16 {
		return false
	}
	low := binary.LittleEndian.Uint64(block[0:8])
	high := binary.LittleEndian.Uint64(block[8:16])

	// The encoder only emits mode 4. Verify the unary prefix explicitly.
	if low&0x1F != 0x10 {
		return false
	}
	compRot := int((low >> 5) & 3)
	indexMode := int((low >> 7) & 1)

	colorRead := low >> 8
	var ep [2][4]int
	for c := 0; c < 3; c++ {
		for e := 0; e < 2; e++ {
			ep[e][c] = int(colorRead & 31)
			colorRead >>= 5
		}
	}
	ep[0][3] = int(colorRead & 63)
	ep[1][3] = int((colorRead >> 6) & 63)

	rgbChunk := (low >> 50) | (high << 14)
	aChunk := high >> 17
	weightBits := [2]uint{2, 3}
	weightMask := [2]uint64{3, 7}
	if indexMode == 1 {
		rgbChunk, aChunk = aChunk, rgbChunk
		weightBits = [2]uint{3, 2}
		weightMask = [2]uint64{7, 3}
	}
	rgbChunk = insertFixupZero(rgbChunk, weightBits[0])
	aChunk = insertFixupZero(aChunk, weightBits[1])

	var weights, aWeights [16]int
	for i := 0; i < 16; i++ {
		weights[i] = int(rgbChunk & weightMask[0])
		rgbChunk >>= weightBits[0]
	}
	for i := 0; i < 16; i++ {
		aWeights[i] = int(aChunk & weightMask[1])
		aChunk >>= weightBits[1]
	}

	for e := 0; e < 2; e++ {
		for c := 0; c < 3; c++ {
			ep[e][c] = dequant(ep[e][c], 5)
		}
		ep[e][3] = dequant(ep[e][3], 6)
	}

	refWeights := [2][]int{bc7Weights2[:], bc7Weights3[:]}
	for i := range out {
		p := pixel{
			R: uint8(lerp64(ep[0][0], ep[1][0], refWeights[0][weights[i]])),
			G: uint8(lerp64(ep[0][1], ep[1][1], refWeights[0][weights[i]])),
			B: uint8(lerp64(ep[0][2], ep[1][2], refWeights[0][weights[i]])),
			A: uint8(lerp64(ep[0][3], ep[1][3], refWeights[1][aWeights[i]])),
		}
		if compRot >= 1 {
			ch := channelPtr(&p, compRot-1)
			p.A, *ch = *ch, p.A
		}
		out[i] = p
	}
	return true
}

// insertFixupZero inserts the implied zero MSB at the fix-up index, mirroring
// the reference decoder's bit-stream handling.
func insertFixupZero(v uint64, bits uint) uint64 {
	lowMask := (uint64(1) << (bits*1 - 1)) - 1
	return ((v &^ lowMask) << 1) | (v & lowMask)
}

func TestMode4MatchesReferenceDecoder(t *testing.T) {
	patterns := [][16]pixel{}
	// A block that combines white fill, opaque dark outline and partially
	// covered dark outline; it needs both a colour index and an alpha index.
	var mixed [16]pixel
	for i := range mixed {
		mixed[i] = pixel{R: footerDark.R, G: footerDark.G, B: footerDark.B, A: 255}
	}
	mixed[0] = pixel{R: footerDark.R, G: footerDark.G, B: footerDark.B, A: 143}
	mixed[15] = white
	patterns = append(patterns, mixed)

	// Opaque dark only, alpha ramp only, and a diagonal white/dark edge.
	var darkOnly [16]pixel
	var ramp [16]pixel
	var edge [16]pixel
	for i := range darkOnly {
		darkOnly[i] = pixel{R: footerDark.R, G: footerDark.G, B: footerDark.B, A: 255}
		ramp[i] = pixel{R: footerDark.R, G: footerDark.G, B: footerDark.B, A: uint8(i * 17)}
		if i%4 > i/4 {
			edge[i] = white
		} else {
			edge[i] = pixel{R: footerDark.R, G: footerDark.G, B: footerDark.B, A: 255}
		}
	}
	patterns = append(patterns, darkOnly, ramp, edge)

	strip, _, err := renderStrip("0.2.1-test", *mustEmbeddedConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	for by := footerY / 4; by < (footerY+footerH)/4; by++ {
		for bx := footerX / 4; bx < (footerX+footerW)/4; bx++ {
			var px [16]pixel
			for i := 0; i < 4; i++ {
				for j := 0; j < 4; j++ {
					px[i*4+j] = strip[(by*4+i-footerY)*stripWidth+(bx*4+j-footerX)]
				}
			}
			patterns = append(patterns, px)
		}
	}

	for n, px := range patterns {
		block := encodeBC7Mode4(px)
		var got, ref [16]pixel
		decodeBC7Mode4(block[:], &got)
		if !decodeBC7Mode4Reference(block[:], &ref) {
			t.Fatalf("pattern %d: reference decoder rejected block %x", n, block)
		}
		if got != ref {
			t.Fatalf("pattern %d: production decoder %v != reference %v", n, got, ref)
		}
	}
}
