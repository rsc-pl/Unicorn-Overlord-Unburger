package cli

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"uo-unburger/internal/appconfig"
	"uo-unburger/internal/extract"
	"uo-unburger/internal/paths"
	"uo-unburger/internal/project"
	"uo-unburger/internal/source"
	"uo-unburger/internal/sourcecache"
)

// sourcesState is the read-only picture of the operator's local environment. It is
// the shared resolver's state so the command line, doctor and the interactive
// interface all report exactly the same facts.
type sourcesState = source.State

func targetTitleID(cfg appconfig.Config) string { return source.TargetTitleID(cfg) }

// inspectSources gathers the read-only local state. It never extracts and never
// quarantines: a damaged cache entry is reported, not moved.
func inspectSources(ctx context.Context, cfg appconfig.Config) sourcesState {
	return source.Inspect(ctx, cfg)
}

// acquireSource resolves the source localization archive a build should use:
// an explicit --cpk, else the configured archive, else a verified cache entry,
// else an already-verified archive under .local/, else — only when allowExtract is
// set — a fresh extraction from the operator's container that is then committed to
// the cache. It is the single resolution path shared with the interactive
// interface. A discovered archive's identity is recorded in the project-local
// configuration so later runs recognise it without re-discovery.
func acquireSource(ctx context.Context, cfg appconfig.Config, explicit string, allowExtract bool) (path, provenance string, err error) {
	res, rerr := source.Resolve(ctx, source.Options{
		Explicit:     explicit,
		Config:       cfg,
		AllowExtract: allowExtract,
	})
	if rerr != nil {
		return "", "", rerr
	}
	if res.Provenance != source.ProvenanceFlag && res.Provenance != source.ProvenanceConfigured {
		// Best-effort: remembering the discovered source must never fail a build.
		_, _, _ = source.Remember(appconfig.Path(), cfg, res)
	}
	return res.Path, res.Provenance, nil
}

// runSources implements `uo-unburger sources`: a read-only report of what the
// toolkit can see. It never extracts and never writes.
func runSources(args []string, out, errOut io.Writer) int {
	for _, a := range args {
		return sourcesUsage(errOut, fmt.Sprintf("unknown sources argument %q", a))
	}
	cfg := loadConfig()
	st := inspectSources(context.Background(), cfg)

	fmt.Fprintln(out, "uo-unburger sources")
	if st.RootErr != nil {
		fmt.Fprintf(out, "  project root:      not found (%s)\n", paths.Error(st.RootErr))
		fmt.Fprintln(out, "  hint:              run from a checkout, or set "+project.RootEnv)
		return exitFail
	}
	fmt.Fprintf(out, "  project root:      %s\n", paths.Display(st.Root))
	fmt.Fprintf(out, "  config file:       %s\n", paths.Display(st.Layout.ConfigFile))
	fmt.Fprintf(out, "  cache folder:      %s\n", paths.Display(st.Layout.CacheDir))

	fmt.Fprintf(out, "  game containers:   %d found under %s\n", len(st.Discovery.Containers), paths.Display(st.Layout.RomsDir))
	for _, c := range st.Discovery.Containers {
		status := "recognised"
		if !c.Parsed {
			status = "not recognised"
		}
		fmt.Fprintf(out, "    - %-8s %-9s %s (%s)\n", c.Role, status, filepath.Base(c.Path), displayHintOr(c))
	}
	if st.Pick != nil {
		fmt.Fprintf(out, "  effective source:  %s (%s, raw version %d)\n", filepath.Base(st.Pick.Path), st.Pick.TitleID, st.Pick.RawVersion)
	} else {
		fmt.Fprintf(out, "  effective source:  none (place the base and update NSP under %s and %s)\n",
			paths.Display(st.Layout.RomBase), paths.Display(st.Layout.RomUpdate))
	}

	switch {
	case st.ToolErr != nil:
		fmt.Fprintf(out, "  extractor:         not found on PATH (set one explicitly in Settings)\n")
	default:
		fmt.Fprintf(out, "  extractor:         %s %s (%s)\n", st.Tool.Name, st.Tool.Version, st.Tool.Source)
	}
	fmt.Fprintf(out, "  key files:         %s\n", keysLine(st.Keys))
	if st.Pick != nil {
		switch {
		case st.CacheOK:
			fmt.Fprintf(out, "  cache:             valid entry %s (%s)\n", sourcecache.EntryName(st.Pick.TitleID, st.Pick.RawVersion), paths.Display(st.CacheDir))
		case st.CacheErr != "":
			fmt.Fprintf(out, "  cache:             damaged entry %s — %s\n", sourcecache.EntryName(st.Pick.TitleID, st.Pick.RawVersion), st.CacheErr)
		default:
			fmt.Fprintf(out, "  cache:             no entry for %s\n", sourcecache.EntryName(st.Pick.TitleID, st.Pick.RawVersion))
		}
	}
	fmt.Fprintln(out, "  note:              sources is read-only: nothing was extracted and nothing was written")
	writeReadySource(out, st)
	return exitOK
}

