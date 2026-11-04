package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeRoot creates a minimal checkout root: go.mod plus the translation marker.
func makeRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, GoModFile), []byte("module example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "translations")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(TranslationsMarker)), []byte("record_id\tenglish_text\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestFindRootFromWalksUp(t *testing.T) {
	root := makeRoot(t)
	deep := filepath.Join(root, "internal", "some", "package")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	got, ok := FindRootFrom(deep)
	if !ok {
		t.Fatal("root not found from a nested directory")
	}
	if got != root {
		t.Fatalf("root = %q, want %q", got, root)
	}
}

func TestFindRootFromRequiresBothMarkers(t *testing.T) {
	// go.mod alone is not enough: every Go module would qualify.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, GoModFile), []byte("module example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := FindRootFrom(dir); ok {
		t.Fatal("a directory with only go.mod must not be a root")
	}
}

func TestFindRootHonoursEnvOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(RootEnv, dir)
	got, err := FindRoot()
	if err != nil {
		t.Fatalf("FindRoot: %v", err)
	}
	if got != dir {
		t.Fatalf("FindRoot = %q, want %q", got, dir)
	}
}

// TestFindRootHonoursTheLegacyEnvNames: the earlier UNBURGER_ROOT and the oldest
// UO_FIDELITY_ROOT are still honoured when the current variable is unset, so an
// older shell environment keeps working. The priority is the current name first.
func TestFindRootHonoursTheLegacyEnvNames(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(RootEnv, "")
	t.Setenv(LegacyRootEnv, dir)
	if got, err := FindRoot(); err != nil || got != dir {
		t.Fatalf("FindRoot with %s = (%q, %v), want %q", LegacyRootEnv, got, err, dir)
	}

	oldest := t.TempDir()
	t.Setenv(LegacyRootEnv, "")
	t.Setenv(Legacy2RootEnv, oldest)
	if got, err := FindRoot(); err != nil || got != oldest {
		t.Fatalf("FindRoot with %s = (%q, %v), want %q", Legacy2RootEnv, got, err, oldest)
	}

	// The current variable wins over both legacy names.
	t.Setenv(LegacyRootEnv, dir)
	if got, err := FindRoot(); err != nil || got != dir {
		t.Fatalf("FindRoot must prefer %s = (%q, %v), want %q", RootEnv, got, err, dir)
	}
}

// TestConfigFileNames: the single source of truth is uo-unburger.json, and the
// two former project-local names are addressable for migration.
func TestConfigFileNames(t *testing.T) {
	root := "/opt/checkout"
	if got, want := ConfigFile(root), filepath.Join(root, ".local", "uo-unburger.json"); got != want {
		t.Fatalf("ConfigFile = %q, want %q", got, want)
	}
	if got, want := LegacyConfigFile(root), filepath.Join(root, ".local", "unburger.json"); got != want {
		t.Fatalf("LegacyConfigFile = %q, want %q", got, want)
	}
	if got, want := Legacy2ConfigFile(root), filepath.Join(root, ".local", "uo-fidelity.json"); got != want {
		t.Fatalf("Legacy2ConfigFile = %q, want %q", got, want)
	}
}

func TestLayoutPaths(t *testing.T) {
	root := "/opt/checkout"
	l := NewLayout(root)
	cases := map[string]string{
		l.LocalDir:   filepath.Join(root, ".local"),
		l.ConfigFile: filepath.Join(root, ".local", ConfigFileName),
		l.CacheDir:   filepath.Join(root, ".local", "cache"),
		l.WorkDir:    filepath.Join(root, ".local", "work"),
		l.BuildsDir:  filepath.Join(root, ".local", "work", "builds"),
		l.ExtractDir: filepath.Join(root, ".local", "work", "extract"),
		l.ReleaseDir: filepath.Join(root, ".local", "release"),
		l.RomBase:    filepath.Join(root, ".local", "roms", "base"),
		l.RomUpdate:  filepath.Join(root, ".local", "roms", "update"),
		l.RomDLC:     filepath.Join(root, ".local", "roms", "dlc"),
		l.KeysDir:    filepath.Join(root, ".local", "keys"),
	}
	for got, want := range cases {
		if got != want {
			t.Fatalf("path = %q, want %q", got, want)
		}
	}
}

func TestEnsureCreatesLayoutWithoutDeleting(t *testing.T) {
	root := t.TempDir()
	l := NewLayout(root)
	// A pre-existing file inside .local must survive.
	if err := os.MkdirAll(l.LocalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(l.LocalDir, "keep.txt")
	if err := os.WriteFile(keep, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	created, err := l.Ensure()
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if len(created) == 0 {
		t.Fatal("expected the layout directories to be created")
	}
	for _, dir := range l.Dir() {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Fatalf("directory %s missing after Ensure (%v)", dir, err)
		}
	}
	if data, err := os.ReadFile(keep); err != nil || string(data) != "keep" {
		t.Fatalf("Ensure must not delete existing content: %v", err)
	}
	if missing := l.Missing(); len(missing) != 0 {
		t.Fatalf("Missing() = %v, want none", missing)
	}
}

func TestEnsureRejectsFileWhereDirectoryExpected(t *testing.T) {
	root := t.TempDir()
	l := NewLayout(root)
	if err := os.MkdirAll(filepath.Dir(l.CacheDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.CacheDir, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Ensure(); err == nil {
		t.Fatal("Ensure must refuse a file where a directory is expected")
	}
}

func TestNoAbsoluteUserPathInSource(t *testing.T) {
	data, err := os.ReadFile("project.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"/home/", "/Users/", "/mnt/", "example-user"} {
		if strings.Contains(string(data), needle) {
			t.Fatalf("project.go must not contain %q", needle)
		}
	}
}
