package importer

import (
	"strings"
	"testing"
)

// The consolidated files are the public contract of this package; these tests
// pin the header, the per-row validation and the single-layout rule.

const (
	neutralCanonicalHdr  = "record_id\tsource_jp_sha\toriginal_en_sha\tenglish_text"
	neutralAlternateHdr  = "record_id\tcanonical_sha256\tenglish_text"
	neutralCanonicalFile = "honorifics.tsv"
	neutralAlternateFile = "no-honorifics.tsv"
)

func neutralCanonicalRow(id, jp, en, text string) string {
	return id + "\t" + sha1Hex12(jp) + "\t" + sha1Hex12(en) + "\t" + text + "\n"
}

func TestNeutralFilesLoadCanonicalAndOverrides(t *testing.T) {
	dir := t.TempDir()
	jp, en := "jp", "en"
	canonical, alternate := "Canonical text", "Alternate text"
	writeFile(t, dir, neutralCanonicalFile, neutralCanonicalHdr+"\n"+
		neutralCanonicalRow("UcScriptMsgConv.fms:1", jp, en, canonical)+
		neutralCanonicalRow("UcItemList.fms:1115", jp, en, "Divine Tree Talisman"))
	writeFile(t, dir, neutralAlternateFile, neutralAlternateHdr+"\n"+
		"UcScriptMsgConv.fms:1\t"+sha256Hex(canonical)+"\t"+alternate+"\n")

	cp, err := LoadCheckpoint(dir)
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	if len(cp.Canonical) != 2 || len(cp.Overrides) != 1 {
		t.Fatalf("canonical=%d overrides=%d, want 2/1", len(cp.Canonical), len(cp.Overrides))
	}
	entry := cp.Canonical["UcScriptMsgConv.fms:1"]
	if entry.Text != canonical || entry.SourceJPSha != sha1Hex12(jp) || entry.OriginalSh != sha1Hex12(en) {
		t.Fatalf("canonical entry = %+v", entry)
	}
	if entry.File != neutralCanonicalFile {
		t.Fatalf("canonical row should name its file, got %q", entry.File)
	}
	ov := cp.Overrides["UcScriptMsgConv.fms:1"]
	if ov.NoHon != alternate || ov.CanonicalSH != sha256Hex(canonical) {
		t.Fatalf("override entry = %+v", ov)
	}

	res := cp.Resolve(map[string]CorpusRow{
		"UcScriptMsgConv.fms:1": {JP: jp, OfficialEN: en},
		"UcItemList.fms:1115":   {JP: jp, OfficialEN: en},
	}, nil)
	if res.Counts.Rejected != 0 || res.Counts.Canonical != 2 || res.Counts.Overrides != 1 {
		t.Fatalf("counts = %+v", res.Counts)
	}
	if !res.DiffPass {
		t.Fatalf("diff drift = %v", res.DiffDrift)
	}
	if res.Counts.OverridesApplied != 1 {
		t.Fatalf("the SHA-256-linked override must apply, counts = %+v", res.Counts)
	}
}

func TestNeutralFormatWithoutAlternateFileResolvesToCanonical(t *testing.T) {
	dir := t.TempDir()
	jp, en := "jp", "en"
	writeFile(t, dir, neutralCanonicalFile, neutralCanonicalHdr+"\n"+
		neutralCanonicalRow("UcScriptMsgConv.fms:2", jp, en, "Only canonical"))
	cp, err := LoadCheckpoint(dir)
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	if len(cp.Overrides) != 0 {
		t.Fatalf("overrides = %d, want 0 when the alternate table is absent", len(cp.Overrides))
	}
	res := cp.Resolve(map[string]CorpusRow{"UcScriptMsgConv.fms:2": {JP: jp, OfficialEN: en}}, nil)
	if res.Records[0].NoHonorifics != "Only canonical" {
		t.Fatalf("no-honorifics text = %q", res.Records[0].NoHonorifics)
	}
}

func TestNeutralMultilineTextIsPreserved(t *testing.T) {
	dir := t.TempDir()
	jp, en := "jp", "en"
	canonical := "first line\nsecond line"
	writeFile(t, dir, neutralCanonicalFile, neutralCanonicalHdr+"\n"+
		"UcScriptMsgConv.fms:3\t"+sha1Hex12(jp)+"\t"+sha1Hex12(en)+"\t\""+canonical+"\"\n")
	cp, err := LoadCheckpoint(dir)
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	if got := cp.Canonical["UcScriptMsgConv.fms:3"].Text; got != canonical {
		t.Fatalf("quoted multi-line text not preserved: %q", got)
	}
}

