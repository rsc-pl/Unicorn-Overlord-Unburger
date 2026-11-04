package build

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"uo-unburger/internal/nso"
	"uo-unburger/internal/voice"
)

// synthMain builds a minimal, self-contained uncompressed NSO0 image whose
// .rodata carries the two synthetic voice-registry fields a test plan edits.
func synthMain(t *testing.T, rodata []byte, moduleID [32]byte) []byte {
	t.Helper()
	const headerSize = 0x100
	text := append([]byte("TEXT-CODE-"), bytes.Repeat([]byte{0x11}, 32)...)
	data := bytes.Repeat([]byte{0x22}, 32)
	blobs := map[nso.Name][]byte{nso.Text: text, nso.Rodata: rodata, nso.Data: data}
	hdr := make([]byte, headerSize)
	copy(hdr[:4], nso.Magic)
	copy(hdr[0x40:0x60], moduleID[:])
	type field struct{ fileOff, memOff, decomp, memsz, csize, hash int }
	fields := map[nso.Name]field{
		nso.Text:   {0x10, 0x14, 0x18, 0x1C, 0x60, 0xA0},
		nso.Rodata: {0x20, 0x24, 0x28, 0x2C, 0x64, 0xC0},
		nso.Data:   {0x30, 0x34, 0x38, 0x3C, 0x68, 0xE0},
	}
	mem := map[nso.Name]uint32{nso.Text: 0, nso.Rodata: 0x1000, nso.Data: 0x2000}
	off := uint32(headerSize)
	out := append([]byte(nil), hdr...)
	for _, n := range []nso.Name{nso.Text, nso.Rodata, nso.Data} {
		f := fields[n]
		b := blobs[n]
		binary.LittleEndian.PutUint32(out[f.fileOff:], off)
		binary.LittleEndian.PutUint32(out[f.memOff:], mem[n])
		binary.LittleEndian.PutUint32(out[f.decomp:], uint32(len(b)))
		binary.LittleEndian.PutUint32(out[f.memsz:], uint32(len(b)))
		binary.LittleEndian.PutUint32(out[f.csize:], uint32(len(b)))
		sum := sha256.Sum256(b)
		copy(out[f.hash:f.hash+32], sum[:])
		out = append(out, b...)
		off += uint32(len(b))
	}
	return out
}

// voiceFixture writes a synthetic CPK + translations + ExeFS main and returns
// the options for a voice-enabled build plus the expected patched image.
func voiceFixture(t *testing.T, dir string) (Options, []byte) {
	t.Helper()
	src, _, _ := writeCPK(t, dir, fixExtract(standardMembers()))
	corpus := writeCorpus(t, dir, [][3]string{{"UcSysMsg.fms:0", "ニューゲーム", "New Game"}})
	translations := filepath.Join(dir, "translations")
	writeTranslations(t, translations, "honorifics.tsv",
		"record_id\tsource_jp_sha\toriginal_en_sha\tenglish_text\n"+
			"UcSysMsg.fms:0\t"+sha1Short("ニューゲーム")+"\t"+sha1Short("New Game")+"\tNew Game\n")

	rodata := make([]byte, 0x100)
	le := func(v uint64) []byte {
		b := make([]byte, 8)
		binary.LittleEndian.PutUint64(b, v)
		return b
	}
	copy(rodata[0x40:], le(0x00AABBCC))
	copy(rodata[0x80:], le(0x00DDEEFF))
	var moduleID [32]byte
	for i := range moduleID {
		moduleID[i] = byte(0xC0 + i)
	}
	main := synthMain(t, rodata, moduleID)
	mainPath := filepath.Join(dir, "main.bin")
	mustWrite(t, mainPath, main)

	sum := sha256.Sum256(main)
	plan := voice.Plan{
		Name: "test-jp-voice",
		Target: voice.Identity{
			ModuleID: hex.EncodeToString(moduleID[:]),
			SHA256:   hex.EncodeToString(sum[:]),
			Size:     int64(len(main)),
		},
		Edits: []voice.Edit{
			{Segment: nso.Rodata, VA: 0x1040, From: le(0x00AABBCC), To: le(0x00112233), Desc: "US->JP (a)"},
			{Segment: nso.Rodata, VA: 0x1080, From: le(0x00DDEEFF), To: le(0x00445566), Desc: "US->JP (b)"},
		},
	}
	want, _, err := voice.Apply(main, plan)
	if err != nil {
		t.Fatalf("reference Apply: %v", err)
	}

	opts := baseOptions(src, corpus, translations, filepath.Join(dir, "out"), VariantBoth)
	opts.Voice = true
	opts.VoiceMain = mainPath
	opts.voicePlan = &plan
	return opts, want
}

