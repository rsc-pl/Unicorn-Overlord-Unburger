package cli

import (
	"fmt"
	"io"
	"os"

	"uo-unburger/internal/paths"
	"uo-unburger/internal/qa"
)

// runQA implements `uo-unburger qa`. It is a read-only static risk-signal
// emitter: it resolves both editions through the shared importer and reports
// per-record control-token flags, authored-break loss vs the official English,
// length ratios and override divergence. It never claims rendered fit and never
// writes to the translation files or corpus.
func runQA(args []string, out, errOut io.Writer) int {
	variantWord := ""
	corpusPath := ""
	sourcePath := ""
	translationsDir := "translations"
	outPath := ""

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--variant":
			if i+1 >= len(args) {
				return qaUsage(errOut, "--variant requires a value")
			}
			i++
			variantWord = args[i]
		case "--corpus":
			if i+1 >= len(args) {
				return qaUsage(errOut, "--corpus requires a value")
			}
			i++
			corpusPath = args[i]
		case "--source":
			if i+1 >= len(args) {
				return qaUsage(errOut, "--source requires a value")
			}
			i++
			sourcePath = args[i]
		case "--translations":
			if i+1 >= len(args) {
				return qaUsage(errOut, "--translations requires a value")
			}
			i++
			translationsDir = args[i]
		case "--out":
			if i+1 >= len(args) {
				return qaUsage(errOut, "--out requires a value")
			}
			i++
			outPath = args[i]
		default:
			return qaUsage(errOut, fmt.Sprintf("unknown qa argument %q", args[i]))
		}
	}

	if variantWord == "" {
		return qaUsage(errOut, "qa requires --variant honorifics|no-honorifics|both")
	}
	scope, err := qa.ParseScope(variantWord)
	if err != nil {
		return qaUsage(errOut, err.Error())
	}

	res, err := qa.Run(qa.Options{
		Scope:           scope,
		CorpusPath:      corpusPath,
		SourcePath:      sourcePath,
		TranslationsDir: translationsDir,
	})
	if err != nil {
		fmt.Fprintf(errOut, "%v\n", err)
		return exitFail
	}

	fmt.Fprint(out, res.SummaryTxt)
	if outPath != "" {
		if err := os.WriteFile(outPath, []byte(res.TSV), 0o644); err != nil {
			fmt.Fprintf(errOut, "qa: writing TSV: %v\n", err)
			return exitFail
		}
		fmt.Fprintf(out, "\n  TSV rows:                   %d\n", len(res.Records))
		fmt.Fprintf(out, "  TSV written:                %s\n", paths.Display(outPath))
	} else {
		fmt.Fprintln(out)
		fmt.Fprint(out, res.TSV)
	}
	return exitOK
}

func qaUsage(errOut io.Writer, reason string) int {
	fmt.Fprintf(errOut, "%s\n", reason)
	fmt.Fprintln(errOut, "usage: uo-unburger qa --variant honorifics|no-honorifics|both")
	fmt.Fprintln(errOut, "       [--source <Unicorn_US.CPK>]   use the game's official English (optional)")
	fmt.Fprintln(errOut, "       [--corpus <path>]             advanced: reference JP/EN corpus (optional)")
	fmt.Fprintln(errOut, "       [--translations translations] [--out <tsv path>]")
	return exitUsage
}
