package build

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestResolveOutputConflicts exercises the four policies on a destination set
// that contains one existing file and one free path.
func TestResolveOutputConflicts(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "x.zip")
	free := filepath.Join(dir, "new.zip")
	mustWrite(t, existing, []byte("old"))
	paths := []string{existing, free}

	// Refuse names the colliding file (not just its directory) and writes nothing.
	if _, err := resolveOutputConflicts(paths, false, nil); !errors.Is(err, ErrOutputExists) {
		t.Fatalf("refuse: err = %v, want ErrOutputExists", err)
	} else if !strings.Contains(err.Error(), existing) {
		t.Fatalf("refusal must name the colliding file: %v", err)
	}

	// Replace keeps the destination names.
	got, err := resolveOutputConflicts(paths, true, nil)
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if got[0] != existing || got[1] != free {
		t.Fatalf("replace changed names: %v", got)
	}

	// Keep both picks a deterministic "-2" sibling for the colliding path only.
	keep := func([]string) ConflictPolicy { return ConflictKeepBoth }
	first, err := resolveOutputConflicts(paths, false, keep)
	if err != nil {
		t.Fatalf("keep both: %v", err)
	}
	want := filepath.Join(dir, "x-2.zip")
	if first[0] != want || first[1] != free {
		t.Fatalf("keep both = %v, want %q and %q", first, want, free)
	}
	again, _ := resolveOutputConflicts(paths, false, keep)
	if again[0] != want {
		t.Fatalf("keep both is not deterministic: %q vs %q", again[0], want)
	}
	// A "-2" already taken pushes the deterministic name to "-3".
	mustWrite(t, want, []byte("second"))
	third, _ := resolveOutputConflicts(paths, false, keep)
	if third[0] != filepath.Join(dir, "x-3.zip") {
		t.Fatalf("keep both should advance to -3, got %q", third[0])
	}

	// Cancel changes nothing.
	if _, err := resolveOutputConflicts(paths, false, func([]string) ConflictPolicy { return ConflictCancel }); !errors.Is(err, ErrOutputCancelled) {
		t.Fatalf("cancel: err = %v, want ErrOutputCancelled", err)
	}
}

// TestPublishSetRollsBackWholeSet: when one rename in a multi-file publish fails,
// every successfully replaced destination is restored to its previous content and
// no backup or half-published file is left behind.
func TestPublishSetRollsBackWholeSet(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.bin")
	b := filepath.Join(dir, "b.bin")
	mustWrite(t, a, []byte("old-a"))
	mustWrite(t, b, []byte("old-b"))

	tmpA := filepath.Join(dir, ".staging-a")
	mustWrite(t, tmpA, []byte("new-a"))
	missing := filepath.Join(dir, ".staging-missing") // never created: its rename fails

	err := publishSet([]publishItem{{tmp: tmpA, final: a}, {tmp: missing, final: b}})
	if err == nil {
		t.Fatal("publishSet must fail when a staged item is missing")
	}
	if got := readFile(t, a); got != "old-a" {
		t.Fatalf("a was not rolled back: %q", got)
	}
	if got := readFile(t, b); got != "old-b" {
		t.Fatalf("b was not preserved: %q", got)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".bak-") {
			t.Fatalf("a backup file was left behind: %s", e.Name())
		}
		if strings.HasPrefix(e.Name(), ".staging-") && e.Name() != filepath.Base(tmpA) {
			t.Fatalf("a stray staging file was left behind: %s", e.Name())
		}
	}
}

// TestStaleStagingSweepDoesNotBlock: an abandoned ".staging-*" directory (and the
// legacy "<name>.staging" form) never blocks a new build, and an old one is swept.
func TestStaleStagingSweepDoesNotBlock(t *testing.T) {
	dir, src, srcSHA, corpus, translations := idempotentFixture(t)
	out := filepath.Join(dir, "out")
	runDir := filepath.Join(out, "test-run")

	stale := filepath.Join(runDir, ".staging-dead")
	legacy := filepath.Join(runDir, "honorifics.staging")
	mustWrite(t, filepath.Join(stale, "junk"), []byte("x"))
	mustWrite(t, filepath.Join(legacy, "junk"), []byte("x"))
	old := time.Now().Add(-2 * time.Hour)
	for _, p := range []string{stale, legacy} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}

	o := baseOptions(src, corpus, translations, out, VariantBoth)
	o.Expect = SourceExpectation{SHA256: srcSHA}
	res, err := Run(o)
	if err != nil {
		t.Fatalf("a stale staging directory must not block a build: %v", err)
	}
	if len(res.Editions) != 2 {
		t.Fatalf("build produced %d editions", len(res.Editions))
	}
	for _, p := range []string{stale, legacy} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("stale staging %s was not swept", p)
		}
	}
}

