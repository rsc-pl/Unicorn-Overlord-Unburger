package tui

import (
	"context"
	"strings"
	"testing"

	"charm.land/bubbletea/v2"

	"uo-unburger/internal/appconfig"
)

// pressCmd applies one key and returns the resulting model plus the command, so a
// test can tell a navigation key apart from a quit request.
func pressCmd(t *testing.T, m *Model, key string) (*Model, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(press(key))
	got, ok := next.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", next)
	}
	return got, cmd
}

// isQuit reports whether a command produces tea.QuitMsg.
func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

// TestEscapeReturnsFromEveryScreenToOverview walks into every section from the
// Overview and checks that Escape returns, never quitting.
func TestEscapeReturnsFromEveryScreenToOverview(t *testing.T) {
	for i := 1; i < len(screenTitles); i++ {
		m := testModel(t)
		m = step(t, m, string(rune('1'+i)))
		if int(m.screen) != i {
			t.Fatalf("digit %d opened %v, want %s", i+1, m.screen, screenTitles[i])
		}
		var cmd tea.Cmd
		m, cmd = pressCmd(t, m, "esc")
		if isQuit(cmd) {
			t.Fatalf("esc on %s must never quit", screenTitles[i])
		}
		if m.screen != ScreenDashboard {
			t.Fatalf("esc on %s should return to the Overview, got %v", screenTitles[i], m.screen)
		}
	}
}

// TestEscapeWalksBackThroughTheHistoryStack checks the previous-screen behaviour
// when screens are opened in sequence.
func TestEscapeWalksBackThroughTheHistoryStack(t *testing.T) {
	m := testModel(t)
	m = step(t, m, "3") // Translations
	m = step(t, m, "]") // next section: Build
	if m.screen != ScreenBuild {
		t.Fatalf("expected Build, got %v", m.screen)
	}
	m = step(t, m, "esc")
	if m.screen != ScreenTranslations {
		t.Fatalf("first esc should return to Translations, got %v", m.screen)
	}
	m = step(t, m, "esc")
	if m.screen != ScreenDashboard {
		t.Fatalf("second esc should return to the Overview, got %v", m.screen)
	}
}

// TestEscapeOnOverviewClearsStatusAndNeverQuits.
func TestEscapeOnOverviewClearsStatusAndNeverQuits(t *testing.T) {
	m := testModel(t)
	m.setStatus("a note", false)
	m, cmd := pressCmd(t, m, "esc")
	if isQuit(cmd) {
		t.Fatal("esc on the Overview must not quit")
	}
	if m.status != "" {
		t.Fatalf("esc must clear the Overview status, got %q", m.status)
	}
	if m.screen != ScreenDashboard {
		t.Fatalf("esc on the Overview must stay on the Overview, got %v", m.screen)
	}
}

// TestEscapeDismissesConfirmAndHelp: both modals are Esc-able and dismissal does
// not run the confirmed action.
func TestEscapeDismissesConfirmAndHelp(t *testing.T) {
	m := testModel(t)
	m = step(t, m, "4") // Build
	m = step(t, m, "b") // real build confirmation
	if m.confirm == nil {
		t.Fatal("b must open a confirmation")
	}
	m, cmd := pressCmd(t, m, "esc")
	if m.confirm != nil {
		t.Fatal("esc must dismiss the confirmation")
	}
	if isQuit(cmd) || m.running() {
		t.Fatal("dismissing the confirmation must not run the action or quit")
	}

	m = step(t, m, "?")
	if !m.showHelp {
		t.Fatal("? must open the help overlay")
	}
	m, cmd = pressCmd(t, m, "esc")
	if m.showHelp {
		t.Fatal("esc must close the help overlay")
	}
	if isQuit(cmd) {
		t.Fatal("closing help must not quit")
	}
}

// TestEscapeFinishesEditingAndKeepsValues: the typed value survives, the screen is
// not left, and nothing is saved yet.
func TestEscapeFinishesEditingAndKeepsValues(t *testing.T) {
	m := testModel(t)
	m = step(t, m, "9") // Settings
	m = step(t, m, "e")
	if !m.set.form.editing {
		t.Fatal("e must begin editing the settings form")
	}
	m = step(t, m, "backspace", "backspace", "backspace", "backspace")
	m = step(t, m, "l", "i", "g", "h", "t")
	if got := m.set.form.Value("theme"); got != "light" {
		t.Fatalf("typed value = %q", got)
	}
	m, cmd := pressCmd(t, m, "esc")
	if m.set.form.editing {
		t.Fatal("esc must end editing")
	}
	if isQuit(cmd) {
		t.Fatal("esc must not quit")
	}
	if m.screen != ScreenSettings {
		t.Fatalf("finishing an edit must stay on Settings, got %v", m.screen)
	}
	if got := m.set.form.Value("theme"); got != "light" {
		t.Fatalf("the typed value must be kept, got %q", got)
	}
	if m.cfg.Theme == "light" && appconfig.Default().Theme != "light" {
		t.Fatal("esc must not have saved the edited value")
	}
}

