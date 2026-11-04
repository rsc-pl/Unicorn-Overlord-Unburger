package build

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadVersionTrimsAndRequiresValue(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadVersion(dir); !errors.Is(err, ErrVersionMissing) {
		t.Fatalf("missing version.txt: err = %v, want ErrVersionMissing", err)
	}
	if err := os.WriteFile(filepath.Join(dir, VersionFileName), []byte("  \n\t"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadVersion(dir); !errors.Is(err, ErrVersionMissing) {
		t.Fatalf("whitespace-only version.txt: err = %v, want ErrVersionMissing", err)
	}
	if err := os.WriteFile(filepath.Join(dir, VersionFileName), []byte(" 1.2.3 \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	v, err := ReadVersion(dir)
	if err != nil || v != "1.2.3" {
		t.Fatalf("ReadVersion = (%q, %v), want (1.2.3, nil)", v, err)
	}
	if got := FormatTag(v); got != "Unburger Mod V1.2.3" {
		t.Fatalf("FormatTag(%q) = %q", v, got)
	}
	if got := FormatTag("  "); got != "" {
		t.Fatalf("FormatTag(empty) = %q, want empty", got)
	}
}

// TestRunRequiresVersionBeforeReading proves the version gate runs before any
// source validation or output staging: a build flagged RequireVersion with no
// version fails with ErrVersionMissing and leaves nothing behind.
func TestRunRequiresVersionBeforeReading(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	_, err := Run(Options{
		Variant:        VariantBoth,
		SourcePath:     filepath.Join(dir, "does-not-exist.CPK"),
		OutDir:         out,
		RunName:        "test-run",
		RequireVersion: true,
	})
	if !errors.Is(err, ErrVersionMissing) {
		t.Fatalf("err = %v, want ErrVersionMissing", err)
	}
	if _, statErr := os.Stat(filepath.Join(out, "test-run")); !os.IsNotExist(statErr) {
		t.Fatalf("a missing version left output behind: %v", statErr)
	}
}

// TestRunRecordsVersionInReports: a supplied version is stamped into every
// edition report (machine and text) and written into the startup-label record.
func TestRunRecordsVersionInReports(t *testing.T) {
	dir := t.TempDir()
	src, _, _ := writeCPK(t, dir, fixExtract(startupLabelMembers()))
	corpus := writeCorpus(t, dir, [][3]string{
		{"UcSysMsg.fms:0", "ニューゲーム", "New Game"},
	})
	translations := filepath.Join(dir, "translations")
	writeTranslations(t, translations, "honorifics.tsv",
		"record_id\tsource_jp_sha\toriginal_en_sha\tenglish_text\n"+
			"UcSysMsg.fms:0\t"+sha1Short("ニューゲーム")+"\t"+sha1Short("New Game")+"\tNew Journey\n")
	opts := baseOptions(src, corpus, translations, filepath.Join(dir, "out"), VariantBoth)
	opts.DryRun = true
	opts.Version = "9.9.9"
	res, err := Run(opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Version != "9.9.9" || res.StartupLabel != "Unburger Mod V9.9.9" {
		t.Fatalf("result version = %q / label %q", res.Version, res.StartupLabel)
	}
	for _, e := range res.Editions {
		if !strings.Contains(e.ReportJSON, `"version": "9.9.9"`) ||
			!strings.Contains(e.ReportJSON, `"startup_label": "Unburger Mod V9.9.9"`) {
			t.Fatalf("edition %s JSON lacks the version fields:\n%s", e.Variant, e.ReportJSON)
		}
		if !strings.Contains(e.ReportTxt, "startup label:      Unburger Mod V9.9.9") {
			t.Fatalf("edition %s text lacks the startup label:\n%s", e.Variant, e.ReportTxt)
		}
	}
}
