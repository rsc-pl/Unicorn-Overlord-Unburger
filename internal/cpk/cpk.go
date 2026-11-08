package cpk

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"
)

const (
	cpkMagic    = "CPK "
	tocMagic    = "TOC "
	etocMagic   = "ETOC"
	utfMagic    = "@UTF"
	packetWrap  = 12 // LE u32 unknown + LE u64 body size
	utfIdentOff = 16 // bytes from the start of a region to the @UTF body
)

var cpkLittleEndian = binary.LittleEndian

// Header carries the subset of the CpkHeader table that controls layout.
type Header struct {
	TocOffset     int64
	ContentOffset int64
	EtocOffset    int64
	ItocOffset    int64
	GtocOffset    int64
	Files         int64
	Align         int64
}

// Entry describes one member file listed in the TOC.
type Entry struct {
	DirName     string
	FileName    string
	FileSize    int64
	ExtractSize int64
	// RelOffset is the FileOffset exactly as stored in the TOC.
	RelOffset int64
	// Offset is RelOffset plus the archive's add-offset.
	Offset     int64
	ID         uint64
	UserString string

	// replacement, when non-nil, supersedes the stored payload on write.
	replacement []byte
	// resized is set by ReplaceMemberResized: the stored and extract sizes are
	// caller-supplied and RebuildVariable must recompute the content layout.
	resized bool
	// newExtract is the extract size declared alongside a resized replacement.
	newExtract int64
	// added is set by AddMember: the entry is not present in the source TOC and
	// RebuildVariable must append a row for it. FileOffset is assigned then.
	added bool
}

// Path returns the member path using the archive's separator convention.
func (e *Entry) Path() string {
	if e.DirName == "" {
		return e.FileName
	}
	return e.DirName + "/" + e.FileName
}

// Compressed reports whether the member is stored CRILAYLA-compressed. A staged
// replacement is measured by the sizes a rebuild will write.
func (e *Entry) Compressed() bool { return e.stagedExtractSize() > e.stagedStoredSize() }

// stagedStoredSize returns the stored size the next rebuild will write: the
// replacement length when a payload is staged, otherwise the original FileSize.
func (e *Entry) stagedStoredSize() int64 {
	if e.replacement != nil {
		return int64(len(e.replacement))
	}
	return e.FileSize
}

// stagedExtractSize returns the extract size the next rebuild will declare.
func (e *Entry) stagedExtractSize() int64 {
	if e.resized {
		return e.newExtract
	}
	return e.ExtractSize
}

// Archive is a parsed CRI CPK. It holds only bounded metadata from the source
// and reads member payloads lazily, so large archives are never buffered whole.
type Archive struct {
	r      io.ReaderAt
	closer io.Closer
	size   int64

	Header  Header
	Entries []*Entry

	addOffset     int64
	tocOffset     int64
	contentOffset int64
	etocOffset    int64
	align         int64

	headerSpan []byte // bytes [0, TocOffset)
	tocSpan    []byte // bytes [TocOffset, ContentOffset)
	etocSpan   []byte // bytes [EtocOffset, size)

	headerTable *utfTable
	tocTable    *utfTable
	etocTable   *utfTable

	headerObfuscated bool
	tocObfuscated    bool
	etocObfuscated   bool
}

// OpenFile opens and parses a CPK from disk. The caller must Close the archive.
func OpenFile(path string) (*Archive, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	a, err := Open(f, info.Size())
	if err != nil {
		f.Close()
		return nil, err
	}
	a.closer = f
	return a, nil
}

// Open parses the archive metadata reachable through r. Only the magic, the
// header packet and the TOC/ETOC regions are read; member data stays on disk.
func Open(r io.ReaderAt, size int64) (*Archive, error) {
	if size < 16 {
		return nil, fmt.Errorf("cpk: file too small for a CPK (%d bytes)", size)
	}
	a := &Archive{r: r, size: size}

	magic, err := a.readAt(0, 4)
	if err != nil {
		return nil, err
	}
	if string(magic) != cpkMagic {
		return nil, fmt.Errorf("cpk: bad magic %q", magic)
	}

	hdrTable, hdrObf, err := a.readPacket(4)
	if err != nil {
		return nil, fmt.Errorf("cpk: header packet: %w", err)
	}
	a.headerTable = hdrTable
	a.headerObfuscated = hdrObf

	if err := a.readHeaderFields(); err != nil {
		return nil, err
	}
	if err := a.validateHeader(); err != nil {
		return nil, err
	}

	if err := a.readTOC(); err != nil {
		return nil, err
	}
	if err := a.readETOC(); err != nil {
		return nil, err
	}
	if err := a.captureSpans(); err != nil {
		return nil, err
	}
	if err := a.buildEntries(); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *Archive) readAt(off, n int64) ([]byte, error) {
	if off < 0 || n < 0 || off > a.size || n > a.size-off {
		return nil, fmt.Errorf("cpk: read [%d,%d) outside file (size %d)", off, off+n, a.size)
	}
	buf := make([]byte, n)
	if _, err := a.r.ReadAt(buf, off); err != nil {
		return nil, fmt.Errorf("cpk: read [%d,%d): %w", off, off+n, err)
	}
	return buf, nil
}

