package importer

import (
	"fmt"
	"sort"

	"uo-unburger/internal/cpk"
	"uo-unburger/internal/fms"
)

// SourceMode names where the official English text used to validate a canonical
// row's original_en_sha came from. It is provenance for reports; it never
// changes how a record is resolved.
type SourceMode string

const (
	// ModeLedgerOnly validates the translation tables alone: record shapes,
	// hash formats, override linkage, empty/redundant/orphan rejection and the
	// exact variant diff. original_en_sha is NOT verified against the user's
	// game data in this mode (that happens against a source CPK at build time).
	ModeLedgerOnly SourceMode = "LEDGER_ONLY"
	// ModeVerifiedSourceCPK validates original_en_sha against the actual
	// official English slot text read read-only from the user's source
	// localization CPK. source_jp_sha is provenance only in this mode: the
	// western source archive carries no Japanese script, so the JP hash is
	// format-checked but never re-derived.
	ModeVerifiedSourceCPK SourceMode = "VERIFIED_SOURCE_CPK"
	// ModeReferenceCorpus validates against a full reference JP/EN corpus, so
	// both source_jp_sha and original_en_sha are re-derived.
	ModeReferenceCorpus SourceMode = "REFERENCE_CORPUS"
)

// MemberDir is the CPK directory that holds every message sheet member.
const MemberDir = "MsgSheet"

// LoadSourceSlotsCPK opens the user's source localization CPK read-only and
// returns the official English slot text for each requested record ID keyed by
// record ID. Only the members referenced by ids are decompressed and parsed;
// nothing else in the archive is read.
//
// A record whose member is missing from the archive, or whose slot number is
// outside its member, is a hard error naming the record, so a record that
// targets a non-existent slot can never be silently resolved.
func LoadSourceSlotsCPK(path string, ids []string) (map[string]CorpusRow, error) {
	if path == "" {
		return nil, fmt.Errorf("importer: source CPK path is empty")
	}
	a, err := cpk.OpenFile(path)
	if err != nil {
		return nil, fmt.Errorf("importer: source CPK: %w", err)
	}
	defer a.Close()

	byMember := map[string][]string{}
	for _, id := range ids {
		file, _, ok := splitRecordID(id)
		if !ok {
			return nil, fmt.Errorf("importer: malformed record id %q", id)
		}
		member := MemberDir + "/" + file
		byMember[member] = append(byMember[member], id)
	}
	members := make([]string, 0, len(byMember))
	for m := range byMember {
		members = append(members, m)
	}
	sort.Strings(members)

	out := map[string]CorpusRow{}
	for _, member := range members {
		e := a.Member(member)
		if e == nil {
			return nil, fmt.Errorf("importer: no such member %q in the source CPK", member)
		}
		data, err := a.Data(e)
		if err != nil {
			return nil, fmt.Errorf("importer: %s: %w", member, err)
		}
		sheet, err := fms.Parse(data)
		if err != nil {
			return nil, fmt.Errorf("importer: %s: %w", member, err)
		}
		ids := append([]string(nil), byMember[member]...)
		sort.Strings(ids)
		for _, id := range ids {
			_, slot, _ := splitRecordID(id)
			if slot >= uint64(sheet.SlotCount()) {
				return nil, fmt.Errorf("importer: unknown record target %s: slot %d outside member %s (slot count %d)",
					id, slot, member, sheet.SlotCount())
			}
			out[id] = CorpusRow{OfficialEN: string(sheet.Slot(int(slot)))}
		}
	}
	return out, nil
}

// CheckAgainstSource resolves the translation ledger against the user's verified
// source localization CPK (the public, corpus-free path used by a normal build).
//
// Every canonical row's original_en_sha must equal the first twelve hex
// characters of the SHA-1 digest of the actual official English slot text in the
// source archive; source_jp_sha is retained as provenance and format-checked
// only.
func CheckAgainstSource(dir, sourcePath string) (*Resolution, error) {
	if sourcePath == "" {
		return nil, fmt.Errorf("importer: source CPK path is required")
	}
	cp, err := LoadCheckpoint(dir)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(cp.Canonical)+len(cp.Overrides))
	for id := range cp.Canonical {
		ids = append(ids, id)
	}
	for id := range cp.Overrides {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	rows, err := LoadSourceSlotsCPK(sourcePath, ids)
	if err != nil {
		return nil, err
	}
	return cp.ResolveWith(ResolveOptions{
		Corpus:   rows,
		Official: true,
		VerifyJP: false,
		Mode:     ModeVerifiedSourceCPK,
	}), nil
}

// CheckLedger validates the translation ledger alone: the two consolidated
// files are parsed and every structural rule is applied, but no official source
// text is available, so original_en_sha is not verified and control-token parity
// cannot be computed. It is the mode used when neither a source archive nor a
// reference corpus is supplied.
func CheckLedger(dir string) (*Resolution, error) {
	cp, err := LoadCheckpoint(dir)
	if err != nil {
		return nil, err
	}
	return cp.ResolveWith(ResolveOptions{Mode: ModeLedgerOnly}), nil
}
