package sourcecache

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---- synthetic fixtures ----------------------------------------------------

// nspMember is one file inside a synthetic NSP.
type nspMember struct {
	name string
	data []byte
}

// buildNSP writes a minimal, self-consistent PFS0 container.
func buildNSP(t *testing.T, members []nspMember) []byte {
	t.Helper()
	var strTab []byte
	offs := make([]uint32, len(members))
	for i, m := range members {
		offs[i] = uint32(len(strTab))
		strTab = append(strTab, m.name...)
		strTab = append(strTab, 0)
	}
	entryEnd := 0x10 + len(members)*0x18
	dataStart := entryEnd + len(strTab)
	out := make([]byte, dataStart)
	copy(out[0:4], "PFS0")
	binary.LittleEndian.PutUint32(out[4:8], uint32(len(members)))
	binary.LittleEndian.PutUint32(out[8:12], uint32(len(strTab)))
	cursor := 0
	for i, m := range members {
		base := 0x10 + i*0x18
		binary.LittleEndian.PutUint64(out[base:base+8], uint64(cursor))
		binary.LittleEndian.PutUint64(out[base+8:base+16], uint64(len(m.data)))
		binary.LittleEndian.PutUint32(out[base+16:base+20], offs[i])
		cursor += len(m.data)
	}
	copy(out[entryEnd:], strTab)
	for _, m := range members {
		out = append(out, m.data...)
	}
	return out
}

func writeNSP(t *testing.T, dir, name string, members []nspMember) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, buildNSP(t, members), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func makeCNMT(typ, id string, version uint32, original string) []byte {
	s := "<?xml version=\"1.0\" encoding=\"utf-8\"?>\n<ContentMeta>\n  <Type>" + typ + "</Type>\n" +
		"  <Id>" + id + "</Id>\n  <Version>" + itoa(version) + "</Version>\n"
	if original != "" {
		s += "  <OriginalId>" + original + "</OriginalId>\n"
	}
	s += "  <Content><Type>Program</Type><Id>aa</Id><Size>5</Size><Hash>bb</Hash></Content>\n</ContentMeta>\n"
	return []byte(s)
}

func itoa(v uint32) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	return string(b)
}

// ---- PFS0 / CNMT -----------------------------------------------------------

func TestParsePFS0AndEmbeddedCNMT(t *testing.T) {
	dir := t.TempDir()
	path := writeNSP(t, dir, "Base.nsp", []nspMember{
		{"aaa.nca", []byte("payload")},
		{"bbbb.cnmt.xml", makeCNMT("Application", "0x010069401adb8000", 0, "")},
	})
	p, err := OpenPFS0(path)
	if err != nil {
		t.Fatalf("OpenPFS0: %v", err)
	}
	if len(p.Members) != 2 || p.Member("bbbb.cnmt.xml") == nil {
		t.Fatalf("members = %+v", p.Members)
	}
	cnmt, _, err := EmbeddedCNMT(path)
	if err != nil {
		t.Fatalf("EmbeddedCNMT: %v", err)
	}
	if cnmt.TitleID != "010069401ADB8000" || cnmt.RawVersion != 0 || cnmt.Type != "Application" {
		t.Fatalf("cnmt = %+v", cnmt)
	}
}

func TestInspectUpdatePatchLinksOriginal(t *testing.T) {
	dir := t.TempDir()
	path := writeNSP(t, dir, "UPD.nsp", []nspMember{
		{"cc.cnmt.xml", makeCNMT("Patch", "0x010069401adb8800", 262144, "0x010069401adb8000")},
	})
	c, err := Inspect(path, RoleUpdate)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !c.Valid() || c.TitleID != "010069401ADB8800" || c.RawVersion != 262144 {
		t.Fatalf("container = %+v", c)
	}
	if c.MetaType != "Patch" || c.OriginalID != "010069401ADB8000" {
		t.Fatalf("patch identity = %+v", c)
	}
}

func TestInspectRejectsNonContainer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "random.bin")
	if err := os.WriteFile(path, []byte("not a container at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(path, RoleBase); err == nil {
		t.Fatal("a non-container must be rejected")
	}
}

func TestInspectXCIExplainsTheExtractorRequirement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "card.xci")
	head := make([]byte, 0x200)
	copy(head[0x100:0x104], "HEAD")
	if err := os.WriteFile(path, head, 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Inspect(path, RoleBase)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if c.Format != FormatXCI || c.Parsed {
		t.Fatalf("xci = %+v", c)
	}
	if !strings.Contains(c.Note, "extractor") {
		t.Fatalf("the XCI note must explain the extractor requirement: %q", c.Note)
	}
}

