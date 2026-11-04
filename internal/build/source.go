package build

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"uo-unburger/internal/cpk"
	"uo-unburger/internal/paths"
)

// supportedTitleIDs are the Nintendo Switch title IDs this project targets: the
// western base title and its update title (the documented alternative for a
// LayeredFS override folder). A build for any other title ID is rejected before
// any archive is touched.
var supportedTitleIDs = []string{"010069401ADB8000", "010069401ADB8800"}

// supportedDisplayVersions are the western display versions this project has
// evidence for. A different version is rejected before any archive is touched.
var supportedDisplayVersions = []string{"1.00", "1.05"}

// DefaultTitleID is the base western title ID used when the caller supplies none.
const DefaultTitleID = "010069401ADB8000"

// SourceIdentity is a recorded, verifiable identity for a localization archive:
// its exact size and SHA-256 plus the structural member identity. A build's source
// is recognised by this record so an already-verified local archive can be reused
// without re-extraction, and so a translated build output can never be mistaken
// for a source.
type SourceIdentity struct {
	Size             int64
	SHA256           string
	Members          int
	FMSMembers       int
	MemberListSHA256 string
}

// VerifiedSourceIdentity is the recorded identity of the western update-effective
// localization archive (Unicorn_US.CPK) this project has evidence for: 47 members
// (32 MsgSheet/*.fms), 4,782,784 bytes, SHA-256 3fdc6240… and member-list digest
// d4824526…. The archive is a no-op rebuild that is byte-identical to the
// extracted update-effective CPK, so this digest identifies the canonical source
// bytes. It is used only to recognise a trusted local archive.
var VerifiedSourceIdentity = SourceIdentity{
	Size:             4782784,
	SHA256:           "3fdc6240f702facf8b11d747f46568041ff25b39269c74923596cfb648f25603",
	Members:          47,
	FMSMembers:       32,
	MemberListSHA256: "d48245260bebd5830d9ea5129dbe020e3413a5c3e8bb0a5bf787401c125f704b",
}

// Matches reports whether a verified archive description satisfies this identity.
// Every populated field must agree; a zero/empty field is not asserted.
func (id SourceIdentity) Matches(size int64, sha256, memberListSHA256 string, members, fms int) bool {
	if id.Size != 0 && size != id.Size {
		return false
	}
	if id.SHA256 != "" && !strings.EqualFold(sha256, id.SHA256) {
		return false
	}
	if id.MemberListSHA256 != "" && !strings.EqualFold(memberListSHA256, id.MemberListSHA256) {
		return false
	}
	if id.Members != 0 && members != id.Members {
		return false
	}
	if id.FMSMembers != 0 && fms != id.FMSMembers {
		return false
	}
	return id.Size != 0 || id.SHA256 != "" || id.MemberListSHA256 != "" || id.Members != 0 || id.FMSMembers != 0
}

// SupportedTitleIDs returns a copy of the accepted title IDs.
func SupportedTitleIDs() []string { return append([]string(nil), supportedTitleIDs...) }

// SupportedDisplayVersions returns a copy of the accepted display versions.
func SupportedDisplayVersions() []string { return append([]string(nil), supportedDisplayVersions...) }

// IsSupportedTitleID reports whether id is an accepted build target.
func IsSupportedTitleID(id string) bool {
	for _, s := range supportedTitleIDs {
		if s == id {
			return true
		}
	}
	return false
}

// IsSupportedDisplayVersion reports whether v is an accepted display version.
func IsSupportedDisplayVersion(v string) bool {
	for _, s := range supportedDisplayVersions {
		if s == v {
			return true
		}
	}
	return false
}

// SourceExpectation is the caller-declared identity a source CPK must satisfy
// for a build to proceed. Every field is optional; an empty/zero field is simply
// not asserted. TitleID and DisplayVersion are caller-supplied metadata because a
// bare CPK carries no title/version record of its own.
type SourceExpectation struct {
	TitleID          string
	DisplayVersion   string
	SHA256           string
	Size             int64
	Members          int
	FMSMembers       int
	MemberListSHA256 string
}