// readPacket reads an LE-wrapped @UTF packet beginning at off, de-obfuscating
// the body when it is not already plain. It returns the parsed table and
// whether the body was obfuscated.
func (a *Archive) readPacket(off int64) (*utfTable, bool, error) {
	head, err := a.readAt(off, packetWrap)
	if err != nil {
		return nil, false, err
	}
	bodySize := int64(cpkLittleEndian.Uint64(head[4:12]))
	if bodySize < 4 || bodySize > a.size-off-packetWrap {
		return nil, false, fmt.Errorf("cpk: packet body size %d out of range", bodySize)
	}
	body, err := a.readAt(off+packetWrap, bodySize)
	if err != nil {
		return nil, false, err
	}
	obfuscated := string(body[:4]) != utfMagic
	if obfuscated {
		body = criXorMask(body)
	}
	table, err := parseUTF(body)
	if err != nil {
		return nil, false, err
	}
	return table, obfuscated, nil
}

func (a *Archive) readHeaderFields() error {
	const row = 0
	get := func(name string) (int64, error) {
		v, ok, err := a.headerTable.intValue(row, name)
		if err != nil {
			return 0, err
		}
		if !ok {
			return 0, fmt.Errorf("cpk: header missing column %q", name)
		}
		return v, nil
	}
	var err error
	if a.Header.TocOffset, err = get("TocOffset"); err != nil {
		return err
	}
	if a.Header.ContentOffset, err = get("ContentOffset"); err != nil {
		return err
	}
	if a.Header.EtocOffset, err = get("EtocOffset"); err != nil {
		return err
	}
	if a.Header.ItocOffset, err = get("ItocOffset"); err != nil {
		return err
	}
	if a.Header.GtocOffset, err = get("GtocOffset"); err != nil {
		return err
	}
	if a.Header.Files, err = get("Files"); err != nil {
		return err
	}
	if a.Header.Align, err = get("Align"); err != nil {
		return err
	}
	return nil
}

func (a *Archive) validateHeader() error {
	if a.Header.TocOffset <= 0 || a.Header.TocOffset >= a.size {
		return fmt.Errorf("cpk: TocOffset %d out of range", a.Header.TocOffset)
	}
	if a.Header.ContentOffset <= 0 || a.Header.ContentOffset > a.size {
		return fmt.Errorf("cpk: ContentOffset %d out of range", a.Header.ContentOffset)
	}
	if a.Header.EtocOffset <= 0 || a.Header.EtocOffset > a.size {
		return fmt.Errorf("cpk: EtocOffset %d out of range", a.Header.EtocOffset)
	}
	if a.Header.Align < 0 {
		return fmt.Errorf("cpk: negative Align %d", a.Header.Align)
	}
	a.tocOffset = a.Header.TocOffset
	a.contentOffset = a.Header.ContentOffset
	a.etocOffset = a.Header.EtocOffset
	a.align = a.Header.Align
	a.addOffset = addOffset(a.Header.TocOffset, a.Header.ContentOffset)
	return nil
}

// addOffset reproduces the CRI rule for the base added to each stored
// FileOffset: the smaller of the TOC and content offsets, treating the
// 0xFFFFFFFFFFFFFFFF sentinel as absent.
func addOffset(toc, content int64) int64 {
	tocUnknown := uint64(toc) == math.MaxUint64
	contentUnknown := uint64(content) == math.MaxUint64
	switch {
	case contentUnknown && tocUnknown:
		return 0
	case contentUnknown:
		return toc
	case tocUnknown:
		return content
	case content < toc:
		return content
	default:
		return toc
	}
}

