package preflight

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"uo-unburger/internal/build"
)

const (
	fakeCorpus = "corpus/corpus.jsonl"
)

// fakeInput is a real (tiny) source file with a real SHA-256, so the suite's
// "source archive unmodified" check can be exercised honestly.
type fakeInput struct {
	path string
	sha  string
	size int64
}

func newFakeInput(t *testing.T) fakeInput {
	t.Helper()
	path := filepath.Join(t.TempDir(), "Unicorn_US.CPK")
	data := []byte("synthetic-source-archive")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("writing fake source: %v", err)
	}
	sum := sha256.Sum256(data)
	return fakeInput{path: path, sha: hex.EncodeToString(sum[:]), size: int64(len(data))}
}

// strictValidate implements the same expectations as the shared gate, so the
// suite's negative checks can be exercised without game data.
func (f fakeInput) strictValidate() func(string, build.SourceExpectation) (*build.SourceFacts, error) {
	return func(path string, exp build.SourceExpectation) (*build.SourceFacts, error) {
		if path != f.path {
			return nil, fmt.Errorf("source CPK not found")
		}
		if exp.TitleID != "" && !build.IsSupportedTitleID(exp.TitleID) {
			return nil, fmt.Errorf("unsupported TitleID %q", exp.TitleID)
		}
		if exp.DisplayVersion != "" && !build.IsSupportedDisplayVersion(exp.DisplayVersion) {
			return nil, fmt.Errorf("unsupported display version %q", exp.DisplayVersion)
		}
		if exp.SHA256 != "" && !strings.EqualFold(exp.SHA256, f.sha) {
			return nil, fmt.Errorf("source SHA-256 mismatch")
		}
		return &build.SourceFacts{Path: path, SHA256: f.sha, Size: f.size, Members: 3, FMSMembers: 1}, nil
	}
}

func (f fakeInput) permissiveValidate() func(string, build.SourceExpectation) (*build.SourceFacts, error) {
	return func(string, build.SourceExpectation) (*build.SourceFacts, error) {
		return &build.SourceFacts{Path: f.path, SHA256: f.sha, Size: f.size, Members: 3, FMSMembers: 1}, nil
	}
}

// deterministicBuild mimics the builder: it rejects a mismatched expectation,
// publishes output directories for real runs and reports measured digests.
func (f fakeInput) deterministicBuild(counter *int) func(build.Options) (*build.Result, error) {
	return func(opts build.Options) (*build.Result, error) {
		if opts.Expect.SHA256 != "" && !strings.EqualFold(opts.Expect.SHA256, f.sha) {
			return nil, fmt.Errorf("build: source SHA-256 mismatch")
		}
		if !opts.DryRun {
			*counter++
		}
		res := &build.Result{
			Plan:    &build.Plan{AppliedRecords: 4},
			RunName: opts.RunName,
			DryRun:  opts.DryRun,
		}
		if !opts.DryRun {
			if err := os.MkdirAll(filepath.Join(opts.OutDir, opts.RunName), 0o755); err != nil {
				return nil, err
			}
		}
		for i, v := range opts.Variant.Editions() {
			res.Editions = append(res.Editions, build.EditionResult{
				Variant:      v,
				SHA256:       fmt.Sprintf("%064d", i+1),
				Size:         1234,
				PlannedSlots: 4,
				ChangedSlots: 4,
				Published:    !opts.DryRun,
			})
		}
		return res, nil
	}
}

func baseOptions(t *testing.T) (Options, fakeInput) {
	t.Helper()
	t.Setenv("TMPDIR", t.TempDir())
	fake := newFakeInput(t)
	return Options{
		SourcePath:      fake.path,
		CorpusPath:      fakeCorpus,
		TranslationsDir: "translations",
		TitleID:         build.DefaultTitleID,
		Hooks:           Hooks{Validate: fake.strictValidate()},
	}, fake
}

func checkByName(t *testing.T, rep *Report, name string) Check {
	t.Helper()
	for _, c := range rep.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("check %q not found in %+v", name, rep.Checks)
	return Check{}
}

func TestRunPassesWithStrictHooks(t *testing.T) {
	calls := 0
	opts, fake := baseOptions(t)
	opts.Hooks.Build = fake.deterministicBuild(&calls)

	var progress []string
	opts.Progress = func(stage string, done, total int) {
		progress = append(progress, fmt.Sprintf("%s:%d/%d", stage, done, total))
	}

	rep, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !rep.OK {
		t.Fatalf("expected PASS, got %+v", rep.Checks)
	}
	if !rep.Determinism || len(rep.Editions) != 2 {
		t.Fatalf("determinism not established: %+v", rep)
	}
	if len(rep.Checks) != len(checkNames) {
		t.Fatalf("expected %d checks, got %d", len(checkNames), len(rep.Checks))
	}
	if len(progress) != len(checkNames) {
		t.Fatalf("progress reports = %d, want %d", len(progress), len(checkNames))
	}
	if calls != 2 {
		t.Fatalf("expected 2 real build runs, got %d", calls)
	}
	if strings.Contains(rep.Text(), "%") {
		t.Fatalf("the report must not invent a percentage:\n%s", rep.Text())
	}
}

func TestScratchDirectoryIsRemovedByDefault(t *testing.T) {
	calls := 0
	opts, fake := baseOptions(t)
	opts.Hooks.Build = fake.deterministicBuild(&calls)
	rep, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(rep.Scratch); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("scratch directory must be cleaned up, stat err = %v", err)
	}
}

