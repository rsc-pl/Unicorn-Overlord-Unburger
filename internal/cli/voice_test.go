package cli

import (
	"bytes"
	"strings"
	"testing"
)

// The voice patch and title footer are part of every normal build; the old
// opt-in flags are gone. Passing one is a usage error rather than a silently
// ignored switch.
func TestBuildRejectsRemovedVoiceFlags(t *testing.T) {
	for _, flag := range []string{"--voice", "--voice-main", "--title-footer"} {
		var stdout, stderr bytes.Buffer
		code := Run([]string{"build", "--variant", "both", flag, "x"}, &stdout, &stderr)
		if code != 2 {
			t.Fatalf("%s: expected usage error (2), got %d (%s)", flag, code, stderr.String())
		}
		if !strings.Contains(stderr.String(), "unknown build argument") {
			t.Fatalf("%s: missing unknown-argument message: %s", flag, stderr.String())
		}
	}
}

func TestPackageRejectsRemovedVoiceFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"package", "--run", "x", "--voice"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected usage error (2), got %d", code)
	}
	if !strings.Contains(stderr.String(), "unknown package argument") {
		t.Fatalf("missing unknown-argument message: %s", stderr.String())
	}
}

func TestHelpMentionsAutomaticVoiceAndFooter(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("unexpected exit code %d: %s", code, stderr.String())
	}
	help := stdout.String()
	for _, want := range []string{
		".local/work/source/exefs/main",
		".local/work/source/romfs/GUI/Opening100.ftx",
		"always adds",
	} {
		if !strings.Contains(help, want) {
			t.Fatalf("help must mention %q:\n%s", want, help)
		}
	}
	if strings.Contains(help, "--voice") || strings.Contains(help, "--title-footer") {
		t.Fatalf("help still advertises a removed flag:\n%s", help)
	}
}
