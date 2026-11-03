package cli

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestConcurrencyCLICompleteUpdateRetryAndDryRun(t *testing.T) {
	f := newFakeServer(t)
	f.answer("GET /api/v1/concurrency-groups/shared", protocol.ConcurrencyGroup{ID: "shared", Name: "Shared", Revision: 7, Min: 1, Initial: 3, Max: 8, Current: 4})
	f.on("POST /api/v1/ops", func(w http.ResponseWriter, _ *http.Request, raw []byte) {
		var env protocol.Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Error(err)
		}
		json.NewEncoder(w).Encode(protocol.APIResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded, Result: json.RawMessage(`{}`)}})
	})
	te := newTestEnv(t, f.srv.URL)
	if code := te.main("agents", "concurrency", "update", "shared", "--max", "9"); code != exitOK {
		t.Fatal(code, te.errOut)
	}
	var original protocol.Envelope
	json.Unmarshal(f.seen("POST /api/v1/ops")[0].body, &original)
	var p protocol.ConfigureConcurrencyPayload
	json.Unmarshal(original.Payload, &p)
	if p.Max != 9 || p.Min != 1 || p.Initial != 3 || p.Name != "Shared" || *original.ExpectedControlRevision != 7 {
		t.Fatal(original, p)
	}
	if code := te.main("agents", "concurrency", "update", "shared", "--data", string(original.Payload), "--request-id", original.RequestID, "--revision", "7"); code != exitOK {
		t.Fatal(code, te.errOut)
	}
	var repeated protocol.Envelope
	json.Unmarshal(f.seen("POST /api/v1/ops")[1].body, &repeated)
	a, _ := protocol.PayloadHash(&original)
	b, _ := protocol.PayloadHash(&repeated)
	if a != b || repeated.RequestID != original.RequestID {
		t.Fatal("retry changed original")
	}
	if code := te.main("agents", "concurrency", "update", "shared", "--max", "9", "--request-id", "old", "--revision", "7"); code != exitUsage {
		t.Fatal("partial retry could reread changed defaults", code)
	}
	before := len(f.seen("POST /api/v1/ops"))
	if code := te.main("agents", "concurrency", "bind", "shared", "--environment", "host", "--runtime", "worker", "--dry-run"); code != exitOK {
		t.Fatal(code, te.errOut)
	}
	if len(f.seen("POST /api/v1/ops")) != before {
		t.Fatal("dry-run wrote")
	}
}
func TestConcurrencyCLIHistoryPassesFiltersAndCursor(t *testing.T) {
	f := newFakeServer(t)
	f.on("GET /api/v1/concurrency-history", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		q := r.URL.Query()
		for key, want := range map[string]string{"group_id": "shared", "issue_id": "issue", "session_id": "native", "environment_id": "host", "runtime_id": "worker", "reason": "congestion", "since": "123", "until": "456", "after": "10", "limit": "7"} {
			if q.Get(key) != want {
				t.Errorf("%s=%q", key, q.Get(key))
			}
		}
		json.NewEncoder(w).Encode(protocol.ConcurrencyHistoryPage{Items: []protocol.ConcurrencyChange{}})
	})
	te := newTestEnv(t, f.srv.URL)
	if code := te.main("agents", "concurrency", "history", "--group", "shared", "--issue", "issue", "--session", "native", "--environment", "host", "--runtime", "worker", "--cause", "congestion", "--since", "123", "--until", "456", "--after", "10", "--limit", "7"); code != exitOK {
		t.Fatal(code, te.errOut)
	}
}

func TestConcurrencyCLIBindingRetryDoesNotReadNewDefaults(t *testing.T) {
	f := newFakeServer(t)
	f.answer("GET /api/v1/concurrency-groups/shared", protocol.ConcurrencyGroup{ID: "shared", Name: "Shared", Revision: 7, Min: 1, Initial: 3, Max: 8, Current: 4})
	f.on("POST /api/v1/ops", func(w http.ResponseWriter, _ *http.Request, raw []byte) {
		var env protocol.Envelope
		json.Unmarshal(raw, &env)
		json.NewEncoder(w).Encode(protocol.APIResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded, Result: json.RawMessage(`{}`)}})
	})
	te := newTestEnv(t, f.srv.URL)
	args := []string{"agents", "concurrency", "bind", "shared", "--environment", "host", "--runtime", "worker"}
	if code := te.main(args...); code != exitOK {
		t.Fatal(code, te.errOut)
	}
	var original protocol.Envelope
	json.Unmarshal(f.seen("POST /api/v1/ops")[0].body, &original)
	if code := te.main(append(args, "--request-id", original.RequestID, "--revision", "7")...); code != exitOK {
		t.Fatal(code, te.errOut)
	}
	var repeated protocol.Envelope
	json.Unmarshal(f.seen("POST /api/v1/ops")[1].body, &repeated)
	a, _ := protocol.PayloadHash(&original)
	b, _ := protocol.PayloadHash(&repeated)
	if a != b {
		t.Fatal("binding replay changed payload", string(original.Payload), string(repeated.Payload))
	}
}
