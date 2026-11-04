// Package appconfig stores the toolkit's private user configuration.
//
// The configuration is project-local: it lives inside the checkout's private
// .local/ tree, so everything the toolkit remembers about the operator's machine
// stays with the checkout and nothing about it ends up in the public source:
//
//	<root>/.local/uo-unburger.json
//
// The former locations are still read once, so an existing setup keeps working;
// they are never deleted, and the project-local file becomes the single source of
// truth as soon as it exists (MigrateLegacy imports it).
//
// Every remembered location is a path the user chose; the package hard-codes no
// home directory, no game directory and no emulator path. Defaults are the
// repository-relative working directories a checkout can use unchanged, plus the
// public title ID this project targets.
package appconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"uo-unburger/internal/build"
	"uo-unburger/internal/project"
)

// AppDirName is the per-application directory under the user config root. The
// project-local file is the single source of truth; this name is only used for
// the machine-global fallback location, which is read once.
const AppDirName = "uo-unburger"

// legacyAppDirName and legacyAppDirName2 are the former per-application
// directories under the user config root. They are retained only to read a
// configuration written by an earlier version, in priority order (the earlier
// `unburger` rename first, then the oldest `uo-fidelity`).
const legacyAppDirName = "unburger"
const legacyAppDirName2 = "uo-fidelity"

// FileName is the project-local configuration file name.
const FileName = project.ConfigFileName

// legacyFileName is the former configuration file name.
const legacyFileName = "config.json"

// Theme values accepted by the TUI.
const (
	ThemeAuto  = "auto"
	ThemeLight = "light"
	ThemeDark  = "dark"
)

// Config is the persisted, machine-local configuration. All fields are optional
// in the file; Normalize fills the safe defaults so a hand-edited or partial
// file still produces a usable configuration.
type Config struct {
	// SourceCPK is the operator's extracted localization archive
	// (Unicorn_US.CPK). It is read-only input and is never copied into the
	// repository by this package.
	SourceCPK string `json:"source_cpk"`
	// Corpus is the reference JP/EN corpus JSONL the importer expects.
	Corpus string `json:"corpus"`
	// TranslationsDir is the directory holding the translation files
	// (honorifics.tsv and no-honorifics.tsv).
	TranslationsDir string `json:"translations"`
	// OutDir is the build output root.
	OutDir string `json:"out_dir"`
	// TitleID is the expected Nintendo Switch title ID.
	TitleID string `json:"title_id"`
	// Variant is the default build edition selection.
	Variant string `json:"variant"`
	// Theme is one of auto|light|dark.
	Theme string `json:"theme"`
	// Mouse enables terminal mouse reporting (wheel scrolling and click
	// selection) in the interactive interface. It defaults to false because mouse
	// reporting takes over the terminal's own mouse handling and can interfere
	// with selecting and copying text; it is also always disabled when NO_COLOR
	// or TERM=dumb is set.
	Mouse bool `json:"mouse"`
	// Extractor is an explicit path to the external container extractor to use
	// before any PATH lookup. Empty means "look on PATH". Extraction itself
	// always requires the explicit --extract flag or the first-run setup; this
	// field only selects which tool runs.
	Extractor string `json:"extractor"`
	// SetupComplete records that the first-run setup flow has been shown and
	// finished or skipped. It suppresses the guided flow on later launches.
	SetupComplete bool `json:"setup_complete"`
	// The Source* fields record the verified identity of the resolved source
	// archive. They are written when discovery finds a trusted archive (cache,
	// workspace or extraction) so later runs recognise it without re-extraction
	// and never mistake a translated build output for a source.
	SourceSHA256           string `json:"source_sha256,omitempty"`
	SourceSize             int64  `json:"source_size,omitempty"`
	SourceMembers          int    `json:"source_members,omitempty"`
	SourceFMSMembers       int    `json:"source_fms_members,omitempty"`
	SourceMemberListSHA256 string `json:"source_member_list_sha256,omitempty"`
	SourceProvenance       string `json:"source_provenance,omitempty"`
}

