package impact

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type FailureRoot struct{ Package, Root string }
type FaultEvidence struct {
	Name                    string
	Plan                    Plan
	Full, Selected          Measurement
	Failures, Missing       []FailureRoot
	Passed                  bool
	WebFailures, WebMissing []string
	Web                     *WebExecution `json:",omitempty"`
}
type FaultOptions struct {
	Dir, Go, Index, Report string
	Log                    io.Writer
}

func missingFailureRoots(plan Plan, full, selectedFailed, selectedRan []FailureRoot) []FailureRoot {
	var missing []FailureRoot
	for _, root := range full {
		if !slices.Contains(plan.Go[root.Package], root.Root) || !slices.Contains(selectedRan, root) || !slices.Contains(selectedFailed, root) {
			missing = append(missing, root)
		}
	}
	return missing
}

// failureEvents consumes native Go JSON, preserving top-level attribution for
// nested subtests. A package build failure alone is not a semantic mutant.
func failureEvents(directory string) (failed, executed []FailureRoot, err error) {
	paths, err := filepath.Glob(filepath.Join(directory, "race-*", "*.jsonl"))
	if err != nil {
		return nil, nil, err
	}
	failures, runs := map[FailureRoot]bool{}, map[FailureRoot]bool{}
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			return nil, nil, err
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 4096), 16<<20)
		for scanner.Scan() {
			var e struct{ Action, Package, Test string }
			if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
				f.Close()
				return nil, nil, err
			}
			if e.Test == "" {
				continue
			}
			root, _, _ := strings.Cut(e.Test, "/")
			key := FailureRoot{e.Package, root}
			if e.Action == "run" {
				runs[key] = true
			}
			if e.Action == "fail" {
				failures[key] = true
			}
		}
		err = scanner.Err()
		closeErr := f.Close()
		if err != nil {
			return nil, nil, err
		}
		if closeErr != nil {
			return nil, nil, closeErr
		}
	}
	for key := range failures {
		failed = append(failed, key)
	}
	for key := range runs {
		executed = append(executed, key)
	}
	compare := func(a, b FailureRoot) int { return strings.Compare(a.Package+"/"+a.Root, b.Package+"/"+b.Root) }
	slices.SortFunc(failed, compare)
	slices.SortFunc(executed, compare)
	return failed, executed, nil
}