// TestEscapeFromSettingsEditReturnsThenSaves: a second Escape navigates away, and
// the unsaved change is announced rather than silently dropped.
func TestEscapeFromSettingsEditReturnsThenSaves(t *testing.T) {
	m := testModel(t)
	m = step(t, m, "9")                       // Settings
	m = step(t, m, "right", "right", "right") // toggle the theme through the cycle
	if !m.settingsDirty() {
		t.Fatal("toggling an option must mark the settings dirty")
	}
	m, cmd := pressCmd(t, m, "esc")
	if isQuit(cmd) {
		t.Fatal("esc must not quit")
	}
	if m.screen != ScreenDashboard {
		t.Fatalf("esc from Settings should return to the Overview, got %v", m.screen)
	}
	if !strings.Contains(m.status, "unsaved") {
		t.Fatalf("leaving with unsaved settings must show a hint, got %q", m.status)
	}
}

// TestRunningTaskEscapeCancelsAndQConfirms: while a task runs, Escape cancels it
// and q asks before quitting.
func TestRunningTaskEscapeCancelsAndQConfirms(t *testing.T) {
	m := testModel(t)
	released := make(chan struct{})
	m.startTask("long task", func(ctx context.Context, prog func(string, int, int)) (string, any, error) {
		defer close(released)
		<-ctx.Done()
		return "", nil, ctx.Err()
	})
	m, cmd := pressCmd(t, m, "esc")
	if isQuit(cmd) {
		t.Fatal("esc while a task runs must cancel, not quit")
	}
	if m.task == nil || !m.task.cancelled {
		t.Fatal("esc must request cancellation of the running task")
	}
	<-released

	m, _ = pressCmd(t, m, "q")
	if m.confirm == nil {
		t.Fatal("q while a task runs must ask for confirmation")
	}
}

// TestFilterTypingQDoesNotQuit: in filter mode every key belongs to the filter.
func TestFilterTypingQDoesNotQuit(t *testing.T) {
	m := testModel(t)
	m.setScreen(ScreenTranslations)
	seedRecords(m)
	m.pane = 1
	m, _ = pressCmd(t, m, "/")
	m, cmd := pressCmd(t, m, "q")
	if isQuit(cmd) {
		t.Fatal("typing q into a filter must not quit")
	}
	if !m.trans.table.filterOn || m.trans.table.filter != "q" {
		t.Fatalf("q should be part of the filter, got %q", m.trans.table.filter)
	}
}

// TestSetupEscapeNeverQuitsAndFinishes: the guided setup is Esc-able; from the
// first step Escape finishes it and marks it complete.
func TestSetupEscapeNeverQuitsAndFinishes(t *testing.T) {
	m, _ := isolatedSetupModel(t)
	// Advance one step so Escape backs up instead of finishing.
	m = step(t, m, "enter")
	m, cmd := pressCmd(t, m, "esc")
	if isQuit(cmd) {
		t.Fatal("esc in setup must not quit")
	}
	if m.setup == nil || m.setup.step != 0 {
		t.Fatalf("esc should back up one step, got %+v", m.setup)
	}
	m, cmd = pressCmd(t, m, "esc")
	if isQuit(cmd) {
		t.Fatal("esc from the first setup step must not quit")
	}
	if m.setup != nil {
		t.Fatal("esc from the first step must finish the setup")
	}
	if m.cfg.SetupComplete {
		t.Fatal("finishing without a verified source must not mark it complete")
	}
	if m.screen != ScreenDashboard {
		t.Fatalf("after setup the Overview should be shown, got %v", m.screen)
	}
}

// TestTabCycleNeverTrapsFocus: every pane ring returns to its starting pane.
func TestTabCycleNeverTrapsFocus(t *testing.T) {
	for i := range screenTitles {
		m := testModel(t)
		m.setScreen(Screen(i))
		start := m.paneKind()
		ring := len(m.kinds)
		for n := 0; n < ring; n++ {
			m = step(t, m, "tab")
		}
		if m.paneKind() != start {
			t.Fatalf("%s: tab did not cycle back to %v (got %v, ring %d)", screenTitles[i], start, m.paneKind(), ring)
		}
		for n := 0; n < ring; n++ {
			m = step(t, m, "shift+tab")
		}
		if m.paneKind() != start {
			t.Fatalf("%s: shift+tab did not cycle back to %v (got %v)", screenTitles[i], start, m.paneKind())
		}
	}
}
