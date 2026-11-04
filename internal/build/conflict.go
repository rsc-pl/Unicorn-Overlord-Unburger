package build

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ConflictPolicy decides what a publish step does when a destination already
// exists. It is chosen once per run and then applies to every colliding output
// of that run, so a multi-archive publish is never decided file by file.
type ConflictPolicy int

const (
	// ConflictRefuse fails without touching anything. It is the default for
	// library callers that supply no policy and no --force.
	ConflictRefuse ConflictPolicy = iota
	// ConflictReplace atomically replaces every existing destination, keeping a
	// backup until the whole new set is in place.
	ConflictReplace
	// ConflictKeepBoth keeps every existing file and writes the new set beside
	// it under deterministic "-2", "-3", ... names.
	ConflictKeepBoth
	// ConflictCancel aborts without touching anything.
	ConflictCancel
)

func (p ConflictPolicy) String() string {
	switch p {
	case ConflictReplace:
		return "replace"
	case ConflictKeepBoth:
		return "keep both"
	case ConflictCancel:
		return "cancel"
	default:
		return "refuse"
	}
}

// ErrOutputExists reports that a publish step refused to overwrite one or more
// existing outputs. Test it with errors.Is; the concrete *CollisionError names
// the files.
var ErrOutputExists = errors.New("output already exists")

// ErrOutputCancelled reports that the operator chose Cancel at an
// existing-output prompt. Nothing was written and nothing existing was changed.
var ErrOutputCancelled = errors.New("output already exists; cancelled")

// CollisionError lists the destination files that already exist. It is
// deliberately actionable: it names every file and tells the operator how to
// proceed, rather than reporting only the containing directory.
type CollisionError struct {
	Paths []string
}

func (e *CollisionError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%v: %d output file(s) already exist", ErrOutputExists, len(e.Paths))
	for _, p := range e.Paths {
		fmt.Fprintf(&b, "\n  %s", p)
	}
	b.WriteString("\nre-run with --force to replace them, or choose another output folder")
	return b.String()
}

// Unwrap makes errors.Is(err, ErrOutputExists) true for a *CollisionError.
func (e *CollisionError) Unwrap() error { return ErrOutputExists }

// findCollisions returns the sorted, de-duplicated subset of paths that already
// exist as a file or a directory.
func findCollisions(paths []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range paths {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		if _, err := os.Lstat(p); err == nil {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// uniquePath returns a deterministic sibling of p that does not exist yet and is
// not in reserved: "<base>-2<ext>", "<base>-3<ext>", ... The counter starts at 2
// because the original name is held by the existing file.
func uniquePath(p string, reserved map[string]bool) string {
	ext := filepath.Ext(p)
	base := strings.TrimSuffix(p, ext)
	for i := 2; ; i++ {
		cand := fmt.Sprintf("%s-%d%s", base, i, ext)
		if reserved[cand] {
			continue
		}
		if _, err := os.Lstat(cand); err == nil {
			continue
		}
		return cand
	}
}

// keepBothPaths resolves a destination set under ConflictKeepBoth: an existing
// path gets a deterministic unique "-N" sibling, a free path keeps its name.
// The mapping preserves order and never yields two equal destinations.
func keepBothPaths(paths []string) []string {
	reserved := map[string]bool{}
	for _, p := range paths {
		if _, err := os.Lstat(p); err != nil {
			reserved[p] = true
		}
	}
	out := make([]string, len(paths))
	for i, p := range paths {
		if reserved[p] {
			out[i] = p
			continue
		}
		cand := uniquePath(p, reserved)
		reserved[cand] = true
		out[i] = cand
	}
	return out
}

// resolveOutputConflicts applies one conflict policy to a whole destination set.
// It returns the paths to write: unchanged for replace/refuse, deterministic
// "-N" siblings for keep-both. onConflict, when non-nil, is called at most once
// with the sorted list of existing paths; it lets a UI ask the operator without
// the build engine depending on any UI. When onConflict is nil, overwrite is the
// fallback (true => replace, false => refuse).
func resolveOutputConflicts(paths []string, overwrite bool, onConflict func([]string) ConflictPolicy) ([]string, error) {
	existing := findCollisions(paths)
	if len(existing) == 0 {
		return paths, nil
	}
	policy := ConflictRefuse
	if overwrite {
		policy = ConflictReplace
	}
	if onConflict != nil {
		policy = onConflict(existing)
	}
	switch policy {
	case ConflictReplace:
		return paths, nil
	case ConflictKeepBoth:
		return keepBothPaths(paths), nil
	case ConflictCancel:
		return nil, fmt.Errorf("%w: %d existing output file(s) left unchanged", ErrOutputCancelled, len(existing))
	default:
		return nil, &CollisionError{Paths: existing}
	}
}

// randomSuffix returns 8 hex characters used to make a temporary or backup name
// unique. A process id is the fallback if the system entropy source fails.
func randomSuffix() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", os.Getpid())
	}
	return hex.EncodeToString(b[:])
}