// Default returns the configuration used when no file exists yet.
func Default() Config {
	return Config{
		TranslationsDir: "translations",
		OutDir:          filepath.Join(".local", "work", "builds"),
		TitleID:         build.DefaultTitleID,
		Variant:         string(build.VariantBoth),
		Theme:           ThemeAuto,
	}
}

// Root resolves the checkout root this configuration belongs to. It returns an
// error when no checkout can be located.
func Root() (string, error) { return project.FindRoot() }

// Dir resolves the project-local configuration directory (<root>/.local). When no
// checkout can be located it falls back to the former machine-global directory.
func Dir() string {
	if root, err := project.FindRoot(); err == nil {
		return project.LocalDir(root)
	}
	return LegacyDir()
}

// Path resolves the project-local configuration file path. When no checkout can be
// located it falls back to the former machine-global path.
func Path() string {
	if root, err := project.FindRoot(); err == nil {
		return project.ConfigFile(root)
	}
	return LegacyPath()
}

// LegacyDir is the machine-global configuration directory
// ($XDG_CONFIG_HOME/uo-unburger, or the platform config directory). It is only
// consulted when no project-local file exists yet.
func LegacyDir() string {
	return configDirFor(AppDirName)
}

// legacyDir is the earlier machine-global directory
// ($XDG_CONFIG_HOME/unburger, or the platform config directory). It is read once
// during migration only.
func legacyDir() string {
	return configDirFor(legacyAppDirName)
}

// legacyDir2 is the oldest machine-global directory
// ($XDG_CONFIG_HOME/uo-fidelity, or the platform config directory). It is read
// once during migration, after legacyDir, and only when the newer locations do
// not exist.
func legacyDir2() string {
	return configDirFor(legacyAppDirName2)
}

func configDirFor(name string) string {
	if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" && filepath.IsAbs(xdg) {
		return filepath.Join(filepath.Clean(xdg), name)
	}
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		return filepath.Join(dir, name)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".config", name)
	}
	return ""
}

// LegacyPath is the former machine-global configuration file path.
func LegacyPath() string {
	if dir := LegacyDir(); dir != "" {
		return filepath.Join(dir, legacyFileName)
	}
	return ""
}

// legacyPaths returns the configuration locations that may hold a file written
// by an earlier version, in priority order: the earlier project-local file
// (.local/unburger.json), the oldest project-local file (.local/uo-fidelity.json),
// the earlier machine-global directory ($XDG_CONFIG_HOME/unburger), the oldest
// machine-global directory ($XDG_CONFIG_HOME/uo-fidelity), and finally the
// current machine-global name ($XDG_CONFIG_HOME/uo-unburger). The current
// project-local path is excluded.
func legacyPaths(current string) []string {
	var out []string
	if root, err := project.FindRoot(); err == nil {
		for _, p := range []string{project.LegacyConfigFile(root), project.Legacy2ConfigFile(root)} {
			if p != "" && p != current {
				out = append(out, p)
			}
		}
	}
	for _, dir := range []string{legacyDir(), legacyDir2(), LegacyDir()} {
		if dir == "" {
			continue
		}
		if p := filepath.Join(dir, legacyFileName); p != current {
			out = append(out, p)
		}
	}
	return out
}

