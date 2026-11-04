package qa

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture writes minimal translation tables and a corpus and returns their paths.
func fixture(t *testing.T, canonRows, overrideRows, corpusRows []string) (dir, corpus string) {
	t.Helper()
	dir = t.TempDir()
	translations := filepath.Join(dir, "translations")
	if err := os.MkdirAll(translations, 0o755); err != nil {
		t.Fatal(err)
	}
	canon := "record_id\tsource_jp_sha\toriginal_en_sha\tenglish_text\n" + strings.Join(canonRows, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(translations, "honorifics.tsv"), []byte(canon), 0o644); err != nil {
		t.Fatal(err)
	}
	if len(overrideRows) > 0 {
		ov := "record_id\tcanonical_sha256\tenglish_text\n" + strings.Join(overrideRows, "\n") + "\n"
		if err := os.WriteFile(filepath.Join(translations, "no-honorifics.tsv"), []byte(ov), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	corpus = filepath.Join(dir, "corpus.jsonl")
	if err := os.WriteFile(corpus, []byte(strings.Join(corpusRows, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, corpus
}

func sha1x12(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}

func row(id, jp, en, canon string) string {
	return strings.Join([]string{id, sha1x12(jp), sha1x12(en), canon}, "\t")
}

func altRow(id, canonical, text string) string {
	sum := sha256.Sum256([]byte(canonical))
	return strings.Join([]string{id, hex.EncodeToString(sum[:]), text}, "\t")
}

func corpusLine(id, jp, en string) string {
	// minimal corpus object; only record_id, jp, en are consumed
	b, err := json.Marshal(map[string]string{"record_id": id, "jp": jp, "en": en})
	if err != nil {
		panic(err)
	}
	return string(b)
}

func TestQATokenMismatchAndBreakLoss(t *testing.T) {
	dir, corpus := fixture(t,
		[]string{
			row("UcTest.fms:1", "clean", "Short.", "Short."),
			row("UcTest.fms:2", "break", "Line one.\nLine two.", "Line one. Line two."),
			row("UcTest.fms:4", "italic", "Hello #iworld#/i.", "Hello world."),
		}, nil,
		[]string{
			corpusLine("UcTest.fms:1", "clean", "Short."),
			corpusLine("UcTest.fms:2", "break", "Line one.\nLine two."),
			corpusLine("UcTest.fms:4", "italic", "Hello #iworld#/i."),
		})

	res, err := Run(Options{Scope: ScopeBoth, TranslationsDir: filepath.Join(dir, "translations"), CorpusPath: corpus})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]RecordSignals{}
	for _, r := range res.Records {
		byID[r.RecordID] = r
	}

	if got := byID["UcTest.fms:1"].Flags; len(got) != 0 {
		t.Fatalf("record :1 should have no flags, got %v", got)
	}
	if !hasFlag(byID["UcTest.fms:2"], "AUTHORED_BREAK_LOSS") {
		t.Fatalf("record :2 must flag AUTHORED_BREAK_LOSS: %v", byID["UcTest.fms:2"].Flags)
	}
	if byID["UcTest.fms:2"].CanonBreakLoss != 1 {
		t.Fatalf("record :2 break loss = %d, want 1", byID["UcTest.fms:2"].CanonBreakLoss)
	}
	if !hasFlag(byID["UcTest.fms:4"], "TOKEN_MISMATCH") {
		t.Fatalf("record :4 must flag TOKEN_MISMATCH: %v", byID["UcTest.fms:4"].Flags)
	}
	if res.Counts.TokenMismatch != 1 {
		t.Fatalf("token mismatch count = %d, want 1", res.Counts.TokenMismatch)
	}
}

func TestQALengthRatio(t *testing.T) {
	long := "Go now, hero, to the faraway land and bring back the light!"
	dir, corpus := fixture(t,
		[]string{row("UcTest.fms:3", "go", "Go.", long)}, nil,
		[]string{corpusLine("UcTest.fms:3", "go", "Go.")})

	res, err := Run(Options{Scope: ScopeBoth, TranslationsDir: filepath.Join(dir, "translations"), CorpusPath: corpus})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Records) != 1 {
		t.Fatalf("want 1 record, got %d", len(res.Records))
	}
	r := res.Records[0]
	if !hasFlag(r, "LENGTH_GE_150") || !hasFlag(r, "LENGTH_GE_200") {
		t.Fatalf("long record must flag both ratios: %v (ratio=%v)", r.Flags, r.CanonRatio)
	}
	if res.Counts.LengthGE150 != 1 || res.Counts.LengthGE200 != 1 {
		t.Fatalf("ratio counts = %d/%d, want 1/1", res.Counts.LengthGE150, res.Counts.LengthGE200)
	}
}

func TestQAOverrideDivergence(t *testing.T) {
	dir, corpus := fixture(t,
		[]string{row("UcTest.fms:5", "hi", "Hi.", "Hi.")},
		[]string{altRow("UcTest.fms:5", "Hi.", "Hey.")},
		[]string{corpusLine("UcTest.fms:5", "hi", "Hi.")})

	res, err := Run(Options{Scope: ScopeBoth, TranslationsDir: filepath.Join(dir, "translations"), CorpusPath: corpus})
	if err != nil {
		t.Fatal(err)
	}
	r := res.Records[0]
	if !r.OverrideDivergence {
		t.Fatalf("record :5 must show override divergence")
	}
	if !hasFlag(r, "OVERRIDE_DIVERGENCE") {
		t.Fatalf("record :5 must carry the OVERRIDE_DIVERGENCE flag: %v", r.Flags)
	}
	if res.Counts.OverrideDivergence != 1 {
		t.Fatalf("override divergence count = %d, want 1", res.Counts.OverrideDivergence)
	}
	// The no-honorifics-only scope must not report divergence.
	res2, err := Run(Options{Scope: ScopeHonorifics, TranslationsDir: filepath.Join(dir, "translations"), CorpusPath: corpus})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Counts.OverrideDivergence != 0 {
		t.Fatalf("honorifics-only scope must not report override divergence")
	}
}

func TestQADeterminism(t *testing.T) {
	dir, corpus := fixture(t,
		[]string{
			row("UcTest.fms:1", "clean", "Short.", "Short."),
			row("UcTest.fms:2", "break", "Line one.\nLine two.", "Line one. Line two."),
			row("UcTest.fms:3", "go", "Go.", "Go now, hero, to the faraway land!"),
		}, nil,
		[]string{
			corpusLine("UcTest.fms:1", "clean", "Short."),
			corpusLine("UcTest.fms:2", "break", "Line one.\nLine two."),
			corpusLine("UcTest.fms:3", "go", "Go."),
		})

	a, err := Run(Options{Scope: ScopeBoth, TranslationsDir: filepath.Join(dir, "translations"), CorpusPath: corpus})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Run(Options{Scope: ScopeBoth, TranslationsDir: filepath.Join(dir, "translations"), CorpusPath: corpus})
	if err != nil {
		t.Fatal(err)
	}
	if a.TSV != b.TSV || a.SummaryTxt != b.SummaryTxt {
		t.Fatalf("QA output is not deterministic")
	}
	if !strings.HasPrefix(a.TSV, "record_id\tstatus\tvariant_scope\t") {
		t.Fatalf("unexpected TSV header: %q", strings.SplitN(a.TSV, "\n", 2)[0])
	}
}

func TestQARejectsBadVariantAndMissingCorpus(t *testing.T) {
	if _, err := Run(Options{Scope: "bogus", CorpusPath: "x", TranslationsDir: t.TempDir()}); err == nil {
		t.Fatal("expected error for bad variant")
	}
	if _, err := Run(Options{Scope: ScopeBoth, TranslationsDir: t.TempDir()}); err == nil {
		t.Fatal("expected error for missing corpus")
	}
}

func hasFlag(r RecordSignals, f string) bool {
	for _, x := range r.Flags {
		if x == f {
			return true
		}
	}
	return false
}
