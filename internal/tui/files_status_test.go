package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"uo-unburger/internal/appconfig"
)

// writeNeutralTranslations writes a minimal valid pair of public translation
// tables into dir and returns it.
func writeNeutralTranslations(t *testing.T, dir string, honorifics, overrides int) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var h strings.Builder
	h.WriteString("record_id\tsource_jp_sha\toriginal_en_sha\tenglish_text\n")
	for i := 0; i < honorifics; i++ {
		h.WriteString("a.fms:")
		h.WriteString(itoa(i + 1))
		h.WriteString("\t0123456789ab\tcdef01234567\tcanonical text\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "honorifics.tsv"), []byte(h.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if overrides > 0 {
		var o strings.Builder
		o.WriteString("record_id\tcanonical_sha256\tenglish_text\n")
		sha := strings.Repeat("a", 64)
		for i := 0; i < overrides; i++ {
			o.WriteString("a.fms:")
			o.WriteString(itoa(i + 1))
			o.WriteString("\t" + sha + "\toverride text\n")
		}
		if err := os.WriteFile(filepath.Join(dir, "no-honorifics.tsv"), []byte(o.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// TestTranslationFilesStatusReportsPublicTables: the normal screens report the
// public translation files (counts and words) with no reference corpus at all.
func TestTranslationFilesStatusReportsPublicTables(t *testing.T) {
	dir := writeNeutralTranslations(t, filepath.Join(t.TempDir(), "translations"), 3, 2)
	m := testModel(t)
	m.trans.form.SetValue("translations", dir)

	st := m.translationFiles()
	if !st.loaded || !st.honorificsPresent || !st.noPresent {
		t.Fatalf("tables not loaded: %+v", st)
	}
	if st.canonicalRows != 3 || st.overrideRows != 2 {
		t.Fatalf("counts = (%d,%d), want (3,2)", st.canonicalRows, st.overrideRows)
	}
	if got := st.honorificsWord(); got != "loaded 3" {
		t.Fatalf("honorificsWord = %q", got)
	}
	if got := st.noHonorificsWord(); got != "loaded 2" {
		t.Fatalf("noHonorificsWord = %q", got)
	}
}

// TestTranslationFilesStatusWithoutOverridesIsNotABlocker: the sparse override
// table is optional, so an absent file is "none", never an error.
func TestTranslationFilesStatusWithoutOverridesIsNotABlocker(t *testing.T) {
	dir := writeNeutralTranslations(t, filepath.Join(t.TempDir(), "translations"), 2, 0)
	m := testModel(t)
	m.trans.form.SetValue("translations", dir)

	st := m.translationFiles()
	if !st.loaded || st.noPresent {
		t.Fatalf("expected the canonical table only: %+v", st)
	}
	if got := st.noHonorificsWord(); got != "none" {
		t.Fatalf("noHonorificsWord = %q, want none", got)
	}
}

// TestNormalScreensNeverRequireReferenceData: the Overview, Game files,
// Translations and Build screens must not present reference data as a
// requirement or a blocker. Only the advanced Diagnostics view may mention it.
func TestNormalScreensNeverRequireReferenceData(t *testing.T) {
	dir := writeNeutralTranslations(t, filepath.Join(t.TempDir(), "translations"), 1, 1)
	m := testModel(t)
	m.trans.form.SetValue("translations", dir)

	for _, screen := range []Screen{ScreenDashboard, ScreenSources, ScreenTranslations, ScreenBuild} {
		m.setScreen(screen)
		got := strings.ToLower(view(m))
		if strings.Contains(got, "reference data") {
			t.Fatalf("screen %d must not mention reference data:\n%s", screen, got)
		}
		if strings.Contains(got, "corpus") {
			t.Fatalf("screen %d must not mention a corpus:\n%s", screen, got)
		}
	}

	// The normal Diagnostics view must not push reference data either; only the
	// advanced toggle reveals the optional corpus field and its status.
	m.setScreen(ScreenDiagnostics)
	if got := strings.ToLower(view(m)); strings.Contains(got, "reference data") {
		t.Fatalf("the normal Diagnostics view must not name reference data:\n%s", got)
	}
	m.diag.advanced = true
	advanced := strings.ToLower(view(m))
	if !strings.Contains(advanced, "reference data") || !strings.Contains(advanced, "not required") {
		t.Fatalf("the advanced Diagnostics view must name the optional reference data:\n%s", advanced)
	}
}

// TestAdvancedCorpusIsEditableButNotRequired: the corpus field round-trips through
// the advanced Diagnostics view and is never needed to run a normal path.
func TestAdvancedCorpusIsEditableButNotRequired(t *testing.T) {
	m := testModel(t)
	m.setScreen(ScreenDiagnostics)
	m.diag.advanced = true
	m.diag.form.SetValue("corpus", "/data/corpus.jsonl")
	if got := m.corpusPath(); got != "/data/corpus.jsonl" {
		t.Fatalf("corpusPath = %q", got)
	}
	m.loadCfgFromFields()
	if m.cfg.Corpus != "/data/corpus.jsonl" {
		t.Fatalf("the advanced corpus was not remembered: %q", m.cfg.Corpus)
	}

	// Without the advanced toggle the reference actions are not offered.
	off := testModel(t)
	off.setScreen(ScreenDiagnostics)
	for _, r := range off.actionRows() {
		if r.ID == "edit-corpus" {
			t.Fatalf("the reference-data action must be advanced-only")
		}
	}
	off.diag.advanced = true
	found := false
	for _, r := range off.actionRows() {
		if r.ID == "edit-corpus" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the advanced toggle must reveal the reference-data action")
	}
}

// TestOverviewReadinessShowsPublicTables: the readiness checklist reports the
// public translation tables and never a reference-data row.
func TestOverviewReadinessShowsPublicTables(t *testing.T) {
	dir := writeNeutralTranslations(t, filepath.Join(t.TempDir(), "translations"), 4, 1)
	m := testModel(t)
	m.trans.form.SetValue("translations", dir)

	var joined strings.Builder
	for _, r := range m.overviewReadiness() {
		joined.WriteString(r.ID + " " + strings.Join(r.Cells, " ") + "\n")
	}
	got := joined.String()
	if strings.Contains(got, "corpus") || strings.Contains(got, "Reference") {
		t.Fatalf("the readiness list must not mention reference data:\n%s", got)
	}
	if !strings.Contains(got, "honorifics") || !strings.Contains(got, "no-honorifics") {
		t.Fatalf("the readiness list must report both public tables:\n%s", got)
	}

	warnings := strings.ToLower(strings.Join(m.overviewWarnings(), "\n"))
	if strings.Contains(warnings, "reference data") || strings.Contains(warnings, "corpus") {
		t.Fatalf("the warnings must not blame missing reference data:\n%s", warnings)
	}
}

// TestConfiguredCorpusStaysOutOfNormalScreens: a configured corpus never leaks
// onto a normal screen, so it can never read as a blocker.
func TestConfiguredCorpusStaysOutOfNormalScreens(t *testing.T) {
	m := testModel(t)
	m.cfg.Corpus = "/data/private/reference.jsonl"
	m.diag.form.SetValue("corpus", m.cfg.Corpus)
	for _, screen := range []Screen{ScreenDashboard, ScreenSources, ScreenTranslations, ScreenBuild} {
		m.setScreen(screen)
		if got := view(m); strings.Contains(got, "reference.jsonl") {
			t.Fatalf("screen %d leaked the corpus path:\n%s", screen, got)
		}
	}
}

// TestTranslationFilesStatusSurvivesAnEmptyConfig: with a default configuration
// and no reference corpus, the model still reports a truthful status.
func TestTranslationFilesStatusSurvivesAnEmptyConfig(t *testing.T) {
	t.Setenv("UO_UNBURGER_ROOT", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := NewModel(Options{Config: appconfig.Default(), Getenv: noEnv})
	st := m.translationFiles()
	if st.loaded {
		t.Fatalf("no tables exist in the isolated temp root: %+v", st)
	}
	if st.honorificsWord() != "missing" {
		t.Fatalf("honorificsWord = %q, want missing", st.honorificsWord())
	}
}