func (a *Archive) readTOC() error {
	ident, err := a.readAt(a.tocOffset, 4)
	if err != nil {
		return err
	}
	if string(ident) != tocMagic {
		return fmt.Errorf("cpk: bad TOC ident %q at %d", ident, a.tocOffset)
	}
	table, obf, err := a.readPacket(a.tocOffset + 4)
	if err != nil {
		return fmt.Errorf("cpk: TOC packet: %w", err)
	}
	a.tocTable = table
	a.tocObfuscated = obf
	return nil
}

func (a *Archive) readETOC() error {
	ident, err := a.readAt(a.etocOffset, 4)
	if err != nil {
		return err
	}
	if string(ident) != etocMagic {
		return fmt.Errorf("cpk: bad ETOC ident %q at %d", ident, a.etocOffset)
	}
	table, obf, err := a.readPacket(a.etocOffset + 4)
	if err != nil {
		return fmt.Errorf("cpk: ETOC packet: %w", err)
	}
	a.etocTable = table
	a.etocObfuscated = obf
	return nil
}

func (a *Archive) captureSpans() error {
	if a.tocOffset < 0 || a.contentOffset < a.tocOffset || a.etocOffset < a.contentOffset {
		return fmt.Errorf("cpk: inconsistent region offsets toc=%d content=%d etoc=%d",
			a.tocOffset, a.contentOffset, a.etocOffset)
	}
	var err error
	if a.headerSpan, err = a.readAt(0, a.tocOffset); err != nil {
		return err
	}
	if a.tocSpan, err = a.readAt(a.tocOffset, a.contentOffset-a.tocOffset); err != nil {
		return err
	}
	if a.etocSpan, err = a.readAt(a.etocOffset, a.size-a.etocOffset); err != nil {
		return err
	}
	return nil
}

func (a *Archive) buildEntries() error {
	if a.tocTable == nil {
		return fmt.Errorf("cpk: archive has no TOC")
	}
	n := a.tocTable.numRows
	entries := make([]*Entry, 0, n)
	for i := 0; i < n; i++ {
		e := &Entry{}
		var err error
		if e.DirName, _, err = a.tocTable.stringValue(i, "DirName"); err != nil {
			return err
		}
		if e.FileName, _, err = a.tocTable.stringValue(i, "FileName"); err != nil {
			return err
		}
		if e.FileSize, _, err = a.tocTable.intValue(i, "FileSize"); err != nil {
			return err
		}
		if e.ExtractSize, _, err = a.tocTable.intValue(i, "ExtractSize"); err != nil {
			return err
		}
		if e.RelOffset, _, err = a.tocTable.intValue(i, "FileOffset"); err != nil {
			return err
		}
		if e.ID, _, err = a.tocTable.uintValue(i, "ID"); err != nil {
			return err
		}
		if e.UserString, _, err = a.tocTable.stringValue(i, "UserString"); err != nil {
			return err
		}
		e.Offset = e.RelOffset + a.addOffset
		if e.FileSize < 0 || e.ExtractSize < 0 {
			return fmt.Errorf("cpk: entry %d (%s) has negative size", i, e.Path())
		}
		if e.Offset < a.contentOffset || e.Offset+e.FileSize > a.etocOffset {
			return fmt.Errorf("cpk: entry %d (%s) payload [%d,%d) outside content region [%d,%d)",
				i, e.Path(), e.Offset, e.Offset+e.FileSize, a.contentOffset, a.etocOffset)
		}
		entries = append(entries, e)
	}
	a.Entries = entries
	return nil
}

// Member returns the entry with the given path, or nil.
func (a *Archive) Member(path string) *Entry {
	for _, e := range a.Entries {
		if e.Path() == path {
			return e
		}
	}
	return nil
}

// RawData returns the member payload exactly as stored (still compressed when
// the entry is compressed).
func (a *Archive) RawData(e *Entry) ([]byte, error) {
	if e.replacement != nil {
		return append([]byte(nil), e.replacement...), nil
	}
	return a.readAt(e.Offset, e.FileSize)
}

// Data returns the member payload with CRILAYLA compression removed when the
// entry declares an extract size larger than its stored size.
func (a *Archive) Data(e *Entry) ([]byte, error) {
	raw, err := a.RawData(e)
	if err != nil {
		return nil, err
	}
	if !e.Compressed() {
		return raw, nil
	}
	out, err := Decompress(raw)
	if err != nil {
		return nil, fmt.Errorf("cpk: %s: %w", e.Path(), err)
	}
	if int64(len(out)) != e.stagedExtractSize() {
		return nil, fmt.Errorf("cpk: %s: decompressed %d bytes, expected %d", e.Path(), len(out), e.stagedExtractSize())
	}
	return out, nil
}