// Faults applies reproducible, compiling semantic mutants in an independent
// clone at the exact coverage revision. It never changes the caller's source.
func Faults(ctx context.Context, o FaultOptions) (err error) {
	if o.Go == "" {
		o.Go = "go"
	}
	if o.Log == nil {
		o.Log = io.Discard
	}
	idx, err := ReadIndex(o.Index)
	if err != nil {
		return err
	}
	indexPath, err := filepath.Abs(o.Index)
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "tend-impact-faults-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	checkBin, raceBin := filepath.Join(tmp, "check"), filepath.Join(tmp, "racecheck")
	for _, build := range []struct{ path, pkg string }{{checkBin, "./internal/testkit/check"}, {raceBin, "./internal/testkit/racecheck"}} {
		if err := Command(ctx, o.Dir, o.Log, nil, o.Go, "build", "-o", build.path, build.pkg); err != nil {
			return err
		}
	}
	repo := filepath.Join(tmp, "source")
	if err := Command(ctx, o.Dir, o.Log, nil, "git", "clone", "--quiet", "--shared", "--no-checkout", o.Dir, repo); err != nil {
		return err
	}
	var report []FaultEvidence
	defer func() {
		if e := WriteJSON(o.Report, report); err == nil {
			err = e
		}
	}()
	mutants := []struct{ name, path, from, to string }{
		{"state-rule", "internal/domain/facts.go", "\tif !from.Valid() || !to.Valid() || from == to {", "\tif from == protocol.RunFinished && to == protocol.RunRunning { return true }\n\tif !from.Valid() || !to.Valid() || from == to {"},
		{"transaction-rollback", "internal/store/store.go", "if err := fn(&Tx{tx: tx, files: s.files}); err != nil {\n\t\treturn err", "if err := fn(&Tx{tx: tx, files: s.files}); err != nil {\n\t\t_ = tx.Commit()\n\t\treturn err"},
		{"http-contract", "internal/api/api.go", "\tw.WriteHeader(status)", "\tif status == http.StatusBadRequest { status = http.StatusOK }\n\tw.WriteHeader(status)"},
	}
	for _, mutant := range mutants {
		if err := Command(ctx, o.Dir, o.Log, nil, "git", "-C", repo, "checkout", "--quiet", "--detach", "--force", idx.Source.Commit); err != nil {
			return err
		}
		path := filepath.Join(repo, mutant.path)
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Count(string(b), mutant.from) != 1 {
			return fmt.Errorf("mutant anchor changed: %s", mutant.name)
		}
		if err := os.WriteFile(path, []byte(strings.Replace(string(b), mutant.from, mutant.to, 1)), 0o600); err != nil {
			return err
		}
		if err := Command(ctx, repo, o.Log, nil, "gofmt", "-w", path); err != nil {
			return err
		}
		plan, err := PlanChecks(ctx, repo, o.Go, indexPath, "", "")
		if err != nil {
			return err
		}
		report = append(report, FaultEvidence{Name: mutant.name, Plan: plan})
		e := &report[len(report)-1]
		fmt.Fprintf(o.Log, "[impact] semantic fault %s started\n", mutant.name)
		caseDir := filepath.Join(tmp, mutant.name)
		fullDir, selectedDir := filepath.Join(caseDir, "full"), filepath.Join(caseDir, "selected")
		for _, series := range []struct{ mode, events string }{{"full", fullDir}, {"affected", selectedDir}} {
			// The child inherits only this invocation's report destination.
			args := []string{"-dir", repo, "-go", o.Go, "-mode", series.mode, "-count", "1", "-index", indexPath, "-report", filepath.Join(caseDir, series.mode+".json"), "-race-runner", raceBin}
			args = append(args, CheckBudgetArgs()...)
			m, runErr := measuredWithEnv(ctx, o.Dir, o.Log, []string{"TEST_REPORT_DIR=" + series.events}, checkBin, args...)
			if series.mode == "full" {
				e.Full = m
			} else {
				e.Selected = m
			}
			var outcome Result
			if readErr := readJSON(filepath.Join(caseDir, series.mode+".json"), &outcome); readErr != nil || !outcome.Valid {
				return fmt.Errorf("invalid mutant execution %s/%s: %v", mutant.name, series.mode, readErr)
			}
			if runErr == nil {
				return fmt.Errorf("mutant unexpectedly passed %s/%s", mutant.name, series.mode)
			}
		}
		failed, _, err := failureEvents(fullDir)
		if err != nil {
			return err
		}
		selectedFailed, ran, err := failureEvents(selectedDir)
		if err != nil {
			return err
		}
		e.Failures = failed
		e.Missing = missingFailureRoots(plan, failed, selectedFailed, ran)
		e.Passed = len(failed) > 0 && len(e.Missing) == 0
		if err := WriteJSON(o.Report, report); err != nil {
			return err
		}
		if !e.Passed {
			return fmt.Errorf("fault selection failed: %s", mutant.name)
		}
	}
	for _, mutant := range []struct{ name, path, from, to string }{
		{"web-duration", "web/src/conversation/model.ts", "const s = Math.max(0, Math.round(ms / 1000));", "const s = Math.max(0, Math.round(ms / 100));"},
		{"web-source-reader", "web/src/conversation/model.ts", "const s = Math.max(0, Math.round(ms / 1000));", "const s = Math.max(0, Math.round(ms / 1000));\n  const invalidCopy = '中A'; void invalidCopy;"},
		{"go-source-web-reader", "internal/domain/task.go", `optionRetryImplement = "retry the implementation"`, `optionRetryImplement = "untranslated option"`},
	} {
		if err := Command(ctx, o.Dir, o.Log, nil, "git", "-C", repo, "checkout", "--quiet", "--detach", "--force", idx.Source.Commit); err != nil {
			return err
		}
		path := filepath.Join(repo, mutant.path)
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Count(string(b), mutant.from) != 1 {
			return fmt.Errorf("web mutant anchor changed: %s", mutant.name)
		}
		if err := os.WriteFile(path, []byte(strings.Replace(string(b), mutant.from, mutant.to, 1)), 0o600); err != nil {
			return err
		}
		plan, err := PlanChecks(ctx, repo, o.Go, indexPath, "", "")
		if err != nil {
			return err
		}
		report = append(report, FaultEvidence{Name: mutant.name, Plan: plan})
		e := &report[len(report)-1]
		fmt.Fprintf(o.Log, "[impact] semantic fault %s started\n", mutant.name)
		if !plan.Web.Run {
			return fmt.Errorf("web data consumer was omitted: %s", mutant.name)
		}
		if err := webFault(ctx, o, repo, tmp, e); err != nil {
			return err
		}
		if err := WriteJSON(o.Report, report); err != nil {
			return err
		}
	}
	return nil
}

