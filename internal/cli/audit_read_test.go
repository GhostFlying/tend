package cli

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestTaskWaitsFiltersLegacyServerResponse(t *testing.T) {
	const route = "GET /api/v1/tasks/sample/external-waits"
	// The original handler answered with the complete array regardless of
	// query parameters. Carry through future keys and original unknown values.
	retained := json.RawMessage(`{"run_id":"original","status":"waiting","stopped":true,"observed_state":"unknown","extension":null}`)
	earlier := json.RawMessage(`{"run_id":"earlier","status":"continued","stopped":true}`)
	f := newFakeServer(t)
	f.answer(route, []json.RawMessage{earlier, retained})
	for _, check := range []struct {
		name string
		args []string
		want []json.RawMessage
	}{
		{"full", nil, []json.RawMessage{earlier, retained}},
		{"run", []string{"--run", "original"}, []json.RawMessage{retained}},
		{"status", []string{"--status", "continued"}, []json.RawMessage{earlier}},
		{"intersection", []string{"--run", "original", "--status", "waiting"}, []json.RawMessage{retained}},
		{"no match", []string{"--run", "original", "--status", "continued"}, []json.RawMessage{}},
	} {
		t.Run(check.name, func(t *testing.T) {
			te := newTestEnv(t, f.srv.URL)
			if code := te.main(append([]string{"task", "waits", "sample"}, check.args...)...); code != exitOK {
				t.Fatal(code, te.errOut.String())
			}
			want, err := json.MarshalIndent(check.want, "", "  ")
			if err != nil || te.out.String() != string(want)+"\n" {
				t.Fatalf("old server response: got %s, want %s, err %v", te.out, want, err)
			}
		})
	}
	before := f.seen(route)
	for _, filter := range [][]string{{"--run", ""}, {"--status", "bogus"}} {
		te := newTestEnv(t, f.srv.URL)
		if code := te.main(append([]string{"task", "waits", "sample"}, filter...)...); code != exitUsage {
			t.Fatalf("invalid filter %v: %d %s", filter, code, te.out.String())
		}
	}
	if after := f.seen(route); !reflect.DeepEqual(after, before) {
		t.Fatal("invalid selection contacted the old endpoint")
	}
}
