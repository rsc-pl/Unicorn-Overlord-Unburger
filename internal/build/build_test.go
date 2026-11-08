package build

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"uo-unburger/internal/cpk"
	"uo-unburger/internal/fms"
	"uo-unburger/internal/titlefooter"
)

// ---------- helpers ----------

func sha1Short(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}

func sha256Full(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func sha256Bytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func mustWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeCPK writes a synthetic source CPK and returns its path and SHA-256.
func writeCPK(t *testing.T, dir string, members []fxMember) (string, string, []byte) {
	t.Helper()
	blob := fxCPK(t, members)
	path := filepath.Join(dir, "Unicorn_US.CPK")
	mustWrite(t, path, blob)
	return path, sha256Bytes(blob), blob
}

// writeCorpus writes a corpus JSONL file with (id, jp, en) rows.
func writeCorpus(t *testing.T, dir string, rows [][3]string) string {
	t.Helper()
	var b strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&b, "{\"record_id\":%q,\"jp\":%q,\"en\":%q}\n", r[0], r[1], r[2])
	}
	path := filepath.Join(dir, "corpus.jsonl")
	mustWrite(t, path, []byte(b.String()))
	return path
}

func writeTranslations(t *testing.T, dir, name, content string) {
	t.Helper()
	mustWrite(t, filepath.Join(dir, name), []byte(content))
}

func mustOpenCPK(t *testing.T, data []byte) *cpk.Archive {
	t.Helper()
	a, err := cpk.Open(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("open rebuilt: %v", err)
	}
	return a
}

func memberSlots(t *testing.T, a *cpk.Archive, member string) []string {
	t.Helper()
	e := a.Member(member)
	if e == nil {
		t.Fatalf("member %q missing", member)
	}
	data, err := a.Data(e)
	if err != nil {
		t.Fatalf("data %q: %v", member, err)
	}
	sheet, err := fms.Parse(data)
	if err != nil {
		t.Fatalf("parse %q: %v", member, err)
	}
	out := make([]string, sheet.SlotCount())
	for i := range out {
		out[i] = string(sheet.Slot(i))
	}
	return out
}

func readOut(t *testing.T, res *Result, variant Variant) []byte {
	t.Helper()
	for _, e := range res.Editions {
		if e.Variant == variant {
			data, err := os.ReadFile(e.ArtifactPath)
			if err != nil {
				t.Fatalf("read artifact: %v", err)
			}
			return data
		}
	}
	t.Fatalf("edition %s not in result", variant)
	return nil
}

// baseOptions returns build options wired to a fixture with an explicit run name.
func baseOptions(sourcePath, corpusPath, translationsDir, outDir string, variant Variant) Options {
	return Options{
		Variant:         variant,
		SourcePath:      sourcePath,
		CorpusPath:      corpusPath,
		TranslationsDir: translationsDir,
		OutDir:          outDir,
		RunName:         "test-run",
		TitleID:         "010069401ADB8000",
		Expect:          SourceExpectation{SHA256: sha256BytesOf(sourcePath)},
	}
}

// sha256BytesOf hashes a file already on disk.
func sha256BytesOf(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return sha256Bytes(data)
}

func standardMembers() []fxMember {
	return []fxMember{
		{dir: "MsgSheet", name: "UcSysMsg.fms", stored: fxFMS([]string{"New Game", "Continue", "Load Game"}), extract: -1},
		{dir: "MsgSheet", name: "Other.fms", stored: fxFMS([]string{"alpha", "beta"}), extract: -1},
		{dir: "", name: "root.bin", stored: []byte("root payload"), extract: -1},
	}
}

