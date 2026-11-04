package extract

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"uo-unburger/internal/sourcecache"
)

// fakeTool writes an executable shell script and returns its path. When run for a
// usage probe (no UO_EXTRACT_OUT in the environment) it prints a version and an
// xci mention; during an extraction it runs script.
func fakeTool(t *testing.T, dir, name, script string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	body := "#!/bin/sh\n" +
		"if [ -z \"$UO_EXTRACT_OUT\" ]; then echo 'NSTool v1.9.2 (C) jakcron'; echo 'supports xci gamecards'; exit 0; fi\n" +
		script + "\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func nspContainer(t *testing.T, dir string) sourcecache.Container {
	t.Helper()
	path := filepath.Join(dir, "Update.nsp")
	if err := os.WriteFile(path, []byte("PFS0\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	return sourcecache.Container{
		Path: path, Format: sourcecache.FormatNSP, Role: sourcecache.RoleUpdate,
		TitleID: "010069401ADB8800", RawVersion: 262144, MetaType: "Patch", Parsed: true,
	}
}

func onlyTool(path string) DetectOptions {
	return DetectOptions{LookPath: func(name string) (string, error) {
		if name == "nstool" {
			return path, nil
		}
		return "", exec.ErrNotFound
	}}
}

func TestDetectFindsToolAndParsesVersionAndXCI(t *testing.T) {
	dir := t.TempDir()
	path := fakeTool(t, dir, "nstool", `echo "NSTool v1.9.2 (C) jakcron"; echo "supports xci gamecards"`)
	tool, err := Detect(context.Background(), onlyTool(path))
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if tool.Name != "nstool" || tool.Version != "v1.9.2" {
		t.Fatalf("tool = %+v", tool)
	}
	if !tool.SupportsXCI {
		t.Fatalf("xci support must be detected from the usage text: %+v", tool)
	}
}

func TestDetectReportsNoExtractor(t *testing.T) {
	_, err := Detect(context.Background(), DetectOptions{LookPath: func(string) (string, error) {
		return "", exec.ErrNotFound
	}})
	if !errors.Is(err, ErrNoExtractor) {
		t.Fatalf("err = %v, want ErrNoExtractor", err)
	}
}

func TestCheckKeysNamesOnly(t *testing.T) {
	dir := t.TempDir()
	if got := CheckKeys(dir); got.ProdKeys {
		t.Fatal("no prod.keys should be reported")
	}
	if err := os.WriteFile(filepath.Join(dir, "prod.keys"), []byte("secret contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := CheckKeys(dir)
	if !got.ProdKeys || got.TitleKeys {
		t.Fatalf("keys = %+v", got)
	}
}

func TestPreflightExplainsMissingKeys(t *testing.T) {
	dir := t.TempDir()
	req := Request{
		Container: nspContainer(t, dir),
		Tool:      &Tool{Name: "nstool", Path: "/usr/local/bin/nstool"},
		Keys:      Keys{Dir: filepath.Join(dir, "keys")},
	}
	err := Preflight(req)
	if err == nil {
		t.Fatal("a missing prod.keys must be refused")
	}
	var rem Remedy
	if !errors.As(err, &rem) {
		t.Fatalf("err = %v, want a Remedy", err)
	}
	if !strings.Contains(rem.Detail, "prod.keys") {
		t.Fatalf("the remedy must name prod.keys: %q", rem.Detail)
	}
}

func TestPreflightExplainsXCIWithoutSupport(t *testing.T) {
	dir := t.TempDir()
	req := Request{
		Container: sourcecache.Container{Path: filepath.Join(dir, "c.xci"), Format: sourcecache.FormatXCI, TitleID: "010069401ADB8000", Parsed: false},
		Tool:      &Tool{Name: "hactool", Path: "/usr/bin/hactool", SupportsXCI: false},
		Keys:      Keys{Dir: dir, ProdKeys: true},
	}
	err := Preflight(req)
	if !errors.Is(err, ErrXCIUnsupported) {
		t.Fatalf("err = %v, want ErrXCIUnsupported", err)
	}
}

func TestCommandRecipe(t *testing.T) {
	req := Request{
		Container: sourcecache.Container{Path: "/roms/UPD.nsp"},
		Keys:      Keys{Dir: "/keys"},
		WantMain:  true,
	}
	args, err := Command(&Tool{Name: "nstool"}, req, "/scratch")
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"-k /keys/prod.keys", "--romfsdir /scratch/romfs", "--exefsdir /scratch/exefs", "/roms/UPD.nsp"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("command %q missing %q", joined, want)
		}
	}
}

func TestCommandRejectsUnsupportedExtractor(t *testing.T) {
	req := Request{Container: sourcecache.Container{Path: "/roms/UPD.nsp"}}
	if args, err := Command(&Tool{Name: "mystery-tool"}, req, "/scratch"); err == nil || args != nil {
		t.Fatalf("unsupported extractor must be refused, got args=%v err=%v", args, err)
	}
}

func TestRunHarvestsResourcesAndRemovesNothingElse(t *testing.T) {
	dir := t.TempDir()
	keys := filepath.Join(dir, "keys")
	if err := os.MkdirAll(keys, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(keys, "prod.keys"), []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := fakeTool(t, dir, "nstool", `
mkdir -p "$UO_EXTRACT_OUT/romfs" "$UO_EXTRACT_OUT/exefs"
printf 'CPKDATA' > "$UO_EXTRACT_OUT/romfs/Unicorn_US.CPK"
printf 'NSO0' > "$UO_EXTRACT_OUT/exefs/main"
echo "done"`)
	tool, err := Detect(context.Background(), onlyTool(path))
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	scratch := filepath.Join(dir, "scratch")
	res, err := Run(context.Background(), Request{
		Container:  nspContainer(t, dir),
		Tool:       tool,
		Keys:       CheckKeys(keys),
		ScratchDir: scratch,
		WantMain:   true,
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(res.CPKPath); err != nil {
		t.Fatalf("harvested CPK missing: %v", err)
	}
	if _, err := os.Stat(res.MainPath); err != nil {
		t.Fatalf("harvested main missing: %v", err)
	}
	if !strings.Contains(res.ToolOutput, "done") {
		t.Fatalf("tool output not captured: %q", res.ToolOutput)
	}
}

func TestRunMissingOutputIsAnErrorAndLeavesNoScratch(t *testing.T) {
	dir := t.TempDir()
	keys := filepath.Join(dir, "keys")
	if err := os.MkdirAll(keys, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(keys, "prod.keys"), []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := fakeTool(t, dir, "nstool", `echo "nothing produced"`)
	tool, _ := Detect(context.Background(), onlyTool(path))
	scratch := filepath.Join(dir, "scratch")
	_, err := Run(context.Background(), Request{
		Container:  nspContainer(t, dir),
		Tool:       tool,
		Keys:       CheckKeys(keys),
		ScratchDir: scratch,
	}, nil)
	if err == nil {
		t.Fatal("a missing resource must be an error")
	}
	assertNoScratch(t, scratch)
}

func TestRunCancellationLeavesNoScratch(t *testing.T) {
	dir := t.TempDir()
	keys := filepath.Join(dir, "keys")
	if err := os.MkdirAll(keys, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(keys, "prod.keys"), []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	started := filepath.Join(dir, "started")
	path := fakeTool(t, dir, "nstool", `
touch "$UO_EXTRACT_OUT/partial"
: > `+started+`
exec sleep 30`)
	tool, _ := Detect(context.Background(), onlyTool(path))
	scratch := filepath.Join(dir, "scratch")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for i := 0; i < 200; i++ {
			if _, err := os.Stat(started); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
	}()
	_, err := Run(ctx, Request{
		Container:  nspContainer(t, dir),
		Tool:       tool,
		Keys:       CheckKeys(keys),
		ScratchDir: scratch,
	}, nil)
	if err == nil {
		t.Fatal("a cancelled run must return an error")
	}
	assertNoScratch(t, scratch)
}

// assertNoScratch checks that no extract-* directory remains under parent.
func assertNoScratch(t *testing.T, parent string) {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if err != nil {
		return // parent itself may not exist, which is fine
	}
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "extract-") {
			t.Fatalf("scratch directory %q was left behind", e.Name())
		}
	}
}
