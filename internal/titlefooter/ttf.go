package titlefooter

// A deliberately narrow TrueType reader and rasteriser.
//
// The footer is drawn from one embedded typeface. Only the tables needed to
// turn a character into filled outlines are read (cmap format 4, glyf/loca,
// head, hhea, hmtx, maxp). Composite glyphs are rejected rather than guessed,
// and a missing glyph is an error instead of a silent blank.
//
// Rasterisation is deterministic: quadratic curves are flattened by a fixed
// length rule, coverage is measured on a fixed 8x8 supersample grid, and the
// outline is a fixed-radius dilation of the fill mask. There is no randomness,
// no hinting and no platform text stack.

import (
	"encoding/binary"
	"fmt"
	"math"
)

// maxFontBytes bounds the embedded typeface. The production face is roughly
// 25 KiB; the limit keeps a mistaken or hostile replacement from making the
// builder parse an arbitrarily large table soup, while leaving ample room for
// another supported face.
const maxFontBytes = 16 << 20

type ttfTable struct{ off, length uint32 }

type ttfFont struct {
	data       []byte
	tables     map[string]ttfTable
	unitsPerEm float64
	indexToLoc int
	numGlyphs  int
	numHMetric int
	cmapSub    int
	glyphs     map[rune]*ttfGlyph
}

type ttfPoint struct {
	X, Y float64
	On   bool
}

// ttfGlyph is one simple glyph in font units plus its advance width.
type ttfGlyph struct {
	Points   []ttfPoint
	Ends     []int
	Advance  float64
	XMin     float64
	YMin     float64
	XMax     float64
	YMax     float64
	IsEmpty  bool
	Coverage float64 // advance width in font units; present for every glyph
}

func be16(b []byte, off int) (int, bool) {
	if off < 0 || off+2 > len(b) {
		return 0, false
	}
	return int(binary.BigEndian.Uint16(b[off:])), true
}

func be32(b []byte, off int) (int, bool) {
	if off < 0 || off+4 > len(b) {
		return 0, false
	}
	return int(binary.BigEndian.Uint32(b[off:])), true
}

// parseTTF validates the table directory and the few fixed fields the reader
// depends on. It does not decode glyphs until one is requested.
func parseTTF(data []byte) (*ttfFont, error) {
	if len(data) > maxFontBytes {
		return nil, fmt.Errorf("titlefooter: typeface is %d bytes, larger than the %d-byte limit", len(data), maxFontBytes)
	}
	if len(data) < 12 {
		return nil, fmt.Errorf("titlefooter: typeface is truncated")
	}
	if binary.BigEndian.Uint32(data) != 0x00010000 {
		return nil, fmt.Errorf("titlefooter: typeface is not a TrueType font")
	}
	n, _ := be16(data, 4)
	f := &ttfFont{data: data, tables: map[string]ttfTable{}, glyphs: map[rune]*ttfGlyph{}}
	for i := 0; i < n; i++ {
		rec := 12 + i*16
		if rec+16 > len(data) {
			return nil, fmt.Errorf("titlefooter: typeface table directory is truncated")
		}
		tag := string(data[rec : rec+4])
		off, _ := be32(data, rec+8)
		length, _ := be32(data, rec+12)
		if off < 0 || length < 0 || off > len(data) || length > len(data)-off {
			return nil, fmt.Errorf("titlefooter: typeface table %q is out of range", tag)
		}
		f.tables[tag] = ttfTable{off: uint32(off), length: uint32(length)}
	}
	head, ok := f.tables["head"]
	if !ok || head.length < 54 {
		return nil, fmt.Errorf("titlefooter: typeface has no usable head table")
	}
	maxp, ok := f.tables["maxp"]
	if !ok || maxp.length < 6 {
		return nil, fmt.Errorf("titlefooter: typeface has no usable maxp table")
	}
	hhea, ok := f.tables["hhea"]
	if !ok || hhea.length < 36 {
		return nil, fmt.Errorf("titlefooter: typeface has no usable hhea table")
	}
	if _, ok := f.tables["loca"]; !ok {
		return nil, fmt.Errorf("titlefooter: typeface has no loca table")
	}
	if _, ok := f.tables["glyf"]; !ok {
		return nil, fmt.Errorf("titlefooter: typeface has no glyf table")
	}
	if _, ok := f.tables["hmtx"]; !ok {
		return nil, fmt.Errorf("titlefooter: typeface has no hmtx table")
	}
	upem, _ := be16(data, int(head.off)+18)
	if upem <= 0 {
		return nil, fmt.Errorf("titlefooter: typeface has an invalid unitsPerEm")
	}
	f.unitsPerEm = float64(upem)
	ilf, _ := be16(data, int(head.off)+50)
	f.indexToLoc = ilf
	ng, _ := be16(data, int(maxp.off)+4)
	if ng <= 0 {
		return nil, fmt.Errorf("titlefooter: typeface has no glyphs")
	}
	f.numGlyphs = ng
	nh, _ := be16(data, int(hhea.off)+34)
	if nh <= 0 || nh > ng {
		return nil, fmt.Errorf("titlefooter: typeface has an invalid horizontal metric count")
	}
	f.numHMetric = nh
	sub, err := f.findCmap4()
	if err != nil {
		return nil, err
	}
	f.cmapSub = sub
	return f, nil
}