// startupLabelMembers mirrors the western source's MsgSheet/UcSysMsg.fms closely
// enough to exercise the build-time startup label: the member keeps the real slot
// count (1462) and the real neighbours around slot 1326, whose whole value is the
// fiction disclaimer the label replaces.
func startupLabelMembers() []fxMember {
	slots := make([]string, 1462)
	slots[0] = "New Game"
	slots[1] = "Continue"
	slots[2] = "Load Game"
	slots[1325] = "This game contains sequences with flashing screens and other flash effects."
	slots[1326] = "This is a work of fiction.\nAny similarity to actual people, organizations,\nevents, or terminology is purely coincidental."
	slots[1327] = "PS4 save data could not be found."
	return []fxMember{
		{dir: "MsgSheet", name: "UcSysMsg.fms", stored: fxFMS(slots), extract: -1},
		{dir: "MsgSheet", name: "Other.fms", stored: fxFMS([]string{"alpha", "beta"}), extract: -1},
		{dir: "", name: "root.bin", stored: []byte("root payload"), extract: -1},
	}
}

func fixExtract(members []fxMember) []fxMember {
	for i := range members {
		if members[i].extract < 0 {
			members[i].extract = int64(len(members[i].stored))
		}
	}
	return members
}

// fxFTX builds a minimal synthetic FTEX atlas with one 2048x2048 BC7 surface,
// mirroring the layout of the game's GUI atlases closely enough for the title
// footer transform. Test fixture only.
func fxFTX(t *testing.T, width, height int) []byte {
	t.Helper()
	const (
		surfaceTag = 320
		nameBytes  = 64
	)
	tex := surfaceTag + 8 + nameBytes
	payload := tex + 0x100
	blocks := ((width + 3) / 4) * ((height + 3) / 4)
	buf := make([]byte, payload+blocks*16)
	copy(buf[:4], "FTEX")
	copy(buf[surfaceTag:surfaceTag+4], "FTX0")
	copy(buf[tex:tex+4], ".tex")
	words := []uint32{0x4D, 1, uint32(width), uint32(height), 1, 1, 0x100, uint32(width * height)}
	for i, w := range words {
		fxLE.PutUint32(buf[tex+4+i*4:], w)
	}
	return buf
}

// ---------- tests ----------

func TestNoOpBuildByteIdentical(t *testing.T) {
	dir := t.TempDir()
	src, _, srcBytes := writeCPK(t, dir, fixExtract(standardMembers()))
	corpus := writeCorpus(t, dir, [][3]string{{"UcSysMsg.fms:0", "ニューゲーム", "New Game"}})
	translations := filepath.Join(dir, "translations")
	// A validated canonical row whose text equals the official source text: the
	// record is applied but changes no byte, so the rebuild must be a no-op.
	writeTranslations(t, translations, "honorifics.tsv",
		"record_id\tsource_jp_sha\toriginal_en_sha\tenglish_text\n"+
			"UcSysMsg.fms:0\t"+sha1Short("ニューゲーム")+"\t"+sha1Short("New Game")+"\tNew Game\n")

	res, err := Run(baseOptions(src, corpus, translations, filepath.Join(dir, "out"), VariantHonorifics))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Editions) != 1 || !res.Editions[0].Published {
		t.Fatalf("expected one published edition: %+v", res.Editions)
	}
	got := readOut(t, res, VariantHonorifics)
	if !bytes.Equal(got, srcBytes) {
		t.Fatalf("no-op build is not byte-identical to the source")
	}
	if res.Editions[0].SHA256 != sha256Bytes(srcBytes) {
		t.Fatalf("reported SHA does not match output bytes")
	}
}

func TestSingleRecordGrowAndShrink(t *testing.T) {
	for _, tc := range []struct{ name, canonical string }{
		{"grow", "New Journey"},
		{"shrink", "New"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			src, _, srcBytes := writeCPK(t, dir, fixExtract(standardMembers()))
			corpus := writeCorpus(t, dir, [][3]string{{"UcSysMsg.fms:0", "ニューゲーム", "New Game"}})
			translations := filepath.Join(dir, "translations")
			writeTranslations(t, translations, "honorifics.tsv",
				"record_id\tsource_jp_sha\toriginal_en_sha\tenglish_text\n"+
					"UcSysMsg.fms:0\t"+sha1Short("ニューゲーム")+"\t"+sha1Short("New Game")+"\t"+tc.canonical+"\n")

			res, err := Run(baseOptions(src, corpus, translations, filepath.Join(dir, "out"), VariantHonorifics))
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			got := readOut(t, res, VariantHonorifics)
			if bytes.Equal(got, srcBytes) {
				t.Fatal("expected the edited archive to differ from the source")
			}
			a := mustOpenCPK(t, got)
			slots := memberSlots(t, a, "MsgSheet/UcSysMsg.fms")
			if slots[0] != tc.canonical || slots[1] != "Continue" || slots[2] != "Load Game" {
				t.Fatalf("slots = %v, want[0]=%q", slots, tc.canonical)
			}
			// untouched member is byte-identical
			sa := mustOpenCPK(t, srcBytes)
			so, _ := sa.RawData(sa.Member("MsgSheet/Other.fms"))
			no, _ := a.RawData(a.Member("MsgSheet/Other.fms"))
			if !bytes.Equal(so, no) {
				t.Fatal("untouched member changed")
			}
		})
	}
}

