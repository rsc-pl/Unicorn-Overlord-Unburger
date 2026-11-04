// Package preflight runs the end-to-end safety suite that has to pass before a
// release is considered: it re-validates the source archive, proves that a
// mismatched hash/version/title ID is rejected, performs a clean dry-run build
// of both editions, performs a real build twice into scratch directories and
// compares the results byte-for-byte, and verifies that a failing build leaves
// no published output and that the source archive is never modified.
//
// Every check is read-only with respect to the operator's inputs. Published
// artifacts go to a scratch directory that the caller owns (a temporary
// directory by default) and are removed before Run returns unless KeepScratch is
// set. The suite computes real, measurable stage counts for progress reporting;
// it never fabricates a percentage.
package preflight

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"uo-unburger/internal/build"
	"uo-unburger/internal/paths"
)

// Hooks allows tests to substitute the validation and build engines. A zero
// Hooks value uses the real engines.
type Hooks struct {
	Validate func(path string, exp build.SourceExpectation) (*build.SourceFacts, error)
	Build    func(opts build.Options) (*build.Result, error)
}

func (h Hooks) validate(path string, exp build.SourceExpectation) (*build.SourceFacts, error) {
	if h.Validate != nil {
		return h.Validate(path, exp)
	}
	return build.ValidateSource(path, exp)
}

func (h Hooks) build(opts build.Options) (*build.Result, error) {
	if h.Build != nil {
		return h.Build(opts)
	}
	return build.Run(opts)
}

// Options configures a self-check run.
type Options struct {
	SourcePath      string
	CorpusPath      string
	TranslationsDir string
	TitleID         string
	DisplayVersion  string
	// Variant selects the editions to build. Empty means both.
	Variant build.Variant
	// Runs is the number of real builds used for the determinism check. Zero
	// means 2; values below 2 disable the byte-identity comparison.
	Runs int
	// ScratchDir overrides the scratch root. When empty a temporary directory is
	// created under the system temporary directory.
	ScratchDir string
	// KeepScratch leaves the scratch directory in place for inspection.
	KeepScratch bool
	// Expect optionally asserts the source identity as well as title/version.
	Expect build.SourceExpectation
	// Progress is called after every completed check with measured counters.
	Progress func(stage string, done, total int)
	Hooks    Hooks
}

// Check is one named result.
type Check struct {
	Name   string
	OK     bool
	Detail string
}

// EditionDigest is the identity of one rebuilt edition.
type EditionDigest struct {
	Variant string
	Size    int64
	SHA256  string
}

// Report is the outcome of a self-check run.
type Report struct {
	Checks      []Check
	OK          bool
	Determinism bool
	Editions    []EditionDigest
	Scratch     string
	Detail      string
}

// checkNames lists the checks in execution order; the count is the measured
// progress total.
var checkNames = []string{
	"source identity",
	"reject mismatched hash",
	"reject unsupported title ID",
	"reject unsupported display version",
	"clean dry-run build (both editions)",
	"publish build to scratch",
	"byte-identical rebuild across runs",
	"failed build publishes nothing",
	"source archive unmodified",
	"package archives reopen cleanly",
	"packaging is byte-identical",
}

