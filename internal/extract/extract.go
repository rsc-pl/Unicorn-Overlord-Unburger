// Package extract integrates an external, user-installed Nintendo Switch container
// extractor so the toolkit can obtain the two resources a build consumes — the
// effective localization archive and the executable `main` excerpt — directly
// from the operator's own game containers.
//
// The package never implements decryption and never reads key material: it
// locates the operator's extractor, checks that the operator's key *files* exist
// (by name only) and runs the tool on the operator's own container. Nothing about
// the container is modified or deleted.
//
// The extraction recipe is tool-specific. It has been exercised end-to-end only
// against test doubles; running it against a real nstool/hactool install is a
// separate step that requires runtime validation and is reported as untested.
package extract

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"uo-unburger/internal/sourcecache"
)

// candidateTools are the extractors looked up on PATH, most capable first.
var candidateTools = []string{"nstool", "hactool", "hactoolnet", "nxDumpMerger"}

// ErrNoExtractor reports that no usable extractor was found.
var ErrNoExtractor = errors.New("extract: no external extractor found")

// ErrNoKeys reports that the operator's key files are missing.
var ErrNoKeys = errors.New("extract: required key files are missing")

// ErrXCIUnsupported reports an XCI container the extractor cannot be handed.
var ErrXCIUnsupported = errors.New("extract: game-card (XCI) images are not supported by this path")

// Remedy is a user-facing explanation for a missing prerequisite: what is
// missing and what to do, in neutral language and without pointing at untrusted
// downloads.
type Remedy struct {
	What   string
	Detail string
}

func (r Remedy) Error() string { return r.What + ": " + r.Detail }

// Tool describes a located extractor.
type Tool struct {
	Name        string
	Path        string
	Version     string
	SupportsXCI bool
	// Source is "config" for an explicitly configured path, else "PATH".
	Source string
}

// DetectOptions configures discovery of an extractor.
type DetectOptions struct {
	// ExplicitPath, when set, is used before PATH lookup.
	ExplicitPath string
	// LookPath defaults to exec.LookPath.
	LookPath func(string) (string, error)
	// Probe runs the tool for version/help output; defaults to a bounded exec.
	Probe func(ctx context.Context, path string) (string, error)
}

// Detect locates an extractor and probes its version and XCI support.
func Detect(ctx context.Context, opts DetectOptions) (*Tool, error) {
	lookPath := opts.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	probe := opts.Probe
	if probe == nil {
		probe = probeTool
	}
	type found struct {
		path, source string
	}
	var candidates []found
	if p := strings.TrimSpace(opts.ExplicitPath); p != "" {
		info, err := os.Stat(p)
		if err != nil {
			return nil, Remedy{What: "configured extractor not found", Detail: p}
		}
		if info.IsDir() {
			return nil, Remedy{What: "configured extractor is a directory", Detail: p}
		}
		candidates = append(candidates, found{p, "config"})
	}
	for _, name := range candidateTools {
		if p, err := lookPath(name); err == nil && p != "" {
			candidates = append(candidates, found{p, "PATH"})
		}
	}
	if len(candidates) == 0 {
		return nil, ErrNoExtractor
	}
	first := candidates[0]
	// A probe failure only costs the version/usage text, not tool discovery, so
	// the error is deliberately ignored here.
	help, _ := probe(ctx, first.path)
	t := &Tool{
		Name:        toolName(first.path),
		Path:        first.path,
		Version:     ParseVersion(help),
		SupportsXCI: strings.Contains(strings.ToLower(help), "xci"),
		Source:      first.source,
	}
	return t, nil
}

