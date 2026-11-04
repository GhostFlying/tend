// Package impact collects execution dependencies and plans conservative checks.
// It is developer tooling, not part of the server or runner protocol.
package impact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/procx"
)

const Format = 1

var ErrConfigurationChanged = errors.New("impact plan build configuration changed")

type Profile struct {
	Go, OS, Arch, Flags string
	Race                bool
	Overlay             string `json:",omitempty"`
}

type Function struct {
	ID, Name, Digest string
	Start, End       int
	Root             bool
}

type File struct {
	Digest, Header, Package string
	Executable              uint32
	ProcessBoundary         bool
	SourceInspection        bool
	SharedState             bool
	Functions               map[string]Function
}

type Package struct {
	ImportPath, Dir      string
	Imports, TestImports []string
	Roots                []string
	ProcessBoundary      bool
	SourceInspection     bool
	SharedState          bool
}

type Snapshot struct {
	Commit, Fingerprint, Module string
	Clean                       bool
	Profile                     Profile
	Files                       map[string]File
	Packages                    map[string]Package
}

type Execution struct {
	Package, Root string
	Mode          string
	Functions     []string
	Seconds       float64
}

type Index struct {
	Format                                       int
	Source                                       Snapshot
	VerifiedFull                                 bool
	FullSeconds                                  float64
	CollectionSeconds                            float64
	AnalysisSeconds, CompileSeconds, RootSeconds float64
	Executions                                   []Execution
}

type WebPlan struct {
	Run, Full bool
	Base      string
}

type Plan struct {
	Format                int
	Commit, Fingerprint   string
	Baseline              string
	Full                  bool
	Reasons               []string
	Go                    map[string][]string
	Web                   WebPlan
	Layout                []string
	Cross                 bool
	HistoricalRootSeconds float64
	Profile               Profile
}

type Result struct {
	Mode, Commit, Fingerprint        string
	Valid, Passed                    bool
	Seconds                          float64
	Stages                           map[string]float64
	FullVerified, SelectionDelegated bool
	Error                            string        `json:",omitempty"`
	Web                              *WebExecution `json:",omitempty"`
	Profile                          Profile
}

func hash(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func WriteJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".impact-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(append(b, '\n'))
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}

func ReadIndex(path string) (*Index, error) {
	var x Index
	if err := readJSON(path, &x); err != nil {
		return nil, err
	}
	if x.Format != Format || !x.VerifiedFull || !x.Source.Clean || !digest(x.Source.Fingerprint, 64) || !digest(x.Source.Commit, 40) || x.Source.Module == "" || x.Source.Profile.Go == "" || len(x.Source.Files) == 0 || len(x.Source.Packages) == 0 {
		return nil, fmt.Errorf("unverified or unsupported impact index")
	}
	functions := map[string]bool{}
	for path, f := range x.Source.Files {
		if filepath.IsAbs(path) || filepath.ToSlash(filepath.Clean(path)) != path || strings.HasPrefix(path, "../") || !digest(f.Digest, 64) {
			return nil, fmt.Errorf("invalid indexed source: %s", path)
		}
		for id, fn := range f.Functions {
			if id != fn.ID || !digest(fn.Digest, 64) || fn.Start < 1 || fn.End < fn.Start {
				return nil, fmt.Errorf("invalid indexed function: %s", id)
			}
			functions[id] = true
		}
	}
	seen := map[string]bool{}
	for _, e := range x.Executions {
		key := e.Package + "/" + e.Root
		if seen[key] || !slices.Contains(x.Source.Packages[e.Package].Roots, e.Root) {
			return nil, fmt.Errorf("invalid execution inventory: %s", key)
		}
		seen[key] = true
		if e.Mode != "coverage" && e.Mode != "static-skipped" && e.Mode != "static-process" {
			return nil, fmt.Errorf("unknown execution attribution: %s", key)
		}
		if e.Mode == "static-process" && (!x.Source.Packages[e.Package].ProcessBoundary || len(e.Functions) != 0) {
			return nil, fmt.Errorf("invalid process fallback: %s", key)
		}
		for _, fn := range e.Functions {
			if !functions[fn] {
				return nil, fmt.Errorf("unknown covered function: %s", fn)
			}
		}
	}
	for pkg, p := range x.Source.Packages {
		if p.ImportPath != pkg || !strings.HasPrefix(pkg, x.Source.Module) {
			return nil, fmt.Errorf("invalid indexed package: %s", pkg)
		}
		rootSeen := map[string]bool{}
		for _, root := range p.Roots {
			if !isRoot(root) || rootSeen[root] || !seen[pkg+"/"+root] {
				return nil, fmt.Errorf("missing coverage for %s/%s", pkg, root)
			}
			rootSeen[root] = true
		}
	}
	return &x, nil
}

func digest(s string, size int) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(s) == size && len(b)*2 == size
}

func ReadPlan(path string) (*Plan, error) {
	var p Plan
	if err := readJSON(path, &p); err != nil {
		return nil, err
	}
	if p.Format != Format || p.Fingerprint == "" || p.Go == nil || p.Profile.Go == "" {
		return nil, fmt.Errorf("invalid impact plan")
	}
	for pkg, roots := range p.Go {
		if pkg == "" || len(roots) == 0 {
			return nil, fmt.Errorf("empty planned test selection")
		}
		seen := map[string]bool{}
		for _, r := range roots {
			if !isRoot(r) || seen[r] {
				return nil, fmt.Errorf("invalid planned root %q", r)
			}
			seen[r] = true
		}
	}
	return &p, nil
}

