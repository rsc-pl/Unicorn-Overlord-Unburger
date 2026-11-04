// Package source is the single resolver for the localization archive a build
// consumes. It is shared by the command line and the interactive interface so the
// two front ends cannot drift apart.
//
// Resolution order (a verified, already-present resource always wins over asking
// the operator again):
//
//  1. an explicit --cpk path;
//  2. the archive remembered in the project-local configuration (verified against
//     its recorded identity when one is stored);
//  3. a verified extraction-cache entry (re-hashed, member list re-derived);
//  4. an existing, already-verified localization archive under .local/ (any
//     archive a previous run recorded) — re-hashed and matched against the
//     recorded identity before it is used, so a translated build output is never
//     mistaken for a source;
//  5. the operator's own game container via the external extractor — only when
//     extraction is explicitly allowed (and, in the interface, consented to).
//
// Nothing is moved, copied or duplicated: a discovered archive is used in place.
// Extraction always requires an explicit opt-in; the package itself never writes a
// cache entry on a read-only inspection.
package source

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"uo-unburger/internal/appconfig"
	"uo-unburger/internal/build"
	"uo-unburger/internal/extract"
	"uo-unburger/internal/paths"
	"uo-unburger/internal/project"
	"uo-unburger/internal/sourcecache"
)

// Provenance labels where a resolved source came from. They are stable strings
// (the command line prints them and tests assert on them).
const (
	ProvenanceFlag       = "flag"
	ProvenanceConfigured = "configured"
	ProvenanceCache      = "cache"
	ProvenanceWorkspace  = "workspace"
	ProvenanceExtracted  = "extracted"
)

// State is the read-only picture of the operator's local environment: the
// checkout root, the recognised game containers, the external extractor, the key
// files, the extraction cache and the verified archives found in the workspace.
// Gathering it never extracts and never quarantines.
type State struct {
	RootErr    error
	Root       string
	Layout     project.Layout
	Discovery  *sourcecache.Discovery
	Pick       *sourcecache.Container
	Tool       *extract.Tool
	ToolErr    error
	Keys       extract.Keys
	Cache      *sourcecache.Manifest
	CacheDir   string
	CacheOK    bool
	CacheErr   string
	Workspace  []WorkspaceCandidate
	Source     string
	Provenance string
}

// WorkspaceCandidate is an existing archive found under .local/ together with the
// result of matching it against the recorded source identity.
type WorkspaceCandidate struct {
	Path       string
	Verified   bool
	Reason     string
	Size       int64
	SHA256     string
	Members    int
	FMSMembers int
}

// DefaultTitleID is the build target title used when the configuration names none.
const DefaultTitleID = build.DefaultTitleID

// TargetTitleID returns the title ID the configuration targets.
func TargetTitleID(cfg appconfig.Config) string {
	if s := strings.TrimSpace(cfg.TitleID); s != "" {
		return strings.ToUpper(s)
	}
	return build.DefaultTitleID
}

// Inspect gathers the read-only local state.
func Inspect(ctx context.Context, cfg appconfig.Config) State {
	st := State{}
	root, err := project.FindRoot()
	if err != nil {
		st.RootErr = err
		return st
	}
	st.Root = root
	st.Layout = project.NewLayout(root)
	st.Discovery = sourcecache.Discover(sourcecache.DiscoverOptions{
		BaseDir:   st.Layout.RomBase,
		UpdateDir: st.Layout.RomUpdate,
		DLCDir:    st.Layout.RomDLC,
	})
	st.Pick = st.Discovery.Pick(TargetTitleID(cfg))
	st.Keys = extract.CheckKeys(st.Layout.KeysDir)
	st.Tool, st.ToolErr = extract.Detect(ctx, extract.DetectOptions{ExplicitPath: cfg.Extractor})
	if st.Pick != nil {
		m, dir, found := sourcecache.Lookup(st.Layout.CacheDir, st.Pick.TitleID, st.Pick.RawVersion)
		st.Cache, st.CacheDir, st.CacheOK = m, dir, found
		if found {
			if verr := sourcecache.Check(dir, m); verr != nil {
				st.CacheErr = verr.Error()
				st.CacheOK = false
			} else {
				st.Source = filepath.Join(dir, m.CPK.Name)
				st.Provenance = ProvenanceCache
			}
		}
	}
	st.Workspace = ScanWorkspace(st.Layout, cfg)
	if st.Source == "" {
		for _, c := range st.Workspace {
			if c.Verified {
				st.Source = c.Path
				st.Provenance = ProvenanceWorkspace
				break
			}
		}
	}
	return st
}