func TestKeepScratchLeavesDirectory(t *testing.T) {
	calls := 0
	opts, fake := baseOptions(t)
	opts.Hooks.Build = fake.deterministicBuild(&calls)
	opts.KeepScratch = true
	rep, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(rep.Scratch); err != nil {
		t.Fatalf("scratch directory should remain: %v", err)
	}
}

func TestRejectionChecksDetectAPermissiveGate(t *testing.T) {
	calls := 0
	opts, fake := baseOptions(t)
	opts.Hooks.Validate = fake.permissiveValidate()
	opts.Hooks.Build = fake.deterministicBuild(&calls)
	rep, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.OK {
		t.Fatalf("a gate that accepts everything must not pass")
	}
	for _, name := range []string{
		"reject mismatched hash", "reject unsupported title ID", "reject unsupported display version",
	} {
		if c := checkByName(t, rep, name); c.OK {
			t.Fatalf("%s should have failed: %+v", name, c)
		}
	}
}

func TestDeterminismFailureIsDetected(t *testing.T) {
	run := 0
	opts, fake := baseOptions(t)
	opts.Hooks.Build = func(o build.Options) (*build.Result, error) {
		res, err := fake.deterministicBuild(&run)(o)
		if err != nil {
			return nil, err
		}
		if !o.DryRun {
			for i := range res.Editions {
				res.Editions[i].SHA256 = strings.Repeat(fmt.Sprint(run), 64)
			}
		}
		return res, nil
	}
	rep, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Determinism {
		t.Fatalf("differing rebuilds must not be reported as deterministic")
	}
	if c := checkByName(t, rep, "byte-identical rebuild across runs"); c.OK {
		t.Fatalf("determinism check should fail: %+v", c)
	}
}

func TestFailedBuildLeavingOutputIsDetected(t *testing.T) {
	opts, _ := baseOptions(t)
	opts.Hooks.Build = func(o build.Options) (*build.Result, error) {
		runDir := filepath.Join(o.OutDir, o.RunName)
		if err := os.MkdirAll(runDir, 0o755); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("build: rejected")
	}
	rep, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.OK {
		t.Fatalf("a build that fails after publishing must not pass")
	}
	if c := checkByName(t, rep, "failed build publishes nothing"); c.OK {
		t.Fatalf("partial-output check should fail: %+v", c)
	}
}

func TestCancellationStopsBetweenChecks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	opts, _ := baseOptions(t)
	rep, err := Run(ctx, opts)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if rep.OK {
		t.Fatalf("a cancelled run must not pass")
	}
}

func TestMissingInputsAreRejected(t *testing.T) {
	if _, err := Run(context.Background(), Options{}); err == nil {
		t.Fatalf("a missing source path must be an error")
	}
	// The reference corpus is optional: with a source path supplied the suite
	// runs and reports the checks, rather than erroring on the absent corpus.
	if _, err := Run(context.Background(), Options{SourcePath: "x"}); err != nil {
		t.Fatalf("an absent optional corpus must not be an error, got %v", err)
	}
	opts := Options{SourcePath: "x", CorpusPath: fakeCorpus, Variant: "bogus"}
	if _, err := Run(context.Background(), opts); err == nil {
		t.Fatalf("an unknown variant must be an error")
	}
}

// TestRealGateRejectsAMissingSource exercises the default hooks, proving the
// suite is wired to the real build gate.
func TestRealGateRejectsAMissingSource(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	rep, err := Run(context.Background(), Options{
		SourcePath:      filepath.Join(t.TempDir(), "absent.cpk"),
		CorpusPath:      fakeCorpus,
		TranslationsDir: "translations",
		TitleID:         build.DefaultTitleID,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.OK {
		t.Fatalf("a missing source must not pass:\n%s", rep.Text())
	}
	if c := checkByName(t, rep, "source identity"); c.OK {
		t.Fatalf("source identity should have failed: %+v", c)
	}
	if !strings.Contains(rep.Text(), "FAIL") {
		t.Fatalf("text report should report FAIL:\n%s", rep.Text())
	}
}

// TestLivePreflight runs the suite against real inputs. It is skipped unless the
// operator opts in with the environment variables, so no private path is ever
// compiled into this repository.
//
//	UO_PREFLIGHT_LIVE=1 \
//	UO_PREFLIGHT_SOURCE=<extracted Unicorn_US.CPK> \
//	UO_PREFLIGHT_CORPUS=<corpus.jsonl> \
//	[UO_PREFLIGHT_TRANSLATIONS=<translations dir>] \
//	go test ./internal/preflight -run Live -v
func TestLivePreflight(t *testing.T) {
	if os.Getenv("UO_PREFLIGHT_LIVE") != "1" {
		t.Skip("set UO_PREFLIGHT_LIVE=1 with UO_PREFLIGHT_SOURCE and UO_PREFLIGHT_CORPUS to run against real inputs")
	}
	source := os.Getenv("UO_PREFLIGHT_SOURCE")
	corpus := os.Getenv("UO_PREFLIGHT_CORPUS")
	if source == "" || corpus == "" {
		t.Fatalf("UO_PREFLIGHT_SOURCE and UO_PREFLIGHT_CORPUS are required")
	}
	rep, err := Run(context.Background(), Options{
		SourcePath:      source,
		CorpusPath:      corpus,
		TranslationsDir: os.Getenv("UO_PREFLIGHT_TRANSLATIONS"),
		TitleID:         build.DefaultTitleID,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	t.Logf("\n%s", rep.Text())
	if !rep.OK {
		t.Fatalf("live preflight failed")
	}
}