func readJSON(path string, out any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 128<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return fmt.Errorf("trailing impact data")
	}
	return nil
}

func output(ctx context.Context, dir, program string, args ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, program, args...)
	c.Dir = dir
	procx.Contain(c)
	b, err := c.Output()
	procx.KillGroup(c)
	if err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("%s failed: %w: %s", program, err, e.Stderr)
		}
		return nil, err
	}
	return b, nil
}

func Command(ctx context.Context, dir string, writer io.Writer, env []string, program string, args ...string) error {
	return command(ctx, dir, writer, env, false, program, args...)
}

// CommandGraceful lets our context-aware orchestrators cancel the process
// groups they own before their parent applies the bounded hard-stop fallback.
func CommandGraceful(ctx context.Context, dir string, writer io.Writer, env []string, program string, args ...string) error {
	return command(ctx, dir, writer, env, true, program, args...)
}

func graceful(c *exec.Cmd) {
	procx.Contain(c)
	hard := c.Cancel
	c.Cancel = func() error {
		if err := c.Process.Signal(os.Interrupt); err != nil {
			return hard()
		}
		return nil
	}
}

func command(ctx context.Context, dir string, writer io.Writer, env []string, soft bool, program string, args ...string) error {
	c := exec.CommandContext(ctx, program, args...)
	c.Dir, c.Stdout, c.Stderr = dir, writer, writer
	if env != nil {
		c.Env = env
	}
	procx.Contain(c)
	if soft {
		graceful(c)
	}
	err := c.Run()
	procx.KillGroup(c)
	return err
}

func BuildProfile(ctx context.Context, dir, goTool string) (Profile, error) {
	p, _, err := buildProfile(ctx, dir, goTool)
	return p, err
}

func buildProfile(ctx context.Context, dir, goTool string) (Profile, compilerOverlay, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return Profile{}, compilerOverlay{}, err
	}
	b, err := output(ctx, dir, goTool, "env", "GOVERSION", "GOOS", "GOARCH", "GOFLAGS", "CGO_ENABLED", "GOEXPERIMENT", "GOAMD64", "GOARM64", "CC", "CXX", "CGO_CFLAGS", "CGO_CPPFLAGS", "CGO_CXXFLAGS", "CGO_LDFLAGS")
	if err != nil {
		return Profile{}, compilerOverlay{}, err
	}
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if len(lines) != 14 {
		return Profile{}, compilerOverlay{}, fmt.Errorf("incomplete Go build configuration")
	}
	overlay, err := loadCompilerOverlay(dir, lines[3])
	if err != nil {
		return Profile{}, compilerOverlay{}, err
	}
	lines[3] = semanticGoFlags(lines[3])
	if overlay.digest != "" {
		lines = append(lines, "compiler-overlay:"+overlay.digest)
	}
	return Profile{Go: lines[0], OS: lines[1], Arch: lines[2], Flags: hash([]byte(strings.Join(lines[3:], "\n"))), Race: true, Overlay: overlay.digest}, overlay, nil
}

func ResolvedGoFlags(ctx context.Context, dir, goTool string) (string, error) {
	b, err := output(ctx, dir, goTool, "env", "GOFLAGS")
	return strings.TrimSuffix(string(b), "\n"), err
}

func DefaultJobs() int { return max(1, min(16, runtime.GOMAXPROCS(0)/2)) }

// GOFLAGS quoting is preserved verbatim. Only standalone parallel-build flags
// injected by the scheduler are omitted from the semantic build identity.
func semanticGoFlags(flags string) string {
	var tokens []string
	for i := 0; i < len(flags); {
		for i < len(flags) && strings.ContainsRune(" \t\r\n", rune(flags[i])) {
			i++
		}
		start := i
		var quote byte
		for i < len(flags) {
			c := flags[i]
			if quote != 0 {
				if c == quote {
					quote = 0
				}
			} else if c == '\'' || c == '"' {
				quote = c
			} else if strings.ContainsRune(" \t\r\n", rune(c)) {
				break
			}
			i++
		}
		token := flags[start:i]
		if token == "" {
			continue
		}
		if value, ok := strings.CutPrefix(token, "-p="); ok {
			if n, err := strconv.Atoi(value); err == nil && n >= 0 {
				continue
			}
		}
		tokens = append(tokens, token)
	}
	return strings.Join(tokens, " ")
}

// CheckBudgetArgs preserves the existing Makefile controls when an immutable
// replay invokes the compiled checker rather than its historical Makefile.
func CheckBudgetArgs() []string {
	var args []string
	for _, entry := range []struct{ env, flag string }{{"CHECK_JOBS", "-jobs"}, {"APP_TEST_SHARDS", "-shards"}, {"TEST_SHARDS", "-suite-shards"}, {"PNPM", "-pnpm"}} {
		if value := os.Getenv(entry.env); value != "" {
			args = append(args, entry.flag, value)
		}
	}
	return args
}

func PNPMTool() string {
	if tool := os.Getenv("PNPM"); tool != "" {
		return tool
	}
	return "pnpm"
}

func duration(start time.Time) float64 { return time.Since(start).Seconds() }
