package titlefooter

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"sync"

	"uo-unburger/internal/assets"
)

// pixel is one RGBA texel. The footer is white type with a dark outline on a
// transparent field.
type pixel struct{ R, G, B, A uint8 }

const (
	// canvasWidth is the reading direction of the rotated credit strip.
	canvasWidth = footerH // 204
	// canvasHeight is the glyph height of the rotated credit strip.
	canvasHeight = footerW // 40
	// minFillMargin keeps the glyph fill away from the strip edge. The outline
	// may extend past this margin (and is clipped at the strip boundary); only
	// the visible glyphs must fit.
	minFillMargin = 0.5
	// supersample is the fixed coverage grid per output pixel. Eight samples per
	// axis give 65 coverage levels per pixel, enough that the fill edge and the
	// outline edge are both represented smoothly after BC7 quantisation.
	supersample = 8

	// stripWidth and stripHeight are the credit strip's dimensions.
	stripWidth  = footerW
	stripHeight = footerH
)

var (
	footerWhite = pixel{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF}
	footerDark  = pixel{R: 0x0C, G: 0x0C, B: 0x0E}
	footerClear = pixel{}

	// white and transparent are the palette aliases used by the BC7 codec and
	// its tests. The rendered mark also contains antialiased intermediate tones.
	white       = footerWhite
	transparent = footerClear
)

var (
	fontOnce sync.Once
	fontData *ttfFont
	fontErr  error
)

func embeddedFont() (*ttfFont, error) {
	fontOnce.Do(func() {
		fontData, fontErr = parseTTF(assets.FontTTF)
	})
	return fontData, fontErr
}

// FontSHA256 returns the lowercase SHA-256 of the embedded typeface bytes.
//
// The digest is derived from the asset actually compiled into the binary; it is
// not compared against a hard-coded allowlist. Replacing internal/assets/font.ttf
// with another valid supported TrueType face therefore changes the build-report
// identity (and invalidates a cached run) instead of being rejected merely for
// having a different hash. The replacement still requires a Go rebuild because
// the font is embedded with //go:embed.
func FontSHA256() string {
	sum := sha256.Sum256(assets.FontTTF)
	return hex.EncodeToString(sum[:])
}

// normalizeVersion trims surrounding whitespace from a version string and
// collapses interior whitespace runs.
func normalizeVersion(v string) string {
	out := make([]rune, 0, len(v))
	for _, r := range v {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			if len(out) == 0 {
				continue
			}
			if out[len(out)-1] == ' ' {
				continue
			}
			out = append(out, ' ')
			continue
		}
		out = append(out, r)
	}
	for len(out) > 0 && out[len(out)-1] == ' ' {
		out = out[:len(out)-1]
	}
	return string(out)
}

// FooterLines returns the two footer lines for a version, in reading order.
// The first line is fixed; the second carries the version verbatim.
func FooterLines(version string) ([]string, error) {
	v := normalizeVersion(version)
	if v == "" {
		return nil, ErrVersionMissing
	}
	return []string{"UNBURGER MOD", v}, nil
}

// FooterText returns the footer as one space-separated string, e.g.
// "UNBURGER MOD 0.2.1-test". It is the exact text drawn on the title screen,
// wrapped onto two centred lines to fit the credit strip's proportions.
func FooterText(version string) (string, error) {
	lines, err := FooterLines(version)
	if err != nil {
		return "", err
	}
	return lines[0] + " " + lines[1], nil
}

// lineMetrics describes one footer line in font units.
type lineMetrics struct {
	runes  []rune
	glyphs []*ttfGlyph
	pens   []float64
	width  float64
	height float64
	yMin   float64
	yMax   float64
	xMin   float64
}

