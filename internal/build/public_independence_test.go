package build

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"uo-unburger/internal/importer"
)

// These tests prove the public, corpus-free path: a build resolves the
// translation ledger directly against the user's verified source CPK, applies
// the canonical and sparse alternate editions exactly, preserves every
// untouched member, and refuses a stale or malformed ledger. No reference
// corpus or other optional input is involved anywhere here.

func writeNeutralTranslations(t *testing.T, dir string, canonRows, altRows []string) {
	t.Helper()
	canon := "record_id\tsource_jp_sha\toriginal_en_sha\tenglish_text\n"
	if len(canonRows) > 0 {
		canon += strings.Join(canonRows, "\n") + "\n"
	}
	mustWrite(t, filepath.Join(dir, "honorifics.tsv"), []byte(canon))
	if altRows != nil {
		alt := "record_id\tcanonical_sha256\tenglish_text\n"
		if len(altRows) > 0 {
			alt += strings.Join(altRows, "\n") + "\n"
		}
		mustWrite(t, filepath.Join(dir, "no-honorifics.tsv"), []byte(alt))
	}
}

// neutralCanonRow builds a canonical row; the JP hash is provenance only in the
// source-CPK mode and is a fixed placeholder here.
func neutralCanonRow(id, en, text string) string {
	return id + "\t" + sha1Short("jp-provenance") + "\t" + sha1Short(en) + "\t" + text
}

func neutralAltRow(id, canonText, text string) string {
	return id + "\t" + sha256Full(canonText) + "\t" + text
}

func publicOptions(src, translationsDir, outDir string, variant Variant) Options {
	return Options{
		Variant:         variant,
		SourcePath:      src,
		TranslationsDir: translationsDir,
		OutDir:          outDir,
		RunName:         "public-run",
		TitleID:         "010069401ADB8000",
		Expect:          SourceExpectation{SHA256: sha256BytesOf(src)},
	}
}

func TestPublicBuildUsesSourceCPKWithoutCorpus(t *testing.T) {
	dir := t.TempDir()
	src, _, srcBytes := writeCPK(t, dir, fixExtract(standardMembers()))
	translations := filepath.Join(dir, "translations")
	canon := "New Journey"
	writeNeutralTranslations(t, translations,
		[]string{neutralCanonRow("UcSysMsg.fms:0", "New Game", canon)},
		[]string{neutralAltRow("UcSysMsg.fms:0", canon, "A New Journey")})

	res, err := Run(publicOptions(src, translations, filepath.Join(dir, "out"), VariantBoth))
	if err != nil {
		t.Fatalf("public build: %v", err)
	}
	if res.SourceMode != importer.ModeVerifiedSourceCPK {
		t.Fatalf("source mode = %q, want %q", res.SourceMode, importer.ModeVerifiedSourceCPK)
	}
	c := res.Resolution.Counts
	if c.Canonical != 1 || c.Overrides != 1 || c.Applied != 1 || c.OverridesApplied != 1 || c.Rejected != 0 {
		t.Fatalf("counts = %+v", c)
	}
	if c.OfficialVerified != 1 {
		t.Fatalf("original_en_sha was not verified against the source CPK: %+v", c)
	}
	if !res.Resolution.DiffPass {
		t.Fatalf("diff drift = %v", res.Resolution.DiffDrift)
	}
	if res.SourceMode == importer.ModeReferenceCorpus {
		t.Fatal("a corpus was never supplied, yet the corpus mode was reported")
	}

	hon := mustOpenCPK(t, readOut(t, res, VariantHonorifics))
	slots := memberSlots(t, hon, "MsgSheet/UcSysMsg.fms")
	if slots[0] != "New Journey" || slots[1] != "Continue" || slots[2] != "Load Game" {
		t.Fatalf("honorifics slots = %v", slots)
	}
	noHon := mustOpenCPK(t, readOut(t, res, VariantNoHonorifics))
	nslots := memberSlots(t, noHon, "MsgSheet/UcSysMsg.fms")
	if nslots[0] != "A New Journey" || nslots[1] != "Continue" {
		t.Fatalf("no-honorifics slots = %v", nslots)
	}

	// Untouched preservation: a member with no record must be byte-identical.
	srcA := mustOpenCPK(t, srcBytes)
	for _, member := range []string{"MsgSheet/Other.fms", "root.bin"} {
		se, de := srcA.Member(member), hon.Member(member)
		if se == nil || de == nil {
			t.Fatalf("member %q missing", member)
		}
		sr, err := srcA.RawData(se)
		if err != nil {
			t.Fatal(err)
		}
		dr, err := hon.RawData(de)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(sr, dr) {
			t.Fatalf("untouched member %q changed", member)
		}
	}
}

