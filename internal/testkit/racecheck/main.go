// Command racecheck runs the complete race suite with isolated package shards.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/GhostFlying/tend/internal/testkit/impact"
)

const appPackage = "github.com/GhostFlying/tend/internal/app"
const e2ePackage = "github.com/GhostFlying/tend/internal/e2e"

// CI groups partition go list output; new packages belong to other by default.
func packageGroup(pkg string) string {
	switch pkg {
	case appPackage:
		return "app"
	case e2ePackage:
		return "e2e"
	case "github.com/GhostFlying/tend/internal/api":
		return "api"
	case "github.com/GhostFlying/tend/internal/mainthread":
		return "mainthread"
	default:
		return "other"
	}
}

func selectGroup(packages []string, group string) ([]string, error) {
	switch group {
	case "all":
		return packages, nil
	case "app", "api", "mainthread", "e2e", "other":
	default:
		return nil, fmt.Errorf("unknown race group %q", group)
	}
	var result []string
	for _, pkg := range packages {
		if packageGroup(pkg) == group {
			result = append(result, pkg)
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("race group %q contains no selected packages", group)
	}
	return result, nil
}

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

func run(ctx context.Context, report, command string, args ...string) (err error) {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if report != "" {
		file, createErr := os.Create(report)
		if createErr != nil {
			return createErr
		}
		defer func() { err = errors.Join(err, file.Close()) }()
		cmd.Stdout = io.MultiWriter(os.Stdout, file)
	}
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

// Measured costs affect placement only. Discovery remains the coverage source,
// including new tests, examples and fuzz seeds absent from the cost table.
func patterns(names []string, count int) []string {
	return weightedPatterns(names, count, nil)
}

func orderPackages(packages []string, costs map[string]int) []string {
	packages = slices.Clone(packages)
	sort.Slice(packages, func(i, j int) bool {
		if costs[packages[i]] != costs[packages[j]] {
			return costs[packages[i]] > costs[packages[j]]
		}
		return packages[i] < packages[j]
	})
	return packages
}

func weightedPatterns(names []string, count int, costs map[string]int) []string {
	names = slices.Clone(names)
	cost := func(name string) int { return max(1, costs[name]) }
	sort.Slice(names, func(i, j int) bool {
		if cost(names[i]) != cost(names[j]) {
			return cost(names[i]) > cost(names[j])
		}
		return names[i] < names[j]
	})
	groups := make([][]string, min(count, len(names)))
	loads := make([]int, len(groups))
	for _, name := range names {
		index := 0
		for i := range groups {
			if loads[i] < loads[index] {
				index = i
			}
		}
		groups[index] = append(groups[index], regexp.QuoteMeta(name))
		loads[index] += cost(name)
	}
	var result []string
	for _, group := range groups {
		slices.Sort(group)
		result = append(result, "^("+strings.Join(group, "|")+")$")
	}
	return result
}

func testArgs(timeout string, count int) []string {
	args := []string{"test", "-race", "-timeout", timeout}
	if count > 0 {
		args = append(args, "-count", strconv.Itoa(count))
	}
	return args
}

func check(ctx context.Context, goTool string, shards, suiteShards, count int, timeout, group, reportDir string, packages []string) error {
	return checkPlan(ctx, goTool, shards, suiteShards, count, timeout, group, reportDir, "", packages)
}

func checkPlan(ctx context.Context, goTool string, shards, suiteShards, count int, timeout, group, reportDir, planPath string, packages []string) error {
	var plan *impact.Plan
	if planPath != "" {
		var err error
		plan, err = impact.ReadPlan(planPath)
		if err != nil {
			return err
		}
		dir, err := os.Getwd()
		if err != nil {
			return err
		}
		if err := impact.SamePlan(ctx, dir, goTool, *plan); err != nil {
			if errors.Is(err, impact.ErrConfigurationChanged) {
				fmt.Println("[impact] build configuration changed: running the complete group")
				plan = nil
			} else {
				return err
			}
		}
	}
	var reports string
	if reportDir != "" {
		if err := os.MkdirAll(reportDir, 0o755); err != nil {
			return err
		}
		var err error
		reports, err = os.MkdirTemp(reportDir, "race-"+group+"-")
		if err != nil {
			return err
		}
	}
	args := testArgs(timeout, count)
	if reports != "" {
		args = append(args, "-json")
	}
	report := func(name string) string {
		if reports == "" {
			return ""
		}
		return filepath.Join(reports, name+".jsonl")
	}
	listed, err := output(ctx, goTool, append([]string{"list", "-race"}, packages...)...)
	if err != nil {
		return fmt.Errorf("list packages: %w", err)
	}
	selected, err := selectGroup(strings.Fields(string(listed)), group)
	if err != nil {
		return err
	}
	if plan != nil {
		known := strings.Fields(string(listed))
		for pkg := range plan.Go {
			if !slices.Contains(known, pkg) {
				return fmt.Errorf("planned package not discovered: %s", pkg)
			}
		}
		selected = slices.DeleteFunc(selected, func(pkg string) bool { _, ok := plan.Go[pkg]; return !ok })
		if len(selected) == 0 {
			fmt.Printf("[impact] %s has no planned targets\n", group)
			return nil
		}
	}
	others, isolated, app := packagePhases(selected)
	type command struct {
		name string
		args []string
	}
	shardCommands := func(pkg string, count int) ([]command, error) {
		name := pkg[strings.LastIndex(pkg, "/")+1:]
		if count == 1 && plan == nil {
			return []command{{name, append(slices.Clone(args), pkg)}}, nil
		}
		list, err := output(ctx, goTool, "test", "-race", "-list", ".", pkg)
		if err != nil {
			return nil, fmt.Errorf("list %s test roots: %w", pkg, err)
		}
		names := roots(string(list))
		if len(names) == 0 {
			return nil, fmt.Errorf("%s race binary returned no test roots", pkg)
		}
		if plan != nil {
			wanted := plan.Go[pkg]
			for _, root := range wanted {
				if !slices.Contains(names, root) {
					return nil, fmt.Errorf("planned root not discovered: %s/%s", pkg, root)
				}
			}
			names = slices.Clone(wanted)
		}
		groups := weightedPatterns(names, count, rootCosts[pkg])
		fmt.Printf("%s race: %d root targets across %d isolated shards\n", name, len(names), len(groups))
		var commands []command
		for i, pattern := range groups {
			commands = append(commands, command{fmt.Sprintf("%s-%d", name, i+1), append(slices.Clone(args), "-run", pattern, pkg)})
		}
		return commands, nil
	}
	runCommands := func(commands []command) error {
		var wg sync.WaitGroup
		results := make(chan error, len(commands))
		limit := len(commands)
		if plan != nil && (group == "other" || group == "all") {
			limit = 1
			for _, flag := range strings.Fields(os.Getenv("GOFLAGS")) {
				if strings.HasPrefix(flag, "-p=") {
					if n, err := strconv.Atoi(strings.TrimPrefix(flag, "-p=")); err == nil {
						limit = max(1, n)
					}
				}
			}
		}
		queue := make(chan command, len(commands))
		for _, command := range commands {
			queue <- command
		}
		close(queue)
		for range min(limit, len(commands)) {
			wg.Go(func() {
				for command := range queue {
					fmt.Printf("race %s started\n", command.name)
					err := run(ctx, report(command.name), goTool, command.args...)
					fmt.Printf("race %s finished: %v\n", command.name, err)
					results <- err
				}
			})
		}
		wg.Wait()
		close(results)
		var errs []error
		for err := range results {
			errs = append(errs, err)
		}
		return errors.Join(errs...)
	}
	var failures []error
	var bulk []command
	var unsharded []string
	for _, pkg := range others {
		if plan != nil || (suiteShards > 1 && (packageGroup(pkg) == "api" || packageGroup(pkg) == "mainthread")) {
			shardCount := suiteShards
			if packageGroup(pkg) == "other" {
				shardCount = 1
			}
			commands, err := shardCommands(pkg, shardCount)
			failures = append(failures, err)
			bulk = append(bulk, commands...)
		} else {
			unsharded = append(unsharded, pkg)
		}
	}
	if len(unsharded) > 0 {
		failures = append(failures, run(ctx, report("packages"), goTool, append(slices.Clone(args), orderPackages(unsharded, packageCosts)...)...))
	}
	// Standalone all-package checks retain an independent transport phase. The
	// local scheduler additionally reserves the entire phase on small machines.
	for _, pkg := range isolated {
		commands, err := shardCommands(pkg, suiteShards)
		if err == nil {
			err = runCommands(commands)
		}
		failures = append(failures, err)
	}
	if app {
		commands, err := shardCommands(appPackage, shards)
		failures = append(failures, err)
		bulk = append(bulk, commands...)
	}
	failures = append(failures, runCommands(bulk))
	return errors.Join(failures...)
}

func main() {
	goTool := flag.String("go", "go", "Go executable used for package discovery and tests")
	shards := flag.Int("shards", 1, "number of isolated app race processes (0 selects by CPU, 1-16 explicit)")
	suiteShards := flag.Int("suite-shards", 1, "isolated API, mainthread and E2E processes per package (0 selects by CPU, 1-4 explicit)")
	count := flag.Int("count", 0, "repeat tests explicitly; 0 keeps Go native caching")
	reportDir := flag.String("report-dir", "", "optional directory for native Go test JSON reports")
	planPath := flag.String("plan", "", "validated impact plan; omitted runs the complete selected group")
	timeout := flag.String("timeout", "60m", "timeout per test process")
	group := flag.String("group", "all", "package group: all, app, api, mainthread, e2e, other")
	flag.Parse()
	if *shards == 0 {
		*shards = max(1, min(4, runtime.GOMAXPROCS(0)/8))
	}
	if *suiteShards == 0 {
		*suiteShards = max(1, min(4, runtime.GOMAXPROCS(0)/8))
	}
	if *shards < 1 || *shards > 16 || *suiteShards < 1 || *suiteShards > 4 || *count < 0 {
		fmt.Fprintln(os.Stderr, "App shards must be 0-16, suite shards 0-4 and count nonnegative")
		os.Exit(2)
	}
	packages := flag.Args()
	if len(packages) == 0 {
		packages = []string{"./..."}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if err := checkPlan(ctx, *goTool, *shards, *suiteShards, *count, *timeout, *group, *reportDir, *planPath, packages); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