func measureLine(font *ttfFont, text string) (*lineMetrics, error) {
	lm := &lineMetrics{runes: []rune(text)}
	pen := 0.0
	first := true
	for _, r := range lm.runes {
		g, err := font.glyph(r)
		if err != nil {
			return nil, err
		}
		lm.glyphs = append(lm.glyphs, g)
		lm.pens = append(lm.pens, pen)
		if !g.IsEmpty {
			if first {
				lm.xMin = pen + g.XMin
				lm.yMin, lm.yMax = g.YMin, g.YMax
				first = false
			} else {
				lm.xMin = math.Min(lm.xMin, pen+g.XMin)
				lm.yMin = math.Min(lm.yMin, g.YMin)
				lm.yMax = math.Max(lm.yMax, g.YMax)
			}
			lm.width = math.Max(lm.width, pen+g.XMax)
		}
		pen += g.Advance
	}
	if first {
		return nil, fmt.Errorf("titlefooter: text %q has no visible glyphs", text)
	}
	lm.width -= lm.xMin
	lm.height = lm.yMax - lm.yMin
	if lm.width <= 0 || lm.height <= 0 {
		return nil, fmt.Errorf("titlefooter: text %q has an empty outline", text)
	}
	return lm, nil
}

// layoutMark lays out both footer lines at the configured type size and returns
// the glyph polylines plus the type size in pixels per em.
//
// The two line ink boxes are packed at their measured heights with line_spacing
// between them, and the whole block is centred vertically in the 204x40 canvas.
// Each line is centred horizontally on its own rendered width rather than on a
// shared left edge, so a short version line and the fixed title line both sit in
// the middle of the strip. offset_x/offset_y then move the laid-out mark. The
// visible glyph fill must stay inside the strip; the dark outline may reach the
// boundary and is clipped there, which is harmless (it only trims the outer
// halo). A configuration whose glyphs do not fit is a hard error, never a silent
// rescale.
func layoutMark(version string, cfg Config) ([][][]ttfPoint, float64, error) {
	font, err := embeddedFont()
	if err != nil {
		return nil, 0, err
	}
	return layoutMarkWithFont(version, cfg, font)
}

// layoutMarkWithFont is the explicit-typeface form used to prove that another
// valid supported TrueType face can replace the embedded one.
func layoutMarkWithFont(version string, cfg Config, font *ttfFont) ([][][]ttfPoint, float64, error) {
	if err := cfg.Validate(); err != nil {
		return nil, 0, err
	}
	lines, err := FooterLines(version)
	if err != nil {
		return nil, 0, err
	}
	metrics := make([]*lineMetrics, len(lines))
	var totalHeight float64
	for i, line := range lines {
		lm, err := measureLine(font, line)
		if err != nil {
			return nil, 0, err
		}
		metrics[i] = lm
		totalHeight += lm.height
	}
	if totalHeight <= 0 {
		return nil, 0, fmt.Errorf("titlefooter: text has an empty outline")
	}

	scale := cfg.FontSize / font.unitsPerEm
	blockHeight := totalHeight*scale + cfg.LineSpacing*float64(len(lines)-1)
	top := (float64(canvasHeight)-blockHeight)/2 + cfg.OffsetY

	polys := make([][][]ttfPoint, 0, len(lines))
	y := top
	for _, lm := range metrics {
		baseline := y + lm.yMax*scale
		// Each line is centred on its own rendered ink bounds. A shared left
		// anchor would centre only the wider line and leave the other one
		// visibly off-centre; offset_x is applied after centring.
		originX := (float64(canvasWidth)-lm.width*scale)/2 + cfg.OffsetX - lm.xMin*scale
		var linePolys [][]ttfPoint
		for i, g := range lm.glyphs {
			linePolys = append(linePolys, glyphPolygons(g, scale, originX+lm.pens[i]*scale, baseline)...)
		}
		polys = append(polys, linePolys)
		y += lm.height*scale + cfg.LineSpacing
	}

	// Fail closed if the visible fill escapes the strip. This is the bounds
	// check that makes a mistyped font_size or offset a build error.
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, line := range polys {
		for _, poly := range line {
			for _, p := range poly {
				minX = math.Min(minX, p.X)
				minY = math.Min(minY, p.Y)
				maxX = math.Max(maxX, p.X)
				maxY = math.Max(maxY, p.Y)
			}
		}
	}
	if minX < minFillMargin || maxX > float64(canvasWidth)-minFillMargin ||
		minY < minFillMargin || maxY > float64(canvasHeight)-minFillMargin {
		return nil, 0, fmt.Errorf(
			"titlefooter: typography font_size=%g outline_size=%g line_spacing=%g offset=(%g,%g) places the glyphs at x %.2f..%.2f y %.2f..%.2f outside the %dx%d strip",
			cfg.FontSize, cfg.OutlineSize, cfg.LineSpacing, cfg.OffsetX, cfg.OffsetY,
			minX, maxX, minY, maxY, canvasWidth, canvasHeight)
	}
	return polys, cfg.FontSize, nil
}

