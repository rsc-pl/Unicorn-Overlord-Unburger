package cpk

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"sort"
)

// MemberListDigest is the SHA-256 of the sorted "path\0\n" member-name list. It
// is a stable identity for a member set that is independent of TOC row order, so
// two archives with the same members in a different order share the digest. It is
// the digest the build engine and the extraction cache both record.
func MemberListDigest(paths []string) string {
	sorted := append([]string(nil), paths...)
	sort.Strings(sorted)
	h := sha256.New()
	for _, p := range sorted {
		io.WriteString(h, p)
		h.Write([]byte{0})
		io.WriteString(h, "\n")
	}
	return hex.EncodeToString(h.Sum(nil))
}
