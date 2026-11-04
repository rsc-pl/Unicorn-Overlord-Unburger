// Package installer installs, updates, uninstalls and rolls back the LayeredFS
// output of a build into a directory the user chooses explicitly.
//
// Safety rules, all enforced in code:
//
//   - the destination root must be given explicitly and be an absolute path
//     that is not a filesystem root; the package never guesses or remembers a
//     game/SD/emulator location and never hard-codes one;
//   - a root listed as a protected (active game) directory requires the caller
//     to repeat the title ID as a typed confirmation, and an existing file is
//     always backed up before it is replaced or removed;
//   - only the exact override file (and this package's own metadata file) is
//     ever written or removed, so unrelated files in the same directories are
//     preserved;
//   - writes are staged to a temporary file and renamed into place, and a
//     per-root lock file serialises concurrent operations, so an interrupted
//     run leaves either the old file or the new file, never a partial one.
//
// The package performs no decryption, downloads nothing, and copies no game
// data anywhere except the destination root the caller supplied.
package installer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"uo-unburger/internal/build"
	"uo-unburger/internal/layeredfs"
)

const (
	// ArtifactRel is the LayeredFS-relative path of the override archive,
	// re-exported from internal/layeredfs so build, install and package agree.
	ArtifactRel = layeredfs.RomFSRel
	// MetaName is this package's install metadata file, next to the override.
	MetaName = ".uo-unburger-install.json"
	// LockName is the per-root operation lock file.
	LockName = ".uo-unburger.lock"
	// BackupDirName is the per-root backup directory.
	BackupDirName = ".uo-unburger-backups"

	staleLockAfter = time.Hour
)

// Sentinel errors. Callers can test them with errors.Is.
var (
	ErrNoRoot             = errors.New("installer: an explicit destination root is required")
	ErrRelativeRoot       = errors.New("installer: destination root must be an absolute path")
	ErrUnsupportedTitleID = errors.New("installer: unsupported title ID")
	ErrProtected          = errors.New("installer: destination is a protected active game directory; typed confirmation required")
	ErrLocked             = errors.New("installer: another operation holds the destination root lock")
	ErrNotInstalled       = errors.New("installer: no override is installed at the destination")
	ErrNoBackup           = errors.New("installer: no backup found")
	ErrInvalidSource      = errors.New("installer: source archive is not valid")
)

// SourceInfo is a validated source artifact.
type SourceInfo struct {
	Path   string
	SHA256 string
	Size   int64
}

// Validator validates a source archive read-only. The default validator uses
// the same gate as the builder (internal/build), so the installer cannot accept
// an archive the build engine would reject.
type Validator func(path string) (*SourceInfo, error)

// DefaultValidator validates through the shared build source gate.
func DefaultValidator(path string) (*SourceInfo, error) {
	facts, err := build.ValidateSource(path, build.SourceExpectation{})
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidSource, err)
	}
	return &SourceInfo{Path: facts.Path, SHA256: facts.SHA256, Size: facts.Size}, nil
}

// Options configures an installer operation. Root and TitleID are mandatory.
type Options struct {
	// Root is the explicit LayeredFS root (for example a mounted SD card's
	// atmosphere directory). Relative paths are rejected.
	Root string
	// TitleID selects the contents folder. Only supported title IDs are allowed.
	TitleID string
	// ProtectedRoots lists directories that must be treated as a live game
	// installation (for example a configured emulator data directory). A target
	// inside one of them requires Confirm to equal TitleID, and always requires
	// a backup first.
	ProtectedRoots []string
	// Confirm is the typed confirmation token. It must equal TitleID when the
	// destination is protected.
	Confirm string
	// RunID overrides the timestamp used to name a backup (tests pin it).
	RunID string
	// Now supplies the clock (tests pin it).
	Now func() time.Time
	// Validate overrides source validation (tests inject one). A nil validator
	// uses DefaultValidator.
	Validate Validator
	// KeepBackup, when true, leaves the most recent backup in place after a
	// rollback in addition to the safety backup taken from the current file.
	KeepBackup bool
}

func (o Options) validator() Validator {
	if o.Validate != nil {
		return o.Validate
	}
	return DefaultValidator
}

func (o Options) clock() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (o Options) stamp() string {
	if o.RunID != "" {
		return sanitize(o.RunID)
	}
	return o.clock().UTC().Format("20060102T150405Z")
}

