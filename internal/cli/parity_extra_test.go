package cli

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"uo-unburger/internal/tui"
)

// The CLI is the scripting front end: its exit codes and its help text are the
// contract a packaging script relies on. These tests extend the existing parity
// checks with a full command inventory and real (synthetic) fixtures.

func syntheticTranslations(t *testing.T) (dir, corpus string) {
	t.Helper()
	dir = t.TempDir()
	jp, en := "ニューゲーム", "New Game"
	canonical, alternate := "New Journey", "A New Journey"
	sha1x12 := func(s string) string {
		sum := sha1.Sum([]byte(s))
		return hex.EncodeToString(sum[:])[:12]
	}
	sha256hex := func(s string) string {
		sum := sha256.Sum256([]byte(s))
		return hex.EncodeToString(sum[:])
	}
	honorifics := "record_id\tsource_jp_sha\toriginal_en_sha\tenglish_text\n" +
		"UcSysMsg.fms:0\t" + sha1x12(jp) + "\t" + sha1x12(en) + "\t" + canonical + "\n"
	if err := os.WriteFile(filepath.Join(dir, "honorifics.tsv"), []byte(honorifics), 0o644); err != nil {
		t.Fatal(err)
	}
	alternates := "record_id\tcanonical_sha256\tenglish_text\n" +
		"UcSysMsg.fms:0\t" + sha256hex(canonical) + "\t" + alternate + "\n"
	if err := os.WriteFile(filepath.Join(dir, "no-honorifics.tsv"), []byte(alternates), 0o644); err != nil {
		t.Fatal(err)
	}
	corpus = filepath.Join(dir, "corpus.jsonl")
	line, err := json.Marshal(map[string]string{"record_id": "UcSysMsg.fms:0", "jp": jp, "en": en})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(corpus, append(line, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, corpus
}

// TestHelpListsEveryCommand: the help text names every implemented command and
// the stable exit codes, and is written to stdout with exit 0.
func TestHelpListsEveryCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("help exit = %d, want 0", code)
	}
	if stderr.Len() != 0 {
		t.Fatalf("help wrote to stderr: %q", stderr.String())
	}
	got := stdout.String()
	for _, cmd := range []string{"help", "tui", "version", "doctor", "import", "build", "validate-source", "qa"} {
		if !strings.Contains(got, cmd) {
			t.Fatalf("help does not list %q:\n%s", cmd, got)
		}
	}
	for _, want := range []string{"Commands:", "Exit codes:", "Accepted source boundary:", "usage"} {
		if !strings.Contains(got, want) {
			t.Fatalf("help missing %q", want)
		}
	}
}

// TestUsageErrorsExitTwo enumerates every usage-error shape.
func TestUsageErrorsExitTwo(t *testing.T) {
	if tui.IsTerminal(os.Stdin) && tui.IsTerminal(os.Stdout) {
		t.Skip("a terminal is present; the no-argument path would start the interface")
	}
	cases := []struct {
		name string
		args []string
	}{
		{"no args", nil},
		{"unknown command", []string{"frobnicate"}},
		{"help with extra args", []string{"help", "extra"}},
		{"build bare", []string{"build"}},
		{"build bad variant", []string{"build", "--variant", "nope"}},
		{"build missing flag value", []string{"build", "--variant"}},
		{"build unknown flag", []string{"build", "--nope"}},
		{"validate-source bare", []string{"validate-source"}},
		{"import bare", []string{"import"}},
		{"import without --check", []string{"import", "--corpus", "x"}},
		{"qa bare", []string{"qa"}},
		{"qa bad variant", []string{"qa", "--variant", "nope", "--corpus", "x"}},
		{"qa unknown flag", []string{"qa", "--nope"}},
		{"version bad flag", []string{"version", "--nope"}},
		{"doctor too many args", []string{"doctor", "a", "b"}},
		{"tui with args", []string{"tui", "--x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Run(tc.args, &stdout, &stderr)
			if code != 2 {
				t.Fatalf("exit = %d, want 2 (stderr=%q)", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), "usage") {
				t.Fatalf("stderr must include a usage line: %q", stderr.String())
			}
		})
	}
}

