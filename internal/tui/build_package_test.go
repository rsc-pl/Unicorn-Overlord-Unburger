package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"uo-unburger/internal/build"
)

// TestBuildScreenTargetAndPackageControls: the Build screen exposes a packaging
// target selector and a package toggle, both changed with the left/right arrow
// keys, without altering the rest of the screen. The normal release flow
// defaults to packaging ON and targeting BOTH layouts.
func TestBuildScreenTargetAndPackageControls(t *testing.T) {
	m := testModel(t)
	m.setScreen(ScreenBuild)
	m.build.options.SetRows(m.buildOptionRows())

	if m.build.target != build.TargetBoth {
		t.Fatalf("default packaging target = %q, want both", m.build.target)
	}
	// The cycle is atmosphere, emulator, both; starting on both, right wraps to
	// atmosphere and left wraps to emulator.
	m.build.options.SetCursor("target")
	m = step(t, m, "right")
	if m.build.target != build.TargetAtmosphere {
		t.Fatalf("right on target = %q, want atmosphere", m.build.target)
	}
	m = step(t, m, "right")
	if m.build.target != build.TargetEmulator {
		t.Fatalf("right on target = %q, want emulator", m.build.target)
	}
	m = step(t, m, "right")
	if m.build.target != build.TargetBoth {
		t.Fatalf("right on target = %q, want both", m.build.target)
	}
	m = step(t, m, "left")
	if m.build.target != build.TargetEmulator {
		t.Fatalf("left on target = %q, want emulator", m.build.target)
	}

	// Packaging is on by default; the arrow keys toggle it.
	m.build.options.SetCursor("package")
	if !m.build.packageOn {
		t.Fatal("packaging should default on")
	}
	m = step(t, m, "right")
	if m.build.packageOn {
		t.Fatal("right on the package row should disable packaging")
	}
	m = step(t, m, "right")
	if !m.build.packageOn {
		t.Fatal("right on the package row should re-enable packaging")
	}

	// The options list reflects the toggled values.
	var sawTarget, sawPackage bool
	for _, r := range m.buildOptionRows() {
		if r.ID == "target" && r.Cells[1] == "emulator" {
			sawTarget = true
		}
		if r.ID == "package" && r.Cells[1] == "on" {
			sawPackage = true
		}
	}
	if !sawTarget || !sawPackage {
		t.Fatalf("options did not reflect target/package state: %+v", m.buildOptionRows())
	}
	if got := view(m); !strings.Contains(got, "Package target") || !strings.Contains(got, "Package ZIPs") {
		t.Fatalf("Build screen does not render the packaging rows:\n%s", got)
	}
}

// TestConflictDialogChoicesRoundTrip: a running operation can ask the operator a
// question. It appears as the "Output already exists." dialog with Replace /
// Keep both / Cancel, one key answers for the whole output set, the value is
// delivered back to the operation, and the operation then completes.
func TestConflictDialogChoicesRoundTrip(t *testing.T) {
	m := testModel(t)
	answer := make(chan string, 1)
	cmd := m.startTaskAsk("package", func(ctx context.Context, prog progressFunc, ask askFunc) (string, any, error) {
		v, err := ask(taskQuestion{
			Title:  "Output already exists.",
			Detail: "2 output file(s)",
			Choices: []taskChoice{
				{Key: "r", Label: "Replace the existing files", Value: "replace"},
				{Key: "k", Label: "Keep both (new files get a -2, -3 … name)", Value: "keep"},
				{Key: "c", Label: "Cancel (change nothing)", Value: "cancel"},
			},
		})
		if err != nil {
			return "", nil, err
		}
		answer <- v
		return "done", nil, nil
	})
	if cmd == nil {
		t.Fatal("startTaskAsk must return a wait command")
	}
	next, c := m.Update(cmd())
	m = next.(*Model)
	if m.confirm == nil || len(m.confirm.choices) != 3 {
		t.Fatalf("an ask must open a 3-choice dialog, got %+v", m.confirm)
	}
	if got := view(m); !strings.Contains(got, "Output already exists.") ||
		!strings.Contains(got, "Replace") || !strings.Contains(got, "Keep both") || !strings.Contains(got, "Cancel") {
		t.Fatalf("dialog does not render the three choices:\n%s", got)
	}
	next, c = m.Update(press("k"))
	m = next.(*Model)
	if m.confirm != nil {
		t.Fatal("the dialog should close after a choice")
	}
	select {
	case v := <-answer:
		if v != "keep" {
			t.Fatalf("answer = %q, want keep", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the answer was never delivered to the task")
	}
	if c == nil {
		t.Fatal("the choice must resume waiting on the task")
	}
	if _, ok := c().(taskDoneMsg); !ok {
		t.Fatalf("after answering, the task must finish")
	}
}

// TestConflictDialogEscapeCancels: Escape takes the last (Cancel) choice, so a
// dismissed dialog still unblocks the operation instead of leaving it stuck.
func TestConflictDialogEscapeCancels(t *testing.T) {
	m := testModel(t)
	answer := make(chan string, 1)
	cmd := m.startTaskAsk("package", func(ctx context.Context, prog progressFunc, ask askFunc) (string, any, error) {
		v, _ := ask(taskQuestion{Title: "Output already exists.", Choices: []taskChoice{
			{Key: "r", Label: "Replace", Value: "replace"},
			{Key: "c", Label: "Cancel", Value: "cancel"},
		}})
		answer <- v
		return "", nil, context.Canceled
	})
	next, _ := m.Update(cmd())
	m = next.(*Model)
	if m.confirm == nil {
		t.Fatal("expected a dialog")
	}
	next, c := m.Update(press("esc"))
	m = next.(*Model)
	if m.confirm != nil {
		t.Fatal("escape must dismiss the dialog")
	}
	select {
	case v := <-answer:
		if v != "cancel" {
			t.Fatalf("escape sent %q, want the cancel choice", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("escape did not unblock the task")
	}
	if c == nil {
		t.Fatal("escape must resume waiting on the task")
	}
	msg := c()
	if _, ok := msg.(taskDoneMsg); !ok {
		t.Fatalf("want taskDoneMsg, got %T", msg)
	}
}

// TestAskConflictPolicyMapsChoices: the dialog answer maps exhaustively to the
// build conflict policy, and anything unexpected (including a cancelled task)
// cancels the publish.
func TestAskConflictPolicyMapsChoices(t *testing.T) {
	m := testModel(t)
	for _, c := range []struct {
		answer string
		want   build.ConflictPolicy
	}{
		{"replace", build.ConflictReplace},
		{"keep", build.ConflictKeepBoth},
		{"cancel", build.ConflictCancel},
		{"", build.ConflictCancel},
	} {
		ask := func(q taskQuestion) (string, error) {
			if q.Title != "Output already exists." {
				t.Fatalf("question title = %q", q.Title)
			}
			if len(q.Choices) != 3 {
				t.Fatalf("want 3 choices, got %d", len(q.Choices))
			}
			return c.answer, nil
		}
		if got := m.askConflictPolicy(context.Background(), ask, []string{"a.zip"}); got != c.want {
			t.Fatalf("answer %q mapped to %v, want %v", c.answer, got, c.want)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := m.askConflictPolicy(ctx, func(taskQuestion) (string, error) { return "replace", nil }, []string{"a.zip"}); got != build.ConflictCancel {
		t.Fatalf("a cancelled context must cancel the publish, got %v", got)
	}
}
