package qa

import (
	"path/filepath"
	"strings"
	"testing"
)

// signals returns the per-record signals for id.
func signals(t *testing.T, res *Result, id string) RecordSignals {
	t.Helper()
	for _, rs := range res.Records {
		if rs.RecordID == id {
			return rs
		}
	}
	t.Fatalf("record %s not found in %d signals", id, len(res.Records))
	return RecordSignals{}
}

func runQA(t *testing.T, dir, corpus string) *Result {
	t.Helper()
	res, err := Run(Options{
		Scope:           ScopeBoth,
		CorpusPath:      corpus,
		TranslationsDir: filepath.Join(dir, "translations"),
	})
	if err != nil {
		t.Fatalf("qa.Run: %v", err)
	}
	return res
}

// TestQAControlCodesAndPlaceholdersPreserved: a canonical line that keeps every
// control code, wait and placeholder of the official English raises no token or
// break flag.
func TestQAControlCodesAndPlaceholdersPreserved(t *testing.T) {
	en := "Wait#w(15) then #iItalic#/i and #FN(Alain)#/FN ##(BUTTON)"
	jp := "待って"
	dir, corpus := fixture(t,
		[]string{row("UcTest.fms:1", jp, en, en)},
		nil,
		[]string{corpusLine("UcTest.fms:1", jp, en)})

	res := runQA(t, dir, corpus)
	rs := signals(t, res, "UcTest.fms:1")
	if rs.CanonTokenFlag != "" || rs.NoHonTokenFlag != "" {
		t.Fatalf("preserved control codes flagged: canon=%q nohon=%q", rs.CanonTokenFlag, rs.NoHonTokenFlag)
	}
	if rs.CanonBreakLoss != 0 || rs.NoHonBreakLoss != 0 {
		t.Fatalf("unexpected break loss: canon=%d nohon=%d", rs.CanonBreakLoss, rs.NoHonBreakLoss)
	}
	if res.Counts.TokenMismatch != 0 || res.Counts.TokenAllowed != 0 {
		t.Fatalf("counts = %+v, want no token flags", res.Counts)
	}
	if res.Counts.BreakLoss != 0 {
		t.Fatalf("break-loss count = %d, want 0", res.Counts.BreakLoss)
	}
}

// TestQAPlaceholderLossIsFlagged: dropping a #FN placeholder pair is a token
// mismatch even though the visible text grows.
func TestQAPlaceholderLossIsFlagged(t *testing.T) {
	jp := "こんにちは"
	en := "Hello #FN(Alain)#/FN"
	dir, corpus := fixture(t,
		[]string{row("UcTest.fms:2", jp, en, "Hello Alain")},
		nil,
		[]string{corpusLine("UcTest.fms:2", jp, en)})

	res := runQA(t, dir, corpus)
	rs := signals(t, res, "UcTest.fms:2")
	if rs.CanonTokenFlag != "TOKEN_MISMATCH" {
		t.Fatalf("canonical token flag = %q, want TOKEN_MISMATCH", rs.CanonTokenFlag)
	}
	if !strings.Contains(rs.CanonTokenDiff, "#FN") {
		t.Fatalf("token diff %q should name the dropped placeholder", rs.CanonTokenDiff)
	}
	if res.Counts.TokenMismatch != 1 {
		t.Fatalf("token mismatch count = %d, want 1", res.Counts.TokenMismatch)
	}
}

// TestQALongAndMultilineGrowthFlags: a much longer multi-line canonical line
// raises the length and max-line-growth signals, and an over-long single line is
// reported as such.
func TestQALongAndMultilineGrowthFlags(t *testing.T) {
	jp := "短い"
	en := "Short."
	longLine := strings.Repeat("verylongword ", 20) // > 100 visible runes
	canon := longLine + "\nsecond line\nthird line\nfourth line"
	// A multi-line field must be RFC 4180 quoted inside the TSV.
	quoted := "\"" + strings.ReplaceAll(canon, "\"", "\"\"") + "\""
	canonRow := "UcTest.fms:5\t" + sha1x12(jp) + "\t" + sha1x12(en) + "\t" + quoted
	dir, corpus := fixture(t,
		[]string{canonRow},
		nil,
		[]string{corpusLine("UcTest.fms:5", jp, en)})

	res := runQA(t, dir, corpus)
	rs := signals(t, res, "UcTest.fms:5")
	for _, want := range []string{"LENGTH_GE_150", "LENGTH_GE_200", "MAX_LINE_GROWTH_GE_30", "OVER_LONG_SINGLE_LINE_100"} {
		if !contains(rs.Flags, want) {
			t.Fatalf("flags %v missing %s", rs.Flags, want)
		}
	}
	if res.Counts.LengthGE200 != 1 || res.Counts.MaxLineGrowth != 1 || res.Counts.OverLongSingleLine != 1 {
		t.Fatalf("counts = %+v", res.Counts)
	}
	if res.Counts.BreakLoss != 0 {
		t.Fatalf("adding lines must not count as break loss, got %d", res.Counts.BreakLoss)
	}
}

// TestQABreakLossForAnAddedLine: a canonical that loses an authored official
// line is reported as break loss with the exact count.
func TestQABreakLossForAnAddedLine(t *testing.T) {
	jp := "二行"
	en := "Line one.\nLine two."
	dir, corpus := fixture(t,
		[]string{row("UcTest.fms:6", jp, en, "Line one. Line two.")},
		nil,
		[]string{corpusLine("UcTest.fms:6", jp, en)})

	res := runQA(t, dir, corpus)
	rs := signals(t, res, "UcTest.fms:6")
	if rs.CanonBreakLoss != 1 {
		t.Fatalf("canonical break loss = %d, want 1", rs.CanonBreakLoss)
	}
	if !contains(rs.Flags, "AUTHORED_BREAK_LOSS") {
		t.Fatalf("flags %v missing AUTHORED_BREAK_LOSS", rs.Flags)
	}
	if res.Counts.BreakLoss != 1 {
		t.Fatalf("break-loss count = %d, want 1", res.Counts.BreakLoss)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