func TestDiscoverScansRolesAndPicksUpdate(t *testing.T) {
	base, update := t.TempDir(), t.TempDir()
	writeNSP(t, base, "base.nsp", []nspMember{{"a.cnmt.xml", makeCNMT("Application", "0x010069401adb8000", 0, "")}})
	writeNSP(t, update, "upd.nsp", []nspMember{{"b.cnmt.xml", makeCNMT("Patch", "0x010069401adb8800", 262144, "0x010069401adb8000")}})
	d := Discover(DiscoverOptions{BaseDir: base, UpdateDir: update})
	if len(d.Containers) != 2 {
		t.Fatalf("containers = %+v", d.Containers)
	}
	got := d.Pick("010069401ADB8000")
	if got == nil || got.TitleID != "010069401ADB8800" {
		t.Fatalf("Pick must prefer the update patch, got %+v", got)
	}
	if d.Find("010069401ADB8000") == nil {
		t.Fatal("Find must locate the base application")
	}
}

// TestPickIgnoresUnlinkedPatchAndUnknownTitle: a patch that does not link to the
// requested base is not used, and an unknown target yields no source at all.
func TestPickIgnoresUnlinkedPatchAndUnknownTitle(t *testing.T) {
	base, update := t.TempDir(), t.TempDir()
	writeNSP(t, base, "base.nsp", []nspMember{{"a.cnmt.xml", makeCNMT("Application", "0x010069401adb8000", 0, "")}})
	writeNSP(t, update, "upd.nsp", []nspMember{{"b.cnmt.xml", makeCNMT("Patch", "0x010069401adb8800", 262144, "0x010099999adb8000")}})
	d := Discover(DiscoverOptions{BaseDir: base, UpdateDir: update})
	got := d.Pick("010069401ADB8000")
	if got == nil || got.TitleID != "010069401ADB8000" {
		t.Fatalf("an unlinked patch must be ignored; got %+v", got)
	}
	if p := d.Pick("0000000000000000"); p != nil {
		t.Fatalf("an unknown target must yield no source, got %+v", p)
	}
}

// ---- manifest / cache ------------------------------------------------------

func fakeContainer(t *testing.T, dir string) Container {
	t.Helper()
	path := writeNSP(t, dir, "src.nsp", []nspMember{{"a.cnmt.xml", makeCNMT("Patch", "0x010069401adb8800", 262144, "0x010069401adb8000")}})
	c, err := Inspect(path, RoleUpdate)
	if err != nil {
		t.Fatal(err)
	}
	return *c
}