func TestBothEditionsFromOneIntermediate(t *testing.T) {
	dir := t.TempDir()
	src, _, _ := writeCPK(t, dir, fixExtract(standardMembers()))
	corpus := writeCorpus(t, dir, [][3]string{
		{"UcSysMsg.fms:0", "ニューゲーム", "New Game"},
		{"UcSysMsg.fms:2", "ロードゲーム", "Load Game"},
	})
	translations := filepath.Join(dir, "translations")
	writeTranslations(t, translations, "honorifics.tsv",
		"record_id\tsource_jp_sha\toriginal_en_sha\tenglish_text\n"+
			"UcSysMsg.fms:0\t"+sha1Short("ニューゲーム")+"\t"+sha1Short("New Game")+"\tNew Journey\n"+
			"UcSysMsg.fms:2\t"+sha1Short("ロードゲーム")+"\t"+sha1Short("Load Game")+"\tLoad Journey\n")
	writeTranslations(t, translations, "no-honorifics.tsv",
		"record_id\tcanonical_sha256\tenglish_text\nUcSysMsg.fms:0\t"+sha256Full("New Journey")+"\tBegin Quest\n")

	res, err := Run(baseOptions(src, corpus, translations, filepath.Join(dir, "out"), VariantBoth))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Editions) != 2 {
		t.Fatalf("expected two editions, got %d", len(res.Editions))
	}
	hon := readOut(t, res, VariantHonorifics)
	no := readOut(t, res, VariantNoHonorifics)

	ha, na := mustOpenCPK(t, hon), mustOpenCPK(t, no)
	hslots := memberSlots(t, ha, "MsgSheet/UcSysMsg.fms")
	nslots := memberSlots(t, na, "MsgSheet/UcSysMsg.fms")
	if hslots[0] != "New Journey" || hslots[2] != "Load Journey" {
		t.Fatalf("honorifics slots = %v", hslots)
	}
	if nslots[0] != "Begin Quest" || nslots[2] != "Load Journey" {
		t.Fatalf("no-honorifics slots = %v", nslots)
	}
	// Variant separation: exactly one member differs and only at slot 0.
	diffs := 0
	for _, e := range ha.Entries {
		h, _ := ha.RawData(e)
		ne := na.Member(e.Path())
		n, _ := na.RawData(ne)
		if !bytes.Equal(h, n) {
			diffs++
			if e.Path() != "MsgSheet/UcSysMsg.fms" {
				t.Fatalf("unexpected differing member %q", e.Path())
			}
		}
	}
	if diffs != 1 {
		t.Fatalf("expected exactly one differing member, got %d", diffs)
	}
}

