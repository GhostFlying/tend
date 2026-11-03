// Command impact manages developer validation indexes and replay evidence.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/GhostFlying/tend/internal/testkit/impact"
)

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: impact baseline|benefit|faults|verify-source|profile [options]")
	}
	f := flag.NewFlagSet(args[0], flag.ContinueOnError)
	goTool := f.String("go", "go", "Go executable")
	directory := f.String("dir", ".", "repository directory")
	indexPath := f.String("index", ".local/impact/index.json", "verified coverage index")
	jobs := f.Int("jobs", impact.DefaultJobs(), "bounded collection workers")
	cases := f.String("cases", ".local/impact/cases.json", "fixed replay manifest")
	report := f.String("report", ".local/impact/benefit.json", "benefit report")
	checkRunner := f.String("check-runner", "", "compiled check runner for immutable historical replay")
	resume := f.String("resume", "", "reuse a compatible successful bootstrap from this prior replay report; all pairs rerun")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	dir, err := filepath.Abs(*directory)
	if err != nil {
		return err
	}
	if args[0] == "profile" {
		p, err := impact.BuildProfile(ctx, dir, *goTool)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(p)
	}
	if args[0] == "verify-source" {
		index, err := impact.ReadIndex(*indexPath)
		if err != nil {
			return err
		}
		return impact.SameSnapshot(ctx, dir, *goTool, index.Source)
	}
	if args[0] == "benefit" {
		return impact.Benefit(ctx, impact.BenefitOptions{Dir: dir, Go: *goTool, Cases: *cases, Report: *report, Log: os.Stdout, CollectionJobs: *jobs, Resume: *resume})
	}
	if args[0] == "faults" {
		return impact.Faults(ctx, impact.FaultOptions{Dir: dir, Go: *goTool, Index: *indexPath, Report: *report, Log: os.Stdout})
	}
	if args[0] != "baseline" {
		return fmt.Errorf("unknown impact command %q", args[0])
	}
	before, err := impact.Capture(ctx, dir, *goTool)
	if err != nil {
		return err
	}
	if !before.Clean {
		return fmt.Errorf("baseline requires a clean committed source tree")
	}
	start := time.Now()
	recordsDir, err := os.MkdirTemp("", "tend-impact-full-events-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(recordsDir)
	runner := *checkRunner
	if runner == "" {
		tmp, err := os.MkdirTemp("", "tend-impact-full-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tmp)
		runner = filepath.Join(tmp, "check")
		if err := impact.Command(ctx, dir, os.Stdout, nil, *goTool, "build", "-o", runner, "./internal/testkit/check"); err != nil {
			return err
		}
	}
	argsFull := append([]string{"-mode", "full", "-count", "1", "-go", *goTool, "-dir", dir}, impact.CheckBudgetArgs()...)
	fullEnv := []string{}
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "TEST_REPORT_DIR=") {
			fullEnv = append(fullEnv, value)
		}
	}
	fullEnv = append(fullEnv, "TEST_REPORT_DIR="+recordsDir)
	fullErr := impact.CommandGraceful(ctx, dir, os.Stdout, fullEnv, runner, argsFull...)
	if fullErr != nil {
		return fmt.Errorf("full verification failed: %w", fullErr)
	}
	fullSeconds := time.Since(start).Seconds()
	fullRecords, err := impact.FullExecutions(recordsDir, before)
	if err != nil {
		return err
	}
	if err := impact.SameSnapshot(ctx, dir, *goTool, before); err != nil {
		return err
	}
	index, err := impact.Collect(ctx, impact.CollectOptions{Dir: dir, Go: *goTool, Jobs: *jobs, Log: os.Stdout, FullExecutions: fullRecords})
	if err != nil {
		return err
	}
	if index.Source.Fingerprint != before.Fingerprint {
		return fmt.Errorf("source changed between full verification and collection")
	}
	index.VerifiedFull, index.FullSeconds = true, fullSeconds
	path := *indexPath
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	if err := impact.WriteJSON(path, index); err != nil {
		return err
	}
	fmt.Printf("[impact] baseline %s: %d roots, full %.1fs, collection %.1fs\n", index.Source.Commit, len(index.Executions), index.FullSeconds, index.CollectionSeconds)
	return nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}
