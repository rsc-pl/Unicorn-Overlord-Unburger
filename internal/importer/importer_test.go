package importer

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// --- helpers ---------------------------------------------------------------

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeCanonical writes a consolidated canonical table with the given rows.
func writeCanonical(t *testing.T, dir string, rows ...string) {
	t.Helper()
	writeFile(t, dir, HonorificsFile, neutralCanonicalHdr+"\n"+strings.Join(rows, ""))
}

// writeAlternate writes a consolidated sparse-override table.
func writeAlternate(t *testing.T, dir string, rows ...string) {
	t.Helper()
	writeFile(t, dir, NoHonorificsFile, neutralAlternateHdr+"\n"+strings.Join(rows, ""))
}

// alternateRow builds one override row linked by canonical SHA-256.
func alternateRow(id, canonicalSHA, text string) string {
	return id + "\t" + canonicalSHA + "\t" + text + "\n"
}

// --- loading ---------------------------------------------------------------

func TestLoadCheckpointRequiresHonorificsFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadCheckpoint(dir); err == nil || !strings.Contains(err.Error(), HonorificsFile) {
		t.Fatalf("a directory without %s must be refused, got %v", HonorificsFile, err)
	}
}

func TestLoadCheckpointRejectsEmptyDirectoryName(t *testing.T) {
	if _, err := LoadCheckpoint(""); err == nil {
		t.Fatal("an empty directory must be refused")
	}
}

// --- resolution ------------------------------------------------------------

func TestResolveStaleSourceHash(t *testing.T) {
	dir := t.TempDir()
	writeCanonical(t, dir, "UcScriptMsgConv.fms:7\t000000000000\t"+sha1Hex12("en")+"\tNew\n")
	cp, err := LoadCheckpoint(dir)
	if err != nil {
		t.Fatal(err)
	}
	res := cp.Resolve(map[string]CorpusRow{
		"UcScriptMsgConv.fms:7": {JP: "jp", OfficialEN: "en"},
	}, nil)
	if res.Counts.Rejected != 1 {
		t.Fatalf("want 1 rejected, got %d", res.Counts.Rejected)
	}
	if !strings.Contains(strings.Join(res.Records[0].Errors, ";"), "STALE_HASH") {
		t.Fatalf("want STALE_HASH, got %v", res.Records[0].Errors)
	}
}

func TestResolveMissingCorpusIsUnresolved(t *testing.T) {
	dir := t.TempDir()
	writeCanonical(t, dir, neutralCanonicalRow("UcScriptMsgConv.fms:8", "jp", "en", "New"))
	cp, err := LoadCheckpoint(dir)
	if err != nil {
		t.Fatal(err)
	}
	res := cp.Resolve(map[string]CorpusRow{}, nil)
	if res.Counts.Rejected != 0 || res.Counts.UnresolvedCanonical != 1 {
		t.Fatalf("want 0 rejected and 1 unresolved-canonical, got %d/%d", res.Counts.Rejected, res.Counts.UnresolvedCanonical)
	}
	if res.Records[0].Status != StatusUnresolved {
		t.Fatalf("want UNRESOLVED, got %s", res.Records[0].Status)
	}
}

func resolveWith(t *testing.T, dir string, corpus map[string]CorpusRow, exc map[string]bool) *Resolution {
	t.Helper()
	cp, err := LoadCheckpoint(dir)
	if err != nil {
		t.Fatal(err)
	}
	return cp.Resolve(corpus, exc)
}

func TestOverrideStaleHash(t *testing.T) {
	dir := t.TempDir()
	jp, en, canon := "jp", "en", "Canon"
	writeCanonical(t, dir, neutralCanonicalRow("UcScriptMsgConv.fms:200", jp, en, canon))
	writeAlternate(t, dir, alternateRow("UcScriptMsgConv.fms:200", strings.Repeat("0", 64), "No hon"))
	res := resolveWith(t, dir, map[string]CorpusRow{"UcScriptMsgConv.fms:200": {JP: jp, OfficialEN: en}}, nil)
	if res.Counts.Rejected != 1 || !strings.Contains(strings.Join(res.Records[0].Errors, ";"), "STALE_OVERRIDE") {
		t.Fatalf("want STALE_OVERRIDE rejection, got %+v", res.Records[0])
	}
}

func TestOverrideEmpty(t *testing.T) {
	dir := t.TempDir()
	jp, en, canon := "jp", "en", "Canon"
	writeCanonical(t, dir, neutralCanonicalRow("UcScriptMsgConv.fms:202", jp, en, canon))
	writeAlternate(t, dir, alternateRow("UcScriptMsgConv.fms:202", sha256Hex(canon), ""))
	res := resolveWith(t, dir, map[string]CorpusRow{"UcScriptMsgConv.fms:202": {JP: jp, OfficialEN: en}}, nil)
	if res.Counts.Rejected != 1 || !strings.Contains(strings.Join(res.Records[0].Errors, ";"), "empty") {
		t.Fatalf("want empty override rejection, got %+v", res.Records[0])
	}
}

