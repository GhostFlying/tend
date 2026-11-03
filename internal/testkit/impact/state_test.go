package impact

import (
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestSharedStateCounterfactualKeepsTheOtherFailingRoot(t *testing.T) {
	dir := fixtureRepo(t)
	base := "package fixture\nvar value int\nfunc A(n int)int{value=1;return 1}\nfunc B()int{return value}\n"
	fixtureWrite(t, dir, "value.go", base)
	fixtureWrite(t, dir, "value_test.go", "package fixture\nimport \"testing\"\nfunc TestA(t *testing.T){if A(1)!=1{t.Fatal(\"a\")}}\nfunc TestB(t *testing.T){if B()>1{t.Fatal(\"shared state\")}}\n")
	fixtureCommit(t, dir)
	if _, err := output(t.Context(), dir, "go", "test", "-race", "-count=1", "./..."); err != nil {
		t.Fatal(err)
	}
	idx, err := Collect(t.Context(), CollectOptions{Dir: dir, Jobs: 2, Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	idx.VerifiedFull = true
	indexPath := filepath.Join(dir, ".local/index.json")
	if err := WriteJSON(indexPath, idx); err != nil {
		t.Fatal(err)
	}
	fixtureWrite(t, dir, "value.go", "package fixture\nvar value int\nfunc A(n int)int{value=2;return 1}\nfunc B()int{return value}\n")
	p, err := PlanChecks(t.Context(), dir, "go", indexPath, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(p.Go[idx.Source.Module], "TestB") {
		t.Fatal("lost the root that observes another root's state", p)
	}
	cmd := exec.CommandContext(t.Context(), "go", "test", "-race", "-count=1", "-json", "./...")
	cmd.Dir = dir
	data, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("state mutant unexpectedly passed")
	}
	fixtureWrite(t, dir, ".local/events/race-full/out.jsonl", string(data))
	failed, _, err := failureEvents(filepath.Join(dir, ".local/events"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(failed, FailureRoot{idx.Source.Module, "TestB"}) {
		t.Fatal("no actual full-suite counterexample", failed)
	}
	for _, root := range failed {
		if !slices.Contains(p.Go[root.Package], root.Root) {
			t.Fatal("omitted failure", root)
		}
	}
}