// ReplaceMember stages a replacement payload only when it does not change the
// stored size. Size-changing edits require the full layout rewriter and are
// intentionally rejected so callers cannot silently produce a broken archive.
func (a *Archive) ReplaceMember(path string, raw []byte) error {
	e := a.Member(path)
	if e == nil {
		return fmt.Errorf("cpk: unknown member %q", path)
	}
	if int64(len(raw)) != e.FileSize {
		return fmt.Errorf("cpk: member %q stored size %d bytes, replacement %d bytes; size-changing edits are unsupported",
			path, e.FileSize, len(raw))
	}
	e.replacement = append([]byte(nil), raw...)
	return nil
}

// ReplaceMemberResized stages a replacement payload of arbitrary length for
// path. The caller supplies the payload to store verbatim (for a text edit this
// is the marshalled plaintext, so the member is stored uncompressed) together
// with its declared extract size — normally int64(len(newStored)). The layout is
// not touched here: RebuildVariable recomputes the content offsets and patches
// the TOC/header. The size-preserving Rebuild rejects an archive staged this way.
func (a *Archive) ReplaceMemberResized(path string, newStored []byte, newExtractSize int64) error {
	e := a.Member(path)
	if e == nil {
		return fmt.Errorf("cpk: unknown member %q", path)
	}
	if newExtractSize < 0 {
		return fmt.Errorf("cpk: member %q: negative extract size %d", path, newExtractSize)
	}
	e.replacement = append([]byte(nil), newStored...)
	e.resized = true
	e.newExtract = newExtractSize
	return nil
}

// AddMember stages a new member that is not present in the source TOC. The
// payload is stored verbatim (uncompressed) with extractSize equal to its
// length; the caller must stage content that the engine reads unchanged, such
// as a rebuilt texture atlas. RebuildVariable places the member after every
// existing member and appends the TOC and ETOC rows.
func (a *Archive) AddMember(dir, name string, stored []byte, extractSize int64) error {
	if name == "" || strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("cpk: invalid new member file name %q", name)
	}
	if strings.ContainsAny(dir, "/\\") {
		return fmt.Errorf("cpk: invalid new member directory %q", dir)
	}
	path := name
	if dir != "" {
		path = dir + "/" + name
	}
	if a.Member(path) != nil {
		return fmt.Errorf("cpk: member %q already exists", path)
	}
	if extractSize != int64(len(stored)) {
		return fmt.Errorf("cpk: new member %q must be stored uncompressed (extract %d, stored %d)", path, extractSize, len(stored))
	}
	var maxID uint64
	for _, e := range a.Entries {
		if e.ID > maxID {
			maxID = e.ID
		}
	}
	if maxID == math.MaxUint64 {
		return fmt.Errorf("cpk: member ID space exhausted")
	}
	e := &Entry{
		DirName:     dir,
		FileName:    name,
		FileSize:    int64(len(stored)),
		ExtractSize: extractSize,
		ID:          maxID + 1,
		replacement: append([]byte(nil), stored...),
		resized:     true,
		newExtract:  extractSize,
		added:       true,
	}
	a.Entries = append(a.Entries, e)
	return nil
}

func alignUp(value, align int64) int64 {
	if align <= 1 {
		return value
	}
	rem := value % align
	if rem == 0 {
		return value
	}
	return value + (align - rem)
}

// Rebuild writes a complete archive. For an unmodified archive the output is
// byte-identical to the source; same-size member replacements are written in
// place. The header, TOC and ETOC packets are preserved verbatim, so the
// rebuilt archive keeps the original layout, alignment and packing mode.
func (a *Archive) Rebuild(w io.Writer) (int64, error) {
	if err := a.checkLayoutUnchanged(); err != nil {
		return 0, err
	}
	cw := &countingWriter{w: w}
	if _, err := cw.Write(a.headerSpan); err != nil {
		return cw.n, err
	}
	if _, err := cw.Write(a.tocSpan); err != nil {
		return cw.n, err
	}

	ordered := make([]*Entry, len(a.Entries))
	copy(ordered, a.Entries)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Offset < ordered[j].Offset })

	cursor := a.contentOffset
	for _, e := range ordered {
		if pad := e.Offset - cursor; pad > 0 {
			if _, err := cw.Write(make([]byte, pad)); err != nil {
				return cw.n, err
			}
			cursor += pad
		} else if pad < 0 {
			return cw.n, fmt.Errorf("cpk: member %q overlaps previous payload by %d bytes", e.Path(), -pad)
		}
		data, err := a.RawData(e)
		if err != nil {
			return cw.n, err
		}
		if _, err := cw.Write(data); err != nil {
			return cw.n, err
		}
		cursor += int64(len(data))
	}
	if pad := a.etocOffset - cursor; pad > 0 {
		if _, err := cw.Write(make([]byte, pad)); err != nil {
			return cw.n, err
		}
	}
	if _, err := cw.Write(a.etocSpan); err != nil {
		return cw.n, err
	}
	if cw.n != a.size {
		return cw.n, fmt.Errorf("cpk: rebuild produced %d bytes, expected %d", cw.n, a.size)
	}
	return cw.n, nil
}

