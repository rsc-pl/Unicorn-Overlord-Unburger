// Package cpk implements a reader and layout-preserving writer for CRI CPK
// archives as used by the game's asset containers.
//
// The format facts encoded here were established by independent read-only
// analysis of locally supplied archives: the file begins with the magic "CPK "
// followed by a @UTF packet; @UTF tables are big-endian with offsets relative to
// the packet payload start plus 8; packets may be XOR-obfuscated; file payloads
// are CRILAYLA-compressed when the declared extract size exceeds the stored size.
package cpk

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sort"
)

// @UTF value type codes (low nibble of a column flags byte).
const (
	utfTypeU8   = 0x00
	utfTypeS8   = 0x01
	utfTypeU16  = 0x02
	utfTypeS16  = 0x03
	utfTypeU32  = 0x04
	utfTypeS32  = 0x05
	utfTypeU64  = 0x06
	utfTypeS64  = 0x07
	utfTypeF32  = 0x08
	utfTypeF64  = 0x09
	utfTypeStr  = 0x0A
	utfTypeData = 0x0B
)

// @UTF storage codes (high nibble of a column flags byte).
const (
	utfStoreNone     = 0x00
	utfStoreZero     = 0x10
	utfStoreConstant = 0x30
	utfStorePerRow   = 0x50
)

var utfByteOrder = binary.BigEndian

// utfColumn describes one column of a @UTF table.
type utfColumn struct {
	name string
	// flags is the raw column flags byte: high nibble = storage, low nibble = value type.
	flags byte
	// rowOffset is the byte offset of a stored value within a row. It is -1 for
	// columns whose value is not stored per row (zero or constant storage).
	rowOffset int
	// constValue holds the recycled value for zero/constant storage.
	constValue any
}

func (c utfColumn) storage() byte   { return c.flags & 0xF0 }
func (c utfColumn) valueType() byte { return c.flags & 0x0F }

// columnWidth returns the number of bytes a stored value occupies in a row.
func (c utfColumn) columnWidth() int {
	switch c.valueType() {
	case utfTypeU8, utfTypeS8:
		return 1
	case utfTypeU16, utfTypeS16:
		return 2
	case utfTypeU32, utfTypeS32, utfTypeF32, utfTypeStr:
		return 4
	case utfTypeU64, utfTypeS64, utfTypeF64, utfTypeData:
		return 8
	default:
		return 0
	}
}

// utfTable is a parsed but byte-preserving view of one @UTF packet.
type utfTable struct {
	// raw is the complete packet beginning with the "@UTF" identifier, exactly
	// as stored (obfuscated form preserved).
	raw []byte
	// body is the decoded packet body used for parsing. It equals raw when the
	// packet was not obfuscated.
	body []byte

	name       string
	numColumns int
	rowLength  int
	numRows    int
	columns    []utfColumn
	rows       [][]any

	// rowsStart is the offset within raw where row data begins.
	rowsStart int
	// stringsRel and dataRel are the raw header offsets of the string pool and
	// the data section, relative to byte 8 of the packet, as stored.
	stringsRel int
	dataRel    int
}

// utfRowEdit lists the column values a rebuilt @UTF row carries. Only the named
// columns are written; every other column keeps its existing bytes (for a patch)
// or stays zero (for an appended row).
type utfRowEdit struct {
	// Strings maps a string column to its new value.
	Strings map[string]string
	// Uints maps an integer column to its new value.
	Uints map[string]uint64
}

func utfCString(buf []byte, off int) (string, error) {
	if off < 0 || off > len(buf) {
		return "", fmt.Errorf("string offset %d out of range (len %d)", off, len(buf))
	}
	end := off
	for end < len(buf) && buf[end] != 0 {
		end++
	}
	return string(buf[off:end]), nil
}