func TestPublicBuildRejectsStaleOriginalEN(t *testing.T) {
	dir := t.TempDir()
	src, _, _ := writeCPK(t, dir, fixExtract(standardMembers()))
	translations := filepath.Join(dir, "translations")
	// The original_en_sha describes a slot text the source archive does not hold.
	writeNeutralTranslations(t, translations,
		[]string{"UcSysMsg.fms:0\t" + sha1Short("jp-provenance") + "\t" + sha1Short("Some Older Text") + "\tNew Journey"},
		nil)

	_, err := Run(publicOptions(src, translations, filepath.Join(dir, "out"), VariantHonorifics))
	if err == nil {
		t.Fatal("a stale original_en_sha must be refused")
	}
	if !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("error should explain the rejection: %v", err)
	}
}

func TestPublicBuildRejectsUnknownTargets(t *testing.T) {
	dir := t.TempDir()
	src, _, _ := writeCPK(t, dir, fixExtract(standardMembers()))

	cases := []struct {
		name string
		row  string
		want string
	}{
		{"unknown member", neutralCanonRow("Nope.fms:0", "New Game", "New Journey"), "no such member"},
		{"slot outside member", neutralCanonRow("UcSysMsg.fms:99", "New Game", "New Journey"), "outside member"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			translations := filepath.Join(t.TempDir(), "translations")
			writeNeutralTranslations(t, translations, []string{tc.row}, nil)
			_, err := Run(publicOptions(src, translations, filepath.Join(dir, "out-"+tc.name), VariantHonorifics))
			if err == nil {
				t.Fatal("an unresolvable record target must be refused")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %v missing %q", err, tc.want)
			}
		})
	}
}

// TestPublicSparseDiffExact: the two editions differ at exactly the sparse
// override slots and nowhere else.
func TestPublicSparseDiffExact(t *testing.T) {
	dir := t.TempDir()
	src, _, _ := writeCPK(t, dir, fixExtract(standardMembers()))
	translations := filepath.Join(dir, "translations")
	canon := "New Journey"
	writeNeutralTranslations(t, translations,
		[]string{
			neutralCanonRow("UcSysMsg.fms:0", "New Game", canon),
			neutralCanonRow("UcSysMsg.fms:1", "Continue", "Carry On"),
		},
		[]string{neutralAltRow("UcSysMsg.fms:0", canon, "A New Journey")})

	res, err := Run(publicOptions(src, translations, filepath.Join(dir, "out"), VariantBoth))
	if err != nil {
		t.Fatalf("public build: %v", err)
	}
	hon := mustOpenCPK(t, readOut(t, res, VariantHonorifics))
	noHon := mustOpenCPK(t, readOut(t, res, VariantNoHonorifics))
	if len(hon.Entries) != len(noHon.Entries) {
		t.Fatalf("member counts differ: %d vs %d", len(hon.Entries), len(noHon.Entries))
	}
	diffMembers := 0
	for _, se := range hon.Entries {
		de := noHon.Member(se.Path())
		if de == nil {
			t.Fatalf("member %q missing from the alternate edition", se.Path())
		}
		sr, err := hon.RawData(se)
		if err != nil {
			t.Fatal(err)
		}
		dr, err := noHon.RawData(de)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(sr, dr) {
			continue
		}
		diffMembers++
		if se.Path() != "MsgSheet/UcSysMsg.fms" {
			t.Fatalf("unexpected member differs between editions: %q", se.Path())
		}
		hs := memberSlots(t, hon, se.Path())
		ns := memberSlots(t, noHon, se.Path())
		for i := range hs {
			if hs[i] != ns[i] && i != 0 {
				t.Fatalf("slot %d differs between editions (%q vs %q) but is not a sparse override", i, hs[i], ns[i])
			}
		}
		if hs[0] == ns[0] {
			t.Fatalf("the sparse override slot did not differ between editions")
		}
	}
	if diffMembers != 1 {
		t.Fatalf("editions differ in %d members, want exactly 1", diffMembers)
	}
}

// TestPublicBuildLaterUpdateWithoutCodeChanges: adding rows to the translation
// tables is enough to extend a build; no code change and no revision number is
// involved.
func TestPublicBuildLaterUpdateWithoutCodeChanges(t *testing.T) {
	dir := t.TempDir()
	src, _, _ := writeCPK(t, dir, fixExtract(standardMembers()))
	translations := filepath.Join(dir, "translations")
	writeNeutralTranslations(t, translations,
		[]string{neutralCanonRow("UcSysMsg.fms:0", "New Game", "New Journey")}, nil)

	first, err := Run(publicOptions(src, translations, filepath.Join(dir, "out1"), VariantHonorifics))
	if err != nil {
		t.Fatalf("first build: %v", err)
	}
	if first.Resolution.Counts.Applied != 1 {
		t.Fatalf("first build applied = %d, want 1", first.Resolution.Counts.Applied)
	}

	// A later revision adds one more canonical row to the same table.
	writeNeutralTranslations(t, translations,
		[]string{
			neutralCanonRow("UcSysMsg.fms:0", "New Game", "New Journey"),
			neutralCanonRow("UcSysMsg.fms:1", "Continue", "Carry On"),
		}, nil)

	opts := publicOptions(src, translations, filepath.Join(dir, "out2"), VariantHonorifics)
	opts.RunName = "public-run-2"
	second, err := Run(opts)
	if err != nil {
		t.Fatalf("second build: %v", err)
	}
	if second.Resolution.Counts.Applied != 2 {
		t.Fatalf("second build applied = %d, want 2", second.Resolution.Counts.Applied)
	}
	a := mustOpenCPK(t, readOut(t, second, VariantHonorifics))
	slots := memberSlots(t, a, "MsgSheet/UcSysMsg.fms")
	if slots[0] != "New Journey" || slots[1] != "Carry On" {
		t.Fatalf("later rows were not applied: %v", slots)
	}
}