// findCmap4 locates a Unicode BMP format-4 subtable. The embedded typeface
// provides both Windows Unicode BMP and Macintosh Roman maps; the Unicode one is
// the correct source for the ASCII footer text.
func (f *ttfFont) findCmap4() (int, error) {
	t, ok := f.tables["cmap"]
	if !ok || t.length < 4 {
		return 0, fmt.Errorf("titlefooter: typeface has no cmap table")
	}
	base := int(t.off)
	n, _ := be16(f.data, base+2)
	for i := 0; i < n; i++ {
		rec := base + 4 + i*8
		pid, _ := be16(f.data, rec)
		eid, _ := be16(f.data, rec+2)
		off, _ := be32(f.data, rec+4)
		if pid != 3 || eid != 1 {
			continue
		}
		sub := base + off
		format, ok := be16(f.data, sub)
		if !ok || format != 4 {
			continue
		}
		return sub, nil
	}
	return 0, fmt.Errorf("titlefooter: typeface has no Unicode BMP cmap")
}

// glyphIndex maps a rune to a glyph id through the format-4 cmap.
func (f *ttfFont) glyphIndex(r rune) (int, bool) {
	if r < 0 || r > 0xFFFF {
		return 0, false
	}
	c := int(r)
	base := f.cmapSub
	segX2, ok := be16(f.data, base+6)
	if !ok || segX2 < 2 || segX2%2 != 0 {
		return 0, false
	}
	seg := segX2 / 2
	for i := 0; i < seg; i++ {
		end, ok1 := be16(f.data, base+14+i*2)
		start, ok2 := be16(f.data, base+16+segX2+i*2)
		delta, ok3 := be16(f.data, base+16+segX2*2+i*2)
		ro, ok4 := be16(f.data, base+16+segX2*3+i*2)
		if !ok1 || !ok2 || !ok3 || !ok4 {
			return 0, false
		}
		if c < start || c > end {
			continue
		}
		if ro == 0 {
			return (c + int(int16(delta))) & 0xFFFF, true
		}
		addr := base + 16 + segX2*3 + i*2 + ro + (c-start)*2
		g, ok := be16(f.data, addr)
		if !ok || g == 0 {
			return 0, false
		}
		return (g + int(int16(delta))) & 0xFFFF, true
	}
	return 0, false
}

func (f *ttfFont) loca(gid int) (start, end int, err error) {
	t := f.tables["loca"]
	glyf := f.tables["glyf"]
	if gid < 0 || gid >= f.numGlyphs {
		return 0, 0, fmt.Errorf("titlefooter: glyph index %d out of range", gid)
	}
	var a, b int
	if f.indexToLoc == 0 {
		a, _ = be16(f.data, int(t.off)+gid*2)
		b, _ = be16(f.data, int(t.off)+gid*2+2)
		a *= 2
		b *= 2
	} else {
		a, _ = be32(f.data, int(t.off)+gid*4)
		b, _ = be32(f.data, int(t.off)+gid*4+4)
	}
	if a < 0 || b < a || b > int(glyf.length) {
		return 0, 0, fmt.Errorf("titlefooter: glyph %d loca entry is invalid", gid)
	}
	return int(glyf.off) + a, int(glyf.off) + b, nil
}

func (f *ttfFont) advance(gid int) float64 {
	if gid < 0 {
		return 0
	}
	hmtx := f.tables["hmtx"]
	i := gid
	if i >= f.numHMetric {
		i = f.numHMetric - 1
	}
	v, _ := be16(f.data, int(hmtx.off)+i*4)
	return float64(v)
}

