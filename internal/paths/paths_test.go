package paths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDisplayRelativeUnchanged(t *testing.T) {
	if got := Display("internal/paths/paths.go"); got != "internal/paths/paths.go" {
		t.Fatalf("relative path changed: %q", got)
	}
	if got := Display("./a//b"); got != "a/b" {
		t.Fatalf("relative path not cleaned: %q", got)
	}
	if got := Display(""); got != "" {
		t.Fatalf("empty path: %q", got)
	}
}

func TestDisplayAbsoluteUnderCwd(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(wd, "internal", "build", "build.go")
	if got := Display(abs); got != "internal/build/build.go" {
		t.Fatalf("under-cwd path = %q", got)
	}
}

func TestDisplayAbsoluteOutsideStripsDirectory(t *testing.T) {
	abs := filepath.Join(string(filepath.Separator), "somewhere", "private", "assets", "archive.cpk")
	got := Display(abs)
	if strings.Contains(got, "somewhere") || strings.Contains(got, "private") {
		t.Fatalf("absolute directory leaked: %q", got)
	}
	if got != "archive.cpk" {
		t.Fatalf("base name expected, got %q", got)
	}
}

func TestErrorStripsPathErrorPath(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "sub", "absent.cpk")
	_, err := os.Stat(missing)
	if err == nil {
		t.Fatal("expected a stat error")
	}
	msg := Error(err)
	if strings.Contains(msg, dir) {
		t.Fatalf("path leaked in error: %q", msg)
	}
	if !strings.Contains(msg, "no such file") {
		t.Fatalf("underlying cause lost: %q", msg)
	}
}

func TestErrorPassesThroughOtherErrors(t *testing.T) {
	err := os.ErrPermission
	if got := Error(err); got != err.Error() {
		t.Fatalf("non-PathError changed: %q", got)
	}
	if got := Error(nil); got != "" {
		t.Fatalf("nil error: %q", got)
	}
}
