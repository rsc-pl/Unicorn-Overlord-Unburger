package appconfig

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultUsesNoMachineSpecificPath(t *testing.T) {
	c := Default()
	for name, val := range map[string]string{
		"SourceCPK": c.SourceCPK, "Corpus": c.Corpus, "TranslationsDir": c.TranslationsDir,
		"OutDir": c.OutDir,
	} {
		if filepath.IsAbs(val) {
			t.Fatalf("%s default %q must not be an absolute path", name, val)
		}
	}
	if strings.HasPrefix(c.TranslationsDir, "~") || strings.HasPrefix(c.OutDir, "~") {
		t.Fatalf("defaults must not embed a home directory: %+v", c)
	}
	if c.TitleID != "010069401ADB8000" || c.Variant != "both" || c.Theme != ThemeAuto {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

// TestSourceHasNoHardcodedUserPath guards the rule that no operator-specific
// location may be compiled into the package.
func TestSourceHasNoHardcodedUserPath(t *testing.T) {
	data, err := os.ReadFile("appconfig.go")
	if err != nil {
		t.Fatalf("reading source: %v", err)
	}
	src := string(data)
	for _, needle := range []string{"/home/", "/Users/", "/mnt/", "example-user"} {
		if strings.Contains(src, needle) {
			t.Fatalf("appconfig.go must not contain %q", needle)
		}
	}
}

func TestPathIsProjectLocalWhenRootFound(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "translations"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "translations", "honorifics.tsv"), []byte("record_id\tenglish_text\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("UO_UNBURGER_ROOT", root)
	want := filepath.Join(root, ".local", FileName)
	if got := Path(); got != want {
		t.Fatalf("Path() = %q, want %q", got, want)
	}
	if got := Dir(); got != filepath.Join(root, ".local") {
		t.Fatalf("Dir() = %q", got)
	}
}

func TestLegacyDirHonoursXDGConfigHome(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	want := filepath.Join(xdg, AppDirName)
	if got := LegacyDir(); got != want {
		t.Fatalf("LegacyDir() = %q, want %q", got, want)
	}
	if got := LegacyPath(); got != filepath.Join(want, legacyFileName) {
		t.Fatalf("LegacyPath() = %q", got)
	}
}

func TestLegacyDirFallsBackToHomeConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", home)
	want := filepath.Join(home, ".config", AppDirName)
	if got := LegacyDir(); got != want {
		t.Fatalf("LegacyDir() = %q, want %q", got, want)
	}
}

func TestLegacyDirIgnoresRelativeXDGConfigHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "relative/path")
	t.Setenv("HOME", home)
	if got := LegacyDir(); got != filepath.Join(home, ".config", AppDirName) {
		t.Fatalf("a relative XDG_CONFIG_HOME must be ignored, got %q", got)
	}
}

// TestLoadImportsLegacyInMemory: a configuration written by an earlier version is
// honoured when no project-local file exists, without creating a second file.
func TestLoadImportsLegacyInMemory(t *testing.T) {
	root := t.TempDir()
	xdg := t.TempDir()
	t.Setenv("UO_UNBURGER_ROOT", root)
	t.Setenv("XDG_CONFIG_HOME", xdg)

	legacy := filepath.Join(xdg, AppDirName, legacyFileName)
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"source_cpk": "/games/Unicorn_US.CPK", "corpus": "/games/corpus.jsonl", "out_dir": ".local/work/builds", "title_id": "010069401ADB8800", "variant": "no-honorifics", "theme": "light", "mouse": true}` + "\n"
	if err := os.WriteFile(legacy, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, path, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if path != filepath.Join(root, ".local", FileName) {
		t.Fatalf("Load path = %q", path)
	}
	if cfg.SourceCPK != "/games/Unicorn_US.CPK" || cfg.TitleID != "010069401ADB8800" || !cfg.Mouse || cfg.Theme != ThemeLight {
		t.Fatalf("legacy values were not imported: %+v", cfg)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Load must not create the project-local file (stat err = %v)", err)
	}
}

// TestMigrateLegacyWritesProjectLocalAndKeepsLegacy: migration writes the
// project-local file, keeps the former one, and the project-local file then wins.
func TestMigrateLegacyWritesProjectLocalAndKeepsLegacy(t *testing.T) {
	root := t.TempDir()
	xdg := t.TempDir()
	t.Setenv("UO_UNBURGER_ROOT", root)
	t.Setenv("XDG_CONFIG_HOME", xdg)

	legacy := filepath.Join(xdg, AppDirName, legacyFileName)
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte(`{"source_cpk": "/games/old.cpk"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	migrated, local, err := MigrateLegacy()
	if err != nil {
		t.Fatalf("MigrateLegacy: %v", err)
	}
	if !migrated {
		t.Fatal("expected a migration")
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("the former configuration must be kept: %v", err)
	}
	cfg, err := LoadFrom(local)
	if err != nil {
		t.Fatalf("LoadFrom(local): %v", err)
	}
	if cfg.SourceCPK != "/games/old.cpk" {
		t.Fatalf("migrated source = %q", cfg.SourceCPK)
	}
	// A second call is a no-op, and the project-local file now wins.
	again, _, err := MigrateLegacy()
	if err != nil || again {
		t.Fatalf("second MigrateLegacy = (%v, %v), want (false, nil)", again, err)
	}
	if err := (Config{SourceCPK: "/games/new.cpk"}).Save(local); err != nil {
		t.Fatal(err)
	}
	got, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.SourceCPK != "/games/new.cpk" {
		t.Fatalf("project-local file must win, got %q", got.SourceCPK)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", FileName)
	cfg := Config{
		SourceCPK:       filepath.Join(dir, "Unicorn_US.CPK"),
		Corpus:          filepath.Join(dir, "corpus.jsonl"),
		TranslationsDir: "translations",
		OutDir:          filepath.Join(dir, "out"),
		TitleID:         "010069401ADB8800",
		Variant:         "no-honorifics",
		Theme:           ThemeLight,
	}
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if got != cfg {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, cfg)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("config permission = %o, want 600", perm)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("Save must leave exactly one file, found %d", len(entries))
	}
}

func TestLoadMissingFileReturnsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("a missing config must not be an error: %v", err)
	}
	if cfg != Default() {
		t.Fatalf("expected defaults, got %+v", cfg)
	}
	loaded, returned, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if returned == "" {
		t.Fatalf("Load must report the config path")
	}
	_ = loaded
}

func TestLoadEmptyFileReturnsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte("\n"), 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}
	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if cfg != Default() {
		t.Fatalf("expected defaults, got %+v", cfg)
	}
}

func TestLoadRejectsMalformedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("writing: %v", err)
	}
	if _, err := LoadFrom(path); err == nil {
		t.Fatalf("malformed JSON must be an error")
	}
}

func TestNormalizeFillsDefaultsAndRejectsInvalidValues(t *testing.T) {
	cfg := Config{Theme: "blue", Variant: "bogus", TitleID: "0100BAD", TranslationsDir: "  "}
	cfg.Normalize()
	def := Default()
	if cfg.Theme != def.Theme {
		t.Fatalf("invalid theme accepted: %q", cfg.Theme)
	}
	if cfg.Variant != def.Variant {
		t.Fatalf("invalid variant accepted: %q", cfg.Variant)
	}
	if cfg.TitleID != def.TitleID {
		t.Fatalf("unsupported title ID accepted: %q", cfg.TitleID)
	}
	if cfg.TranslationsDir != def.TranslationsDir || cfg.OutDir != def.OutDir {
		t.Fatalf("blank directories not defaulted: %+v", cfg)
	}
}

func TestNormalizeTrimsPastedPathsAndDropsControls(t *testing.T) {
	cfg := Default()
	cfg.SourceCPK = "/path/to/Unicorn_US.CPK\n"
	cfg.Corpus = "\t/path/to/corpus.jsonl\x00"
	cfg.Normalize()
	if cfg.SourceCPK != "/path/to/Unicorn_US.CPK" {
		t.Fatalf("trailing newline not trimmed: %q", cfg.SourceCPK)
	}
	if cfg.Corpus != "/path/to/corpus.jsonl" {
		t.Fatalf("control character not dropped: %q", cfg.Corpus)
	}
}

func TestSaveWithoutPathFails(t *testing.T) {
	if err := Default().Save(""); err == nil {
		t.Fatalf("Save with an empty path must fail")
	}
}

// TestMigrateLegacyImportsTheFormerProjectLocalFile: a configuration written by an
// earlier version at .local/uo-fidelity.json is imported into .local/uo-unburger.json;
// the former file is kept and the new file then wins.
func TestMigrateLegacyImportsTheFormerProjectLocalFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "translations"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "translations", "honorifics.tsv"), []byte("record_id\tenglish_text\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("UO_UNBURGER_ROOT", root)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	legacy := filepath.Join(root, ".local", "uo-fidelity.json")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte(`{"source_cpk": "/games/old.cpk"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Load imports the former file in memory without writing.
	cfg, path, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SourceCPK != "/games/old.cpk" {
		t.Fatalf("former project-local config not imported: %+v", cfg)
	}
	if path != filepath.Join(root, ".local", FileName) {
		t.Fatalf("path = %q", path)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Load must not create the new file (stat err = %v)", err)
	}
	// MigrateLegacy writes the new file and keeps the former one.
	migrated, local, err := MigrateLegacy()
	if err != nil || !migrated {
		t.Fatalf("MigrateLegacy = (%v, %q, %v)", migrated, local, err)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("the former file must be kept: %v", err)
	}
	if got, _, err := Load(); err != nil || got.SourceCPK != "/games/old.cpk" {
		t.Fatalf("new file must win and carry the value: %+v, %v", got, err)
	}
}

