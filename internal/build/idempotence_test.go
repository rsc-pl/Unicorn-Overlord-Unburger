package build

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// idempotentFixture prepares a source CPK, corpus and one-batch checkpoint with
// one real edit so a rebuild differs from the source.
func idempotentFixture(t *testing.T) (dir, src, srcSHA, corpus, translations string) {
	t.Helper()
	dir = t.TempDir()
	src, srcSHA, _ = writeCPK(t, dir, fixExtract(startupLabelMembers()))
	corpus = writeCorpus(t, dir, [][3]string{{"UcSysMsg.fms:0", "ニューゲーム", "New Game"}})
	translations = filepath.Join(dir, "translations")
	writeTranslations(t, translations, "honorifics.tsv",
		"record_id\tsource_jp_sha\toriginal_en_sha\tenglish_text\n"+
			"UcSysMsg.fms:0\t"+sha1Short("ニューゲーム")+"\t"+sha1Short("New Game")+"\tNew Journey\n")
	return
}

// TestDryRunTwiceIsByteIdentical: repeating an import/resolve/build dry-run with
// the same checkpoint yields identical reports and identical edition digests.
func TestDryRunTwiceIsByteIdentical(t *testing.T) {
	dir, src, srcSHA, corpus, translations := idempotentFixture(t)
	run := func() *Result {
		o := baseOptions(src, corpus, translations, filepath.Join(dir, "out"), VariantBoth)
		o.Expect = SourceExpectation{SHA256: srcSHA}
		o.DryRun = true
		res, err := Run(o)
		if err != nil {
			t.Fatalf("dry run: %v", err)
		}
		return res
	}
	first, second := run(), run()
	if first.SummaryTxt != second.SummaryTxt {
		t.Fatalf("dry-run summary differs between runs:\n%s\n---\n%s", first.SummaryTxt, second.SummaryTxt)
	}
	if len(first.Editions) != len(second.Editions) {
		t.Fatalf("edition count differs: %d vs %d", len(first.Editions), len(second.Editions))
	}
	for i := range first.Editions {
		if first.Editions[i].SHA256 != second.Editions[i].SHA256 || first.Editions[i].Size != second.Editions[i].Size {
			t.Fatalf("edition %s digest differs between dry runs", first.Editions[i].Variant)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "out")); !os.IsNotExist(err) {
		t.Fatal("repeated dry runs wrote an output directory")
	}
}

// TestRealRebuildIsIdempotentAcrossOutputRoots: two real builds of the same
// checkpoint into different output roots produce byte-identical artifacts.
func TestRealRebuildIsIdempotentAcrossOutputRoots(t *testing.T) {
	dir, src, srcSHA, corpus, translations := idempotentFixture(t)
	var arts [][]byte
	for i := 0; i < 2; i++ {
		o := baseOptions(src, corpus, translations, filepath.Join(dir, "out", string(rune('a'+i))), VariantBoth)
		o.Expect = SourceExpectation{SHA256: srcSHA}
		res, err := Run(o)
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		for _, e := range res.Editions {
			data, err := os.ReadFile(e.ArtifactPath)
			if err != nil {
				t.Fatal(err)
			}
			arts = append(arts, data)
		}
	}
	if len(arts) != 4 {
		t.Fatalf("collected %d artifacts, want 4", len(arts))
	}
	if !bytes.Equal(arts[0], arts[2]) {
		t.Fatal("honorifics artifact is not reproducible across output roots")
	}
	if !bytes.Equal(arts[1], arts[3]) {
		t.Fatal("no-honorifics artifact is not reproducible across output roots")
	}
}

// TestRerunReusesVerifiedRunAndPreserves: a second identical build into the same
// run name reuses the verified run — no rebuild, no overwrite — and returns the
// same artifact digests while leaving the first build byte-identical and no
// staging directory behind. A rerun whose identity differs, by contrast, is
// refused and the first build is preserved untouched.
func TestRerunReusesVerifiedRunAndPreserves(t *testing.T) {
	dir, src, srcSHA, corpus, translations := idempotentFixture(t)
	out := filepath.Join(dir, "out")
	o := baseOptions(src, corpus, translations, out, VariantBoth)
	o.Expect = SourceExpectation{SHA256: srcSHA}
	first, err := Run(o)
	if err != nil {
		t.Fatalf("first build: %v", err)
	}
	var before []string
	for _, e := range first.Editions {
		before = append(before, sha256BytesOf(e.ArtifactPath))
	}

	second, err := Run(o)
	if err != nil {
		t.Fatalf("an identical rerun must reuse the verified run, not fail: %v", err)
	}
	if len(second.Editions) != len(first.Editions) {
		t.Fatalf("reused run has %d editions, want %d", len(second.Editions), len(first.Editions))
	}
	for i, e := range second.Editions {
		if e.SHA256 != first.Editions[i].SHA256 || e.Size != first.Editions[i].Size {
			t.Fatalf("reused edition %s digest differs from the first build", e.Variant)
		}
		if !e.Published {
			t.Fatalf("reused edition %s is not marked published", e.Variant)
		}
		if now := sha256BytesOf(e.ArtifactPath); now != before[i] {
			t.Fatalf("first build artifact %s was rewritten by the rerun", e.Variant)
		}
	}

	// A different identity (here a different version) is not reusable: the rerun
	// must be refused and must not disturb the run already on disk.
	changed := o
	changed.Version = "9.9.9"
	if _, err := Run(changed); err == nil {
		t.Fatal("a rerun with a different identity must be refused")
	} else if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error = %v, want an 'already exists' refusal", err)
	}
	for i, e := range first.Editions {
		if now := sha256BytesOf(e.ArtifactPath); now != before[i] {
			t.Fatalf("first build artifact %s was overwritten by the refused rerun", e.Variant)
		}
	}
	// No staging directory of any naming convention may be left behind.
	for _, pat := range []string{".staging-*", "*.staging"} {
		matches, _ := filepath.Glob(filepath.Join(out, "test-run", pat))
		if len(matches) != 0 {
			t.Fatalf("staging directories left behind: %v", matches)
		}
	}
}

