// Package importer loads the translation ledger and resolves the canonical and
// no-honorifics editions of each record.
//
// The package is revision-agnostic: it discovers its inputs by file name rather
// than by hard-coded revision numbers. It performs no I/O against private paths
// of its own; all inputs (translation directory, corpus) are supplied by the
// caller.
//
// The source data are the two consolidated files described in translations.go:
//
//	<dir>/honorifics.tsv      canonical rows
//	<dir>/no-honorifics.tsv  sparse no-honorifics overrides
//
// These tables are the only translation input this package reads. A directory
// without honorifics.tsv is an error rather than a fallback to another layout.
package importer

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// CorpusRow is the minimal corpus data needed to validate a canonical row.
// JP and OfficialEN are the verbatim UTF-8 source and Western English strings.
type CorpusRow struct {
	JP         string
	OfficialEN string
}

// Linkage describes how an override is tied back to its canonical text.
type Linkage string

const (
	LinkNone     Linkage = "NONE"
	LinkSHA256   Linkage = "VERIFIED_SHA256"
	LinkText     Linkage = "VERIFIED_TEXT"
	LinkUnverifd Linkage = "UNVERIFIED"
)

// TokenFlag classifies control-token parity against the official English.
type TokenFlag string

const (
	TokenOK        TokenFlag = ""
	TokenAllowed   TokenFlag = "ALLOWED_WITH_EXCEPTION"
	TokenMismatchF TokenFlag = "TOKEN_MISMATCH"
)

// Status is the per-record resolution status.
type Status string

const (
	StatusApplied    Status = "APPLIED"
	StatusRejected   Status = "REJECTED"
	StatusUnresolved Status = "UNRESOLVED"
)

// canonicalEntry is one resolved canonical row.
type canonicalEntry struct {
	RecordID    string
	SourceJPSha string
	OriginalSh  string
	Text        string
	File        string // source file name, for diagnostics
}

// overrideEntry is one parsed no-honorifics override row.
type overrideEntry struct {
	RecordID    string
	NoHon       string
	CanonicalEN string // optional text linkage
	ProposedEN  string // optional text linkage (source-variant shape)
	CanonicalSH string // optional SHA-256 linkage
	File        string
}

// Checkpoint is a parsed, checkpoint-agnostic view of the translation ledger.
type Checkpoint struct {
	// Canonical maps record_id to the resolved canonical row.
	Canonical map[string]canonicalEntry
	// Overrides maps record_id to the parsed override row.
	Overrides map[string]overrideEntry
	// Dataset identifies the tables the checkpoint was loaded from.
	Dataset Dataset
}

// RecordResult is the resolution outcome for a single record.
type RecordResult struct {
	RecordID     string
	Status       Status
	Canonical    string
	NoHonorifics string
	Linkage      Linkage
	TokenFlag    TokenFlag
	TokenDiff    map[string]int
	// SourceEN is the official English text the record was validated against
	// when official text was available (verified source CPK or reference
	// corpus). It is empty in ledger-only mode.
	SourceEN string
	Errors   []string
}

// Counts summarises a Resolution.
type Counts struct {
	Canonical           int
	Overrides           int
	Applied             int
	Rejected            int
	Unresolved          int
	OverridesApplied    int
	TokenAllowed        int
	TokenMismatch       int
	UnresolvedCanonical int
	// OfficialVerified counts canonical rows whose original_en_sha was
	// re-derived from real official English text (a verified source CPK or a
	// reference corpus). It is zero in ledger-only mode.
	OfficialVerified int
}

// Dataset identifies the translation tables a checkpoint was loaded from, so a
// report can name the exact rows and file digests an output was built from. The
// digest is over the file bytes as shipped; the tables are read-only inputs and
// are never rewritten here.
type Dataset struct {
	Format           string
	CanonicalFile    string
	CanonicalSHA256  string
	CanonicalRows    int
	AlternateFile    string
	AlternateSHA256  string
	AlternateRows    int
	AlternatePresent bool
}

// ResolveOptions carries the source text a checkpoint is resolved against and
// the provenance of that text.
type ResolveOptions struct {
	// Corpus maps record_id to the source row. It may be empty for ledger-only
	// resolution.
	Corpus map[string]CorpusRow
	// Exceptions is the markup-exception allowlist: record IDs whose
	// italic-only control-token difference is accepted.
	Exceptions map[string]bool
	// VerifyJP re-derives source_jp_sha from the corpus Japanese text. It is
	// only meaningful when the corpus carries the Japanese source (the
	// reference-corpus mode); a western source CPK has no JP script.
	VerifyJP bool
	// Official reports whether Corpus carries the official English text, which
	// enables original_en_sha verification and control-token parity.
	Official bool
	// Mode records the provenance of the source text for reporting.
	Mode SourceMode
}