// TestTitleFooterAddedToBothEditions builds both editions with a synthetic
// title-screen atlas, reads the version from a root version.txt, and verifies
// the footer is rendered once and staged identically in both CPKs (and recorded
// in the per-edition reports and reuse identity).
func TestTitleFooterAddedToBothEditions(t *testing.T) {
	dir := t.TempDir()
	src, _, _ := writeCPK(t, dir, fixExtract(startupLabelMembers()))
	corpus := writeCorpus(t, dir, [][3]string{{"UcSysMsg.fms:0", "ニューゲーム", "New Game"}})
	translations := filepath.Join(dir, "translations")
	writeTranslations(t, translations, "honorifics.tsv",
		"record_id\tsource_jp_sha\toriginal_en_sha\tenglish_text\n"+
			"UcSysMsg.fms:0\t"+sha1Short("ニューゲーム")+"\t"+sha1Short("New Game")+"\tNew Journey\n")

	root := filepath.Join(dir, "root")
	mustWrite(t, filepath.Join(root, VersionFileName), []byte("0.2.1-test\n"))
	version, err := ReadVersion(root)
	if err != nil {
		t.Fatalf("ReadVersion: %v", err)
	}
	atlas := fxFTX(t, 2048, 2048)
	footerPath := filepath.Join(dir, "Opening100.ftx")
	mustWrite(t, footerPath, atlas)

	opts := baseOptions(src, corpus, translations, filepath.Join(dir, "out"), VariantBoth)
	opts.Version = version
	opts.TitleFooterFTX = footerPath
	res, err := Run(opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	wantText := "UNBURGER MOD " + version
	if res.TitleFooterTag != wantText {
		t.Fatalf("footer text = %q, want %q", res.TitleFooterTag, wantText)
	}
	if res.TitleFooterMember != "GUI/Opening100.ftx" {
		t.Fatalf("footer member = %q", res.TitleFooterMember)
	}
	wantBytes, wantProof, err := titlefooter.Apply(atlas, version)
	if err != nil {
		t.Fatal(err)
	}
	if res.TitleFooterSize != int64(len(wantBytes)) || res.TitleFooterSHA256 != sha256Bytes(wantBytes) {
		t.Fatalf("footer size/sha = %d/%s, want %d/%s",
			res.TitleFooterSize, res.TitleFooterSHA256, len(wantBytes), sha256Bytes(wantBytes))
	}
	if res.TitleFooterProof == nil || *res.TitleFooterProof != wantProof {
		t.Fatalf("footer proof = %+v, want %+v", res.TitleFooterProof, wantProof)
	}

	for _, variant := range []Variant{VariantHonorifics, VariantNoHonorifics} {
		out := readOut(t, res, variant)
		a := mustOpenCPK(t, out)
		if len(a.Entries) != 4 {
			t.Fatalf("%s: entries = %d, want 4", variant, len(a.Entries))
		}
		e := a.Member("GUI/Opening100.ftx")
		if e == nil {
			t.Fatalf("%s: footer member missing", variant)
		}
		raw, err := a.RawData(e)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(raw, wantBytes) {
			t.Fatalf("%s: staged footer differs from titlefooter.Apply output", variant)
		}
		if e.Compressed() || e.FileSize != int64(len(wantBytes)) {
			t.Fatalf("%s: footer stored compressed or wrong size", variant)
		}
		reportPath := filepath.Join(res.RunDir, string(variant), "BUILD_REPORT.json")
		reportData, err := os.ReadFile(reportPath)
		if err != nil {
			t.Fatalf("%s: read report: %v", variant, err)
		}
		var rep editionReport
		if err := json.Unmarshal(reportData, &rep); err != nil {
			t.Fatalf("%s: decode report: %v", variant, err)
		}
		if rep.TitleFooterTag != wantText || rep.TitleFooterSHA != res.TitleFooterSHA256 {
			t.Fatalf("%s: report footer = %q %q", variant, rep.TitleFooterTag, rep.TitleFooterSHA)
		}
	}

	if got := reuseEdition(res, res.RunDir, VariantHonorifics); got == nil {
		t.Fatal("a matching published run should be reusable")
	}
	mismatch := *res
	mismatch.TitleFooterSHA256 = strings.Repeat("0", 64)
	if got := reuseEdition(&mismatch, res.RunDir, VariantHonorifics); got != nil {
		t.Fatal("a run built with a different footer atlas must not be reused")
	}
	fontMismatch := *res
	fontMismatch.TitleFooterFontSHA256 = strings.Repeat("1", 64)
	if got := reuseEdition(&fontMismatch, res.RunDir, VariantHonorifics); got != nil {
		t.Fatal("a run built with a different typeface must not be reused")
	}
	configMismatch := *res
	configMismatch.TitleFooterConfigSHA256 = strings.Repeat("2", 64)
	if got := reuseEdition(&configMismatch, res.RunDir, VariantHonorifics); got != nil {
		t.Fatal("a run built with different typography must not be reused")
	}
}

func TestDeterminism(t *testing.T) {
	dir := t.TempDir()
	src, _, _ := writeCPK(t, dir, fixExtract(standardMembers()))
	corpus := writeCorpus(t, dir, [][3]string{{"UcSysMsg.fms:0", "ニューゲーム", "New Game"}})
	translations := filepath.Join(dir, "translations")
	writeTranslations(t, translations, "honorifics.tsv",
		"record_id\tsource_jp_sha\toriginal_en_sha\tenglish_text\n"+
			"UcSysMsg.fms:0\t"+sha1Short("ニューゲーム")+"\t"+sha1Short("New Game")+"\tNew Journey\n")

	var hashes []string
	for i := 0; i < 2; i++ {
		out := filepath.Join(dir, fmt.Sprintf("out%d", i))
		res, err := Run(baseOptions(src, corpus, translations, out, VariantBoth))
		if err != nil {
			t.Fatalf("Run %d: %v", i, err)
		}
		for _, e := range res.Editions {
			hashes = append(hashes, e.SHA256)
		}
	}
	if hashes[0] != hashes[2] || hashes[1] != hashes[3] {
		t.Fatalf("non-deterministic output hashes: %v", hashes)
	}
}

func TestRollbackLeavesNoOutput(t *testing.T) {
	dir := t.TempDir()
	src, _, _ := writeCPK(t, dir, fixExtract(standardMembers()))
	corpus := writeCorpus(t, dir, [][3]string{{"UcSysMsg.fms:0", "ニューゲーム", "New Game"}})
	translations := filepath.Join(dir, "translations")
	writeTranslations(t, translations, "honorifics.tsv",
		"record_id\tsource_jp_sha\toriginal_en_sha\tenglish_text\n"+
			"UcSysMsg.fms:0\t"+sha1Short("ニューゲーム")+"\t"+sha1Short("New Game")+"\tNew Journey\n")

	outDir := filepath.Join(dir, "out")
	sentinel := filepath.Join(outDir, "test-run", "honorifics")
	mustWrite(t, filepath.Join(sentinel, "SENTINEL"), []byte("keep"))

	_, err := Run(baseOptions(src, corpus, translations, outDir, VariantHonorifics))
	if err == nil {
		t.Fatal("expected the build to fail when the output directory already exists")
	}
	if _, statErr := os.Stat(filepath.Join(sentinel, "SENTINEL")); statErr != nil {
		t.Fatal("pre-existing directory was disturbed")
	}
	if _, statErr := os.Stat(sentinel + ".staging"); !os.IsNotExist(statErr) {
		t.Fatal("staging directory was not removed after failure")
	}
	if _, statErr := os.Stat(filepath.Join(sentinel, "romfs")); !os.IsNotExist(statErr) {
		t.Fatal("partial output was published")
	}
}

func TestPublishAllRollsBackFirstEdition(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "run", "honorifics")
	second := filepath.Join(dir, "run", "no-honorifics")
	mustWrite(t, filepath.Join(second, "SENTINEL"), []byte("keep"))

	err := publishAll(filepath.Join(dir, "run"), []stagedDir{
		{dir: first, files: map[string][]byte{"romfs/a.bin": {1}}},
		{dir: second, files: map[string][]byte{"romfs/b.bin": {2}}},
	})
	if err == nil {
		t.Fatal("expected publishAll to fail on the pre-existing directory")
	}
	if _, statErr := os.Stat(first); !os.IsNotExist(statErr) {
		t.Fatal("already-renamed edition was not rolled back")
	}
	if _, statErr := os.Stat(first + ".staging"); !os.IsNotExist(statErr) {
		t.Fatal("staging directory was not removed")
	}
}

