package preflight

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestPreCancelledContextRunsNoChecks: an already-cancelled context aborts
// before the first check, returns the context error and removes its scratch
// directory.
func TestPreCancelledContextRunsNoChecks(t *testing.T) {
	opts, _ := baseOptions(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	rep, err := Run(ctx, opts)
	if err == nil {
		t.Fatal("a cancelled context must abort the suite")
	}
	if !IsCancelled(err) {
		t.Fatalf("err = %v, want a cancellation error", err)
	}
	if rep == nil {
		t.Fatal("a report must still be returned")
	}
	if len(rep.Checks) != 0 {
		t.Fatalf("checks ran after cancellation: %+v", rep.Checks)
	}
	if rep.Scratch != "" {
		if _, statErr := os.Stat(rep.Scratch); !os.IsNotExist(statErr) {
			t.Fatalf("scratch directory %s was left behind", rep.Scratch)
		}
	}
}

// TestCancelAfterDryRunPublishesNothing cancels from the progress callback once
// the dry-run check has completed: the suite must stop before the publish stage,
// so no real build runs and the scratch directory holds no run output.
func TestCancelAfterDryRunPublishesNothing(t *testing.T) {
	calls := 0
	opts, fake := baseOptions(t)
	opts.Hooks.Build = fake.deterministicBuild(&calls)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stages []string
	opts.Progress = func(stage string, done, total int) {
		stages = append(stages, stage)
		if stage == "clean dry-run build (both editions)" {
			cancel()
		}
	}

	rep, err := Run(ctx, opts)
	if !IsCancelled(err) {
		t.Fatalf("err = %v, want a cancellation error", err)
	}
	if calls != 0 {
		t.Fatalf("%d non-dry builds ran after cancellation", calls)
	}
	for _, name := range stages {
		if name == "publish build to scratch" {
			t.Fatal("the suite reached the publish check after cancellation")
		}
	}
	if rep.Scratch != "" {
		if _, statErr := os.Stat(rep.Scratch); !os.IsNotExist(statErr) {
			t.Fatalf("scratch directory %s was left behind", rep.Scratch)
		}
	}
}

// TestProgressTotalsAreMeasured: progress reports the real check count, is
// strictly increasing, and ends exactly at the total.
func TestProgressTotalsAreMeasured(t *testing.T) {
	calls := 0
	opts, fake := baseOptions(t)
	opts.Hooks.Build = fake.deterministicBuild(&calls)

	var doneCounts []int
	var totals []int
	opts.Progress = func(stage string, done, total int) {
		doneCounts = append(doneCounts, done)
		totals = append(totals, total)
	}
	rep, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !rep.OK {
		t.Fatalf("suite did not pass: %s", rep.Text())
	}
	if len(doneCounts) != len(checkNames) {
		t.Fatalf("progress callbacks = %d, want %d", len(doneCounts), len(checkNames))
	}
	for i, total := range totals {
		if total != len(checkNames) {
			t.Fatalf("callback %d total = %d, want %d", i, total, len(checkNames))
		}
	}
	for i, done := range doneCounts {
		if done != i+1 {
			t.Fatalf("callback %d done = %d, want %d", i, done, i+1)
		}
	}
	if doneCounts[len(doneCounts)-1] != len(checkNames) {
		t.Fatalf("final done = %d, want %d", doneCounts[len(doneCounts)-1], len(checkNames))
	}
	if len(rep.Checks) != len(checkNames) {
		t.Fatalf("report checks = %d, want %d", len(rep.Checks), len(checkNames))
	}
}

// TestKeepScratchRetainsOutput: with KeepScratch the explicitly supplied scratch
// directory survives a cancelled run, which is what an operator inspects after a
// failed gate.
func TestKeepScratchRetainsOutput(t *testing.T) {
	opts, _ := baseOptions(t)
	opts.ScratchDir = filepath.Join(t.TempDir(), "scratch")
	opts.KeepScratch = true
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := Run(ctx, opts); !IsCancelled(err) {
		t.Fatalf("err = %v, want cancellation", err)
	}
	if info, statErr := os.Stat(opts.ScratchDir); statErr != nil || !info.IsDir() {
		t.Fatalf("kept scratch directory missing: %v", statErr)
	}
}
