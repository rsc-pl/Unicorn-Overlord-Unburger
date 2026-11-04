package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"uo-unburger/internal/build"
	"uo-unburger/internal/importer"
	"uo-unburger/internal/paths"
)

// runDoctor implements `uo-unburger doctor`. It validates the translation input
// the current build actually consumes — the two consolidated table files — and,
// when a source archive is available, verifies every record against it.
func runDoctor(args []string, out, errOut io.Writer) int {
	translationsDir := "translations"
	sourcePath := ""

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--translations":
			if i+1 >= len(args) {
				return doctorUsage(errOut, "--translations requires a value")
			}
			i++
			translationsDir = args[i]
		case "--source":
			if i+1 >= len(args) {
				return doctorUsage(errOut, "--source requires a value")
			}
			i++
			sourcePath = args[i]
		default:
			return doctorUsage(errOut, fmt.Sprintf("unknown doctor argument %q", args[i]))
		}
	}

	explicitSource := sourcePath != ""
	if sourcePath == "" {
		sourcePath = configuredSource()
	}

	var res *importer.Resolution
	var err error
	if sourcePath != "" {
		res, err = importer.CheckAgainstSource(translationsDir, sourcePath)
	} else {
		res, err = importer.CheckLedger(translationsDir)
	}
	if err != nil {
		fmt.Fprintf(errOut, "doctor: %v\n", err)
		return exitFail
	}

	fmt.Fprintln(out, "uo-unburger doctor")
	fmt.Fprintf(out, "  translations:      %s\n", paths.Display(translationsDir))
	if res.Dataset.CanonicalFile != "" {
		fmt.Fprintf(out, "  canonical:         %s rows=%d sha256=%s\n",
			res.Dataset.CanonicalFile, res.Dataset.CanonicalRows, res.Dataset.CanonicalSHA256)
	}
	if res.Dataset.AlternatePresent {
		fmt.Fprintf(out, "  alternates:        %s rows=%d sha256=%s\n",
			res.Dataset.AlternateFile, res.Dataset.AlternateRows, res.Dataset.AlternateSHA256)
	}
	fmt.Fprintf(out, "  applied:           %d\n", res.Counts.Applied)
	fmt.Fprintf(out, "  overrides applied: %d\n", res.Counts.OverridesApplied)
	fmt.Fprintf(out, "  rejected:          %d\n", res.Counts.Rejected)
	if res.DiffPass {
		fmt.Fprintf(out, "  diff verification: PASS\n")
	} else {
		fmt.Fprintf(out, "  diff verification: FAIL (drift=%d)\n", len(res.DiffDrift))
	}
	if sourcePath == "" {
		fmt.Fprintf(out, "  source archive:    not configured (set one with --source or the interface)\n")
	} else {
		fmt.Fprintf(out, "  source archive:    %s\n", paths.Display(sourcePath))
		if !explicitSource {
			fmt.Fprintf(out, "                     (from the saved configuration)\n")
		}
		fmt.Fprintf(out, "  official verified: %d\n", res.Counts.OfficialVerified)
	}
	fmt.Fprintf(out, "  supported ids:     %s\n", strings.Join(build.SupportedTitleIDs(), ", "))
	fmt.Fprintf(out, "  supported versions: %s\n", strings.Join(build.SupportedDisplayVersions(), ", "))
	printEnvironment(out, inspectSources(context.Background(), loadConfig()))

	for _, rr := range res.Records {
		if rr.Status == importer.StatusRejected {
			fmt.Fprintf(out, "  rejected record: %s: %s\n", rr.RecordID, strings.Join(rr.Errors, "; "))
		}
	}
	if res.Counts.Rejected > 0 || !res.DiffPass {
		fmt.Fprintln(out, "doctor: FAIL")
		return exitFail
	}
	fmt.Fprintln(out, "doctor: OK")
	return exitOK
}

func doctorUsage(out io.Writer, reason string) int {
	fmt.Fprintf(out, "%s\n", reason)
	fmt.Fprintln(out, "usage: uo-unburger doctor [--translations <dir>] [--source <Unicorn_US.CPK>]")
	return exitUsage
}
