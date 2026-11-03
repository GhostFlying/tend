// Command check runs the complete local checks with bounded concurrency.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/GhostFlying/tend/internal/procx"
	"github.com/GhostFlying/tend/internal/testkit/impact"
)

type budget struct {
	jobs, shards, suiteShards, packages, procs int
	exclusiveE2E                               bool
}

func capacity(cpus, jobs, shards, suiteShards int) (budget, error) {
	cpus = max(1, cpus)
	if jobs < 0 || jobs > 7 || shards < 0 || shards > 16 || suiteShards < 0 || suiteShards > 4 {
		return budget{}, fmt.Errorf("jobs must be 0-7, App shards 0-16 and suite shards 0-4 (0 selects automatically)")
	}
	if jobs == 0 {
		switch {
		case cpus >= 32:
			jobs = 7
		case cpus >= 16:
			jobs = 4
		case cpus >= 8:
			jobs = 2
		default:
			jobs = 1
		}
	}
	if shards == 0 {
		shards = max(1, min(8, cpus/8, 2*jobs))
		if jobs == 1 {
			shards = 1
		}
	}
	if suiteShards == 0 {
		suiteShards = max(1, min(4, cpus/16, jobs))
		if jobs == 1 {
			suiteShards = 1
		}
	}
	packages := max(1, min(6, cpus/10))
	// Budget the largest simultaneously active test-process sets, rather than
	// treating a four-shard group as one Go process. Static checks and Web each
	// reserve one slot; their own build/worker parallelism remains bounded.
	slots := []int{shards, suiteShards, suiteShards, suiteShards, packages, 1, 1}
	slices.Sort(slots)
	processes := 0
	for _, n := range slots[len(slots)-jobs:] {
		processes += n
	}
	return budget{
		jobs: jobs, shards: shards, suiteShards: suiteShards,
		packages:     packages,
		procs:        max(1, min(4, cpus/processes)),
		exclusiveE2E: cpus < 32 || jobs == 1,
	}, nil
}

type task struct {
	name string
	args []string
	env  []string
}

func environment(source []string, values map[string]string) []string {
	out := make([]string, 0, len(source)+len(values))
	for _, entry := range source {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := values[key]; !replaced {
			out = append(out, entry)
		}
	}
	for key, value := range values {
		out = append(out, key+"="+value)
	}
	return out
}

func tasks(b budget, directory, goTool, pnpm string, count int) (parallel, isolated []task) {
	base := environment(os.Environ(), map[string]string{
		"MAKEFLAGS": "", "MFLAGS": "", "MAKEOVERRIDES": "",
		"GO": goTool, "PNPM": pnpm,
		"GOFLAGS":         strings.TrimSpace(os.Getenv("GOFLAGS") + " -p=" + strconv.Itoa(b.packages)),
		"GOMAXPROCS":      strconv.Itoa(b.procs),
		"APP_TEST_SHARDS": strconv.Itoa(b.shards),
		"TEST_SHARDS":     strconv.Itoa(b.suiteShards),
		"TEST_COUNT":      strconv.Itoa(count),
		"TEST_PLAN":       "",
	})
	parallel = append(parallel,
		task{"quality", []string{"go-check"}, base},
		task{"web", []string{"web-check"}, environment(base, map[string]string{
			"TEND_WEB_BUILD_OUT_DIR": filepath.Join(directory, "web-app"),
			"VITEST_MAX_WORKERS":     strconv.Itoa(b.procs),
		})},
	)
	for _, group := range []string{"app", "api", "mainthread", "other", "e2e"} {
		t := task{group, []string{"test", "TEST_GROUP=" + group}, base}
		if group == "e2e" && b.exclusiveE2E {
			isolated = append(isolated, t)
		} else {
			parallel = append(parallel, t)
		}
	}
	return parallel, isolated
}

func runTask(ctx context.Context, t task) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	start := time.Now()
	fmt.Printf("[check] %s started\n", t.name)
	cmd := exec.CommandContext(ctx, "make", t.args...)
	cmd.Env, cmd.Stdout, cmd.Stderr = t.env, os.Stdout, os.Stderr
	procx.Contain(cmd)
	err := cmd.Run()
	procx.KillGroup(cmd)
	fmt.Printf("[check] %s finished in %s: %v\n", t.name, time.Since(start).Round(time.Second), err)
	return err
}

