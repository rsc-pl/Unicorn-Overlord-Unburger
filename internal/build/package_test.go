package build

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"uo-unburger/internal/layeredfs"
)

func packageFixture(t *testing.T) (*Result, string) {
	t.Helper()
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
	return res, dir
}

func zipNames(t *testing.T, path string) []string {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer zr.Close()
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	return names
}

func zipManifest(t *testing.T, path string) map[string]any {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		// The Atmosphère layout keeps manifest.json at the archive root; the
		// emulator layout nests every entry inside the single mod folder, so the
		// manifest is <mod-dir>/manifest.json.
		if f.Name != ManifestEntryName && !strings.HasSuffix(f.Name, "/"+ManifestEntryName) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		defer rc.Close()
		var m map[string]any
		dec := json.NewDecoder(rc)
		if err := dec.Decode(&m); err != nil {
			t.Fatalf("decode manifest: %v", err)
		}
		return m
	}
	t.Fatalf("no manifest.json in %s", path)
	return nil
}

func TestParsePackageTarget(t *testing.T) {
	for _, s := range []string{"atmosphere", "emulator", "both"} {
		if _, err := ParsePackageTarget(s); err != nil {
			t.Fatalf("ParsePackageTarget(%q): %v", s, err)
		}
	}
	if _, err := ParsePackageTarget("nope"); err == nil {
		t.Fatal("expected an error for an unknown target")
	}
	if got := TargetBoth.Targets(); len(got) != 2 {
		t.Fatalf("both should expand to two targets, got %v", got)
	}
}

// TestReportTargetSingleRomfs is the regression for the doubled romfs/romfs
// target that report.go produced by joining "romfs/" onto an ArtifactRel that
// already began with "romfs/".
func TestReportTargetSingleRomfs(t *testing.T) {
	res, _ := packageFixture(t)
	for _, e := range res.Editions {
		if strings.Contains(e.ReportTxt, "romfs/romfs") || strings.Contains(e.ReportJSON, "romfs/romfs") {
			t.Fatalf("report still doubles romfs for %s:\n%s", e.Variant, e.ReportTxt)
		}
		var doc struct {
			Target string `json:"target"`
		}
		if err := json.Unmarshal([]byte(e.ReportJSON), &doc); err != nil {
			t.Fatal(err)
		}
		want := "atmosphere/contents/010069401ADB8000/romfs/Unicorn_US.CPK"
		if doc.Target != want {
			t.Fatalf("target = %q, want %q", doc.Target, want)
		}
	}
}