func TestNeutralHeaderIsValidatedExactly(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"reordered", "english_text\tsource_jp_sha\toriginal_en_sha\trecord_id\n"},
		{"renamed", neutralCanonicalHdr + "_x\n"},
		{"extra column", neutralCanonicalHdr + "\treason\n"},
		{"missing column", "record_id\tsource_jp_sha\tenglish_text\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, neutralCanonicalFile, tc.body)
			_, err := LoadCheckpoint(dir)
			if err == nil {
				t.Fatal("a wrong header must be refused")
			}
			if !strings.Contains(err.Error(), "unexpected header") || !strings.Contains(err.Error(), neutralCanonicalFile) {
				t.Fatalf("error must name the file and the header: %v", err)
			}
		})
	}
}

func TestNeutralRowValidation(t *testing.T) {
	dir := t.TempDir()
	jp, en := "jp", "en"
	good := neutralCanonicalRow("UcScriptMsgConv.fms:4", jp, en, "text")

	cases := []struct {
		name string
		body string
		want string
	}{
		{"bad record id", neutralCanonicalHdr + "\nnot-an-id\t" + sha1Hex12(jp) + "\t" + sha1Hex12(en) + "\ttext\n", "invalid record_id"},
		{"short source hash", neutralCanonicalHdr + "\nUcScriptMsgConv.fms:4\t0000\t" + sha1Hex12(en) + "\ttext\n", "malformed source_jp_sha"},
		{"uppercase source hash", neutralCanonicalHdr + "\nUcScriptMsgConv.fms:4\t" + strings.ToUpper(sha1Hex12(jp)) + "\t" + sha1Hex12(en) + "\ttext\n", "malformed source_jp_sha"},
		{"bad original hash", neutralCanonicalHdr + "\nUcScriptMsgConv.fms:4\t" + sha1Hex12(jp) + "\tzz\ttext\n", "malformed original_en_sha"},
		{"too few columns", neutralCanonicalHdr + "\nUcScriptMsgConv.fms:4\t" + sha1Hex12(jp) + "\n", "expected 4 columns"},
		{"conflicting duplicate", neutralCanonicalHdr + "\n" + good + neutralCanonicalRow("UcScriptMsgConv.fms:4", jp, en, "other"), "conflicting canonical rows"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeFile(t, dir, neutralCanonicalFile, tc.body)
			_, err := LoadCheckpoint(dir)
			if err == nil {
				t.Fatal("malformed row must be refused")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %v missing %q", err, tc.want)
			}
		})
	}
}

func TestNeutralIdenticalDuplicateIsDeduplicated(t *testing.T) {
	dir := t.TempDir()
	jp, en := "jp", "en"
	row := neutralCanonicalRow("UcScriptMsgConv.fms:5", jp, en, "same")
	writeFile(t, dir, neutralCanonicalFile, neutralCanonicalHdr+"\n"+row+row)
	cp, err := LoadCheckpoint(dir)
	if err != nil {
		t.Fatalf("identical duplicate must dedup: %v", err)
	}
	if len(cp.Canonical) != 1 {
		t.Fatalf("canonical rows = %d, want 1", len(cp.Canonical))
	}
}

func TestNeutralOverrideValidation(t *testing.T) {
	dir := t.TempDir()
	jp, en := "jp", "en"
	canonical := "Canon"
	base := neutralCanonicalHdr + "\n" + neutralCanonicalRow("UcScriptMsgConv.fms:6", jp, en, canonical)

	cases := []struct {
		name string
		body string
		want string
	}{
		{"bad sha length", neutralAlternateHdr + "\nUcScriptMsgConv.fms:6\t" + sha256Hex(canonical)[:63] + "\ttext\n", "malformed canonical_sha256"},
		{"not hex", neutralAlternateHdr + "\nUcScriptMsgConv.fms:6\t" + strings.Repeat("g", 64) + "\ttext\n", "malformed canonical_sha256"},
		{"bad record id", neutralAlternateHdr + "\nnope\t" + sha256Hex(canonical) + "\ttext\n", "invalid record_id"},
		{"duplicate row", neutralAlternateHdr + "\nUcScriptMsgConv.fms:6\t" + sha256Hex(canonical) + "\ttext\n" +
			"UcScriptMsgConv.fms:6\t" + sha256Hex(canonical) + "\ttext\n", "duplicate override row"},
		{"wrong header", "record_id\tcanonical_sha\tenglish_text\n", "unexpected header"},
		{"too few columns", neutralAlternateHdr + "\nUcScriptMsgConv.fms:6\t" + sha256Hex(canonical) + "\n", "expected 3 columns"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeFile(t, dir, neutralCanonicalFile, base)
			writeFile(t, dir, neutralAlternateFile, tc.body)
			_, err := LoadCheckpoint(dir)
			if err == nil {
				t.Fatal("malformed override table must be refused")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %v missing %q", err, tc.want)
			}
		})
	}
}

