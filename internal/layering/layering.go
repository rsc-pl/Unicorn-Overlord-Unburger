// Package layering decides, for each RomFS path, how the effective update
// filesystem is produced from a base filesystem and a patch filesystem.
//
// The logic is format independent: it only consumes file lists and a per-file
// "backed by the patch" tag. Decryption and container parsing stay elsewhere.
package layering

import (
	"fmt"
	"sort"
)

// Backing describes how a path is supplied in the effective update filesystem.
type Backing string

const (
	// InheritedFromBase: the file is not shipped by the patch and comes from base.
	InheritedFromBase Backing = "INHERITED_FROM_BASE"
	// ReplacedByPatch: the patch ships its own copy of a file that also exists in base.
	ReplacedByPatch Backing = "REPLACED_BY_PATCH"
	// AddedByPatch: the patch ships a file that base does not contain.
	AddedByPatch Backing = "ADDED_BY_PATCH"
)

// PatchFile is one entry of the effective update filesystem listing.
type PatchFile struct {
	Path string
	Size int64
	// UpdatedByPatch reports whether this entry is physically backed by the
	// patch data (true) or inherited from the base data (false).
	UpdatedByPatch bool
}

// File is a resolved effective-filesystem entry.
type File struct {
	Path    string
	Size    int64
	Backing Backing
}

// Resolve computes the effective update filesystem. base maps each base path to
// its size; update lists the effective update filesystem entries with their
// backing tag. Duplicate or empty update paths are rejected.
func Resolve(base map[string]int64, update []PatchFile) ([]File, error) {
	seen := make(map[string]struct{}, len(update))
	out := make([]File, 0, len(update)+len(base))
	for i, pf := range update {
		if pf.Path == "" {
			return nil, fmt.Errorf("layering: update entry %d has empty path", i)
		}
		if _, dup := seen[pf.Path]; dup {
			return nil, fmt.Errorf("layering: duplicate update path %q", pf.Path)
		}
		seen[pf.Path] = struct{}{}
		if pf.Size < 0 {
			return nil, fmt.Errorf("layering: update path %q has negative size", pf.Path)
		}

		backing := InheritedFromBase
		switch {
		case !pf.UpdatedByPatch:
			backing = InheritedFromBase
		case baseHas(base, pf.Path):
			backing = ReplacedByPatch
		default:
			backing = AddedByPatch
		}
		out = append(out, File{Path: pf.Path, Size: pf.Size, Backing: backing})
	}

	for path, size := range base {
		if _, ok := seen[path]; ok {
			continue
		}
		out = append(out, File{Path: path, Size: size, Backing: InheritedFromBase})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func baseHas(base map[string]int64, path string) bool {
	_, ok := base[path]
	return ok
}