// TestRuntimeErrorsExitOne: a missing input is a runtime failure (1), not a
// usage error.
func TestRuntimeErrorsExitOne(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "absent")
	translationsDir, corpus := syntheticTranslations(t)

	cases := []struct {
		name string
		args []string
	}{
		{"import missing corpus", []string{"import", "--check", "--corpus", missing, "--translations", translationsDir}},
		{"qa missing corpus", []string{"qa", "--variant", "both", "--corpus", missing, "--translations", translationsDir}},
		{"validate-source missing cpk", []string{"validate-source", "--cpk", missing}},
		{"build missing cpk", []string{"build", "--variant", "honorifics", "--cpk", missing, "--corpus", corpus, "--translations", translationsDir}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := Run(tc.args, &stdout, &stderr); code != 1 {
				t.Fatalf("exit = %d, want 1 (stderr=%q)", code, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("a runtime failure should not print a success report: %q", stdout.String())
			}
		})
	}
}

// TestImportCheckOnSyntheticTranslations: the CLI resolves a minimal translation
// pair end to end with exit 0 and a PASS diff verdict.
func TestImportCheckOnSyntheticTranslations(t *testing.T) {
	translationsDir, corpus := syntheticTranslations(t)
	var stdout, stderr bytes.Buffer
	code := Run([]string{"import", "--check", "--corpus", corpus, "--translations", translationsDir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, stderr.String())
	}
	got := stdout.String()
	for _, want := range []string{"canonical rows:  1", "overrides:       1", "overrides applied: 1", "diff verification: PASS", "import --check: PASS"} {
		if !strings.Contains(got, want) {
			t.Fatalf("import output missing %q:\n%s", want, got)
		}
	}
}

