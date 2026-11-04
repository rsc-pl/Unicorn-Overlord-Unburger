package build

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// stagingPrefix names a build's temporary staging directory. Every publication
// creates a fresh, uniquely named one under its run directory, so an interrupted
// or abandoned staging directory can never collide with, or block, the next one.
const stagingPrefix = ".staging-"

// staleStagingAge is how long an abandoned staging entry must have been untouched
// before the next publication sweeps it. A fresh staging directory that belongs
// to a concurrently running build is left alone.
const staleStagingAge = 30 * time.Minute

// stagedDir is one edition waiting to be published.
type stagedDir struct {
	dir   string
	files map[string][]byte
}

// publishItem is one staged file or directory ready to be renamed into place.
type publishItem struct {
	tmp   string
	final string
}

// backupPair remembers where an existing destination was moved so a failed
// publication can restore it.
type backupPair struct {
	original string
	backup   string
}

// publishSet renames every staged item into its final location. An existing
// destination is first moved to a uniquely named sibling backup; if any rename
// fails, the whole set rolls back — every destination this call created is
// removed and every backup is restored — so a failed multi-target publish never
// leaves a mixed old/new set and never loses a previously valid file. The
// backups are deleted only once the entire set is in place.
func publishSet(items []publishItem) error {
	var backups []backupPair
	var placed []string
	rollback := func() {
		for i := len(placed) - 1; i >= 0; i-- {
			os.RemoveAll(placed[i])
		}
		for i := len(backups) - 1; i >= 0; i-- {
			os.Rename(backups[i].backup, backups[i].original)
		}
	}
	for _, it := range items {
		if _, err := os.Lstat(it.final); err == nil {
			bak := it.final + ".bak-" + randomSuffix()
			if err := os.Rename(it.final, bak); err != nil {
				rollback()
				return fmt.Errorf("publish: backing up %q: %w", it.final, err)
			}
			backups = append(backups, backupPair{original: it.final, backup: bak})
		}
		if err := os.Rename(it.tmp, it.final); err != nil {
			rollback()
			return fmt.Errorf("publish: publishing %q: %w", it.final, err)
		}
		placed = append(placed, it.final)
	}
	for _, b := range backups {
		os.RemoveAll(b.backup)
	}
	return nil
}

// sweepStaleStaging removes abandoned staging entries under dir. Only names this
// package creates are ever considered (".staging-*" plus the legacy
// "<name>.staging" suffix), and only once they are older than staleStagingAge, so
// a running build's staging is never disturbed. It is best-effort: a leftover
// staging entry never blocks a new build anyway, because every staging name is
// unique.
func sweepStaleStaging(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-staleStagingAge)
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, stagingPrefix) && !strings.HasSuffix(name, ".staging") {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(dir, name))
	}
}

// publishAll writes every edition into a fresh, uniquely named staging directory
// under runDir, then renames each staging directory into its final location. If
// any step fails, all staging directories and any directories already renamed by
// this call are restored, so a failed build never leaves a partial output and the
// previous build in runDir (if any) is preserved.
//
// A run directory that already holds a complete, verified build is handled a
// level up, in Run, by reusing it; publishAll itself refuses to overwrite an
// edition directory it did not create in this call, so an unverified directory is
// never silently replaced.
func publishAll(runDir string, pending []stagedDir) (err error) {
	if len(pending) == 0 {
		return nil
	}
	if info, statErr := os.Stat(runDir); statErr == nil && !info.IsDir() {
		return fmt.Errorf("build: output path %q exists and is not a directory", runDir)
	}
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return fmt.Errorf("build: creating output directory: %w", err)
	}
	sweepStaleStaging(runDir)

	// Refuse up front if any final edition directory already exists: Run reuses a
	// verified run before reaching here, so an existing directory at this point is
	// unverified and must not be overwritten.
	for _, p := range pending {
		if _, statErr := os.Lstat(p.dir); statErr == nil {
			return fmt.Errorf("build: output directory %q already exists; refusing to overwrite", p.dir)
		}
	}

	var items []publishItem
	cleanupStaging := func() {
		for _, it := range items {
			os.RemoveAll(it.tmp)
		}
	}

	// Phase 1: stage everything in unique temporary directories.
	for _, p := range pending {
		tmp, err := os.MkdirTemp(runDir, stagingPrefix)
		if err != nil {
			cleanupStaging()
			return fmt.Errorf("build: creating staging directory: %w", err)
		}
		items = append(items, publishItem{tmp: tmp, final: p.dir})
		for rel, data := range p.files {
			full := filepath.Join(tmp, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				cleanupStaging()
				return fmt.Errorf("build: staging %s: %w", rel, err)
			}
			if err := os.WriteFile(full, data, 0o644); err != nil {
				cleanupStaging()
				return fmt.Errorf("build: writing %s: %w", rel, err)
			}
		}
	}

	// Phase 2: publish by rename, rolling the whole set back on failure.
	if perr := publishSet(items); perr != nil {
		cleanupStaging()
		return perr
	}
	return nil
}

// listStagedFiles is a deterministic helper used by tests.
func listStagedFiles(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}