func TestNeutralEmptyFilesAreRefused(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, neutralCanonicalFile, "")
	if _, err := LoadCheckpoint(dir); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("an empty canonical table must be refused, got %v", err)
	}
	writeFile(t, dir, neutralCanonicalFile, neutralCanonicalHdr+"\n")
	writeFile(t, dir, neutralAlternateFile, "")
	if _, err := LoadCheckpoint(dir); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("an empty override table must be refused, got %v", err)
	}
}

func TestNeutralOverrideValidationAgainstCanonical(t *testing.T) {
	dir := t.TempDir()
	jp, en := "jp", "en"
	canonical := "Canon"
	writeFile(t, dir, neutralCanonicalFile, neutralCanonicalHdr+"\n"+
		neutralCanonicalRow("UcScriptMsgConv.fms:7", jp, en, canonical))
	corpus := map[string]CorpusRow{"UcScriptMsgConv.fms:7": {JP: jp, OfficialEN: en}}

	// Stale canonical_sha256 -> rejected, and the diff verification fails because
	// the override is authorised but never applied.
	writeFile(t, dir, neutralAlternateFile, neutralAlternateHdr+"\n"+
		"UcScriptMsgConv.fms:7\t"+strings.Repeat("0", 64)+"\tAlternate\n")
	cp, err := LoadCheckpoint(dir)
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	res := cp.Resolve(corpus, nil)
	if res.Counts.Rejected != 1 || !strings.Contains(strings.Join(res.Records[0].Errors, ";"), "STALE_OVERRIDE") {
		t.Fatalf("want STALE_OVERRIDE, got %+v", res.Records[0])
	}
	if res.DiffPass {
		t.Fatal("a rejected override must fail the exact-diff verification")
	}

	// Redundant override (identical to the canonical text) -> rejected.
	writeFile(t, dir, neutralAlternateFile, neutralAlternateHdr+"\n"+
		"UcScriptMsgConv.fms:7\t"+sha256Hex(canonical)+"\t"+canonical+"\n")
	cp, err = LoadCheckpoint(dir)
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	res = cp.Resolve(corpus, nil)
	if res.Counts.Rejected != 1 || !strings.Contains(strings.Join(res.Records[0].Errors, ";"), "redundant") {
		t.Fatalf("want a redundant override rejection, got %+v", res.Records[0])
	}

	// Orphan override (no canonical row) -> rejected.
	writeFile(t, dir, neutralAlternateFile, neutralAlternateHdr+"\n"+
		"UcScriptMsgConv.fms:999\t"+sha256Hex(canonical)+"\tAlternate\n")
	cp, err = LoadCheckpoint(dir)
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	res = cp.Resolve(corpus, nil)
	var orphan *RecordResult
	for i := range res.Records {
		if res.Records[i].RecordID == "UcScriptMsgConv.fms:999" {
			orphan = &res.Records[i]
		}
	}
	if orphan == nil || orphan.Status != StatusRejected || !strings.Contains(strings.Join(orphan.Errors, ";"), "no canonical row") {
		t.Fatalf("want an orphan override rejection, got %+v", orphan)
	}
	if res.DiffPass {
		t.Fatal("an orphan override must fail the exact-diff verification")
	}
}

func TestUsesNeutralFormat(t *testing.T) {
	dir := t.TempDir()
	if UsesNeutralFormat(dir) {
		t.Fatal("an empty directory does not hold the consolidated files")
	}
	writeFile(t, dir, neutralCanonicalFile, neutralCanonicalHdr+"\n")
	if !UsesNeutralFormat(dir) {
		t.Fatal("the consolidated canonical file must select the neutral format")
	}
}
