package titlefooter

import (
	"math"
	"testing"
)

// lineBounds returns the ink bounds of one laid-out footer line in canvas
// coordinates. layoutMark returns flattened fill contours, so these bounds are
// the rendered glyph fill, not nominal font metrics.
func lineBounds(t *testing.T, polys [][]ttfPoint) (minX, minY, maxX, maxY float64) {
	t.Helper()
	if len(polys) == 0 {
		t.Fatal("laid-out line has no visible contours")
	}
	minX, minY = math.Inf(1), math.Inf(1)
	maxX, maxY = math.Inf(-1), math.Inf(-1)
	for _, poly := range polys {
		for _, p := range poly {
			minX = math.Min(minX, p.X)
			minY = math.Min(minY, p.Y)
			maxX = math.Max(maxX, p.X)
			maxY = math.Max(maxY, p.Y)
		}
	}
	return minX, minY, maxX, maxY
}

func assertNear(t *testing.T, name string, got, want float64) {
	t.Helper()
	const tolerance = 1e-6
	if math.Abs(got-want) > tolerance {
		t.Fatalf("%s = %.9f, want %.9f (tolerance %g)", name, got, want, tolerance)
	}
}

// TestLayoutCentresEachLineIndependently proves that both footer lines are
// centred on their own rendered ink width. A single shared left anchor would
// leave the shorter line visibly off-centre whenever the two lines differ in
// width, which they do ("UNBURGER MOD" versus a version string).
func TestLayoutCentresEachLineIndependently(t *testing.T) {
	cfg := *mustEmbeddedConfig(t)
	lines, _, err := layoutMark("0.2.1-test", cfg)
	if err != nil {
		t.Fatalf("layoutMark: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2", len(lines))
	}
	var widths [2]float64
	for i, polys := range lines {
		minX, _, maxX, _ := lineBounds(t, polys)
		widths[i] = maxX - minX
		assertNear(t, "line centre x", (minX+maxX)/2, float64(canvasWidth)/2+cfg.OffsetX)
	}
	if math.Abs(widths[0]-widths[1]) < 1 {
		t.Fatalf("test lines have nearly equal widths (%.3f, %.3f); centring is not exercised", widths[0], widths[1])
	}
}

// TestLayoutCentresDifferentVersionLengths repeats the centring check across
// short, medium and long version strings. The fixed title line must stay
// centred as well, independent of the version's width.
func TestLayoutCentresDifferentVersionLengths(t *testing.T) {
	cfg := *mustEmbeddedConfig(t)
	for _, version := range []string{"1", "1.2", "0.2.1-test", "1.2.3-rc1"} {
		lines, _, err := layoutMark(version, cfg)
		if err != nil {
			t.Fatalf("%s: layoutMark: %v", version, err)
		}
		for i, polys := range lines {
			minX, _, maxX, _ := lineBounds(t, polys)
			assertNear(t, version+" line centre x", (minX+maxX)/2, float64(canvasWidth)/2)
			if minX < minFillMargin || maxX > float64(canvasWidth)-minFillMargin {
				t.Fatalf("%s line %d fill x %.3f..%.3f escapes the strip", version, i, minX, maxX)
			}
		}
	}
}

// TestLayoutOffsetMovesBothLineCentres checks that offset_x is applied after
// centring, so a non-zero offset translates both independently centred lines by
// the same amount rather than re-anchoring one of them.
func TestLayoutOffsetMovesBothLineCentres(t *testing.T) {
	cfg := *mustEmbeddedConfig(t)
	for _, offset := range []float64{-3, 0, 2.5} {
		shifted := cfg
		shifted.OffsetX = offset
		lines, _, err := layoutMark("0.2.1-test", shifted)
		if err != nil {
			t.Fatalf("offset %g: layoutMark: %v", offset, err)
		}
		for _, polys := range lines {
			minX, _, maxX, _ := lineBounds(t, polys)
			assertNear(t, "offset line centre x", (minX+maxX)/2, float64(canvasWidth)/2+offset)
		}
	}
}

// TestLayoutKeepsBlockCentredAndLineSpacing checks the vertical arrangement:
// the two line ink boxes keep exactly line_spacing between them and the whole
// block remains vertically centred in the strip.
func TestLayoutKeepsBlockCentredAndLineSpacing(t *testing.T) {
	cfg := *mustEmbeddedConfig(t)
	lines, _, err := layoutMark("0.2.1-test", cfg)
	if err != nil {
		t.Fatalf("layoutMark: %v", err)
	}
	_, firstMinY, _, firstMaxY := lineBounds(t, lines[0])
	_, secondMinY, _, secondMaxY := lineBounds(t, lines[1])
	assertNear(t, "line gap", secondMinY-firstMaxY, cfg.LineSpacing)
	assertNear(t, "block centre y", (firstMinY+secondMaxY)/2, float64(canvasHeight)/2+cfg.OffsetY)

	// The visible fill must fit inside the strip with the documented margin; the
	// outline may touch the boundary and is clipped there.
	for i, polys := range lines {
		minX, minY, maxX, maxY := lineBounds(t, polys)
		if minX < minFillMargin || maxX > float64(canvasWidth)-minFillMargin ||
			minY < minFillMargin || maxY > float64(canvasHeight)-minFillMargin {
			t.Fatalf("line %d fill x %.3f..%.3f y %.3f..%.3f escapes the strip", i, minX, maxX, minY, maxY)
		}
	}
}

// TestRenderStripPaintsOnlyTheCreditStrip confirms the rendered output is the
// expected rotated 40x204 atlas strip and that white fill and dark outline are
// both present inside it.
func TestRenderStripPaintsOnlyTheCreditStrip(t *testing.T) {
	cfg := *mustEmbeddedConfig(t)
	strip, size, err := renderStrip("0.2.1-test", cfg)
	if err != nil {
		t.Fatalf("renderStrip: %v", err)
	}
	if size != 29 {
		t.Fatalf("font size = %d, want 29", size)
	}
	if len(strip) != footerW*footerH {
		t.Fatalf("strip length = %d, want %d", len(strip), footerW*footerH)
	}
	var whitePixels, darkPixels int
	for _, p := range strip {
		switch {
		case p == white:
			whitePixels++
		case p.A != 0:
			darkPixels++
		}
	}
	if whitePixels == 0 || darkPixels == 0 {
		t.Fatalf("rendered strip coverage: white=%d dark=%d", whitePixels, darkPixels)
	}
}