func TestUnsupportedSourceRejection(t *testing.T) {
	dir := t.TempDir()
	src, srcSHA, _ := writeCPK(t, dir, fixExtract(standardMembers()))
	corpus := writeCorpus(t, dir, nil)
	translations := filepath.Join(dir, "translations")
	os.MkdirAll(translations, 0o755)

	cases := []struct {
		name string
		opt  func(o *Options)
		want string
	}{
		{"missing", func(o *Options) {
			o.SourcePath = filepath.Join(dir, "nope.cpk")
			o.Expect = SourceExpectation{}
		}, "source CPK"},
		{"not-cpk", func(o *Options) {
			p := filepath.Join(dir, "notcpk.bin")
			mustWrite(t, p, []byte("not an archive at all......"))
			o.SourcePath = p
			o.Expect = SourceExpectation{}
		}, "not a supported CRI CPK"},
		{"sha-mismatch", func(o *Options) {
			o.Expect = SourceExpectation{SHA256: strings.Repeat("0", 64)}
		}, "SHA-256 mismatch"},
		{"bad-tid", func(o *Options) {
			o.Expect = SourceExpectation{SHA256: srcSHA}
			o.TitleID = "0100000000000000"
		}, "unsupported TitleID"},
		{"bad-version", func(o *Options) {
			o.Expect = SourceExpectation{}
			o.DisplayVersion = "9.99"
		}, "unsupported display version"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := baseOptions(src, corpus, translations, filepath.Join(dir, "out"), VariantHonorifics)
			tc.opt(&o)
			if _, err := Run(o); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want substring %q", err, tc.want)
			}
			if _, statErr := os.Stat(filepath.Join(dir, "out")); !os.IsNotExist(statErr) {
				t.Fatal("failed build created an output directory")
			}
		})
	}
}

