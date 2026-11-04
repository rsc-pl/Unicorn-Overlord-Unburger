package cli

import (
	"fmt"
	"io"

	"uo-unburger/internal/importer"
	"uo-unburger/internal/paths"
)

// runImport implements `uo-unburger import --check`. It is a read-only,
// dry-run diagnostic: it loads the translation files, validates every record,
// resolves both translation editions, and reports a summary. It never writes to
// the translation files.
//
// Source identity is verified against the user's game data when a source archive
// (or an advanced reference corpus) is supplied; without one it validates the
// records, their linkage and the exact variant diff on their own, and reports
// that original_en_sha was not yet checked against the game. A normal build
// always verifies against the extracted source CPK.
func runImport(args []string, out, errOut io.Writer) int {
	check := false
	translationsDir := "translations"
	corpusPath := ""
	sourcePath := ""

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--check":
			check = true
		case "--translations":
			if i+1 >= len(args) {
				return importUsage(errOut, "--translations requires a value")
			}
			i++
			translationsDir = args[i]
		case "--corpus":
			if i+1 >= len(args) {
				return importUsage(errOut, "--corpus requires a value")
			}
			i++
			corpusPath = args[i]
		case "--source":
			if i+1 >= len(args) {
				return importUsage(errOut, "--source requires a value")
			}
			i++
			sourcePath = args[i]
		default:
			return importUsage(errOut, fmt.Sprintf("unknown import argument %q", args[i]))
		}
	}

	if !check {
		return importUsage(errOut, "import requires --check (dry-run validation)")
	}

	var res *importer.Resolution
	var err error
	switch {
	case corpusPath != "":
		res, err = importer.Check(translationsDir, corpusPath)
	case sourcePath != "":
		res, err = importer.CheckAgainstSource(translationsDir, sourcePath)
	default:
		res, err = importer.CheckLedger(translationsDir)
	}
	if err != nil {
		fmt.Fprintf(errOut, "import: %v\n", err)
		return exitFail
	}

	fmt.Fprintf(out, "import --check (dry run)\n")
	fmt.Fprintf(out, "  translation files: %s\n", paths.Display(translationsDir))
	fmt.Fprintf(out, "  source identity: %s\n", sourceModeWord(res.SourceMode))
	if corpusPath != "" {
		fmt.Fprintf(out, "  reference corpus: %s\n", paths.Display(corpusPath))
	}
	if sourcePath != "" {
		fmt.Fprintf(out, "  source archive:  %s\n", paths.Display(sourcePath))
	}
	if res.Dataset.CanonicalFile != "" {
		fmt.Fprintf(out, "  dataset:         %s rows=%d sha256=%s\n",
			res.Dataset.CanonicalFile, res.Dataset.CanonicalRows, res.Dataset.CanonicalSHA256)
		if res.Dataset.AlternatePresent {
			fmt.Fprintf(out, "  dataset (alt):   %s rows=%d sha256=%s\n",
				res.Dataset.AlternateFile, res.Dataset.AlternateRows, res.Dataset.AlternateSHA256)
		}
	}
	fmt.Fprintf(out, "  canonical rows:  %d\n", res.Counts.Canonical)
	fmt.Fprintf(out, "  overrides:       %d\n", res.Counts.Overrides)
	fmt.Fprintf(out, "  applied:         %d\n", res.Counts.Applied)
	fmt.Fprintf(out, "  overrides applied: %d\n", res.Counts.OverridesApplied)
	fmt.Fprintf(out, "  official en verified: %d\n", res.Counts.OfficialVerified)
	fmt.Fprintf(out, "  rejected:        %d\n", res.Counts.Rejected)
	fmt.Fprintf(out, "  unresolved:      %d\n", res.Counts.Unresolved)
	fmt.Fprintf(out, "  token flags:     allowed_with_exception=%d token_mismatch=%d%s\n",
		res.Counts.TokenAllowed, res.Counts.TokenMismatch, advisoryWord(res.SourceMode))
	if res.DiffPass {
		fmt.Fprintf(out, "  diff verification: PASS\n")
	} else {
		fmt.Fprintf(out, "  diff verification: FAIL (drift=%d)\n", len(res.DiffDrift))
	}

	if res.Counts.Rejected > 0 || !res.DiffPass {
		fmt.Fprintln(out, "import --check: FAIL")
		return exitFail
	}
	fmt.Fprintln(out, "import --check: PASS")
	if res.SourceMode == importer.ModeLedgerOnly {
		fmt.Fprintln(out, "note: original_en_sha was not checked against the game; a build verifies it against the source CPK.")
	}
	return exitOK
}

// sourceModeWord renders a resolution's source provenance for a user.
func sourceModeWord(m importer.SourceMode) string {
	switch m {
	case importer.ModeVerifiedSourceCPK:
		return "verified against the source CPK"
	case importer.ModeReferenceCorpus:
		return "verified against the reference JP/EN corpus"
	default:
		return "records and linkage only (no source archive supplied)"
	}
}

func advisoryWord(m importer.SourceMode) string {
	if m == importer.ModeLedgerOnly {
		return " (not computed: no official text)"
	}
	return " (advisory)"
}

func importUsage(errOut io.Writer, reason string) int {
	fmt.Fprintf(errOut, "%s\n", reason)
	fmt.Fprintln(errOut, "usage: uo-unburger import --check [--translations translations]")
	fmt.Fprintln(errOut, "       [--source <Unicorn_US.CPK>]      verify original_en_sha against the game")
	fmt.Fprintln(errOut, "       [--corpus <path>]                advanced: reference JP/EN corpus")
	fmt.Fprintln(errOut, "       (without --source or --corpus the records, linkage and diff are checked on their own)")
	return exitUsage
}