func schedule(ctx context.Context, jobs int, parallel, isolated []task, run func(context.Context, task) error) error {
	queue := make(chan task, len(parallel))
	results := make(chan error, len(parallel)+len(isolated))
	for _, t := range parallel {
		queue <- t
	}
	close(queue)
	var wg sync.WaitGroup
	for range min(jobs, len(parallel)) {
		wg.Go(func() {
			for t := range queue {
				if err := run(ctx, t); err != nil {
					results <- fmt.Errorf("%s: %w", t.name, err)
				}
			}
		})
	}
	wg.Wait()
	for _, t := range isolated {
		if err := run(ctx, t); err != nil {
			results <- fmt.Errorf("%s: %w", t.name, err)
		}
	}
	close(results)
	var failures []error
	for err := range results {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

func check(ctx context.Context, b budget, goTool, pnpm string, count int) (map[string]float64, error) {
	stages := map[string]float64{}
	directory, err := os.MkdirTemp("", "tend-check-")
	if err != nil {
		return stages, err
	}
	defer os.RemoveAll(directory)
	parallel, isolated := tasks(b, directory, goTool, pnpm, count)
	fmt.Printf("[check] jobs=%d app-shards=%d suite-shards=%d Go-package-parallel=%d GOMAXPROCS=%d isolated-e2e=%t\n",
		b.jobs, b.shards, b.suiteShards, b.packages, b.procs, b.exclusiveE2E)
	var mu sync.Mutex
	err = schedule(ctx, b.jobs, parallel, isolated, func(ctx context.Context, t task) error {
		start := time.Now()
		err := runTask(ctx, t)
		mu.Lock()
		stages[t.name] = time.Since(start).Seconds()
		mu.Unlock()
		return err
	})
	return stages, err
}

func complete(ctx context.Context, dir string, b budget, goTool, pnpm string, count int) (impact.Result, error) {
	start := time.Now()
	r := impact.Result{Mode: "full", Stages: map[string]float64{}}
	s, err := impact.Capture(ctx, dir, goTool)
	if err == nil {
		r.Commit, r.Fingerprint = s.Commit, s.Fingerprint
		r.Profile = s.Profile
		r.Stages, err = check(ctx, b, goTool, pnpm, count)
		identityErr := impact.SameSnapshot(ctx, dir, goTool, s)
		r.Valid = identityErr == nil
		err = errors.Join(err, identityErr)
	}
	r.Seconds, r.Passed = time.Since(start).Seconds(), err == nil
	r.FullVerified = r.Passed && r.Valid
	if err != nil {
		r.Error = err.Error()
	}
	return r, err
}

func main() {
	jobs := flag.Int("jobs", 0, "concurrent check groups (0 selects by CPU, 1 selects sequential checks)")
	shards := flag.Int("shards", 0, "App test shards (0 selects by CPU)")
	suiteShards := flag.Int("suite-shards", 0, "API, mainthread and E2E shards (0 selects by CPU)")
	goTool := flag.String("go", "go", "Go executable")
	pnpm := flag.String("pnpm", "pnpm", "pnpm executable")
	count := flag.Int("count", 0, "repeat race tests explicitly; 0 keeps Go native caching")
	mode := flag.String("mode", "shadow", "full, shadow or affected")
	indexPath := flag.String("index", ".local/impact/index.json", "trusted coverage index")
	base := flag.String("base", "", "optional comparison base; index-to-candidate changes always included")
	report := flag.String("report", ".local/impact/result.json", "result report")
	planOnly := flag.Bool("plan-only", false, "write plan without executing checks")
	force := flag.String("force-full", "", "reason to force complete validation")
	directory := flag.String("dir", ".", "repository directory")
	raceRunner := flag.String("race-runner", "", "optional compiled racecheck for historical replay")
	flag.Parse()
	b, err := capacity(runtime.GOMAXPROCS(0), *jobs, *shards, *suiteShards)
	if err == nil && *count < 0 {
		err = fmt.Errorf("count must be nonnegative")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *mode != "full" && *mode != "shadow" && *mode != "affected" {
		fmt.Fprintln(os.Stderr, "mode must be full, shadow or affected")
		os.Exit(2)
	}
	dir, err := filepath.Abs(*directory)
	if err == nil {
		err = os.Chdir(dir)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if !*planOnly {
		pending := impact.Result{Mode: *mode, Error: "validation in progress"}
		if commit, gitErr := exec.CommandContext(ctx, "git", "rev-parse", "HEAD").Output(); gitErr == nil {
			pending.Commit = strings.TrimSpace(string(commit))
		}
		if err := impact.WriteJSON(*report, pending); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	resolvedFlags, resolveErr := impact.ResolvedGoFlags(ctx, dir, *goTool)
	if resolveErr == nil {
		resolveErr = os.Setenv("GOFLAGS", resolvedFlags)
	}
	if resolveErr != nil {
		if !*planOnly {
			_ = impact.WriteJSON(*report, impact.Result{Mode: *mode, Error: resolveErr.Error()})
		}
		fmt.Fprintln(os.Stderr, resolveErr)
		os.Exit(1)
	}
	if *mode == "full" && !*planOnly {
		var result impact.Result
		result, err = complete(ctx, dir, b, *goTool, *pnpm, *count)
		err = errors.Join(err, impact.WriteJSON(*report, result))
	} else {
		var plan impact.Plan
		analysisStart := time.Now()
		plan, err = impact.PlanChecks(ctx, dir, *goTool, *indexPath, *base, *force)
		analysisSeconds := time.Since(analysisStart).Seconds()
		planPath := filepath.Join(filepath.Dir(*report), "plan.json")
		if err == nil {
			err = impact.WriteJSON(planPath, plan)
		}
		if err == nil {
			total := 0
			for _, roots := range plan.Go {
				total += len(roots)
			}
			fmt.Printf("[impact] mode=%s full=%t packages=%d roots=%d web=%t\n", *mode, plan.Full, len(plan.Go), total, plan.Web.Run)
			for _, reason := range plan.Reasons {
				fmt.Printf("[impact] %s\n", reason)
			}
		}
		if err == nil && !*planOnly {
			var full impact.Result
			if *mode == "shadow" || plan.Full {
				full, err = complete(ctx, dir, b, *goTool, *pnpm, *count)
				if full.Fingerprint != plan.Fingerprint {
					full.Valid, full.Passed, full.FullVerified = false, false, false
					full.Error = "source changed between planning and full validation"
					err = errors.Join(err, errors.New(full.Error))
				}
				full.Mode = *mode
				full.Stages["analysis"] = analysisSeconds
				full.Seconds += analysisSeconds
				full.SelectionDelegated = plan.Full
				err = errors.Join(err, impact.WriteJSON(*report, full))
			}
			absolute, pathErr := filepath.Abs(planPath)
			if err != nil {
				// The full failure remains the gate and its result is retained.
			} else if pathErr != nil {
				err = pathErr
			} else if !plan.Full {
				var result impact.Result
				result, err = impact.Run(ctx, impact.RunOptions{Dir: dir, Go: *goTool, PNPM: *pnpm, PlanPath: absolute, RaceRunner: *raceRunner, Jobs: b.jobs, Procs: b.procs, Packages: b.packages, AppShards: b.shards, SuiteShards: b.suiteShards, Count: *count, IsolateE2E: b.exclusiveE2E, Log: os.Stdout}, plan)
				result.Mode = *mode
				result.Stages["analysis"] = analysisSeconds
				result.Seconds += analysisSeconds
				if *mode == "shadow" {
					result.FullVerified = full.FullVerified
					result.Stages["full"] = full.Seconds - analysisSeconds
					result.Seconds += full.Seconds - analysisSeconds
				}
				if writeErr := impact.WriteJSON(*report, result); writeErr != nil {
					err = errors.Join(err, writeErr)
				}
			}
		}
	}
	if err != nil {
		if !*planOnly {
			var result impact.Result
			if body, readErr := os.ReadFile(*report); readErr == nil && json.Unmarshal(body, &result) == nil {
				result.Passed = false
				result.Error = err.Error()
				if writeErr := impact.WriteJSON(*report, result); writeErr != nil {
					fmt.Fprintln(os.Stderr, writeErr)
				}
			}
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
