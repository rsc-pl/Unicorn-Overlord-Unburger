package preflight

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"uo-unburger/internal/build"
	"uo-unburger/internal/layeredfs"
)

// TestPackagingChecksRunOnRealArtifacts: when a published run carries real
// artifact files, the packaging checks assemble and reopen the archives instead
// of skipping. The build hook writes small stand-in artifacts on disk, so the
// check exercises the real packager end to end without any game data.
func TestPackagingChecksRunOnRealArtifacts(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	fake := newFakeInput(t)
	opts := Options{
		SourcePath:      fake.path,
		TranslationsDir: "translations",
		TitleID:         build.DefaultTitleID,
		Hooks:           Hooks{Validate: fake.strictValidate()},
	}
	opts.Hooks.Build = func(o build.Options) (*build.Result, error) {
		if o.Expect.SHA256 != "" && !strings.EqualFold(o.Expect.SHA256, fake.sha) {
			return nil, fmt.Errorf("build: source SHA-256 mismatch")
		}
		res := &build.Result{
			Plan:    &build.Plan{AppliedRecords: 4},
			RunName: o.RunName,
			RunDir:  filepath.Join(o.OutDir, o.RunName),
			DryRun:  o.DryRun,
			TitleID: build.DefaultTitleID,
		}
		for _, v := range o.Variant.Editions() {
			e := build.EditionResult{Variant: v, ArtifactRel: layeredfs.RomFSRel, Published: !o.DryRun}
			if !o.DryRun {
				p := filepath.Join(o.OutDir, o.RunName, string(v), filepath.FromSlash(layeredfs.RomFSRel))
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					return nil, err
				}
				if err := os.WriteFile(p, []byte("stand-in artifact for "+string(v)), 0o644); err != nil {
					return nil, err
				}
				e.ArtifactPath = p
			}
			res.Editions = append(res.Editions, e)
		}
		return res, nil
	}

	rep, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !rep.OK {
		t.Fatalf("self-check failed: %+v", rep.Checks)
	}
	reopen := checkByName(t, rep, "package archives reopen cleanly")
	if !reopen.OK || strings.Contains(reopen.Detail, "skipped") {
		t.Fatalf("packaging check did not run: %+v", reopen)
	}
	if !strings.Contains(reopen.Detail, "4 archive(s)") {
		t.Fatalf("packaging check detail = %q", reopen.Detail)
	}
	det := checkByName(t, rep, "packaging is byte-identical")
	if !det.OK || strings.Contains(det.Detail, "skipped") {
		t.Fatalf("packaging determinism check did not run: %+v", det)
	}
}

// TestPackagingChecksSkipSyntheticRun: a synthetic run with no artifact files
// skips the packaging checks explicitly instead of reporting a false pass.
func TestPackagingChecksSkipSyntheticRun(t *testing.T) {
	calls := 0
	opts, fake := baseOptions(t)
	opts.Hooks.Build = fake.deterministicBuild(&calls)
	rep, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !rep.OK {
		t.Fatalf("synthetic run must still pass: %+v", rep.Checks)
	}
	if c := checkByName(t, rep, "package archives reopen cleanly"); !strings.Contains(c.Detail, "skipped") {
		t.Fatalf("expected an explicit skip, got %+v", c)
	}
}
