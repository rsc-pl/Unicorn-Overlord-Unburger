package installer

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestFullLifecyclePreservesNeighboursAtEveryStep walks install -> update ->
// uninstall -> reinstall -> rollback on one root while an unrelated neighbour
// file exists, and checks that every step leaves that neighbour untouched and
// that the backup chain grows and rolls back in order.
func TestFullLifecyclePreservesNeighboursAtEveryStep(t *testing.T) {
	root := t.TempDir()
	src := t.TempDir()
	v1 := writeSource(t, src, "v1.cpk", "cpk-v1")
	v2 := writeSource(t, src, "v2.cpk", "cpk-v2")
	v3 := writeSource(t, src, "v3.cpk", "cpk-v3")

	// An unrelated file inside the very directory the installer manages.
	neighbourDir := filepath.Dir(targetPath(root))
	if err := os.MkdirAll(neighbourDir, 0o755); err != nil {
		t.Fatal(err)
	}
	neighbour := filepath.Join(neighbourDir, "keep-me.bin")
	if err := os.WriteFile(neighbour, []byte("unrelated"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A second .fms member next to the archive must also survive.
	otherMember := filepath.Join(filepath.Dir(neighbourDir), "romfs", "Sibling.fms")
	if err := os.WriteFile(otherMember, []byte("sibling"), 0o600); err != nil {
		t.Fatal(err)
	}

	steps := []struct {
		name string
		run  func() (*Result, error)
		want string
	}{
		{"install", func() (*Result, error) { return Install(v1, options(root)) }, "cpk-v1"},
		{"update", func() (*Result, error) { return Install(v2, options(root)) }, "cpk-v2"},
		{"uninstall", func() (*Result, error) { return Uninstall(options(root)) }, ""},
		{"reinstall", func() (*Result, error) { return Install(v3, options(root)) }, "cpk-v3"},
	}
	for _, step := range steps {
		res, err := step.run()
		if err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if step.want == "" {
			if _, err := os.Stat(targetPath(root)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("%s: target still present", step.name)
			}
		} else if got := readFile(t, targetPath(root)); got != step.want {
			t.Fatalf("%s: target = %q, want %q", step.name, got, step.want)
		}
		if got := readFile(t, neighbour); got != "unrelated" {
			t.Fatalf("%s: neighbour changed to %q", step.name, got)
		}
		if got := readFile(t, otherMember); got != "sibling" {
			t.Fatalf("%s: sibling member changed to %q", step.name, got)
		}
		if res.Action == "" {
			t.Fatalf("%s: empty action", step.name)
		}
	}

	backups, err := Backups(options(root))
	if err != nil {
		t.Fatalf("backups: %v", err)
	}
	if len(backups) < 2 {
		t.Fatalf("expected a backup chain of at least 2 entries, got %d", len(backups))
	}
	// Rolling back to the backup that holds the first installed bytes restores v1.
	var oldest string
	for _, b := range backups {
		if readFile(t, b.Path) == "cpk-v1" {
			oldest = b.Path
		}
	}
	if oldest == "" {
		t.Fatalf("no backup holds the first installed bytes; chain = %v", backups)
	}
	if _, err := Rollback(options(root), oldest); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if got := readFile(t, targetPath(root)); got != "cpk-v1" {
		t.Fatalf("rollback restored %q, want cpk-v1", got)
	}
	if got := readFile(t, neighbour); got != "unrelated" {
		t.Fatalf("rollback changed the neighbour to %q", got)
	}
	assertNoTempFiles(t, neighbourDir)
}

// TestReadOnlyTargetFailsCleanly: when the destination directory refuses writes
// the install must fail without touching the existing bytes and without leaving
// a temporary file behind.
func TestReadOnlyTargetFailsCleanly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permissions are not enforced")
	}
	root := t.TempDir()
	src := t.TempDir()
	v1 := writeSource(t, src, "v1.cpk", "cpk-v1")
	v2 := writeSource(t, src, "v2.cpk", "cpk-v2")
	if _, err := Install(v1, options(root)); err != nil {
		t.Fatalf("initial install: %v", err)
	}
	dir := filepath.Dir(targetPath(root))
	before := readFile(t, targetPath(root))
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o755) //nolint:errcheck // best-effort restore for cleanup

	_, err := Install(v2, options(root))
	if err == nil {
		t.Fatal("install into a read-only directory unexpectedly succeeded")
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, targetPath(root)); got != before {
		t.Fatalf("failed install changed the target: %q -> %q", before, got)
	}
	assertNoTempFiles(t, dir)
	// The lock must have been released so a later operation still works.
	if _, err := Install(v2, options(root)); err != nil {
		t.Fatalf("install after a failed attempt: %v", err)
	}
}

