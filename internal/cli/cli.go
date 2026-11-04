package cli

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/debug"
	"strings"

	"uo-unburger/internal/appconfig"
	"uo-unburger/internal/build"
	"uo-unburger/internal/project"
	"uo-unburger/internal/tui"
)

// fallbackVersion is reported when no checkout root (and therefore no
// version.txt) can be located, for example by a relocated standalone binary.
const fallbackVersion = "0.0.0-dev"

// Version returns the toolkit version. The checkout-root version.txt is the
// single authoritative source — it is the same value a packaged build stamps —
// so the CLI, the interactive header and the build/package reports cannot drift
// apart. A binary run outside a checkout falls back to fallbackVersion.
func Version() string {
	if root, err := project.FindRoot(); err == nil {
		if v, err := build.ReadVersion(root); err == nil {
			return v
		}
	}
	return fallbackVersion
}

// Exit codes are stable and documented in the help text:
//
//	0  success
//	1  runtime failure (a read, validation or write could not complete)
//	2  usage error (unknown command, bad flag, missing required argument)
const (
	exitOK    = 0
	exitFail  = 1
	exitUsage = 2
)

func Run(args []string, out, errOut io.Writer) int {
	// Import a configuration written by an earlier version once, best-effort. A
	// failure here must never block the command the operator asked for.
	_, _, _ = appconfig.MigrateLegacy()

	if len(args) == 0 {
		// With a terminal, no arguments opens the interactive interface. Without
		// one (a pipe, a script, a cron job), print the help and return the usage
		// exit code instead of emitting control sequences into the stream.
		if tui.IsTerminal(os.Stdin) && tui.IsTerminal(os.Stdout) {
			return runTUI(nil, errOut)
		}
		fmt.Fprintln(errOut, "uo-unburger: no terminal detected; showing help instead of the interactive interface.")
		printHelp(errOut)
		return exitUsage
	}

	switch args[0] {
	case "help", "--help", "-h":
		if len(args) != 1 {
			return usageError(errOut)
		}
		printHelp(out)
		return exitOK
	case "tui":
		return runTUI(args[1:], errOut)
	case "version", "--version":
		switch {
		case len(args) == 1:
			fmt.Fprintln(out, Version())
			return exitOK
		case len(args) == 2 && (args[1] == "--verbose" || args[1] == "-v"):
			printVersionVerbose(out)
			return exitOK
		default:
			return usageError(errOut)
		}
	case "doctor":
		return runDoctor(args[1:], out, errOut)
	case "sources":
		return runSources(args[1:], out, errOut)
	case "build":
		return runBuild(args[1:], out, errOut)
	case "package":
		return runPackage(args[1:], out, errOut)
	case "validate-source":
		return runValidateSource(args[1:], out, errOut)
	case "import":
		return runImport(args[1:], out, errOut)
	case "qa":
		return runQA(args[1:], out, errOut)
	default:
		return usageError(errOut)
	}
}

func usageError(out io.Writer) int {
	fmt.Fprintln(out, "Invalid command or arguments. Use 'uo-unburger help'.")
	fmt.Fprintln(out, "usage: uo-unburger <command> [flags]")
	return exitUsage
}

// printVersionVerbose prints the release version plus the toolchain that built
// this binary and, when the module was built from a VCS checkout, the revision
// and dirty flag. It is intentionally free of wall-clock timestamps.
func printVersionVerbose(out io.Writer) {
	fmt.Fprintf(out, "uo-unburger %s\n", Version())
	fmt.Fprintf(out, "go:                 %s (%s/%s)\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	if bi.Main.Path != "" {
		fmt.Fprintf(out, "module:             %s\n", bi.Main.Path)
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			if s.Value != "" {
				fmt.Fprintf(out, "vcs revision:       %s\n", s.Value)
			}
		case "vcs.modified":
			fmt.Fprintf(out, "vcs modified:       %s\n", s.Value)
		}
	}
}