// TestLegacyXDGDirectoryHonoursTheFormerName: a file under the older
// $XDG_CONFIG_HOME/uo-fidelity directory is still read as a fallback.
func TestLegacyXDGDirectoryHonoursTheFormerName(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "translations"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "translations", "honorifics.tsv"), []byte("record_id\tenglish_text\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("UO_UNBURGER_ROOT", root)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", home)

	old := filepath.Join(home, ".config", "uo-fidelity", legacyFileName)
	if err := os.MkdirAll(filepath.Dir(old), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, []byte(`{"source_cpk": "/games/xdg-old.cpk"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SourceCPK != "/games/xdg-old.cpk" {
		t.Fatalf("the older XDG directory name must still be read: %+v", cfg)
	}
}

// makeConfigRoot creates a minimal checkout root for a configuration test.
func makeConfigRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "translations"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "translations", "honorifics.tsv"), []byte("record_id\tenglish_text\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestConfigChainPrefersTheEarlierRename: when both former project-local files
// exist, .local/unburger.json (the earlier rename) is read before
// .local/uo-fidelity.json (the oldest), and both are migrated into the new file
// without being deleted.
func TestConfigChainPrefersTheEarlierRename(t *testing.T) {
	root := makeConfigRoot(t)
	t.Setenv("UO_UNBURGER_ROOT", root)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	local := filepath.Join(root, ".local")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	intermediate := filepath.Join(local, "unburger.json")
	oldest := filepath.Join(local, "uo-fidelity.json")
	if err := os.WriteFile(oldest, []byte(`{"source_cpk": "/games/oldest.cpk"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(intermediate, []byte(`{"source_cpk": "/games/intermediate.cpk"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, path, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SourceCPK != "/games/intermediate.cpk" {
		t.Fatalf("the earlier rename must win over the oldest file: %+v", cfg)
	}
	if path != filepath.Join(local, "uo-unburger.json") {
		t.Fatalf("path = %q", path)
	}

	migrated, newPath, err := MigrateLegacy()
	if err != nil || !migrated {
		t.Fatalf("MigrateLegacy = (%v, %q, %v)", migrated, newPath, err)
	}
	if _, err := os.Stat(intermediate); err != nil {
		t.Fatalf("the earlier file must be kept: %v", err)
	}
	if _, err := os.Stat(oldest); err != nil {
		t.Fatalf("the oldest file must be kept: %v", err)
	}
	if got, _, err := Load(); err != nil || got.SourceCPK != "/games/intermediate.cpk" {
		t.Fatalf("the new file must win and carry the value: %+v, %v", got, err)
	}
}

// TestMigrateLegacyNeverOverwritesAPopulatedNewFile: when the single source of
// truth already exists it is never replaced by an older location, so a populated
// configuration can never be overwritten with defaults or stale values.
func TestMigrateLegacyNeverOverwritesAPopulatedNewFile(t *testing.T) {
	root := makeConfigRoot(t)
	t.Setenv("UO_UNBURGER_ROOT", root)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	local := filepath.Join(root, ".local")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	current := filepath.Join(local, "uo-unburger.json")
	if err := os.WriteFile(current, []byte(`{"source_cpk": "/games/current.cpk"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "unburger.json"), []byte(`{"source_cpk": "/games/stale.cpk"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	migrated, path, err := MigrateLegacy()
	if err != nil || migrated {
		t.Fatalf("MigrateLegacy must not rewrite an existing file: (%v, %q, %v)", migrated, path, err)
	}
	data, err := os.ReadFile(current)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "/games/current.cpk") {
		t.Fatalf("the populated file was overwritten: %s", data)
	}
	if cfg, _, err := Load(); err != nil || cfg.SourceCPK != "/games/current.cpk" {
		t.Fatalf("Load must keep the current value: %+v, %v", cfg, err)
	}
}

// TestLegacyXDGChainPrefersTheEarlierRename: the intermediate machine-global
// directory ($XDG_CONFIG_HOME/unburger) is read before the oldest
// ($XDG_CONFIG_HOME/uo-fidelity).
func TestLegacyXDGChainPrefersTheEarlierRename(t *testing.T) {
	root := makeConfigRoot(t)
	xdg := t.TempDir()
	t.Setenv("UO_UNBURGER_ROOT", root)
	t.Setenv("XDG_CONFIG_HOME", xdg)

	intermediate := filepath.Join(xdg, "unburger", legacyFileName)
	oldest := filepath.Join(xdg, "uo-fidelity", legacyFileName)
	for path, body := range map[string]string{
		intermediate: `{"source_cpk": "/games/intermediate.cpk"}` + "\n",
		oldest:       `{"source_cpk": "/games/oldest.cpk"}` + "\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg, _, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SourceCPK != "/games/intermediate.cpk" {
		t.Fatalf("the intermediate XDG directory must win over the oldest: %+v", cfg)
	}
}
