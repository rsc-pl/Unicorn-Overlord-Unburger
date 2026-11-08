package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildPackageFlagUsageErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"package with dry run", []string{"build", "--variant", "honorifics", "--package", "--dry-run"}, "--package requires a real build"},
		{"bad target", []string{"build", "--variant", "honorifics", "--target", "bogus"}, "unknown target"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := Run(tc.args, &stdout, &stderr); code != 2 {
				t.Fatalf("expected usage error (2), got %d (%s)", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("stderr = %q, want substring %q", stderr.String(), tc.want)
			}
		})
	}
}

func TestPackageRequiresRun(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"package"}, &stdout, &stderr); code != 2 {
		t.Fatalf("expected usage error (2), got %d", code)
	}
	if !strings.Contains(stderr.String(), "--run") {
		t.Fatalf("missing usage message: %s", stderr.String())
	}
}

func TestPackageRejectsBadTarget(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"package", "--run", "nope", "--target", "bogus"}, &stdout, &stderr); code != 2 {
		t.Fatalf("expected usage error (2), got %d", code)
	}
	if !strings.Contains(stderr.String(), "unknown target") {
		t.Fatalf("missing target message: %s", stderr.String())
	}
}

// TestModNameIsRejected: the emulator folder name is fixed by the packager, so
// the CLI refuses --mod-name (exit 2) for both build and package — the flag is
// kept only so a stale invocation gets a clear error instead of being ignored.
func TestModNameIsRejected(t *testing.T) {
	cases := [][]string{
		{"package", "--run", "some-run", "--mod-name", "custom"},
		{"build", "--variant", "honorifics", "--mod-name", "custom"},
	}
	for _, args := range cases {
		var stdout, stderr bytes.Buffer
		if code := Run(args, &stdout, &stderr); code != 2 {
			t.Fatalf("%v: expected usage error (2), got %d (%s)", args, code, stderr.String())
		}
		if !strings.Contains(stderr.String(), "not supported") ||
			!strings.Contains(stderr.String(), "Unicorn Overlord - Unburger Mod") {
			t.Fatalf("%v: stderr = %q, want a mod-name refusal naming the fixed folder", args, stderr.String())
		}
	}
}

func TestHelpMentionsPackage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("unexpected exit code %d", code)
	}
	if !strings.Contains(stdout.String(), "package") {
		t.Fatalf("help must mention package: %s", stdout.String())
	}
}