// toolName derives a clean tool name from a path.
func toolName(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// ParseVersion extracts a version-looking token (v1.2.3 or 1.2.3) from tool
// output, falling back to the first non-empty line.
func ParseVersion(out string) string {
	for _, line := range strings.Split(out, "\n") {
		for _, tok := range strings.Fields(line) {
			tok = strings.Trim(tok, "()[],;")
			trimmed := strings.TrimPrefix(tok, "v")
			if looksLikeVersion(trimmed) {
				return tok
			}
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if s := strings.TrimSpace(line); s != "" {
			return truncate(s, 60)
		}
	}
	return ""
}

func looksLikeVersion(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) < 2 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// probeTool runs the extractor once for usage/version output, bounded in time.
func probeTool(parent context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "-h")
	out, _ := cmd.CombinedOutput()
	return string(out), nil
}

// Keys reports which key files exist. Only file names are inspected; the contents
// are never opened or read.
type Keys struct {
	Dir       string
	ProdKeys  bool
	TitleKeys bool
}

// Required reports whether the mandatory key file is present.
func (k Keys) Required() bool { return k.ProdKeys }

// CheckKeys inspects a keys directory by file name only.
func CheckKeys(dir string) Keys {
	k := Keys{Dir: dir}
	if dir == "" {
		return k
	}
	k.ProdKeys = fileExists(filepath.Join(dir, "prod.keys"))
	k.TitleKeys = fileExists(filepath.Join(dir, "title.keys"))
	return k
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// Request is one extraction job.
type Request struct {
	// CacheRoot is where a committed entry would live (used for messages only).
	CacheRoot string
	// Container is the recognised source container.
	Container sourcecache.Container
	// Tool and Keys are the located prerequisites.
	Tool *Tool
	Keys Keys
	// ScratchDir is the parent for the temporary extraction directory; the caller
	// passes <root>/.local/work/extract so no scratch escapes the project tree.
	ScratchDir string
	// WantMain requests the executable `main` excerpt in addition to the archive.
	WantMain bool
	// Timeout bounds a single tool invocation; zero means no extra bound.
	Timeout time.Duration
}

// Result is a successful extraction: paths to the harvested artifacts inside the
// (still caller-owned) scratch directory.
type Result struct {
	Scratch    string
	CPKPath    string
	MainPath   string
	Command    []string
	ToolOutput string
}

// Runner executes the extractor. It exists so tests can substitute a fake.
type Runner func(ctx context.Context, path string, args, env []string, stdout, stderr io.Writer) error

// ExecRunner is the production runner.
func ExecRunner(ctx context.Context, path string, args, env []string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

// Command builds the extractor argv for a request. It is exported so the recipe is
// visible and testable without running a real tool. An unrecognised extractor is
// an error rather than a bare container path.
func Command(t *Tool, req Request, scratch string) ([]string, error) {
	keys := ""
	if req.Keys.Dir != "" {
		keys = filepath.Join(req.Keys.Dir, "prod.keys")
	}
	switch t.Name {
	case "nstool":
		args := []string{}
		if keys != "" {
			args = append(args, "-k", keys)
		}
		args = append(args, "-x", "--romfsdir", filepath.Join(scratch, "romfs"))
		if req.WantMain {
			args = append(args, "--exefsdir", filepath.Join(scratch, "exefs"))
		}
		return append(args, req.Container.Path), nil
	case "hactool":
		args := []string{"-x", "--romfsdir", filepath.Join(scratch, "romfs")}
		if req.WantMain {
			args = append(args, "--exefsdir", filepath.Join(scratch, "exefs"))
		}
		if keys != "" {
			args = append(args, "-k", keys)
		}
		return append(args, req.Container.Path), nil
	case "hactoolnet":
		args := []string{"--romfsdir", filepath.Join(scratch, "romfs")}
		if req.WantMain {
			args = append(args, "--exefsdir", filepath.Join(scratch, "exefs"))
		}
		if keys != "" {
			args = append(args, "-k", keys)
		}
		return append(args, req.Container.Path), nil
	default:
		return nil, fmt.Errorf("extract: unsupported extractor %q", t.Name)
	}
}

// Preflight explains whether an extraction can run. A nil error means yes.
func Preflight(req Request) error {
	if req.Tool == nil || req.Tool.Path == "" {
		return Remedy{
			What:   "no external extractor found",
			Detail: "install nstool or hactool (from your distribution or the tool's official project) and make it available on PATH, or set its path explicitly",
		}
	}
	if !req.Keys.Required() {
		return Remedy{
			What:   "the required key file prod.keys was not found",
			Detail: fmt.Sprintf("place your own prod.keys in %s (title.keys is optional)", displayDir(req.Keys.Dir)),
		}
	}
	switch req.Container.Format {
	case sourcecache.FormatNSP:
	case sourcecache.FormatXCI:
		if !req.Tool.SupportsXCI {
			return fmt.Errorf("%w: %s does not advertise game-card support in its usage text", ErrXCIUnsupported, req.Tool.Name)
		}
	default:
		return fmt.Errorf("extract: unrecognised container format %q", req.Container.Format)
	}
	return nil
}

func displayDir(dir string) string {
	if strings.TrimSpace(dir) == "" {
		return "the project keys folder (.local/keys)"
	}
	return dir
}

// Run extracts the requested resources into a fresh scratch directory. On any
// failure — including cancellation — the scratch directory is removed, so an
// interrupted run leaves nothing a later run could mistake for a cache entry.
func Run(ctx context.Context, req Request, runner Runner) (*Result, error) {
	if runner == nil {
		runner = ExecRunner
	}
	if err := Preflight(req); err != nil {
		return nil, err
	}
	parent := req.ScratchDir
	if parent == "" {
		parent = os.TempDir()
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, fmt.Errorf("extract: preparing scratch parent: %w", err)
	}
	scratch, err := os.MkdirTemp(parent, "extract-*")
	if err != nil {
		return nil, fmt.Errorf("extract: creating scratch directory: %w", err)
	}
	success := false
	defer func() {
		if !success {
			os.RemoveAll(scratch)
		}
	}()

	args, err := Command(req.Tool, req, scratch)
	if err != nil {
		return nil, err
	}
	env := []string{
		"UO_EXTRACT_OUT=" + scratch,
		"UO_EXTRACT_ROLE=" + string(req.Container.Role),
		"UO_EXTRACT_TITLEID=" + req.Container.TitleID,
		fmt.Sprintf("UO_EXTRACT_RAW_VERSION=%d", req.Container.RawVersion),
	}
	if req.Keys.Dir != "" {
		env = append(env, "UO_EXTRACT_KEYS="+req.Keys.Dir)
	}

	runCtx := ctx
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}
	var log strings.Builder
	runErr := runner(runCtx, req.Tool.Path, args, env, &log, &log)
	if runErr != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("extract: %s failed: %w", req.Tool.Name, runErr)
	}
	// A cancelled context after a "successful" exit is still a cancellation.
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	cpkPath, err := findResource(scratch, sourcecache.CPKMemberName)
	if err != nil {
		return nil, err
	}
	res := &Result{Scratch: scratch, CPKPath: cpkPath, Command: append([]string{req.Tool.Path}, args...), ToolOutput: log.String()}
	if req.WantMain {
		mainPath, err := findResource(scratch, sourcecache.MainMemberName)
		if err != nil {
			return nil, err
		}
		res.MainPath = mainPath
	}
	success = true
	return res, nil
}

// findResource locates a harvested file in the scratch tree, preferring the
// shortest path (an update-effective copy tends to sit closer to the root).
func findResource(root, name string) (string, error) {
	var found []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if d.Name() == name {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(found) == 0 {
		return "", Remedy{
			What:   fmt.Sprintf("the extractor did not produce %s", name),
			Detail: "check the extractor output; extraction of the required resource failed",
		}
	}
	sort.Slice(found, func(i, j int) bool { return len(found[i]) < len(found[j]) })
	return found[0], nil
}