func webFault(ctx context.Context, o FaultOptions, repo, tmp string, e *FaultEvidence) error {
	caseDir, err := os.MkdirTemp(tmp, "web-fault-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(caseDir)
	webDir := filepath.Join(repo, "web")
	if err := Command(ctx, webDir, o.Log, nil, PNPMTool(), "install", "--frozen-lockfile"); err != nil {
		return err
	}
	fullPath := filepath.Join(caseDir, "full-web.json")
	e.Full, err = measured(ctx, webDir, o.Log, PNPMTool(), "exec", "vitest", "run", "--reporter=json", "--outputFile="+fullPath)
	if err == nil {
		return fmt.Errorf("web mutant unexpectedly passed full suite")
	}
	var results struct {
		TestResults []struct {
			Name, Status     string
			AssertionResults []struct{ Status string } `json:"assertionResults"`
		} `json:"testResults"`
	}
	// Vitest's report contains additional framework fields by design.
	fullJSON, err := os.ReadFile(fullPath)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(fullJSON, &results); err != nil {
		return err
	}
	for _, result := range results.TestResults {
		semanticFailure := false
		for _, assertion := range result.AssertionResults {
			semanticFailure = semanticFailure || assertion.Status == "failed"
		}
		if semanticFailure {
			rel, err := filepath.Rel(webDir, result.Name)
			if err != nil {
				return err
			}
			e.WebFailures = append(e.WebFailures, filepath.ToSlash(rel))
		}
	}
	runner, webPlan, webReport := filepath.Join(caseDir, "web.cjs"), filepath.Join(caseDir, "web-plan.json"), filepath.Join(caseDir, "web-result.json")
	if err := os.WriteFile(runner, webRunner, 0o600); err != nil {
		return err
	}
	if err := WriteJSON(webPlan, e.Plan.Web); err != nil {
		return err
	}
	e.Selected, err = measured(ctx, repo, o.Log, "node", runner, webDir, PNPMTool(), webPlan, webReport)
	if err == nil {
		return fmt.Errorf("web mutant unexpectedly passed selection")
	}
	var execution WebExecution
	if err := readJSON(webReport, &execution); err != nil {
		return err
	}
	e.Web = &execution
	for _, file := range e.WebFailures {
		if !execution.Completed || !slices.Contains(execution.Files, file) || !slices.Contains(execution.FailedFiles, file) {
			e.WebMissing = append(e.WebMissing, file)
		}
	}
	e.Passed = len(e.WebFailures) > 0 && len(e.WebMissing) == 0
	if !e.Passed {
		return fmt.Errorf("web fault selection failed")
	}
	return nil
}
