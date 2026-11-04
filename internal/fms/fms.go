// Package fms implements a byte-faithful reader and writer for the message
// sheet ("FMSB") containers used by the game's text archives.
//
// Layout (all header integers little-endian):
//
//	0x00  "FMSB"
//	0x04  data size      = FEOC offset - 0x20
//	0x08  data start     = 0x20
//	0x0C  reserved (0)
//	0x10  reserved (0)
//	0x14  slot count
//	0x18  version / format id
//	0x1C  reserved (0)
//	0x20  slot index region, slot count * 8 bytes (zero in observed files)
//	...   slot count NUL-terminated raw slot strings, then padding
//	last 16 bytes: "FEOC" + u32 + u32 + u32 footer
//
// Slots are preserved as raw bytes, so markup, embedded multibyte UTF-8 and
// empty slots survive a read/write round trip unchanged.
package fms

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	magic        = "FMSB"
	footerMagic  = "FEOC"
	headerSize   = 0x20
	indexStride  = 8
	slotTermByte = 0x00
)

var le = binary.LittleEndian

// Sheet is a parsed FMSB message sheet.
type Sheet struct {
	// DataStart is the header field at 0x08; only the canonical 0x20 is supported.
	DataStart uint32
	// Reserved0/1/2 are the zero header fields at 0x0C/0x10/0x1C, kept verbatim.
	Reserved0 uint32
	Reserved1 uint32
	Reserved2 uint32
	// Version is the format id at 0x18.
	Version uint32

	Slots [][]byte

	// index is the raw slot index region, preserved verbatim.
	index []byte
	// padding is the trailing byte run after the last slot terminator.
	padding []byte

	footer0 uint32
	footer1 uint32
	footer2 uint32
}

// Parse decodes an FMSB sheet. Malformed input yields a precise error.
func Parse(data []byte) (*Sheet, error) {
	if len(data) < headerSize+16 {
		return nil, fmt.Errorf("fms: input too short (%d bytes)", len(data))
	}
	if string(data[:4]) != magic {
		return nil, fmt.Errorf("fms: bad magic %q", data[:4])
	}

	dataSize := le.Uint32(data[4:8])
	dataStart := le.Uint32(data[8:12])
	res0 := le.Uint32(data[12:16])
	res1 := le.Uint32(data[16:20])
	slotCount := le.Uint32(data[20:24])
	version := le.Uint32(data[24:28])
	res2 := le.Uint32(data[28:32])

	if dataStart != headerSize {
		return nil, fmt.Errorf("fms: unsupported data start 0x%x (want 0x%x)", dataStart, headerSize)
	}

	footer := len(data) - 16
	if string(data[footer:footer+4]) != footerMagic {
		return nil, fmt.Errorf("fms: missing FEOC footer at %d", footer)
	}
	if uint64(dataSize) != uint64(footer)-uint64(headerSize) {
		return nil, fmt.Errorf("fms: data size %d does not match footer offset %d", dataSize, footer)
	}

	indexEnd := uint64(dataStart) + uint64(slotCount)*indexStride
	if indexEnd > uint64(footer) {
		return nil, fmt.Errorf("fms: slot index region (%d bytes) exceeds data region", slotCount*indexStride)
	}

	index := append([]byte(nil), data[dataStart:indexEnd]...)
	region := data[indexEnd:footer]

	slots := make([][]byte, 0, slotCount)
	pos := 0
	for i := uint32(0); i < slotCount; i++ {
		end := indexOfNUL(region, pos)
		if end < 0 {
			return nil, fmt.Errorf("fms: slot %d/%d is not NUL-terminated", i, slotCount)
		}
		slots = append(slots, append([]byte(nil), region[pos:end]...))
		pos = end + 1
	}

	return &Sheet{
		DataStart: dataStart,
		Reserved0: res0,
		Reserved1: res1,
		Reserved2: res2,
		Version:   version,
		Slots:     slots,
		index:     index,
		padding:   append([]byte(nil), region[pos:]...),
		footer0:   le.Uint32(data[footer+4 : footer+8]),
		footer1:   le.Uint32(data[footer+8 : footer+12]),
		footer2:   le.Uint32(data[footer+12 : footer+16]),
	}, nil
}

func indexOfNUL(buf []byte, from int) int {
	for i := from; i < len(buf); i++ {
		if buf[i] == slotTermByte {
			return i
		}
	}
	return -1
}

// SlotCount returns the number of slots.
func (s *Sheet) SlotCount() int { return len(s.Slots) }

// Slot returns the raw bytes of slot i.
func (s *Sheet) Slot(i int) []byte { return s.Slots[i] }

// SetSlot replaces the raw bytes of slot i. The slot count is unchanged.
func (s *Sheet) SetSlot(i int, raw []byte) error {
	if i < 0 || i >= len(s.Slots) {
		return fmt.Errorf("fms: slot %d out of range (count %d)", i, len(s.Slots))
	}
	s.Slots[i] = append([]byte(nil), raw...)
	return nil
}

// Marshal encodes the sheet. For a sheet parsed and not modified, the output is
// byte-identical to the source.
func (s *Sheet) Marshal() ([]byte, error) {
	if s.DataStart != headerSize {
		return nil, fmt.Errorf("fms: unsupported data start 0x%x", s.DataStart)
	}
	regionLen := len(s.padding)
	for _, slot := range s.Slots {
		regionLen += len(slot) + 1
	}
	dataSize := len(s.Slots)*indexStride + regionLen
	total := headerSize + dataSize + 16
	if dataSize < 0 || total < headerSize {
		return nil, errors.New("fms: slot data too large to encode")
	}

	out := make([]byte, 0, total)
	out = append(out, magic...)
	out = appendU32(out, uint32(dataSize))
	out = appendU32(out, s.DataStart)
	out = appendU32(out, s.Reserved0)
	out = appendU32(out, s.Reserved1)
	out = appendU32(out, uint32(len(s.Slots)))
	out = appendU32(out, s.Version)
	out = appendU32(out, s.Reserved2)
	out = append(out, s.index...)
	for _, slot := range s.Slots {
		out = append(out, slot...)
		out = append(out, slotTermByte)
	}
	out = append(out, s.padding...)
	out = append(out, footerMagic...)
	out = appendU32(out, s.footer0)
	out = appendU32(out, s.footer1)
	out = appendU32(out, s.footer2)
	return out, nil
}

func appendU32(dst []byte, v uint32) []byte {
	var b [4]byte
	le.PutUint32(b[:], v)
	return append(dst, b[:]...)
}