// Options configures Resolve.
type Options struct {
	// Explicit is a caller-supplied path (the --cpk flag). It always wins.
	Explicit string
	// Config is the loaded project-local configuration.
	Config appconfig.Config
	// AllowExtract permits running the external extractor when no verified
	// resource is present.
	AllowExtract bool
	// Consent, when non-nil, is asked before an extraction runs. Returning false
	// stops the resolution with no side effects.
	Consent func(container sourcecache.Container) bool
	// Now is the clock used for cache quarantine bookkeeping.
	Now time.Time
}

// Resolution is a resolved source archive and its provenance.
type Resolution struct {
	Path       string
	Provenance string
	Facts      *build.SourceFacts
	State      State
	Extracted  bool
}

// Resolve returns the archive a build should use, following the documented order.
func Resolve(ctx context.Context, opts Options) (Resolution, error) {
	cfg := opts.Config

	if s := strings.TrimSpace(opts.Explicit); s != "" {
		return Resolution{Path: s, Provenance: ProvenanceFlag}, nil
	}

	if s := strings.TrimSpace(cfg.SourceCPK); s != "" {
		if _, err := os.Stat(s); err == nil {
			// Validate against the recorded identity when one exists; a
			// remembered path whose content changed falls through to discovery so
			// a replaced or re-extracted archive is found again.
			if !hasRecordedIdentity(cfg) || configuredStillMatches(s, cfg) {
				return Resolution{Path: s, Provenance: ProvenanceConfigured}, nil
			}
		}
	}

	st := Inspect(ctx, cfg)
	if st.RootErr != nil {
		return Resolution{State: st, Facts: nil}, st.RootErr
	}

	// (a) verified cache entry.
	if st.Pick != nil {
		if !st.Pick.Valid() {
			return Resolution{State: st}, fmt.Errorf("the game container %s has no readable metadata", filepath.Base(st.Pick.Path))
		}
		m, dir, ok, rerr := sourcecache.Reuse(st.Layout.CacheDir, *st.Pick, opts.Now)
		if ok {
			p := filepath.Join(dir, m.CPK.Name)
			if facts, ferr := build.ValidateSource(p, build.SourceExpectation{}); ferr == nil {
				return Resolution{Path: p, Provenance: ProvenanceCache, Facts: facts, State: st}, nil
			}
			// The cached entry no longer validates; fall through to the other
			// candidates rather than returning an unverified path.
		}
		_ = rerr // a damaged entry was quarantined; a fresh extraction is the remedy
	}

	// (b) an existing verified archive under .local/.
	for _, c := range st.Workspace {
		if !c.Verified {
			continue
		}
		facts, verr := build.ValidateSource(c.Path, build.SourceExpectation{})
		if verr != nil {
			continue
		}
		return Resolution{Path: c.Path, Provenance: ProvenanceWorkspace, Facts: facts, State: st}, nil
	}

	// (c) the operator's own container, extracted only on explicit request.
	if st.Pick == nil {
		if s := strings.TrimSpace(cfg.SourceCPK); s != "" {
			return Resolution{State: st}, fmt.Errorf("the configured source archive %s no longer exists and no game container was found under %s",
				paths.Display(s), paths.Display(st.Layout.RomBase))
		}
		return Resolution{State: st}, fmt.Errorf("no source archive: pass --cpk, configure one, or place a game container under %s", paths.Display(st.Layout.RomBase))
	}
	if !opts.AllowExtract {
		return Resolution{State: st}, fmt.Errorf("no verified cache entry for %s; re-run with --extract to extract the required resources, or pass --cpk",
			sourcecache.EntryName(st.Pick.TitleID, st.Pick.RawVersion))
	}
	if opts.Consent != nil && !opts.Consent(*st.Pick) {
		return Resolution{State: st}, errors.New("extraction was declined")
	}
	if st.ToolErr != nil {
		return Resolution{State: st}, st.ToolErr
	}
	if !st.Keys.Required() {
		return Resolution{State: st}, extract.Remedy{
			What:   "the required key file prod.keys was not found",
			Detail: fmt.Sprintf("place your own prod.keys in %s (title.keys is optional)", paths.Display(st.Layout.KeysDir)),
		}
	}

	res, xerr := extract.Run(ctx, extract.Request{
		CacheRoot:  st.Layout.CacheDir,
		Container:  *st.Pick,
		Tool:       st.Tool,
		Keys:       st.Keys,
		ScratchDir: st.Layout.ExtractDir,
		WantMain:   true,
	}, nil)
	if xerr != nil {
		return Resolution{State: st}, xerr
	}
	facts, verr := build.ValidateSource(res.CPKPath, build.SourceExpectation{})
	if verr != nil {
		os.RemoveAll(res.Scratch)
		return Resolution{State: st}, fmt.Errorf("extracted archive is not usable: %w", verr)
	}
	art := sourcecache.Artifact{
		Name: sourcecache.CPKMemberName, Size: facts.Size, SHA256: facts.SHA256,
		Members: facts.Members, FMSMembers: facts.FMSMembers, MemberListSHA256: facts.MemberListSHA256,
	}
	var mainArt *sourcecache.Artifact
	if res.MainPath != "" {
		if a, aerr := sourcecache.DescribeMain(res.MainPath); aerr == nil {
			mainArt = &a
		}
	}
	_, entry, cerr := sourcecache.Commit(sourcecache.CommitRequest{
		CacheRoot: st.Layout.CacheDir,
		Container: *st.Pick,
		CPKPath:   res.CPKPath,
		CPK:       art,
		MainPath:  res.MainPath,
		Main:      mainArt,
		Extractor: &sourcecache.ExtractorInfo{Name: st.Tool.Name, Version: st.Tool.Version, Path: st.Tool.Path},
	})
	if cerr != nil {
		os.RemoveAll(res.Scratch)
		return Resolution{State: st}, cerr
	}
	// Only the two cached resources are kept; the rest of the extractor's scratch
	// output is discarded so no full extraction is duplicated.
	os.RemoveAll(res.Scratch)
	p := filepath.Join(entry, sourcecache.CPKMemberName)
	cfacts, verr := build.ValidateSource(p, build.SourceExpectation{})
	if verr != nil {
		os.RemoveAll(res.Scratch)
		return Resolution{State: st}, fmt.Errorf("cached archive is not usable: %w", verr)
	}
	return Resolution{Path: p, Provenance: ProvenanceExtracted, Facts: cfacts, State: st, Extracted: true}, nil
}