// Run executes the suite. A cancelled context aborts at the next check
// boundary; because builds only publish in their final stage, an aborted run
// leaves no published output behind.
func Run(ctx context.Context, opts Options) (*Report, error) {
	if opts.Variant == "" {
		opts.Variant = build.VariantBoth
	}
	if _, err := build.ParseVariant(string(opts.Variant)); err != nil {
		return nil, err
	}
	if opts.SourcePath == "" {
		return nil, fmt.Errorf("self-check: source CPK path is required")
	}
	// CorpusPath is optional: without it the build resolves source identity
	// against the source CPK itself.
	if opts.TranslationsDir == "" {
		opts.TranslationsDir = "translations"
	}
	if opts.Runs == 0 {
		opts.Runs = 2
	}
	if opts.TitleID == "" {
		opts.TitleID = build.DefaultTitleID
	}

	rep := &Report{}
	done := 0
	check := func(name string, fn func() (bool, string)) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		ok, detail := fn()
		rep.Checks = append(rep.Checks, Check{Name: name, OK: ok, Detail: detail})
		done++
		if opts.Progress != nil {
			opts.Progress(name, done, len(checkNames))
		}
		return nil
	}

	scratch := opts.ScratchDir
	cleanup := func() {}
	if scratch == "" {
		dir, err := os.MkdirTemp("", "uo-selfcheck-*")
		if err != nil {
			return nil, fmt.Errorf("self-check: creating scratch directory: %w", err)
		}
		scratch = dir
		if !opts.KeepScratch {
			cleanup = func() { os.RemoveAll(dir) }
		}
	} else if err := os.MkdirAll(scratch, 0o755); err != nil {
		return nil, fmt.Errorf("self-check: creating scratch directory: %w", err)
	}
	defer cleanup()
	rep.Scratch = scratch

	exp := opts.Expect
	if exp.TitleID == "" {
		exp.TitleID = opts.TitleID
	}
	if exp.DisplayVersion == "" {
		exp.DisplayVersion = opts.DisplayVersion
	}

	var facts *build.SourceFacts
	buildOpts := func(dry bool, runName, outDir string) build.Options {
		return build.Options{
			Variant:         opts.Variant,
			SourcePath:      opts.SourcePath,
			CorpusPath:      opts.CorpusPath,
			TranslationsDir: opts.TranslationsDir,
			OutDir:          outDir,
			RunName:         runName,
			TitleID:         exp.TitleID,
			DisplayVersion:  exp.DisplayVersion,
			Expect:          exp,
			DryRun:          dry,
			Date:            "19700101",
			Progress: func(stage string, d, total int) error {
				return ctx.Err()
			},
		}
	}

	if err := check(checkNames[0], func() (bool, string) {
		f, err := opts.Hooks.validate(opts.SourcePath, exp)
		if err != nil {
			return false, "source rejected: " + paths.Error(err)
		}
		facts = f
		return true, fmt.Sprintf("%d members (%d .fms), %d bytes, sha256 %s",
			f.Members, f.FMSMembers, f.Size, f.SHA256)
	}); err != nil {
		return rep, err
	}

	if err := check(checkNames[1], func() (bool, string) {
		wrong := strings.Repeat("0", 64)
		if facts != nil && facts.SHA256 == wrong {
			wrong = strings.Repeat("f", 64)
		}
		bad := exp
		bad.SHA256 = wrong
		if _, err := opts.Hooks.validate(opts.SourcePath, bad); err == nil {
			return false, "a mismatched SHA-256 was accepted"
		}
		return true, "mismatched SHA-256 rejected"
	}); err != nil {
		return rep, err
	}

	if err := check(checkNames[2], func() (bool, string) {
		bad := exp
		bad.TitleID = "0000000000000000"
		if _, err := opts.Hooks.validate(opts.SourcePath, bad); err == nil {
			return false, "an unsupported title ID was accepted"
		}
		return true, "unsupported title ID rejected"
	}); err != nil {
		return rep, err
	}

	if err := check(checkNames[3], func() (bool, string) {
		bad := exp
		bad.DisplayVersion = "9.99"
		if _, err := opts.Hooks.validate(opts.SourcePath, bad); err == nil {
			return false, "an unsupported display version was accepted"
		}
		return true, "unsupported display version rejected"
	}); err != nil {
		return rep, err
	}

	var dry *build.Result
	if err := check(checkNames[4], func() (bool, string) {
		res, err := opts.Hooks.build(buildOpts(true, "selfcheck-dry", filepath.Join(scratch, "dry")))
		if err != nil {
			return false, "dry-run build failed: " + paths.Error(err)
		}
		if len(res.Editions) != len(opts.Variant.Editions()) {
			return false, fmt.Sprintf("expected %d editions, got %d", len(opts.Variant.Editions()), len(res.Editions))
		}
		dry = res
		return true, fmt.Sprintf("%d edition(s), %d applied records", len(res.Editions), res.Plan.AppliedRecords)
	}); err != nil {
		return rep, err
	}

	var runs []*build.Result
	if err := check(checkNames[5], func() (bool, string) {
		for i := 0; i < opts.Runs; i++ {
			name := fmt.Sprintf("selfcheck-run%d", i+1)
			res, err := opts.Hooks.build(buildOpts(false, name, filepath.Join(scratch, name)))
			if err != nil {
				return false, fmt.Sprintf("build run %d failed: %s", i+1, paths.Error(err))
			}
			for _, e := range res.Editions {
				if !e.Published {
					return false, fmt.Sprintf("run %d edition %s was not published", i+1, e.Variant)
				}
			}
			runs = append(runs, res)
		}
		return true, fmt.Sprintf("%d published run(s) in scratch", len(runs))
	}); err != nil {
		return rep, err
	}

	if err := check(checkNames[6], func() (bool, string) {
		if len(runs) < 2 {
			return true, "byte-identity comparison disabled (fewer than 2 runs)"
		}
		base := digests(runs[0])
		for i := 1; i < len(runs); i++ {
			got := digests(runs[i])
			if !sameDigests(base, got) {
				return false, fmt.Sprintf("run %d differs from run 1", i+1)
			}
		}
		rep.Editions = base
		rep.Determinism = true
		if len(runs) >= 2 && dry != nil {
			for _, e := range dry.Editions {
				if d, ok := findDigest(base, string(e.Variant)); ok && d.SHA256 != e.SHA256 {
					return false, fmt.Sprintf("dry-run and published %s digests differ", e.Variant)
				}
			}
		}
		return true, fmt.Sprintf("%d run(s) byte-identical", len(runs))
	}); err != nil {
		return rep, err
	}

	if err := check(checkNames[7], func() (bool, string) {
		badOpts := buildOpts(false, "selfcheck-should-fail", filepath.Join(scratch, "reject"))
		badOpts.Expect = exp
		badOpts.Expect.SHA256 = strings.Repeat("a", 64)
		if facts != nil && facts.SHA256 == badOpts.Expect.SHA256 {
			badOpts.Expect.SHA256 = strings.Repeat("b", 64)
		}
		if _, err := opts.Hooks.build(badOpts); err == nil {
			return false, "a build with a mismatched source hash succeeded"
		}
		if _, err := os.Stat(filepath.Join(scratch, "reject")); err == nil {
			return false, "a failed build left published output"
		}
		return true, "rejected build published nothing"
	}); err != nil {
		return rep, err
	}

	if err := check(checkNames[8], func() (bool, string) {
		if facts == nil {
			return false, "source facts unavailable"
		}
		sum, size, err := hashFile(opts.SourcePath)
		if err != nil {
			return false, "re-hashing source failed: " + paths.Error(err)
		}
		if sum != facts.SHA256 || size != facts.Size {
			return false, "the source archive changed during the self-check"
		}
		return true, "source archive byte-identical after the suite"
	}); err != nil {
		return rep, err
	}

	// Packaging checks: assemble install-ready archives from the first published
	// run into scratch and prove they reopen cleanly and are byte-deterministic.
	// A synthetic run (no artifact files on disk) skips them with an explicit
	// detail rather than a false pass or a spurious failure.
	var pkgA *build.PackageResult
	if err := check(checkNames[9], func() (bool, string) {
		if len(runs) == 0 || !hasPackagingInputs(runs[0]) {
			return true, "skipped: the run has no artifact files on disk"
		}
		p, err := build.Package(runs[0], build.PackageOptions{
			Targets: build.TargetBoth,
			OutDir:  filepath.Join(scratch, "packages"),
		})
		if err != nil {
			return false, "packaging failed: " + paths.Error(err)
		}
		pkgA = p
		files := 0
		for _, a := range p.Artifacts {
			files += len(a.Files)
		}
		return true, fmt.Sprintf("%d archive(s) reopened, %d file(s) verified", len(p.Artifacts), files)
	}); err != nil {
		return rep, err
	}

	if err := check(checkNames[10], func() (bool, string) {
		if pkgA == nil {
			return true, "skipped: packaging was not exercised"
		}
		p, err := build.Package(runs[0], build.PackageOptions{
			Targets: build.TargetBoth,
			OutDir:  filepath.Join(scratch, "packages-2"),
		})
		if err != nil {
			return false, "second packaging failed: " + paths.Error(err)
		}
		if len(p.Artifacts) != len(pkgA.Artifacts) {
			return false, fmt.Sprintf("archive count changed (%d -> %d)", len(pkgA.Artifacts), len(p.Artifacts))
		}
		for i := range p.Artifacts {
			if p.Artifacts[i].SHA256 != pkgA.Artifacts[i].SHA256 {
				return false, fmt.Sprintf("%s differs between two packaging runs", p.Artifacts[i].RelPath)
			}
		}
		return true, fmt.Sprintf("%d archive(s) byte-identical across two packaging runs", len(p.Artifacts))
	}); err != nil {
		return rep, err
	}

	rep.OK = true
	for _, c := range rep.Checks {
		if !c.OK {
			rep.OK = false
			break
		}
	}
	return rep, nil
}

