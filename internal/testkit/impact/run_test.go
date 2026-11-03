package impact

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConcurrentReportReplacementCannotNarrowExecution(t *testing.T) {
	tools, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	race := filepath.Join(t.TempDir(), "racecheck")
	if _, err := output(t.Context(), tools, "go", "build", "-o", race, "../racecheck"); err != nil {
		t.Fatal(err)
	}
	dir := fixtureRepo(t)
	fixtureWrite(t, dir, "Makefile", "go-check:\n\tcp .local/replaced.json .local/plan.json\n")
	fixtureWrite(t, dir, "fail_test.go", "package fixture\nimport \"testing\"\nfunc TestChanged(t *testing.T){t.Fatal(\"selected root executed\")}\n")
	s, err := Capture(t.Context(), dir, "go")
	if err != nil {
		t.Fatal(err)
	}
	p := Plan{Format: Format, Commit: s.Commit, Fingerprint: s.Fingerprint, Go: map[string][]string{s.Module: {"TestChanged"}}, Profile: s.Profile}
	path := filepath.Join(dir, ".local/plan.json")
	replaced := p
	replaced.Go = map[string][]string{}
	if err := WriteJSON(filepath.Join(dir, ".local/replaced.json"), replaced); err != nil {
		t.Fatal(err)
	}
	if err := WriteJSON(path, replaced); err != nil {
		t.Fatal(err)
	}
	if r, err := Run(t.Context(), RunOptions{Dir: dir, Go: "go", PlanPath: path}, p); err == nil || r.Passed {
		t.Fatal("mismatched report was accepted", r, err)
	}
	if err := WriteJSON(path, p); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	r, err := Run(t.Context(), RunOptions{Dir: dir, Go: "go", PlanPath: path, RaceRunner: race, Count: 1, Jobs: 1, Procs: 1, Packages: 1, Log: &log}, p)
	if err == nil || r.Passed || !r.Valid || !strings.Contains(log.String(), "selected root executed") {
		t.Fatal("shared plan replacement lost the required failure", r, err, log.String())
	}
}

func TestSelectedRaceRunHonorsConfiguredTimeout(t *testing.T) {
	tools, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	race := filepath.Join(t.TempDir(), "racecheck")
	if _, err := output(t.Context(), tools, "go", "build", "-o", race, "../racecheck"); err != nil {
		t.Fatal(err)
	}
	dir := fixtureRepo(t)
	fixtureWrite(t, dir, "Makefile", "go-check:\n\t@true\n")
	fixtureWrite(t, dir, "slow_test.go", "package fixture\nimport(\"testing\";\"time\")\nfunc TestSlow(t *testing.T){time.Sleep(3*time.Second)}\n")
	s, err := Capture(t.Context(), dir, "go")
	if err != nil {
		t.Fatal(err)
	}
	p := Plan{Format: Format, Commit: s.Commit, Fingerprint: s.Fingerprint, Go: map[string][]string{s.Module: {"TestSlow"}}, Layout: []string{}, Reasons: []string{}, Profile: s.Profile}
	planPath := filepath.Join(dir, ".local/plan.json")
	if err := WriteJSON(planPath, p); err != nil {
		t.Fatal(err)
	}
	log, err := os.CreateTemp(t.TempDir(), "run-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	t.Setenv("TEST_TIMEOUT", "30ms")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	r, err := Run(ctx, RunOptions{Dir: dir, Go: "go", PlanPath: planPath, RaceRunner: race, Count: 1, Jobs: 1, Procs: 2, Packages: 1, Log: log}, p)
	if err == nil || r.Passed || !r.Valid {
		t.Fatal(r, err)
	}
	b, err := os.ReadFile(log.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "test timed out after 30ms") {
		t.Fatalf("selected runner lost timeout policy: %s", b)
	}
}