// RebuildVariable writes a complete archive with a recomputed content layout.
// Members staged with ReplaceMemberResized are stored verbatim (uncompressed when
// their declared extract size equals the payload length); members staged with
// AddMember are appended after the existing members and gain TOC/ETOC rows; every
// other member is copied byte-for-byte, without decompression or recompression.
// The member order, alignment and content start follow the source; TOC
// FileOffset/FileSize/ExtractSize and the derived header size fields are patched,
// and the TOC may grow into the slack that precedes the content region. Output is
// deterministic: a stable offset-order sort and zero padding. Unknown layout
// state and integer overflows are rejected.
func (a *Archive) RebuildVariable(w io.Writer) (int64, error) {
	if a.headerSpan == nil || a.tocSpan == nil || a.etocSpan == nil {
		return 0, fmt.Errorf("cpk: archive spans are not captured")
	}
	if a.headerTable == nil || a.tocTable == nil || a.etocTable == nil {
		return 0, fmt.Errorf("cpk: archive tables are not captured")
	}

	addedCount := 0
	for _, e := range a.Entries {
		if e.added {
			addedCount++
		}
	}
	existingCount := len(a.Entries) - addedCount

	// Existing members keep their source offsets unless one of them changed
	// size. A new member is appended after the last existing payload, so the
	// content layout of the base archive is preserved exactly.
	relayout := false
	for _, e := range a.Entries {
		if !e.added && e.resized {
			relayout = true
			break
		}
	}

	ordered := make([]*Entry, 0, len(a.Entries))
	existing := make([]*Entry, 0, existingCount)
	for _, e := range a.Entries {
		if !e.added {
			existing = append(existing, e)
		}
	}
	sort.SliceStable(existing, func(i, j int) bool { return existing[i].Offset < existing[j].Offset })
	ordered = append(ordered, existing...)
	for _, e := range a.Entries {
		if e.added {
			ordered = append(ordered, e)
		}
	}

	placement := make(map[*Entry]int64, len(ordered))
	var cursor int64
	var totalStored, totalExtract int64
	if relayout {
		cursor = a.contentOffset
		for _, e := range ordered {
			stored := e.stagedStoredSize()
			start := alignUp(cursor, a.align)
			if start > math.MaxInt64-stored {
				return 0, fmt.Errorf("cpk: member %q overflows the content layout", e.Path())
			}
			placement[e] = start
			cursor = start + stored
		}
	} else {
		// Preserve the source offsets, and append new members after the last
		// existing payload.
		for _, e := range existing {
			if e.Offset < a.contentOffset {
				return 0, fmt.Errorf("cpk: member %q offset %d below ContentOffset %d", e.Path(), e.Offset, a.contentOffset)
			}
			placement[e] = e.Offset
			if end := e.Offset + e.stagedStoredSize(); end > cursor {
				cursor = end
			}
		}
		for _, e := range a.Entries {
			if !e.added {
				continue
			}
			start := alignUp(cursor, a.align)
			placement[e] = start
			cursor = start + e.stagedStoredSize()
		}
	}
	for _, e := range ordered {
		stored := e.stagedStoredSize()
		extract := e.stagedExtractSize()
		if stored < 0 || extract < 0 {
			return 0, fmt.Errorf("cpk: member %q has a negative staged size", e.Path())
		}
		totalStored += stored
		totalExtract += extract
		if totalStored < 0 || totalExtract < 0 {
			return 0, fmt.Errorf("cpk: archive size counters overflow")
		}
	}
	newEtoc := alignUp(cursor, a.align)

	// Existing entries are identified by their source row index; a new member is
	// inserted by path order below.
	sourceIndex := make(map[*Entry]int, existingCount)
	for i, e := range a.Entries {
		if !e.added {
			sourceIndex[e] = i
		}
	}
	var sorted []*Entry
	if addedCount > 0 {
		sorted = append([]*Entry(nil), a.Entries...)
		sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Path() < sorted[j].Path() })
	}

	// Patch or rebuild the TOC rows. Adding a member rewrites the whole row
	// sequence in path order (the archive header declares Sorted=1), renumbers
	// IDs 0..N-1, and patches each member's staged sizes and offset.
	newTocBody := a.tocTable.raw
	if addedCount > 0 {
		builder, err := a.tocTable.newBuilder()
		if err != nil {
			return 0, err
		}
		rows := make([][]byte, 0, len(sorted))
		for idx, e := range sorted {
			off := placement[e]
			if off < a.addOffset {
				return 0, fmt.Errorf("cpk: member %q offset %d below add offset %d", e.Path(), off, a.addOffset)
			}
			e.ID = uint64(idx)
			var row []byte
			if e.added {
				row = a.tocTable.blankRow()
				ed, err := a.tocRowEdit(e, uint64(off-a.addOffset))
				if err != nil {
					return 0, err
				}
				if err := a.tocTable.applyRowEdit(builder, row, ed); err != nil {
					return 0, fmt.Errorf("cpk: TOC row %q: %w", e.Path(), err)
				}
			} else {
				row, err = a.tocTable.rowBytes(sourceIndex[e])
				if err != nil {
					return 0, err
				}
				for name, val := range map[string]uint64{
					"FileOffset":  uint64(off - a.addOffset),
					"FileSize":    uint64(e.stagedStoredSize()),
					"ExtractSize": uint64(e.stagedExtractSize()),
					"ID":          e.ID,
				} {
					ci, ok := a.tocTable.column(name)
					if !ok {
						return 0, fmt.Errorf("cpk: TOC missing column %q", name)
					}
					if err := putUTFUint(row, a.tocTable.columns[ci], val); err != nil {
						return 0, err
					}
				}
			}
			rows = append(rows, row)
		}
		newTocBody, err = a.tocTable.encodeRowSequence(rows, builder)
		if err != nil {
			return 0, err
		}
	} else {
		for i := 0; i < existingCount; i++ {
			e := a.Entries[i]
			off := placement[e]
			if off < a.addOffset {
				return 0, fmt.Errorf("cpk: member %q offset %d below add offset %d", e.Path(), off, a.addOffset)
			}
			if err := a.tocTable.setRowUint(i, "FileOffset", uint64(off-a.addOffset)); err != nil {
				return 0, err
			}
			if err := a.tocTable.setRowUint(i, "FileSize", uint64(e.stagedStoredSize())); err != nil {
				return 0, err
			}
			if err := a.tocTable.setRowUint(i, "ExtractSize", uint64(e.stagedExtractSize())); err != nil {
				return 0, err
			}
		}
	}

	var newTocSpan []byte
	if addedCount == 0 {
		var err error
		newTocSpan, err = splicePacketBody(a.tocSpan, newTocBody, a.tocObfuscated)
		if err != nil {
			return 0, err
		}
	} else {
		var err error
		newTocSpan, err = wrapPacket(a.tocSpan, newTocBody, a.tocObfuscated)
		if err != nil {
			return 0, err
		}
	}
	if maxSpan := a.contentOffset - a.tocOffset; int64(len(newTocSpan)) > maxSpan {
		return 0, fmt.Errorf("cpk: rebuilt TOC packet is %d bytes, only %d fit before ContentOffset", len(newTocSpan), maxSpan)
	}

	// Rebuild the ETOC. Rows are positionally parallel to the path-sorted TOC,
	// and the trailing empty sentinel stays last: adding a member inserts one
	// file row before it. The new member's timestamp stays 0; existing rows keep
	// their stored metadata.
	newEtocSpan := a.etocSpan
	if addedCount > 0 {
		if a.etocTable.numRows < existingCount {
			return 0, fmt.Errorf("cpk: ETOC has %d rows for %d existing members", a.etocTable.numRows, existingCount)
		}
		builder, err := a.etocTable.newBuilder()
		if err != nil {
			return 0, err
		}
		sentinel, err := a.etocTable.rowBytes(a.etocTable.numRows - 1)
		if err != nil {
			return 0, err
		}
		if a.etocTable.numRows == existingCount {
			sentinel = a.etocTable.blankRow()
		}
		rows := make([][]byte, 0, len(sorted)+1)
		for _, e := range sorted {
			if e.added {
				row := a.etocTable.blankRow()
				ed := utfRowEdit{Strings: map[string]string{}}
				if ci, ok := a.etocTable.column("LocalDir"); ok && a.etocTable.columns[ci].rowOffset >= 0 {
					ed.Strings["LocalDir"] = e.DirName
				}
				if err := a.etocTable.applyRowEdit(builder, row, ed); err != nil {
					return 0, fmt.Errorf("cpk: ETOC row %q: %w", e.Path(), err)
				}
				rows = append(rows, row)
				continue
			}
			row, err := a.etocTable.rowBytes(sourceIndex[e])
			if err != nil {
				return 0, err
			}
			rows = append(rows, row)
		}
		rows = append(rows, sentinel)
		body, err := a.etocTable.encodeRowSequence(rows, builder)
		if err != nil {
			return 0, err
		}
		newEtocSpan, err = wrapPacket(a.etocSpan, body, a.etocObfuscated)
		if err != nil {
			return 0, err
		}
	}

	// Patch the derived header fields in place (same packet body size).
	patchHeader := func(name string, v uint64, required bool) error {
		if !a.headerTable.hasColumn(name) {
			if required {
				return fmt.Errorf("cpk: header missing column %q", name)
			}
			return nil
		}
		return a.headerTable.setRowUint(0, name, v)
	}
	if err := patchHeader("EtocOffset", uint64(newEtoc), true); err != nil {
		return 0, err
	}
	if err := patchHeader("ContentSize", uint64(newEtoc-a.contentOffset), false); err != nil {
		return 0, err
	}
	if err := patchHeader("EtocSize", uint64(len(newEtocSpan)), false); err != nil {
		return 0, err
	}
	if err := patchHeader("TocSize", uint64(16+len(newTocBody)), false); err != nil {
		return 0, err
	}
	if err := patchHeader("Files", uint64(len(a.Entries)), true); err != nil {
		return 0, err
	}
	if addedCount > 0 {
		if err := patchHeader("Sorted", 1, false); err != nil {
			return 0, err
		}
	}
	if err := patchHeader("EnabledPackedSize", uint64(totalExtract), false); err != nil {
		return 0, err
	}
	if err := patchHeader("EnabledDataSize", uint64(totalStored), false); err != nil {
		return 0, err
	}
	newHeaderSpan, err := splicePacketBody(a.headerSpan, a.headerTable.raw, a.headerObfuscated)
	if err != nil {
		return 0, err
	}

	etocLen := int64(len(newEtocSpan))
	if newEtoc > math.MaxInt64-etocLen {
		return 0, fmt.Errorf("cpk: archive size overflows")
	}
	newSize := newEtoc + etocLen

	cw := &countingWriter{w: w}
	if _, err := cw.Write(newHeaderSpan); err != nil {
		return cw.n, err
	}
	if _, err := cw.Write(newTocSpan); err != nil {
		return cw.n, err
	}
	if pad := a.contentOffset - int64(len(newHeaderSpan)) - int64(len(newTocSpan)); pad > 0 {
		if _, err := cw.Write(make([]byte, pad)); err != nil {
			return cw.n, err
		}
	}
	cursor = a.contentOffset
	for _, e := range ordered {
		start := placement[e]
		if start < cursor {
			return cw.n, fmt.Errorf("cpk: member %q overlaps the previous payload by %d bytes", e.Path(), cursor-start)
		}
		if pad := start - cursor; pad > 0 {
			if _, err := cw.Write(make([]byte, pad)); err != nil {
				return cw.n, err
			}
		}
		data, err := a.RawData(e)
		if err != nil {
			return cw.n, err
		}
		if int64(len(data)) != e.stagedStoredSize() {
			return cw.n, fmt.Errorf("cpk: member %q payload %d bytes, expected %d", e.Path(), len(data), e.stagedStoredSize())
		}
		if _, err := cw.Write(data); err != nil {
			return cw.n, err
		}
		cursor = start + int64(len(data))
	}
	if pad := newEtoc - cursor; pad > 0 {
		if _, err := cw.Write(make([]byte, pad)); err != nil {
			return cw.n, err
		}
	} else if pad < 0 {
		return cw.n, fmt.Errorf("cpk: content layout overruns the ETOC region by %d bytes", -pad)
	}
	if _, err := cw.Write(newEtocSpan); err != nil {
		return cw.n, err
	}
	if cw.n != newSize {
		return cw.n, fmt.Errorf("cpk: variable rebuild produced %d bytes, expected %d", cw.n, newSize)
	}
	return cw.n, nil
}

