// Package project locates the checkout this toolkit belongs to and derives the
// private, project-local operational directories from it.
//
// Everything the toolkit remembers about the operator's machine lives under the
// repository's own .local/ tree, never in the public source and never in a
// machine-global configuration directory:
//
//	<root>/.local/uo-unburger.json      the single configuration file
//	<root>/.local/roms/{base,update,dlc}  the operator's game containers
//	<root>/.local/keys/                 the operator's decryption keys
//	<root>/.local/cache/                verified extraction cache (reusable)
//	<root>/.local/work/                 derived working state (builds, extract)
//	<root>/.local/release/              final user-facing ZIPs from a packaged build
//
// The root is a directory that contains both go.mod and translations/
// honorifics.tsv, found by walking up from the current working directory (then
// the executable's directory). UO_UNBURGER_ROOT overrides discovery; it exists so
// tests, scripts and an out-of-tree launch can pin the checkout explicitly. The
// earlier UNBURGER_ROOT and the oldest UO_FIDELITY_ROOT overrides are still
// honoured as fallbacks so an older shell environment keeps working.
//
// No absolute path is ever embedded in a value returned by this package's
// constructors: every path is derived from a caller-supplied root.
package project

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	// LocalDirName is the private operational directory inside the checkout.
	LocalDirName = ".local"
	// ConfigFileName is the project-local configuration file.
	ConfigFileName = "uo-unburger.json"
	// LegacyConfigFileName is the former project-local configuration file name
	// written by the earlier `unburger` rename. It is still honoured on read so
	// an existing private setup keeps working; the current file becomes the
	// single source of truth once it exists and the former file is never
	// deleted.
	LegacyConfigFileName = "unburger.json"
	// Legacy2ConfigFileName is the oldest project-local configuration file name
	// (`uo-fidelity`). It is read once during migration, after
	// LegacyConfigFileName, and never deleted.
	Legacy2ConfigFileName = "uo-fidelity.json"
	// GoModFile is the module marker.
	GoModFile = "go.mod"
	// TranslationsMarker is the second root marker: the public translation table
	// that only exists in a real checkout of this project.
	TranslationsMarker = "translations/honorifics.tsv"

	// RootEnv optionally pins the checkout root (absolute path).
	RootEnv = "UO_UNBURGER_ROOT"
	// LegacyRootEnv is the former root override (`UNBURGER_ROOT`). It is
	// consulted only when RootEnv is unset, so a shell configured for the
	// earlier name still works.
	LegacyRootEnv = "UNBURGER_ROOT"
	// Legacy2RootEnv is the oldest root override (`UO_FIDELITY_ROOT`), consulted
	// only when both names above are unset.
	Legacy2RootEnv = "UO_FIDELITY_ROOT"

	romsDirName    = "roms"
	baseDirName    = "base"
	updateDirName  = "update"
	dlcDirName     = "dlc"
	keysDirName    = "keys"
	cacheDirName   = "cache"
	workDirName    = "work"
	buildsDirName  = "builds"
	extractDirName = "extract"
	tmpDirName     = "tmp"
	releaseDirName = "release"
)

// ErrRootNotFound is returned when no checkout root can be located.
var ErrRootNotFound = errors.New("project: could not locate the project root (a directory containing go.mod and translations/honorifics.tsv)")

