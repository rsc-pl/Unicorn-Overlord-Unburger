package installer

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

// TestHeldLockBlocksEveryMutatingOperation: a fresh lock owned by another run
// stops install, uninstall and rollback alike, and leaves the installed file
// untouched with no temporary files behind.
func TestHeldLockBlocksEveryMutatingOperation(t *testing.T) {
	root := t.TempDir()
	srcDir := t.TempDir()
	first := writeSource(t, srcDir, "a.cpk", "cpk-A")
	second := writeSource(t, srcDir, "b.cpk", "cpk-B")
	if _, err := Install(first, options(root)); err != nil {
		t.Fatalf("initial install: %v", err)
	}
	before := readFile(t, targetPath(root))

	lock := filepath.Join(root, LockName)
	if err := os.WriteFile(lock, []byte("pid=4242\n"), 0o600); err != nil {
		t.Fatalf("writing lock: %v", err)
	}

	if _, err := Install(second, options(root)); !errors.Is(err, ErrLocked) {
		t.Fatalf("install with a held lock = %v, want ErrLocked", err)
	}
	if _, err := Uninstall(options(root)); !errors.Is(err, ErrLocked) {
		t.Fatalf("uninstall with a held lock = %v, want ErrLocked", err)
	}
	if _, err := Rollback(options(root), ""); !errors.Is(err, ErrLocked) {
		t.Fatalf("rollback with a held lock = %v, want ErrLocked", err)
	}
	if got := readFile(t, targetPath(root)); got != before {
		t.Fatalf("target changed while locked: %q -> %q", before, got)
	}
	assertNoTempFiles(t, filepath.Join(root, "contents", testTitleID, "romfs"))

	// Releasing the lock lets the next operation succeed.
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if _, err := Uninstall(options(root)); err != nil {
		t.Fatalf("uninstall after releasing the lock: %v", err)
	}
}

// TestConcurrentInstallsNeverInterleaveBytes: with a foreign lock in place every
// concurrent install must fail with ErrLocked; once the lock is gone the calls
// serialise (some may still observe contention) and the installed bytes must
// equal exactly one of the offered sources, never a mixture.
func TestConcurrentInstallsNeverInterleaveBytes(t *testing.T) {
	root := t.TempDir()
	srcDir := t.TempDir()
	const n = 6
	sources := make([]string, n)
	for i := 0; i < n; i++ {
		sources[i] = writeSource(t, srcDir, string(rune('a'+i))+".cpk", strings.Repeat(string(rune('A'+i)), 256))
	}

	// The barrier makes every goroutine reach the validation step before any of
	// them can try to acquire the lock, so the contention is deliberate.
	blockingValidator := func(gate *sync.WaitGroup, entered chan<- struct{}) Validator {
		return func(path string) (*SourceInfo, error) {
			entered <- struct{}{}
			gate.Wait()
			return testValidator(path)
		}
	}
	runAll := func(gate *sync.WaitGroup, entered chan struct{}) []error {
		results := make([]error, n)
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				opts := options(root)
				opts.Validate = blockingValidator(gate, entered)
				_, err := Install(sources[i], opts)
				results[i] = err
			}(i)
		}
		for i := 0; i < n; i++ {
			<-entered
		}
		gate.Done()
		wg.Wait()
		return results
	}

	// Phase 1: a lock owned by "another run" blocks every caller.
	lock := filepath.Join(root, LockName)
	if err := os.WriteFile(lock, []byte("pid=777\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gate := &sync.WaitGroup{}
	gate.Add(1)
	phase1 := runAll(gate, make(chan struct{}, n))
	for i, err := range phase1 {
		if !errors.Is(err, ErrLocked) {
			t.Fatalf("goroutine %d with a held lock = %v, want ErrLocked", i, err)
		}
	}
	if _, err := os.Stat(targetPath(root)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a blocked install wrote the override: %v", err)
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}

	// Phase 2: no foreign lock; calls serialise and the result is one source.
	gate2 := &sync.WaitGroup{}
	gate2.Add(1)
	phase2 := runAll(gate2, make(chan struct{}, n))
	ok, locked := 0, 0
	for i, err := range phase2 {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrLocked):
			locked++
		default:
			t.Fatalf("goroutine %d: unexpected error %v", i, err)
		}
	}
	if ok == 0 {
		t.Fatalf("no install succeeded (locked=%d)", locked)
	}
	got := readFile(t, targetPath(root))
	for _, src := range sources {
		if data, _ := os.ReadFile(src); string(data) == got {
			return
		}
	}
	t.Fatal("installed bytes do not match any offered source")
}

// TestUnwritableRootFailsCleanly: a read-only destination root makes install
// fail without creating the override or any temporary file.
func TestUnwritableRootFailsCleanly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permissions do not deny writes")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(root, 0o755)
	src := writeSource(t, t.TempDir(), "a.cpk", "cpk-A")

	if _, err := Install(src, options(root)); err == nil {
		t.Fatal("an install into a read-only root must fail")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") || e.Name() == "contents" {
			t.Fatalf("failed install left %q behind", e.Name())
		}
	}
}

