package installer

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testTitleID = "010069401ADB8000"

// testValidator is a deterministic stand-in for the shared source gate: it
// hashes whatever file it is given, so the installer tests need no game data.
func testValidator(path string) (*SourceInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	return &SourceInfo{Path: path, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))}, nil
}

func options(root string) Options {
	return Options{
		Root:     root,
		TitleID:  testTitleID,
		Validate: testValidator,
		RunID:    "test-run",
		Now:      func() time.Time { return time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC) },
	}
}

func writeSource(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing source: %v", err)
	}
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}

func targetPath(root string) string {
	return filepath.Join(root, "contents", testTitleID, filepath.FromSlash(ArtifactRel))
}

func TestInstallWritesOverrideAndMeta(t *testing.T) {
	root := t.TempDir()
	src := writeSource(t, t.TempDir(), "Unicorn_US.CPK", "cpk-A")

	preview, err := PreviewInstall(src, options(root))
	if err != nil {
		t.Fatalf("PreviewInstall: %v", err)
	}
	if preview.Action != "install" || preview.Existing {
		t.Fatalf("unexpected preview: %+v", preview)
	}

	res, err := Install(src, options(root))
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if res.Action != "install" || !res.Changed {
		t.Fatalf("unexpected result: %+v", res)
	}
	if got := readFile(t, targetPath(root)); got != "cpk-A" {
		t.Fatalf("override content = %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, "contents", testTitleID, MetaName)); err != nil {
		t.Fatalf("metadata missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, LockName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock file must be released, stat err = %v", err)
	}
}

func TestUpdateBacksUpPreviousFile(t *testing.T) {
	root := t.TempDir()
	dir := t.TempDir()
	first := writeSource(t, dir, "a.cpk", "cpk-A")
	second := writeSource(t, dir, "b.cpk", "cpk-B")

	if _, err := Install(first, options(root)); err != nil {
		t.Fatalf("first install: %v", err)
	}
	preview, err := PreviewInstall(second, options(root))
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview.Action != "update" || !preview.BackupRequired {
		t.Fatalf("expected an update requiring a backup, got %+v", preview)
	}
	res, err := Install(second, options(root))
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if res.Action != "update" || res.BackupPath == "" {
		t.Fatalf("unexpected result %+v", res)
	}
	if got := readFile(t, targetPath(root)); got != "cpk-B" {
		t.Fatalf("override content = %q", got)
	}
	if got := readFile(t, res.BackupPath); got != "cpk-A" {
		t.Fatalf("backup content = %q", got)
	}
	backups, err := Backups(options(root))
	if err != nil {
		t.Fatalf("Backups: %v", err)
	}
	if len(backups) != 1 {
		t.Fatalf("expected 1 backup, got %d", len(backups))
	}
}

func TestReinstallWhenMetadataIsAbsent(t *testing.T) {
	root := t.TempDir()
	dir := t.TempDir()
	first := writeSource(t, dir, "a.cpk", "cpk-A")
	second := writeSource(t, dir, "b.cpk", "cpk-B")

	if _, err := Install(first, options(root)); err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := os.Remove(filepath.Join(root, "contents", testTitleID, MetaName)); err != nil {
		t.Fatalf("removing meta: %v", err)
	}
	preview, err := PreviewInstall(second, options(root))
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview.Action != "reinstall" {
		t.Fatalf("action = %q, want reinstall", preview.Action)
	}
}

func TestInstallIsIdempotentNoopWhenIdentical(t *testing.T) {
	root := t.TempDir()
	src := writeSource(t, t.TempDir(), "a.cpk", "cpk-A")
	if _, err := Install(src, options(root)); err != nil {
		t.Fatalf("install: %v", err)
	}
	preview, err := PreviewInstall(src, options(root))
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview.Action != "noop" || !preview.Identical {
		t.Fatalf("expected a no-op install, got %+v", preview)
	}
	res, err := Install(src, options(root))
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if res.Changed {
		t.Fatalf("a no-op install must not report a change")
	}
	backups, err := Backups(options(root))
	if err != nil {
		t.Fatalf("Backups: %v", err)
	}
	if len(backups) != 0 {
		t.Fatalf("a no-op install must not create a backup, got %d", len(backups))
	}
}

func TestUninstallPreservesUnrelatedFiles(t *testing.T) {
	root := t.TempDir()
	src := writeSource(t, t.TempDir(), "a.cpk", "cpk-A")
	if _, err := Install(src, options(root)); err != nil {
		t.Fatalf("install: %v", err)
	}
	contents := filepath.Join(root, "contents", testTitleID)
	extra := filepath.Join(contents, "romfs", "EXTRA.bin")
	if err := os.WriteFile(extra, []byte("keep"), 0o600); err != nil {
		t.Fatalf("writing unrelated file: %v", err)
	}
	notes := filepath.Join(contents, "NOTES.txt")
	if err := os.WriteFile(notes, []byte("keep"), 0o600); err != nil {
		t.Fatalf("writing unrelated note: %v", err)
	}

	res, err := Uninstall(options(root))
	if err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if res.Action != "uninstall" || res.BackupPath == "" {
		t.Fatalf("unexpected result %+v", res)
	}
	if _, err := os.Stat(targetPath(root)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("override must be removed, stat err = %v", err)
	}
	if got := readFile(t, res.BackupPath); got != "cpk-A" {
		t.Fatalf("backup content = %q", got)
	}
	if got := readFile(t, extra); got != "keep" {
		t.Fatalf("unrelated romfs file lost: %q", got)
	}
	if got := readFile(t, notes); got != "keep" {
		t.Fatalf("unrelated contents file lost: %q", got)
	}
	if _, err := os.Stat(filepath.Join(contents, "romfs")); err != nil {
		t.Fatalf("romfs directory holding an unrelated file must remain: %v", err)
	}
	if _, err := os.Stat(filepath.Join(contents, MetaName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("metadata must be removed with the override, stat err = %v", err)
	}
}

func TestUninstallRemovesEmptyDirectories(t *testing.T) {
	root := t.TempDir()
	src := writeSource(t, t.TempDir(), "a.cpk", "cpk-A")
	if _, err := Install(src, options(root)); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := Uninstall(options(root)); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "contents", testTitleID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty contents directory should be removed, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "contents")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty contents root should be removed, stat err = %v", err)
	}
}

