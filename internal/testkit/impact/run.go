package impact

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

type RunOptions struct {
	Dir, Go, PNPM, PlanPath string
	// A compiled racecheck binary also supports replaying historical snapshots
	// that predate the impact-aware Makefile and racecheck implementation.
	RaceRunner                                    string
	Jobs, Procs, Packages, AppShards, SuiteShards int
	Count                                         int
	IsolateE2E                                    bool
	Log                                           io.Writer
}

func group(pkg string) string {
	for _, name := range []string{"app", "api", "mainthread", "e2e"} {
		if strings.HasSuffix(pkg, "/internal/"+name) {
			return name
		}
	}
	return "other"
}

func (o RunOptions) env(flags string) []string {
	values := map[string]string{"GOMAXPROCS": strconv.Itoa(max(1, o.Procs)), "GOFLAGS": strings.TrimSpace(flags + " -p=" + strconv.Itoa(max(1, o.Packages))), "MAKEFLAGS": "", "MFLAGS": "", "MAKEOVERRIDES": "", "VITEST_MAX_WORKERS": strconv.Itoa(max(1, o.Procs))}
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, ok := values[key]; !ok {
			env = append(env, entry)
		}
	}
	for key, value := range values {
		env = append(env, key+"="+value)
	}
	return env
}

// Run executes the selected workload under the existing scheduler's budget.
// Fast global quality checks are deliberately retained for every plan.
func Run(ctx context.Context, o RunOptions, p Plan) (Result, error) {
	start := time.Now()
	r := Result{Mode: "affected", Commit: p.Commit, Fingerprint: p.Fingerprint, Stages: map[string]float64{}, Profile: p.Profile}
	if o.Go == "" {
		o.Go = "go"
	}
	if o.PNPM == "" {
		o.PNPM = "pnpm"
	}
	if o.Log == nil {
		o.Log = io.Discard
	}
	if o.Jobs < 1 {
		o.Jobs = 1
	}
	if err := SamePlan(ctx, o.Dir, o.Go, p); err != nil {
		return r, err
	}
	// Do not trust the planned names as a regex or silently match zero tests.
	saved, err := ReadPlan(o.PlanPath)
	if err != nil {
		return r, err
	}
	if !reflect.DeepEqual(*saved, p) {
		return r, fmt.Errorf("saved execution plan differs from selected workload")
	}
	tmp, err := os.MkdirTemp("", "tend-impact-run-")
	if err != nil {
		return r, err
	}
	defer os.RemoveAll(tmp)
	// Report destinations may be shared by concurrent local invocations. Child
	// schedulers must consume this invocation's immutable private plan instead.
	executionPlan := filepath.Join(tmp, "execution-plan.json")
	if err := WriteJSON(executionPlan, p); err != nil {
		return r, err
	}
	type work struct {
		name string
		run  func() error
	}
	flags, err := ResolvedGoFlags(ctx, o.Dir, o.Go)
	if err != nil {
		return r, err
	}
	env := o.env(flags)
	run := func(program string, args ...string) error { return Command(ctx, o.Dir, o.Log, env, program, args...) }
	queue := []work{{"quality", func() error {
		if err := run("make", "go-check", "GO="+o.Go); err != nil {
			return err
		}
		// Historical checkouts can predate this tool's Makefile changes. Compile
		// every race configuration even when the impact plan has no test roots.
		return run(o.Go, "vet", "-race", "./...")
	}}}
	if p.Web.Run {
		queue = append(queue, work{"web", func() error {
			webEnv := append(slices.Clone(env), "TEND_WEB_BUILD_OUT_DIR="+filepath.Join(tmp, "web-app"))
			call := func(args ...string) error {
				return Command(ctx, o.Dir, o.Log, webEnv, o.PNPM, append([]string{"--dir", "web"}, args...)...)
			}
			if err := call("install", "--frozen-lockfile"); err != nil {
				return err
			}
			for _, stage := range []string{"typecheck", "lint", "format:check", "style:check"} {
				if err := call(stage); err != nil {
					return err
				}
			}
			runner := filepath.Join(tmp, "web.cjs")
			plan := filepath.Join(tmp, "web-plan.json")
			report := filepath.Join(tmp, "web-result.json")
			if err := os.WriteFile(runner, webRunner, 0o600); err != nil {
				return err
			}
			if err := WriteJSON(plan, p.Web); err != nil {
				return err
			}
			webErr := Command(ctx, o.Dir, o.Log, webEnv, "node", runner, filepath.Join(o.Dir, "web"), o.PNPM, plan, report)
			var execution WebExecution
			if err := readJSON(report, &execution); err == nil {
				r.Web = &execution
			}
			if webErr != nil {
				return webErr
			}
			return call("build")
		}})
	}
	var isolated []work
	for _, g := range []string{"app", "api", "mainthread", "other", "e2e"} {
		found := false
		for pkg := range p.Go {
			if group(pkg) == g {
				found = true
				break
			}
		}
		if !found {
			continue
		}
		w := work{g, func() error {
			args := []string{"-go", o.Go, "-shards", strconv.Itoa(max(1, o.AppShards)), "-suite-shards", strconv.Itoa(max(1, o.SuiteShards)), "-count", strconv.Itoa(o.Count), "-group", g, "-plan", executionPlan, "-report-dir", os.Getenv("TEST_REPORT_DIR"), "./..."}
			timeout := os.Getenv("TEST_TIMEOUT")
			if timeout == "" {
				timeout = "60m"
			}
			args = append(args[:len(args)-1], "-timeout", timeout, "./...")
			if o.RaceRunner != "" {
				return run(o.RaceRunner, args...)
			}
			return run(o.Go, append([]string{"run", "./internal/testkit/racecheck"}, args...)...)
		}}
		if g == "e2e" && o.IsolateE2E {
			isolated = append(isolated, w)
		} else {
			queue = append(queue, w)
		}
	}
	var mu sync.Mutex
	var failures []error
	execute := func(w work) {
		t := time.Now()
		fmt.Fprintf(o.Log, "[impact] %s started\n", w.name)
		err := w.run()
		elapsed := duration(t)
		mu.Lock()
		r.Stages[w.name] = elapsed
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", w.name, err))
		}
		mu.Unlock()
		fmt.Fprintf(o.Log, "[impact] %s finished in %.1fs: %v\n", w.name, elapsed, err)
	}
	jobs := make(chan work, len(queue))
	for _, w := range queue {
		jobs <- w
	}
	close(jobs)
	var wg sync.WaitGroup
	for range min(o.Jobs, len(queue)) {
		wg.Go(func() {
			for w := range jobs {
				execute(w)
			}
		})
	}
	wg.Wait()
	for _, w := range isolated {
		execute(w)
	}
	r.Seconds = duration(start)
	if err := SamePlan(ctx, o.Dir, o.Go, p); err != nil {
		r.Error = err.Error()
		return r, err
	}
	r.Valid = true
	if len(failures) > 0 {
		r.Error = fmt.Sprint(failures)
		return r, fmt.Errorf("selected checks failed: %v", failures)
	}
	r.Passed = true
	return r, nil
}
