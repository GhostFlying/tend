package cli

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestIssueMaterialsCLIForwardsTypedFiltersAndPreservesHistory(t *testing.T) {
	f := newFakeServer(t)
	e := newTestEnv(t, f.srv.URL)
	want := map[string]any{"issue_ids": []any{"original-issue"}, "kind": "files", "seq": float64(42), "observed_at": float64(1234), "files": []any{map[string]any{"file_id": "original-file", "size": float64(12), "sha256": strings.Repeat("a", 64), "sources": []any{map[string]any{"kind": "background", "issue_id": "original-issue"}}}}, "more": true, "next": "original-cursor"}
	f.on("GET /api/v1/issues/original-issue/materials", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		for k, expected := range map[string]string{"kind": "files", "id": "original-file", "after": "original-cursor", "limit": "1"} {
			if r.URL.Query().Get(k) != expected {
				t.Error("lost source selector or page cursor", k, r.URL.RawQuery)
			}
		}
		_ = json.NewEncoder(w).Encode(want)
	})
	if code := e.main("issue", "materials", "original-issue", "--kind", "files", "--id", "original-file", "--after", "original-cursor", "--limit", "1", "--json"); code != exitOK {
		t.Fatal(code, e.errOut.String())
	}
	var got map[string]any
	if err := json.Unmarshal(e.out.Bytes(), &got); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("CLI rewrote source, history or pagination", got, err)
	}
	if len(f.seen("POST /api/v1/ops")) != 0 {
		t.Fatal("materials read performed a write")
	}
	usage := commandUsage([]string{"issue", "materials"})
	for _, field := range []string{"--kind", "--id", "--after", "--limit", "read", "original", "sources", "facts", "superseded", "sources_more", "capabilities", "agent_capabilities", "agent_capability_history", "task_capabilities", "environment/agent", "retired"} {
		if !strings.Contains(strings.ToLower(usage), field) {
			t.Fatal("missing useful material-read help", field, usage)
		}
	}
}

func TestIssueMaterialsCLICapabilityViewsAndQueryValidation(t *testing.T) {
	for _, kind := range []string{"capabilities", "agent_capabilities", "agent_capability_history", "task_capabilities"} {
		t.Run(kind, func(t *testing.T) {
			f := newFakeServer(t)
			e := newTestEnv(t, f.srv.URL)
			id := "environment/agent"
			if kind == "capabilities" {
				id = "image_gen"
			} else if kind == "task_capabilities" {
				id = "task_01J00000000000000000000000"
			}
			f.on("GET /api/v1/issues/original/materials", func(w http.ResponseWriter, r *http.Request, _ []byte) {
				if r.URL.Query().Get("kind") != kind || r.URL.Query().Get("id") != id || r.URL.Query().Get("after") != "preserved-page" {
					t.Error("capability source identity or cursor changed", r.URL.RawQuery)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"kind": kind, "more": false, "next": "", "capabilities": map[string]any{"items": []any{}, "as_of": 7}})
			})
			if code := e.main("issue", "materials", "original", "--kind", kind, "--id", id, "--after", "preserved-page", "--limit", "2", "--json"); code != exitOK {
				t.Fatal(code, e.errOut.String())
			}
		})
	}
	for _, flags := range [][]string{{"--limit", "0"}, {"--limit", "101"}, {"--kind", "not-a-kind"}, {"--data", `{}`}} {
		f := newFakeServer(t)
		e := newTestEnv(t, f.srv.URL)
		if code := e.main(append([]string{"issue", "materials", "original"}, flags...)...); code != exitUsage {
			t.Fatal("invalid read accepted", flags, code, e.errOut.String())
		}
		if len(f.seen("GET /api/v1/issues/original/materials")) != 0 || len(f.seen("POST /api/v1/ops")) != 0 {
			t.Fatal("invalid read performed an HTTP operation", flags)
		}
	}
}
