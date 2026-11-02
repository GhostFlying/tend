// Command racecheck runs the complete race suite with isolated app-package shards.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strings"
	"sync"
)

const appPackage = "github.com/GhostFlying/tend/internal/app"
const e2ePackage = "github.com/GhostFlying/tend/internal/e2e"

func packagePhases(packages []string) (others, isolated []string, app bool) {
	for _, pkg := range packages {
		switch pkg {
		case appPackage:
			app = true
		case e2ePackage:
			isolated = append(isolated, pkg)
		default:
			others = append(others, pkg)
		}
	}
	return others, isolated, app
}

func run(ctx context.Context, command string, args ...string) error {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func output(ctx context.Context, command string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Stderr = os.Stderr
	return cmd.Output()
}

func roots(list string) []string {
	var result []string
	for line := range strings.Lines(list) {
		name := strings.TrimSpace(line)
		if strings.HasPrefix(name, "Test") || strings.HasPrefix(name, "Example") || strings.HasPrefix(name, "Fuzz") {
			result = append(result, name)
		}
	}
	return result
}

func patterns(names []string, count int) []string {
	groups := make([][]string, min(count, len(names)))
	for index, name := range names {
		groups[index%len(groups)] = append(groups[index%len(groups)], regexp.QuoteMeta(name))
	}
	var result []string
	for _, group := range groups {
		result = append(result, "^("+strings.Join(group, "|")+")$")
	}
	return result
}

func check(ctx context.Context, shards int, timeout string, packages []string) error {
	listed, err := output(ctx, "go", append([]string{"list"}, packages...)...)
	if err != nil {
		return fmt.Errorf("list packages: %w", err)
	}
	others, isolated, app := packagePhases(strings.Fields(string(listed)))
	var commands [][]string
	if app {
		list, err := output(ctx, "go", "test", "-race", "-list", ".", appPackage)
		if err != nil {
			return fmt.Errorf("list app test roots: %w", err)
		}
		names := roots(string(list))
		if len(names) == 0 {
			return fmt.Errorf("app race binary returned no test roots")
		}
		groups := patterns(names, shards)
		fmt.Printf("app race: %d root targets across %d isolated shards\n", len(names), len(groups))
		for _, pattern := range groups {
			commands = append(commands, []string{"go", "test", "-race", "-timeout", timeout, "-run", pattern, appPackage})
		}
	}
	// Keep timing-sensitive transport suites out of the CPU-bound app phase.
	var failures []error
	if len(others) > 0 {
		failures = append(failures, run(ctx, "go", append([]string{"test", "-race", "-timeout", timeout}, others...)...))
	}
	// These suites exercise real transport and lease timing. Keep their CPU
	// and filesystem budgets independent of the bulk store/API test processes.
	for _, pkg := range isolated {
		fmt.Printf("isolated race package %s started\n", pkg)
		failures = append(failures, run(ctx, "go", "test", "-race", "-timeout", timeout, pkg))
	}
	var wg sync.WaitGroup
	results := make(chan error, len(commands))
	for index, command := range commands {
		wg.Go(func() {
			fmt.Printf("race group %d/%d started\n", index+1, len(commands))
			err := run(ctx, command[0], command[1:]...)
			fmt.Printf("race group %d/%d finished: %v\n", index+1, len(commands), err)
			results <- err
		})
	}
	wg.Wait()
	close(results)
	for err := range results {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

func main() {
	shards := flag.Int("shards", 1, "number of isolated app race processes (1-16)")
	timeout := flag.String("timeout", "60m", "timeout per test process")
	flag.Parse()
	if *shards < 1 || *shards > 16 {
		fmt.Fprintln(os.Stderr, "shards must be 1-16")
		os.Exit(2)
	}
	packages := flag.Args()
	if len(packages) == 0 {
		packages = []string{"./..."}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if err := check(ctx, *shards, *timeout, packages); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