// Remember records a resolved source's identity in the configuration and writes
// the project-local file. It is a no-op for a caller-supplied (flag) path or when
// no verified identity is available, and it never rewrites the identity of an
// already-configured source unless the resolution actually discovered a file.
func Remember(cfgPath string, cfg appconfig.Config, res Resolution) (appconfig.Config, bool, error) {
	if res.Provenance == ProvenanceFlag || res.Provenance == "" {
		return cfg, false, nil
	}
	if res.Facts == nil {
		return cfg, false, nil
	}
	changed := cfg.SourceCPK != res.Path ||
		cfg.SourceSHA256 != res.Facts.SHA256 ||
		cfg.SourceSize != res.Facts.Size ||
		cfg.SourceMembers != res.Facts.Members ||
		cfg.SourceFMSMembers != res.Facts.FMSMembers ||
		cfg.SourceMemberListSHA256 != res.Facts.MemberListSHA256 ||
		cfg.SourceProvenance != res.Provenance
	cfg.SourceCPK = res.Path
	cfg.SourceSHA256 = res.Facts.SHA256
	cfg.SourceSize = res.Facts.Size
	cfg.SourceMembers = res.Facts.Members
	cfg.SourceFMSMembers = res.Facts.FMSMembers
	cfg.SourceMemberListSHA256 = res.Facts.MemberListSHA256
	cfg.SourceProvenance = res.Provenance
	if !changed {
		return cfg, false, nil
	}
	if cfgPath == "" {
		return cfg, false, nil
	}
	if err := cfg.Save(cfgPath); err != nil {
		return cfg, false, err
	}
	return cfg, true, nil
}

