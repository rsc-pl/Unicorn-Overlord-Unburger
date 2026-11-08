package titlefooter

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"testing"

	"uo-unburger/internal/assets"
)

// buildTestTTF assembles a tiny but structurally valid TrueType font. Every
// printable ASCII rune maps to a simple rectangle glyph, which is enough for
// the footer parser and rasteriser. It exists so a test can prove that a face
// other than the embedded one is accepted without any hard-coded hash check.
func buildTestTTF(composite bool) []byte {
	const (
		upem       = 1000
		numGlyphs  = 96 // gid 0 (.notdef) plus gids 1..95 for 0x20..0x7E
		advance    = 500
		glyphBytes = 26
	)
	glyph := make([]byte, glyphBytes)
	binary.BigEndian.PutUint16(glyph[0:], 1) // numberOfContours
	if composite {
		binary.BigEndian.PutUint16(glyph[0:], 0xFFFF) // -1: composite
	}
	binary.BigEndian.PutUint16(glyph[2:], 100) // xMin
	binary.BigEndian.PutUint16(glyph[4:], 0)   // yMin
	binary.BigEndian.PutUint16(glyph[6:], 300) // xMax
	binary.BigEndian.PutUint16(glyph[8:], 200) // yMax
	binary.BigEndian.PutUint16(glyph[10:], 3)  // endPtsOfContours[0]
	binary.BigEndian.PutUint16(glyph[12:], 0)  // instructionLength
	glyph[14], glyph[15], glyph[16], glyph[17] = 0x37, 0x37, 0x35, 0x23
	glyph[18], glyph[19], glyph[20] = 100, 200, 200 // x deltas
	glyph[21], glyph[22], glyph[23] = 0, 0, 200     // y deltas

	glyf := make([]byte, 0, numGlyphs*glyphBytes)
	loca := make([]uint16, numGlyphs+1)
	for gid := 0; gid < numGlyphs; gid++ {
		loca[gid] = uint16(len(glyf) / 2)
		if gid == 0 {
			continue
		}
		glyf = append(glyf, glyph...)
	}
	loca[numGlyphs] = uint16(len(glyf) / 2)

	head := make([]byte, 54)
	binary.BigEndian.PutUint32(head[0:], 0x00010000)
	binary.BigEndian.PutUint32(head[4:], 0x00010000)
	binary.BigEndian.PutUint32(head[12:], 0x5F0F3CF5)
	binary.BigEndian.PutUint16(head[18:], upem)
	binary.BigEndian.PutUint16(head[40:], 300)
	binary.BigEndian.PutUint16(head[42:], 200)
	binary.BigEndian.PutUint16(head[46:], 8)
	binary.BigEndian.PutUint16(head[48:], 2)
	binary.BigEndian.PutUint16(head[50:], 0) // indexToLocFormat: short

	maxp := make([]byte, 32)
	binary.BigEndian.PutUint32(maxp[0:], 0x00010000)
	binary.BigEndian.PutUint16(maxp[4:], numGlyphs)

	hhea := make([]byte, 36)
	binary.BigEndian.PutUint32(hhea[0:], 0x00010000)
	binary.BigEndian.PutUint16(hhea[4:], 800) // ascender
	binary.BigEndian.PutUint16(hhea[6:], 0xFE00)
	binary.BigEndian.PutUint16(hhea[10:], advance)
	binary.BigEndian.PutUint16(hhea[34:], numGlyphs)

	hmtx := make([]byte, numGlyphs*4)
	for gid := 0; gid < numGlyphs; gid++ {
		binary.BigEndian.PutUint16(hmtx[gid*4:], advance)
		binary.BigEndian.PutUint16(hmtx[gid*4+2:], 100)
	}

	cmapSub := make([]byte, 32)
	binary.BigEndian.PutUint16(cmapSub[0:], 4)  // format 4
	binary.BigEndian.PutUint16(cmapSub[2:], 32) // length
	binary.BigEndian.PutUint16(cmapSub[6:], 4)  // segCountX2
	binary.BigEndian.PutUint16(cmapSub[8:], 4)  // searchRange
	binary.BigEndian.PutUint16(cmapSub[10:], 1) // entrySelector
	binary.BigEndian.PutUint16(cmapSub[14:], 0x007E)
	binary.BigEndian.PutUint16(cmapSub[16:], 0xFFFF)
	binary.BigEndian.PutUint16(cmapSub[18:], 0) // reservedPad
	binary.BigEndian.PutUint16(cmapSub[20:], 0x0020)
	binary.BigEndian.PutUint16(cmapSub[22:], 0xFFFF)
	delta := int16(1 - 0x20)
	binary.BigEndian.PutUint16(cmapSub[24:], uint16(delta))
	binary.BigEndian.PutUint16(cmapSub[26:], 1)
	binary.BigEndian.PutUint16(cmapSub[28:], 0)
	binary.BigEndian.PutUint16(cmapSub[30:], 0)

	cmap := make([]byte, 12+len(cmapSub))
	binary.BigEndian.PutUint16(cmap[2:], 1)  // numTables
	binary.BigEndian.PutUint16(cmap[4:], 3)  // platformID: Windows
	binary.BigEndian.PutUint16(cmap[6:], 1)  // encodingID: Unicode BMP
	binary.BigEndian.PutUint32(cmap[8:], 12) // offset to subtable
	copy(cmap[12:], cmapSub)

	locaData := make([]byte, len(loca)*2)
	for i, off := range loca {
		binary.BigEndian.PutUint16(locaData[i*2:], off)
	}

	tables := map[string][]byte{
		"cmap": cmap,
		"glyf": glyf,
		"head": head,
		"hhea": hhea,
		"hmtx": hmtx,
		"loca": locaData,
		"maxp": maxp,
	}
	tags := []string{"cmap", "glyf", "head", "hhea", "hmtx", "loca", "maxp"}
	header := 12 + 16*len(tags)
	offset := header
	offsets := map[string]int{}
	for _, tag := range tags {
		offset = (offset + 3) &^ 3
		offsets[tag] = offset
		offset += len(tables[tag])
	}
	out := make([]byte, offset)
	binary.BigEndian.PutUint32(out[0:], 0x00010000)
	binary.BigEndian.PutUint16(out[4:], uint16(len(tags)))
	binary.BigEndian.PutUint16(out[6:], 16*8)
	binary.BigEndian.PutUint16(out[8:], 3)
	binary.BigEndian.PutUint16(out[10:], 16*8-16*8)
	for i, tag := range tags {
		rec := 12 + i*16
		copy(out[rec:rec+4], tag)
		binary.BigEndian.PutUint32(out[rec+8:], uint32(offsets[tag]))
		binary.BigEndian.PutUint32(out[rec+12:], uint32(len(tables[tag])))
		copy(out[offsets[tag]:], tables[tag])
	}
	return out
}