// parseUTF decodes a @UTF packet body. pkt must begin with the "@UTF" identifier.
func parseUTF(pkt []byte) (*utfTable, error) {
	if len(pkt) < 32 {
		return nil, fmt.Errorf("cpk: @UTF packet too short (%d bytes)", len(pkt))
	}
	if string(pkt[:4]) != "@UTF" {
		return nil, fmt.Errorf("cpk: not a @UTF packet (got %q)", pkt[:4])
	}

	rowsRel := utfByteOrder.Uint32(pkt[8:12])
	stringsRel := utfByteOrder.Uint32(pkt[12:16])
	dataRel := utfByteOrder.Uint32(pkt[16:20])
	nameRel := utfByteOrder.Uint32(pkt[20:24])
	numColumns := int(utfByteOrder.Uint16(pkt[24:26]))
	rowLength := int(utfByteOrder.Uint16(pkt[26:28]))
	numRows := int(utfByteOrder.Uint32(pkt[28:32]))

	stringsBase := 8 + int(stringsRel)
	dataBase := 8 + int(dataRel)
	rowsBase := 8 + int(rowsRel)
	if stringsBase > len(pkt) || dataBase > len(pkt) || rowsBase > len(pkt) {
		return nil, fmt.Errorf("cpk: @UTF section offset out of range")
	}

	t := &utfTable{
		raw:        pkt,
		body:       pkt,
		numColumns: numColumns,
		rowLength:  rowLength,
		numRows:    numRows,
		rowsStart:  rowsBase,
		stringsRel: int(stringsRel),
		dataRel:    int(dataRel),
	}
	if rowsBase+numRows*rowLength > len(pkt) {
		return nil, fmt.Errorf("cpk: @UTF rows region exceeds packet")
	}

	name, err := utfCString(pkt, stringsBase+int(nameRel))
	if err != nil {
		return nil, fmt.Errorf("cpk: @UTF table name: %w", err)
	}
	t.name = name

	pos := 32
	t.columns = make([]utfColumn, 0, numColumns)
	rowOffset := 0
	for i := 0; i < numColumns; i++ {
		if pos >= len(pkt) {
			return nil, fmt.Errorf("cpk: @UTF column %d header truncated", i)
		}
		flags := pkt[pos]
		pos++
		if flags == 0 {
			if pos+4 > len(pkt) {
				return nil, fmt.Errorf("cpk: @UTF column %d extended header truncated", i)
			}
			pos += 3
			flags = pkt[pos]
			pos++
		}
		if pos+4 > len(pkt) {
			return nil, fmt.Errorf("cpk: @UTF column %d name offset truncated", i)
		}
		nameOff := int(utfByteOrder.Uint32(pkt[pos : pos+4]))
		pos += 4
		colName, err := utfCString(pkt, stringsBase+nameOff)
		if err != nil {
			return nil, fmt.Errorf("cpk: @UTF column %d name: %w", i, err)
		}
		col := utfColumn{name: colName, flags: flags}
		switch col.storage() {
		case utfStoreConstant:
			v, np, err := readUTFValue(pkt, pos, col.valueType(), stringsBase, dataBase)
			if err != nil {
				return nil, fmt.Errorf("cpk: @UTF column %q constant: %w", colName, err)
			}
			col.constValue = v
			pos = np
		case utfStoreZero:
			col.constValue = uint64(0)
		}
		if col.storage() == utfStoreNone || col.storage() == utfStorePerRow {
			col.rowOffset = rowOffset
			rowOffset += col.columnWidth()
		} else {
			col.rowOffset = -1
		}
		t.columns = append(t.columns, col)
	}

	t.rows = make([][]any, numRows)
	for r := 0; r < numRows; r++ {
		row := make([]any, numColumns)
		rp := rowsBase + r*rowLength
		for i := range t.columns {
			col := t.columns[i]
			switch col.storage() {
			case utfStoreZero:
				row[i] = uint64(0)
			case utfStoreConstant:
				row[i] = col.constValue
			default:
				if rp+col.rowOffset+col.columnWidth() > len(pkt) {
					return nil, fmt.Errorf("cpk: @UTF row %d column %q out of range", r, col.name)
				}
				v, _, err := readUTFValue(pkt, rp+col.rowOffset, col.valueType(), stringsBase, dataBase)
				if err != nil {
					return nil, fmt.Errorf("cpk: @UTF row %d column %q: %w", r, col.name, err)
				}
				row[i] = v
			}
		}
		t.rows[r] = row
	}
	return t, nil
}

