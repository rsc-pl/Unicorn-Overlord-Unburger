package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/bubbletea/v2"

	"uo-unburger/internal/appconfig"
	"uo-unburger/internal/project"
	"uo-unburger/internal/source"
)

// runCmd executes a command returned by Update and feeds its message back.
func runCmd(t *testing.T, m *Model, cmd tea.Cmd) *Model {
	t.Helper()
	if cmd == nil {
		return m
	}
	msg := cmd()
	next, _ := m.Update(msg)
	got, ok := next.(*Model)
	if !ok {
		t.Fatalf("Update returned %T", next)
	}
	return got
}

// isolatedSetupModel returns a model pinned to a temporary project root with the
// guided setup open.
func isolatedSetupModel(t *testing.T) (*Model, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("UO_UNBURGER_ROOT", root)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfgPath := filepath.Join(root, ".local", "uo-unburger.json")
	m := NewModel(Options{
		Config:     appconfig.Default(),
		ConfigPath: cfgPath,
		Version:    "test",
		Getenv:     noEnv,
		FirstRun:   true,
	})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return m, cfgPath
}

func TestFirstRunSetupOpensAndRenders(t *testing.T) {
	m, _ := isolatedSetupModel(t)
	if m.setup == nil {
		t.Fatal("FirstRun must open the guided setup")
	}
	got := view(m)
	for _, want := range []string{"First-run setup", "Welcome", "Project root", "enter: continue"} {
		if !strings.Contains(got, want) {
			t.Fatalf("setup view missing %q:\n%s", want, got)
		}
	}
}

func TestFirstRunSetupAdvancesBacksAndSaves(t *testing.T) {
	m, cfgPath := isolatedSetupModel(t)

	// Walk to the last step.
	for m.setup.step < len(setupSteps)-1 {
		m = step(t, m, "enter")
	}
	if got := view(m); !strings.Contains(got, "Ready") {
		t.Fatalf("expected the Ready step:\n%s", got)
	}
	// Esc backs up one step, enter returns.
	m = step(t, m, "esc")
	if m.setup == nil || m.setup.step != len(setupSteps)-2 {
		t.Fatalf("esc must go back a step, step=%v", m.setup)
	}
	m = step(t, m, "enter")

	// Finish: the setup closes and the settings are written. This root has no
	// verified archive and no translations, so setup must NOT be marked complete.
	next, cmd := m.Update(press("enter"))
	m = next.(*Model)
	if m.setup != nil {
		t.Fatal("finishing must close the guided setup")
	}
	if m.cfg.SetupComplete {
		t.Fatal("setup must not be marked complete while the source and translations are unresolved")
	}
	m = runCmd(t, m, cmd)
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("settings were not saved: %v", err)
	}
	if !strings.Contains(string(data), `"setup_complete": false`) {
		t.Fatalf("saved settings must keep the setup flag false:\n%s", data)
	}
}

