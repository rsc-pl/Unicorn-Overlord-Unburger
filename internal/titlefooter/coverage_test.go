package titlefooter

import (
	"testing"
)

// encodeDecodeStrip runs the production BC7 block codec over a rendered strip
// and returns the texels the game would actually decode. The strip is 40x204,
// so every 4x4 block is block-aligned.
func encodeDecodeStrip(strip []pixel) []pixel {
	out := make([]pixel, len(strip))
	for by := 0; by < footerH/4; by++ {
		for bx := 0; bx < footerW/4; bx++ {
			var px [16]pixel
			for i := 0; i < 4; i++ {
				for j := 0; j < 4; j++ {
					px[i*4+j] = strip[(by*4+i)*stripWidth+bx*4+j]
				}
			}
			block := encodeBC7Block(px)
			var got [16]pixel
			decodeBC7Block(block[:], &got)
			for i := 0; i < 4; i++ {
				for j := 0; j < 4; j++ {
					out[(by*4+i)*stripWidth+bx*4+j] = got[i*4+j]
				}
			}
		}
	}
	return out
}

func distinctAlpha(pixels []pixel, partialOnly bool) map[uint8]bool {
	seen := map[uint8]bool{}
	for _, p := range pixels {
		if p.A == 0 {
			continue
		}
		if partialOnly && p.A == 255 {
			continue
		}
		seen[p.A] = true
	}
	return seen
}

// TestRasterizeCoverageCompositing checks the area-weighted compositing model:
// white fill and dark outline remain exact at full coverage, partially covered
// texels carry fractional alpha, and every visible texel lies on the geometric
// white-to-dark line (R == G, B within two levels of R). This is the property
// the old 50% fill cut violated: it discarded all subpixel white coverage.
func TestRasterizeCoverageCompositing(t *testing.T) {
	cfg := *mustEmbeddedConfig(t)
	strip, _, err := renderStrip("0.2.1-test", cfg)
	if err != nil {
		t.Fatalf("renderStrip: %v", err)
	}
	var whiteCount, darkCount, partial int
	for i, p := range strip {
		switch {
		case p == white:
			whiteCount++
		case p.A == 255 && p.R == footerDark.R && p.G == footerDark.G && p.B == footerDark.B:
			darkCount++
		}
		if p.A > 0 && p.A < 255 {
			partial++
		}
		if p.A == 0 {
			continue
		}
		if p.R != p.G {
			t.Fatalf("texel %d has off-line colour %v (R != G)", i, p)
		}
		if p.B < p.R || p.B-p.R > 3 {
			t.Fatalf("texel %d has off-line colour %v (B-R outside 0..3)", i, p)
		}
	}
	if whiteCount < 100 || darkCount < 100 || partial < 100 {
		t.Fatalf("coverage classes: white=%d dark=%d partial=%d", whiteCount, darkCount, partial)
	}
	if len(distinctAlpha(strip, true)) < 8 {
		t.Fatalf("partial-alpha levels = %d, want a real coverage ramp at %dx%d supersampling",
			len(distinctAlpha(strip, true)), supersample, supersample)
	}
}

// TestCoverageGridImprovesBC7DecodedEdges is the quantitative regression for the
// visual fix. It compares the 4x4 and 8x8 renders after they have been through
// the real BC7 encoder and decoder, using a 16x16 render as the high-coverage
// reference. The 8x8 grid must track the reference more closely, keep alpha
// error bounded, and preserve fully covered white texels exactly.
func TestCoverageGridImprovesBC7DecodedEdges(t *testing.T) {
	cfg := *mustEmbeddedConfig(t)
	ref, _, err := renderStripAt("0.2.1-test", cfg, 16)
	if err != nil {
		t.Fatalf("reference render: %v", err)
	}
	type metrics struct {
		meanAlpha, meanColour     float64
		maxAlphaRef, maxAlphaSelf int
		distinct                  int
	}
	measure := func(ss int) (metrics, []pixel) {
		strip, _, err := renderStripAt("0.2.1-test", cfg, ss)
		if err != nil {
			t.Fatalf("render %dx: %v", ss, err)
		}
		decoded := encodeDecodeStrip(strip)
		var m metrics
		var sumAlpha, sumColour int
		visible := 0
		for i, r := range ref {
			if r.A == 0 {
				continue
			}
			visible++
			g := decoded[i]
			da := absDiff(g.A, r.A)
			sumAlpha += da
			if da > m.maxAlphaRef {
				m.maxAlphaRef = da
			}
			sumColour += absDiff(g.R, r.R) + absDiff(g.G, r.G) + absDiff(g.B, r.B)
			if self := absDiff(g.A, strip[i].A); self > m.maxAlphaSelf {
				m.maxAlphaSelf = self
			}
		}
		m.meanAlpha = float64(sumAlpha) / float64(visible)
		m.meanColour = float64(sumColour) / float64(visible)
		m.distinct = len(distinctAlpha(decoded, true))
		return m, strip
	}

	four, _ := measure(4)
	eight, eightStrip := measure(8)
	t.Logf("4x: mean alpha %.4f mean colour %.4f max ref alpha %d self alpha %d distinct alpha %d",
		four.meanAlpha, four.meanColour, four.maxAlphaRef, four.maxAlphaSelf, four.distinct)
	t.Logf("8x: mean alpha %.4f mean colour %.4f max ref alpha %d self alpha %d distinct alpha %d",
		eight.meanAlpha, eight.meanColour, eight.maxAlphaRef, eight.maxAlphaSelf, eight.distinct)

	if !(eight.meanAlpha < four.meanAlpha*0.75) {
		t.Fatalf("8x mean alpha error %.4f did not improve enough over 4x %.4f",
			eight.meanAlpha, four.meanAlpha)
	}
	if !(eight.meanColour < four.meanColour) {
		t.Fatalf("8x mean colour error %.4f did not improve over 4x %.4f",
			eight.meanColour, four.meanColour)
	}
	if eight.maxAlphaSelf > 24 {
		t.Fatalf("8x max alpha error against its own coverage = %d, want <= 24", eight.maxAlphaSelf)
	}
	// The number of distinct alpha values is font- and edge-shape-dependent, so
	// it is logged rather than asserted; the mean-error comparison above is the
	// font-independent regression.

	// Fully covered white lettering must survive the codec untinted.
	decoded := encodeDecodeStrip(eightStrip)
	whiteTexels := 0
	for i, p := range eightStrip {
		if p != white {
			continue
		}
		whiteTexels++
		if g := decoded[i]; g.R != 255 || g.G != 255 || g.B != 255 || g.A != 255 {
			t.Fatalf("white texel %d encoded as %v", i, g)
		}
	}
	if whiteTexels < 100 {
		t.Fatalf("white texels = %d, want a legible white interior", whiteTexels)
	}
}

// TestRasterizeIsDeterministic pins the renderer's byte-for-byte stability: the
// same layout and coverage grid must always produce the same strip.
func TestRasterizeIsDeterministic(t *testing.T) {
	cfg := *mustEmbeddedConfig(t)
	a, _, err := renderStrip("0.2.1-test", cfg)
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := renderStrip("0.2.1-test", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != len(b) {
		t.Fatalf("strip lengths differ: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("strip differs at texel %d: %v vs %v", i, a[i], b[i])
		}
	}
}
