package tui

import (
	"context"
	"strings"
	"sync"
	"testing"

	"charm.land/bubbletea/v2"

	"uo-unburger/internal/appconfig"
)

// colorFreeModel builds a model whose rendering never contains escape
// sequences, so layout assertions can count runes directly.
func colorFreeModel(t *testing.T, w, h int) *Model {
	t.Helper()
	env := func(k string) string {
		if k == "NO_COLOR" {
			return "1"
		}
		return ""
	}
	m := NewModel(Options{
		Config:     appconfig.Default(),
		ConfigPath: "/nonexistent/config.json",
		Version:    "test",
		Getenv:     env,
	})
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return m
}

func maxRunes(s string) int {
	best := 0
	for _, line := range strings.Split(s, "\n") {
		if n := len([]rune(line)); n > best {
			best = n
		}
	}
	return best
}

// TestNumberKeysJumpToEveryScreen: each digit selects its section (an
// accelerator; the sidebar and the bracket keys reach the same screens).
func TestNumberKeysJumpToEveryScreen(t *testing.T) {
	for i, want := range screenTitles {
		m := testModel(t)
		m = step(t, m, string(rune('1'+i)))
		if int(m.screen) != i {
			t.Fatalf("%s selected %v, want %s", string(rune('1'+i)), m.screen, want)
		}
	}
}

// TestResizeFallbacksAndFullRender covers the startup, compact and full layouts
// at the exact boundaries.
func TestResizeFallbacksAndFullRender(t *testing.T) {
	m := testModel(t)

	m.Update(tea.WindowSizeMsg{Width: 0, Height: 0})
	if got := view(m); !strings.Contains(got, "starting") {
		t.Fatalf("0x0 render = %q, want the startup placeholder", got)
	}

	// One column cannot fit the message text, but the fallback must still draw
	// something instead of a blank screen (regression: it used to render empty
	// lines at width 1).
	one := view(colorFreeModel(t, 1, 1))
	if strings.TrimSpace(one) == "" {
		t.Fatal("a 1x1 terminal rendered a blank screen")
	}
	if n := maxRunes(one); n > 1 {
		t.Fatalf("1x1 render exceeded one column: %q", one)
	}

	compact := []struct{ w, h int }{{40, 10}, {47, 12}, {48, 11}, {200, 3}}
	for _, c := range compact {
		mm := colorFreeModel(t, c.w, c.h)
		got := view(mm)
		if !strings.Contains(got, "Terminal too small") {
			t.Fatalf("%dx%d render = %q, want the compact fallback", c.w, c.h, got)
		}
		if strings.Contains(got, "Sections") {
			t.Fatalf("%dx%d render unexpectedly drew the sidebar", c.w, c.h)
		}
	}

	mm := colorFreeModel(t, 48, 12)
	got := view(mm)
	if !strings.Contains(got, "Sections") || strings.Contains(got, "Terminal too small") {
		t.Fatalf("48x12 render should be the full layout: %q", got)
	}

	big := colorFreeModel(t, 200, 50)
	if !strings.Contains(view(big), "Sections") {
		t.Fatal("200x50 render should be the full layout")
	}
}

// TestCompactFallbackKeepsTheReasonAtVeryNarrowWidths is the regression test
// for a defect found by PTY capture: at 20x6 the compact fallback truncated the
// message to "Terminal…", so the user could not read *why* the interface was
// blank. The wrapped fallback must keep the reason and the required size.
func TestCompactFallbackKeepsTheReasonAtVeryNarrowWidths(t *testing.T) {
	for _, size := range []struct{ w, h int }{{20, 6}, {18, 6}, {12, 4}, {10, 3}} {
		m := colorFreeModel(t, size.w, size.h)
		got := view(m)
		flat := strings.ReplaceAll(got, "\n", " ")
		if !strings.Contains(flat, "too small") {
			t.Fatalf("%dx%d fallback lost the reason: %q", size.w, size.h, got)
		}
		if !strings.Contains(flat, "48x12") || !strings.Contains(flat, "need") {
			t.Fatalf("%dx%d fallback lost the required size: %q", size.w, size.h, got)
		}
		if strings.Contains(flat, "Terminal…") || strings.Contains(flat, "resize…") {
			t.Fatalf("%dx%d fallback truncated a message with an ellipsis: %q", size.w, size.h, got)
		}
		for _, ln := range strings.Split(got, "\n") {
			if n := maxRunes(ln); n > size.w {
				t.Fatalf("%dx%d fallback line is %d columns wide: %q", size.w, size.h, n, ln)
			}
		}
	}
}

