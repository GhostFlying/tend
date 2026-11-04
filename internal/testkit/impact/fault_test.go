package impact

import (
	"bytes"
	"path/filepath"
	"reflect"
	"testing"
)

func TestFaultAuditRejectsPassingExpectedRootDespiteUnrelatedNativeFailure(t *testing.T) {
	dir := fixtureRepo(t)
	fixtureWrite(t, dir, "audit_test.go", "package fixture\nimport(\"os\";\"testing\")\nfunc TestExpected(t *testing.T){if os.Getenv(\"FAULT_FULL\")==\"1\"{t.Fatal(\"expected mutant\")}}\nfunc TestUnrelated(t *testing.T){t.Fatal(\"unrelated failure\")}\n")
	var full, selectedFailed, selectedRan []FailureRoot
	for _, mode := range []string{"full", "selected"} {
		t.Setenv("FAULT_FULL", map[string]string{"full": "1", "selected": "0"}[mode])
		var log bytes.Buffer
		if err := Command(t.Context(), dir, &log, nil, "go", "test", "-count=1", "-json", "-run=^Test(Expected|Unrelated)$", "./..."); err == nil {
			t.Fatal("native counterexample unexpectedly passed")
		}
		records := t.TempDir()
		fixtureWrite(t, records, "race-all-1/native.jsonl", log.String())
		failed, ran, err := failureEvents(records)
		if err != nil {
			t.Fatal(err)
		}
		if mode == "full" {
			full = failed
		} else {
			selectedFailed, selectedRan = failed, ran
		}
	}
	plan := Plan{Go: map[string][]string{"example.test/fixture": {"TestExpected", "TestUnrelated"}}}
	want := []FailureRoot{{"example.test/fixture", "TestExpected"}}
	if got := missingFailureRoots(plan, full, selectedFailed, selectedRan); !reflect.DeepEqual(got, want) {
		t.Fatal("unrelated failure hid passing expected root", got)
	}
	if got := missingFailureRoots(plan, full, full, selectedRan); len(got) != 0 {
		t.Fatal("matching native failures rejected", got)
	}
}

func TestFailureEventsAttributeNestedFailuresToAllExecutedRoots(t *testing.T) {
	dir := t.TempDir()
	fixtureWrite(t, dir, "race-app-1/shard.jsonl", `{"Action":"run","Package":"m/app","Test":"TestRule"}
{"Action":"run","Package":"m/app","Test":"TestRule/branch"}
{"Action":"fail","Package":"m/app","Test":"TestRule/branch"}
{"Action":"fail","Package":"m/app","Test":"TestRule"}
{"Action":"fail","Package":"m/app"}
{"Action":"run","Package":"m/app","Test":"Example"}
{"Action":"pass","Package":"m/app","Test":"Example"}
`)
	failed, ran, err := failureEvents(dir)
	if err != nil || !reflect.DeepEqual(failed, []FailureRoot{{"m/app", "TestRule"}}) || len(ran) != 2 {
		t.Fatal(failed, ran, err)
	}
	fixtureWrite(t, dir, filepath.Join("race-app-2", "bad.jsonl"), "invalid native report")
	if _, _, err := failureEvents(dir); err == nil {
		t.Fatal("corrupt report accepted")
	}
}
