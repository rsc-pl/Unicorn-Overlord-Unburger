package build

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"uo-unburger/internal/sourcecache"
)

// These tests prove the extraction cache reuses only an archive whose member
// identity still matches, and quarantines (never deletes) a damaged entry. The
// cache is exercised with a real synthetic CPK through the build package's
// fixture, so the member-list check is the same code a build uses.

func cacheContainer(path string) sourcecache.Container {
	return sourcecache.Container{
		Path: path, Format: sourcecache.FormatNSP, Role: sourcecache.RoleUpdate,
		TitleID: "010069401ADB8800", RawVersion: 262144, MetaType: "Patch", Parsed: true,
	}
}

func TestCacheCommitReuseAndMemberIdentity(t *testing.T) {
	dir := t.TempDir()
	src, _, _ := writeCPK(t, dir, fixExtract(standardMembers()))
	c := cacheContainer(src)

	facts, err := ValidateSource(src, SourceExpectation{})
	if err != nil {
		t.Fatalf("ValidateSource: %v", err)
	}
	art := sourcecache.Artifact{
		Name: sourcecache.CPKMemberName, Size: facts.Size, SHA256: facts.SHA256,
		Members: facts.Members, FMSMembers: facts.FMSMembers, MemberListSHA256: facts.MemberListSHA256,
	}
	cacheRoot := filepath.Join(dir, "cache")
	m, entry, err := sourcecache.Commit(sourcecache.CommitRequest{
		CacheRoot: cacheRoot, Container: c, CPKPath: src, CPK: art,
	})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if m.CPK.Members != len(standardMembers()) {
		t.Fatalf("cached member count = %d", m.CPK.Members)
	}

	// A valid entry is reused, and the cached archive verifies by member list.
	m2, dir2, ok, err := sourcecache.Reuse(cacheRoot, c, time.Now())
	if !ok || err != nil {
		t.Fatalf("Reuse = ok %v err %v", ok, err)
	}
	if dir2 != entry || m2.CPK.MemberListSHA256 != facts.MemberListSHA256 {
		t.Fatalf("reused entry = %q vs %q", dir2, entry)
	}

	// A member-list mismatch is rejected even though size and SHA match.
	tampered := m2.CPK
	tampered.MemberListSHA256 = "0000000000000000000000000000000000000000000000000000000000000000"
	if err := sourcecache.Verify(filepath.Join(entry, m2.CPK.Name), tampered); err == nil {
		t.Fatal("a member-list identity mismatch must be rejected")
	}

	// Overwriting the cached archive with a different member set damages the entry;
	// reuse must fail and quarantine it.
	other, _, otherBytes := writeCPK(t, dir, fixExtract([]fxMember{
		{dir: "MsgSheet", name: "Ucsys.fms", stored: fxFMS([]string{"one"}), extract: -1},
		{dir: "", name: "root.bin", stored: []byte("different"), extract: -1},
	}))
	_ = other
	if err := os.WriteFile(filepath.Join(entry, m2.CPK.Name), otherBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, err := sourcecache.Reuse(cacheRoot, c, time.Unix(1700000000, 0)); ok || err == nil {
		t.Fatalf("a damaged entry must not be reused (ok=%v err=%v)", ok, err)
	}
	if _, err := os.Stat(entry); !os.IsNotExist(err) {
		t.Fatalf("the damaged entry should be moved aside (stat err = %v)", err)
	}
}