// layout holds the resolved destination paths for one option set.
type layout struct {
	root        string
	titleID     string
	contentsDir string
	romfsDir    string
	target      string
	meta        string
	lock        string
	backupDir   string
}

func resolve(o Options) (layout, error) {
	if strings.TrimSpace(o.Root) == "" {
		return layout{}, ErrNoRoot
	}
	root := filepath.Clean(o.Root)
	if !filepath.IsAbs(root) {
		return layout{}, fmt.Errorf("%w (got %q)", ErrRelativeRoot, o.Root)
	}
	if root == string(filepath.Separator) || root == "." {
		return layout{}, fmt.Errorf("%w: refusing to use filesystem root %q", ErrNoRoot, root)
	}
	if o.TitleID == "" {
		return layout{}, fmt.Errorf("%w: no title ID given", ErrUnsupportedTitleID)
	}
	if !build.IsSupportedTitleID(o.TitleID) {
		return layout{}, fmt.Errorf("%w %q (supported: %s)", ErrUnsupportedTitleID,
			o.TitleID, strings.Join(build.SupportedTitleIDs(), ", "))
	}
	if strings.ContainsAny(o.TitleID, `/\`) {
		return layout{}, fmt.Errorf("%w %q", ErrUnsupportedTitleID, o.TitleID)
	}
	contents := filepath.Join(root, "contents", o.TitleID)
	romfs := filepath.Join(contents, "romfs")
	return layout{
		root:        root,
		titleID:     o.TitleID,
		contentsDir: contents,
		romfsDir:    romfs,
		target:      filepath.Join(contents, filepath.FromSlash(ArtifactRel)),
		meta:        filepath.Join(contents, MetaName),
		lock:        filepath.Join(root, LockName),
		backupDir:   filepath.Join(root, BackupDirName, o.TitleID),
	}, nil
}

// isProtected reports whether target lies inside one of the protected roots.
func isProtected(target string, roots []string) bool {
	target = filepath.Clean(target)
	for _, r := range roots {
		r = strings.TrimSpace(r)
		if r == "" || !filepath.IsAbs(r) {
			continue
		}
		r = filepath.Clean(r)
		if target == r {
			return true
		}
		if strings.HasPrefix(target, r+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// Backup describes one stored backup.
type Backup struct {
	Path   string
	Size   int64
	SHA256 string
	Time   time.Time
}

// Meta is the install metadata written next to the override file.
type Meta struct {
	TitleID     string `json:"title_id"`
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
	SourcePath  string `json:"source_path"`
	Action      string `json:"action"`
	InstalledAt string `json:"installed_at"`
}

// Preview is a read-only description of what an operation would do. It performs
// no writes: it only hashes and stats.
type Preview struct {
	Action            string // install | update | reinstall | noop | uninstall
	Root              string
	TitleID           string
	Target            string
	Source            string
	SourceSHA256      string
	SourceSize        int64
	Existing          bool
	ExistingSHA256    string
	ExistingSize      int64
	Identical         bool
	Protected         bool
	ConfirmationToken string
	BackupRequired    bool
	UnrelatedFiles    []string
	Meta              *Meta
}

// PreviewInstall validates the source and destination and reports the intended
// install action without writing anything.
func PreviewInstall(source string, o Options) (*Preview, error) {
	lay, err := resolve(o)
	if err != nil {
		return nil, err
	}
	info, err := o.validator()(source)
	if err != nil {
		return nil, err
	}
	p := &Preview{
		Root:              lay.root,
		TitleID:           lay.titleID,
		Target:            lay.target,
		Source:            info.Path,
		SourceSHA256:      info.SHA256,
		SourceSize:        info.Size,
		Protected:         isProtected(lay.target, o.ProtectedRoots),
		ConfirmationToken: lay.titleID,
	}
	if p.Protected {
		p.BackupRequired = true
	}

	if existing, err := os.Stat(lay.target); err == nil && !existing.IsDir() {
		p.Existing = true
		p.ExistingSize = existing.Size()
		sum, _, err := hashFile(lay.target)
		if err != nil {
			return nil, err
		}
		p.ExistingSHA256 = sum
		p.Identical = sum == info.SHA256
	}
	if meta, err := readMeta(lay.meta); err == nil {
		p.Meta = meta
	}

	switch {
	case !p.Existing:
		p.Action = "install"
	case p.Identical:
		p.Action = "noop"
	case p.Meta != nil && p.Meta.SHA256 != "" && strings.EqualFold(p.Meta.SHA256, p.ExistingSHA256):
		p.Action = "update"
		p.BackupRequired = true
	default:
		p.Action = "reinstall"
		p.BackupRequired = true
	}
	if p.Existing && !p.Identical {
		p.BackupRequired = true
	}
	p.UnrelatedFiles = unrelatedFiles(lay)
	return p, nil
}

// unrelatedFiles lists files in the destination tree other than the override and
// this package's metadata. They are never touched, but they are surfaced so the
// operator can see them in a preview.
func unrelatedFiles(lay layout) []string {
	var out []string
	if entries, err := os.ReadDir(lay.romfsDir); err == nil {
		for _, e := range entries {
			if e.Name() == filepath.Base(lay.target) {
				continue
			}
			out = append(out, filepath.ToSlash(filepath.Join("romfs", e.Name())))
		}
	}
	if entries, err := os.ReadDir(lay.contentsDir); err == nil {
		for _, e := range entries {
			if e.Name() == "romfs" || e.Name() == MetaName {
				continue
			}
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// Result is the outcome of a completed operation.
type Result struct {
	Action     string
	Target     string
	Root       string
	TitleID    string
	SHA256     string
	Size       int64
	BackupPath string
	Changed    bool
	Unrelated  []string
}

// PreviewUninstall reports what an uninstall would do, without writing. A
// missing override is an error, matching Uninstall.
func PreviewUninstall(o Options) (*Preview, error) {
	lay, err := resolve(o)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(lay.target)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrNotInstalled, lay.target)
		}
		return nil, fmt.Errorf("installer: inspecting destination: %w", err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("installer: destination %s is a directory", lay.target)
	}
	sum, size, err := hashFile(lay.target)
	if err != nil {
		return nil, err
	}
	meta, _ := readMeta(lay.meta)
	return &Preview{
		Action:            "uninstall",
		Root:              lay.root,
		TitleID:           lay.titleID,
		Target:            lay.target,
		Existing:          true,
		ExistingSHA256:    sum,
		ExistingSize:      size,
		Protected:         isProtected(lay.target, o.ProtectedRoots),
		ConfirmationToken: lay.titleID,
		BackupRequired:    true,
		UnrelatedFiles:    unrelatedFiles(lay),
		Meta:              meta,
	}, nil
}

// Install writes the validated source archive to the destination override path.
// An existing file is backed up first. The destination directory keeps every
// unrelated file it already contains.
func Install(source string, o Options) (*Result, error) {
	lay, err := resolve(o)
	if err != nil {
		return nil, err
	}
	p, err := PreviewInstall(source, o)
	if err != nil {
		return nil, err
	}
	if p.Protected && strings.TrimSpace(o.Confirm) != lay.titleID {
		return nil, fmt.Errorf("%w: repeat the title ID %q as the confirmation token", ErrProtected, lay.titleID)
	}
	if err := os.MkdirAll(lay.root, 0o755); err != nil {
		return nil, fmt.Errorf("installer: creating destination root: %w", err)
	}
	unlock, err := acquireLock(lay.lock, o.clock(), o.RunID)
	if err != nil {
		return nil, err
	}
	defer unlock()

	res := &Result{
		Action:    p.Action,
		Target:    lay.target,
		Root:      lay.root,
		TitleID:   lay.titleID,
		SHA256:    p.SourceSHA256,
		Size:      p.SourceSize,
		Unrelated: p.UnrelatedFiles,
	}
	if p.Action == "noop" {
		return res, nil
	}

	if p.Existing && !p.Identical {
		backup, err := backupFile(lay, o, "install")
		if err != nil {
			return nil, err
		}
		res.BackupPath = backup
	}

	if err := os.MkdirAll(lay.romfsDir, 0o755); err != nil {
		return nil, fmt.Errorf("installer: creating romfs directory: %w", err)
	}
	if err := writeAtomic(lay.target, source, o.clock()); err != nil {
		return nil, err
	}
	if err := writeMeta(lay.meta, Meta{
		TitleID:     lay.titleID,
		SHA256:      p.SourceSHA256,
		Size:        p.SourceSize,
		SourcePath:  filepath.Base(p.Source),
		Action:      p.Action,
		InstalledAt: o.clock().UTC().Format(time.RFC3339),
	}); err != nil {
		return nil, err
	}
	res.Changed = true
	return res, nil
}

// Uninstall removes the override file (after backing it up) and leaves every
// unrelated file in place. The romfs directory itself is only removed when it is
// empty.
func Uninstall(o Options) (*Result, error) {
	lay, err := resolve(o)
	if err != nil {
		return nil, err
	}
	if isProtected(lay.target, o.ProtectedRoots) && strings.TrimSpace(o.Confirm) != lay.titleID {
		return nil, fmt.Errorf("%w: repeat the title ID %q as the confirmation token", ErrProtected, lay.titleID)
	}
	if _, err := os.Stat(lay.target); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrNotInstalled, lay.target)
		}
		return nil, fmt.Errorf("installer: inspecting destination: %w", err)
	}
	if err := os.MkdirAll(lay.root, 0o755); err != nil {
		return nil, fmt.Errorf("installer: creating destination root: %w", err)
	}
	unlock, err := acquireLock(lay.lock, o.clock(), o.RunID)
	if err != nil {
		return nil, err
	}
	defer unlock()

	res := &Result{
		Action:    "uninstall",
		Target:    lay.target,
		Root:      lay.root,
		TitleID:   lay.titleID,
		Unrelated: unrelatedFiles(lay),
	}
	backup, err := backupFile(lay, o, "uninstall")
	if err != nil {
		return nil, err
	}
	res.BackupPath = backup

	if err := os.Remove(lay.target); err != nil {
		return nil, fmt.Errorf("installer: removing override: %w", err)
	}
	_ = os.Remove(lay.meta)
	// Only remove directories that became empty; unrelated files keep them alive.
	_ = os.Remove(lay.romfsDir)
	_ = os.Remove(lay.contentsDir)
	_ = os.Remove(filepath.Dir(lay.contentsDir))
	res.Changed = true
	return res, nil
}

// Rollback restores a previously stored backup over the override path. An empty
// backupPath selects the most recent backup. The current file is backed up
// first, so a rollback is itself reversible.
func Rollback(o Options, backupPath string) (*Result, error) {
	lay, err := resolve(o)
	if err != nil {
		return nil, err
	}
	if isProtected(lay.target, o.ProtectedRoots) && strings.TrimSpace(o.Confirm) != lay.titleID {
		return nil, fmt.Errorf("%w: repeat the title ID %q as the confirmation token", ErrProtected, lay.titleID)
	}
	if err := os.MkdirAll(lay.root, 0o755); err != nil {
		return nil, fmt.Errorf("installer: creating destination root: %w", err)
	}
	unlock, err := acquireLock(lay.lock, o.clock(), o.RunID)
	if err != nil {
		return nil, err
	}
	defer unlock()

	backups, err := Backups(o)
	if err != nil {
		return nil, err
	}
	if backupPath == "" {
		if len(backups) == 0 {
			return nil, ErrNoBackup
		}
		backupPath = backups[len(backups)-1].Path // newest (sorted ascending)
	}
	if _, err := os.Stat(backupPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrNoBackup, backupPath)
		}
		return nil, fmt.Errorf("installer: inspecting backup: %w", err)
	}

	res := &Result{
		Action:    "rollback",
		Target:    lay.target,
		Root:      lay.root,
		TitleID:   lay.titleID,
		Unrelated: unrelatedFiles(lay),
	}
	if _, err := os.Stat(lay.target); err == nil {
		safety, err := backupFile(lay, o, "pre-rollback")
		if err != nil {
			return nil, err
		}
		res.BackupPath = safety
	}
	if err := os.MkdirAll(lay.romfsDir, 0o755); err != nil {
		return nil, fmt.Errorf("installer: creating romfs directory: %w", err)
	}
	if err := writeAtomic(lay.target, backupPath, o.clock()); err != nil {
		return nil, err
	}
	sum, size, err := hashFile(lay.target)
	if err != nil {
		return nil, err
	}
	res.SHA256 = sum
	res.Size = size
	res.Changed = true
	return res, nil
}

// Backups lists the stored backups for the destination, oldest first.
func Backups(o Options) ([]Backup, error) {
	lay, err := resolve(o)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(lay.backupDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("installer: reading backups: %w", err)
	}
	var out []Backup
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".cpk") {
			continue
		}
		full := filepath.Join(lay.backupDir, e.Name())
		info, err := e.Info()
		if err != nil {
			continue
		}
		sum, size, err := hashFile(full)
		if err != nil {
			return nil, err
		}
		out = append(out, Backup{Path: full, Size: size, SHA256: sum, Time: info.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Time.Equal(out[j].Time) {
			return out[i].Path < out[j].Path
		}
		return out[i].Time.Before(out[j].Time)
	})
	return out, nil
}

// backupFile copies the current override into the backup directory and returns
// the stored path. It returns an empty path when the override does not exist.
func backupFile(lay layout, o Options, action string) (string, error) {
	src := lay.target
	if _, err := os.Stat(src); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("installer: inspecting file to back up: %w", err)
	}
	if err := os.MkdirAll(lay.backupDir, 0o755); err != nil {
		return "", fmt.Errorf("installer: creating backup directory: %w", err)
	}
	name := fmt.Sprintf("%s-%s-%s", o.stamp(), sanitize(action), filepath.Base(lay.target))
	dst := uniqueBackupPath(lay.backupDir, name)
	if err := copyFile(src, dst); err != nil {
		return "", err
	}
	return dst, nil
}

// uniqueBackupPath avoids overwriting an existing backup when two operations
// happen inside the same timestamp granularity.
func uniqueBackupPath(dir, name string) string {
	candidate := filepath.Join(dir, name)
	if _, err := os.Stat(candidate); errors.Is(err, fs.ErrNotExist) {
		return candidate
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 2; i < 1000; i++ {
		candidate = filepath.Join(dir, fmt.Sprintf("%s-%d%s", base, i, ext))
		if _, err := os.Stat(candidate); errors.Is(err, fs.ErrNotExist) {
			return candidate
		}
	}
	return filepath.Join(dir, fmt.Sprintf("%s-%d%s", base, time.Now().UnixNano(), ext))
}

// writeAtomic copies src to a temporary file next to dst, syncs it and renames
// it over dst. An interrupted operation leaves the previous destination intact.
func writeAtomic(dst, src string, now time.Time) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("installer: opening source: %w", err)
	}
	defer in.Close()

	dir := filepath.Dir(dst)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(dst)+".tmp-*")
	if err != nil {
		return fmt.Errorf("installer: creating temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return fmt.Errorf("installer: writing temporary file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("installer: syncing temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("installer: closing temporary file: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("installer: setting permissions: %w", err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return fmt.Errorf("installer: replacing destination: %w", err)
	}
	_ = os.Chtimes(dst, now, now)
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("installer: opening %s: %w", filepath.Base(src), err)
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".backup-*.tmp")
	if err != nil {
		return fmt.Errorf("installer: creating backup temporary: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return fmt.Errorf("installer: writing backup: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("installer: closing backup: %w", err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return fmt.Errorf("installer: storing backup: %w", err)
	}
	return nil
}

func writeMeta(path string, m Meta) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("installer: encoding metadata: %w", err)
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("installer: creating metadata temporary: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return fmt.Errorf("installer: writing metadata: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("installer: syncing metadata: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("installer: closing metadata: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("installer: setting metadata permissions: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("installer: storing metadata: %w", err)
	}
	return nil
}

func readMeta(path string) (*Meta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, fmt.Errorf("installer: hashing %s: %w", filepath.Base(path), err)
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, fmt.Errorf("installer: hashing %s: %w", filepath.Base(path), err)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// acquireLock creates an exclusive lock file for the destination root. A lock
// older than staleLockAfter is treated as left behind by an interrupted run and
// is replaced. The returned function removes the lock.
func acquireLock(path string, now time.Time, runID string) (func(), error) {
	write := func() error {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		payload := fmt.Sprintf("pid=%d\nstarted=%s\nrun=%s\n",
			os.Getpid(), now.UTC().Format(time.RFC3339), runID)
		if _, err := f.WriteString(payload); err != nil {
			f.Close()
			os.Remove(path)
			return err
		}
		return f.Close()
	}
	err := write()
	if err == nil {
		return func() { os.Remove(path) }, nil
	}
	if !errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("installer: creating lock: %w", err)
	}
	info, statErr := os.Stat(path)
	if statErr == nil && now.Sub(info.ModTime()) > staleLockAfter {
		if rmErr := os.Remove(path); rmErr != nil {
			return nil, fmt.Errorf("installer: removing stale lock: %w", rmErr)
		}
		if err := write(); err == nil {
			return func() { os.Remove(path) }, nil
		}
	}
	return nil, fmt.Errorf("%w (%s)", ErrLocked, filepath.Base(path))
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "backup"
	}
	return out
}
