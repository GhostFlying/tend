//go:build !windows

package impact

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestLocalModuleExecutableModeDriftRetainsActualFailure(t *testing.T) {
	dir, dependency := fixtureRepo(t), dependencyFixture(t, "1")
	script := filepath.Join(dependency, "check.sh")
	fixtureWrite(t, dependency, "check.sh", "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(script, 0o700); err != nil {
		t.Fatal(err)
	}
	fixtureWrite(t, dependency, "script.go", "package dependency\nimport _ \"embed\"\n//go:embed check.sh\nvar Script string\nfunc ScriptPath()string{return "+strconv.Quote(script)+"}\n")
	fixtureWrite(t, dir, "go.mod", "module example.test/fixture\n\ngo 1.27.1\nrequire example.test/dependency v0.0.0\nreplace example.test/dependency => "+strconv.Quote(filepath.ToSlash(dependency))+"\n")
	fixtureWrite(t, dir, "dependency_test.go", "package fixture\nimport(\"testing\";\"os/exec\";\"example.test/dependency\")\nfunc TestDependency(t *testing.T){if dependency.Script==\"\"{t.Fatal(\"missing embedded input\")};if err:=exec.Command(dependency.ScriptPath()).Run();err!=nil{t.Fatal(\"dependency executable failed\",err)}}\n")
	fixtureCommit(t, dir)
	before, err := Capture(t.Context(), dir, "go")
	if err != nil {
		t.Fatal(err)
	}
	var native bytes.Buffer
	if err := Command(t.Context(), dir, &native, nil, "go", "test", "-race", "-count=1", "-json", "./..."); err != nil {
		t.Fatal("ordinary full baseline failed", err, native.String())
	}
	reports := filepath.Join(dir, ".local/full")
	fixtureWrite(t, reports, "race-other-1/events.jsonl", native.String())
	records, err := FullExecutions(reports, before)
	if err != nil {
		t.Fatal(err)
	}
	index, err := Collect(t.Context(), CollectOptions{Dir: dir, Go: "go", FullExecutions: records})
	if err != nil {
		t.Fatal(err)
	}
	index.VerifiedFull = true
	path := filepath.Join(dir, ".local/index.json")
	if err := WriteJSON(path, index); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(script, 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := Capture(t.Context(), dir, "go")
	if err != nil || after.Fingerprint == before.Fingerprint || after.Profile.LocalModules == before.Profile.LocalModules {
		t.Fatal("permission-only input change retained its old identity", after, err)
	}
	if err := SameSnapshot(t.Context(), dir, "go", before); err == nil {
		t.Fatal("permission change retained a valid execution plan")
	}
	plan, err := PlanChecks(t.Context(), dir, "go", path, "", "")
	if err != nil || !plan.Full {
		t.Fatal(plan, err)
	}
	nativeSelectedFailure(t, dir, plan, "TestDependency")
}