// hasRecordedIdentity reports whether the configuration carries a verified source
// identity from a previous run.
func hasRecordedIdentity(cfg appconfig.Config) bool {
	return strings.TrimSpace(cfg.SourceSHA256) != "" || strings.TrimSpace(cfg.SourceMemberListSHA256) != ""
}

// configuredStillMatches re-verifies a remembered source against the recorded
// identity. A malformed or unreadable archive is treated as no longer matching.
func configuredStillMatches(path string, cfg appconfig.Config) bool {
	facts, err := build.ValidateSource(path, build.SourceExpectation{})
	if err != nil {
		return false
	}
	return identityFromConfig(cfg).Matches(facts.Size, facts.SHA256, facts.MemberListSHA256, facts.Members, facts.FMSMembers)
}

// identityFromConfig builds a build.SourceIdentity from the recorded configuration
// fields; an empty field is not asserted.
func identityFromConfig(cfg appconfig.Config) build.SourceIdentity {
	return build.SourceIdentity{
		Size:             cfg.SourceSize,
		SHA256:           cfg.SourceSHA256,
		Members:          cfg.SourceMembers,
		FMSMembers:       cfg.SourceFMSMembers,
		MemberListSHA256: cfg.SourceMemberListSHA256,
	}
}

// ScanWorkspace looks for already-present localization archives under .local/ that
// match a recorded (or the pinned) source identity. It never modifies anything and
// never descends into build outputs, staged load packages or the extraction cache,
// so a translated build artifact can never be selected as a source.
func ScanWorkspace(layout project.Layout, cfg appconfig.Config) []WorkspaceCandidate {
	identities := knownIdentities(cfg)
	roots := []string{filepath.Join(layout.WorkDir)}
	candidates := workspaceFiles(roots)
	out := make([]WorkspaceCandidate, 0, len(candidates))
	for _, p := range candidates {
		info, err := os.Stat(p)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		cand := WorkspaceCandidate{Path: p, Size: info.Size()}
		facts, verr := build.ValidateSource(p, build.SourceExpectation{})
		if verr != nil {
			cand.Reason = "not a usable localization archive"
			out = append(out, cand)
			continue
		}
		cand.Size = facts.Size
		cand.SHA256 = facts.SHA256
		cand.Members = facts.Members
		cand.FMSMembers = facts.FMSMembers
		for _, id := range identities {
			if id.Matches(facts.Size, facts.SHA256, facts.MemberListSHA256, facts.Members, facts.FMSMembers) {
				cand.Verified = true
				break
			}
		}
		if !cand.Verified {
			cand.Reason = "content does not match a verified source identity"
		}
		out = append(out, cand)
	}
	return out
}

// knownIdentities returns every identity a workspace archive may match: the
// project-pinned archive and, when present, the identity recorded in the
// configuration.
func knownIdentities(cfg appconfig.Config) []build.SourceIdentity {
	ids := []build.SourceIdentity{build.VerifiedSourceIdentity}
	if hasRecordedIdentity(cfg) {
		ids = append(ids, identityFromConfig(cfg))
	}
	return ids
}

// skippedWorkspaceDirs are directory names that never hold a source archive: build
// outputs and staged packages are translated artifacts, not sources, and the cache
// has its own verification path.
var skippedWorkspaceDirs = map[string]bool{
	"builds":   true,
	"load":     true,
	"packages": true,
	"cache":    true,
	"extract":  true,
}

// workspaceFiles returns every *.cpk / *.CPK archive under the given roots,
// skipping build outputs, staged packages and the extraction scratch.
func workspaceFiles(roots []string) []string {
	var out []string
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if path != root && skippedWorkspaceDirs[strings.ToLower(d.Name())] {
					return fs.SkipDir
				}
				return nil
			}
			if strings.EqualFold(filepath.Ext(d.Name()), ".cpk") {
				out = append(out, path)
			}
			return nil
		})
	}
	sort.Strings(out)
	return out
}