// Resolution is the deterministic result of resolving a checkpoint.
type Resolution struct {
	// Records is sorted by RecordID.
	Records []RecordResult
	// Unresolved lists corpus record IDs with no canonical row, sorted.
	Unresolved []string
	// DiffPass reports whether the set of records whose two variants differ
	// equals the authorised override set exactly.
	DiffPass  bool
	DiffDrift []string
	Counts    Counts
	// SourceMode names where the official English text came from.
	SourceMode SourceMode
	// OfficialVerified reports whether original_en_sha was checked against real
	// official English text.
	OfficialVerified bool
	// JPVerified reports whether source_jp_sha was re-derived from Japanese
	// source text (false for the western source CPK: JP is provenance only).
	JPVerified bool
	// Dataset identifies the translation tables that were resolved.
	Dataset Dataset
}

var recordIDRe = regexp.MustCompile(`^[A-Za-z0-9_]+\.fms:[0-9]+$`)
var hex12Re = regexp.MustCompile(`^[0-9a-f]{12}$`)
var hex64Re = regexp.MustCompile(`^[0-9a-f]{64}$`)

// validRecordID reports whether id has the exact "<fms>:<slot>" shape.
func validRecordID(id string) bool { return recordIDRe.MatchString(id) }

// splitRecordID returns the FMS file name and slot number of a valid record ID.
func splitRecordID(id string) (file string, slot uint64, ok bool) {
	i := strings.LastIndex(id, ":")
	if i < 0 {
		return "", 0, false
	}
	slot, err := strconv.ParseUint(id[i+1:], 10, 64)
	if err != nil {
		return "", 0, false
	}
	return id[:i], slot, true
}

// sha1Hex12 is the first 12 lowercase hex characters of the SHA-1 digest of s.
func sha1Hex12(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}

// sha256Hex is the full lowercase hex SHA-256 digest of s.
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// LoadCheckpoint parses the consolidated translation tables in dir into a
// Checkpoint. It is the only translation loader: honorifics.tsv is required and
// no-honorifics.tsv is optional.
func LoadCheckpoint(dir string) (*Checkpoint, error) {
	if !UsesNeutralFormat(dir) {
		return nil, fmt.Errorf("%s: no translation files found (expected %s)",
			filepath.Base(dir), HonorificsFile)
	}
	return loadNeutral(dir)
}

// Resolve validates every canonical row against the corpus, applies overrides
// to the no-honorifics variant and returns a deterministic resolution.
//
// corpus maps record_id to the JP source and official Western English bytes.
// exceptions is the set of record IDs for which an italic-only control-token
// difference is authorised. It is the reference-corpus entry point and preserves
// the original semantics (both hashes re-derived, control-token parity computed).
func (c *Checkpoint) Resolve(corpus map[string]CorpusRow, exceptions map[string]bool) *Resolution {
	return c.ResolveWith(ResolveOptions{
		Corpus:     corpus,
		Exceptions: exceptions,
		VerifyJP:   true,
		Official:   true,
		Mode:       ModeReferenceCorpus,
	})
}