// TestPackageExistingOutputPolicy: a repeated package must never silently
// overwrite a previous release. Without a policy it refuses and names every
// colliding file; Keep both writes a fresh, deterministic "-2" set and leaves the
// originals byte-identical; Replace overwrites in place with identical bytes.
func TestPackageExistingOutputPolicy(t *testing.T) {
	res, dir := packageFixture(t)
	outDir := filepath.Join(dir, "release")

	first, err := Package(res, PackageOptions{Targets: TargetBoth, OutDir: outDir})
	if err != nil {
		t.Fatalf("first package: %v", err)
	}
	orig := map[string][]byte{}
	for _, a := range first.Artifacts {
		data, err := os.ReadFile(a.Path)
		if err != nil {
			t.Fatal(err)
		}
		orig[a.RelPath] = data
	}

	// Refuse: the error names the colliding files, and nothing changes.
	_, err = Package(res, PackageOptions{Targets: TargetBoth, OutDir: outDir})
	if !errors.Is(err, ErrOutputExists) {
		t.Fatalf("repeat package err = %v, want ErrOutputExists", err)
	}
	for rel := range orig {
		if !strings.Contains(err.Error(), rel) {
			t.Fatalf("refusal does not name %s: %v", rel, err)
		}
	}
	for rel, data := range orig {
		if got, _ := os.ReadFile(filepath.Join(outDir, rel)); !bytes.Equal(got, data) {
			t.Fatalf("refused package modified %s", rel)
		}
	}

	// Keep both: the new archives get "-2" names and the originals are untouched.
	second, err := Package(res, PackageOptions{Targets: TargetBoth, OutDir: outDir,
		OnConflict: func([]string) ConflictPolicy { return ConflictKeepBoth }})
	if err != nil {
		t.Fatalf("keep-both package: %v", err)
	}
	for _, a := range second.Artifacts {
		if _, clash := orig[a.RelPath]; clash {
			t.Fatalf("keep both reused the existing name %s", a.RelPath)
		}
		if !strings.Contains(a.RelPath, "-2.zip") {
			t.Fatalf("keep-both archive %s lacks a -2 suffix", a.RelPath)
		}
		if _, err := os.Stat(a.Path); err != nil {
			t.Fatalf("keep-both archive missing: %v", err)
		}
	}
	for rel, data := range orig {
		if got, _ := os.ReadFile(filepath.Join(outDir, rel)); !bytes.Equal(got, data) {
			t.Fatalf("keep both modified the original %s", rel)
		}
	}

	// Cancel: nothing is written and every existing file keeps its bytes.
	_, err = Package(res, PackageOptions{Targets: TargetBoth, OutDir: outDir,
		OnConflict: func([]string) ConflictPolicy { return ConflictCancel }})
	if !errors.Is(err, ErrOutputCancelled) {
		t.Fatalf("cancel package err = %v, want ErrOutputCancelled", err)
	}
	for rel, data := range orig {
		if got, _ := os.ReadFile(filepath.Join(outDir, rel)); !bytes.Equal(got, data) {
			t.Fatalf("cancel modified %s", rel)
		}
	}

	// Replace: same names, identical bytes (the archives are deterministic).
	third, err := Package(res, PackageOptions{Targets: TargetBoth, OutDir: outDir, Overwrite: true})
	if err != nil {
		t.Fatalf("overwrite package: %v", err)
	}
	if len(third.Artifacts) != len(first.Artifacts) {
		t.Fatalf("overwrite changed the archive count")
	}
	for i, a := range third.Artifacts {
		if a.RelPath != first.Artifacts[i].RelPath {
			t.Fatalf("overwrite changed %s to %s", first.Artifacts[i].RelPath, a.RelPath)
		}
		got, _ := os.ReadFile(a.Path)
		if !bytes.Equal(got, orig[a.RelPath]) {
			t.Fatalf("overwrite produced different bytes for %s", a.RelPath)
		}
	}
}

// TestPackageSweepsStaleTempFiles: a partial ".tmp" left by an interrupted
// packaging run is swept (when old) and never blocks the next package.
func TestPackageSweepsStaleTempFiles(t *testing.T) {
	res, dir := packageFixture(t)
	outDir := filepath.Join(dir, "release")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(outDir, ".unicorn-overlord-honorifics-atmosphere-000000000000.zip.tmp-dead")
	mustWrite(t, stale, []byte("half a zip"))
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := Package(res, PackageOptions{Targets: TargetBoth, OutDir: outDir}); err != nil {
		t.Fatalf("a stale temp file must not block packaging: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale temp file was not swept")
	}
	entries, _ := os.ReadDir(outDir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Fatalf("packaging left a temp file behind: %s", e.Name())
		}
	}
}

// TestDefaultRunNameDoesNotCollide: the date-based default run name never blocks a
// same-day rebuild. An identical repeat reuses the verified run in place; a build
// whose identity changed writes a new "-2" run beside the first instead of
// overwriting it or failing.
func TestDefaultRunNameDoesNotCollide(t *testing.T) {
	dir, src, srcSHA, corpus, translations := idempotentFixture(t)
	out := filepath.Join(dir, "out")
	o := baseOptions(src, corpus, translations, out, VariantBoth)
	o.RunName = "" // the default, date-based name
	o.Date = "20260101"
	o.Expect = SourceExpectation{SHA256: srcSHA}

	first, err := Run(o)
	if err != nil {
		t.Fatalf("first build: %v", err)
	}
	if first.RunName != "translations-20260101" {
		t.Fatalf("default run name = %q", first.RunName)
	}

	// An identical repeat reuses the run: same name, same artifacts, no new dir.
	again, err := Run(o)
	if err != nil {
		t.Fatalf("identical repeat: %v", err)
	}
	if again.RunName != first.RunName || again.RunDir != first.RunDir {
		t.Fatalf("identical repeat created a new run: %q vs %q", again.RunName, first.RunName)
	}

	// A changed identity gets its own run name; the first is preserved untouched.
	changed := o
	changed.Version = "9.9.9"
	third, err := Run(changed)
	if err != nil {
		t.Fatalf("changed build: %v", err)
	}
	if third.RunName != "translations-20260101-2" {
		t.Fatalf("changed build run name = %q, want translations-20260101-2", third.RunName)
	}
	cpk := filepath.Join(out, "translations-20260101", "honorifics", filepath.FromSlash(artifactRel))
	if _, err := os.Stat(cpk); err != nil {
		t.Fatalf("the first run was disturbed: %v", err)
	}
}
