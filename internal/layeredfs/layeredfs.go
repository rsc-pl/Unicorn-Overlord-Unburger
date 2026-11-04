// Package layeredfs is the single source of truth for the LayeredFS-relative
// locations this project's outputs use. A path is therefore written in exactly
// one place, and a build report, the installer and the packager can never
// disagree about where an override belongs.
//
// Two loader families are named here:
//
//   - Atmosphère (console CFW) reads, under its contents directory,
//     <title id>/romfs/... for RomFS overrides and <title id>/exefs/... for
//     executable (NSO) replacements. Its build-id-gated IPS patches live under
//     exefs_patches/<patch set>/<build id>.ips.
//   - The Eden/Yuzu family of emulators reads mod folders under load/, shaped
//     load/<title id>/<mod name>/{romfs,exefs}/.... An emulator archive we ship
//     is *not* a load/ tree: it holds exactly one top-level directory, the mod
//     folder itself (EmulatorModDir), which the operator extracts into the
//     emulator's load/<title id>/ directory.
//
// Nothing here touches the filesystem: the package only names paths.
package layeredfs

import (
	"path"
	"strings"
)

const (
	// RomFSRel is the RomFS override path, relative to a title's contents or
	// mod directory. The localization archive keeps its original RomFS name, so
	// the override replaces exactly one already-present file.
	RomFSRel = "romfs/Unicorn_US.CPK"
	// VoiceExeFSRel is the ExeFS replacement path of the shared voice `main`,
	// relative to a title's contents or mod directory.
	VoiceExeFSRel = "exefs/main"
	// AtmosphereRoot is the Atmosphère contents prefix on an SD card.
	AtmosphereRoot = "atmosphere/contents"
	// EmulatorRoot is the emulator mod prefix under the emulator's data
	// directory. It is used to describe where the mod folder belongs on the
	// operator's machine (install instructions), never as an archive entry.
	EmulatorRoot = "load"
	// EmulatorModDir is the name of the single, exactly spelled top-level
	// directory every emulator archive must contain. It is the mod folder the
	// operator ends up with, so the archive never carries a
	// load/<title id>/<mod name> wrapper: extracting the archive into the
	// emulator's mod directory (load/<title id>/) creates this folder there.
	// The spelling — including the spaces and the hyphen — is fixed and is the
	// single source of truth; no caller may substitute another name.
	EmulatorModDir = "Unicorn Overlord - Unburger Mod"
	// ExefsPatchesRoot is the Atmosphère build-id-gated IPS patch prefix.
	ExefsPatchesRoot = "atmosphere/exefs_patches"
)

// AtmospherePath joins a title's Atmosphère override path with rel.
func AtmospherePath(titleID, rel string) string {
	return path.Join(AtmosphereRoot, titleID, rel)
}

// EmulatorPath joins a title's emulator mod path with rel.
func EmulatorPath(titleID, modName, rel string) string {
	return path.Join(EmulatorRoot, titleID, modName, rel)
}

// EmulatorZipEntry is the archive-relative path of rel inside an emulator mod
// ZIP. Every emulator archive entry — the RomFS override, the optional ExeFS
// override, the install note and the manifest — sits inside the one top-level
// mod directory, so extracting the archive yields exactly one folder and no
// load/<title id>/<mod name> wrapper. EmulatorPath describes where that folder
// belongs on the operator's machine; this function names the entry.
func EmulatorZipEntry(rel string) string {
	return path.Join(EmulatorModDir, rel)
}

// ExefsPatchPath is the Atmosphère build-id-gated IPS location for a patch set.
func ExefsPatchPath(patchSet, buildID string) string {
	return path.Join(ExefsPatchesRoot, patchSet, ExefsPatchName(buildID)+".ips")
}

// ExefsPatchName renders an NSO build id the way Atmosphère's exefs_patches
// lookup accepts it: the hexadecimal build id with trailing zero bytes left
// off. A non-hex or empty id is returned trimmed but otherwise unchanged.
func ExefsPatchName(buildID string) string {
	s := strings.ToLower(strings.TrimSpace(buildID))
	for len(s) >= 2 && strings.HasSuffix(s, "00") {
		s = s[:len(s)-2]
	}
	return s
}
