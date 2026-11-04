package build

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"uo-unburger/internal/importer"
)

// MemberDir is the CPK directory that holds every message sheet member.
const MemberDir = "MsgSheet"

// Edit is one resolved per-record text change for both editions. Honorifics is
// the canonical text; NoHonorifics always has a value (it falls back to the
// canonical text when no sparse override applies).
type Edit struct {
	RecordID     string
	File         string // e.g. "UcSysMsg.fms"
	Member       string // e.g. "MsgSheet/UcSysMsg.fms"
	Slot         int
	Honorifics   []byte
	NoHonorifics []byte
}

// FilePlan groups every edit that targets one CPK member.
type FilePlan struct {
	File   string
	Member string
	// Edits maps slot index to its resolved edit.
	Edits map[int]*Edit
}

// Plan is the shared intermediate edit plan derived from one resolution. Both
// editions are produced from this single plan, so they cannot drift apart.
type Plan struct {
	// Files maps member path to its plan.
	Files map[string]*FilePlan
	// Order is the sorted member path order.
	Order []string

	AppliedRecords int
	Rejected       []string
	Unresolved     []string
	TokenMismatch  []string
	TokenAllowed   int
}

// DerivePlan converts a resolution into a per-member edit plan. A record is
// editable only when it resolved as APPLIED (a validated canonical row); any
// other status needs no edit. Malformed record IDs and duplicate targets are
// hard errors.
func DerivePlan(res *importer.Resolution) (*Plan, error) {
	if res == nil {
		return nil, fmt.Errorf("build: nil resolution")
	}
	p := &Plan{Files: map[string]*FilePlan{}}

	for _, rr := range res.Records {
		switch rr.Status {
		case importer.StatusApplied:
			file, slot, ok := splitRecordID(rr.RecordID)
			if !ok {
				return nil, fmt.Errorf("build: malformed record id %q", rr.RecordID)
			}
			member := MemberDir + "/" + file
			fp := p.Files[member]
			if fp == nil {
				fp = &FilePlan{File: file, Member: member, Edits: map[int]*Edit{}}
				p.Files[member] = fp
			}
			if prev, dup := fp.Edits[slot]; dup {
				return nil, fmt.Errorf("build: duplicate edit target %s (also %s)", rr.RecordID, prev.RecordID)
			}
			fp.Edits[slot] = &Edit{
				RecordID:     rr.RecordID,
				File:         file,
				Member:       member,
				Slot:         slot,
				Honorifics:   []byte(rr.Canonical),
				NoHonorifics: []byte(rr.NoHonorifics),
			}
			p.AppliedRecords++
		case importer.StatusRejected:
			p.Rejected = append(p.Rejected, rr.RecordID)
		case importer.StatusUnresolved:
			p.Unresolved = append(p.Unresolved, rr.RecordID)
		}
		if rr.TokenFlag == importer.TokenMismatchF {
			p.TokenMismatch = append(p.TokenMismatch, rr.RecordID)
		}
		if rr.TokenFlag == importer.TokenAllowed {
			p.TokenAllowed++
		}
	}

	for member := range p.Files {
		p.Order = append(p.Order, member)
	}
	sort.Strings(p.Order)
	sortRecordIDs(p.Rejected)
	sortRecordIDs(p.Unresolved)
	sortRecordIDs(p.TokenMismatch)
	return p, nil
}

// splitRecordID returns the FMS file name and slot number of a "<file>:<slot>" id.
func splitRecordID(id string) (file string, slot int, ok bool) {
	i := strings.LastIndex(id, ":")
	if i <= 0 {
		return "", 0, false
	}
	n, err := strconv.Atoi(id[i+1:])
	if err != nil || n < 0 {
		return "", 0, false
	}
	return id[:i], n, true
}
