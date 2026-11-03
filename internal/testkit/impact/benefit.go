package impact

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/procx"
)

type ReplayCase struct {
	PR     int
	Commit string
	Base   string `json:",omitempty"`
}
type ReplayManifest struct {
	Baseline string
	Cases    []ReplayCase
	Web      *ReplayCase `json:",omitempty"`
}
type Measurement struct {
	Seconds, UserSeconds, SystemSeconds float64
	LoadBefore, LoadAfter               []string
	Stages                              map[string]float64 `json:",omitempty"`
	StartedAt, FinishedAt               string
	Success                             bool
	Error                               string  `json:",omitempty"`
	Execution                           *Result `json:",omitempty"`
}
type Pair struct {
	Full, Selected Measurement
	SelectedRoots  int
	FullFallback   bool
}
type CaseBenefit struct {
	Case                  ReplayCase
	Pairs                 []Pair
	Reasons               []string
	Preparation           map[string]Measurement
	HistoricalRootSeconds float64
}
type BenefitReport struct {
	Format                                              int
	Manifest                                            ReplayManifest
	Initial, Refresh                                    Measurement
	Cases                                               []CaseBenefit
	Web                                                 *CaseBenefit `json:",omitempty"`
	WebBaseline                                         Measurement
	BaselineFullSeconds, BaselineCollectionSeconds      float64
	RefreshFullSeconds, RefreshCollectionSeconds        float64
	FullCycleSeconds, SelectedCycleSeconds, NetSeconds  float64
	BootstrapCycleSeconds, BootstrapNetSeconds          float64
	Completed                                           bool
	Validated                                           bool
	Failures                                            []string `json:",omitempty"`
	Error                                               string   `json:",omitempty"`
	CPUs                                                int
	Profile                                             Profile
	NativeCachePrime, NativeCacheWarm                   map[string]Measurement
	ToolFingerprint                                     string
	SetupSeconds                                        float64
	CollectionJobs                                      int
	BudgetArgs                                          []string
	ResourceReport, ResourceError                       string
	BootstrapToolFingerprint, BootstrapIndexFingerprint string
	CommonDependencySeconds                             float64
	MissingBaseline                                     Measurement
}
type BenefitOptions struct {
	Dir, Go, Cases, Report string
	CollectionJobs         int
	Resume                 string
	Log                    io.Writer
}

func measured(ctx context.Context, dir string, log io.Writer, program string, args ...string) (Measurement, error) {
	return measuredWithEnv(ctx, dir, log, nil, program, args...)
}

func measuredWithEnv(ctx context.Context, dir string, log io.Writer, overrides []string, program string, args ...string) (Measurement, error) {
	start := time.Now()
	load := func() []string {
		b, _ := os.ReadFile("/proc/loadavg")
		v := strings.Fields(string(b))
		return v[:min(3, len(v))]
	}
	m := Measurement{LoadBefore: load(), StartedAt: start.UTC().Format(time.RFC3339Nano)}
	c := exec.CommandContext(ctx, program, args...)
	if overrides != nil {
		c.Env = append([]string{}, os.Environ()...)
		for _, value := range overrides {
			key, _, _ := strings.Cut(value, "=")
			c.Env = slices.DeleteFunc(c.Env, func(entry string) bool { return strings.HasPrefix(entry, key+"=") })
			c.Env = append(c.Env, value)
		}
	}
	c.Dir = dir
	c.Stdout, c.Stderr = log, log
	graceful(c)
	err := c.Run()
	procx.KillGroup(c)
	m.Seconds, m.LoadAfter = duration(start), load()
	m.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	m.Success = err == nil
	if err != nil {
		m.Error = err.Error()
	}
	if c.ProcessState != nil {
		m.UserSeconds = c.ProcessState.UserTime().Seconds()
		m.SystemSeconds = c.ProcessState.SystemTime().Seconds()
	}
	return m, err
}