// TestSetupSkipsWhenEverythingIsFound: when a verified source and the translation
// files are both present, a first run skips the flow entirely and records complete.
func TestSetupSkipsWhenEverythingIsFound(t *testing.T) {
	root := t.TempDir()
	t.Setenv("UO_UNBURGER_ROOT", root)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Join(root, "translations"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "translations", "honorifics.tsv"), []byte("record_id\tenglish_text\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(root, "archive.cpk")
	if err := os.WriteFile(src, []byte("archive"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := appconfig.Default()
	cfg.SourceCPK = src
	cfgPath := filepath.Join(root, ".local", "uo-unburger.json")
	m := NewModel(Options{Config: cfg, ConfigPath: cfgPath, Version: "test", Getenv: noEnv, FirstRun: true})
	if m.setup != nil {
		t.Fatal("setup must be skipped when a verified source and the translations are present")
	}
	if !m.cfg.SetupComplete {
		t.Fatal("skipping setup with everything found must record it complete")
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("the complete flag must be persisted: %v", err)
	}
	if !strings.Contains(string(data), `"setup_complete": true`) {
		t.Fatalf("saved settings missing setup flag:\n%s", data)
	}
}

func TestFirstRunSetupEscFromFirstStepFinishes(t *testing.T) {
	m, _ := isolatedSetupModel(t)
	m = step(t, m, "esc")
	if m.setup != nil {
		t.Fatal("esc from the first step must finish the setup")
	}
	if m.cfg.SetupComplete {
		t.Fatal("a skip without resources must not mark setup complete")
	}
}

func TestSetupRemembersResolvedSource(t *testing.T) {
	root := t.TempDir()
	t.Setenv("UO_UNBURGER_ROOT", root)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfgPath := filepath.Join(root, ".local", "uo-unburger.json")
	m := NewModel(Options{Config: appconfig.Default(), ConfigPath: cfgPath, Version: "test", Getenv: noEnv})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	// Simulate the flow reaching its final step with a resolved cache archive and
	// the translation files present.
	archive := filepath.Join(root, ".local", "cache", "010069401ADB8800-262144", "Unicorn_US.CPK")
	m.setup = &setupState{
		step: len(setupSteps) - 1,
		info: setupInfo{
			State:          source.State{Root: root, Layout: project.NewLayout(root), Source: archive, Provenance: source.ProvenanceCache},
			TranslationsOK: true,
		},
		choices: []setupChoice{{Path: archive, Provenance: source.ProvenanceCache}},
	}
	next, cmd := m.Update(press("enter"))
	m = next.(*Model)
	m = runCmd(t, m, cmd)
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("settings not saved: %v", err)
	}
	if !strings.Contains(string(data), archive) {
		t.Fatalf("the resolved source must be remembered:\n%s", data)
	}
	if !m.cfg.SetupComplete {
		t.Fatal("a resolved source plus translations must record setup complete")
	}
}

func TestSetupActionReopensTheFlow(t *testing.T) {
	root := t.TempDir()
	t.Setenv("UO_UNBURGER_ROOT", root)
	m := testModel(t)
	if m.setup != nil {
		t.Fatal("a normal model must not open the setup")
	}
	m.runAction("setup")
	if m.setup == nil {
		t.Fatal("the setup action must open the guided flow")
	}
}

// TestFirstRunOpensWhenConfigMissing pins the startup integration: a checkout with
// no saved configuration loads with setup_complete=false, the guided setup opens
// automatically, finishing it returns to the normal Overview, and the interface is
// usable straight away.
func TestFirstRunOpensWhenConfigMissing(t *testing.T) {
	root := t.TempDir()
	t.Setenv("UO_UNBURGER_ROOT", root)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, cfgPath, err := appconfig.Load()
	if err != nil {
		t.Fatalf("appconfig.Load: %v", err)
	}
	if cfg.SetupComplete {
		t.Fatal("a missing configuration must not count as completed setup")
	}
	m := NewModel(Options{
		Config:     cfg,
		ConfigPath: cfgPath,
		Version:    "test",
		Getenv:     noEnv,
		FirstRun:   !cfg.SetupComplete,
	})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if m.setup == nil {
		t.Fatal("the guided setup must open automatically on a first run")
	}
	if m.screen != ScreenDashboard {
		t.Fatalf("the setup opens over the Overview, got %v", m.screen)
	}
	// Escape from the first step finishes the flow and saves the choices; because
	// this root has no verified source, setup_complete stays false so the flow is
	// offered again next launch. The normal Overview remains usable regardless.
	next, cmd := m.Update(press("esc"))
	m = next.(*Model)
	m = runCmd(t, m, cmd)
	if m.setup != nil {
		t.Fatal("finishing the setup must close it")
	}
	if m.cfg.SetupComplete {
		t.Fatal("unresolved resources must keep setup_complete false")
	}
	if m.screen != ScreenDashboard {
		t.Fatalf("after the setup the Overview must be shown, got %v", m.screen)
	}
	m = step(t, m, "3")
	if m.screen != ScreenTranslations {
		t.Fatalf("the interface must be usable after the setup, got %v", m.screen)
	}
}
