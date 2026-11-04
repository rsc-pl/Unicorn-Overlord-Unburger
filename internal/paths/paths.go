// Package paths renders filesystem paths for user-facing command output and
// error messages without leaking an absolute location. Generated reports and
// diagnostics are meant to be reproducible and safe to quote, so a private
// absolute path (for example a home-directory game backup) must never appear in
// them.
package paths

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Display reduces a path to a relative, non-absolute form for output:
//
//   - an empty path stays empty;
//   - a relative path is cleaned and returned as-is;
//   - an absolute path under the current working directory becomes
//     workspace-relative (forward slashes), which is how the project refers to
//     its own files;
//   - any other absolute path is reduced to its base name so the surrounding
//     private directory structure is not disclosed.
func Display(p string) string {
	if p == "" {
		return ""
	}
	if !filepath.IsAbs(p) {
		return filepath.ToSlash(filepath.Clean(p))
	}
	if wd, err := os.Getwd(); err == nil {
		if rel, err := filepath.Rel(wd, p); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.Base(p)
}

// Error renders an error without its filesystem path. An *fs.PathError embeds
// the offending path in its message; only its underlying cause is returned so a
// wrapped absolute path cannot leak through an error. Other errors are rendered
// unchanged.
func Error(err error) string {
	if err == nil {
		return ""
	}
	var perr *fs.PathError
	if errors.As(err, &perr) && perr.Err != nil {
		return perr.Err.Error()
	}
	return err.Error()
}