func TestAlternativeFontIsSupported(t *testing.T) {
	altData := buildTestTTF(false)
	alt, err := parseTTF(altData)
	if err != nil {
		t.Fatalf("parseTTF(alternate): %v", err)
	}
	cfg := *mustEmbeddedConfig(t)
	lines, size, err := layoutMarkWithFont("0.2.1-test", cfg, alt)
	if err != nil {
		t.Fatalf("layoutMarkWithFont: %v", err)
	}
	if size != cfg.FontSize {
		t.Fatalf("size = %g, want %g", size, cfg.FontSize)
	}
	canvas := rasterizeMarkAt(lines, cfg.OutlineSize, supersample)
	visible := 0
	for _, p := range canvas {
		if p.A != 0 {
			visible++
		}
	}
	if visible == 0 {
		t.Fatal("alternate face rendered no visible glyphs")
	}

	embedded, err := embeddedFont()
	if err != nil {
		t.Fatalf("embeddedFont: %v", err)
	}
	embeddedLines, _, err := layoutMarkWithFont("0.2.1-test", cfg, embedded)
	if err != nil {
		t.Fatal(err)
	}
	embeddedCanvas := rasterizeMarkAt(embeddedLines, cfg.OutlineSize, supersample)
	if samePixels(canvas, embeddedCanvas) {
		t.Fatal("alternate face produced the same image as the embedded face")
	}

	sum := sha256.Sum256(assets.FontTTF)
	if got, want := FontSHA256(), hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("FontSHA256 = %s, want digest of embedded bytes %s", got, want)
	}
	altSum := sha256.Sum256(altData)
	if FontSHA256() == hex.EncodeToString(altSum[:]) {
		t.Fatal("embedded and alternate font hashes unexpectedly match")
	}
}

func TestParseFontRejectsInvalidUnsupportedAndOversized(t *testing.T) {
	if _, err := parseTTF(nil); err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("empty font error = %v", err)
	}
	otf := append([]byte("OTTO"), make([]byte, 32)...)
	if _, err := parseTTF(otf); err == nil || !strings.Contains(err.Error(), "not a TrueType font") {
		t.Fatalf("OTTO font error = %v", err)
	}
	valid := buildTestTTF(false)
	if _, err := parseTTF(valid[:len(valid)/2]); err == nil {
		t.Fatal("truncated font accepted")
	}
	if _, err := parseTTF(make([]byte, maxFontBytes+1)); err == nil ||
		!strings.Contains(err.Error(), "larger than") {
		t.Fatalf("oversized font error = %v", err)
	}

	// A missing required table is reported by name.
	noHead := append([]byte(nil), valid...)
	for i := 0; i+4 <= len(noHead); i++ {
		if string(noHead[i:i+4]) == "head" {
			copy(noHead[i:i+4], "xxxx")
			break
		}
	}
	if _, err := parseTTF(noHead); err == nil || !strings.Contains(err.Error(), "head") {
		t.Fatalf("missing head error = %v", err)
	}

	// Composite glyphs are deliberately unsupported by this narrow reader and
	// must fail clearly instead of being guessed.
	composite := buildTestTTF(true)
	c, err := parseTTF(composite)
	if err != nil {
		t.Fatalf("parse composite fixture: %v", err)
	}
	if _, err := c.glyph(' '); err == nil || !strings.Contains(err.Error(), "composite") {
		t.Fatalf("composite glyph error = %v", err)
	}
}

func samePixels(a, b []pixel) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