// isRoot reports whether dir is a checkout root: it must contain both the Go
// module file and the public translation marker.
func isRoot(dir string) bool {
	if !isFile(filepath.Join(dir, GoModFile)) {
		return false
	}
	return isFile(filepath.Join(dir, filepath.FromSlash(TranslationsMarker)))
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// FindRootFrom walks up from start (inclusive) looking for a checkout root. The
// reported bool is false when none is found.
func FindRootFrom(start string) (string, bool) {
	if start == "" {
		return "", false
	}
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", false
	}
	for {
		if isRoot(dir) {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// FindRoot locates the checkout root. UO_UNBURGER_ROOT wins when it names a
// directory; the former UNBURGER_ROOT and the oldest UO_FIDELITY_ROOT are used
// when the current variable is unset. Otherwise the current working directory is
// searched, then the executable's directory.
func FindRoot() (string, error) {
	envName, env := rootOverride()
	if env != "" {
		abs, err := filepath.Abs(env)
		if err != nil {
			return "", fmt.Errorf("project: invalid %s: %w", envName, err)
		}
		if info, err := os.Stat(abs); err != nil || !info.IsDir() {
			return "", fmt.Errorf("project: %s=%q is not a directory", envName, env)
		}
		return abs, nil
	}
	if wd, err := os.Getwd(); err == nil {
		if root, ok := FindRootFrom(wd); ok {
			return root, nil
		}
	}
	if exe, err := os.Executable(); err == nil {
		if root, ok := FindRootFrom(filepath.Dir(exe)); ok {
			return root, nil
		}
	}
	return "", ErrRootNotFound
}

// rootOverride returns the active root-pinning environment variable, in
// priority order: the current name, then the earlier rename (`UNBURGER_ROOT`),
// then the oldest name (`UO_FIDELITY_ROOT`). It reports the name so an error
// message names the variable the operator actually set.
func rootOverride() (name, value string) {
	for _, name := range [...]string{RootEnv, LegacyRootEnv, Legacy2RootEnv} {
		if v := os.Getenv(name); v != "" {
			return name, v
		}
	}
	return RootEnv, ""
}

// LocalDir is <root>/.local.
func LocalDir(root string) string { return filepath.Join(root, LocalDirName) }

// ConfigFile is <root>/.local/uo-unburger.json.
func ConfigFile(root string) string { return filepath.Join(LocalDir(root), ConfigFileName) }

// LegacyConfigFile is <root>/.local/unburger.json, the project-local
// configuration file written by the earlier rename. It is read once during
// migration and never deleted.
func LegacyConfigFile(root string) string {
	return filepath.Join(LocalDir(root), LegacyConfigFileName)
}

// Legacy2ConfigFile is <root>/.local/uo-fidelity.json, the oldest project-local
// configuration file. It is read once during migration, after LegacyConfigFile,
// and never deleted.
func Legacy2ConfigFile(root string) string {
	return filepath.Join(LocalDir(root), Legacy2ConfigFileName)
}

// CacheDir is <root>/.local/cache.
func CacheDir(root string) string { return filepath.Join(LocalDir(root), cacheDirName) }

// WorkDir is <root>/.local/work.
func WorkDir(root string) string { return filepath.Join(LocalDir(root), workDirName) }

// BuildsDir is <root>/.local/work/builds.
func BuildsDir(root string) string { return filepath.Join(WorkDir(root), buildsDirName) }

// ExtractDir is <root>/.local/work/extract (scratch for an extractor run).
func ExtractDir(root string) string { return filepath.Join(WorkDir(root), extractDirName) }

// TmpDir is <root>/.local/tmp.
func TmpDir(root string) string { return filepath.Join(LocalDir(root), tmpDirName) }

// ReleaseDir is <root>/.local/release, the destination of the user-facing ZIPs
// assembled by a packaged build. Intermediate build runs stay under
// <root>/.local/work/builds; only the final archives land here.
func ReleaseDir(root string) string { return filepath.Join(LocalDir(root), releaseDirName) }

// RomsDir is <root>/.local/roms.
func RomsDir(root string) string { return filepath.Join(LocalDir(root), romsDirName) }

// RomBaseDir is <root>/.local/roms/base.
func RomBaseDir(root string) string { return filepath.Join(RomsDir(root), baseDirName) }

// RomUpdateDir is <root>/.local/roms/update.
func RomUpdateDir(root string) string { return filepath.Join(RomsDir(root), updateDirName) }

// RomDLCDir is <root>/.local/roms/dlc.
func RomDLCDir(root string) string { return filepath.Join(RomsDir(root), dlcDirName) }

// KeysDir is <root>/.local/keys.
func KeysDir(root string) string { return filepath.Join(LocalDir(root), keysDirName) }

// Layout is the resolved set of project-local directories. It is a value, so a
// caller cannot accidentally mutate a shared path set.
type Layout struct {
	Root       string
	LocalDir   string
	ConfigFile string
	CacheDir   string
	WorkDir    string
	BuildsDir  string
	ExtractDir string
	TmpDir     string
	ReleaseDir string
	RomsDir    string
	RomBase    string
	RomUpdate  string
	RomDLC     string
	KeysDir    string
}

// NewLayout derives the full directory layout for a root.
func NewLayout(root string) Layout {
	return Layout{
		Root:       root,
		LocalDir:   LocalDir(root),
		ConfigFile: ConfigFile(root),
		CacheDir:   CacheDir(root),
		WorkDir:    WorkDir(root),
		BuildsDir:  BuildsDir(root),
		ExtractDir: ExtractDir(root),
		TmpDir:     TmpDir(root),
		ReleaseDir: ReleaseDir(root),
		RomsDir:    RomsDir(root),
		RomBase:    RomBaseDir(root),
		RomUpdate:  RomUpdateDir(root),
		RomDLC:     RomDLCDir(root),
		KeysDir:    KeysDir(root),
	}
}

// Dir returns every directory a first-run layout should contain, in creation
// order.
func (l Layout) Dir() []string {
	return []string{
		l.LocalDir, l.RomsDir, l.RomBase, l.RomUpdate, l.RomDLC,
		l.KeysDir, l.CacheDir, l.WorkDir, l.BuildsDir, l.ExtractDir, l.TmpDir, l.ReleaseDir,
	}
}

// Ensure creates the layout directories (never deleting or truncating anything)
// and returns the directories it had to create. Existing directories are left
// untouched.
func (l Layout) Ensure() ([]string, error) {
	var created []string
	for _, dir := range l.Dir() {
		if dir == "" {
			continue
		}
		if info, err := os.Stat(dir); err == nil {
			if !info.IsDir() {
				return created, fmt.Errorf("project: %s exists and is not a directory", dir)
			}
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return created, fmt.Errorf("project: inspecting %s: %w", dir, err)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return created, fmt.Errorf("project: creating %s: %w", dir, err)
		}
		created = append(created, dir)
	}
	return created, nil
}

// Missing reports which layout directories do not exist yet, for a guided
// first-run check.
func (l Layout) Missing() []string {
	var out []string
	for _, dir := range l.Dir() {
		if dir == "" {
			continue
		}
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			out = append(out, dir)
		}
	}
	return out
}