// TestWrapNeverDropsContent checks the wrap helper used by the compact fallback.
func TestWrapNeverDropsContent(t *testing.T) {
	samples := []string{
		"Terminal too small",
		"20x6; need 48x12",
		"resize, or press q to quit, ? for help",
		"a",
		"supercalifragilisticexpialidocious",
	}
	for _, s := range samples {
		for _, w := range []int{1, 2, 5, 9, 18, 40, 80} {
			lines := wrap(s, w)
			for _, ln := range lines {
				if n := maxRunes(ln); n > w {
					t.Fatalf("wrap(%q,%d) produced a %d-column line %q", s, w, n, ln)
				}
			}
			joined := strings.Join(lines, " ")
			// Wrapping may insert or remove spaces at break points; compare the
			// non-whitespace content so nothing is silently dropped.
			strip := func(v string) string { return strings.Join(strings.Fields(v), "") }
			if strip(joined) != strip(s) {
				t.Fatalf("wrap(%q,%d) changed the text to %q", s, w, joined)
			}
			for _, ln := range lines {
				if strings.Contains(ln, "…") {
					t.Fatalf("wrap(%q,%d) introduced an ellipsis", s, w)
				}
			}
		}
	}
}

// TestEveryScreenRendersWithinTheTerminalWidth checks that no screen ever emits
// a line wider than the terminal it was told about.
func TestEveryScreenRendersWithinTheTerminalWidth(t *testing.T) {
	for _, size := range []struct{ w, h int }{{48, 12}, {60, 20}, {100, 30}, {200, 50}} {
		for i := range screenTitles {
			m := colorFreeModel(t, size.w, size.h)
			m.setScreen(Screen(i))
			got := view(m)
			if strings.TrimSpace(got) == "" {
				t.Fatalf("%s at %dx%d rendered nothing", screenTitles[i], size.w, size.h)
			}
			if n := maxRunes(got); n > size.w {
				t.Fatalf("%s at %dx%d rendered a %d-rune line", screenTitles[i], size.w, size.h, n)
			}
		}
	}
}

// TestNoColorRendersEveryScreenWithoutEscapes: NO_COLOR must fully disable
// styling on all nine screens, including the help overlay and a confirmation.
func TestNoColorRendersEveryScreenWithoutEscapes(t *testing.T) {
	for i := range screenTitles {
		m := colorFreeModel(t, 100, 30)
		m.setScreen(Screen(i))
		if got := view(m); strings.Contains(got, "\x1b[") {
			t.Fatalf("%s emitted escape sequences with NO_COLOR set", screenTitles[i])
		}
		if got := view(step(t, m, "?")); strings.Contains(got, "\x1b[") {
			t.Fatalf("%s help overlay emitted escape sequences with NO_COLOR set", screenTitles[i])
		}
	}

	// A confirmation dialog in no-color mode is plain too.
	m := colorFreeModel(t, 100, 30)
	m.setScreen(ScreenBuild)
	m = step(t, m, "b")
	if m.confirm == nil {
		t.Fatal("b on the Build screen should open a confirmation")
	}
	if got := view(m); strings.Contains(got, "\x1b[") {
		t.Fatalf("confirmation emitted escape sequences: %q", got)
	}
}

// TestNoColorKeepsSelectionVisible: with colour off the selected row still
// carries the "›" marker, so the interface never becomes an undifferentiated
// block of text.
func TestNoColorKeepsSelectionVisible(t *testing.T) {
	m := colorFreeModel(t, 100, 30)
	m.setScreen(ScreenTranslations)
	seedRecords(m)
	got := view(m)
	if !strings.Contains(got, "›") {
		t.Fatalf("the plain fallback lost the selection marker:\n%s", got)
	}
}

