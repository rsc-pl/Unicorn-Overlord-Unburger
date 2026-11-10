package cli

import (
	"fmt"
	"io"

	"uo-unburger/internal/build"
	"uo-unburger/internal/layeredfs"
	"uo-unburger/internal/paths"
	"uo-unburger/internal/project"
)

// runPackage implements `uo-unburger package`. It assembles install-ready ZIP
// archives from an already-published build run (the per-edition BUILD_REPORT
// files plus the optional shared voice stage) and never rebuilds a CPK. It is
// the same service as `build --package`, called without a build.
func runPackage(args []string, out, errOut io.Writer) int {
	runDir := ""
	variantWord := string(build.VariantBoth)
	targetWord := ""
	releaseName := ""
	modName := ""
	outDir := ""
	force := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--run":
			if i+1 >= len(args) {
				return packageUsage(errOut, "--run requires a value")
			}
			i++
			runDir = args[i]
		case "--variant":
			if i+1 >= len(args) {
				return packageUsage(errOut, "--variant requires a value")
			}
			i++
			variantWord = args[i]
		case "--target":
			if i+1 >= len(args) {
				return packageUsage(errOut, "--target requires a value")
			}
			i++
			targetWord = args[i]
		case "--release":
			if i+1 >= len(args) {
				return packageUsage(errOut, "--release requires a value")
			}
			i++
			releaseName = args[i]
		case "--mod-name":
			if i+1 >= len(args) {
				return packageUsage(errOut, "--mod-name requires a value")
			}
			i++
			modName = args[i]
		case "--out":
			if i+1 >= len(args) {
				return packageUsage(errOut, "--out requires a value")
			}
			i++
			outDir = args[i]
		case "--force":
			force = true
		default:
			return packageUsage(errOut, fmt.Sprintf("unknown package argument %q", args[i]))
		}
	}
	if runDir == "" {
		return packageUsage(errOut, "package requires --run <build-run-directory>")
	}
	if modName != "" {
		fmt.Fprintf(errOut, "package: --mod-name is not supported: an emulator archive always extracts into the single folder %q\n", layeredfs.EmulatorModDir)
		return exitUsage
	}
	variant, err := build.ParseVariant(variantWord)
	if err != nil {
		return packageUsage(errOut, err.Error())
	}
	var variants []build.Variant
	if variant != build.VariantBoth {
		variants = []build.Variant{variant}
	}
	var target build.PackageTarget
	if targetWord != "" {
		t, err := build.ParsePackageTarget(targetWord)
		if err != nil {
			return packageUsage(errOut, err.Error())
		}
		target = t
	}

	// Packaging produces the user-facing release, so the version is mandatory and
	// read from the checkout root's version.txt before anything is assembled.
	root, rootErr := project.FindRoot()
	if rootErr != nil {
		fmt.Fprintf(errOut, "%v (%v)\n", build.ErrVersionMissing, rootErr)
		return exitFail
	}
	version, verr := build.ReadVersion(root)
	if verr != nil {
		fmt.Fprintf(errOut, "%v\n", verr)
		return exitFail
	}
	if outDir == "" {
		outDir = project.ReleaseDir(root)
	}

	res, err := build.LoadRun(runDir, variants)
	if err != nil {
		fmt.Fprintf(errOut, "%v\n", err)
		return exitFail
	}
	pkg, err := build.Package(res, build.PackageOptions{
		Variants:  variants,
		Targets:   target,
		OutDir:    outDir,
		Version:   version,
		Release:   releaseName,
		Overwrite: force,
	})
	if err != nil {
		fmt.Fprintf(errOut, "%v\n", err)
		return exitFail
	}
	fmt.Fprint(out, pkg.SummaryTxt)
	fmt.Fprintf(out, "report:             %s\n", paths.Display(pkg.ReportPath))
	return exitOK
}

func packageUsage(errOut io.Writer, reason string) int {
	fmt.Fprintf(errOut, "%s\n", reason)
	fmt.Fprintln(errOut, "usage: uo-unburger package --run <build-run-directory> [--variant honorifics|no-honorifics|both]")
	fmt.Fprintln(errOut, "       [--target atmosphere|emulator|both] [--release <label>] [--out <dir>]")
	fmt.Fprintln(errOut, "       [--force]  replace existing ZIPs and report; without it an existing")
	fmt.Fprintln(errOut, "                  output is refused and every colliding file is named")
	fmt.Fprintln(errOut, "Assembles install-ready ZIPs from an already-published run; it rebuilds nothing.")
	fmt.Fprintln(errOut, "Default target both; ZIPs are named with the root version.txt and written to")
	fmt.Fprintln(errOut, ".local/release. --release only labels the manifests and install notes.")
	return exitUsage
}