func readUTFValue(buf []byte, pos int, valueType byte, stringsBase, dataBase int) (any, int, error) {
	switch valueType {
	case utfTypeU8, utfTypeS8:
		if pos+1 > len(buf) {
			return nil, pos, fmt.Errorf("truncated u8")
		}
		return uint64(buf[pos]), pos + 1, nil
	case utfTypeU16, utfTypeS16:
		if pos+2 > len(buf) {
			return nil, pos, fmt.Errorf("truncated u16")
		}
		return uint64(utfByteOrder.Uint16(buf[pos : pos+2])), pos + 2, nil
	case utfTypeU32, utfTypeS32:
		if pos+4 > len(buf) {
			return nil, pos, fmt.Errorf("truncated u32")
		}
		return uint64(utfByteOrder.Uint32(buf[pos : pos+4])), pos + 4, nil
	case utfTypeU64, utfTypeS64:
		if pos+8 > len(buf) {
			return nil, pos, fmt.Errorf("truncated u64")
		}
		return utfByteOrder.Uint64(buf[pos : pos+8]), pos + 8, nil
	case utfTypeF32:
		if pos+4 > len(buf) {
			return nil, pos, fmt.Errorf("truncated f32")
		}
		return 0.0, pos + 4, nil
	case utfTypeF64:
		if pos+8 > len(buf) {
			return nil, pos, fmt.Errorf("truncated f64")
		}
		return 0.0, pos + 8, nil
	case utfTypeStr:
		if pos+4 > len(buf) {
			return nil, pos, fmt.Errorf("truncated string offset")
		}
		off := int(utfByteOrder.Uint32(buf[pos : pos+4]))
		s, err := utfCString(buf, stringsBase+off)
		if err != nil {
			return nil, pos, err
		}
		return s, pos + 4, nil
	case utfTypeData:
		if pos+8 > len(buf) {
			return nil, pos, fmt.Errorf("truncated data header")
		}
		off := int(utfByteOrder.Uint32(buf[pos : pos+4]))
		size := int(utfByteOrder.Uint32(buf[pos+4 : pos+8]))
		if dataBase+off < 0 || dataBase+off+size > len(buf) {
			return nil, pos, fmt.Errorf("data slice out of range")
		}
		slice := make([]byte, size)
		copy(slice, buf[dataBase+off:dataBase+off+size])
		return slice, pos + 8, nil
	default:
		return nil, pos, fmt.Errorf("unknown value type 0x%02x", valueType)
	}
}

func (t *utfTable) column(name string) (int, bool) {
	for i := range t.columns {
		if t.columns[i].name == name {
			return i, true
		}
	}
	return 0, false
}

// hasColumn reports whether the table defines a column with the given name.
func (t *utfTable) hasColumn(name string) bool {
	_, ok := t.column(name)
	return ok
}

func (t *utfTable) value(row int, name string) (any, bool) {
	ci, ok := t.column(name)
	if !ok || row < 0 || row >= len(t.rows) {
		return nil, false
	}
	return t.rows[row][ci], true
}

func (t *utfTable) intValue(row int, name string) (int64, bool, error) {
	v, ok := t.value(row, name)
	if !ok {
		return 0, false, nil
	}
	switch n := v.(type) {
	case uint64:
		return int64(n), true, nil
	case int64:
		return n, true, nil
	default:
		return 0, true, fmt.Errorf("cpk: column %q is not an integer", name)
	}
}

func (t *utfTable) uintValue(row int, name string) (uint64, bool, error) {
	v, ok := t.value(row, name)
	if !ok {
		return 0, false, nil
	}
	switch n := v.(type) {
	case uint64:
		return n, true, nil
	default:
		return 0, true, fmt.Errorf("cpk: column %q is not an unsigned integer", name)
	}
}