// TestProgressAbortNeverLeavesPartialOutput aborts the build at every stage
// boundary in turn and asserts the invariant the TUI relies on: after a
// cancellation the run directory either does not exist or holds a complete
// published build — never a partial artifact.
func TestProgressAbortNeverLeavesPartialOutput(t *testing.T) {
	dir, src, srcSHA, corpus, translations := idempotentFixture(t)

	// Discover the stage names and count with a real probe run (a dry run omits
	// the final publish stage).
	var stages []string
	probe := baseOptions(src, corpus, translations, filepath.Join(dir, "probe"), VariantBoth)
	probe.Expect = SourceExpectation{SHA256: srcSHA}
	probe.Progress = func(stage string, done, total int) error {
		stages = append(stages, stage)
		return nil
	}
	if _, err := Run(probe); err != nil {
		t.Fatalf("probe run: %v", err)
	}
	if len(stages) == 0 {
		t.Fatal("no progress stages reported")
	}

	abortErr := errors.New("test abort")
	var publishedAt []string
	for i, target := range stages {
		out := filepath.Join(dir, "abort", string(rune('a'+i)))
		o := baseOptions(src, corpus, translations, out, VariantBoth)
		o.Expect = SourceExpectation{SHA256: srcSHA}
		o.Progress = func(stage string, done, total int) error {
			if stage == target {
				return abortErr
			}
			return nil
		}
		_, err := Run(o)
		if !errors.Is(err, abortErr) {
			t.Fatalf("abort at %q: err = %v, want the abort error", target, err)
		}
		runDir := filepath.Join(out, "test-run")
		if _, statErr := os.Stat(runDir); os.IsNotExist(statErr) {
			continue // nothing was published: the expected case before the final stage
		}
		publishedAt = append(publishedAt, target)
		// Published: every edition must be complete.
		for _, v := range []string{"honorifics", "no-honorifics"} {
			ed := filepath.Join(runDir, v)
			for _, rel := range []string{"romfs/Unicorn_US.CPK", "BUILD_REPORT.txt", "BUILD_REPORT.json"} {
				if _, statErr := os.Stat(filepath.Join(ed, filepath.FromSlash(rel))); statErr != nil {
					t.Fatalf("abort at %q left a partial %s (%s): %v", target, v, rel, statErr)
				}
			}
		}
		staging, _ := filepath.Glob(filepath.Join(out, "test-run", ".staging-*"))
		legacy, _ := filepath.Glob(filepath.Join(out, "test-run", "*.staging"))
		if len(staging) != 0 || len(legacy) != 0 {
			t.Fatalf("abort at %q left staging directories: %v %v", target, staging, legacy)
		}
	}
	// Only a cancellation that lands on the final publish-stage callback can
	// observe a published build: every earlier abort publishes nothing. This
	// also means a cancel arriving exactly there reports an error even though a
	// complete build was written (reported to the caller as an error, not a partial).
	if len(publishedAt) != 1 || publishedAt[0] != stages[len(stages)-1] {
		t.Fatalf("published-on-abort stages = %v, want exactly [%q]", publishedAt, stages[len(stages)-1])
	}
}

// TestReadOnlyOutputRootFailsCleanly: an unwritable output root makes the build
// fail with an error and leaves no partial or staging output.
func TestReadOnlyOutputRootFailsCleanly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permissions do not deny writes")
	}
	dir, src, srcSHA, corpus, translations := idempotentFixture(t)
	out := filepath.Join(dir, "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(out, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(out, 0o755)

	o := baseOptions(src, corpus, translations, out, VariantBoth)
	o.Expect = SourceExpectation{SHA256: srcSHA}
	if _, err := Run(o); err == nil {
		t.Fatal("a build into a read-only output root must fail")
	}
	if _, err := os.Stat(filepath.Join(out, "test-run")); !os.IsNotExist(err) {
		t.Fatal("the failed build created a run directory")
	}
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("the failed build left files in the output root: %v", entries)
	}
}