// A failed test is a retained observation, but missing, stale or contradictory
// execution evidence cannot be used to continue a comparison.
func checkMeasurement(m Measurement, commandErr error, path string, plan Plan, mode string) (Measurement, error) {
	var execution Result
	if err := readJSON(path, &execution); err != nil {
		return m, fmt.Errorf("missing measurement receipt: %w", err)
	}
	m.Execution, m.Stages = &execution, execution.Stages
	if !execution.Valid || execution.Commit != plan.Commit || execution.Fingerprint != plan.Fingerprint || execution.Profile != plan.Profile || execution.Mode != mode || execution.Passed != (commandErr == nil) || execution.Seconds <= 0 || ((mode == "full" || plan.Full) && execution.Passed && !execution.FullVerified) {
		return m, fmt.Errorf("invalid measurement receipt for %s", mode)
	}
	if commandErr != nil {
		var exited *exec.ExitError
		if !errors.As(commandErr, &exited) {
			return m, commandErr
		}
		m.Error = execution.Error
		if m.Error == "" {
			m.Error = commandErr.Error()
		}
	}
	return m, nil
}

func replayPairs(ctx context.Context, result *CaseBenefit, count int, full bool, run func(string, int) (Measurement, error), checkpoint func() error) error {
	for round := range 3 {
		result.Pairs = append(result.Pairs, Pair{SelectedRoots: count, FullFallback: full})
		pair := &result.Pairs[len(result.Pairs)-1]
		for step := range 2 {
			if err := ctx.Err(); err != nil {
				return err
			}
			mode := "full"
			if (round+step)%2 == 1 {
				mode = "affected"
			}
			m, runErr := run(mode, round)
			if mode == "full" {
				pair.Full = m
			} else {
				pair.Selected = m
			}
			if err := checkpoint(); err != nil {
				return err
			}
			if runErr != nil {
				return runErr
			}
		}
	}
	return nil
}

func validateReplay(ctx context.Context, dir string, manifest ReplayManifest) error {
	sha := regexp.MustCompile(`^[0-9a-f]{40}$`)
	if len(manifest.Cases) != 5 || !sha.MatchString(manifest.Baseline) {
		return fmt.Errorf("replay requires a baseline SHA and exactly five consecutive merged PRs")
	}
	seen := map[int]bool{}
	previous := manifest.Baseline
	for _, candidate := range manifest.Cases {
		if candidate.PR < 1 || seen[candidate.PR] || !sha.MatchString(candidate.Commit) {
			return fmt.Errorf("invalid or duplicate replay PR")
		}
		seen[candidate.PR] = true
		if _, err := output(ctx, dir, "git", "merge-base", "--is-ancestor", previous, candidate.Commit); err != nil {
			return fmt.Errorf("replay commits are not ordered ancestors: %w", err)
		}
		if previous == candidate.Commit {
			return fmt.Errorf("duplicate replay commit")
		}
		previous = candidate.Commit
	}
	if manifest.Web != nil {
		if !sha.MatchString(manifest.Web.Commit) || !sha.MatchString(manifest.Web.Base) || manifest.Web.PR < 1 {
			return fmt.Errorf("extra Web case requires an exact PR base and tip")
		}
		if _, err := output(ctx, dir, "git", "merge-base", "--is-ancestor", manifest.Web.Base, manifest.Web.Commit); err != nil {
			return err
		}
	}
	return nil
}