// TestQaWritesTSV: qa writes the machine TSV where asked, and a missing or
// unwritable destination is a clean runtime failure with no partial file.
func TestQaWritesTSV(t *testing.T) {
	translationsDir, corpus := syntheticTranslations(t)
	dir := t.TempDir()
	outTSV := filepath.Join(dir, "signals.tsv")

	var stdout, stderr bytes.Buffer
	code := Run([]string{"qa", "--variant", "both", "--corpus", corpus, "--translations", translationsDir, "--out", outTSV}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("qa exit = %d, stderr=%q", code, stderr.String())
	}
	data, err := os.ReadFile(outTSV)
	if err != nil {
		t.Fatalf("qa did not write the TSV: %v", err)
	}
	if !strings.Contains(string(data), "UcSysMsg.fms:0") {
		t.Fatalf("TSV missing the record:\n%s", data)
	}
	if !strings.Contains(stdout.String(), "TSV rows:") {
		t.Fatalf("qa summary missing the TSV line:\n%s", stdout.String())
	}

	// Missing destination directory: runtime failure, nothing written.
	missing := filepath.Join(dir, "nope", "signals.tsv")
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"qa", "--variant", "both", "--corpus", corpus, "--translations", translationsDir, "--out", missing}, &stdout, &stderr); code != 1 {
		t.Fatalf("qa with an unwritable destination exit = %d, want 1", code)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("a failed qa left a partial TSV: %v", err)
	}

	if os.Geteuid() == 0 {
		t.Skip("running as root: a read-only directory does not deny writes")
	}
	roDir := filepath.Join(dir, "ro")
	if err := os.MkdirAll(roDir, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(roDir, 0o755)
	stdout.Reset()
	stderr.Reset()
	roOut := filepath.Join(roDir, "signals.tsv")
	if code := Run([]string{"qa", "--variant", "both", "--corpus", corpus, "--translations", translationsDir, "--out", roOut}, &stdout, &stderr); code != 1 {
		t.Fatalf("qa into a read-only directory exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "writing TSV") {
		t.Fatalf("stderr should explain the write failure: %q", stderr.String())
	}
	if _, err := os.Stat(roOut); !os.IsNotExist(err) {
		t.Fatalf("a failed qa left a file in a read-only directory: %v", err)
	}
}

// TestValidateSourceRejectsANonCPKFile: a real file that is not a CPK fails the
// runtime gate (exit 1) with a readable message.
func TestValidateSourceRejectsANonCPKFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-cpk.bin")
	if err := os.WriteFile(path, []byte("this is not a CRI CPK at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"validate-source", "--cpk", path}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "CPK") {
		t.Fatalf("stderr should mention the CPK rejection: %q", stderr.String())
	}
}

// TestNeutralHeaderIsValidatedStrictly: a reordered or renamed header in the
// consolidated files is refused with a precise error, never reinterpreted.
func TestNeutralHeaderIsValidatedStrictly(t *testing.T) {
	cases := []struct {
		name   string
		file   string
		header string
	}{
		{"honorifics reordered", "honorifics.tsv", "record_id\tenglish_text\tsource_jp_sha\toriginal_en_sha\n"},
		{"honorifics renamed", "honorifics.tsv", "record_id\tsource_jp_sha\toriginal_en_sha\tcanonical_en\n"},
		{"alternates renamed", "no-honorifics.tsv", "record_id\tcanonical_sha\tenglish_text\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			corpus := filepath.Join(dir, "corpus.jsonl")
			if err := os.WriteFile(corpus, []byte(""), 0o644); err != nil {
				t.Fatal(err)
			}
			if tc.file == "honorifics.tsv" {
				if err := os.WriteFile(filepath.Join(dir, tc.file), []byte(tc.header), 0o644); err != nil {
					t.Fatal(err)
				}
			} else {
				good := "record_id\tsource_jp_sha\toriginal_en_sha\tenglish_text\n" +
					"UcSysMsg.fms:0\taaaaaaaaaaaa\tbbbbbbbbbbbb\tNew Game\n"
				if err := os.WriteFile(filepath.Join(dir, "honorifics.tsv"), []byte(good), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, tc.file), []byte(tc.header), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			var stdout, stderr bytes.Buffer
			if code := Run([]string{"import", "--check", "--corpus", corpus, "--translations", dir}, &stdout, &stderr); code != 1 {
				t.Fatalf("exit = %d, want 1 (stderr=%q)", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), "unexpected header") {
				t.Fatalf("error should name the unexpected header: %q", stderr.String())
			}
		})
	}
}

// neutralTranslations writes a minimal consolidated translation pair and returns
// their directory. The JP hash is provenance only in the public path.
func neutralTranslations(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	sha1x12 := func(s string) string {
		sum := sha1.Sum([]byte(s))
		return hex.EncodeToString(sum[:])[:12]
	}
	hon := "record_id\tsource_jp_sha\toriginal_en_sha\tenglish_text\n" +
		"UcSysMsg.fms:0\t" + sha1x12("jp") + "\t" + sha1x12("New Game") + "\tNew Journey\n"
	if err := os.WriteFile(filepath.Join(dir, "honorifics.tsv"), []byte(hon), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestDoctorValidatesCurrentFormatWithoutManifest: doctor reads the current
// translation tables and never requires the legacy manifest placeholder.
func TestDoctorValidatesCurrentFormatWithoutManifest(t *testing.T) {
	// Isolate the saved configuration so the check cannot pick up a machine-local
	// source archive. The project-local configuration root is pinned too.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("UO_UNBURGER_ROOT", t.TempDir())
	dir := neutralTranslations(t)

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"doctor", "--translations", dir}, &stdout, &stderr); code != 0 {
		t.Fatalf("doctor exit = %d, stderr=%q", code, stderr.String())
	}
	got := stdout.String()
	if !strings.Contains(got, "doctor: OK") {
		t.Fatalf("doctor should pass on the current format:\n%s", got)
	}
	if !strings.Contains(got, "honorifics.tsv rows=1") {
		t.Fatalf("doctor should report the dataset identity:\n%s", got)
	}
	if strings.Contains(got, "manifest") {
		t.Fatalf("doctor must not mention the legacy manifest:\n%s", got)
	}

	// A malformed table is a runtime failure naming the problem.
	bad := t.TempDir()
	if err := os.WriteFile(filepath.Join(bad, "honorifics.tsv"), []byte("record_id\tenglish_text\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"doctor", "--translations", bad}, &stdout, &stderr); code != 1 {
		t.Fatalf("doctor on a malformed table exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "unexpected header") {
		t.Fatalf("doctor should explain the malformed table: %q", stderr.String())
	}
}

// TestImportSourceFlagMissingFile: --source is validated like any other input
// and a missing archive is a runtime failure.
func TestImportSourceFlagMissingFile(t *testing.T) {
	dir := neutralTranslations(t)
	missing := filepath.Join(t.TempDir(), "absent.cpk")
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"import", "--check", "--translations", dir, "--source", missing}, &stdout, &stderr); code != 1 {
		t.Fatalf("import with a missing --source exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "source CPK") {
		t.Fatalf("stderr should mention the source CPK: %q", stderr.String())
	}
}

// TestQaWithoutCorpusRuns: a QA pass needs no reference corpus and states that
// source identity was not verified.
func TestQaWithoutCorpusRuns(t *testing.T) {
	dir := neutralTranslations(t)
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"qa", "--variant", "both", "--translations", dir}, &stdout, &stderr); code != 0 {
		t.Fatalf("qa without a corpus exit = %d, stderr=%q", code, stderr.String())
	}
	got := stdout.String()
	if !strings.Contains(got, "records and linkage only") {
		t.Fatalf("qa should state the limited source identity:\n%s", got)
	}
	if !strings.Contains(got, "SOURCE_IDENTITY_UNVERIFIED") {
		t.Fatalf("qa should flag unverified source identity:\n%s", got)
	}
}