func TestUnknownRecordTargetRejected(t *testing.T) {
	dir := t.TempDir()
	src, srcSHA, _ := writeCPK(t, dir, fixExtract(standardMembers()))
	corpus := writeCorpus(t, dir, [][3]string{{"Missing.fms:0", "jp", "en"}})
	translations := filepath.Join(dir, "translations")
	writeTranslations(t, translations, "honorifics.tsv",
		"record_id\tsource_jp_sha\toriginal_en_sha\tenglish_text\n"+
			"Missing.fms:0\t"+sha1Short("jp")+"\t"+sha1Short("en")+"\tchanged\n")

	o := baseOptions(src, corpus, translations, filepath.Join(dir, "out"), VariantHonorifics)
	o.Expect = SourceExpectation{SHA256: srcSHA}
	if _, err := Run(o); err == nil || !strings.Contains(err.Error(), "unknown record target") {
		t.Fatalf("err = %v, want unknown record target", err)
	}
}

func TestSlotOutOfRangeRejected(t *testing.T) {
	dir := t.TempDir()
	src, srcSHA, _ := writeCPK(t, dir, fixExtract(standardMembers()))
	corpus := writeCorpus(t, dir, [][3]string{{"Other.fms:99", "jp", "en"}})
	translations := filepath.Join(dir, "translations")
	writeTranslations(t, translations, "honorifics.tsv",
		"record_id\tsource_jp_sha\toriginal_en_sha\tenglish_text\n"+
			"Other.fms:99\t"+sha1Short("jp")+"\t"+sha1Short("en")+"\tchanged\n")

	o := baseOptions(src, corpus, translations, filepath.Join(dir, "out"), VariantHonorifics)
	o.Expect = SourceExpectation{SHA256: srcSHA}
	if _, err := Run(o); err == nil || !strings.Contains(err.Error(), "unknown record target") {
		t.Fatalf("err = %v, want out-of-range rejection", err)
	}
}