// TestLedgerOnlyCheckWithoutAnySource: the ledger validates on its own and
// reports that source identity was not verified.
func TestLedgerOnlyCheckWithoutAnySource(t *testing.T) {
	dir := t.TempDir()
	translations := filepath.Join(dir, "translations")
	canon := "New Journey"
	writeNeutralTranslations(t, translations,
		[]string{neutralCanonRow("UcSysMsg.fms:0", "New Game", canon)},
		[]string{neutralAltRow("UcSysMsg.fms:0", canon, "A New Journey")})

	res, err := importer.CheckLedger(translations)
	if err != nil {
		t.Fatalf("CheckLedger: %v", err)
	}
	if res.SourceMode != importer.ModeLedgerOnly {
		t.Fatalf("source mode = %q", res.SourceMode)
	}
	if res.Counts.OfficialVerified != 0 {
		t.Fatalf("ledger-only mode must not claim verified official English")
	}
	if res.Counts.Applied != 1 || res.Counts.OverridesApplied != 1 || res.Counts.Rejected != 0 {
		t.Fatalf("counts = %+v", res.Counts)
	}
	if !res.DiffPass {
		t.Fatalf("diff drift = %v", res.DiffDrift)
	}
}

// TestLedgerOnlyRejectsMalformedAndMissingTables pins the structural refusals
// that the public path must keep.
func TestLedgerOnlyRejectsMalformedAndMissingTables(t *testing.T) {
	empty := filepath.Join(t.TempDir(), "missing")
	if _, err := importer.CheckLedger(empty); err == nil {
		t.Fatal("a missing translation directory must be refused")
	}

	dir := t.TempDir()
	translations := filepath.Join(dir, "translations")
	writeNeutralTranslations(t, translations,
		[]string{"UcSysMsg.fms:0\tNOT_HEX_12\t" + sha1Short("New Game") + "\tNew Journey"}, nil)
	if _, err := importer.CheckLedger(translations); err == nil {
		t.Fatal("a malformed hash must be refused")
	}

	badHeader := filepath.Join(dir, "bad")
	mustWrite(t, filepath.Join(badHeader, "honorifics.tsv"), []byte("record_id\tenglish_text\n"))
	if _, err := importer.CheckLedger(badHeader); err == nil {
		t.Fatal("an unexpected header must be refused")
	}
}

// TestLedgerOnlyRejectsOrphanAndRedundantOverrides: the exact-diff rule still
// holds without any source text.
func TestLedgerOnlyRejectsOrphanAndRedundantOverrides(t *testing.T) {
	dir := t.TempDir()
	translations := filepath.Join(dir, "translations")
	canon := "New Journey"
	// An orphan override has no canonical row; a redundant override equals the
	// canonical text.
	writeNeutralTranslations(t, translations,
		[]string{neutralCanonRow("UcSysMsg.fms:0", "New Game", canon)},
		[]string{
			neutralAltRow("UcSysMsg.fms:5", canon, "Orphan"),
			neutralAltRow("UcSysMsg.fms:0", canon, canon),
		})
	res, err := importer.CheckLedger(translations)
	if err != nil {
		t.Fatalf("CheckLedger: %v", err)
	}
	if res.Counts.Rejected != 2 {
		t.Fatalf("rejected = %d, want 2 (orphan + redundant)", res.Counts.Rejected)
	}
	if res.DiffPass {
		t.Fatal("rejected overrides must fail the exact-diff verification")
	}
	if res.Counts.OverridesApplied != 0 {
		t.Fatalf("overrides applied = %d, want 0", res.Counts.OverridesApplied)
	}
}

// TestPublicSourceSlotsAreReadReadOnly: the source archive is not modified by a
// public build.
func TestPublicSourceSlotsAreReadReadOnly(t *testing.T) {
	dir := t.TempDir()
	src, _, srcBytes := writeCPK(t, dir, fixExtract(standardMembers()))
	translations := filepath.Join(dir, "translations")
	writeNeutralTranslations(t, translations,
		[]string{neutralCanonRow("UcSysMsg.fms:0", "New Game", "New Journey")},
		[]string{neutralAltRow("UcSysMsg.fms:0", "New Journey", "A New Journey")})

	if _, err := Run(publicOptions(src, translations, filepath.Join(dir, "out"), VariantBoth)); err != nil {
		t.Fatalf("public build: %v", err)
	}
	got, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, srcBytes) {
		t.Fatal("the source archive was modified by the build")
	}
}