// Prepare both sides before timing their paired forced reruns. Compilation does
// not execute test roots or populate their result cache. Web collection warms
// transforms while preserving the same frozen source and dependency store.
func prepareReplay(ctx context.Context, repo, goTool string, log io.Writer) (map[string]Measurement, error) {
	result := map[string]Measurement{}
	flags, err := ResolvedGoFlags(ctx, repo, goTool)
	if err != nil {
		return result, err
	}
	cpus := max(1, runtime.GOMAXPROCS(0))
	processes := min(2, cpus)
	procs := strconv.Itoa(processes)
	packages := strconv.Itoa(max(1, min(3, cpus/processes)))
	env := []string{"GOMAXPROCS=" + procs, "GOFLAGS=" + strings.TrimSpace(flags+" -p="+packages), "VITEST_MAX_WORKERS=" + procs}
	for _, mode := range []struct {
		name string
		race bool
	}{{"go_compile", false}, {"go_race_compile", true}} {
		args := []string{"test"}
		if mode.race {
			args = append(args, "-race")
		}
		args = append(args, "-run=^$", "-count=1", "./...")
		result[mode.name], err = measuredWithEnv(ctx, repo, log, env, goTool, args...)
		if err != nil {
			return result, err
		}
	}
	if _, err := os.Stat(filepath.Join(repo, "web/package.json")); os.IsNotExist(err) {
		return result, nil
	}
	for _, stage := range []struct {
		name string
		args []string
	}{
		{"dependencies", []string{"install", "--frozen-lockfile"}},
		{"web_modules", []string{"exec", "vitest", "list", "--staticParse=false"}},
	} {
		result[stage.name], err = measuredWithEnv(ctx, filepath.Join(repo, "web"), log, env, PNPMTool(), stage.args...)
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

// Benefit replays immutable revisions in a private clone. Each pair uses the
// same source and budget, disabled result caching, and alternating order.
// Raw observations include refresh costs and never enable selection by itself.
func Benefit(ctx context.Context, o BenefitOptions) (err error) {
	setupStart := time.Now()
	if o.Go == "" {
		o.Go = "go"
	}
	if o.CollectionJobs < 1 {
		o.CollectionJobs = DefaultJobs()
	}
	if o.Log == nil {
		o.Log = io.Discard
	}
	var manifest ReplayManifest
	if err := readJSON(o.Cases, &manifest); err != nil {
		return err
	}
	if err := validateReplay(ctx, o.Dir, manifest); err != nil {
		return err
	}
	report := BenefitReport{Format: Format, Manifest: manifest, CPUs: runtime.GOMAXPROCS(0), CollectionJobs: o.CollectionJobs, BudgetArgs: CheckBudgetArgs()}
	report.Profile, err = BuildProfile(ctx, o.Dir, o.Go)
	if err != nil {
		return err
	}
	toolSource, err := Capture(ctx, o.Dir, o.Go)
	if err != nil {
		return err
	}
	report.ToolFingerprint = toolSource.Fingerprint
	checkpoint := func() error { return WriteJSON(o.Report, report) }
	stopMonitor, monitorErr := startResourceMonitor(ctx, filepath.Join(filepath.Dir(o.Report), "resources.jsonl"))
	if monitorErr != nil {
		report.ResourceError = monitorErr.Error()
	} else {
		report.ResourceReport = "resources.jsonl"
		defer stopMonitor()
	}
	defer func() {
		if err != nil {
			report.Error = err.Error()
		}
		if writeErr := WriteJSON(o.Report, report); err == nil {
			err = writeErr
		}
	}()
	tmp, err := os.MkdirTemp("", "tend-impact-replay-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	checkBin, raceBin, impactBin := filepath.Join(tmp, "check"), filepath.Join(tmp, "racecheck"), filepath.Join(tmp, "impact")
	for _, build := range []struct{ path, pkg string }{{checkBin, "./internal/testkit/check"}, {raceBin, "./internal/testkit/racecheck"}, {impactBin, "./internal/testkit/impact/cmd"}} {
		if err := Command(ctx, o.Dir, o.Log, nil, o.Go, "build", "-o", build.path, build.pkg); err != nil {
			return err
		}
	}
	repo := filepath.Join(tmp, "source")
	if err := Command(ctx, o.Dir, o.Log, nil, "git", "clone", "--quiet", "--shared", "--no-checkout", o.Dir, repo); err != nil {
		return err
	}
	checkout := func(commit string) error {
		return Command(ctx, repo, o.Log, nil, "git", "checkout", "--quiet", "--detach", "--force", commit)
	}
	if err := checkout(manifest.Baseline); err != nil {
		return err
	}
	indexPath, err := filepath.Abs(filepath.Join(filepath.Dir(o.Report), "baseline.json"))
	if err != nil {
		return err
	}
	refreshPath := filepath.Join(filepath.Dir(indexPath), "refresh.json")
	if o.Resume != "" {
		if err := reuseBootstrap(ctx, repo, o.Go, indexPath, o.Resume, &report); err != nil {
			return err
		}
		fmt.Fprintln(o.Log, "[impact] reusing the compatible verified bootstrap; replaying every pair with the current frozen tools")
	} else {
		report.SetupSeconds = duration(setupStart)
		report.Initial, err = measured(ctx, o.Dir, o.Log, impactBin, "baseline", "-dir", repo, "-go", o.Go, "-index", indexPath, "-check-runner", checkBin, "-jobs", fmt.Sprint(o.CollectionJobs))
		if err != nil {
			return fmt.Errorf("initial baseline: %w", err)
		}
	}
	if o.Resume != "" {
		report.SetupSeconds += duration(setupStart)
	}
	index, err := ReadIndex(indexPath)
	if err != nil {
		return err
	}
	report.BaselineFullSeconds, report.BaselineCollectionSeconds = index.FullSeconds, index.CollectionSeconds
	if err := checkpoint(); err != nil {
		return err
	}
	measureCheck := func(plan Plan, mode, name string, count int) (Measurement, error) {
		path := filepath.Join(tmp, name, "result.json")
		args := append([]string{"-dir", repo, "-go", o.Go, "-mode", mode, "-count", strconv.Itoa(count), "-index", indexPath, "-report", path, "-race-runner", raceBin}, CheckBudgetArgs()...)
		m, commandErr := measured(ctx, o.Dir, o.Log, checkBin, args...)
		if err := ctx.Err(); err != nil {
			return m, err
		}
		return checkMeasurement(m, commandErr, path, plan, mode)
	}
	runCase := func(result *CaseBenefit) error {
		candidate := result.Case
		if err := checkout(candidate.Commit); err != nil {
			return err
		}
		plan, err := PlanChecks(ctx, repo, o.Go, indexPath, "", "")
		if err != nil {
			return err
		}
		result.Reasons = plan.Reasons
		result.HistoricalRootSeconds = plan.HistoricalRootSeconds
		fmt.Fprintf(o.Log, "[impact] prepare warm build caches for PR %d\n", candidate.PR)
		result.Preparation, err = prepareReplay(ctx, repo, o.Go, o.Log)
		if err != nil {
			return fmt.Errorf("prepare replay PR %d: %w", candidate.PR, err)
		}
		count := 0
		for _, roots := range plan.Go {
			count += len(roots)
		}
		return replayPairs(ctx, result, count, plan.Full, func(mode string, round int) (Measurement, error) {
			fmt.Fprintf(o.Log, "[impact] replay PR %d %s round %d %s\n", candidate.PR, candidate.Commit, round+1, mode)
			m, err := measureCheck(plan, mode, fmt.Sprintf("pr-%d-%d-%s", candidate.PR, round, mode), 1)
			if err == nil && !m.Success {
				report.Failures = append(report.Failures, fmt.Sprintf("PR %d round %d %s: %s", candidate.PR, round+1, mode, m.Error))
			}
			return m, err
		}, checkpoint)
	}
	for _, candidate := range manifest.Cases {
		report.Cases = append(report.Cases, CaseBenefit{Case: candidate})
		if err := runCase(&report.Cases[len(report.Cases)-1]); err != nil {
			return err
		}
	}
	tipPlan, err := PlanChecks(ctx, repo, o.Go, indexPath, "", "")
	if err != nil {
		return err
	}
	report.Refresh, err = measured(ctx, o.Dir, o.Log, impactBin, "baseline", "-dir", repo, "-go", o.Go, "-index", refreshPath, "-check-runner", checkBin, "-jobs", fmt.Sprint(o.CollectionJobs))
	if sourceErr := SamePlan(ctx, repo, o.Go, tipPlan); sourceErr != nil {
		return sourceErr
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		report.Failures = append(report.Failures, "refresh: "+err.Error())
	} else {
		refresh, err := ReadIndex(refreshPath)
		if err != nil {
			return err
		}
		report.RefreshFullSeconds, report.RefreshCollectionSeconds = refresh.FullSeconds, refresh.CollectionSeconds
	}
	if err := checkpoint(); err != nil {
		return err
	}
	// A missing baseline must execute the complete fixed candidate as well.
	originalIndex := indexPath
	indexPath = filepath.Join(tmp, "absent-index.json")
	missingPlan, err := PlanChecks(ctx, repo, o.Go, indexPath, "", "")
	if err != nil || !missingPlan.Full {
		return fmt.Errorf("missing-index fallback plan: %v", err)
	}
	report.MissingBaseline, err = measureCheck(missingPlan, "affected", "missing-baseline", 1)
	indexPath = originalIndex
	if err != nil {
		return err
	}
	if !report.MissingBaseline.Success {
		report.Failures = append(report.Failures, "missing baseline: "+report.MissingBaseline.Error)
	}
	if err := checkpoint(); err != nil {
		return err
	}
	// Separately observe the ordinary native result cache at the cycle tip.
	// Forced reruns above never depend on these cached observations.
	report.NativeCachePrime, report.NativeCacheWarm = map[string]Measurement{}, map[string]Measurement{}
	for i, series := range []map[string]Measurement{report.NativeCachePrime, report.NativeCacheWarm} {
		for _, mode := range []string{"full", "affected"} {
			m, err := measureCheck(tipPlan, mode, fmt.Sprintf("native-%d-%s", i, mode), 0)
			series[mode] = m
			if err != nil {
				return err
			}
			if !m.Success {
				report.Failures = append(report.Failures, "native cache "+mode+": "+m.Error)
			}
			if err := checkpoint(); err != nil {
				return err
			}
		}
	}
	if manifest.Web != nil {
		if err := checkout(manifest.Web.Base); err != nil {
			return err
		}
		indexPath = filepath.Join(filepath.Dir(indexPath), "web-baseline.json")
		report.WebBaseline, err = measured(ctx, o.Dir, o.Log, impactBin, "baseline", "-dir", repo, "-go", o.Go, "-index", indexPath, "-check-runner", checkBin, "-jobs", fmt.Sprint(o.CollectionJobs))
		if err != nil {
			return fmt.Errorf("extra Web baseline: %w", err)
		}
		report.Web = &CaseBenefit{Case: *manifest.Web}
		if err := runCase(report.Web); err != nil {
			return err
		}
	}
	if err := SameSnapshot(ctx, o.Dir, o.Go, toolSource); err != nil {
		return fmt.Errorf("replay tooling changed: %w", err)
	}
	report.Completed = true
	if len(report.Failures) > 0 {
		return fmt.Errorf("replay collected with %d failed observations; benefit is not validated", len(report.Failures))
	}
	report.Validated = true
	median := func(values []float64) float64 { slices.Sort(values); return values[len(values)/2] }
	for _, c := range report.Cases {
		var full, selected []float64
		for _, pair := range c.Pairs {
			full = append(full, pair.Full.Seconds)
			selected = append(selected, pair.Selected.Seconds)
		}
		report.FullCycleSeconds += median(full)
		report.SelectedCycleSeconds += median(selected)
		report.CommonDependencySeconds += c.Preparation["dependencies"].Seconds
	}
	// Downloads prepared before the warm-cache comparison are common costs of
	// both strategies. Include them on both sides; compiler/cache preparation
	// remains separately reported as an experimental control.
	report.FullCycleSeconds += report.CommonDependencySeconds
	report.SelectedCycleSeconds += report.CommonDependencySeconds
	report.SelectedCycleSeconds += report.Refresh.Seconds
	report.NetSeconds = report.FullCycleSeconds - report.SelectedCycleSeconds
	report.BootstrapCycleSeconds = report.SelectedCycleSeconds + report.Initial.Seconds + report.SetupSeconds
	report.BootstrapNetSeconds = report.FullCycleSeconds - report.BootstrapCycleSeconds
	report.Completed = true
	return nil
}

// Reuse only a completed ordinary-full + collection bootstrap, never any
// measured pairs. Source, profile, manifest and CPU controls must all match.
func reuseBootstrap(ctx context.Context, repo, goTool, indexPath, previousPath string, report *BenefitReport) error {
	var previous BenefitReport
	if err := readJSON(previousPath, &previous); err != nil {
		return err
	}
	if !reflect.DeepEqual(previous.Manifest, report.Manifest) || previous.Profile != report.Profile || previous.CPUs != report.CPUs || previous.CollectionJobs != report.CollectionJobs || !slices.Equal(previous.BudgetArgs, report.BudgetArgs) || previous.Initial.Seconds <= 0 || previous.Initial.FinishedAt == "" || previous.BaselineFullSeconds <= 0 || previous.BaselineCollectionSeconds <= 0 {
		return fmt.Errorf("resume baseline measurement is incomplete or incompatible")
	}
	oldPath := filepath.Join(filepath.Dir(previousPath), "baseline.json")
	idx, err := ReadIndex(oldPath)
	if err != nil {
		return err
	}
	if idx.Source.Commit != report.Manifest.Baseline || idx.FullSeconds != previous.BaselineFullSeconds || idx.CollectionSeconds != previous.BaselineCollectionSeconds {
		return fmt.Errorf("resume index does not match the measured bootstrap")
	}
	if err := SameSnapshot(ctx, repo, goTool, idx.Source); err != nil {
		return err
	}
	b, err := os.ReadFile(oldPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(indexPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(indexPath, b, 0o600); err != nil {
		return err
	}
	report.Initial = previous.Initial
	report.BootstrapToolFingerprint = previous.ToolFingerprint
	report.BootstrapIndexFingerprint = hash(b)
	report.SetupSeconds += previous.SetupSeconds
	return nil
}