// writeReadySource reports the already-verified archive a build would reuse and
// the local archives that were inspected, without resolving anything new.
func writeReadySource(out io.Writer, st sourcesState) {
	if st.Source != "" {
		fmt.Fprintf(out, "  ready source:      %s (from %s)\n", paths.Display(st.Source), st.Provenance)
	} else {
		fmt.Fprintln(out, "  ready source:      none verified yet (a build resolves or extracts one)")
	}
	if len(st.Workspace) == 0 {
		return
	}
	fmt.Fprintf(out, "  local archives:    %d found under the work folder\n", len(st.Workspace))
	for _, c := range st.Workspace {
		state := "unverified"
		if c.Verified {
			state = "verified"
		}
		fmt.Fprintf(out, "    - %-9s %s\n", state, paths.Display(c.Path))
	}
}

func displayHintOr(c sourcecache.Container) string {
	if c.DisplayHint != "" {
		return c.DisplayHint
	}
	if c.Note != "" {
		return c.Note
	}
	return "-"
}

func keysLine(k extract.Keys) string {
	var parts []string
	if k.ProdKeys {
		parts = append(parts, "prod.keys present")
	} else {
		parts = append(parts, "prod.keys missing")
	}
	if k.TitleKeys {
		parts = append(parts, "title.keys present")
	} else {
		parts = append(parts, "title.keys absent")
	}
	return strings.Join(parts, ", ")
}

func sourcesUsage(errOut io.Writer, reason string) int {
	if reason != "" {
		fmt.Fprintln(errOut, reason)
	}
	fmt.Fprintln(errOut, "usage: uo-unburger sources")
	fmt.Fprintln(errOut, "       Read-only: lists the recognised game containers, the extractor, the key files")
	fmt.Fprintln(errOut, "       and the extraction cache state. Nothing is extracted or written.")
	return exitUsage
}

// printEnvironment writes a compact, read-only environment report used by doctor.
func printEnvironment(out io.Writer, st sourcesState) {
	if st.RootErr != nil {
		fmt.Fprintf(out, "  project root:      not found (%s)\n", paths.Error(st.RootErr))
		return
	}
	fmt.Fprintf(out, "  project root:      %s\n", paths.Display(st.Root))
	fmt.Fprintf(out, "  config file:       %s\n", paths.Display(st.Layout.ConfigFile))
	recognised := 0
	for _, c := range st.Discovery.Containers {
		if c.Valid() {
			recognised++
		}
	}
	fmt.Fprintf(out, "  game containers:   %d found, %d recognised\n", len(st.Discovery.Containers), recognised)
	if st.Pick != nil {
		fmt.Fprintf(out, "  effective source:  %s (%s, raw version %d)\n", filepath.Base(st.Pick.Path), st.Pick.TitleID, st.Pick.RawVersion)
	}
	if st.ToolErr != nil {
		fmt.Fprintf(out, "  extractor:         not found on PATH\n")
	} else {
		fmt.Fprintf(out, "  extractor:         %s %s (%s)\n", st.Tool.Name, st.Tool.Version, st.Tool.Source)
	}
	fmt.Fprintf(out, "  key files:         %s\n", keysLine(st.Keys))
	if st.Pick != nil {
		switch {
		case st.CacheOK:
			fmt.Fprintf(out, "  cache:             valid entry %s\n", sourcecache.EntryName(st.Pick.TitleID, st.Pick.RawVersion))
		case st.CacheErr != "":
			fmt.Fprintf(out, "  cache:             damaged entry — %s\n", st.CacheErr)
		default:
			fmt.Fprintf(out, "  cache:             no entry for %s\n", sourcecache.EntryName(st.Pick.TitleID, st.Pick.RawVersion))
		}
	}
	writeReadySource(out, st)
}

// loadConfig loads the project-local configuration, falling back to defaults.
func loadConfig() appconfig.Config {
	cfg, _, err := appconfig.Load()
	if err != nil {
		return appconfig.Default()
	}
	return cfg
}