// TestPackageRequiresRootVersion: packaging is a release step, so it must read a
// non-empty root version.txt before it assembles anything.
func TestPackageRequiresRootVersion(t *testing.T) {
	root := makeProjectRoot(t)
	runDir := t.TempDir()

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"package", "--run", runDir}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit = %d, want 1 (%s)", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "version.txt missing/empty") {
		t.Fatalf("stderr = %q, want a version.txt missing/empty message", stderr.String())
	}

	// With a version present the command passes the version gate and continues
	// to the run check (which fails here because the run is empty).
	if err := os.WriteFile(filepath.Join(root, "version.txt"), []byte(" 2.0 \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"package", "--run", runDir}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit = %d, want 1 (%s)", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "version.txt missing/empty") {
		t.Fatalf("a present version was still rejected: %s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "no published editions") {
		t.Fatalf("stderr = %q, want the run check to be reached", stderr.String())
	}
}

// writePublishedRun creates a minimal published run directory (one edition) that
// LoadRun accepts, so the package command can be exercised without a build.
func writePublishedRun(t *testing.T, root string) string {
	t.Helper()
	runDir := t.TempDir()
	cpk := []byte("published cpk bytes for the cli package test")
	sum := sha256.Sum256(cpk)
	hexSum := hex.EncodeToString(sum[:])
	editionDir := filepath.Join(runDir, "honorifics", "romfs")
	if err := os.MkdirAll(editionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(editionDir, "Unicorn_US.CPK"), cpk, 0o644); err != nil {
		t.Fatal(err)
	}
	// The voice stage and title footer are mandatory packaging components since
	// the shared patch became part of every normal build, so a published run
	// fixture has to carry both.
	voiceMain := []byte("published synthetic patched exefs main")
	voiceSum := sha256.Sum256(voiceMain)
	voiceHex := hex.EncodeToString(voiceSum[:])
	voiceDir := filepath.Join(runDir, "voice", "exefs")
	if err := os.MkdirAll(voiceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(voiceDir, "main"), voiceMain, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "voice", "main.ips"), []byte("published synthetic ips"), 0o644); err != nil {
		t.Fatal(err)
	}
	voiceReport := "{\n" +
		"  \"plan\": \"jp-voice-config-v5\",\n" +
		"  \"build_id\": \"c841ffe2717ff03a13990480c51da73f091c04fa\",\n" +
		"  \"artifact_rel\": \"exefs/main\",\n" +
		"  \"artifact_sha256\": \"" + voiceHex + "\",\n" +
		"  \"artifact_size\": " + itoaCLI(uint32(len(voiceMain))) + ",\n" +
		"  \"runtime_status\": \"INTEGRATED_VERIFIED\"\n}\n"
	if err := os.WriteFile(filepath.Join(runDir, "voice", "VOICE_REPORT.json"), []byte(voiceReport), 0o644); err != nil {
		t.Fatal(err)
	}
	report := "{\n" +
		"  \"variant\": \"honorifics\",\n" +
		"  \"title_id\": \"010069401ADB8000\",\n" +
		"  \"dataset_canonical_sha256\": \"" + strings.Repeat("ab", 32) + "\",\n" +
		"  \"version\": \"2.0\",\n" +
		"  \"output_sha256\": \"" + hexSum + "\",\n" +
		"  \"output_size\": " + itoaCLI(uint32(len(cpk))) + ",\n" +
		"  \"title_footer\": \"UNBURGER MOD 1.2.3\",\n" +
		"  \"title_footer_member\": \"GUI/Opening100.ftx\",\n" +
		"  \"title_footer_size\": 18876304,\n" +
		"  \"title_footer_sha256\": \"" + strings.Repeat("d", 64) + "\"\n}\n"
	if err := os.WriteFile(filepath.Join(runDir, "honorifics", "BUILD_REPORT.json"), []byte(report), 0o644); err != nil {
		t.Fatal(err)
	}
	return runDir
}

// TestPackageForceReplacesExistingRelease: a second package into a populated
// release folder is refused by name without --force, and replaces it with --force.
func TestPackageForceReplacesExistingRelease(t *testing.T) {
	root := makeProjectRoot(t)
	if err := os.WriteFile(filepath.Join(root, "version.txt"), []byte("2.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runDir := writePublishedRun(t, root)

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"package", "--run", runDir}, &stdout, &stderr); code != 0 {
		t.Fatalf("first package exit = %d (%s)", code, stderr.String())
	}
	release := filepath.Join(root, ".local", "release")
	first, err := os.ReadDir(release)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) == 0 {
		t.Fatal("first package wrote nothing")
	}

	// A repeat names the colliding files and writes nothing.
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"package", "--run", runDir}, &stdout, &stderr); code != 1 {
		t.Fatalf("repeat package exit = %d, want 1 (%s)", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "output already exists") {
		t.Fatalf("stderr = %q, want an actionable collision message", stderr.String())
	}
	zipName := "unicorn-overlord-honorifics-atmosphere-" + shortHash(strings.Repeat("ab", 32)) + ".zip"
	if !strings.Contains(stderr.String(), zipName) {
		t.Fatalf("refusal must name %s: %s", zipName, stderr.String())
	}

	// --force replaces it.
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"package", "--run", runDir, "--force"}, &stdout, &stderr); code != 0 {
		t.Fatalf("--force package exit = %d (%s)", code, stderr.String())
	}
}

func shortHash(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
