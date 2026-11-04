package build

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// VersionFileName is the checkout-root file that carries the release version.
// The toolkit reads it, never writes it, and never hard-codes its value.
const VersionFileName = "version.txt"

// ErrVersionMissing reports an absent or empty root version.txt. It is returned
// before any source is read or any output is staged, so a version failure never
// leaves a partial build or package behind.
var ErrVersionMissing = errors.New("version.txt missing/empty")

// ReadVersion reads <root>/version.txt and returns the version string with
// surrounding whitespace (including a trailing newline) trimmed. A missing file
// or an empty/whitespace-only value is reported as ErrVersionMissing; the
// version is never invented or defaulted.
func ReadVersion(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", ErrVersionMissing
	}
	path := filepath.Join(root, VersionFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("%w: %s", ErrVersionMissing, filepath.ToSlash(path))
		}
		return "", fmt.Errorf("build: reading %s: %w", VersionFileName, err)
	}
	v := strings.TrimSpace(string(data))
	if v == "" {
		return "", fmt.Errorf("%w: %s is empty", ErrVersionMissing, filepath.ToSlash(path))
	}
	return v, nil
}

// FormatTag renders the startup-label text for a version: "Unburger Mod V<version>".
// An empty version yields an empty tag so a report can carry it without
// claiming a version that does not exist.
func FormatTag(version string) string {
	v := strings.TrimSpace(version)
	if v == "" {
		return ""
	}
	return "Unburger Mod V" + v
}
