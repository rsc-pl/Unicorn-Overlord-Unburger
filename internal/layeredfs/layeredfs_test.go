package layeredfs

import (
	"strings"
	"testing"
)

// TestAtmospherePathHasNoDoubledSegment guards the regression that motivated
// this package: joining a path that already carries "romfs/" onto a
// "romfs/"-suffixed prefix produced "atmosphere/contents/<id>/romfs/romfs/...".
func TestAtmospherePathHasNoDoubledSegment(t *testing.T) {
	got := AtmospherePath("010069401ADB8000", RomFSRel)
	want := "atmosphere/contents/010069401ADB8000/romfs/Unicorn_US.CPK"
	if got != want {
		t.Fatalf("AtmospherePath = %q, want %q", got, want)
	}
}

func TestEmulatorPath(t *testing.T) {
	got := EmulatorPath("010069401ADB8800", "uo-unburger", VoiceExeFSRel)
	want := "load/010069401ADB8800/uo-unburger/exefs/main"
	if got != want {
		t.Fatalf("EmulatorPath = %q, want %q", got, want)
	}
}

// TestEmulatorZipEntryModDir: an emulator mod ZIP contains exactly one
// top-level directory — the mod folder itself — and every entry lives inside
// it, with no load/<title>/<mod> wrapper.
func TestEmulatorZipEntryModDir(t *testing.T) {
	if EmulatorModDir != "Unicorn Overlord - Unburger Mod" {
		t.Fatalf("EmulatorModDir = %q, want the exact mod folder name", EmulatorModDir)
	}
	if got, want := EmulatorZipEntry(RomFSRel), "Unicorn Overlord - Unburger Mod/romfs/Unicorn_US.CPK"; got != want {
		t.Fatalf("EmulatorZipEntry(RomFSRel) = %q, want %q", got, want)
	}
	if got, want := EmulatorZipEntry(VoiceExeFSRel), "Unicorn Overlord - Unburger Mod/exefs/main"; got != want {
		t.Fatalf("EmulatorZipEntry(VoiceExeFSRel) = %q, want %q", got, want)
	}
	for _, rel := range []string{"manifest.json", "README-install.txt"} {
		if got, want := EmulatorZipEntry(rel), EmulatorModDir+"/"+rel; got != want {
			t.Fatalf("EmulatorZipEntry(%q) = %q, want %q", rel, got, want)
		}
	}
	for _, bad := range []string{"load/", "atmosphere/", "010069401ADB8000"} {
		if got := EmulatorZipEntry(RomFSRel); strings.Contains(got, bad) {
			t.Fatalf("EmulatorZipEntry(RomFSRel) = %q must not contain %q", got, bad)
		}
	}
}

func TestExefsPatchNameTrimsTrailingZeroBytes(t *testing.T) {
	in := "c841ffe2717ff03a13990480c51da73f091c04fa000000000000000000000000"
	want := "c841ffe2717ff03a13990480c51da73f091c04fa"
	if got := ExefsPatchName(in); got != want {
		t.Fatalf("ExefsPatchName = %q, want %q", got, want)
	}
}

func TestExefsPatchPath(t *testing.T) {
	got := ExefsPatchPath("uo-unburger-voice", "c841ffe2717ff03a13990480c51da73f091c04fa00000000")
	want := "atmosphere/exefs_patches/uo-unburger-voice/c841ffe2717ff03a13990480c51da73f091c04fa.ips"
	if got != want {
		t.Fatalf("ExefsPatchPath = %q, want %q", got, want)
	}
}