// TestDirectoryAtTargetFailsCleanly: if the override path is occupied by a
// directory, install and uninstall both fail cleanly and the directory survives.
func TestDirectoryAtTargetFailsCleanly(t *testing.T) {
	root := t.TempDir()
	dirTarget := targetPath(root)
	if err := os.MkdirAll(dirTarget, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirTarget, "keep"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := writeSource(t, t.TempDir(), "a.cpk", "cpk-A")

	if _, err := Install(src, options(root)); err == nil {
		t.Fatal("installing over a directory must fail")
	}
	if _, err := Uninstall(options(root)); err == nil {
		t.Fatal("uninstalling a directory target must fail")
	}
	if data := readFile(t, filepath.Join(dirTarget, "keep")); data != "keep" {
		t.Fatalf("directory contents changed: %q", data)
	}
	assertNoTempFiles(t, filepath.Join(root, "contents", testTitleID))
}

// TestRollbackFromDirectoryBackupFailsCleanly: a backup path that is a
// directory is refused without disturbing the installed file.
func TestRollbackFromDirectoryBackupFailsCleanly(t *testing.T) {
	root := t.TempDir()
	src := writeSource(t, t.TempDir(), "a.cpk", "cpk-A")
	if _, err := Install(src, options(root)); err != nil {
		t.Fatalf("install: %v", err)
	}
	before := readFile(t, targetPath(root))

	bogus := filepath.Join(root, BackupDirName, testTitleID, "not-a-file.cpk")
	if err := os.MkdirAll(bogus, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Rollback(options(root), bogus); err == nil {
		t.Fatal("a directory backup path must be refused")
	}
	if got := readFile(t, targetPath(root)); got != before {
		t.Fatalf("failed rollback disturbed the target: %q", got)
	}
	assertNoTempFiles(t, filepath.Join(root, "contents", testTitleID, "romfs"))
}

// TestFullLifecyclePreservesNeighbours walks install -> update -> reinstall ->
// uninstall, keeping unrelated files in the same romfs directory, in the title
// contents directory and in a sibling title directory alive throughout.
func TestFullLifecyclePreservesNeighbours(t *testing.T) {
	root := t.TempDir()
	srcDir := t.TempDir()
	v1 := writeSource(t, srcDir, "v1.cpk", "cpk-version-1")
	v2 := writeSource(t, srcDir, "v2.cpk", "cpk-version-2")

	contents := filepath.Join(root, "contents", testTitleID)
	romfs := filepath.Join(contents, "romfs")
	sibling := filepath.Join(root, "contents", "010069401ADB8800")
	for _, p := range []string{romfs, sibling} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	neighbours := map[string]string{
		filepath.Join(romfs, "unrelated.bin"): "romfs neighbour",
		filepath.Join(contents, "extra.json"): "contents neighbour",
		filepath.Join(sibling, "other.txt"):   "sibling title",
	}
	for p, v := range neighbours {
		if err := os.WriteFile(p, []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	checkNeighbours := func(stage string) {
		t.Helper()
		for p, want := range neighbours {
			if got := readFile(t, p); got != want {
				t.Fatalf("%s: neighbour %s = %q, want %q", stage, filepath.Base(p), got, want)
			}
		}
	}

	if _, err := Install(v1, options(root)); err != nil {
		t.Fatalf("install v1: %v", err)
	}
	if got := readFile(t, targetPath(root)); got != "cpk-version-1" {
		t.Fatalf("v1 bytes = %q", got)
	}
	checkNeighbours("after install")

	// Update: metadata matches the installed file, so the preview says update.
	p, err := PreviewInstall(v2, options(root))
	if err != nil {
		t.Fatalf("preview update: %v", err)
	}
	if p.Action != "update" {
		t.Fatalf("preview action = %q, want update", p.Action)
	}
	if _, err := Install(v2, options(root)); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got := readFile(t, targetPath(root)); got != "cpk-version-2" {
		t.Fatalf("v2 bytes = %q", got)
	}
	checkNeighbours("after update")

	// Reinstall: metadata removed, file replaced by hand -> action reinstall.
	if err := os.Remove(filepath.Join(contents, MetaName)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(targetPath(root), []byte("hand-edited"), 0o600); err != nil {
		t.Fatal(err)
	}
	rp, err := PreviewInstall(v1, options(root))
	if err != nil {
		t.Fatalf("preview reinstall: %v", err)
	}
	if rp.Action != "reinstall" {
		t.Fatalf("preview action = %q, want reinstall", rp.Action)
	}
	if _, err := Install(v1, options(root)); err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	if got := readFile(t, targetPath(root)); got != "cpk-version-1" {
		t.Fatalf("reinstalled bytes = %q", got)
	}
	checkNeighbours("after reinstall")

	backups, err := Backups(options(root))
	if err != nil {
		t.Fatalf("backups: %v", err)
	}
	if len(backups) != 2 {
		t.Fatalf("backups = %d, want 2 (update + reinstall)", len(backups))
	}
	names := make([]string, len(backups))
	for i, b := range backups {
		names[i] = filepath.Base(b.Path)
	}
	sort.Strings(names)
	if len(names) != 2 || names[0] == names[1] {
		t.Fatalf("backups must have unique names: %v", names)
	}

	if _, err := Uninstall(options(root)); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, err := os.Stat(targetPath(root)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("override still present after uninstall: %v", err)
	}
	checkNeighbours("after uninstall")
}

// assertNoTempFiles fails if any staged temporary file is left in dir.
func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		t.Fatalf("readdir %s: %v", dir, err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Fatalf("temporary file left behind: %s", filepath.Join(dir, e.Name()))
		}
	}
}
