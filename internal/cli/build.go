package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"uo-unburger/internal/appconfig"
	"uo-unburger/internal/build"
	"uo-unburger/internal/layeredfs"
	"uo-unburger/internal/paths"
	"uo-unburger/internal/project"
)

// runBuild implements `uo-unburger build`. It validates the source CPK, resolves
// the translation files through the revision-agnostic importer, rebuilds every
// requested edition from one shared intermediate state, and publishes each
// edition atomically. It never modifies the source archive and never installs
// anything.
func runBuild(args []string, out, errOut io.Writer) int {
	variantWord := ""
	sourcePath := ""
	corpusPath := ""
	translationsDir := "translations"
	outDir := ""
	runName := ""
	titleID := ""
	displayVersion := ""
	sourceSHA := ""
	strictTokens := false
	dryRun := false
	allowExtract := false
	voice := false
	voiceMain := ""
	packageFlag := false
	force := false
	targetWord := ""
	releaseName := ""
	modName := ""

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--variant":
			if i+1 >= len(args) {
				return buildUsage(errOut, "--variant requires a value")
			}
			i++
			variantWord = args[i]
		case "--cpk":
			if i+1 >= len(args) {
				return buildUsage(errOut, "--cpk requires a value")
			}
			i++
			sourcePath = args[i]
		case "--corpus":
			if i+1 >= len(args) {
				return buildUsage(errOut, "--corpus requires a value")
			}
			i++
			corpusPath = args[i]
		case "--translations":
			if i+1 >= len(args) {
				return buildUsage(errOut, "--translations requires a value")
			}
			i++
			translationsDir = args[i]
		case "--out":
			if i+1 >= len(args) {
				return buildUsage(errOut, "--out requires a value")
			}
			i++
			outDir = args[i]
		case "--run-name":
			if i+1 >= len(args) {
				return buildUsage(errOut, "--run-name requires a value")
			}
			i++
			runName = args[i]
		case "--title-id":
			if i+1 >= len(args) {
				return buildUsage(errOut, "--title-id requires a value")
			}
			i++
			titleID = args[i]
		case "--display-version":
			if i+1 >= len(args) {
				return buildUsage(errOut, "--display-version requires a value")
			}
			i++
			displayVersion = args[i]
		case "--source-sha256":
			if i+1 >= len(args) {
				return buildUsage(errOut, "--source-sha256 requires a value")
			}
			i++
			sourceSHA = args[i]
		case "--strict-tokens":
			strictTokens = true
		case "--dry-run":
			dryRun = true
		case "--extract":
			allowExtract = true
		case "--voice":
			voice = true
		case "--voice-main":
			if i+1 >= len(args) {
				return buildUsage(errOut, "--voice-main requires a value")
			}
			i++
			voiceMain = args[i]
		case "--package":
			packageFlag = true
		case "--force":
			force = true
		case "--target":
			if i+1 >= len(args) {
				return buildUsage(errOut, "--target requires a value")
			}
			i++
			targetWord = args[i]
		case "--release":
			if i+1 >= len(args) {
				return buildUsage(errOut, "--release requires a value")
			}
			i++
			releaseName = args[i]
		case "--mod-name":
			if i+1 >= len(args) {
				return buildUsage(errOut, "--mod-name requires a value")
			}
			i++
			modName = args[i]
		default:
			return buildUsage(errOut, fmt.Sprintf("unknown build argument %q", args[i]))
		}
	}

	if variantWord == "" {
		return buildUsage(errOut, "build requires --variant honorifics|no-honorifics|both")
	}
	if modName != "" {
		fmt.Fprintf(errOut, "build: --mod-name is not supported: an emulator archive always extracts into the single folder %q\n", layeredfs.EmulatorModDir)
		return exitUsage
	}
	if voiceMain != "" && !voice {
		return buildUsage(errOut, "--voice-main requires --voice")
	}
	if voice && voiceMain == "" {
		return buildUsage(errOut, "--voice requires the extracted ExeFS main: --voice-main <path>")
	}
	if voice {
		fmt.Fprintln(errOut, "WARNING: --voice builds the EXPERIMENTAL, Build-ID-gated Japanese-voice ExeFS patch.")
		fmt.Fprintln(errOut, "         It is NOT runtime-approved: it has never been installed or run. In-game validation is still required.")
	}
	if packageFlag && dryRun {
		return buildUsage(errOut, "--package requires a real build; remove --dry-run")
	}
	var packageTarget build.PackageTarget
	if targetWord != "" {
		t, err := build.ParsePackageTarget(targetWord)
		if err != nil {
			return buildUsage(errOut, err.Error())
		}
		packageTarget = t
	}
	variant, err := build.ParseVariant(variantWord)
	if err != nil {
		return buildUsage(errOut, err.Error())
	}
	sourcePath, provenance, err := acquireSource(context.Background(), loadConfig(), sourcePath, allowExtract)
	if err != nil {
		fmt.Fprintf(errOut, "%v\n", err)
		return exitFail
	}

	// A packaged release must carry a real version read from the checkout root's
	// version.txt; a plain build (no --package) and a dry run may proceed without
	// one and report no version. The check runs before the build reads the
	// source, so a missing version leaves no partial output.
	root, rootErr := project.FindRoot()
	version := ""
	if rootErr == nil {
		if v, verr := build.ReadVersion(root); verr == nil {
			version = v
		} else if packageFlag {
			fmt.Fprintf(errOut, "%v\n", verr)
			return exitFail
		}
	} else if packageFlag {
		fmt.Fprintf(errOut, "%v (%v)\n", build.ErrVersionMissing, rootErr)
		return exitFail
	}

	res, err := build.Run(build.Options{
		Variant:         variant,
		SourcePath:      sourcePath,
		CorpusPath:      corpusPath,
		TranslationsDir: translationsDir,
		OutDir:          outDir,
		RunName:         runName,
		TitleID:         titleID,
		DisplayVersion:  displayVersion,
		Expect:          build.SourceExpectation{SHA256: sourceSHA},
		StrictTokens:    strictTokens,
		DryRun:          dryRun,
		Voice:           voice,
		VoiceMain:       voiceMain,
		Version:         version,
		RequireVersion:  packageFlag,
	})
	if err != nil {
		fmt.Fprintf(errOut, "%v\n", err)
		return exitFail
	}

	if provenance != "flag" {
		fmt.Fprintf(out, "source archive: %s (from %s)\n", paths.Display(sourcePath), provenance)
	}
	fmt.Fprint(out, res.SummaryTxt)
	if dryRun {
		for _, e := range res.Editions {
			fmt.Fprintln(out)
			fmt.Fprint(out, e.ReportTxt)
		}
	}
	if packageFlag {
		pkgOut := ""
		if root != "" {
			pkgOut = project.ReleaseDir(root)
		}
		pkg, err := build.Package(res, build.PackageOptions{
			Targets:      packageTarget,
			IncludeVoice: voice,
			OutDir:       pkgOut,
			Version:      version,
			Release:      releaseName,
			Overwrite:    force,
		})
		if err != nil {
			fmt.Fprintf(errOut, "%v\n", err)
			return exitFail
		}
		fmt.Fprint(out, pkg.SummaryTxt)
		fmt.Fprintf(out, "report:             %s\n", paths.Display(pkg.ReportPath))
	}
	return exitOK
}

