package impact

import (
	"path/filepath"
	"reflect"
	"testing"
)

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
