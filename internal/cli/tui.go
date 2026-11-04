package cli

import (
	"errors"
	"fmt"
	"io"
	"os"

	"uo-unburger/internal/appconfig"
	"uo-unburger/internal/paths"
	"uo-unburger/internal/project"
	"uo-unburger/internal/tui"
)

// runTUI starts the interactive interface. It is the only command that needs a
// terminal: the interface refuses to start otherwise, so a redirected
// invocation never emits escape sequences into a pipe.
func runTUI(args []string, errOut io.Writer) int {
	if len(args) != 0 {
		return tuiUsage(errOut, fmt.Sprintf("unknown tui argument %q", args[0]))
	}
	cfg, cfgPath, err := appconfig.Load()
	if err != nil {
		fmt.Fprintf(errOut, "tui: %s\n", paths.Error(err))
		return exitFail
	}
	// Prepare the project-local layout on demand: only missing directories are
	// created, nothing is ever deleted.
	if root, rerr := appconfig.Root(); rerr == nil {
		if _, eerr := project.NewLayout(root).Ensure(); eerr != nil {
			fmt.Fprintf(errOut, "tui: %s\n", paths.Error(eerr))
			return exitFail
		}
	}
	// The guided setup opens automatically once, while it has never been finished:
	// a missing settings file loads with setup_complete=false (so a first run, or a
	// checkout whose cache/sources are not resolved yet, is guided), and finishing
	// or skipping the flow records setup_complete=true so it is not forced again.
	// It stays re-openable from the Settings and Overview Actions panes.
	err = tui.Run(tui.Options{
		Config:     cfg,
		ConfigPath: cfgPath,
		Input:      os.Stdin,
		Output:     os.Stdout,
		Version:    Version(),
		FirstRun:   !cfg.SetupComplete,
	})
	switch {
	case err == nil:
		return exitOK
	case errors.Is(err, tui.ErrNotTerminal):
		fmt.Fprintln(errOut, "tui: a terminal is required; stdin/stdout are not a TTY.")
		fmt.Fprintln(errOut, "     In a script or pipe, use a subcommand (see 'uo-unburger help').")
		return exitFail
	default:
		fmt.Fprintf(errOut, "tui: %s\n", paths.Error(err))
		return exitFail
	}
}

func tuiUsage(errOut io.Writer, reason string) int {
	fmt.Fprintf(errOut, "%s\n", reason)
	fmt.Fprintln(errOut, "usage: uo-unburger tui")
	return exitUsage
}
