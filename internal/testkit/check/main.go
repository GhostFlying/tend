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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/GhostFlying/tend/internal/procx"
)

type budget struct {
	jobs, shards, packages, procs int
	exclusiveE2E                  bool
}

func capacity(cpus, jobs, shards int) (budget, error) {
	cpus = max(1, cpus)
	if jobs < 0 || jobs > 7 || shards < 0 || shards > 16 {
		return budget{}, fmt.Errorf("jobs must be 0-7 and shards must be 0-16 (0 selects automatically)")
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
		shards = max(1, min(4, cpus/8, jobs))
	}
	return budget{
		jobs: jobs, shards: shards,
		packages:     max(1, min(4, cpus/(2*jobs+shards))),
		procs:        max(1, min(4, cpus/jobs)),
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
		"TEST_COUNT":      strconv.Itoa(count),
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

func check(ctx context.Context, b budget, goTool, pnpm string, count int) error {
	directory, err := os.MkdirTemp("", "tend-check-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	parallel, isolated := tasks(b, directory, goTool, pnpm, count)
	fmt.Printf("[check] jobs=%d app-shards=%d Go-package-parallel=%d GOMAXPROCS=%d isolated-e2e=%t\n",
		b.jobs, b.shards, b.packages, b.procs, b.exclusiveE2E)
	return schedule(ctx, b.jobs, parallel, isolated, runTask)
}

func main() {
	jobs := flag.Int("jobs", 0, "concurrent check groups (0 selects by CPU, 1 selects sequential checks)")
	shards := flag.Int("shards", 0, "App test shards (0 selects by CPU)")
	goTool := flag.String("go", "go", "Go executable")
	pnpm := flag.String("pnpm", "pnpm", "pnpm executable")
	count := flag.Int("count", 0, "repeat race tests explicitly; 0 keeps Go native caching")
	flag.Parse()
	b, err := capacity(runtime.GOMAXPROCS(0), *jobs, *shards)
	if err == nil && *count < 0 {
		err = fmt.Errorf("count must be nonnegative")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := check(ctx, b, *goTool, *pnpm, *count); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