// renderStrip draws the footer into the atlas-oriented strip (40 wide by 204
// tall) and returns the chosen font size in pixels per em.
//
// The mark is drawn on a horizontal 204x40 canvas and rotated a quarter turn
// counter-clockwise, which is the orientation the title screen expects: turning
// the atlas a quarter turn clockwise restores upright text.
func renderStrip(version string, cfg Config) ([]pixel, int, error) {
	return renderStripAt(version, cfg, supersample)
}

// renderStripAt is the explicit-coverage-grid form used by quality tests. The
// layout, rotation and bounds are identical at every grid; only the coverage
// estimate changes.
func renderStripAt(version string, cfg Config, ss int) ([]pixel, int, error) {
	lines, size, err := layoutMark(version, cfg)
	if err != nil {
		return nil, 0, err
	}
	canvas := rasterizeMarkAt(lines, cfg.OutlineSize, ss)
	strip := make([]pixel, footerW*footerH)
	for y := 0; y < footerH; y++ {
		for x := 0; x < footerW; x++ {
			strip[y*footerW+x] = canvas[x*canvasWidth+(canvasWidth-1-y)]
		}
	}
	return strip, int(math.Round(size)), nil
}

// rasterizeMark builds the coverage mask for every line at a fixed supersample
// grid, dilates it into the dark outline, and composites both shapes into RGBA.
//
// The fill and the outline are opaque shapes. For an output pixel let f be the
// fraction of its supersamples inside the fill and o the fraction inside the
// dilated outline (the outline contains the fill, so f <= o). The covered part
// of the pixel is white where it is fill and dark where it is outline only, so
// the straight-alpha result is
//
//	A = o
//	C = (f*white + (o-f)*dark) / o
//
// This is area-weighted compositing: a fully covered white texel stays exactly
// white, a fully covered outline texel stays exactly dark, and the one-pixel
// transition between them carries the true subpixel coverage instead of a hard
// 50% fill cut. Only the coverage grid changes; the white fill itself is never
// blurred or tinted beyond that geometric blend.
func rasterizeMark(lines [][][]ttfPoint, outlineSize float64) []pixel {
	return rasterizeMarkAt(lines, outlineSize, supersample)
}

// rasterizeMarkAt is the explicit-supersample form used by tests to compare
// coverage grids. Production always uses supersample.
func rasterizeMarkAt(lines [][][]ttfPoint, outlineSize float64, ss int) []pixel {
	sw, sh := canvasWidth*ss, canvasHeight*ss
	fill := make([]bool, sw*sh)
	for _, polys := range lines {
		if len(polys) == 0 {
			continue
		}
		minX, minY := math.Inf(1), math.Inf(1)
		maxX, maxY := math.Inf(-1), math.Inf(-1)
		for _, poly := range polys {
			for _, p := range poly {
				minX = math.Min(minX, p.X)
				minY = math.Min(minY, p.Y)
				maxX = math.Max(maxX, p.X)
				maxY = math.Max(maxY, p.Y)
			}
		}
		x0 := int(math.Floor(minX * float64(ss)))
		x1 := int(math.Ceil(maxX * float64(ss)))
		y0 := int(math.Floor(minY * float64(ss)))
		y1 := int(math.Ceil(maxY * float64(ss)))
		if x0 < 0 {
			x0 = 0
		}
		if y0 < 0 {
			y0 = 0
		}
		if x1 > sw {
			x1 = sw
		}
		if y1 > sh {
			y1 = sh
		}
		for sy := y0; sy < y1; sy++ {
			y := (float64(sy) + 0.5) / float64(ss)
			for sx := x0; sx < x1; sx++ {
				x := (float64(sx) + 0.5) / float64(ss)
				if windingAt(polys, x, y) {
					fill[sy*sw+sx] = true
				}
			}
		}
	}

	// The outline is the fill dilated by a disk of outlineSize pixels. Computing
	// the exact Euclidean distance to the nearest fill sample once is much
	// cheaper than convolving every sample with a disk kernel, and it keeps the
	// 8x8 grid affordable.
	dist2 := fillDistanceSquared(fill, sw, sh)
	radius := outlineSize * float64(ss)
	r2 := radius * radius

	out := make([]pixel, canvasWidth*canvasHeight)
	samples := float64(ss * ss)
	for py := 0; py < canvasHeight; py++ {
		for px := 0; px < canvasWidth; px++ {
			fillCount, outlineCount := 0, 0
			row := py * ss * sw
			for dy := 0; dy < ss; dy++ {
				idx := row + dy*sw + px*ss
				for dx := 0; dx < ss; dx++ {
					if fill[idx] {
						fillCount++
					}
					if dist2[idx] <= r2 {
						outlineCount++
					}
					idx++
				}
			}
			if outlineCount == 0 {
				out[py*canvasWidth+px] = footerClear
				continue
			}
			alpha := uint8(math.Round(float64(outlineCount) / samples * 255))
			if fillCount == outlineCount {
				// Fully covered by fill: exact, untinted white.
				out[py*canvasWidth+px] = pixel{R: 0xFF, G: 0xFF, B: 0xFF, A: alpha}
				continue
			}
			// Composite white fill over the dark outline-only area using the
			// covered-area fractions. fillCount == 0 reduces exactly to footerDark.
			cover := float64(fillCount) / float64(outlineCount)
			out[py*canvasWidth+px] = pixel{
				R: uint8(math.Round(cover*255 + (1-cover)*float64(footerDark.R))),
				G: uint8(math.Round(cover*255 + (1-cover)*float64(footerDark.G))),
				B: uint8(math.Round(cover*255 + (1-cover)*float64(footerDark.B))),
				A: alpha,
			}
		}
	}
	return out
}

