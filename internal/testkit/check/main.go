// Command check runs the complete local checks with bounded concurrency.
package main

import (
	"context"
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
	"github.com/GhostFlying/tend/internal/testkit/testbudget"
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
		shards = max(1, min(8, cpus/8, 2*jobs), min(2, cpus/(2*jobs)))
		if jobs == 1 {
			shards = 1
		}
	}
	if suiteShards == 0 {
		suiteShards = max(1, min(4, cpus/16, jobs), min(2, cpus/(2*jobs)))
		if jobs == 1 {
			suiteShards = 1
		}
	}
	packages := testbudget.Packages(cpus, jobs)
	// Budget the largest simultaneously active test-process sets, rather than
	// treating a four-shard group as one Go process. Static checks and Web each
	// reserve one slot; their own build/worker parallelism remains bounded.
	slots := []int{shards, suiteShards, suiteShards, suiteShards, packages, 1, 1, 1, 1, 1}
	slices.Sort(slots)
	processes := 0
	for _, n := range slots[len(slots)-jobs:] {
		processes += n
	}
	return budget{
		jobs: jobs, shards: shards, suiteShards: suiteShards,
		packages:     packages,
		procs:        testbudget.Procs(cpus, processes),
		exclusiveE2E: cpus < 32 || jobs == 1,
	}, nil
}

// serialReason makes an intentional multicore serial override reviewable.
func serialReason(cpus, requestedJobs int, b budget, reason string) (string, error) {
	reason = strings.TrimSpace(reason)
	if b.jobs != 1 {
		return "", nil
	}
	if reason != "" {
		return reason, nil
	}
	if cpus <= 1 {
		return "single effective CPU", nil
	}
	if requestedJobs == 0 {
		return "automatic low CPU budget", nil
	}
	return "", fmt.Errorf("explicit jobs=1 on %d effective CPUs requires CHECK_SERIAL_REASON or -serial-reason; omit CHECK_JOBS to select a bounded parallel budget", cpus)
}

type task struct {
	name string
	args []string
	env  []string
}

func environment(source []string, values map[string]string) []string {
	return testbudget.Environment(source, values)
}

func tasks(b budget, cpus int, directory, goTool, pnpm string, count int) (parallel, isolated []task) {
	base := environment(os.Environ(), map[string]string{
		"MAKEFLAGS": "", "MFLAGS": "", "MAKEOVERRIDES": "",
		"GO": goTool, "PNPM": pnpm,
		"GOFLAGS":         strings.TrimSpace(os.Getenv("GOFLAGS") + " -p=" + strconv.Itoa(b.packages)),
		"GOMAXPROCS":      strconv.Itoa(b.procs),
		"APP_TEST_SHARDS": strconv.Itoa(b.shards),
		"TEST_SHARDS":     strconv.Itoa(b.suiteShards),
		"TEST_COUNT":      strconv.Itoa(count),
	})
	parallel = append(parallel,
		task{"quality", []string{"go-check"}, base},
		task{"web", []string{"web-check"}, environment(base, map[string]string{
			"TEND_WEB_BUILD_OUT_DIR": filepath.Join(directory, "web-app"),
			"VITEST_MAX_WORKERS":     strconv.Itoa(b.procs),
		})},
	)
	for _, group := range impact.Groups() {
		t := task{group, []string{"test", "TEST_GROUP=" + group}, base}
		if group == "e2e" && b.exclusiveE2E {
			// This phase starts only after all parallel groups have joined.
			t.env = environment(base, map[string]string{"GOMAXPROCS": strconv.Itoa(testbudget.Procs(cpus, b.suiteShards))})
			isolated = append(isolated, t)
		} else {
			parallel = append(parallel, t)
		}
	}
	return parallel, isolated
}

// plannedTasks shares the CI package plan while retaining original group budgets.
func plannedTasks(parallel, isolated []task, plan impact.Plan, path string) ([]task, []task) {
	selectTasks := func(source []task) []task {
		var selected []task
		for _, t := range source {
			include := t.name == "quality" || t.name == "web" && plan.Web || slices.Contains(plan.Groups, t.name)
			if !include {
				fmt.Printf("[check] %s excluded by impact plan\n", t.name)
				continue
			}
			if t.name == "quality" {
				t.args = []string{"impact-quality"}
			}
			t.env = environment(t.env, map[string]string{"TEST_PLAN": path})
			selected = append(selected, t)
		}
		return selected
	}
	return selectTasks(parallel), selectTasks(isolated)
}