// tocRowEdit builds the TOC row values for a member staged by AddMember.
func (a *Archive) tocRowEdit(e *Entry, fileOffset uint64) (utfRowEdit, error) {
	ed := utfRowEdit{Strings: map[string]string{}, Uints: map[string]uint64{}}
	// Only per-row string columns are writable; a constant column (such as the
	// real TOC's constant UserString) already carries its value in the schema.
	stringCol := func(name string) bool {
		ci, ok := a.tocTable.column(name)
		return ok && a.tocTable.columns[ci].rowOffset >= 0
	}
	if stringCol("DirName") {
		ed.Strings["DirName"] = e.DirName
	}
	if stringCol("FileName") {
		ed.Strings["FileName"] = e.FileName
	}
	if stringCol("UserString") {
		ed.Strings["UserString"] = e.UserString
	}
	for _, name := range []string{"FileSize", "ExtractSize", "FileOffset", "ID"} {
		if !a.tocTable.hasColumn(name) {
			return utfRowEdit{}, fmt.Errorf("cpk: TOC missing column %q", name)
		}
	}
	ed.Uints["FileSize"] = uint64(e.stagedStoredSize())
	ed.Uints["ExtractSize"] = uint64(e.stagedExtractSize())
	ed.Uints["FileOffset"] = fileOffset
	ed.Uints["ID"] = e.ID
	return ed, nil
}