// glyph decodes one simple glyph. Composite glyphs are rejected because the
// embedded typeface does not use them and interpreting them incorrectly would
// silently change the mark.
func (f *ttfFont) glyph(r rune) (*ttfGlyph, error) {
	if g, ok := f.glyphs[r]; ok {
		return g, nil
	}
	gid, ok := f.glyphIndex(r)
	if !ok || gid <= 0 {
		return nil, fmt.Errorf("titlefooter: typeface has no glyph for %q", r)
	}
	start, end, err := f.loca(gid)
	if err != nil {
		return nil, err
	}
	g := &ttfGlyph{Advance: f.advance(gid), Coverage: f.advance(gid)}
	if end == start {
		g.IsEmpty = true
		f.glyphs[r] = g
		return g, nil
	}
	if start+10 > end {
		return nil, fmt.Errorf("titlefooter: glyph %q header is truncated", r)
	}
	nc := int(int16(binary.BigEndian.Uint16(f.data[start:])))
	if nc < 0 {
		return nil, fmt.Errorf("titlefooter: glyph %q is composite and is not supported", r)
	}
	xMin := float64(int16(binary.BigEndian.Uint16(f.data[start+2:])))
	yMin := float64(int16(binary.BigEndian.Uint16(f.data[start+4:])))
	xMax := float64(int16(binary.BigEndian.Uint16(f.data[start+6:])))
	yMax := float64(int16(binary.BigEndian.Uint16(f.data[start+8:])))
	if nc == 0 {
		g.IsEmpty = true
		g.XMin, g.YMin, g.XMax, g.YMax = xMin, yMin, xMax, yMax
		f.glyphs[r] = g
		return g, nil
	}
	pos := start + 10
	if pos+nc*2 > end {
		return nil, fmt.Errorf("titlefooter: glyph %q contour list is truncated", r)
	}
	ends := make([]int, nc)
	for i := 0; i < nc; i++ {
		ends[i] = int(binary.BigEndian.Uint16(f.data[pos+i*2:]))
	}
	pos += nc * 2
	if pos+2 > end {
		return nil, fmt.Errorf("titlefooter: glyph %q instructions are truncated", r)
	}
	instr := int(binary.BigEndian.Uint16(f.data[pos:]))
	pos += 2 + instr
	if pos > end {
		return nil, fmt.Errorf("titlefooter: glyph %q instructions exceed the glyph", r)
	}
	npts := ends[nc-1] + 1
	if npts <= 0 || npts > 0xFFFF {
		return nil, fmt.Errorf("titlefooter: glyph %q has an invalid point count", r)
	}
	flags := make([]byte, 0, npts)
	for len(flags) < npts {
		if pos >= end {
			return nil, fmt.Errorf("titlefooter: glyph %q flags are truncated", r)
		}
		fl := f.data[pos]
		pos++
		flags = append(flags, fl)
		if fl&0x08 != 0 {
			if pos >= end {
				return nil, fmt.Errorf("titlefooter: glyph %q repeat count is truncated", r)
			}
			rep := int(f.data[pos])
			pos++
			for j := 0; j < rep && len(flags) < npts; j++ {
				flags = append(flags, fl)
			}
		}
	}
	if len(flags) != npts {
		return nil, fmt.Errorf("titlefooter: glyph %q has too many points", r)
	}
	xs := make([]int, npts)
	x := 0
	for i, fl := range flags {
		if fl&0x02 != 0 {
			if pos >= end {
				return nil, fmt.Errorf("titlefooter: glyph %q x coordinates are truncated", r)
			}
			d := int(f.data[pos])
			pos++
			if fl&0x10 == 0 {
				d = -d
			}
			x += d
		} else if fl&0x10 == 0 {
			if pos+2 > end {
				return nil, fmt.Errorf("titlefooter: glyph %q x coordinates are truncated", r)
			}
			x += int(int16(binary.BigEndian.Uint16(f.data[pos:])))
			pos += 2
		}
		xs[i] = x
	}
	ys := make([]int, npts)
	y := 0
	for i, fl := range flags {
		if fl&0x04 != 0 {
			if pos >= end {
				return nil, fmt.Errorf("titlefooter: glyph %q y coordinates are truncated", r)
			}
			d := int(f.data[pos])
			pos++
			if fl&0x20 == 0 {
				d = -d
			}
			y += d
		} else if fl&0x20 == 0 {
			if pos+2 > end {
				return nil, fmt.Errorf("titlefooter: glyph %q y coordinates are truncated", r)
			}
			y += int(int16(binary.BigEndian.Uint16(f.data[pos:])))
			pos += 2
		}
		ys[i] = y
	}
	g.XMin, g.YMin, g.XMax, g.YMax = xMin, yMin, xMax, yMax
	g.Ends = ends
	g.Points = make([]ttfPoint, npts)
	for i := range g.Points {
		g.Points[i] = ttfPoint{X: float64(xs[i]), Y: float64(ys[i]), On: flags[i]&1 != 0}
	}
	f.glyphs[r] = g
	return g, nil
}