func runTask(ctx context.Context, t task) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	start := time.Now()
	fmt.Printf("[check] %s started at=%s GOMAXPROCS=%s GOFLAGS=%q suite-shards=%s\n", t.name, start.UTC().Format(time.RFC3339Nano), testbudget.Value(t.env, "GOMAXPROCS"), testbudget.Value(t.env, "GOFLAGS"), testbudget.Value(t.env, "TEST_SHARDS"))
	cmd := exec.CommandContext(ctx, "make", t.args...)
	cmd.Env, cmd.Stdout, cmd.Stderr = t.env, os.Stdout, os.Stderr
	procx.Contain(cmd)
	err := cmd.Run()
	procx.KillGroup(cmd)
	finished := time.Now()
	elapsed := finished.Sub(start)
	fmt.Printf("[check] %s finished in %s: %v at=%s elapsed=%s\n", t.name, elapsed.Round(time.Second), err, finished.UTC().Format(time.RFC3339Nano), elapsed)
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

func packageSummary(plan impact.Plan) string {
	if plan.Full {
		return fmt.Sprintf("package-patterns=%v graph-count=not-expanded", plan.Packages)
	}
	return fmt.Sprintf("selected-packages=%d", len(plan.Packages))
}

func check(ctx context.Context, b budget, cpus int, reason, goTool, pnpm string, count int, mode, base string) error {
	directory, err := os.MkdirTemp("", "tend-check-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	parallel, isolated := tasks(b, cpus, directory, goTool, pnpm, count)
	if mode != "full" && mode != "affected" {
		return fmt.Errorf("check mode must be affected or full")
	}
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	plan, err := impact.Load(ctx, root, impact.Options{Base: base, Head: "HEAD", Go: goTool, Worktree: true, Full: mode == "full"})
	if err != nil {
		return err
	}
	path := filepath.Join(directory, "impact.json")
	if err := impact.Write(path, plan); err != nil {
		return err
	}
	fmt.Printf("[check] impact full=%t base=%s head=%s %s groups=%v reasons=%v\n", plan.Full, plan.Source.Base, plan.Source.Head, packageSummary(plan), plan.Groups, plan.Reasons)
	parallel, isolated = plannedTasks(parallel, isolated, plan, path)

	fmt.Printf("[check] jobs=%d app-shards=%d suite-shards=%d Go-package-parallel=%d GOMAXPROCS=%d isolated-e2e=%t effective-cpus=%d serial-reason=%q\n",
		b.jobs, b.shards, b.suiteShards, b.packages, b.procs, b.exclusiveE2E, cpus, reason)
	return schedule(ctx, b.jobs, parallel, isolated, runTask)
}

func main() {
	jobs := flag.Int("jobs", 0, "concurrent check groups (0 selects by CPU; multicore 1 requires -serial-reason or CHECK_SERIAL_REASON)")
	reason := flag.String("serial-reason", os.Getenv("CHECK_SERIAL_REASON"), "reason for an explicit multicore serial budget")
	shards := flag.Int("shards", 0, "App test shards (0 selects by CPU)")
	suiteShards := flag.Int("suite-shards", 0, "API, mainthread and E2E shards (0 selects by CPU)")
	goTool := flag.String("go", "go", "Go executable")
	pnpm := flag.String("pnpm", "pnpm", "pnpm executable")
	mode := flag.String("mode", "affected", "validation mode: affected or full")
	base := flag.String("base", "", "Git comparison base (default origin/main merge-base)")
	count := flag.Int("count", 0, "repeat race tests explicitly; 0 keeps Go native caching")
	flag.Parse()
	cpus := runtime.GOMAXPROCS(0)
	b, err := capacity(cpus, *jobs, *shards, *suiteShards)
	if err == nil {
		*reason, err = serialReason(cpus, *jobs, b, *reason)
	}
	if err == nil && *count < 0 {
		err = fmt.Errorf("count must be nonnegative")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := check(ctx, b, cpus, *reason, *goTool, *pnpm, *count, *mode, *base); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