func printHelp(out io.Writer) {
	fmt.Fprintln(out, "uo-unburger — Unicorn Overlord translation toolkit")
	fmt.Fprintln(out, "Usage: uo-unburger <command> [flags]")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Running with no arguments in a terminal opens the interactive interface (keyboard-first;")
	fmt.Fprintln(out, "'?' for help, 'q' to quit). With no terminal it prints this help and exits 2.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Commands:")
	fmt.Fprintln(out, "  help                 Show this help")
	fmt.Fprintln(out, "  tui                  Force the interactive interface (requires a terminal)")
	fmt.Fprintln(out, "  version [--verbose]  Show the toolkit version (and toolchain details)")
	fmt.Fprintln(out, "  doctor               Validate the translation files (+ a source archive when available)")
	fmt.Fprintln(out, "                       --translations <dir>  translation directory (default translations)")
	fmt.Fprintln(out, "                       --source <path>     verify every record against this source CPK")
	fmt.Fprintln(out, "  sources              Read-only: list game containers, extractor, key files and cache state")
	fmt.Fprintln(out, "  import --check       Dry-run: validate the translation files and resolve both editions")
	fmt.Fprintln(out, "                       --translations <dir>  translation directory (default translations)")
	fmt.Fprintln(out, "                       --source <path>     verify original_en_sha against the game")
	fmt.Fprintln(out, "                       --corpus <path>     advanced: reference JP/EN corpus (optional)")
	fmt.Fprintln(out, "  build                Rebuild the localization CPK for one or both editions")
	fmt.Fprintln(out, "                       --variant honorifics|no-honorifics|both  (required)")
	fmt.Fprintln(out, "                       --cpk <path>        source localization CPK (read-only; or a configured one)")
	fmt.Fprintln(out, "                       --corpus <path>     advanced: reference JP/EN corpus (optional)")
	fmt.Fprintln(out, "                       --translations <dir>  translation directory (default translations)")
	fmt.Fprintln(out, "                       --out <dir>         output root (default .local/work/builds)")
	fmt.Fprintln(out, "                       --title-id <id>     expected title ID (default 010069401ADB8000)")
	fmt.Fprintln(out, "                       --display-version <v>  expected display version (1.00|1.05)")
	fmt.Fprintln(out, "                       --source-sha256 <hex>  expected source CPK SHA-256")
	fmt.Fprintln(out, "                       --strict-tokens     treat control-token mismatches as errors")
	fmt.Fprintln(out, "                       --dry-run           validate and report without writing")
	fmt.Fprintln(out, "                       --extract           extract the source from a game container when the cache has no entry")
	fmt.Fprintln(out, "                       --run-name <name>   override the default output run name")
	fmt.Fprintln(out, "                       --voice --voice-main <main>  EXPERIMENTAL, not runtime-approved: also build the")
	fmt.Fprintln(out, "                                           shared Build-ID-gated Japanese-voice ExeFS patch")
	fmt.Fprintln(out, "                       --package           assemble install-ready ZIPs from the published run")
	fmt.Fprintln(out, "                                           (default target both; ZIPs land in .local/release)")
	fmt.Fprintln(out, "                       --target <t>        packaging target: atmosphere|emulator|both (default both)")
	fmt.Fprintln(out, "                       --release <label>   neutral release label for the ZIP file names (default: dataset id)")
	fmt.Fprintln(out, "                       --mod-name <name>   rejected: the emulator archive always extracts into the")
	fmt.Fprintln(out, "                                           single folder \"Unicorn Overlord - Unburger Mod\"")
	fmt.Fprintln(out, "  package              Assemble install-ready ZIPs from an already-published build run (no rebuild)")
	fmt.Fprintln(out, "                       --run <dir>         published build run directory (required)")
	fmt.Fprintln(out, "                       --variant <v>       editions to package (default both)")
	fmt.Fprintln(out, "                       --target <t>        atmosphere|emulator|both (default both)")
	fmt.Fprintln(out, "                       --voice             include the shared EXPERIMENTAL voice ExeFS patch")
	fmt.Fprintln(out, "                       --release <label>   neutral release label (default: dataset id)")
	fmt.Fprintln(out, "                       --mod-name <name>   rejected: the emulator archive always extracts into the")
	fmt.Fprintln(out, "                                           single folder \"Unicorn Overlord - Unburger Mod\"")
	fmt.Fprintln(out, "                       --out <dir>         output directory (default .local/release)")
	fmt.Fprintln(out, "  validate-source      Read-only hash/identity/version gate for a source CPK")
	fmt.Fprintln(out, "                       --cpk <path>        source CPK (required)")
	fmt.Fprintln(out, "                       --title-id <id>     expected title ID")
	fmt.Fprintln(out, "                       --display-version <v>  expected display version")
	fmt.Fprintln(out, "                       --source-sha256 <hex>  expected SHA-256")
	fmt.Fprintln(out, "  qa                   Static per-record risk signals (no fit/render claim)")
	fmt.Fprintln(out, "                       --variant honorifics|no-honorifics|both  (required)")
	fmt.Fprintln(out, "                       --source <path>     use the game's official English (optional)")
	fmt.Fprintln(out, "                       --corpus <path>     advanced: reference JP/EN corpus (optional)")
	fmt.Fprintln(out, "                       --translations <dir>  translation directory (default translations)")
	fmt.Fprintln(out, "                       --out <path>        write the machine TSV to a file (else stdout)")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "A normal build and check need only the translation files and the user's own game data;")
	fmt.Fprintln(out, "the reference JP/EN corpus (--corpus) is an advanced cross-check and is never required.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Accepted source boundary:")
	fmt.Fprintf(out, "  title IDs:       %s\n", strings.Join(build.SupportedTitleIDs(), ", "))
	fmt.Fprintf(out, "  display versions: %s\n", strings.Join(build.SupportedDisplayVersions(), ", "))
	fmt.Fprintln(out, "  input:           an already-extracted RomFS localization CPK (Unicorn_US.CPK)")
	fmt.Fprintln(out, "                   validated by size/SHA-256 and member identity")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Exit codes: 0 success, 1 runtime failure, 2 usage error")
}
