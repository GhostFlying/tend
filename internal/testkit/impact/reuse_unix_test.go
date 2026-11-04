//go:build !windows

package impact

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestShadowReusesEquivalentLocalWorkloadWithoutWideningBrowser(t *testing.T) {
	tools, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	checker := filepath.Join(t.TempDir(), "check")
	fixtureCommand(t, tools, "go", "build", "-o", checker, "../check")
	dir := fixtureRepo(t)
	fixtureWrite(t, dir, "inspection_test.go", "package fixture\nimport \"os\"\nvar _ = os.ReadFile\n")
	fixtureWrite(t, dir, "web/package.json", `{"scripts":{"test:one":"node scripts/one.ts","test:two":"node scripts/two.ts"}}`)
	fixtureWrite(t, dir, "web/scripts/layout-groups.ts", `const groups=["test:one","test:two"]`)
	fixtureWrite(t, dir, "web/scripts/one.ts", "// one\n")
	fixtureWrite(t, dir, "web/scripts/two.ts", "// two\n")
	fixtureWrite(t, dir, "web/src/inspection.test.ts", "import {readFileSync} from 'node:fs';\n")
	fixtureWrite(t, dir, "Makefile", "go-check web-check:\n\t@true\ntest:\n\t@if [ \"$(TEST_GROUP)\" = other ]; then echo ran >> .local/runs; go test -race -count=1 -json ./...; fi\n")
	fixtureCommit(t, dir)
	before, err := Capture(t.Context(), dir, "go")
	if err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(dir, ".local/index.json")
	if err := WriteJSON(index, indexFor(before)); err != nil {
		t.Fatal(err)
	}
	fixtureWrite(t, dir, "web/scripts/one.ts", "// changed one\n")
	plan, err := PlanChecks(t.Context(), dir, "go", index, "", "")
	if err != nil || plan.Full || !plan.LocalFull || !slices.Equal(plan.Layout, []string{"test:one"}) || plan.Cross {
		t.Fatalf("local workload equivalence widened independent CI scope: %+v %v", plan, err)
	}
	refuse := filepath.Join(dir, ".local/refuse-selected")
	if err := os.WriteFile(refuse, []byte("#!/bin/sh\necho selected-must-not-start >&2\nexit 71\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"shadow", "affected"} {
		report := filepath.Join(dir, ".local/"+mode+".json")
		var log bytes.Buffer
		err := Command(t.Context(), tools, &log, nil, checker, "-dir", dir, "-mode", mode, "-index", index, "-report", report, "-race-runner", refuse, "-jobs", "1", "-count", "1")
		if err != nil {
			t.Fatal(err, log.String())
		}
		var result Result
		if err := readJSON(report, &result); err != nil || !result.Valid || !result.Passed || !result.FullVerified || !result.SelectionDelegated {
			t.Fatal(result, err)
		}
		for _, root := range before.Packages[before.Module].Roots {
			event := `"Action":"run","Package":"example.test/fixture","Test":"` + root + `"`
			if bytes.Count(log.Bytes(), []byte(event)) != 1 {
				t.Fatalf("native root did not execute exactly once: %s\n%s", root, log.String())
			}
		}
		t.Logf("%s reused full native execution:\n%s", mode, log.String())
	}
	runs, err := os.ReadFile(filepath.Join(dir, ".local/runs"))
	if err != nil || string(runs) != "ran\nran\n" {
		t.Fatal("equivalent selected execution ran twice", string(runs), err)
	}
}