// wrapPacket returns a new region holding a packet with the given body,
// preserving the region's identifier and LE wrapper and re-applying the XOR
// obfuscation when the source packet was obfuscated. Unlike splicePacketBody the
// body may change size.
func wrapPacket(span, body []byte, obfuscated bool) ([]byte, error) {
	if len(span) < utfIdentOff {
		return nil, fmt.Errorf("cpk: captured region too short for a packet")
	}
	out := make([]byte, utfIdentOff+len(body))
	copy(out[:utfIdentOff], span[:utfIdentOff])
	cpkLittleEndian.PutUint64(out[8:16], uint64(len(body)))
	if obfuscated {
		body = criXorMask(body)
	}
	copy(out[utfIdentOff:], body)
	return out, nil
}

// splicePacketBody returns a copy of a captured region with the @UTF packet body
// (which begins at utfIdentOff bytes into the region) replaced by body, re-applying
// the region's XOR obfuscation when needed. The body size must be unchanged.
func splicePacketBody(span, body []byte, obfuscated bool) ([]byte, error) {
	if len(span) < utfIdentOff {
		return nil, fmt.Errorf("cpk: captured region too short for a packet")
	}
	bodySize := int64(cpkLittleEndian.Uint64(span[8:16]))
	if bodySize != int64(len(body)) {
		return nil, fmt.Errorf("cpk: packet body size changed (%d -> %d)", bodySize, len(body))
	}
	if int64(utfIdentOff)+bodySize > int64(len(span)) {
		return nil, fmt.Errorf("cpk: packet body exceeds captured region")
	}
	out := append([]byte(nil), span...)
	if obfuscated {
		body = criXorMask(body)
	}
	copy(out[utfIdentOff:], body)
	return out, nil
}