// firstLegacy returns the first former configuration path that exists, or "".
func firstLegacy(current string) string {
	for _, p := range legacyPaths(current) {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// Load reads the configuration from the project-local location. A missing file is
// not an error: when a file written by an earlier version exists at a former
// location (the project-local unburger.json or uo-fidelity.json, or the
// machine-global unburger/ or uo-fidelity/ directory) its values are imported in
// memory, and otherwise the defaults are returned, so a first run always works.
// The file path is returned even when it does not exist yet.
func Load() (Config, string, error) {
	path := Path()
	cfg, err := LoadFrom(path)
	if err != nil {
		return cfg, path, err
	}
	if path != "" {
		if _, statErr := os.Stat(path); errors.Is(statErr, fs.ErrNotExist) {
			if legacy := firstLegacy(path); legacy != "" {
				if legacyCfg, lerr := LoadFrom(legacy); lerr == nil {
					cfg = legacyCfg
				}
			}
		}
	}
	return cfg, path, nil
}

// MigrateLegacy imports a configuration written by an earlier version from a
// former location into the project-local file when the latter does not exist yet.
// It never deletes the former file. It reports whether it wrote a project-local
// file.
func MigrateLegacy() (bool, string, error) {
	local := Path()
	if local == "" {
		return false, "", nil
	}
	if _, err := os.Stat(local); err == nil {
		return false, local, nil
	}
	legacy := firstLegacy(local)
	if legacy == "" {
		return false, local, nil
	}
	cfg, err := LoadFrom(legacy)
	if err != nil {
		return false, local, err
	}
	if err := cfg.Save(local); err != nil {
		return false, local, err
	}
	return true, local, nil
}

// LoadFrom reads a configuration file. A missing or empty file yields the
// defaults with no error. Unknown keys are ignored; the current key names are
// always written back.
func LoadFrom(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("appconfig: reading config: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return cfg, nil
	}
	var file struct {
		Config
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return Default(), fmt.Errorf("appconfig: parsing config: %w", err)
	}
	cfg = file.Config
	cfg.Normalize()
	return cfg, nil
}

// Normalize fills in defaults for empty fields and rejects meaningless values
// by falling back to a safe default. It never invents an absolute path.
func (c *Config) Normalize() {
	def := Default()
	c.SourceCPK = cleanUserPath(c.SourceCPK)
	c.Corpus = cleanUserPath(c.Corpus)
	c.TranslationsDir = cleanUserPath(c.TranslationsDir)
	c.OutDir = cleanUserPath(c.OutDir)
	c.Extractor = cleanUserPath(c.Extractor)
	c.SourceSHA256 = strings.ToLower(strings.TrimSpace(c.SourceSHA256))
	c.SourceMemberListSHA256 = strings.ToLower(strings.TrimSpace(c.SourceMemberListSHA256))
	c.SourceProvenance = strings.TrimSpace(c.SourceProvenance)
	if c.SourceSize < 0 {
		c.SourceSize = 0
	}
	if c.SourceMembers < 0 {
		c.SourceMembers = 0
	}
	if c.SourceFMSMembers < 0 {
		c.SourceFMSMembers = 0
	}
	if c.TranslationsDir == "" {
		c.TranslationsDir = def.TranslationsDir
	}
	if c.OutDir == "" {
		c.OutDir = def.OutDir
	}
	if c.TitleID == "" || !build.IsSupportedTitleID(c.TitleID) {
		c.TitleID = def.TitleID
	}
	if _, err := build.ParseVariant(c.Variant); err != nil {
		c.Variant = def.Variant
	}
	switch c.Theme {
	case ThemeAuto, ThemeLight, ThemeDark:
	default:
		c.Theme = def.Theme
	}
}

// Save writes the configuration atomically with user-only permissions. The
// directory is created when missing. A partially written file can never be
// observed because the content lands in a temporary file first.
func (c Config) Save(path string) error {
	if path == "" {
		return fmt.Errorf("appconfig: no configuration path available")
	}
	c.Normalize()
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("appconfig: encoding config: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("appconfig: creating config directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("appconfig: creating temporary config: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("appconfig: writing temporary config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("appconfig: syncing temporary config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("appconfig: closing temporary config: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("appconfig: securing temporary config: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("appconfig: replacing config: %w", err)
	}
	return nil
}

// cleanUserPath trims surrounding whitespace (including a trailing newline from
// a pasted path) and drops control characters. It keeps the user's path exactly
// as typed otherwise; it must not rewrite someone's location.
func cleanUserPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range p {
		if unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}