func TestVoicePatchBuildStage(t *testing.T) {
	dir := t.TempDir()
	opts, want := voiceFixture(t, dir)
	res, err := Run(opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Voice == nil {
		t.Fatal("Voice result missing")
	}
	if res.Voice.RuntimeStatus != "NOT_RUNTIME_APPROVED" {
		t.Fatalf("runtime status = %q", res.Voice.RuntimeStatus)
	}
	if res.Voice.ChangedBytes == 0 || res.Voice.Edits != 2 {
		t.Fatalf("voice report = %+v", res.Voice)
	}

	stage := filepath.Join(res.RunDir, "voice")
	got, err := os.ReadFile(filepath.Join(stage, "exefs", "main"))
	if err != nil {
		t.Fatalf("read voice artifact: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("published voice artifact differs from the independently built image")
	}
	if _, err := os.Stat(filepath.Join(stage, "main.ips")); err != nil {
		t.Fatalf("IPS missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stage, "NOT_RUNTIME_APPROVED")); err != nil {
		t.Fatalf("marker missing: %v", err)
	}

	// The per-edition reports assert the pinned Build ID and list the component.
	for _, e := range res.Editions {
		var rep editionReport
		if err := json.Unmarshal([]byte(e.ReportJSON), &rep); err != nil {
			t.Fatalf("edition report JSON: %v", err)
		}
		if rep.VoiceBuildID != opts.voicePlan.Target.ModuleID {
			t.Fatalf("edition %s voice_build_id = %q", e.Variant, rep.VoiceBuildID)
		}
		joined := strings.Join(rep.PatchComponents, "\n")
		if !strings.Contains(joined, "voice-exefs-patch") || strings.Contains(joined, "NOT_INCLUDED") {
			t.Fatalf("edition %s patch components = %q", e.Variant, joined)
		}
		if !strings.Contains(rep.VoiceStatus, "EXPERIMENTAL") {
			t.Fatalf("edition %s voice status = %q", e.Variant, rep.VoiceStatus)
		}
	}
	// The voice component is shared: exactly one stage dir for both editions.
	if _, err := os.Stat(filepath.Join(res.RunDir, "honorifics", "voice")); err == nil {
		t.Fatal("voice artifact was duplicated into an edition directory")
	}
}

func TestVoicePatchDryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	opts, _ := voiceFixture(t, dir)
	opts.DryRun = true
	res, err := Run(opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Voice == nil {
		t.Fatal("dry-run produced no voice result")
	}
	if _, err := os.Stat(filepath.Join(opts.OutDir, res.RunName)); err == nil {
		t.Fatal("dry-run wrote output")
	}
}

func TestVoicePatchRejectsUnpinnedMain(t *testing.T) {
	dir := t.TempDir()
	opts, _ := voiceFixture(t, dir)
	// Drop the test plan: the build must gate the synthetic main against the
	// pinned Western update identity and refuse.
	opts.voicePlan = nil
	_, err := Run(opts)
	if err == nil || !strings.Contains(err.Error(), "voice") {
		t.Fatalf("want identity rejection, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(opts.OutDir, "test-run")); statErr == nil {
		t.Fatal("a failed voice build published output")
	}
}

func TestVoicePatchWithoutMainIsAnError(t *testing.T) {
	dir := t.TempDir()
	opts, _ := voiceFixture(t, dir)
	opts.VoiceMain = ""
	_, err := Run(opts)
	if err == nil || !strings.Contains(err.Error(), "--voice-main") {
		t.Fatalf("want missing-main error, got %v", err)
	}
}