func TestStrictTokens(t *testing.T) {
	dir := t.TempDir()
	src, srcSHA, _ := writeCPK(t, dir, fixExtract(standardMembers()))
	corpus := writeCorpus(t, dir, [][3]string{{"UcSysMsg.fms:0", "こんにちは", "Hello #i"}})
	translations := filepath.Join(dir, "translations")
	writeTranslations(t, translations, "honorifics.tsv",
		"record_id\tsource_jp_sha\toriginal_en_sha\tenglish_text\n"+
			"UcSysMsg.fms:0\t"+sha1Short("こんにちは")+"\t"+sha1Short("Hello #i")+"\tHello friend\n")

	// Default: token mismatches are advisory.
	o := baseOptions(src, corpus, translations, filepath.Join(dir, "out-advisory"), VariantHonorifics)
	o.Expect = SourceExpectation{SHA256: srcSHA}
	res, err := Run(o)
	if err != nil {
		t.Fatalf("advisory run failed: %v", err)
	}
	if res.Resolution.Counts.TokenMismatch != 1 {
		t.Fatalf("expected 1 advisory token mismatch, got %d", res.Resolution.Counts.TokenMismatch)
	}

	// Strict: the same mismatch is fatal.
	o = baseOptions(src, corpus, translations, filepath.Join(dir, "out-strict"), VariantHonorifics)
	o.Expect = SourceExpectation{SHA256: srcSHA}
	o.StrictTokens = true
	if _, err := Run(o); err == nil || !strings.Contains(err.Error(), "token mismatch") {
		t.Fatalf("err = %v, want strict-token failure", err)
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	src, srcSHA, _ := writeCPK(t, dir, fixExtract(standardMembers()))
	corpus := writeCorpus(t, dir, [][3]string{{"UcSysMsg.fms:0", "ニューゲーム", "New Game"}})
	translations := filepath.Join(dir, "translations")
	writeTranslations(t, translations, "honorifics.tsv",
		"record_id\tsource_jp_sha\toriginal_en_sha\tenglish_text\n"+
			"UcSysMsg.fms:0\t"+sha1Short("ニューゲーム")+"\t"+sha1Short("New Game")+"\tNew Journey\n")

	outDir := filepath.Join(dir, "out")
	o := baseOptions(src, corpus, translations, outDir, VariantBoth)
	o.Expect = SourceExpectation{SHA256: srcSHA}
	o.DryRun = true
	res, err := Run(o)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if _, statErr := os.Stat(outDir); !os.IsNotExist(statErr) {
		t.Fatal("dry run created an output directory")
	}
	if len(res.Editions) != 2 {
		t.Fatalf("expected two editions, got %d", len(res.Editions))
	}
	for _, e := range res.Editions {
		if e.Published {
			t.Fatal("dry run reported a published edition")
		}
		if len(e.SHA256) != 64 || e.Size == 0 {
			t.Fatalf("dry run missing output hashes: %+v", e)
		}
	}
	if !strings.Contains(res.SummaryTxt, "DRY-RUN") {
		t.Fatal("summary should mark DRY-RUN")
	}
}

func TestOverrideEqualToSourceProducesNoByteChange(t *testing.T) {
	dir := t.TempDir()
	src, srcSHA, srcBytes := writeCPK(t, dir, fixExtract(standardMembers()))
	corpus := writeCorpus(t, dir, [][3]string{{"UcSysMsg.fms:0", "ニューゲーム", "New Game"}})
	translations := filepath.Join(dir, "translations")
	writeTranslations(t, translations, "honorifics.tsv",
		"record_id\tsource_jp_sha\toriginal_en_sha\tenglish_text\n"+
			"UcSysMsg.fms:0\t"+sha1Short("ニューゲーム")+"\t"+sha1Short("New Game")+"\tNew Journey\n")
	writeTranslations(t, translations, "no-honorifics.tsv",
		"record_id\tcanonical_sha256\tenglish_text\nUcSysMsg.fms:0\t"+sha256Full("New Journey")+"\tNew Game\n")

	o := baseOptions(src, corpus, translations, filepath.Join(dir, "out"), VariantBoth)
	o.Expect = SourceExpectation{SHA256: srcSHA}
	res, err := Run(o)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	hon := readOut(t, res, VariantHonorifics)
	no := readOut(t, res, VariantNoHonorifics)
	if bytes.Equal(hon, srcBytes) {
		t.Fatal("honorifics edition should change slot 0")
	}
	if !bytes.Equal(no, srcBytes) {
		t.Fatal("no-honorifics override equals the source text, so the edition must be byte-identical")
	}
	for _, e := range res.Editions {
		switch e.Variant {
		case VariantNoHonorifics:
			if e.ChangedSlots != 0 || len(e.Files) != 0 {
				t.Fatalf("no-honorifics reported changes: %+v", e)
			}
		case VariantHonorifics:
			if e.ChangedSlots != 1 || len(e.Files) != 1 {
				t.Fatalf("honorifics changed-slot count wrong: %+v", e)
			}
		}
	}
}

func TestPublishedLayoutAndReports(t *testing.T) {
	dir := t.TempDir()
	src, srcSHA, _ := writeCPK(t, dir, fixExtract(standardMembers()))
	corpus := writeCorpus(t, dir, [][3]string{{"UcSysMsg.fms:0", "ニューゲーム", "New Game"}})
	translations := filepath.Join(dir, "translations")
	writeTranslations(t, translations, "honorifics.tsv",
		"record_id\tsource_jp_sha\toriginal_en_sha\tenglish_text\n"+
			"UcSysMsg.fms:0\t"+sha1Short("ニューゲーム")+"\t"+sha1Short("New Game")+"\tNew Journey\n")

	outDir := filepath.Join(dir, "out")
	o := baseOptions(src, corpus, translations, outDir, VariantHonorifics)
	o.Expect = SourceExpectation{SHA256: srcSHA}
	res, err := Run(o)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	dirp := filepath.Join(outDir, "test-run", "honorifics")
	files, err := listStagedFiles(dirp)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"BUILD_REPORT.json", "BUILD_REPORT.txt", "romfs/Unicorn_US.CPK"}
	if strings.Join(files, ",") != strings.Join(want, ",") {
		t.Fatalf("published layout = %v, want %v", files, want)
	}
	if !strings.Contains(res.Editions[0].ReportTxt, "diff verification:  PASS") {
		t.Fatalf("report missing diff PASS line:\n%s", res.Editions[0].ReportTxt)
	}
	if !strings.Contains(res.Editions[0].ReportJSON, "\"output_sha256\"") {
		t.Fatal("json report missing output hash")
	}
}