func TestPackageLayoutBothTargetsAndReopen(t *testing.T) {
	res, _ := packageFixture(t)
	pkg, err := Package(res, PackageOptions{Targets: TargetBoth, Version: "1.2.3"})
	if err != nil {
		t.Fatalf("Package: %v", err)
	}
	if len(pkg.Artifacts) != 4 {
		t.Fatalf("expected 4 archives, got %d", len(pkg.Artifacts))
	}
	if pkg.Version != "1.2.3" || pkg.StartupLabel != "Unburger Mod V1.2.3" {
		t.Fatalf("package version = %q / label %q", pkg.Version, pkg.StartupLabel)
	}
	for _, a := range pkg.Artifacts {
		names := zipNames(t, a.Path)
		var want []string
		switch a.Target {
		case TargetAtmosphere:
			// The Atmosphère layout is unchanged: the manifest and install note sit
			// at the archive root beside the atmosphere/contents/<id>/... override.
			want = []string{
				ManifestEntryName,
				InstallReadmeName,
				layeredfs.AtmospherePath("010069401ADB8000", layeredfs.RomFSRel),
			}
			if a.TargetStatus != "VERIFIED_LAYOUT" {
				t.Fatalf("atmosphere status = %q", a.TargetStatus)
			}
		case TargetEmulator:
			// The emulator archive holds exactly one top-level directory — the mod
			// folder itself — so the RomFS override, the manifest and the install
			// note all sit inside it, with no load/<title>/<mod> wrapper.
			want = []string{
				layeredfs.EmulatorZipEntry(ManifestEntryName),
				layeredfs.EmulatorZipEntry(InstallReadmeName),
				layeredfs.EmulatorZipEntry(layeredfs.RomFSRel),
			}
			if a.TargetStatus != "EXPERIMENTAL" {
				t.Fatalf("emulator status = %q", a.TargetStatus)
			}
		}
		got := strings.Join(names, ",")
		for _, p := range want {
			if !strings.Contains(","+got+",", ","+p+",") {
				t.Fatalf("archive %s missing %s (got %v)", a.RelPath, p, names)
			}
		}
		if len(names) != len(want) {
			t.Fatalf("archive %s has %d entries, want exactly %d: %v", a.RelPath, len(names), len(want), names)
		}
		if a.Target == TargetEmulator {
			if strings.Contains(got, "load/") {
				t.Fatalf("emulator archive %s still carries a load/ wrapper: %v", a.RelPath, names)
			}
			// The machine-checked contract on the re-opened archive: exactly one
			// top-level directory, holding only the known payload names.
			files, err := verifyZip(a.Path)
			if err != nil {
				t.Fatalf("verifyZip(%s): %v", a.RelPath, err)
			}
			if err := verifyEmulatorLayout(files); err != nil {
				t.Fatalf("emulator archive %s layout: %v", a.RelPath, err)
			}
		}
		if strings.Contains(got, "romfs/romfs") {
			t.Fatalf("archive %s doubles romfs: %v", a.RelPath, names)
		}
		if !strings.HasPrefix(a.RelPath, "unicorn-overlord-") || !strings.HasSuffix(a.RelPath, pkg.Release+".zip") {
			t.Fatalf("archive name %q does not follow the neutral pattern for release %q", a.RelPath, pkg.Release)
		}
		// Reopen + hash: the artifact's recorded digests must match a fresh read.
		files, err := verifyZip(a.Path)
		if err != nil {
			t.Fatalf("verifyZip(%s): %v", a.RelPath, err)
		}
		if len(files) != len(a.Files) {
			t.Fatalf("reopened file count %d != recorded %d", len(files), len(a.Files))
		}
		m := zipManifest(t, a.Path)
		val, _ := m["validation"].(map[string]any)
		if val == nil || val["status"] != "PASS" || val["reopened"] != true {
			t.Fatalf("manifest validation = %v", val)
		}
		if m["version"] != "1.2.3" || m["startup_label"] != "Unburger Mod V1.2.3" {
			t.Fatalf("manifest version = %v / label = %v", m["version"], m["startup_label"])
		}
		target, _ := m["target"].(map[string]any)
		if target == nil || target["kind"] != string(a.Target) {
			t.Fatalf("manifest target = %v", target)
		}
		if target["layout"] == "" {
			t.Fatalf("manifest target layout is empty")
		}
		switch a.Target {
		case TargetEmulator:
			if target["layout"] != layeredfs.EmulatorModDir {
				t.Fatalf("emulator manifest layout = %v, want %q", target["layout"], layeredfs.EmulatorModDir)
			}
		case TargetAtmosphere:
			if want := layeredfs.AtmosphereRoot + "/010069401ADB8000"; target["layout"] != want {
				t.Fatalf("atmosphere manifest layout = %v, want %q", target["layout"], want)
			}
		}
		// No private or game-container material may appear.
		for _, name := range names {
			lower := strings.ToLower(name)
			for _, bad := range []string{".local", ".nsp", ".xci", ".nca", "prod.keys", "title.keys", ".tmp"} {
				if strings.Contains(lower, bad) {
					t.Fatalf("archive %s contains forbidden path %q", a.RelPath, name)
				}
			}
		}
	}
}

// TestVerifyEmulatorLayout: the emulator archive contract — every entry lives
// inside exactly one top-level directory (the mod folder) and only the known
// payload names appear. Any archive-root file, any load/ or title-id wrapper and
// any unexpected entry inside the folder is rejected.
func TestVerifyEmulatorLayout(t *testing.T) {
	mod := layeredfs.EmulatorModDir
	good := []PackageFile{
		{Path: mod + "/" + ManifestEntryName},
		{Path: mod + "/" + InstallReadmeName},
		{Path: mod + "/" + layeredfs.RomFSRel},
		{Path: mod + "/" + layeredfs.VoiceExeFSRel},
	}
	if err := verifyEmulatorLayout(good); err != nil {
		t.Fatalf("a well-formed emulator layout was rejected: %v", err)
	}

	bad := map[string][]PackageFile{
		"manifest at the archive root": {
			{Path: ManifestEntryName},
			{Path: mod + "/" + InstallReadmeName},
			{Path: mod + "/" + layeredfs.RomFSRel},
		},
		"load/<title>/<mod> wrapper": {
			{Path: "load/010069401ADB8000/" + mod + "/" + ManifestEntryName},
			{Path: "load/010069401ADB8000/" + mod + "/" + InstallReadmeName},
			{Path: "load/010069401ADB8000/" + mod + "/" + layeredfs.RomFSRel},
		},
		"a second top-level directory": {
			{Path: mod + "/" + ManifestEntryName},
			{Path: mod + "/" + InstallReadmeName},
			{Path: mod + "/" + layeredfs.RomFSRel},
			{Path: "other/" + ManifestEntryName},
		},
		"an unexpected entry inside the mod folder": {
			{Path: mod + "/" + ManifestEntryName},
			{Path: mod + "/" + InstallReadmeName},
			{Path: mod + "/" + layeredfs.RomFSRel},
			{Path: mod + "/extra.txt"},
		},
		"missing manifest": {
			{Path: mod + "/" + InstallReadmeName},
			{Path: mod + "/" + layeredfs.RomFSRel},
		},
	}
	for name, files := range bad {
		if err := verifyEmulatorLayout(files); err == nil {
			t.Fatalf("%s: a malformed emulator layout was accepted", name)
		}
	}
}