// canvasPoint converts a font-unit point to canvas pixels. Font Y points up;
// canvas Y points down.
func canvasPoint(p ttfPoint, scale, originX, baselineY float64) ttfPoint {
	return ttfPoint{X: originX + p.X*scale, Y: baselineY - p.Y*scale, On: p.On}
}

// flattenContour turns one TrueType contour into a closed polyline in canvas
// coordinates. Off-curve points are quadratic control points; adjacent
// off-curve points imply an on-curve midpoint.
func flattenContour(pts []ttfPoint) []ttfPoint {
	n := len(pts)
	if n == 0 {
		return nil
	}
	start := -1
	for i := 0; i < n; i++ {
		if pts[i].On {
			start = i
			break
		}
	}
	var out []ttfPoint
	var cur ttfPoint
	if start < 0 {
		// All control points: begin at the implied midpoint of the last and
		// first points, which is on the curve.
		cur = midpoint(pts[n-1], pts[0])
		start = 0
	} else {
		cur = pts[start]
		start++
	}
	out = append(out, cur)
	i := 0
	for i < n {
		p := pts[(start+i)%n]
		if p.On {
			out = append(out, p)
			cur = p
			i++
			continue
		}
		next := pts[(start+i+1)%n]
		if next.On {
			out = appendQuad(out, cur, p, next)
			cur = next
			i += 2
			continue
		}
		mid := midpoint(p, next)
		out = appendQuad(out, cur, p, mid)
		cur = mid
		i++
	}
	return out
}

func midpoint(a, b ttfPoint) ttfPoint {
	return ttfPoint{X: (a.X + b.X) / 2, Y: (a.Y + b.Y) / 2, On: true}
}

// appendQuad appends a quadratic curve, excluding its start point, using a
// fixed arc-length rule so the same input always produces the same polyline.
func appendQuad(out []ttfPoint, p0, c, p1 ttfPoint) []ttfPoint {
	length := math.Hypot(c.X-p0.X, c.Y-p0.Y) + math.Hypot(p1.X-c.X, p1.Y-c.Y)
	segments := int(math.Ceil(length / 0.75))
	if segments < 2 {
		segments = 2
	}
	if segments > 48 {
		segments = 48
	}
	for i := 1; i <= segments; i++ {
		t := float64(i) / float64(segments)
		u := 1 - t
		out = append(out, ttfPoint{
			X:  u*u*p0.X + 2*u*t*c.X + t*t*p1.X,
			Y:  u*u*p0.Y + 2*u*t*c.Y + t*t*p1.Y,
			On: true,
		})
	}
	return out
}

// glyphPolygons converts one glyph into canvas-space polylines.
func glyphPolygons(g *ttfGlyph, scale, originX, baselineY float64) [][]ttfPoint {
	if g.IsEmpty || len(g.Points) == 0 {
		return nil
	}
	points := make([]ttfPoint, len(g.Points))
	for i, p := range g.Points {
		points[i] = canvasPoint(p, scale, originX, baselineY)
	}
	polys := make([][]ttfPoint, 0, len(g.Ends))
	first := 0
	for _, end := range g.Ends {
		if end < first || end >= len(points) {
			break
		}
		contour := points[first : end+1]
		if poly := flattenContour(contour); len(poly) >= 3 {
			polys = append(polys, poly)
		}
		first = end + 1
	}
	return polys
}

// windingAt reports whether (x,y) lies inside a set of closed contours using the
// non-zero winding rule.
func windingAt(polys [][]ttfPoint, x, y float64) bool {
	w := 0
	for _, poly := range polys {
		for i := 0; i < len(poly); i++ {
			a := poly[i]
			b := poly[(i+1)%len(poly)]
			if a.Y <= y {
				if b.Y > y {
					if cross(a, b, x, y) > 0 {
						w++
					}
				}
			} else if b.Y <= y {
				if cross(a, b, x, y) < 0 {
					w--
				}
			}
		}
	}
	return w != 0
}

func cross(a, b ttfPoint, x, y float64) float64 {
	return (b.X-a.X)*(y-a.Y) - (x-a.X)*(b.Y-a.Y)
}