func TestUninstallRequiresAnInstallation(t *testing.T) {
	if _, err := Uninstall(options(t.TempDir())); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("want ErrNotInstalled, got %v", err)
	}
	if _, err := PreviewUninstall(options(t.TempDir())); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("want ErrNotInstalled from PreviewUninstall, got %v", err)
	}
}

func TestRollbackRestoresPreviousBytes(t *testing.T) {
	root := t.TempDir()
	dir := t.TempDir()
	first := writeSource(t, dir, "a.cpk", "cpk-A")
	second := writeSource(t, dir, "b.cpk", "cpk-B")
	if _, err := Install(first, options(root)); err != nil {
		t.Fatalf("install A: %v", err)
	}
	if _, err := Install(second, options(root)); err != nil {
		t.Fatalf("install B: %v", err)
	}
	res, err := Rollback(options(root), "")
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if res.Action != "rollback" || !res.Changed {
		t.Fatalf("unexpected result %+v", res)
	}
	if got := readFile(t, targetPath(root)); got != "cpk-A" {
		t.Fatalf("rollback content = %q, want cpk-A", got)
	}
	if res.BackupPath == "" {
		t.Fatalf("rollback must back up the file it replaces")
	}
	if got := readFile(t, res.BackupPath); got != "cpk-B" {
		t.Fatalf("pre-rollback backup content = %q, want cpk-B", got)
	}
}

func TestRollbackSpecificBackup(t *testing.T) {
	root := t.TempDir()
	dir := t.TempDir()
	first := writeSource(t, dir, "a.cpk", "cpk-A")
	second := writeSource(t, dir, "b.cpk", "cpk-B")
	if _, err := Install(first, options(root)); err != nil {
		t.Fatalf("install A: %v", err)
	}
	if _, err := Install(second, options(root)); err != nil {
		t.Fatalf("install B: %v", err)
	}
	backups, err := Backups(options(root))
	if err != nil || len(backups) != 1 {
		t.Fatalf("Backups: %v (%d)", err, len(backups))
	}
	if _, err := Rollback(options(root), backups[0].Path); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if got := readFile(t, targetPath(root)); got != "cpk-A" {
		t.Fatalf("rollback content = %q", got)
	}
	if _, err := Rollback(options(root), filepath.Join(root, "missing.cpk")); !errors.Is(err, ErrNoBackup) {
		t.Fatalf("want ErrNoBackup, got %v", err)
	}
}

func TestRollbackAfterUninstallRestoresFile(t *testing.T) {
	root := t.TempDir()
	src := writeSource(t, t.TempDir(), "a.cpk", "cpk-A")
	if _, err := Install(src, options(root)); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := Uninstall(options(root)); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, err := Rollback(options(root), ""); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if got := readFile(t, targetPath(root)); got != "cpk-A" {
		t.Fatalf("restored content = %q", got)
	}
}