func TestManifestRoundTripAndSchemaCheck(t *testing.T) {
	dir := t.TempDir()
	m := &Manifest{Schema: CacheSchema, TitleID: "010069401ADB8800", RawVersion: 262144, CPK: Artifact{Name: CPKMemberName, Size: 3, SHA256: "abc"}}
	if err := WriteManifest(dir, m); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	got, err := ReadManifest(dir)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if got.TitleID != m.TitleID || got.CPK.Size != 3 {
		t.Fatalf("round trip = %+v", got)
	}
	// A future schema is refused rather than misread.
	if err := os.WriteFile(filepath.Join(dir, ManifestFileName), []byte(`{"schema": 999}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManifest(dir); err == nil {
		t.Fatal("an unsupported manifest schema must be refused")
	}
}

func TestVerifySizeSHAAndBuildID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main")
	data := make([]byte, 0x60)
	copy(data[0x40:0x50], []byte{0xc8, 0x41, 0xff, 0xe2, 0x71, 0x7f, 0xf0, 0x3a, 0x13, 0x99, 0x04, 0x80, 0xc5, 0x1d, 0xa7, 0x3f})
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := DescribeMain(path)
	if err != nil {
		t.Fatalf("DescribeMain: %v", err)
	}
	if a.BuildID != "c841ffe2717ff03a13990480c51da73f" {
		t.Fatalf("build id = %q", a.BuildID)
	}
	if err := Verify(path, a); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	bad := a
	bad.BuildID = "00000000000000000000000000000000"
	if err := Verify(path, bad); err == nil {
		t.Fatal("a Build ID mismatch must be rejected")
	}
	// A size-only identity for a non-CPK file works too.
	small, err := DescribeMain(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(path, Artifact{Name: small.Name, Size: small.Size, SHA256: small.SHA256}); err != nil {
		t.Fatalf("Verify size+SHA only: %v", err)
	}
	if err := Verify(path, Artifact{Name: small.Name, Size: small.Size + 1, SHA256: small.SHA256}); err == nil {
		t.Fatal("a size mismatch must be rejected")
	}
}

func TestQuarantineMovesAndNeverDeletes(t *testing.T) {
	cacheRoot := t.TempDir()
	entry := filepath.Join(cacheRoot, "010069401ADB8800-262144")
	if err := os.MkdirAll(entry, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(entry, "Unicorn_US.CPK"), []byte("broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0)
	dest, err := Quarantine(cacheRoot, entry, now)
	if err != nil {
		t.Fatalf("Quarantine: %v", err)
	}
	if _, err := os.Stat(entry); !os.IsNotExist(err) {
		t.Fatalf("the entry should have been moved aside, stat err = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dest, "Unicorn_US.CPK"))
	if err != nil || string(data) != "broken" {
		t.Fatalf("quarantined content lost: %v", err)
	}
}

func TestCommitAndFailedCommitLeavesNoEntry(t *testing.T) {
	dir := t.TempDir()
	c := fakeContainer(t, dir)
	cacheRoot := filepath.Join(dir, "cache")

	// A missing artifact must not create an entry.
	if _, _, err := Commit(CommitRequest{CacheRoot: cacheRoot, Container: c, CPKPath: filepath.Join(dir, "nope.bin")}); err == nil {
		t.Fatal("committing a missing artifact must fail")
	}
	if _, err := os.Stat(EntryDir(cacheRoot, c.TitleID, c.RawVersion)); !os.IsNotExist(err) {
		t.Fatalf("a failed commit left a cache entry (stat err = %v)", err)
	}
}

func TestCommitReuseAndCorruptRecovery(t *testing.T) {
	dir := t.TempDir()
	c := fakeContainer(t, dir)
	cacheRoot := filepath.Join(dir, "cache")
	src := filepath.Join(dir, "Unicorn_US.src")
	if err := os.WriteFile(src, []byte("CPKDATA-version-one"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, entry, err := Commit(CommitRequest{CacheRoot: cacheRoot, Container: c, CPKPath: src, Now: time.Unix(1700000000, 0)})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if m.TitleID != c.TitleID || m.Source.Size == 0 {
		t.Fatalf("manifest = %+v", m)
	}

	// A valid entry is reused.
	if _, _, ok, err := Reuse(cacheRoot, c, time.Now()); !ok || err != nil {
		t.Fatalf("Reuse = ok %v err %v", ok, err)
	}

	// Corrupt the cached archive; reuse must fail and quarantine the entry.
	if err := os.WriteFile(filepath.Join(entry, CPKMemberName), []byte("CPKDATA-version-two"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, err := Reuse(cacheRoot, c, time.Unix(1700000100, 0)); ok || err == nil {
		t.Fatalf("a damaged entry must not be reused (ok=%v err=%v)", ok, err)
	}
	if _, err := os.Stat(entry); !os.IsNotExist(err) {
		t.Fatalf("the damaged entry should have been moved aside (stat err = %v)", err)
	}
	if entries, err := os.ReadDir(filepath.Join(cacheRoot, quarantineDirName)); err != nil || len(entries) == 0 {
		t.Fatalf("a quarantine copy should exist: %v (%d entries)", err, len(entries))
	}

	// Re-extraction and commit recover cleanly.
	if _, _, err := Commit(CommitRequest{CacheRoot: cacheRoot, Container: c, CPKPath: src, Now: time.Unix(1700000200, 0)}); err != nil {
		t.Fatalf("re-commit: %v", err)
	}
	if _, _, ok, err := Reuse(cacheRoot, c, time.Now()); !ok || err != nil {
		t.Fatalf("reuse after recovery = ok %v err %v", ok, err)
	}
}

func TestFingerprintIsStableAndDistinct(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.bin")
	b := filepath.Join(dir, "b.bin")
	if err := os.WriteFile(a, bytes.Repeat([]byte{1}, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, bytes.Repeat([]byte{2}, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	fa, err := FingerprintFile(a)
	if err != nil {
		t.Fatal(err)
	}
	fa2, _ := FingerprintFile(a)
	if fa != fa2 {
		t.Fatalf("fingerprint is not stable: %+v vs %+v", fa, fa2)
	}
	fb, _ := FingerprintFile(b)
	if fa.HeadSHA256 == fb.HeadSHA256 {
		t.Fatal("different content must fingerprint differently")
	}
}