// runValidateSource implements `uo-unburger validate-source`, the read-only
// hash/identity/version gate used before any build.
func runValidateSource(args []string, out, errOut io.Writer) int {
	sourcePath := ""
	titleID := ""
	displayVersion := ""
	sourceSHA := ""

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--cpk":
			if i+1 >= len(args) {
				return buildUsage(errOut, "--cpk requires a value")
			}
			i++
			sourcePath = args[i]
		case "--title-id":
			if i+1 >= len(args) {
				return buildUsage(errOut, "--title-id requires a value")
			}
			i++
			titleID = args[i]
		case "--display-version":
			if i+1 >= len(args) {
				return buildUsage(errOut, "--display-version requires a value")
			}
			i++
			displayVersion = args[i]
		case "--source-sha256":
			if i+1 >= len(args) {
				return buildUsage(errOut, "--source-sha256 requires a value")
			}
			i++
			sourceSHA = args[i]
		default:
			return buildUsage(errOut, fmt.Sprintf("unknown validate-source argument %q", args[i]))
		}
	}
	if sourcePath == "" {
		return buildUsage(errOut, "validate-source requires --cpk <path>")
	}

	facts, err := build.ValidateSource(sourcePath, build.SourceExpectation{
		TitleID:        titleID,
		DisplayVersion: displayVersion,
		SHA256:         sourceSHA,
	})
	if err != nil {
		fmt.Fprintf(errOut, "%v\n", err)
		return exitFail
	}
	title := titleID
	if title == "" {
		title = "UNASSERTED"
	}
	version := displayVersion
	if version == "" {
		version = "UNASSERTED"
	}
	fmt.Fprintf(out, "validate-source: OK\n")
	fmt.Fprintf(out, "  path:               %s\n", paths.Display(facts.Path))
	fmt.Fprintf(out, "  size:               %d\n", facts.Size)
	fmt.Fprintf(out, "  sha256:             %s\n", facts.SHA256)
	fmt.Fprintf(out, "  members:            %d (fms %d)\n", facts.Members, facts.FMSMembers)
	fmt.Fprintf(out, "  member-list sha256: %s\n", facts.MemberListSHA256)
	fmt.Fprintf(out, "  align:              %d\n", facts.Align)
	fmt.Fprintf(out, "  title id:           %s\n", title)
	fmt.Fprintf(out, "  display version:    %s\n", version)
	fmt.Fprintf(out, "  supported ids:      %s\n", strings.Join(build.SupportedTitleIDs(), ", "))
	fmt.Fprintf(out, "  supported versions: %s\n", strings.Join(build.SupportedDisplayVersions(), ", "))
	return exitOK
}