// TestStatusSetAndCleared: a screen action that cannot run reports an error
// status, and escape clears it.
func TestStatusSetAndCleared(t *testing.T) {
	m := testModel(t)
	m = step(t, m, "2") // Game files
	m = step(t, m, "v") // no game archive configured
	if m.status == "" || !m.statusErr {
		t.Fatalf("expected an error status, got %q (err=%v)", m.status, m.statusErr)
	}
	m = step(t, m, "esc")
	if m.status != "" || m.statusErr {
		t.Fatalf("escape must clear the status, got %q (err=%v)", m.status, m.statusErr)
	}
}

// TestEditCancellation: entering edit mode and pressing escape restores the
// non-editing state without starting a task.
func TestEditCancellation(t *testing.T) {
	m := testModel(t)
	m = step(t, m, "2") // Game files
	m = step(t, m, "e")
	f := m.activeForm()
	if f == nil || !f.editing {
		t.Fatal("e must focus the Game files form")
	}
	m = step(t, m, "esc")
	if f.editing {
		t.Fatal("escape must leave edit mode")
	}
	if m.running() {
		t.Fatal("cancelling an edit must not start a task")
	}
}

// TestConfirmYesNoOnBuild: the Build screen's real-build confirmation is a plain
// yes/no; n cancels, y runs the configured action.
func TestConfirmYesNoOnBuild(t *testing.T) {
	m := testModel(t)
	m.build.form.SetValues(map[string]string{"variant": "honorifics"})
	m.setScreen(ScreenBuild)
	m = step(t, m, "b")
	if m.confirm == nil {
		t.Fatal("b must open a confirmation")
	}
	m = step(t, m, "n")
	if m.confirm != nil {
		t.Fatal("n must dismiss the confirmation")
	}
	if m.running() {
		t.Fatal("n must not start the build")
	}

	m = step(t, m, "b")
	m = step(t, m, "y")
	if m.confirm != nil {
		t.Fatal("y must close the confirmation")
	}
	// The build lacks an archive and reference data, so the action refuses with a
	// status; what matters here is that y actually invoked the action.
	if m.status == "" && !m.running() {
		t.Fatal("y should have invoked the confirmed action")
	}
}

// TestQuitWhileTaskRunsAsksFirst: q during a task opens a confirmation, escape
// returns to the running task, and y cancels then quits.
func TestQuitWhileTaskRunsAsksFirst(t *testing.T) {
	m := testModel(t)
	exited := make(chan struct{})
	var once sync.Once
	m.startTask("slow test task", func(ctx context.Context, prog func(string, int, int)) (string, any, error) {
		defer once.Do(func() { close(exited) })
		<-ctx.Done()
		return "", nil, ctx.Err()
	})
	if !m.running() {
		t.Fatal("task should be running")
	}

	m = step(t, m, "q")
	if m.confirm == nil {
		t.Fatal("q during a task must ask for confirmation")
	}
	m = step(t, m, "esc")
	if m.confirm != nil {
		t.Fatal("escape must dismiss the quit confirmation")
	}
	if !m.running() {
		t.Fatal("dismissing the confirmation must not cancel the task")
	}

	m = step(t, m, "q")
	_, cmd := m.Update(press("y"))
	if m.confirm != nil {
		t.Fatal("y must close the confirmation")
	}
	if cmd == nil {
		t.Fatal("y must request a quit")
	}
	<-exited
}

// TestEscapeCancelsARunningTask: escape cancels without quitting and the task's
// context is actually cancelled.
func TestEscapeCancelsARunningTask(t *testing.T) {
	m := testModel(t)
	exited := make(chan error, 1)
	m.startTask("cancellable", func(ctx context.Context, prog func(string, int, int)) (string, any, error) {
		<-ctx.Done()
		exited <- ctx.Err()
		return "", nil, ctx.Err()
	})
	m = step(t, m, "esc")
	err := <-exited
	if err == nil {
		t.Fatal("escape must cancel the task context")
	}
	if m.task == nil || !m.task.cancelled {
		t.Fatal("the task should be marked cancelled")
	}
}
