package build

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const startupLabelFixtureSourceText = "This is a work of fiction.\nAny similarity to actual people, organizations,\nevents, or terminology is purely coincidental."

// startupLabelFixture builds a source + corpus + translations set whose
// MsgSheet/UcSysMsg.fms carries the real startup-label slot.
func startupLabelFixture(t *testing.T) (dir, src, corpus, translations string, srcBytes []byte) {
	t.Helper()
	dir = t.TempDir()
	src, _, srcBytes = writeCPK(t, dir, fixExtract(startupLabelMembers()))
	corpus = writeCorpus(t, dir, [][3]string{
		{"UcSysMsg.fms:0", "ニューゲーム", "New Game"},
	})
	translations = filepath.Join(dir, "translations")
	writeTranslations(t, translations, "honorifics.tsv",
		"record_id\tsource_jp_sha\toriginal_en_sha\tenglish_text\n"+
			"UcSysMsg.fms:0\t"+sha1Short("ニューゲーム")+"\t"+sha1Short("New Game")+"\tNew Journey\n")
	return dir, src, corpus, translations, srcBytes
}

// TestStartupLabelForcedIntoBothEditions: with a version supplied, the whole
// startup record is replaced by "Unburger Mod V<version>" in every edition, the
// neighbouring records are untouched, and the two editions carry identical bytes
// for that record.
func TestStartupLabelForcedIntoBothEditions(t *testing.T) {
	dir, src, corpus, translations, srcBytes := startupLabelFixture(t)
	opts := baseOptions(src, corpus, translations, filepath.Join(dir, "out"), VariantBoth)
	opts.Version = "1.2.3"
	res, err := Run(opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.StartupLabel != "Unburger Mod V1.2.3" {
		t.Fatalf("StartupLabel = %q", res.StartupLabel)
	}

	srcA := mustOpenCPK(t, srcBytes)
	srcSlots := memberSlots(t, srcA, StartupLabelMember)
	if srcSlots[StartupLabelSlot] != startupLabelFixtureSourceText {
		t.Fatalf("fixture source slot %d = %q", StartupLabelSlot, srcSlots[StartupLabelSlot])
	}

	for _, v := range []Variant{VariantHonorifics, VariantNoHonorifics} {
		a := mustOpenCPK(t, readOut(t, res, v))
		slots := memberSlots(t, a, StartupLabelMember)
		if len(slots) != len(srcSlots) {
			t.Fatalf("%s: slot count changed (%d -> %d)", v, len(srcSlots), len(slots))
		}
		if slots[StartupLabelSlot] != "Unburger Mod V1.2.3" {
			t.Fatalf("%s: startup label = %q", v, slots[StartupLabelSlot])
		}
		// The neighbours are untouched, so only the label slot changed.
		if slots[StartupLabelSlot-1] != srcSlots[StartupLabelSlot-1] ||
			slots[StartupLabelSlot+1] != srcSlots[StartupLabelSlot+1] {
			t.Fatalf("%s: a neighbour of the startup record changed", v)
		}
	}

	// The label record is identical across editions: it is a build property, not
	// a translation choice.
	hon := mustOpenCPK(t, readOut(t, res, VariantHonorifics))
	no := mustOpenCPK(t, readOut(t, res, VariantNoHonorifics))
	honSlots := memberSlots(t, hon, StartupLabelMember)
	noSlots := memberSlots(t, no, StartupLabelMember)
	if honSlots[StartupLabelSlot] != noSlots[StartupLabelSlot] {
		t.Fatalf("startup label differs across editions: %q vs %q", honSlots[StartupLabelSlot], noSlots[StartupLabelSlot])
	}
}

// TestStartupLabelTracksVersion: changing the supplied version changes the bytes
// written into the startup record, so a version.txt change is observable in the
// rebuilt CPK (and invalidates a cached run, see reuse.go).
func TestStartupLabelTracksVersion(t *testing.T) {
	dir, src, corpus, translations, _ := startupLabelFixture(t)
	labels := map[string]string{}
	for _, version := range []string{"1.2.3", "9.9.9"} {
		out := filepath.Join(dir, "out-"+version)
		opts := baseOptions(src, corpus, translations, out, VariantHonorifics)
		opts.Version = version
		res, err := Run(opts)
		if err != nil {
			t.Fatalf("Run(%s): %v", version, err)
		}
		a := mustOpenCPK(t, readOut(t, res, VariantHonorifics))
		slots := memberSlots(t, a, StartupLabelMember)
		want := "Unburger Mod V" + version
		if slots[StartupLabelSlot] != want {
			t.Fatalf("version %s: startup label = %q, want %q", version, slots[StartupLabelSlot], want)
		}
		labels[version] = slots[StartupLabelSlot]
	}
	if labels["1.2.3"] == labels["9.9.9"] {
		t.Fatal("changing the version did not change the startup record")
	}
}

// TestStartupLabelTracksVersionFile: the label is driven by the root version.txt
// (through ReadVersion), so changing that file changes the rebuilt record. This
// is the two-version fixture proof.
func TestStartupLabelTracksVersionFile(t *testing.T) {
	dir, src, corpus, translations, _ := startupLabelFixture(t)
	got := map[string]string{}
	for _, version := range []string{"0.1.0-dev", "9.9.9-test"} {
		if err := os.WriteFile(filepath.Join(dir, VersionFileName), []byte(version+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		v, err := ReadVersion(dir)
		if err != nil {
			t.Fatalf("ReadVersion: %v", err)
		}
		out := filepath.Join(dir, "out-"+v)
		opts := baseOptions(src, corpus, translations, out, VariantNoHonorifics)
		opts.Version = v
		res, err := Run(opts)
		if err != nil {
			t.Fatalf("Run(%s): %v", v, err)
		}
		a := mustOpenCPK(t, readOut(t, res, VariantNoHonorifics))
		got[v] = memberSlots(t, a, StartupLabelMember)[StartupLabelSlot]
	}
	if got["0.1.0-dev"] != "Unburger Mod V0.1.0-dev" || got["9.9.9-test"] != "Unburger Mod V9.9.9-test" {
		t.Fatalf("labels = %v", got)
	}
	if got["0.1.0-dev"] == got["9.9.9-test"] {
		t.Fatal("a version.txt change did not change the startup record")
	}
}

// TestStartupLabelAbsentWithoutVersion: a build with no version carries no label
// and leaves the startup record at its original value.
func TestStartupLabelAbsentWithoutVersion(t *testing.T) {
	dir, src, corpus, translations, srcBytes := startupLabelFixture(t)
	res, err := Run(baseOptions(src, corpus, translations, filepath.Join(dir, "out"), VariantHonorifics))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.StartupLabel != "" {
		t.Fatalf("StartupLabel = %q, want empty", res.StartupLabel)
	}
	a := mustOpenCPK(t, readOut(t, res, VariantHonorifics))
	slots := memberSlots(t, a, StartupLabelMember)
	srcSlots := memberSlots(t, mustOpenCPK(t, srcBytes), StartupLabelMember)
	if slots[StartupLabelSlot] != srcSlots[StartupLabelSlot] {
		t.Fatalf("startup record changed without a version: %q", slots[StartupLabelSlot])
	}
	// The label slot must not be listed as an edit either.
	for _, e := range res.Editions {
		for _, f := range e.Files {
			if f.Member != StartupLabelMember {
				continue
			}
			for _, s := range f.Slots {
				if s == StartupLabelSlot {
					t.Fatalf("slot %d reported as edited without a version", StartupLabelSlot)
				}
			}
		}
	}
}

// TestReuseRejectsMismatchedStartupLabel: a published run whose recorded startup
// label disagrees with the build being run is not adopted, so a run built before
// the label existed can never be silently reused.
func TestReuseRejectsMismatchedStartupLabel(t *testing.T) {
	dir, src, corpus, translations, _ := startupLabelFixture(t)
	out := filepath.Join(dir, "out")
	o := baseOptions(src, corpus, translations, out, VariantHonorifics)
	o.Version = "1.2.3"
	if _, err := Run(o); err != nil {
		t.Fatalf("first build: %v", err)
	}
	report := filepath.Join(out, "test-run", "honorifics", "BUILD_REPORT.json")
	data, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["startup_label"] != "Unburger Mod V1.2.3" {
		t.Fatalf("report startup_label = %v", doc["startup_label"])
	}
	doc["startup_label"] = "Unburger Mod V0.0.0-foreign"
	tampered, _ := json.MarshalIndent(doc, "", "  ")
	if err := os.WriteFile(report, append(tampered, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(o); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("a mismatched startup label was adopted: err = %v", err)
	}
}

// TestApplyStartupLabelOverridesExistingEdit: the build-time label wins over any
// translation row at the same slot, and it is byte-identical for both editions.
func TestApplyStartupLabelOverridesExistingEdit(t *testing.T) {
	plan := &Plan{Files: map[string]*FilePlan{
		StartupLabelMember: {
			File:   StartupLabelFile,
			Member: StartupLabelMember,
			Edits: map[int]*Edit{
				StartupLabelSlot: {RecordID: StartupLabelFile + ":1326", File: StartupLabelFile,
					Member: StartupLabelMember, Slot: StartupLabelSlot,
					Honorifics: []byte("from-a-tsv"), NoHonorifics: []byte("from-a-tsv-no-hon")},
			},
		},
	}}
	label, err := applyStartupLabel(plan, "2.0.0")
	if err != nil {
		t.Fatalf("applyStartupLabel: %v", err)
	}
	if label != "Unburger Mod V2.0.0" {
		t.Fatalf("label = %q", label)
	}
	ed := plan.Files[StartupLabelMember].Edits[StartupLabelSlot]
	if !bytes.Equal(ed.Honorifics, []byte(label)) || !bytes.Equal(ed.NoHonorifics, []byte(label)) {
		t.Fatalf("forced edit = %q / %q", ed.Honorifics, ed.NoHonorifics)
	}

	empty := &Plan{Files: map[string]*FilePlan{}}
	if got, err := applyStartupLabel(empty, "  "); err != nil || got != "" {
		t.Fatalf("empty version: label = %q, err = %v", got, err)
	}
	if len(empty.Files) != 0 {
		t.Fatalf("an empty version added a plan entry: %v", empty.Files)
	}
}