// checkLayoutUnchanged rejects state the Step-1 writer cannot faithfully
// represent, so a size change cannot silently corrupt offsets.
func (a *Archive) checkLayoutUnchanged() error {
	if a.headerSpan == nil || a.tocSpan == nil || a.etocSpan == nil {
		return fmt.Errorf("cpk: archive spans are not captured")
	}
	total := int64(len(a.headerSpan)) + int64(len(a.tocSpan)) + int64(len(a.etocSpan))
	if total > a.size {
		return fmt.Errorf("cpk: captured spans exceed file size")
	}
	for _, e := range a.Entries {
		if e.added {
			return fmt.Errorf("cpk: member %q was added; use RebuildVariable to append it", e.Path())
		}
		if e.resized {
			return fmt.Errorf("cpk: member %q was resized; use RebuildVariable for size-changing rebuilds", e.Path())
		}
		if e.replacement != nil && int64(len(e.replacement)) != e.FileSize {
			return fmt.Errorf("cpk: member %q size changed; only size-preserving rebuilds are supported", e.Path())
		}
	}
	return nil
}

// Size returns the source archive size in bytes.
func (a *Archive) Size() int64 { return a.size }

// Close releases the underlying file when the archive was opened with OpenFile.
func (a *Archive) Close() error {
	if a.closer != nil {
		return a.closer.Close()
	}
	return nil
}

// ContentOffset returns the start of the member data region.
func (a *Archive) ContentOffset() int64 { return a.contentOffset }

// EtocOffset returns the start of the ETOC region.
func (a *Archive) EtocOffset() int64 { return a.etocOffset }

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}
