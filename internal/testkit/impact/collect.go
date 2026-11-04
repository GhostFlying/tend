package impact

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GhostFlying/tend/internal/procx"
)

type CollectOptions struct {
	Dir, Go        string
	Jobs, Procs    int
	Timeout        time.Duration
	Log            io.Writer
	FullExecutions []Execution
}

func collectRoot(ctx context.Context, o CollectOptions, s Snapshot, pkg Package, binary, root, directory string) (Execution, error) {
	e := Execution{Package: pkg.ImportPath, Root: root, Mode: "coverage"}
	dir, err := os.MkdirTemp(directory, "root-")
	if err != nil {
		return e, err
	}
	defer os.RemoveAll(dir)
	profile := filepath.Join(dir, "coverage.out")
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	c := exec.CommandContext(ctx, binary, "-test.run=^"+regexp.QuoteMeta(root)+"$", "-test.v", "-test.timeout="+o.Timeout.String(), "-test.coverprofile="+profile)
	c.Dir = filepath.Join(o.Dir, pkg.Dir)
	c.Env = append(os.Environ(), "GOMAXPROCS="+strconv.Itoa(o.Procs), "GOCOVERDIR="+dir)
	procx.Contain(c)
	start := time.Now()
	b, err := c.CombinedOutput()
	procx.KillGroup(c)
	e.Seconds = duration(start)
	if err != nil {
		return e, fmt.Errorf("collect %s/%s: %w\n%s", pkg.ImportPath, root, err, b)
	}
	if !strings.Contains(string(b), "=== RUN   "+root+"\n") || (!strings.Contains(string(b), "--- PASS: "+root+" (") && !strings.Contains(string(b), "--- SKIP: "+root+" (")) {
		return e, fmt.Errorf("root did not execute: %s/%s", pkg.ImportPath, root)
	}
	if strings.Contains(string(b), "--- SKIP: "+root+" (") {
		e.Mode = "static-skipped"
		// A skipped root is retained in the inventory with a conservative
		// dependency on every function in its reachable production packages.
		for id, p := range s.Packages {
			if dependentTests(s, id)[pkg.ImportPath] {
				for _, f := range s.Files {
					if f.Package == p.ImportPath {
						for id := range f.Functions {
							e.Functions = append(e.Functions, id)
						}
					}
				}
			}
		}
		slices.Sort(e.Functions)
		return e, nil
	}
	f, err := os.Open(profile)
	if err != nil {
		return e, fmt.Errorf("missing root coverage: %w", err)
	}
	defer f.Close()
	e.Functions, err = coveredFunctions(f, s)
	return e, err
}

