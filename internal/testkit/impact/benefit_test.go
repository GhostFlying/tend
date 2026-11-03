package impact

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestReplayPreparationDoesNotRunFailingTestRoots(t *testing.T) {
	dir := fixtureRepo(t)
	fixtureWrite(t, dir, "fail_test.go", "package fixture\nimport \"testing\"\nfunc TestPreparationMustNotRun(t *testing.T){t.Fatal(\"test root executed\")}\n")
	prepared, err := prepareReplay(t.Context(), dir, "go", io.Discard)
	if err != nil || prepared["go_compile"].Seconds <= 0 || prepared["go_race_compile"].Seconds <= 0 {
		t.Fatal(prepared, err)
	}
	if _, err := output(t.Context(), dir, "go", "test", "-race", "-count=1", "./..."); err == nil {
		t.Fatal("forced test execution unexpectedly passed")
	}
}

func TestBootstrapReuseRequiresMatchingProofAndDoesNotReusePairs(t *testing.T) {
	dir := fixtureRepo(t)
	s, err := Capture(t.Context(), dir, "go")
	if err != nil {
		t.Fatal(err)
	}
	idx := indexFor(s)
	idx.FullSeconds = 1
	idx.CollectionSeconds = 2
	previousDir := t.TempDir()
	if err := WriteJSON(filepath.Join(previousDir, "baseline.json"), idx); err != nil {
		t.Fatal(err)
	}
	previous := BenefitReport{Format: Format, Manifest: ReplayManifest{Baseline: s.Commit}, Profile: s.Profile, CPUs: 4, CollectionJobs: 2, Initial: Measurement{Seconds: 3, FinishedAt: "completed"}, BaselineFullSeconds: 1, BaselineCollectionSeconds: 2, SetupSeconds: 5, ToolFingerprint: "previous-tool", Cases: []CaseBenefit{{Case: ReplayCase{PR: 1}}}}
	path := filepath.Join(previousDir, "benefit.json")
	if err := WriteJSON(path, previous); err != nil {
		t.Fatal(err)
	}
	current := BenefitReport{Manifest: previous.Manifest, Profile: s.Profile, CPUs: 4, CollectionJobs: 2, ToolFingerprint: "current-tool", SetupSeconds: 2}
	destination := filepath.Join(t.TempDir(), "baseline.json")
	if err := reuseBootstrap(t.Context(), dir, "go", destination, path, &current); err != nil {
		t.Fatal(err)
	}
	if current.Initial.Seconds != 3 || current.SetupSeconds != 7 || len(current.Cases) != 0 || current.BootstrapToolFingerprint != "previous-tool" || current.ToolFingerprint != "current-tool" {
		t.Fatal(current)
	}
	b, err := os.ReadFile(destination)
	if err != nil || hash(b) != current.BootstrapIndexFingerprint {
		t.Fatal("bootstrap identity lost", err)
	}
	current.CPUs = 8
	if err := reuseBootstrap(t.Context(), dir, "go", destination, path, &current); err == nil {
		t.Fatal("changed CPU budget reused")
	}
	current.CPUs = 4
	fixtureWrite(t, dir, "value.go", "package fixture\nfunc A(n int)int{return 9}\nfunc B()int{return 2}\n")
	if err := reuseBootstrap(t.Context(), dir, "go", destination, path, &current); err == nil {
		t.Fatal("changed source reused")
	}
}

func TestReplayPairsRetainsFailureAndEveryAlternatingObservation(t *testing.T) {
	var c CaseBenefit
	var modes []string
	saved := 0
	err := replayPairs(t.Context(), &c, 7, true, func(mode string, round int) (Measurement, error) {
		modes = append(modes, mode)
		return Measurement{Success: !(round == 0 && mode == "affected"), Seconds: 1, Error: "retained test failure"}, nil
	}, func() error { saved++; return nil })
	if err != nil || saved != 6 || len(c.Pairs) != 3 || !reflect.DeepEqual(modes, []string{"full", "affected", "affected", "full", "full", "affected"}) || c.Pairs[0].Selected.Success || c.Pairs[0].Selected.Error == "" || !c.Pairs[2].Selected.Success {
		t.Fatal(c, modes, saved, err)
	}
	var invalid CaseBenefit
	err = replayPairs(t.Context(), &invalid, 7, true, func(string, int) (Measurement, error) { return Measurement{}, errors.New("source changed") }, func() error { return nil })
	if err == nil || len(invalid.Pairs) != 1 || invalid.Pairs[0].Selected.Seconds != 0 {
		t.Fatal(invalid, err)
	}
}

func TestMeasurementRequiresFreshMatchingReceiptEvenWhenCommandFails(t *testing.T) {
	m, commandErr := measured(t.Context(), t.TempDir(), io.Discard, "go", "definitely-not-a-go-command")
	if commandErr == nil || m.Success || m.Error == "" {
		t.Fatal(m, commandErr)
	}
	p := Plan{Commit: "fixed-candidate", Fingerprint: "fixed-source", Profile: Profile{Go: "fixed-go"}, Full: true}
	r := Result{Mode: "affected", Commit: p.Commit, Fingerprint: p.Fingerprint, Profile: p.Profile, Valid: true, Seconds: 1, Error: "actual failed test"}
	path := filepath.Join(t.TempDir(), "result.json")
	if _, err := checkMeasurement(m, commandErr, path, p, "affected"); err == nil {
		t.Fatal("missing receipt accepted")
	}
	if err := WriteJSON(path, r); err != nil {
		t.Fatal(err)
	}
	observed, err := checkMeasurement(m, commandErr, path, p, "affected")
	if err != nil || observed.Success || observed.Execution.Passed || observed.Error != r.Error {
		t.Fatal(observed, err)
	}
	r.Fingerprint = "stale-source"
	if err := WriteJSON(path, r); err != nil {
		t.Fatal(err)
	}
	if _, err := checkMeasurement(m, commandErr, path, p, "affected"); err == nil {
		t.Fatal("stale receipt accepted")
	}
	r.Fingerprint, r.Passed, r.FullVerified = p.Fingerprint, true, true
	if err := WriteJSON(path, r); err != nil {
		t.Fatal(err)
	}
	if _, err := checkMeasurement(m, commandErr, path, p, "affected"); err == nil {
		t.Fatal("success receipt contradicting exit failure accepted")
	}
}