func TestProtectedRootRequiresTypedConfirmation(t *testing.T) {
	root := t.TempDir()
	src := writeSource(t, t.TempDir(), "a.cpk", "cpk-A")
	opts := options(root)
	opts.ProtectedRoots = []string{root}

	preview, err := PreviewInstall(src, opts)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if !preview.Protected || preview.ConfirmationToken != testTitleID {
		t.Fatalf("expected a protected target, got %+v", preview)
	}
	if _, err := Install(src, opts); !errors.Is(err, ErrProtected) {
		t.Fatalf("want ErrProtected without confirmation, got %v", err)
	}
	if _, err := os.Stat(targetPath(root)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused install must not write anything")
	}
	opts.Confirm = testTitleID
	if _, err := Install(src, opts); err != nil {
		t.Fatalf("install with confirmation: %v", err)
	}
	if _, err := Uninstall(opts); err != nil {
		t.Fatalf("uninstall with confirmation: %v", err)
	}
	opts.Confirm = ""
	if _, err := Uninstall(opts); !errors.Is(err, ErrProtected) {
		t.Fatalf("want ErrProtected on unconfirmed uninstall, got %v", err)
	}
	if _, err := Rollback(opts, ""); !errors.Is(err, ErrProtected) {
		t.Fatalf("want ErrProtected on unconfirmed rollback, got %v", err)
	}
}

func TestOptionValidation(t *testing.T) {
	src := writeSource(t, t.TempDir(), "a.cpk", "cpk-A")
	cases := []struct {
		name string
		opts Options
		want error
	}{
		{"empty root", Options{TitleID: testTitleID, Validate: testValidator}, ErrNoRoot},
		{"root separator", Options{Root: "/", TitleID: testTitleID, Validate: testValidator}, ErrNoRoot},
		{"relative root", Options{Root: "atmosphere", TitleID: testTitleID, Validate: testValidator}, ErrRelativeRoot},
		{"empty title", Options{Root: t.TempDir(), Validate: testValidator}, ErrUnsupportedTitleID},
		{"bad title", Options{Root: t.TempDir(), TitleID: "0100BAD", Validate: testValidator}, ErrUnsupportedTitleID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := PreviewInstall(src, tc.opts); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestFreshLockBlocksAndStaleLockIsReplaced(t *testing.T) {
	root := t.TempDir()
	src := writeSource(t, t.TempDir(), "a.cpk", "cpk-A")
	lock := filepath.Join(root, LockName)
	if err := os.WriteFile(lock, []byte("pid=1\n"), 0o600); err != nil {
		t.Fatalf("writing lock: %v", err)
	}
	if _, err := Install(src, options(root)); !errors.Is(err, ErrLocked) {
		t.Fatalf("a fresh lock must block an install, got %v", err)
	}

	// options() pins the clock, so age the lock relative to that pinned time.
	stale := time.Date(2026, 10, 17, 8, 0, 0, 0, time.UTC)
	if err := os.Chtimes(lock, stale, stale); err != nil {
		t.Fatalf("aging lock: %v", err)
	}
	if _, err := Install(src, options(root)); err != nil {
		t.Fatalf("a stale lock must be replaced, got %v", err)
	}
	if _, err := os.Stat(lock); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock must be released after the operation, stat err = %v", err)
	}
}

func TestDefaultValidatorRejectsAMissingSource(t *testing.T) {
	root := t.TempDir()
	opts := Options{Root: root, TitleID: testTitleID}
	_, err := Install(filepath.Join(t.TempDir(), "absent.cpk"), opts)
	if err == nil {
		t.Fatalf("a missing source must be rejected")
	}
	if !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("want ErrInvalidSource from the shared gate, got %v", err)
	}
	if _, statErr := os.Stat(targetPath(root)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("nothing may be written when the source is rejected")
	}
}

func TestInstallLeavesNoTemporaryFiles(t *testing.T) {
	root := t.TempDir()
	src := writeSource(t, t.TempDir(), "a.cpk", strings.Repeat("x", 4096))
	if _, err := Install(src, options(root)); err != nil {
		t.Fatalf("install: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "contents", testTitleID, "romfs"))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Fatalf("temporary file left behind: %s", e.Name())
		}
		if e.Name() != filepath.Base(targetPath(root)) {
			t.Fatalf("unexpected file in romfs: %s", e.Name())
		}
	}
}