func TestOverrideRedundantFailsDiff(t *testing.T) {
	dir := t.TempDir()
	jp, en, canon := "jp", "en", "Canon"
	writeCanonical(t, dir, neutralCanonicalRow("UcScriptMsgConv.fms:203", jp, en, canon))
	writeAlternate(t, dir, alternateRow("UcScriptMsgConv.fms:203", sha256Hex(canon), canon))
	res := resolveWith(t, dir, map[string]CorpusRow{"UcScriptMsgConv.fms:203": {JP: jp, OfficialEN: en}}, nil)
	if res.Counts.Rejected != 1 {
		t.Fatalf("redundant override must be rejected, got %+v", res.Counts)
	}
	if res.DiffPass {
		t.Fatal("exact-diff verification must FAIL when a redundant override is rejected")
	}
	if len(res.DiffDrift) != 1 || res.DiffDrift[0] != "UcScriptMsgConv.fms:203" {
		t.Fatalf("want drift on the override ID, got %v", res.DiffDrift)
	}
}

// --- tokens ----------------------------------------------------------------

func TestTokenParityAndException(t *testing.T) {
	dir := t.TempDir()
	jp := "jp"
	official := "He said #ihello#/i to me."
	canon := "He greeted me."
	writeCanonical(t, dir, neutralCanonicalRow("UcScriptMsgConv.fms:300", jp, official, canon))
	corpus := map[string]CorpusRow{"UcScriptMsgConv.fms:300": {JP: jp, OfficialEN: official}}

	// Without exception -> TOKEN_MISMATCH.
	res := resolveWith(t, dir, corpus, nil)
	if res.Records[0].TokenFlag != TokenMismatchF {
		t.Fatalf("want TOKEN_MISMATCH, got %q", res.Records[0].TokenFlag)
	}
	// With exception and italic-only diff -> ALLOWED_WITH_EXCEPTION.
	res = resolveWith(t, dir, corpus, map[string]bool{"UcScriptMsgConv.fms:300": true})
	if res.Records[0].TokenFlag != TokenAllowed {
		t.Fatalf("want ALLOWED_WITH_EXCEPTION, got %q", res.Records[0].TokenFlag)
	}
}

func TestTokenExceptionNotForOtherTokens(t *testing.T) {
	dir := t.TempDir()
	jp, official := "jp", "Wait #w(20)here."
	canon := "Wait here."
	writeCanonical(t, dir, neutralCanonicalRow("UcScriptMsgConv.fms:301", jp, official, canon))
	res := resolveWith(t, dir,
		map[string]CorpusRow{"UcScriptMsgConv.fms:301": {JP: jp, OfficialEN: official}},
		map[string]bool{"UcScriptMsgConv.fms:301": true})
	if res.Records[0].TokenFlag != TokenMismatchF {
		t.Fatalf("non-italic diff must stay TOKEN_MISMATCH even when listed, got %q", res.Records[0].TokenFlag)
	}
}

func TestTokenize(t *testing.T) {
	got := Tokenize("A#iword#/i B #w(20) C #c(12)name#/c D #FN E @w F ##(x)")
	want := []string{"#i", "#/i", "#w(20)", "#c(12)", "#/c", "#FN", "@w", "##(x)"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Tokenize = %v, want %v", got, want)
	}
}

// --- determinism -----------------------------------------------------------

func TestDeterminism(t *testing.T) {
	dir := t.TempDir()
	writeCanonical(t, dir,
		neutralCanonicalRow("UcScriptMsgConv.fms:3", "jp", "en", "New"),
		neutralCanonicalRow("UcScriptMsgConv.fms:1", "jp", "en", "New"))
	corpus := map[string]CorpusRow{
		"UcScriptMsgConv.fms:3": {JP: "jp", OfficialEN: "en"},
		"UcScriptMsgConv.fms:1": {JP: "jp", OfficialEN: "en"},
	}
	a := resolveWith(t, dir, corpus, nil)
	b := resolveWith(t, dir, corpus, nil)
	if !reflect.DeepEqual(a.Records, b.Records) || !reflect.DeepEqual(a.Unresolved, b.Unresolved) || a.DiffPass != b.DiffPass {
		t.Fatal("resolution must be deterministic")
	}
	if a.Records[0].RecordID != "UcScriptMsgConv.fms:1" {
		t.Fatalf("records must be sorted by record_id, got %s first", a.Records[0].RecordID)
	}
}

// --- corpus loader ---------------------------------------------------------

func TestLoadCorpusJSONL(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "corpus.jsonl",
		`{"record_id":"A.fms:0","jp":"あ","en":"a"}`+"\n"+
			"\n"+
			`{"record_id":"A.fms:1","jp":"い","en":"b"}`+"\n")
	corpus, err := LoadCorpusJSONL(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(corpus) != 2 || corpus["A.fms:0"].JP != "あ" || corpus["A.fms:1"].OfficialEN != "b" {
		t.Fatalf("unexpected corpus: %+v", corpus)
	}
}
