package impact

import (
	"path/filepath"
	"testing"
)

func TestOrdinaryFullEvidenceRequiresRunAndSuccessfulTerminalEvents(t *testing.T) {
	dir := t.TempDir()
	s := Snapshot{Packages: map[string]Package{"m/p": {Roots: []string{"TestA", "Example"}}}}
	fixtureWrite(t, dir, "race-other-1/all.jsonl", `{"Action":"run","Package":"m/p","Test":"TestA"}
{"Action":"pass","Package":"m/p","Test":"TestA","Elapsed":0.1}
{"Action":"pass","Package":"m/p"}
`)
	if _, err := FullExecutions(dir, s); err == nil {
		t.Fatal("package success hid a missing root")
	}
	fixtureWrite(t, dir, "race-other-1/example.jsonl", `{"Action":"run","Package":"m/p","Test":"Example"}
{"Action":"skip","Package":"m/p","Test":"Example"}
`)
	rows, err := FullExecutions(dir, s)
	if err != nil || len(rows) != 2 {
		t.Fatal(rows, err)
	}
	fixtureWrite(t, dir, filepath.Join("race-other-2", "repeat.jsonl"), `{"Action":"run","Package":"m/p","Test":"TestA"}`)
	if _, err := FullExecutions(dir, s); err == nil {
		t.Fatal("repeated root accepted as one complete run")
	}
}

func TestProcessFallbackRequiresOrdinaryExecutionAndNeverLaunchesTheRoot(t *testing.T) {
	dir := fixtureRepo(t)
	fixtureWrite(t, dir, "launch_test.go", "package fixture\nimport(\"os/exec\";\"testing\")\nfunc TestDetached(t *testing.T){_ = exec.Command(\"unexpected\");t.Fatal(\"must use ordinary full evidence\")}\n")
	if _, err := Collect(t.Context(), CollectOptions{Dir: dir, Jobs: 1}); err == nil {
		t.Fatal("process fallback without full evidence was accepted")
	}
	s, err := Capture(t.Context(), dir, "go")
	if err != nil {
		t.Fatal(err)
	}
	var full []Execution
	for _, root := range s.Packages[s.Module].Roots {
		full = append(full, Execution{Package: s.Module, Root: root, Mode: "full-pass", Seconds: 1})
	}
	x, err := Collect(t.Context(), CollectOptions{Dir: dir, Jobs: 1, FullExecutions: full})
	if err != nil {
		t.Fatal(err)
	}
	if len(x.Executions) != len(full) || x.CompileSeconds != 0 || x.RootSeconds != 0 {
		t.Fatal("process package was independently instrumented", x)
	}
	for _, row := range x.Executions {
		if row.Mode != "static-process" || len(row.Functions) > 0 {
			t.Fatal(row)
		}
	}
}