func TestReportsContainNoAbsolutePaths(t *testing.T) {
	dir := t.TempDir()
	src, srcSHA, _ := writeCPK(t, dir, fixExtract(standardMembers()))
	corpus := writeCorpus(t, dir, [][3]string{{"UcSysMsg.fms:0", "ニューゲーム", "New Game"}})
	translations := filepath.Join(dir, "translations")
	writeTranslations(t, translations, "honorifics.tsv",
		"record_id\tsource_jp_sha\toriginal_en_sha\tenglish_text\n"+
			"UcSysMsg.fms:0\t"+sha1Short("ニューゲーム")+"\t"+sha1Short("New Game")+"\tNew Journey\n")

	o := baseOptions(src, corpus, translations, filepath.Join(dir, "out"), VariantHonorifics)
	o.Expect = SourceExpectation{SHA256: srcSHA}
	res, err := Run(o)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, e := range res.Editions {
		for name, text := range map[string]string{"report.txt": e.ReportTxt, "report.json": e.ReportJSON} {
			if strings.Contains(text, dir) {
				t.Fatalf("%s leaked the absolute temp directory:\n%s", name, text)
			}
			if strings.Contains(text, "\n/") || strings.Contains(text, "\": \"/") {
				t.Fatalf("%s contains an absolute path value:\n%s", name, text)
			}
		}
	}
	if strings.Contains(res.SummaryTxt, dir) {
		t.Fatalf("summary leaked the absolute temp directory:\n%s", res.SummaryTxt)
	}
}

func TestErrorsContainNoAbsolutePaths(t *testing.T) {
	dir := t.TempDir()
	_, err := ValidateSource(filepath.Join(dir, "missing.cpk"), SourceExpectation{})
	if err == nil {
		t.Fatal("expected a source error")
	}
	if strings.Contains(err.Error(), dir) {
		t.Fatalf("error leaked the absolute temp directory: %v", err)
	}
	if !strings.Contains(err.Error(), "missing.cpk") {
		t.Fatalf("error should name the file: %v", err)
	}
}