// SourceFacts is the verified description of a source CPK. It is evidence only:
// obtaining it never modifies the archive.
type SourceFacts struct {
	Path             string
	Size             int64
	SHA256           string
	Members          int
	FMSMembers       int
	MemberListSHA256 string
	Align            int64
	ContentOffset    int64
	EtocOffset       int64
}

// ValidateSource hashes and structurally validates a source CPK and applies
// every declared expectation. It rejects a missing file, a non-CPK file, an
// unsupported title ID or display version, a size/SHA mismatch and a member
// count mismatch before any mutation can occur. The archive is opened read-only
// and only its header/TOC are parsed; member data is never read here.
func ValidateSource(path string, exp SourceExpectation) (*SourceFacts, error) {
	if path == "" {
		return nil, fmt.Errorf("build: source CPK path is empty")
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("build: source CPK not found: %s", paths.Display(path))
		}
		return nil, fmt.Errorf("build: source CPK %s: %s", paths.Display(path), paths.Error(err))
	}
	if info.IsDir() {
		return nil, fmt.Errorf("build: source CPK %q is a directory", paths.Display(path))
	}

	if exp.TitleID != "" && !IsSupportedTitleID(exp.TitleID) {
		return nil, fmt.Errorf("build: unsupported TitleID %q (supported: %s)",
			exp.TitleID, strings.Join(supportedTitleIDs, ", "))
	}
	if exp.DisplayVersion != "" && !IsSupportedDisplayVersion(exp.DisplayVersion) {
		return nil, fmt.Errorf("build: unsupported display version %q (supported: %s)",
			exp.DisplayVersion, strings.Join(supportedDisplayVersions, ", "))
	}

	sum, size, err := hashFileSHA256(path)
	if err != nil {
		return nil, fmt.Errorf("build: hashing source CPK %s: %s", paths.Display(path), paths.Error(err))
	}
	if exp.SHA256 != "" && !strings.EqualFold(exp.SHA256, sum) {
		return nil, fmt.Errorf("build: source SHA-256 mismatch: file %s, expected %s", sum, exp.SHA256)
	}
	if exp.Size != 0 && exp.Size != size {
		return nil, fmt.Errorf("build: source size mismatch: file %d bytes, expected %d", size, exp.Size)
	}

	a, err := cpk.OpenFile(path)
	if err != nil {
		return nil, fmt.Errorf("build: source is not a supported CRI CPK: %w", err)
	}
	defer a.Close()

	if len(a.Entries) == 0 {
		return nil, fmt.Errorf("build: source CPK has no members")
	}
	paths := make([]string, len(a.Entries))
	fms := 0
	for i, e := range a.Entries {
		p := e.Path()
		paths[i] = p
		if strings.HasSuffix(strings.ToLower(p), ".fms") {
			fms++
		}
	}
	sort.Strings(paths)
	listSHA := memberListDigest(paths)

	if exp.Members != 0 && exp.Members != len(paths) {
		return nil, fmt.Errorf("build: source member count mismatch: %d members, expected %d", len(paths), exp.Members)
	}
	if exp.FMSMembers != 0 && exp.FMSMembers != fms {
		return nil, fmt.Errorf("build: source FMS member count mismatch: %d, expected %d", fms, exp.FMSMembers)
	}
	if exp.MemberListSHA256 != "" && !strings.EqualFold(exp.MemberListSHA256, listSHA) {
		return nil, fmt.Errorf("build: source member-list identity mismatch: computed %s, expected %s (the CPK member set differs; expected %d members, %d .fms)",
			listSHA, exp.MemberListSHA256, len(paths), fms)
	}

	return &SourceFacts{
		Path:             path,
		Size:             size,
		SHA256:           sum,
		Members:          len(paths),
		FMSMembers:       fms,
		MemberListSHA256: listSHA,
		Align:            a.Header.Align,
		ContentOffset:    a.ContentOffset(),
		EtocOffset:       a.EtocOffset(),
	}, nil
}

// memberListDigest is the stable member-list identity for the source CPK. It is
// shared with the extraction cache through internal/cpk so a cached archive can
// be recognised by the same digest a build computes.
func memberListDigest(paths []string) string { return cpk.MemberListDigest(paths) }

func hashFileSHA256(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