// fillDistanceSquared returns the squared Euclidean distance, in supersamples,
// from every grid cell to the nearest fill cell. Cells outside the canvas are
// not fill, which clips the outline at the strip boundary exactly as before.
// The implementation is the exact Felzenszwalb-Huttenlocher distance transform,
// so it is deterministic and linear in the number of samples.
func fillDistanceSquared(fill []bool, sw, sh int) []float64 {
	const far = 1e9
	d := make([]float64, len(fill))
	for i, on := range fill {
		if on {
			d[i] = 0
		} else {
			d[i] = far
		}
	}
	// Column pass: transform each column independently.
	col := make([]float64, sh)
	for x := 0; x < sw; x++ {
		for y := 0; y < sh; y++ {
			col[y] = d[y*sw+x]
		}
		transform1D(col)
		for y := 0; y < sh; y++ {
			d[y*sw+x] = col[y]
		}
	}
	// Row pass: transform each row independently.
	row := make([]float64, sw)
	for y := 0; y < sh; y++ {
		base := y * sw
		copy(row, d[base:base+sw])
		transform1D(row)
		copy(d[base:base+sw], row)
	}
	return d
}

// transform1D applies the lower-envelope algorithm to one line of squared
// distances. f[i] is the initial cost of choosing column i; on return d[q] is
// min_i f[i] + (q-i)^2.
func transform1D(f []float64) {
	n := len(f)
	if n == 0 {
		return
	}
	d := make([]float64, n)
	v := make([]int, n)
	z := make([]float64, n+1)
	k := 0
	v[0] = 0
	z[0] = math.Inf(-1)
	z[1] = math.Inf(1)
	for q := 1; q < n; q++ {
		s := ((f[q] + float64(q*q)) - (f[v[k]] + float64(v[k]*v[k]))) / (2 * float64(q-v[k]))
		for s <= z[k] {
			k--
			s = ((f[q] + float64(q*q)) - (f[v[k]] + float64(v[k]*v[k]))) / (2 * float64(q-v[k]))
		}
		k++
		v[k] = q
		z[k] = s
		z[k+1] = math.Inf(1)
	}
	k = 0
	for q := 0; q < n; q++ {
		for z[k+1] < float64(q) {
			k++
		}
		dq := float64(q - v[k])
		d[q] = dq*dq + f[v[k]]
	}
	copy(f, d)
}

// footerDescription is used by tests and reports to describe the rendered mark.
func footerDescription(version string) (string, error) {
	lines, err := FooterLines(version)
	if err != nil {
		return "", err
	}
	return strings.Join(lines, " / "), nil
}
