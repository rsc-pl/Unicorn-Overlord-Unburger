// Package sourcecache recognises the operator's game containers and manages a
// verified, reusable extraction cache for the resources a build needs.
//
// Recognition reads only plaintext container metadata: the PFS0 header and
// string table of an NSP, and the embedded ContentMeta XML (a plaintext member of
// the NSP). No key material is used and no decryption is performed, so the
// package works without the operator's keys. A game-card image (XCI) has no
// plaintext metadata this package can read without an extractor, so it is
// detected and reported as requiring the extractor path rather than guessed at.
//
// The cache holds only the resources a build consumes — the effective
// localization archive and (for the planned voice work) the executable `main`
// excerpt — alongside a manifest that fingerprints the container they came from.
// A cache entry is reused only after its artifact identity is re-verified; a
// damaged entry is quarantined (renamed aside), never deleted, and re-extracted.
package sourcecache

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

// pfs0HeaderSize is the fixed part of a PFS0 header.
const pfs0HeaderSize = 0x10

// pfs0EntrySize is the size of one PFS0 file-table entry.
const pfs0EntrySize = 0x18

// maxPFS0Files bounds the file count a header may declare, so a corrupt or
// hostile header cannot make the parser allocate unreasonably.
const maxPFS0Files = 1 << 16

// maxStringTableSize bounds the string table read.
const maxStringTableSize = 1 << 20

// PFS0Member is one file inside a PFS0 (NSP) container.
type PFS0Member struct {
	Name   string
	Offset int64 // relative to the start of the data region
	Size   int64
}

// PFS0 is the parsed plaintext header of an NSP container.
type PFS0 struct {
	// DataStart is the absolute offset where member data begins.
	DataStart int64
	Members   []PFS0Member
}

// Member returns the named member, or nil.
func (p *PFS0) Member(name string) *PFS0Member {
	for i := range p.Members {
		if p.Members[i].Name == name {
			return &p.Members[i]
		}
	}
	return nil
}

// ErrNotPFS0 reports a file whose first bytes are not the PFS0 magic.
var ErrNotPFS0 = errors.New("sourcecache: not a PFS0/NSP container")

// ParsePFS0 reads the plaintext PFS0 header and string table of an NSP. It reads
// only the bounded header region; member payloads are never touched.
func ParsePFS0(f io.ReaderAt, size int64) (*PFS0, error) {
	if size < pfs0HeaderSize {
		return nil, fmt.Errorf("%w: file too small (%d bytes)", ErrNotPFS0, size)
	}
	head := make([]byte, pfs0HeaderSize)
	if _, err := f.ReadAt(head, 0); err != nil {
		return nil, fmt.Errorf("sourcecache: reading PFS0 header: %w", err)
	}
	if !bytes.Equal(head[0:4], []byte("PFS0")) {
		return nil, ErrNotPFS0
	}
	count := int(binary.LittleEndian.Uint32(head[4:8]))
	strTabSize := int64(binary.LittleEndian.Uint32(head[8:12]))
	switch {
	case count < 0 || count > maxPFS0Files:
		return nil, fmt.Errorf("sourcecache: implausible PFS0 file count %d", count)
	case strTabSize < 0 || strTabSize > maxStringTableSize:
		return nil, fmt.Errorf("sourcecache: implausible PFS0 string table size %d", strTabSize)
	}
	entryEnd := int64(pfs0HeaderSize) + int64(count)*pfs0EntrySize
	dataStart := entryEnd + strTabSize
	if dataStart > size {
		return nil, fmt.Errorf("sourcecache: PFS0 header extends past end of file (%d > %d)", dataStart, size)
	}

	entries := make([]byte, int64(count)*pfs0EntrySize)
	if count > 0 {
		if _, err := f.ReadAt(entries, pfs0HeaderSize); err != nil {
			return nil, fmt.Errorf("sourcecache: reading PFS0 file table: %w", err)
		}
	}
	strTab := make([]byte, strTabSize)
	if strTabSize > 0 {
		if _, err := f.ReadAt(strTab, entryEnd); err != nil {
			return nil, fmt.Errorf("sourcecache: reading PFS0 string table: %w", err)
		}
	}

	p := &PFS0{DataStart: dataStart, Members: make([]PFS0Member, 0, count)}
	for i := 0; i < count; i++ {
		base := i * pfs0EntrySize
		off := int64(binary.LittleEndian.Uint64(entries[base : base+8]))
		sz := int64(binary.LittleEndian.Uint64(entries[base+8 : base+16]))
		nameOff := int64(binary.LittleEndian.Uint32(entries[base+16 : base+20]))
		if off < 0 || sz < 0 || off+sz > size-dataStart {
			return nil, fmt.Errorf("sourcecache: PFS0 member %d is outside the container", i)
		}
		if nameOff < 0 || nameOff >= strTabSize {
			return nil, fmt.Errorf("sourcecache: PFS0 member %d has an invalid name offset", i)
		}
		name := string(strTab[nameOff:])
		if idx := bytes.IndexByte([]byte(name), 0); idx >= 0 {
			name = name[:idx]
		}
		p.Members = append(p.Members, PFS0Member{Name: name, Offset: off, Size: sz})
	}
	return p, nil
}

// ReadMember returns up to max bytes of a member's payload. It is used for the
// small plaintext metadata members (the embedded ContentMeta XML); it must never
// be used for a large payload.
func (p *PFS0) ReadMember(f io.ReaderAt, m *PFS0Member, max int64) ([]byte, error) {
	if m == nil {
		return nil, fmt.Errorf("sourcecache: no such member")
	}
	n := m.Size
	if max > 0 && n > max {
		n = max
	}
	buf := make([]byte, n)
	if n == 0 {
		return buf, nil
	}
	if _, err := f.ReadAt(buf, p.DataStart+m.Offset); err != nil {
		return nil, fmt.Errorf("sourcecache: reading member %q: %w", m.Name, err)
	}
	return buf, nil
}

// OpenPFS0 opens the file read-only, parses its PFS0 header and closes it.
func OpenPFS0(path string) (*PFS0, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	return ParsePFS0(f, info.Size())
}
