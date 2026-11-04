package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("unexpected exit code %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Commands:") {
		t.Fatalf("missing help commands: %s", stdout.String())
	}
}

func TestBuildRequiresArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"build"}, &stdout, &stderr); code != 2 {
		t.Fatalf("expected usage error (2), got %d", code)
	}
	if !strings.Contains(stderr.String(), "--variant") {
		t.Fatalf("missing usage message: %s", stderr.String())
	}
}

func TestBuildRejectsUnknownVariant(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"build", "--variant", "bogus", "--cpk", "x", "--corpus", "y"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected usage error (2), got %d", code)
	}
	if !strings.Contains(stderr.String(), "unknown variant") {
		t.Fatalf("missing variant message: %s", stderr.String())
	}
}

func TestValidateSourceRequiresCPK(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"validate-source"}, &stdout, &stderr); code != 2 {
		t.Fatalf("expected usage error (2), got %d", code)
	}
	if !strings.Contains(stderr.String(), "--cpk") {
		t.Fatalf("missing usage message: %s", stderr.String())
	}
}

func TestHelpMentionsBuildAndValidateSource(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("unexpected exit code %d", code)
	}
	if !strings.Contains(stdout.String(), "build ") || !strings.Contains(stdout.String(), "validate-source") {
		t.Fatalf("help must mention build and validate-source: %s", stdout.String())
	}
}

func TestUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"unexpected"}, &stdout, &stderr); code != 2 {
		t.Fatalf("expected usage error (2), got %d", code)
	}
}

func TestHelpMentionsImport(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("unexpected exit code %d", code)
	}
	if !strings.Contains(stdout.String(), "import --check") {
		t.Fatalf("help must mention the import command: %s", stdout.String())
	}
}

func TestImportRequiresCheck(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"import"}, &stdout, &stderr); code != 2 {
		t.Fatalf("expected usage error (2), got %d", code)
	}
}

func TestImportWorksWithoutCorpus(t *testing.T) {
	dir := t.TempDir()
	hon := "record_id\tsource_jp_sha\toriginal_en_sha\tenglish_text\n" +
		"UcSysMsg.fms:0\taaaaaaaaaaaa\tbbbbbbbbbbbb\tNew Game\n"
	if err := os.WriteFile(filepath.Join(dir, "honorifics.tsv"), []byte(hon), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"import", "--check", "--translations", dir}, &stdout, &stderr); code != 0 {
		t.Fatalf("import --check without a corpus exit = %d, stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "import --check: PASS") {
		t.Fatalf("output missing PASS verdict:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "records and linkage only") {
		t.Fatalf("output should state that source identity was not verified:\n%s", stdout.String())
	}
}

func TestHelpMentionsQA(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("unexpected exit code %d", code)
	}
	if !strings.Contains(stdout.String(), "qa ") {
		t.Fatalf("help must mention the qa command: %s", stdout.String())
	}
}

func TestQARequiresVariant(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"qa", "--corpus", "x"}, &stdout, &stderr); code != 2 {
		t.Fatalf("expected usage error (2), got %d", code)
	}
	if !strings.Contains(stderr.String(), "--variant") {
		t.Fatalf("missing variant usage: %s", stderr.String())
	}
}

func TestQARejectsUnknownArgument(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"qa", "--variant", "both", "--corpus", "x", "--bogus"}, &stdout, &stderr); code != 2 {
		t.Fatalf("expected usage error (2), got %d", code)
	}
}

func TestVersionVerbose(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"version", "--verbose"}, &stdout, &stderr); code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, stderr.String())
	}
	got := stdout.String()
	if !strings.Contains(got, "uo-unburger") || !strings.Contains(got, "go:") {
		t.Fatalf("verbose version incomplete: %s", got)
	}
}

func TestVersionRejectsUnknownFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"version", "--bogus"}, &stdout, &stderr); code != 2 {
		t.Fatalf("expected usage error (2), got %d", code)
	}
}

func TestHelpDocumentsExitCodesAndBoundary(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("unexpected exit code %d", code)
	}
	got := stdout.String()
	for _, want := range []string{"Exit codes:", "010069401ADB8000", "010069401ADB8800", "1.00", "1.05"} {
		if !strings.Contains(got, want) {
			t.Fatalf("help missing %q:\n%s", want, got)
		}
	}
}

func TestImportUsageOnBadArgument(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"import", "--bogus"}, &stdout, &stderr); code != 2 {
		t.Fatalf("expected usage error (2), got %d", code)
	}
	if !strings.Contains(stderr.String(), "usage: uo-unburger import") {
		t.Fatalf("import usage missing: %s", stderr.String())
	}
}

func TestImportMissingCheckShowsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"import"}, &stdout, &stderr); code != 2 {
		t.Fatalf("expected usage error (2), got %d", code)
	}
	if !strings.Contains(stderr.String(), "usage: uo-unburger import") {
		t.Fatalf("import usage missing: %s", stderr.String())
	}
}

func TestDoctorTooManyArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"doctor", "a.json", "b.json"}, &stdout, &stderr); code != 2 {
		t.Fatalf("expected usage error (2), got %d", code)
	}
	if !strings.Contains(stderr.String(), "usage: uo-unburger doctor") {
		t.Fatalf("doctor usage missing: %s", stderr.String())
	}
}

func TestBuildMissingFlagValueShowsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"build", "--variant"}, &stdout, &stderr); code != 2 {
		t.Fatalf("expected usage error (2), got %d", code)
	}
	if !strings.Contains(stderr.String(), "--variant requires a value") {
		t.Fatalf("missing flag message: %s", stderr.String())
	}
}
