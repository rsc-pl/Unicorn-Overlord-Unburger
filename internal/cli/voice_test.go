package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestBuildVoiceRequiresMain(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"build", "--variant", "both", "--voice"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected usage error (2), got %d", code)
	}
	if !strings.Contains(stderr.String(), "--voice-main") {
		t.Fatalf("missing voice-main usage message: %s", stderr.String())
	}
}

func TestBuildVoiceMainRequiresVoice(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"build", "--variant", "both", "--voice-main", "main.bin"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected usage error (2), got %d", code)
	}
	if !strings.Contains(stderr.String(), "--voice") {
		t.Fatalf("missing voice usage message: %s", stderr.String())
	}
}

func TestHelpMentionsVoicePatch(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("unexpected exit code %d", code)
	}
	if !strings.Contains(stdout.String(), "--voice") {
		t.Fatalf("help must mention the voice flag: %s", stdout.String())
	}
}
