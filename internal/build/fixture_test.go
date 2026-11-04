package build

import (
	"encoding/binary"
	"testing"
)

// This file provides a self-contained synthetic CRI CPK + FMSB fixture builder
// for the build package tests. It is deliberately independent of the cpk test
// helpers (which are internal to that package) so the build tests exercise the
// real reader/writer through the public API only.

const (
	fxU8   = 0x00
	fxU32  = 0x04
	fxU64  = 0x06
	fxStr  = 0x0A
	fxData = 0x0B
)

var fxBE = binary.BigEndian
var fxLE = binary.LittleEndian

type fxCol struct {
	name string
	typ  byte
}

func fxWidth(t byte) int {
	switch t {
	case fxU8:
		return 1
	case fxU32, fxStr:
		return 4
	case fxU64, fxData:
		return 8
	}
	return 0
}

func fxAppendBE32(dst []byte, v uint32) []byte {
	return append(dst, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

func fxAppendBE64(dst []byte, v uint64) []byte {
	return append(dst, byte(v>>56), byte(v>>48), byte(v>>40), byte(v>>32),
		byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

func fxAppendLE64(dst []byte, v uint64) []byte {
	return append(dst, byte(v), byte(v>>8), byte(v>>16), byte(v>>24),
		byte(v>>32), byte(v>>40), byte(v>>48), byte(v>>56))
}

func fxPad(out []byte, n int) []byte {
	for len(out) < n {
		out = append(out, 0)
	}
	return out
}

// fxUTF builds a minimal self-consistent @UTF packet.
func fxUTF(t *testing.T, name string, cols []fxCol, rows [][]any) []byte {
	t.Helper()
	var pool []byte
	seen := map[string]uint32{}
	addStr := func(s string) uint32 {
		if off, ok := seen[s]; ok {
			return off
		}
		off := uint32(len(pool))
		seen[s] = off
		pool = append(pool, s...)
		pool = append(pool, 0)
		return off
	}
	var dataPool []byte
	nameOff := addStr(name)
	colOff := make([]uint32, len(cols))
	rowWidth := 0
	for i, c := range cols {
		colOff[i] = addStr(c.name)
		rowWidth += fxWidth(c.typ)
	}
	encoded := make([][]byte, len(rows))
	for ri, row := range rows {
		var buf []byte
		for ci, c := range cols {
			v := row[ci]
			switch c.typ {
			case fxStr:
				buf = fxAppendBE32(buf, addStr(v.(string)))
			case fxData:
				d := v.([]byte)
				off := len(dataPool)
				dataPool = append(dataPool, d...)
				buf = fxAppendBE32(buf, uint32(off))
				buf = fxAppendBE32(buf, uint32(len(d)))
			case fxU32:
				buf = fxAppendBE32(buf, uint32(v.(uint64)))
			case fxU64:
				buf = fxAppendBE64(buf, v.(uint64))
			case fxU8:
				buf = append(buf, byte(v.(uint64)))
			}
		}
		encoded[ri] = buf
	}

	colDefsLen := 5 * len(cols)
	rowsStart := 32 + colDefsLen
	rowsLen := rowWidth * len(rows)
	strStart := rowsStart + rowsLen
	dataStart := strStart + len(pool)
	total := dataStart + len(dataPool)

	pkt := make([]byte, total)
	copy(pkt[0:4], "@UTF")
	fxBE.PutUint32(pkt[4:8], uint32(total))
	fxBE.PutUint32(pkt[8:12], uint32(rowsStart-8))
	fxBE.PutUint32(pkt[12:16], uint32(strStart-8))
	fxBE.PutUint32(pkt[16:20], uint32(dataStart-8))
	fxBE.PutUint32(pkt[20:24], nameOff)
	fxBE.PutUint16(pkt[24:26], uint16(len(cols)))
	fxBE.PutUint16(pkt[26:28], uint16(rowWidth))
	fxBE.PutUint32(pkt[28:32], uint32(len(rows)))
	pos := 32
	for i, c := range cols {
		pkt[pos] = c.typ
		pos++
		fxBE.PutUint32(pkt[pos:pos+4], colOff[i])
		pos += 4
	}
	for _, row := range encoded {
		copy(pkt[pos:], row)
		pos += len(row)
	}
	copy(pkt[strStart:], pool)
	copy(pkt[dataStart:], dataPool)
	return pkt
}

type fxMember struct {
	dir     string
	name    string
	stored  []byte
	extract int64
}

// fxCPK builds a synthetic CPK that the real internal/cpk reader accepts.
func fxCPK(t *testing.T, members []fxMember) []byte {
	t.Helper()
	const (
		tocOffset     = 0x200
		contentOffset = 0x400
		align         = 0x100
	)
	addOffset := int64(tocOffset)
	type placed struct {
		m   fxMember
		off int64
		rel int64
	}
	list := make([]placed, len(members))
	cursor := int64(contentOffset)
	for i, m := range members {
		off := cursor
		if r := off % align; r != 0 {
			off += align - r
		}
		list[i] = placed{m: m, off: off, rel: off - addOffset}
		cursor = off + int64(len(m.stored))
	}
	etocOffset := cursor
	if r := etocOffset % align; r != 0 {
		etocOffset += align - r
	}

	tocRows := make([][]any, len(members))
	for i, p := range list {
		tocRows[i] = []any{p.m.dir, p.m.name, uint64(len(p.m.stored)), uint64(p.m.extract), uint64(p.rel), uint64(i), ""}
	}
	tocPacket := fxUTF(t, "TOC ", []fxCol{
		{"DirName", fxStr}, {"FileName", fxStr}, {"FileSize", fxU32},
		{"ExtractSize", fxU32}, {"FileOffset", fxU64}, {"ID", fxU32}, {"UserString", fxStr},
	}, tocRows)
	headerPacket := fxUTF(t, "CpkHeader", []fxCol{
		{"TocOffset", fxU32}, {"ContentOffset", fxU32}, {"EtocOffset", fxU32},
		{"ItocOffset", fxU32}, {"GtocOffset", fxU32}, {"Files", fxU32}, {"Align", fxU32},
	}, [][]any{{
		uint64(tocOffset), uint64(contentOffset), uint64(etocOffset), uint64(0), uint64(0),
		uint64(len(members)), uint64(align),
	}})
	etocPacket := fxUTF(t, "ETOC", nil, nil)

	out := []byte("CPK ")
	out = append(out, 0, 0, 0, 0) // LE u32
	out = fxAppendLE64(out, uint64(len(headerPacket)))
	out = append(out, headerPacket...)
	out = fxPad(out, tocOffset)
	out = append(out, "TOC "...)
	out = append(out, 0, 0, 0, 0)
	out = fxAppendLE64(out, uint64(len(tocPacket)))
	out = append(out, tocPacket...)
	out = fxPad(out, contentOffset)
	for _, p := range list {
		out = fxPad(out, int(p.off))
		out = append(out, p.m.stored...)
	}
	out = fxPad(out, int(etocOffset))
	out = append(out, "ETOC"...)
	out = append(out, 0, 0, 0, 0)
	out = fxAppendLE64(out, uint64(len(etocPacket)))
	out = append(out, etocPacket...)
	return out
}

// fxFMS builds an FMSB message sheet whose slots are the given strings. It
// mirrors the byte layout produced by internal/fms Marshal.
func fxFMS(slots []string) []byte {
	indexLen := len(slots) * 8
	regionLen := 0
	for _, s := range slots {
		regionLen += len(s) + 1
	}
	dataSize := indexLen + regionLen
	total := 0x20 + dataSize + 16
	out := make([]byte, 0, total)
	out = append(out, "FMSB"...)
	var b4 [4]byte
	put := func(v uint32) {
		fxLE.PutUint32(b4[:], v)
		out = append(out, b4[:]...)
	}
	put(uint32(dataSize))   // 0x04
	put(0x20)               // 0x08 data start
	put(0)                  // 0x0C
	put(0)                  // 0x10
	put(uint32(len(slots))) // 0x14 slot count
	put(0)                  // 0x18 version
	put(0)                  // 0x1C
	out = append(out, make([]byte, indexLen)...)
	for _, s := range slots {
		out = append(out, s...)
		out = append(out, 0)
	}
	out = append(out, "FEOC"...)
	put(0)
	put(0)
	put(0)
	return out
}
