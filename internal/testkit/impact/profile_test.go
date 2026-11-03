package impact

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSemanticBuildFlagsPreserveQuotedValuesAndIgnoreParallelBudget(t *testing.T) {
	for _, sample := range []struct{ in, want string }{
		{"-p=3 -tags=extra -p=8", "-tags=extra"},
		{"'-gcflags=all=-N  -l' -p=4", "'-gcflags=all=-N  -l'"},
		{"\"-p=2\" -p=3", "\"-p=2\""},
		{"-ldflags=\"-X 'a=-p=3'\" -p=2", "-ldflags=\"-X 'a=-p=3'\""},
	} {
		if got := semanticGoFlags(sample.in); got != sample.want {
			t.Fatalf("%q => %q, want %q", sample.in, got, sample.want)
		}
	}
}

func TestSelectedChecksPreserveFlagsFromGoEnvironmentFile(t *testing.T) {
	dir := fixtureRepo(t)
	fixtureWrite(t, dir, "Makefile", "go-check:\n\t@true\n")
	fixtureWrite(t, dir, "extra_test.go", "//go:build impact_extra\n\npackage fixture\nimport \"testing\"\nfunc TestExtra(t *testing.T){t.Fatal(\"GOENV root ran\")}\n")
	goenv := filepath.Join(t.TempDir(), "env")
	if err := os.WriteFile(goenv, []byte("GOFLAGS=-tags=impact_extra\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOENV", goenv)
	t.Setenv("GOFLAGS", "")
	if err := os.Unsetenv("GOFLAGS"); err != nil {
		t.Fatal(err)
	}
	s, err := Capture(t.Context(), dir, "go")
	if err != nil {
		t.Fatal(err)
	}
	p := Select(s, nil, "fixture")
	p.Go = map[string][]string{s.Module: {"TestExtra"}}
	plan := filepath.Join(dir, ".local/plan.json")
	if err := WriteJSON(plan, p); err != nil {
		t.Fatal(err)
	}
	tools, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	runner := filepath.Join(t.TempDir(), "racecheck")
	if _, err := output(t.Context(), tools, "go", "build", "-o", runner, "../racecheck"); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	r, err := Run(t.Context(), RunOptions{Dir: dir, Go: "go", PlanPath: plan, RaceRunner: runner, Jobs: 1, Procs: 1, Packages: 1, Log: &log}, p)
	if err == nil || r.Passed || !r.Valid || !strings.Contains(log.String(), "GOENV root ran") {
		t.Fatal("stored Go build flags were dropped", r, err, log.String())
	}
}

func TestSavedPlanConfigurationChangeRunsNewTaggedRoots(t *testing.T) {
	tools, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	runner := filepath.Join(t.TempDir(), "racecheck")
	if _, err := output(t.Context(), tools, "go", "build", "-o", runner, "../racecheck"); err != nil {
		t.Fatal(err)
	}
	dir := fixtureRepo(t)
	fixtureWrite(t, dir, "extra_test.go", "//go:build impact_extra\n\npackage fixture\nimport \"testing\"\nfunc TestExtra(t *testing.T){t.Fatal(\"extra root ran\")}\n")
	fixtureCommit(t, dir)
	s, err := Capture(t.Context(), dir, "go")
	if err != nil {
		t.Fatal(err)
	}
	p := Select(s, indexFor(s), "")
	plan := filepath.Join(dir, ".local/plan.json")
	if err := WriteJSON(plan, p); err != nil {
		t.Fatal(err)
	}
	flags := os.Getenv("GOFLAGS")
	t.Setenv("GOFLAGS", strings.TrimSpace(flags+" -p=1"))
	if err := SamePlan(t.Context(), dir, "go", p); err != nil {
		t.Fatal("parallel budget invalidated semantic plan", err)
	}
	t.Setenv("GOFLAGS", strings.TrimSpace(flags+" -tags=impact_extra"))
	if err := SamePlan(t.Context(), dir, "go", p); !errors.Is(err, ErrConfigurationChanged) {
		t.Fatal("changed tags accepted", err)
	}
	cmd := exec.CommandContext(t.Context(), runner, "-group", "other", "-plan", plan, "-count", "1", "./...")
	cmd.Dir = dir
	b, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(b), "extra root ran") {
		t.Fatalf("stale profile omitted newly enabled root: %v\n%s", err, b)
	}
}
