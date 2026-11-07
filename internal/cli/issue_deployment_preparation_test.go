package cli

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestIssueDeploymentPreparationPreservesCompleteRead(t *testing.T) {
	f := newFakeServer(t)
	const route = "GET /api/v1/issues/issue-a/deployment-preparation"
	want := map[string]any{
		"actual_server": map[string]any{"status": "unknown"},
		"preparation": map[string]any{
			"source": "recorded_deployment", "observed_at": float64(1234), "seq": float64(27),
			"host_ids": []any{"host-a", "host-b"}, "target_complete": true,
			"hosts":   []any{map[string]any{"host_id": "host-a", "runner": map[string]any{"control_revision": float64(8), "applied_control_revision": float64(7)}, "installation_status": "unknown"}},
			"missing": []any{"host-a:installation_receipt"}, "complete": false,
			"future_optional_fact": map[string]any{"known": false, "reason": "not_observed"},
		},
	}
	f.answer(route, want)
	e := newTestEnv(t, f.srv.URL)
	for _, format := range [][]string{nil, {"--json"}, {"--json=false"}} {
		e.out.Reset()
		if code := e.main(append([]string{"issue", "deployment-preparation", "issue-a"}, format...)...); code != 0 {
			t.Fatalf("read failed: %d %s", code, e.errOut)
		}
		var got map[string]any
		if err := json.Unmarshal(e.out.Bytes(), &got); err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("read lost provenance or unknown fields: %s, %v", e.out, err)
		}
	}
	reads := f.seen(route)
	if len(reads) != 3 {
		t.Fatalf("read requests: %+v", reads)
	}
	for _, r := range reads {
		if r.auth != "Bearer owner-token" || len(r.body) != 0 {
			t.Fatalf("unexpected read authority/body: %+v", r)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) != len(reads) {
		t.Fatalf("read issued extra requests: %+v", f.requests)
	}
}

func TestIssueDeploymentPreparationValidatesBeforeHTTP(t *testing.T) {
	f := newFakeServer(t)
	for _, args := range [][]string{
		nil, {""}, {" "}, {"issue-a", "issue-b"},
		{"issue-a", "--host", "host-b"}, {"issue-a", "--provider", "github"},
		{"issue-a", "--request-id", "request-a"}, {"issue-a", "--revision", "1"},
		{"issue-a", "--data", "{}"}, {"issue-a", "--dry-run"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			e := newTestEnv(t, f.srv.URL)
			if code := e.main(append([]string{"issue", "deployment-preparation"}, args...)...); code != exitUsage {
				t.Fatalf("invalid read input: exit=%d stdout=%s stderr=%s", code, e.out, e.errOut)
			}
		})
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) != 0 {
		t.Fatalf("invalid read reached server: %+v", f.requests)
	}
}

func TestIssueDeploymentPreparationHelpAndCapability(t *testing.T) {
	e := newTestEnv(t, "")
	if code := e.main("issue", "deployment-preparation", "--help"); code != exitOK {
		t.Fatalf("help needs no login: %d %s", code, e.errOut)
	}
	for _, text := range []string{"current fixed server", "active hosts", "unknown", "read-only", "-json"} {
		if !strings.Contains(strings.ToLower(e.out.String()), text) {
			t.Errorf("help missing %q: %s", text, e.out)
		}
	}
	for _, binding := range CapabilityBindings() {
		if binding.Command == "issue deployment-preparation" && binding.Capability == "GET /api/v1/issues/{id}/deployment-preparation" {
			return
		}
	}
	t.Fatal("deployment preparation is absent from capability coverage")
}
