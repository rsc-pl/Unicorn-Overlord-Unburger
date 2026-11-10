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

// maxFilenameVersionLen bounds the version component of a release ZIP name. It
// stays far below the common 255-byte filename limit even after the descriptive
// prefix, and keeps a pathological version.txt from creating an unusable path.
const maxFilenameVersionLen = 64

// filenameVersion validates the release version for use as the version component
// of a release ZIP filename. The version always comes from the same authoritative
// mechanism as the rest of the release (ReadVersion -> Options.Version ->
// Result.Version); this function never derives or invents one. It returns the
// trimmed value unchanged, or a descriptive error when the value could escape the
// output directory, collide with a reserved device name, or produce an ambiguous
// or hidden filename. Validation is a hard failure rather than a lossy
// sanitisation: a release filename must name exactly the version that was built.
func filenameVersion(version string) (string, error) {
	v := strings.TrimSpace(version)
	if v == "" {
		return "", fmt.Errorf("package: a non-empty version from %s is required for the release ZIP filename", VersionFileName)
	}
	if len(v) > maxFilenameVersionLen {
		return "", fmt.Errorf("package: version %q is %d bytes; the release ZIP filename limit is %d", v, len(v), maxFilenameVersionLen)
	}
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '_', r == '+':
		default:
			return "", fmt.Errorf("package: version %q is not safe in a filename (allowed: letters, digits, '.', '-', '_', '+')", v)
		}
	}
	if v == "." || v == ".." || strings.HasPrefix(v, ".") || strings.HasSuffix(v, ".") {
		return "", fmt.Errorf("package: version %q is not usable as a filename component", v)
	}
	// Windows reserves device names regardless of extension (CON, CON.txt, ...).
	stem := strings.ToUpper(v)
	if i := strings.IndexByte(stem, '.'); i >= 0 {
		stem = stem[:i]
	}
	switch stem {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return "", fmt.Errorf("package: version %q is a reserved device name and cannot be used in a filename", v)
	}
	return v, nil
}

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