// ResolveWith resolves a checkpoint against the source text described by opts.
// It is the single resolution engine behind every front end (build, import
// --check, qa and the interactive interface), so no two front ends can drift.
//
// When opts.Official is false the ledger is validated on its own: record shapes,
// hash formats, override linkage, empty/redundant/orphan rejection and the exact
// variant diff are still applied, but original_en_sha is not verified and no
// control-token parity is computed (there is no official text to compare with).
func (c *Checkpoint) ResolveWith(opts ResolveOptions) *Resolution {
	res := &Resolution{SourceMode: opts.Mode, OfficialVerified: opts.Official, JPVerified: opts.VerifyJP, Dataset: c.Dataset}
	corpus := opts.Corpus
	if corpus == nil {
		corpus = map[string]CorpusRow{}
	}
	exceptions := opts.Exceptions

	canonicalIDs := make([]string, 0, len(c.Canonical))
	for id := range c.Canonical {
		canonicalIDs = append(canonicalIDs, id)
	}
	sort.Strings(canonicalIDs)

	// Records whose two resolved variants differ.
	diffSet := map[string]bool{}
	// Override IDs that were applied to a valid canonical row.
	appliedOverrides := map[string]bool{}

	for _, id := range canonicalIDs {
		e := c.Canonical[id]
		rr := RecordResult{RecordID: id, Canonical: e.Text}

		if !hex12Re.MatchString(e.SourceJPSha) || !hex12Re.MatchString(e.OriginalSh) {
			rr.Status = StatusRejected
			rr.Errors = append(rr.Errors, "malformed source hash (want 12 lowercase hex)")
			res.Records = append(res.Records, rr)
			continue
		}

		if opts.Official {
			crow, ok := corpus[id]
			if !ok {
				rr.Status = StatusUnresolved
				rr.Errors = append(rr.Errors, "no source row")
				res.Counts.UnresolvedCanonical++
				res.Records = append(res.Records, rr)
				continue
			}
			rr.SourceEN = crow.OfficialEN
			if opts.VerifyJP && sha1Hex12(crow.JP) != e.SourceJPSha {
				rr.Status = StatusRejected
				rr.Errors = append(rr.Errors, "STALE_HASH source_jp_sha")
			}
			if sha1Hex12(crow.OfficialEN) != e.OriginalSh {
				rr.Status = StatusRejected
				rr.Errors = append(rr.Errors, "STALE_HASH original_en_sha")
			} else {
				res.Counts.OfficialVerified++
			}

			// Control-token parity: canonical proposal vs official English.
			diff := TokenDiff(Tokenize(e.Text), Tokenize(crow.OfficialEN))
			if len(diff) > 0 {
				rr.TokenDiff = diff
				if exceptions[id] && italicOnlyDiff(diff) {
					rr.TokenFlag = TokenAllowed
				} else {
					rr.TokenFlag = TokenMismatchF
				}
			}
		}

		// Apply the no-honorifics override, if any.
		if ov, has := c.Overrides[id]; has {
			rr.Linkage = ov.linkage(e.Text)
			switch {
			case ov.NoHon == "":
				rr.Status = StatusRejected
				rr.Errors = append(rr.Errors, "empty no_honorifics_en override")
			case ov.CanonicalSH != "" && !hex64Re.MatchString(ov.CanonicalSH):
				rr.Status = StatusRejected
				rr.Errors = append(rr.Errors, "malformed canonical_sha256 (want 64 lowercase hex)")
			case ov.CanonicalSH != "" && sha256Hex(e.Text) != ov.CanonicalSH:
				rr.Status = StatusRejected
				rr.Errors = append(rr.Errors, "STALE_OVERRIDE canonical_sha256")
			case ov.textLink() != "" && ov.textLink() != e.Text:
				rr.Status = StatusRejected
				rr.Errors = append(rr.Errors, "MISMATCH canonical_en linkage")
			case ov.NoHon == e.Text:
				rr.Status = StatusRejected
				rr.Errors = append(rr.Errors, "redundant override identical to canonical")
			default:
				rr.NoHonorifics = ov.NoHon
				appliedOverrides[id] = true
			}
		}

		if rr.NoHonorifics == "" {
			rr.NoHonorifics = e.Text
		}
		if rr.Status == "" || rr.Status == StatusApplied {
			rr.Status = StatusApplied
		}
		if rr.Status == StatusApplied && rr.NoHonorifics != rr.Canonical {
			diffSet[id] = true
		}
		res.Records = append(res.Records, rr)
	}

	// Overrides that reference an ID with no canonical row.
	overrideIDs := make([]string, 0, len(c.Overrides))
	for id := range c.Overrides {
		overrideIDs = append(overrideIDs, id)
	}
	sort.Strings(overrideIDs)
	for _, id := range overrideIDs {
		if _, ok := c.Canonical[id]; ok {
			continue
		}
		res.Records = append(res.Records, RecordResult{
			RecordID: id,
			Status:   StatusRejected,
			Errors:   []string{"override has no canonical row"},
		})
	}

	sort.Slice(res.Records, func(i, j int) bool { return res.Records[i].RecordID < res.Records[j].RecordID })

	// Unresolved: corpus IDs with no canonical row.
	for id := range corpus {
		if _, ok := c.Canonical[id]; ok {
			continue
		}
		res.Unresolved = append(res.Unresolved, id)
	}
	sort.Strings(res.Unresolved)

	// Exact-diff verification: the records whose variants differ must be exactly
	// the authorised override set.
	authorised := map[string]bool{}
	for id := range c.Overrides {
		authorised[id] = true
	}
	for id := range diffSet {
		if !authorised[id] {
			res.DiffDrift = append(res.DiffDrift, id)
		}
	}
	for id := range authorised {
		if !diffSet[id] {
			res.DiffDrift = append(res.DiffDrift, id)
		}
	}
	sort.Strings(res.DiffDrift)
	res.DiffPass = len(res.DiffDrift) == 0

	// Counts.
	for _, rr := range res.Records {
		switch rr.Status {
		case StatusApplied:
			res.Counts.Applied++
		case StatusRejected:
			res.Counts.Rejected++
		case StatusUnresolved:
		}
		switch rr.TokenFlag {
		case TokenAllowed:
			res.Counts.TokenAllowed++
		case TokenMismatchF:
			res.Counts.TokenMismatch++
		}
	}
	res.Counts.Canonical = len(c.Canonical)
	res.Counts.Overrides = len(c.Overrides)
	res.Counts.OverridesApplied = len(appliedOverrides)
	res.Counts.Unresolved = len(res.Unresolved)

	return res
}

// linkage classifies how the override is tied to the canonical text.
func (o overrideEntry) linkage(canonicalText string) Linkage {
	switch {
	case o.CanonicalSH != "":
		return LinkSHA256
	case o.textLink() != "":
		return LinkText
	default:
		return LinkUnverifd
	}
}

// textLink returns the override's textual canonical linkage, if any.
func (o overrideEntry) textLink() string {
	if o.CanonicalEN != "" {
		return o.CanonicalEN
	}
	return o.ProposedEN
}