// hasPackagingInputs reports whether a build result carries real, on-disk
// edition artifacts that packaging can consume. A synthetic test run does not,
// so the packaging checks skip it with an explicit detail.
func hasPackagingInputs(res *build.Result) bool {
	if res == nil || len(res.Editions) == 0 {
		return false
	}
	for _, e := range res.Editions {
		if e.ArtifactPath == "" {
			return false
		}
		if _, err := os.Stat(e.ArtifactPath); err != nil {
			return false
		}
	}
	return true
}

// Text renders the deterministic report text.
func (r *Report) Text() string {
	var b strings.Builder
	status := "PASS"
	if !r.OK {
		status = "FAIL"
	}
	fmt.Fprintf(&b, "self-check: %s\n", status)
	for _, c := range r.Checks {
		mark := "ok"
		if !c.OK {
			mark = "FAIL"
		}
		fmt.Fprintf(&b, "  [%s] %s: %s\n", mark, c.Name, c.Detail)
	}
	if r.Determinism {
		for _, e := range r.Editions {
			fmt.Fprintf(&b, "  determinism %s: %d bytes sha256 %s\n", e.Variant, e.Size, e.SHA256)
		}
	}
	fmt.Fprintf(&b, "  go: %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	return b.String()
}

func digests(res *build.Result) []EditionDigest {
	out := make([]EditionDigest, 0, len(res.Editions))
	for _, e := range res.Editions {
		out = append(out, EditionDigest{Variant: string(e.Variant), Size: e.Size, SHA256: e.SHA256})
	}
	return out
}

func sameDigests(a, b []EditionDigest) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func findDigest(list []EditionDigest, variant string) (EditionDigest, bool) {
	for _, d := range list {
		if d.Variant == variant {
			return d, true
		}
	}
	return EditionDigest{}, false
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// ErrCancelled is returned when the suite is aborted between checks.
var ErrCancelled = errors.New("self-check: cancelled")

// IsCancelled reports whether err came from context cancellation.
func IsCancelled(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