func buildUsage(errOut io.Writer, reason string) int {
	fmt.Fprintf(errOut, "%s\n", reason)
	fmt.Fprintln(errOut, "usage: uo-unburger build --variant honorifics|no-honorifics|both --cpk <path>")
	fmt.Fprintln(errOut, "       [--corpus <path>]    advanced: reference JP/EN corpus (optional)")
	fmt.Fprintln(errOut, "       [--translations translations] [--out .local/work/builds]")
	fmt.Fprintln(errOut, "       [--title-id 010069401ADB8000] [--display-version 1.05] [--source-sha256 <hex>]")
	fmt.Fprintln(errOut, "       [--strict-tokens] [--dry-run] [--run-name <name>]")
	fmt.Fprintln(errOut, "       [--extract]  extract the source from a game container when the cache has no entry")
	fmt.Fprintln(errOut, "       [--voice --voice-main <main>]  EXPERIMENTAL, not runtime-approved: add the shared")
	fmt.Fprintln(errOut, "                                     Build-ID-gated Japanese-voice ExeFS patch built from <main>")
	fmt.Fprintln(errOut, "       [--package] [--target atmosphere|emulator|both] [--release <label>]")
	fmt.Fprintln(errOut, "                                     assemble install-ready ZIPs from the published run")
	fmt.Fprintln(errOut, "                                     (default target both; ZIPs land in .local/release)")
	fmt.Fprintln(errOut, "                                     a packaged build needs a root version.txt")
	fmt.Fprintln(errOut, "       [--force]                     replace existing ZIPs/report in the output folder")
	fmt.Fprintln(errOut, "                                     (without it an existing output is refused by name)")
	fmt.Fprintln(errOut, "       uo-unburger validate-source --cpk <path> [--title-id <id>] [--display-version <v>] [--source-sha256 <hex>]")
	fmt.Fprintln(errOut, "       uo-unburger package --run <run-dir> [--variant honorifics|no-honorifics|both]")
	fmt.Fprintln(errOut, "                                     [--target atmosphere|emulator|both] [--voice] [--release <label>]")
	fmt.Fprintln(errOut, "                                     [--out <dir>] [--force]")
	return exitUsage
}

// configuredSource returns the source archive remembered in the project
// configuration, or "" when none is configured. It is only consulted when the
// --cpk flag was not supplied, so an explicit flag always wins.
func configuredSource() string {
	cfg, _, err := appconfig.Load()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cfg.SourceCPK)
}