func coveredFunctions(reader io.Reader, source Snapshot) ([]string, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	if !scanner.Scan() || scanner.Text() != "mode: atomic" {
		return nil, fmt.Errorf("coverage must use atomic counters")
	}
	seen := map[string]bool{}
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 3 {
			return nil, fmt.Errorf("invalid coverage record")
		}
		count, err := strconv.ParseUint(fields[2], 10, 64)
		if err != nil {
			return nil, err
		}
		colon := strings.LastIndex(fields[0], ":")
		if colon < 0 {
			return nil, fmt.Errorf("missing coverage position")
		}
		path := strings.TrimPrefix(fields[0][:colon], source.Module+"/")
		var start, col, end, endCol int
		if n, err := fmt.Sscanf(fields[0][colon+1:], "%d.%d,%d.%d", &start, &col, &end, &endCol); err != nil || n != 4 {
			return nil, fmt.Errorf("invalid coverage span")
		}
		if count == 0 {
			continue
		}
		file, ok := source.Files[path]
		if !ok {
			return nil, fmt.Errorf("coverage source outside snapshot: %s", path)
		}
		for id, fn := range file.Functions {
			if start >= fn.Start && start <= fn.End {
				seen[id] = true
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	var result []string
	for id := range seen {
		result = append(result, id)
	}
	slices.Sort(result)
	return result, nil
}

// Collect does not attest that a full suite passed. Baseline's ordinary full
// validation and source identity check are required before publishing its index.
func Collect(ctx context.Context, o CollectOptions) (*Index, error) {
	if o.Go == "" {
		o.Go = "go"
	}
	if o.Jobs < 1 {
		o.Jobs = DefaultJobs()
	}
	if o.Procs < 1 {
		o.Procs = 2
	}
	if o.Timeout == 0 {
		o.Timeout = time.Hour
	}
	if o.Log == nil {
		o.Log = io.Discard
	}
	start := time.Now()
	s, err := Capture(ctx, o.Dir, o.Go)
	if err != nil {
		return nil, err
	}
	if s.Profile.Overlay != "" {
		return nil, fmt.Errorf("function coverage baselines do not support compiler overlays; use complete validation")
	}
	if s.Profile.ModuleConfig != "" {
		return nil, fmt.Errorf("function coverage baselines do not support alternate modules or workspaces; use complete validation")
	}
	index := &Index{Format: Format, Source: s, AnalysisSeconds: duration(start)}
	tmp, err := os.MkdirTemp("", "tend-impact-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	var packages []string
	for id, pkg := range s.Packages {
		if len(pkg.Roots) > 0 {
			packages = append(packages, id)
		}
	}
	slices.Sort(packages)
	for n, id := range packages {
		pkg := s.Packages[id]
		if pkg.ProcessBoundary {
			// These roots already executed in the ordinary full suite. Repeating
			// unknown process trees under instrumentation cannot establish complete
			// attribution, and could leave detached children on cancellation.
			for _, root := range pkg.Roots {
				found := false
				for _, record := range o.FullExecutions {
					if record.Package == id && record.Root == root && (record.Mode == "full-pass" || record.Mode == "full-skip") {
						record.Mode = "static-process"
						record.Functions = nil
						index.Executions = append(index.Executions, record)
						found = true
						break
					}
				}
				if !found {
					return nil, fmt.Errorf("process fallback requires complete ordinary-full evidence: %s/%s", id, root)
				}
			}
			fmt.Fprintf(o.Log, "[impact] static process fallback %d/%d %s: %d verified full-suite roots\n", n+1, len(packages), id, len(pkg.Roots))
			continue
		}
		binary := filepath.Join(tmp, "package.test")
		compileStart := time.Now()
		if err := Command(ctx, o.Dir, o.Log, nil, o.Go, "test", "-c", "-race", "-covermode=atomic", "-coverpkg="+s.Module+"/...", "-o", binary, id); err != nil {
			return nil, fmt.Errorf("compile %s: %w", id, err)
		}
		index.CompileSeconds += duration(compileStart)
		listed, err := output(ctx, filepath.Join(o.Dir, pkg.Dir), binary, "-test.list=.")
		if err != nil {
			return nil, err
		}
		var roots []string
		for line := range strings.Lines(string(listed)) {
			name := strings.TrimSpace(line)
			if isRoot(name) && !strings.ContainsAny(name, " \t") {
				roots = append(roots, name)
			}
		}
		slices.Sort(roots)
		if !slices.Equal(roots, pkg.Roots) {
			return nil, fmt.Errorf("source/runtime root inventory mismatch in %s: source=%v runtime=%v", id, pkg.Roots, roots)
		}
		fmt.Fprintf(o.Log, "[impact] collect %d/%d %s: %d roots\n", n+1, len(packages), id, len(roots))
		queue := make(chan string, len(roots))
		for _, root := range roots {
			queue <- root
		}
		close(queue)
		type result struct {
			execution Execution
			err       error
		}
		results := make(chan result, len(roots))
		var wg sync.WaitGroup
		for range min(o.Jobs, len(roots)) {
			wg.Go(func() {
				for root := range queue {
					e, err := collectRoot(ctx, o, s, pkg, binary, root, tmp)
					results <- result{e, err}
				}
			})
		}
		wg.Wait()
		close(results)
		for r := range results {
			if r.err != nil {
				return nil, r.err
			}
			index.Executions = append(index.Executions, r.execution)
			index.RootSeconds += r.execution.Seconds
		}
		if err := os.Remove(binary); err != nil {
			return nil, err
		}
	}
	if err := SameSnapshot(ctx, o.Dir, o.Go, s); err != nil {
		return nil, err
	}
	slices.SortFunc(index.Executions, func(a, b Execution) int { return strings.Compare(a.Package+"/"+a.Root, b.Package+"/"+b.Root) })
	index.CollectionSeconds = duration(start)
	return index, nil
}
