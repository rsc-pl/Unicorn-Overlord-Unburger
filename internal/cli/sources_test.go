package cli

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"uo-unburger/internal/appconfig"
	"uo-unburger/internal/sourcecache"
)

// ---- fixtures --------------------------------------------------------------

func makeProjectRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "translations"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "translations", "honorifics.tsv"), []byte("record_id\tenglish_text\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("UO_UNBURGER_ROOT", root)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	return root
}

// writeTestNSP writes a minimal PFS0 container carrying a plaintext CNMT XML.
func writeTestNSP(t *testing.T, dir, name, typ, id string, version uint32, original string) string {
	t.Helper()
	xml := "<?xml version=\"1.0\"?>\n<ContentMeta><Type>" + typ + "</Type><Id>" + id + "</Id><Version>" +
		itoaCLI(version) + "</Version>"
	if original != "" {
		xml += "<OriginalId>" + original + "</OriginalId>"
	}
	xml += "<Content><Type>Program</Type><Id>aa</Id><Size>1</Size><Hash>bb</Hash></Content></ContentMeta>\n"
	members := []struct {
		name string
		data []byte
	}{
		{"aaa.nca", []byte("nca")},
		{"bbbb.cnmt.xml", []byte(xml)},
	}
	var strTab []byte
	offs := make([]uint32, len(members))
	for i, m := range members {
		offs[i] = uint32(len(strTab))
		strTab = append(strTab, m.name...)
		strTab = append(strTab, 0)
	}
	dataStart := 0x10 + len(members)*0x18 + len(strTab)
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
	copy(out[dataStart-len(strTab):], strTab)
	for _, m := range members {
		out = append(out, m.data...)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func itoaCLI(v uint32) string {
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

// installFakeExtractor puts a fake nstool on PATH and returns its directory.
func installFakeExtractor(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"if [ -z \"$UO_EXTRACT_OUT\" ]; then echo 'NSTool v1.9.2'; echo 'xci'; exit 0; fi\n" +
		"mkdir -p \"$UO_EXTRACT_OUT/romfs\"\n" +
		"printf 'CPK' > \"$UO_EXTRACT_OUT/romfs/Unicorn_US.CPK\"\n"
	if err := os.WriteFile(filepath.Join(dir, "nstool"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return dir
}

// ---- sources command -------------------------------------------------------

func TestSourcesIsReadOnlyAndReportsEnvironment(t *testing.T) {
	root := makeProjectRoot(t)
	updateDir := filepath.Join(root, ".local", "roms", "update")
	if err := os.MkdirAll(updateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestNSP(t, updateDir, "Update.nsp", "Patch", "0x010069401adb8800", 262144, "0x010069401adb8000")
	keys := filepath.Join(root, ".local", "keys")
	if err := os.MkdirAll(keys, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(keys, "prod.keys"), []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	installFakeExtractor(t)

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"sources"}, &stdout, &stderr); code != 0 {
		t.Fatalf("sources exit = %d, stderr=%q", code, stderr.String())
	}
	got := stdout.String()
	for _, want := range []string{"project root:", "recognised", "010069401ADB8800", "prod.keys present", "extractor:", "nstool", "no entry for"} {
		if !strings.Contains(got, want) {
			t.Fatalf("sources output missing %q:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "read-only") {
		t.Fatalf("sources must state that it is read-only:\n%s", got)
	}
	// No cache entry and no scratch must have been created by a read-only command.
	if _, err := os.Stat(filepath.Join(root, ".local", "cache")); !os.IsNotExist(err) {
		t.Fatalf("sources must not create the cache (stat err = %v)", err)
	}
	if entries, err := os.ReadDir(filepath.Join(root, ".local", "work", "extract")); err == nil && len(entries) > 0 {
		t.Fatalf("sources left scratch entries: %v", entries)
	}
}

func TestSourcesWithoutRootFails(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	missing := filepath.Join(t.TempDir(), "absent")
	t.Setenv("UO_UNBURGER_ROOT", missing)
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"sources"}, &stdout, &stderr); code != 1 {
		t.Fatalf("sources exit = %d, want 1 (stderr=%q)", code, stderr.String())
	}
}

// ---- acquireSource ---------------------------------------------------------

func TestAcquireSourceExplicitAndConfigured(t *testing.T) {
	makeProjectRoot(t)
	if p, prov, err := acquireSource(context.Background(), appconfig.Default(), "/games/explicit.cpk", false); err != nil || p != "/games/explicit.cpk" || prov != "flag" {
		t.Fatalf("explicit = (%q, %q, %v)", p, prov, err)
	}
	configured := filepath.Join(t.TempDir(), "configured.cpk")
	if err := os.WriteFile(configured, []byte("cpk"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := appconfig.Default()
	cfg.SourceCPK = configured
	if p, prov, err := acquireSource(context.Background(), cfg, "", false); err != nil || p != configured || prov != "configured" {
		t.Fatalf("configured = (%q, %q, %v)", p, prov, err)
	}
}

func TestAcquireSourceStaleConfiguredFallsThrough(t *testing.T) {
	makeProjectRoot(t)
	cfg := appconfig.Default()
	cfg.SourceCPK = filepath.Join(t.TempDir(), "gone.cpk")
	_, _, err := acquireSource(context.Background(), cfg, "", false)
	if err == nil {
		t.Fatal("a stale configured source with no container must fail")
	}
	if !strings.Contains(err.Error(), "no longer exists") {
		t.Fatalf("the error should explain the stale configuration: %v", err)
	}
}

func TestAcquireSourceRequiresExplicitExtractionWhenCacheMissing(t *testing.T) {
	root := makeProjectRoot(t)
	updateDir := filepath.Join(root, ".local", "roms", "update")
	if err := os.MkdirAll(updateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestNSP(t, updateDir, "Update.nsp", "Patch", "0x010069401adb8800", 262144, "0x010069401adb8000")

	_, _, err := acquireSource(context.Background(), appconfig.Default(), "", false)
	if err == nil || !strings.Contains(err.Error(), "--extract") {
		t.Fatalf("a missing cache must require --extract, got %v", err)
	}
	// Nothing may have been extracted or cached.
	if _, statErr := os.Stat(filepath.Join(root, ".local", "cache")); !os.IsNotExist(statErr) {
		t.Fatalf("no cache entry may be created without --extract (stat err = %v)", statErr)
	}
}

func TestAcquireSourceRejectsUnverifiedCacheEntry(t *testing.T) {
	root := makeProjectRoot(t)
	updateDir := filepath.Join(root, ".local", "roms", "update")
	if err := os.MkdirAll(updateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	nsp := writeTestNSP(t, updateDir, "Update.nsp", "Patch", "0x010069401adb8800", 262144, "0x010069401adb8000")
	container, err := sourcecache.Inspect(nsp, sourcecache.RoleUpdate)
	if err != nil {
		t.Fatal(err)
	}
	cacheRoot := filepath.Join(root, ".local", "cache")
	src := filepath.Join(root, "archive.bin")
	if err := os.WriteFile(src, []byte("cached-archive-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := sourcecache.Commit(sourcecache.CommitRequest{
		CacheRoot: cacheRoot, Container: *container, CPKPath: src, Now: time.Unix(1700000000, 0),
	}); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	_, _, err = acquireSource(context.Background(), appconfig.Default(), "", false)
	if err == nil || !strings.Contains(err.Error(), "--extract") {
		t.Fatalf("an unverifiable cache entry must not be adopted; got %v", err)
	}
}

func TestAcquireSourceNoContainerExplains(t *testing.T) {
	makeProjectRoot(t)
	_, _, err := acquireSource(context.Background(), appconfig.Default(), "", false)
	if err == nil || !strings.Contains(err.Error(), "no source archive") {
		t.Fatalf("err = %v", err)
	}
}