// TestPackageRejectsModName: the emulator folder name is fixed by
// layeredfs.EmulatorModDir, so a caller-supplied ModName is refused rather than
// silently ignored (the CLI keeps accepting --mod-name only to report this).
func TestPackageRejectsModName(t *testing.T) {
	res, _ := packageFixture(t)
	_, err := Package(res, PackageOptions{Targets: TargetBoth, ModName: "custom-name"})
	if err == nil {
		t.Fatal("expected a non-empty ModName to be rejected")
	}
	if !strings.Contains(err.Error(), layeredfs.EmulatorModDir) {
		t.Fatalf("the refusal must name the fixed mod folder %q: %v", layeredfs.EmulatorModDir, err)
	}
}

// TestPackageAtmosphereLayoutUnchanged guards the Atmosphère layout while the
// emulator layout moved its entries into a single top-level folder: the
// console archive still has the manifest and install note at its root beside the
// atmosphere/contents/<id>/romfs/Unicorn_US.CPK override, and never a mod folder.
func TestPackageAtmosphereLayoutUnchanged(t *testing.T) {
	res, _ := packageFixture(t)
	pkg, err := Package(res, PackageOptions{Targets: TargetAtmosphere, Version: "1.2.3"})
	if err != nil {
		t.Fatalf("Package: %v", err)
	}
	if len(pkg.Artifacts) != 2 {
		t.Fatalf("expected 2 atmosphere archives, got %d", len(pkg.Artifacts))
	}
	want := []string{
		ManifestEntryName,
		InstallReadmeName,
		layeredfs.AtmospherePath("010069401ADB8000", layeredfs.RomFSRel),
	}
	for _, a := range pkg.Artifacts {
		if a.Target != TargetAtmosphere {
			t.Fatalf("atmosphere-only packaging produced a %s archive", a.Target)
		}
		names := zipNames(t, a.Path)
		if len(names) != len(want) {
			t.Fatalf("archive %s has %d entries, want %d: %v", a.RelPath, len(names), len(want), names)
		}
		for _, p := range want {
			if !containsString(names, p) {
				t.Fatalf("atmosphere archive %s missing %s (got %v)", a.RelPath, p, names)
			}
		}
		joined := strings.Join(names, ",")
		if strings.Contains(joined, layeredfs.EmulatorModDir) || strings.Contains(joined, "load/") {
			t.Fatalf("atmosphere archive %s carries an emulator wrapper: %v", a.RelPath, names)
		}
		m := zipManifest(t, a.Path)
		target, _ := m["target"].(map[string]any)
		if target == nil || target["layout"] != layeredfs.AtmosphereRoot+"/010069401ADB8000" {
			t.Fatalf("atmosphere manifest layout = %v, want %s", target, layeredfs.AtmosphereRoot+"/010069401ADB8000")
		}
	}
}