func (t *utfTable) stringValue(row int, name string) (string, bool, error) {
	v, ok := t.value(row, name)
	if !ok {
		return "", false, nil
	}
	s, ok := v.(string)
	if !ok {
		return "", true, fmt.Errorf("cpk: column %q is not a string", name)
	}
	return s, true, nil
}

// setRowUint patches a stored integer value directly in the raw packet bytes.
// It refuses columns whose value is not stored per row.
func (t *utfTable) setRowUint(row int, name string, v uint64) error {
	ci, ok := t.column(name)
	if !ok {
		return fmt.Errorf("cpk: unknown column %q", name)
	}
	if row < 0 || row >= t.numRows {
		return fmt.Errorf("cpk: row %d out of range", row)
	}
	col := t.columns[ci]
	if col.rowOffset < 0 {
		return fmt.Errorf("cpk: column %q is not stored per row", name)
	}
	pos := t.rowsStart + row*t.rowLength + col.rowOffset
	switch col.valueType() {
	case utfTypeU8:
		t.raw[pos] = byte(v)
	case utfTypeU16:
		utfByteOrder.PutUint16(t.raw[pos:pos+2], uint16(v))
	case utfTypeU32:
		utfByteOrder.PutUint32(t.raw[pos:pos+4], uint32(v))
	case utfTypeU64:
		utfByteOrder.PutUint64(t.raw[pos:pos+8], v)
	default:
		return fmt.Errorf("cpk: column %q has non-integer value type 0x%02x", name, col.valueType())
	}
	return nil
}

// putUTFUint writes v into row at col's stored offset.
func putUTFUint(row []byte, col utfColumn, v uint64) error {
	if col.rowOffset < 0 || col.rowOffset+col.columnWidth() > len(row) {
		return fmt.Errorf("cpk: column %q is not stored per row", col.name)
	}
	pos := col.rowOffset
	switch col.valueType() {
	case utfTypeU8:
		row[pos] = byte(v)
	case utfTypeU16:
		utfByteOrder.PutUint16(row[pos:pos+2], uint16(v))
	case utfTypeU32:
		utfByteOrder.PutUint32(row[pos:pos+4], uint32(v))
	case utfTypeU64:
		utfByteOrder.PutUint64(row[pos:pos+8], v)
	default:
		return fmt.Errorf("cpk: column %q has non-integer value type 0x%02x", col.name, col.valueType())
	}
	return nil
}

// utfBuilder owns the pool and data sections for a row-sequence rebuild. It
// starts from the table's existing pools, so every offset already stored in a
// copied row stays valid; only genuinely new strings are appended.
type utfBuilder struct {
	t    *utfTable
	pool []byte
	data []byte
}

func (t *utfTable) newBuilder() (*utfBuilder, error) {
	stringsBase := 8 + t.stringsRel
	dataBase := 8 + t.dataRel
	if stringsBase < t.rowsStart || dataBase < stringsBase || dataBase > len(t.raw) {
		return nil, fmt.Errorf("cpk: @UTF sections are inconsistent")
	}
	return &utfBuilder{
		t:    t,
		pool: append([]byte(nil), t.raw[stringsBase:dataBase]...),
		data: append([]byte(nil), t.raw[dataBase:]...),
	}, nil
}

// stringOffset returns the pool offset for s, appending the string when it is
// not already present.
func (b *utfBuilder) stringOffset(s string) uint32 {
	needle := append([]byte(s), 0)
	if i := bytes.Index(b.pool, needle); i >= 0 {
		return uint32(i)
	}
	off := uint32(len(b.pool))
	b.pool = append(b.pool, needle...)
	return off
}

// rowBytes returns a copy of the stored row at index i.
func (t *utfTable) rowBytes(i int) ([]byte, error) {
	if i < 0 || i >= t.numRows {
		return nil, fmt.Errorf("cpk: row %d out of range", i)
	}
	start := t.rowsStart + i*t.rowLength
	if start+t.rowLength > len(t.raw) {
		return nil, fmt.Errorf("cpk: row %d exceeds the packet", i)
	}
	return append([]byte(nil), t.raw[start:start+t.rowLength]...), nil
}

