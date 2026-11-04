package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"uo-unburger/internal/tui"
)

// The no-arguments behaviour is what a packaging script depends on: with a
// terminal it opens the interface, without one it must print the help and use
// the usage exit code so a pipe never receives control sequences.
func TestNoArgsWithoutATerminalPrintsHelpAndExits2(t *testing.T) {
	if tui.IsTerminal(os.Stdin) {
		t.Skip("stdin is a terminal; the no-argument path would start the interface")
	}
	var stdout, stderr bytes.Buffer
	code := Run(nil, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("help must go to stderr on the no-terminal path, got stdout %q", stdout.String())
	}
	got := stderr.String()
	for _, want := range []string{"no terminal detected", "Commands:", "tui"} {
		if !strings.Contains(got, want) {
			t.Fatalf("stderr missing %q:\n%s", want, got)
		}
	}
}

func TestNoArgsIsRepeatable(t *testing.T) {
	if tui.IsTerminal(os.Stdin) {
		t.Skip("stdin is a terminal")
	}
	first := func() (int, string) {
		var stdout, stderr bytes.Buffer
		code := Run(nil, &stdout, &stderr)
		return code, stderr.String()
	}
	c1, o1 := first()
	c2, o2 := first()
	if c1 != c2 || o1 != o2 {
		t.Fatalf("no-argument output must be deterministic")
	}
}

func TestTUICommandRequiresATerminal(t *testing.T) {
	if tui.IsTerminal(os.Stdin) && tui.IsTerminal(os.Stdout) {
		t.Skip("a terminal is present")
	}
	var stdout, stderr bytes.Buffer
	code := Run([]string{"tui"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "terminal is required") {
		t.Fatalf("missing terminal message: %s", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("the interface must not write to stdout without a terminal: %q", stdout.String())
	}
}

func TestTUIRejectsArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"tui", "--bogus"}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "usage: uo-unburger tui") {
		t.Fatalf("missing usage: %s", stderr.String())
	}
}

func TestHelpMentionsTheInterface(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	got := stdout.String()
	if !strings.Contains(got, "interactive interface") || !strings.Contains(got, "tui") {
		t.Fatalf("help must describe the interface:\n%s", got)
	}
	if !strings.Contains(got, "exits 2") {
		t.Fatalf("help must document the no-terminal exit code:\n%s", got)
	}
}