// TestPackageRecordsStartupLabelAndNeverInjectsIt: the version and the startup
// label are recorded where they belong — the machine manifest, the human install
// note inside each archive, and the external reports — but packaging itself never
// injects the label into a game asset: it assembles archives from an
// already-published build and never rebuilds the CPK. (The label lives in the
// asset because the *build* wrote it into the startup record, not because
// packaging did.)
func TestPackageRecordsStartupLabelAndNeverInjectsIt(t *testing.T) {
	res, _ := packageFixture(t)
	pkg, err := Package(res, PackageOptions{Targets: TargetBoth, Version: "1.2.3"})
	if err != nil {
		t.Fatalf("Package: %v", err)
	}
	const version, label = "1.2.3", "Unburger Mod V1.2.3"
	if !strings.Contains(pkg.SummaryTxt, version) || !strings.Contains(pkg.SummaryTxt, label) {
		t.Fatalf("summary must record the version and startup label:\n%s", pkg.SummaryTxt)
	}
	for name, body := range map[string]string{
		"PACKAGE_REPORT.json": pkg.ReportJSON,
		"PACKAGE_REPORT.txt":  pkg.ReportTxt,
	} {
		if !strings.Contains(body, version) || !strings.Contains(body, label) {
			t.Fatalf("%s must record version %q and label %q:\n%s", name, version, label, body)
		}
	}
	for _, a := range pkg.Artifacts {
		zr, err := zip.OpenReader(a.Path)
		if err != nil {
			t.Fatal(err)
		}
		var sawManifest, sawReadme, sawAsset bool
		for _, f := range zr.File {
			rc, err := f.Open()
			if err != nil {
				zr.Close()
				t.Fatal(err)
			}
			data, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				zr.Close()
				t.Fatal(err)
			}
			switch {
			case strings.HasSuffix(f.Name, ManifestEntryName):
				sawManifest = true
				if !bytes.Contains(data, []byte(label)) {
					zr.Close()
					t.Fatalf("%s: manifest does not record the startup label", a.RelPath)
				}
			case strings.HasSuffix(f.Name, InstallReadmeName):
				sawReadme = true
				if !bytes.Contains(data, []byte(label)) {
					zr.Close()
					t.Fatalf("%s: install note does not record the startup label", a.RelPath)
				}
			case strings.HasSuffix(f.Name, "Unicorn_US.CPK"):
				sawAsset = true
				if bytes.Contains(data, []byte(label)) {
					zr.Close()
					t.Fatalf("%s: packaging injected the startup label into the game asset (it must not rebuild a CPK)", a.RelPath)
				}
			}
		}
		zr.Close()
		if !sawManifest || !sawReadme || !sawAsset {
			t.Fatalf("%s: expected manifest, install note and game asset entries", a.RelPath)
		}
	}
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func TestPackageDeterministic(t *testing.T) {
	res, dir := packageFixture(t)
	first, err := Package(res, PackageOptions{Targets: TargetBoth, OutDir: filepath.Join(dir, "packages-a")})
	if err != nil {
		t.Fatalf("Package a: %v", err)
	}
	second, err := Package(res, PackageOptions{Targets: TargetBoth, OutDir: filepath.Join(dir, "packages-b"), VerifyDeterminism: true})
	if err != nil {
		t.Fatalf("Package b: %v", err)
	}
	if len(first.Artifacts) != len(second.Artifacts) {
		t.Fatal("archive count differs")
	}
	for i := range first.Artifacts {
		a, b := first.Artifacts[i], second.Artifacts[i]
		if a.RelPath != b.RelPath {
			t.Fatalf("archive %d name differs: %s vs %s", i, a.RelPath, b.RelPath)
		}
		ba, err := os.ReadFile(a.Path)
		if err != nil {
			t.Fatal(err)
		}
		bb, err := os.ReadFile(b.Path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(ba, bb) {
			t.Fatalf("%s is not byte-identical across two packaging runs", a.RelPath)
		}
	}
}

func TestPackageMissingArtifactLeavesNothing(t *testing.T) {
	res, _ := packageFixture(t)
	// Remove one edition's CPK after it was built.
	for _, e := range res.Editions {
		if e.Variant == VariantNoHonorifics {
			if err := os.Remove(e.ArtifactPath); err != nil {
				t.Fatal(err)
			}
		}
	}
	pkg, err := Package(res, PackageOptions{Targets: TargetBoth})
	if err == nil {
		t.Fatalf("expected Package to fail, got %+v", pkg)
	}
	outDir := filepath.Join(res.RunDir, "packages")
	entries, rerr := os.ReadDir(outDir)
	if rerr == nil {
		for _, e := range entries {
			t.Fatalf("failed package left output %q", e.Name())
		}
	}
}

func TestPackageVoiceArtifact(t *testing.T) {
	dir := t.TempDir()
	cpk := filepath.Join(dir, "honorifics", "romfs", "Unicorn_US.CPK")
	mustWrite(t, cpk, []byte("edited cpk bytes"))
	mainPath := filepath.Join(dir, "voice", "exefs", "main")
	mustWrite(t, mainPath, []byte("patched nso bytes"))
	ipsPath := filepath.Join(dir, "voice", "main.ips")
	mustWrite(t, ipsPath, []byte("ips bytes"))

	mainSum, mainSize, _ := hashFileSHA256(mainPath)
	ipsSum, ipsSize, _ := hashFileSHA256(ipsPath)
	res := &Result{
		RunDir:  dir,
		TitleID: "010069401ADB8000",
		Facts:   &SourceFacts{SHA256: strings.Repeat("a", 64), Size: 100},
		Editions: []EditionResult{{
			Variant: VariantHonorifics, ArtifactRel: layeredfs.RomFSRel, ArtifactPath: cpk,
		}},
		Voice: &VoiceResult{
			BuildID:        "c841ffe2717ff03a13990480c51da73f091c04fa",
			ArtifactRel:    layeredfs.VoiceExeFSRel,
			ArtifactSHA256: mainSum,
			ArtifactSize:   mainSize,
			IPSSHA256:      ipsSum,
			IPSSize:        ipsSize,
			RuntimeStatus:  "NOT_RUNTIME_APPROVED",
		},
	}
	res.Dataset.CanonicalSHA256 = strings.Repeat("b", 64)
	res.Dataset.CanonicalRows = 7
	pkg, err := Package(res, PackageOptions{Variants: []Variant{VariantHonorifics}, Targets: TargetBoth, IncludeVoice: true})
	if err != nil {
		t.Fatalf("Package: %v", err)
	}
	if len(pkg.Artifacts) != 2 {
		t.Fatalf("expected 2 archives, got %d", len(pkg.Artifacts))
	}
	for _, a := range pkg.Artifacts {
		var voiceName string
		switch a.Target {
		case TargetAtmosphere:
			voiceName = layeredfs.AtmospherePath("010069401ADB8000", layeredfs.VoiceExeFSRel)
		case TargetEmulator:
			voiceName = layeredfs.EmulatorZipEntry(layeredfs.VoiceExeFSRel)
		}
		if !strings.Contains(","+strings.Join(zipNames(t, a.Path), ",")+",", ","+voiceName+",") {
			t.Fatalf("archive %s is missing the voice override %s", a.RelPath, voiceName)
		}
		m := zipManifest(t, a.Path)
		v, _ := m["voice"].(map[string]any)
		if v == nil || v["build_id"] != "c841ffe2717ff03a13990480c51da73f091c04fa" {
			t.Fatalf("manifest voice = %v", v)
		}
		if v["runtime_status"] != "NOT_RUNTIME_APPROVED" {
			t.Fatalf("voice runtime status = %v", v["runtime_status"])
		}
	}
}

func TestPackageRejectsTamperedVoice(t *testing.T) {
	dir := t.TempDir()
	cpk := filepath.Join(dir, "honorifics", "romfs", "Unicorn_US.CPK")
	mustWrite(t, cpk, []byte("edited cpk bytes"))
	mainPath := filepath.Join(dir, "voice", "exefs", "main")
	mustWrite(t, mainPath, []byte("patched nso bytes"))
	res := &Result{
		RunDir:   dir,
		TitleID:  "010069401ADB8000",
		Editions: []EditionResult{{Variant: VariantHonorifics, ArtifactPath: cpk, ArtifactRel: layeredfs.RomFSRel}},
		Voice:    &VoiceResult{ArtifactSHA256: strings.Repeat("0", 64), ArtifactSize: 3},
	}
	if _, err := Package(res, PackageOptions{Variants: []Variant{VariantHonorifics}, IncludeVoice: true}); err == nil {
		t.Fatal("expected a voice sha256 mismatch to be rejected")
	}
}

func TestLoadRunRepackagesPublishedRun(t *testing.T) {
	res, _ := packageFixture(t)
	loaded, err := LoadRun(res.RunDir, nil)
	if err != nil {
		t.Fatalf("LoadRun: %v", err)
	}
	if len(loaded.Editions) != 2 {
		t.Fatalf("LoadRun found %d editions, want 2", len(loaded.Editions))
	}
	pkg, err := Package(loaded, PackageOptions{Targets: TargetAtmosphere})
	if err != nil {
		t.Fatalf("Package from loaded run: %v", err)
	}
	if len(pkg.Artifacts) != 2 {
		t.Fatalf("expected 2 archives, got %d", len(pkg.Artifacts))
	}
	if !isHex12(pkg.Release) {
		t.Fatalf("release %q is not a 12-hex neutral label", pkg.Release)
	}
	// The report's dataset identity is only present for the neutral table
	// layout; when it is, the release must be its prefix.
	if loaded.Dataset.CanonicalSHA256 != "" {
		if want := shortHex(loaded.Dataset.CanonicalSHA256, 12); pkg.Release != want {
			t.Fatalf("release = %q, want dataset-derived %q", pkg.Release, want)
		}
	}
}

func TestReleaseLabelPrefersDatasetIdentity(t *testing.T) {
	res := &Result{Editions: []EditionResult{
		{Variant: VariantHonorifics, SHA256: strings.Repeat("1", 64)},
		{Variant: VariantNoHonorifics, SHA256: strings.Repeat("2", 64)},
	}}
	res.Dataset.CanonicalSHA256 = "ABCDEF0123456789" + strings.Repeat("f", 48)
	if got := releaseLabel(res); got != "abcdef012345" {
		t.Fatalf("releaseLabel = %q, want the dataset prefix", got)
	}
	res.Dataset.CanonicalSHA256 = ""
	a := releaseLabel(res)
	b := releaseLabel(res)
	if !isHex12(a) || a != b {
		t.Fatalf("fallback release is not a stable 12-hex label: %q vs %q", a, b)
	}
	res.Editions = nil
	if releaseLabel(res) != "" {
		t.Fatal("releaseLabel should be empty without any identity")
	}
}

func isHex12(s string) bool {
	if len(s) != 12 {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func TestPackageRollsBackPartialSet(t *testing.T) {
	res, _ := packageFixture(t)
	outDir := filepath.Join(res.RunDir, "packages")
	release := releaseLabel(res)
	// Pre-create a directory where the second archive must land, so publishing
	// the first succeeds and the second fails; the whole set must roll back.
	blocker := filepath.Join(outDir, "unicorn-overlord-honorifics-emulator-"+release+".zip")
	if err := os.MkdirAll(blocker, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Package(res, PackageOptions{Targets: TargetBoth}); err == nil {
		t.Fatal("expected packaging to fail when the destination is a directory")
	}
	if _, err := os.Stat(filepath.Join(outDir, "unicorn-overlord-honorifics-atmosphere-"+release+".zip")); !os.IsNotExist(err) {
		t.Fatal("the already-published archive was not rolled back")
	}
	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if name == filepath.Base(blocker) {
			continue
		}
		t.Fatalf("failed packaging left %q behind", name)
	}
}

func TestPackageRejectsUnknownTitleID(t *testing.T) {
	res, _ := packageFixture(t)
	res.TitleID = "0000000000000000"
	if _, err := Package(res, PackageOptions{}); err == nil {
		t.Fatal("expected an unsupported TitleID to be rejected")
	}
}

// TestPackageDefaultsBothAndReleaseDir: an empty target selection packages both
// layouts, and when the run directory sits inside a checkout the default output
// directory is the project release directory (.local/release).
func TestPackageDefaultsBothAndReleaseDir(t *testing.T) {
	res, dir := packageFixture(t)
	// Promote the fixture directory into a checkout root so FindRootFrom resolves
	// it: go.mod plus the public translation marker.
	mustWrite(t, filepath.Join(dir, "go.mod"), []byte("module example\n"))
	mustWrite(t, filepath.Join(dir, "translations", "honorifics.tsv"), []byte("record_id\n"))

	pkg, err := Package(res, PackageOptions{})
	if err != nil {
		t.Fatalf("Package: %v", err)
	}
	if len(pkg.Artifacts) != 4 {
		t.Fatalf("default target should package both layouts for two editions: got %d archives", len(pkg.Artifacts))
	}
	wantRoot := filepath.Join(dir, ".local", "release")
	if pkg.OutDir != wantRoot {
		t.Fatalf("default out dir = %q, want %q", pkg.OutDir, wantRoot)
	}
	for _, a := range pkg.Artifacts {
		if filepath.Dir(a.Path) != wantRoot {
			t.Fatalf("archive %s landed in %q, want %q", a.RelPath, filepath.Dir(a.Path), wantRoot)
		}
	}
	seen := map[PackageTarget]bool{}
	for _, a := range pkg.Artifacts {
		seen[a.Target] = true
	}
	if !seen[TargetAtmosphere] || !seen[TargetEmulator] {
		t.Fatalf("default packaging did not cover both layouts: %v", seen)
	}
}