// blankRow returns a zero row; unnamed columns stay zero or point at offset 0.
func (t *utfTable) blankRow() []byte {
	return make([]byte, t.rowLength)
}

// sortedStringKeys returns the map's keys in lexical order. String values are
// appended to the rebuilt pool, so the iteration order must be deterministic;
// Go map order is not.
func sortedStringKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// applyRowEdit writes the named values into a row. String values are resolved
// through the rebuild's builder so their offsets are valid in the new packet.
func (t *utfTable) applyRowEdit(b *utfBuilder, row []byte, ed utfRowEdit) error {
	if len(row) != t.rowLength {
		return fmt.Errorf("cpk: row length %d, want %d", len(row), t.rowLength)
	}
	for _, name := range sortedStringKeys(ed.Strings) {
		val := ed.Strings[name]
		ci, ok := t.column(name)
		if !ok {
			return fmt.Errorf("cpk: unknown column %q", name)
		}
		col := t.columns[ci]
		if col.rowOffset < 0 {
			return fmt.Errorf("cpk: column %q is not stored per row", name)
		}
		if col.valueType() != utfTypeStr {
			return fmt.Errorf("cpk: column %q is not a string", name)
		}
		utfByteOrder.PutUint32(row[col.rowOffset:col.rowOffset+4], b.stringOffset(val))
	}
	for name, val := range ed.Uints {
		ci, ok := t.column(name)
		if !ok {
			return fmt.Errorf("cpk: unknown column %q", name)
		}
		if err := putUTFUint(row, t.columns[ci], val); err != nil {
			return err
		}
	}
	return nil
}

// encodeRowSequence returns a new @UTF packet body whose row data is exactly
// rows, in the supplied order, with the column definitions and data section
// preserved and the string pool taken from b. The table itself is not mutated,
// so a repeat rebuild produces the same bytes.
func (t *utfTable) encodeRowSequence(rows [][]byte, b *utfBuilder) ([]byte, error) {
	rowsBase := t.rowsStart
	if rowsBase < 32 || rowsBase > len(t.raw) {
		return nil, fmt.Errorf("cpk: @UTF rows offset is invalid")
	}
	for i, row := range rows {
		if len(row) != t.rowLength {
			return nil, fmt.Errorf("cpk: rebuilt row %d is %d bytes, want %d", i, len(row), t.rowLength)
		}
	}

	rowsBlob := make([]byte, 0, len(rows)*t.rowLength)
	for _, row := range rows {
		rowsBlob = append(rowsBlob, row...)
	}

	rowsRel := rowsBase - 8
	stringsRel := rowsRel + len(rowsBlob)
	dataRel := stringsRel + len(b.pool)
	totalLen := 8 + dataRel + len(b.data)

	body := make([]byte, totalLen)
	copy(body[:rowsBase], t.raw[:rowsBase])
	copy(body[rowsBase:], rowsBlob)
	copy(body[8+stringsRel:], b.pool)
	copy(body[8+dataRel:], b.data)

	// This archive series stores the @UTF "total size" as the data-section
	// offset (dataRel), excluding the eight-byte @UTF header; the LE wrapper
	// length remains 8+len(body). Match the source convention so a re-encoded
	// packet is byte-identical to the container's own tables.
	utfByteOrder.PutUint32(body[4:8], uint32(dataRel))
	utfByteOrder.PutUint32(body[8:12], uint32(rowsRel))
	utfByteOrder.PutUint32(body[12:16], uint32(stringsRel))
	utfByteOrder.PutUint32(body[16:20], uint32(dataRel))
	utfByteOrder.PutUint16(body[26:28], uint16(t.rowLength))
	utfByteOrder.PutUint32(body[28:32], uint32(len(rows)))
	return body, nil
}