// TestBackupDirectoryBlockedFailsCleanly: if the backup directory path is
// occupied by a regular file the operation must fail cleanly and leave both the
// installed bytes and the blocking file alone.
func TestBackupDirectoryBlockedFailsCleanly(t *testing.T) {
	root := t.TempDir()
	src := t.TempDir()
	v1 := writeSource(t, src, "v1.cpk", "cpk-v1")
	v2 := writeSource(t, src, "v2.cpk", "cpk-v2")
	if _, err := Install(v1, options(root)); err != nil {
		t.Fatalf("initial install: %v", err)
	}
	before := readFile(t, targetPath(root))
	blocker := filepath.Join(root, BackupDirName)
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(v2, options(root)); err == nil {
		t.Fatal("install with a blocked backup directory unexpectedly succeeded")
	}
	if got := readFile(t, targetPath(root)); got != before {
		t.Fatalf("blocked install changed the target: %q -> %q", before, got)
	}
	if got := readFile(t, blocker); got != "not a directory" {
		t.Fatalf("blocker file changed to %q", got)
	}
	assertNoTempFiles(t, filepath.Dir(targetPath(root)))
}

// TestRollbackWithAnUnreadableBackupFailsCleanly: a corrupt backup must never be
// installed; the current file stays exactly as it was.
func TestRollbackWithAnUnreadableBackupFailsCleanly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: file permissions are not enforced")
	}
	root := t.TempDir()
	src := t.TempDir()
	v1 := writeSource(t, src, "v1.cpk", "cpk-v1")
	v2 := writeSource(t, src, "v2.cpk", "cpk-v2")
	if _, err := Install(v1, options(root)); err != nil {
		t.Fatalf("initial install: %v", err)
	}
	if _, err := Install(v2, options(root)); err != nil {
		t.Fatalf("update: %v", err)
	}
	before := readFile(t, targetPath(root))
	backups, err := Backups(options(root))
	if err != nil || len(backups) == 0 {
		t.Fatalf("backups: %v (%d)", err, len(backups))
	}
	target := backups[len(backups)-1].Path
	if err := os.Chmod(target, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(target, 0o600) //nolint:errcheck // best-effort restore for cleanup

	if _, err := Rollback(options(root), target); err == nil {
		t.Fatal("rollback from an unreadable backup unexpectedly succeeded")
	}
	if got := readFile(t, targetPath(root)); got != before {
		t.Fatalf("failed rollback changed the target: %q -> %q", before, got)
	}
	assertNoTempFiles(t, filepath.Dir(targetPath(root)))
}

// TestUninstallKeepsForeignOverrideBytes: if the installed file was replaced by
// something else after installation, uninstall still backs it up before removing
// it, so the foreign bytes are recoverable.
func TestUninstallKeepsForeignOverrideBytes(t *testing.T) {
	root := t.TempDir()
	src := t.TempDir()
	v1 := writeSource(t, src, "v1.cpk", "cpk-v1")
	if _, err := Install(v1, options(root)); err != nil {
		t.Fatalf("initial install: %v", err)
	}
	// Simulate a user (or another mod) overwriting the file behind our back.
	foreign := "foreign-bytes"
	if err := os.WriteFile(targetPath(root), []byte(foreign), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Uninstall(options(root))
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if res.BackupPath == "" {
		t.Fatal("uninstall of a foreign file did not create a backup")
	}
	if got := readFile(t, res.BackupPath); got != foreign {
		t.Fatalf("backup holds %q, want the foreign bytes %q", got, foreign)
	}
	if _, err := os.Stat(targetPath(root)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("uninstall left the target in place")
	}
}

// TestInvalidSourceLeavesNoSideEffects: a rejected source must stop before the
// root is created, before the lock is taken and before any file is written.
func TestInvalidSourceLeavesNoSideEffects(t *testing.T) {
	root := t.TempDir()
	src := t.TempDir()
	bad := writeSource(t, src, "bad.cpk", "not a real archive")
	opts := options(root)
	opts.Validate = func(string) (*SourceInfo, error) {
		return nil, errors.New("rejected by the test source gate")
	}
	if _, err := Install(bad, opts); err == nil {
		t.Fatal("install with a rejected source unexpectedly succeeded")
	}
	if _, err := os.Stat(filepath.Join(root, LockName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a rejected install left a lock file behind: %v", err)
	}
	if _, err := os.Stat(targetPath(root)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a rejected install wrote a target: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, BackupDirName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a rejected install created the backup directory: %v", err)
	}
	if entries, err := os.ReadDir(root); err == nil && len(entries) != 0 {
		t.Fatalf("a rejected install left %d entries in the root", len(entries))
	}
}
