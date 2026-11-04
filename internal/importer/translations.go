package importer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// The consolidated neutral translation files are the public, repository-visible
// contract of this package. They carry exactly two tables:
//
//	honorifics.tsv      record_id, source_jp_sha, original_en_sha, english_text
//	no-honorifics.tsv   record_id, canonical_sha256, english_text
//
// The honorifics table is the canonical edition: one source-linked row per
// corrected record, where source_jp_sha and original_en_sha are the first twelve
// lowercase hex characters of the SHA-1 digest of the verbatim Japanese source
// and of the official Western English text of that record. The second table is
// the sparse alternate edition: canonical_sha256 is the full lowercase hex
// SHA-256 of the canonical english_text the override replaces, so every override
// is cryptographically tied to the exact canonical row it amends.
//
// Both files are tab-separated and may use RFC 4180 double quoting for text
// fields that contain tabs or newlines. Header order is fixed and validated
// exactly; an unknown or reordered header is a hard error naming the file and
// the offending line, never a silent reinterpretation.
const (
	// HonorificsFile is the file name of the consolidated canonical table.
	HonorificsFile = "honorifics.tsv"
	// NoHonorificsFile is the file name of the consolidated sparse-override table.
	NoHonorificsFile = "no-honorifics.tsv"

	honorificsHeader   = "record_id\tsource_jp_sha\toriginal_en_sha\tenglish_text"
	noHonorificsHeader = "record_id\tcanonical_sha256\tenglish_text"
)

// UsesNeutralFormat reports whether dir holds the consolidated translation
// files, in which case they are the only files this package reads from that
// directory.
func UsesNeutralFormat(dir string) bool {
	if dir == "" {
		return false
	}
	fi, err := os.Stat(filepath.Join(dir, HonorificsFile))
	return err == nil && !fi.IsDir()
}

// NeutralFormat is the Dataset.Format value for the consolidated files.
const NeutralFormat = "honorifics.tsv+no-honorifics.tsv"

// loadNeutral parses the consolidated neutral translation files from dir.
//
// Every row is validated before it becomes part of the ledger: the header must
// match the documented columns exactly, record IDs must have the
// "<fms>:<slot>" shape, the two SHA-1 columns must be twelve lowercase hex
// characters and the override's canonical_sha256 must be sixty-four lowercase
// hex characters. A duplicate record ID is deduplicated when the payload is
// identical and refused otherwise, so a conflicting row can never be resolved
// by file order.
//
// The override table is optional: a directory that only carries the canonical
// table resolves with no overrides. A present but malformed override table is an
// error rather than an empty override set.
func loadNeutral(dir string) (*Checkpoint, error) {
	cp := &Checkpoint{
		Canonical: map[string]canonicalEntry{},
		Overrides: map[string]overrideEntry{},
	}

	canonicalPath := filepath.Join(dir, HonorificsFile)
	rows, err := readTSV(canonicalPath)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%s: file is empty", HonorificsFile)
	}
	if got := headerString(rows[0]); got != honorificsHeader {
		return nil, fmt.Errorf("%s: unexpected header %q (want %q)", HonorificsFile, got, honorificsHeader)
	}
	for i, row := range rows[1:] {
		if len(row) != 4 {
			return nil, fmt.Errorf("%s: line %d: expected 4 columns, got %d", HonorificsFile, i+2, len(row))
		}
		id := row[0]
		if !validRecordID(id) {
			return nil, fmt.Errorf("%s: line %d: invalid record_id %q", HonorificsFile, i+2, id)
		}
		if !hex12Re.MatchString(row[1]) {
			return nil, fmt.Errorf("%s: %s: malformed source_jp_sha %q (want 12 lowercase hex)", HonorificsFile, id, row[1])
		}
		if !hex12Re.MatchString(row[2]) {
			return nil, fmt.Errorf("%s: %s: malformed original_en_sha %q (want 12 lowercase hex)", HonorificsFile, id, row[2])
		}
		text := row[3]
		if prev, dup := cp.Canonical[id]; dup {
			if prev.Text == text {
				continue // identical duplicate: dedup silently
			}
			return nil, fmt.Errorf("conflicting canonical rows for %s in %s", id, HonorificsFile)
		}
		cp.Canonical[id] = canonicalEntry{
			RecordID:    id,
			SourceJPSha: row[1],
			OriginalSh:  row[2],
			Text:        text,
			File:        HonorificsFile,
		}
	}
	if len(cp.Canonical) == 0 {
		return nil, fmt.Errorf("%s: empty table (no canonical rows)", HonorificsFile)
	}

	overridePath := filepath.Join(dir, NoHonorificsFile)
	cp.Dataset = Dataset{
		Format:          NeutralFormat,
		CanonicalFile:   HonorificsFile,
		CanonicalSHA256: fileSHA256Hex(canonicalPath),
		CanonicalRows:   len(cp.Canonical),
		AlternateFile:   NoHonorificsFile,
	}
	fi, err := os.Stat(overridePath)
	if err != nil || fi.IsDir() {
		return cp, nil // no sparse overrides in this directory
	}
	overrides, err := readTSV(overridePath)
	if err != nil {
		return nil, err
	}
	if len(overrides) == 0 {
		return nil, fmt.Errorf("%s: file is empty", NoHonorificsFile)
	}
	if got := headerString(overrides[0]); got != noHonorificsHeader {
		return nil, fmt.Errorf("%s: unexpected header %q (want %q)", NoHonorificsFile, got, noHonorificsHeader)
	}
	for i, row := range overrides[1:] {
		if len(row) != 3 {
			return nil, fmt.Errorf("%s: line %d: expected 3 columns, got %d", NoHonorificsFile, i+2, len(row))
		}
		id := row[0]
		if !validRecordID(id) {
			return nil, fmt.Errorf("%s: line %d: invalid record_id %q", NoHonorificsFile, i+2, id)
		}
		if !hex64Re.MatchString(row[1]) {
			return nil, fmt.Errorf("%s: %s: malformed canonical_sha256 %q (want 64 lowercase hex)", NoHonorificsFile, id, row[1])
		}
		if _, dup := cp.Overrides[id]; dup {
			return nil, fmt.Errorf("%s: duplicate override row for %s", NoHonorificsFile, id)
		}
		cp.Overrides[id] = overrideEntry{
			RecordID:    id,
			NoHon:       row[2],
			CanonicalSH: row[1],
			File:        NoHonorificsFile,
		}
	}

	cp.Dataset = Dataset{
		Format:           NeutralFormat,
		CanonicalFile:    HonorificsFile,
		CanonicalSHA256:  fileSHA256Hex(canonicalPath),
		CanonicalRows:    len(cp.Canonical),
		AlternateFile:    NoHonorificsFile,
		AlternateSHA256:  fileSHA256Hex(overridePath),
		AlternateRows:    len(cp.Overrides),
		AlternatePresent: true,
	}
	return cp, nil
}

// fileSHA256Hex is the lowercase hex SHA-256 of a file's bytes, or "" when the
// file cannot be read. It is used only for dataset identity in reports.
func fileSHA256Hex(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
