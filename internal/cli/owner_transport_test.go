package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestOwnerLostAnswerRequiresBusinessStateReconciliation(t *testing.T) {
	f := newFakeServer(t)
	original := protocol.Envelope{RequestID: "req_01ARZ3NDEKTSV4RRFFQ69G5FAV", Operation: "decisions.respond", Target: protocol.Target{Type: "decision", ID: "dec_original"}, Payload: json.RawMessage(`{"version":3,"option":"retry","text":"original choice"}`)}
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "original.json")
	if err := os.WriteFile(file, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	f.on("POST /api/v1/ops", func(w http.ResponseWriter, _ *http.Request, body []byte) {
		if !bytes.Equal(body, raw) {
			t.Error("original Envelope changed")
		}
		// The server has accepted and recorded this request before losing its reply.
		connection, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = connection.Close()
	})
	f.answer("GET /api/v1/decisions/"+original.Target.ID+"/continuation", protocol.DecisionContinuationView{DecisionID: original.Target.ID, State: "completed", DecisionVersion: 3})
	te := newTestEnv(t, f.srv.URL)
	if code := te.main("operation", "submit", "--file", file); code != exitError {
		t.Fatalf("lost reply exit=%d: %s", code, te.errOut)
	}
	if sent := f.seen("POST /api/v1/ops"); len(sent) != 1 {
		t.Fatalf("unknown write was automatically repeated: %d POSTs", len(sent))
	}
	if !strings.Contains(te.errOut.String(), "unknown_outcome") || !strings.Contains(te.errOut.String(), "decision-continuation show "+original.Target.ID) {
		t.Fatalf("missing original-request reconciliation: %s", te.errOut)
	}
	if code := te.main("decision-continuation", "show", original.Target.ID, "--json"); code != exitOK {
		t.Fatalf("read original request=%d: %s", code, te.errOut)
	}
	var view protocol.DecisionContinuationView
	if err := json.Unmarshal(te.out.Bytes(), &view); err != nil || view.DecisionID != original.Target.ID || view.State != "completed" {
		t.Fatalf("original accepted result unavailable: %+v %v", view, err)
	}
	if len(f.seen("POST /api/v1/ops")) != 1 {
		t.Fatal("reconciliation submitted another write")
	}
}

func TestOwnerEnvelopeSuccessAndRejectionStaySingleAttempt(t *testing.T) {
	for _, reject := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "rejected"}[reject], func(t *testing.T) {
			f := newFakeServer(t)
			response := protocol.APIResponse{Operation: &protocol.Operation{RequestID: "req_01ARZ3NDEKTSV4RRFFQ69G5FAV", Status: protocol.OpSucceeded}}
			if reject {
				response.Operation.Status = protocol.OpFailed
				response.Operation.Error = protocol.Errorf(protocol.CodeStaleRevision, "original revision changed")
			}
			f.answer("POST /api/v1/ops", response)
			te := newTestEnv(t, f.srv.URL)
			code := te.main("operation", "submit", "--data", `{"request_id":"req_01ARZ3NDEKTSV4RRFFQ69G5FAV","operation":"decisions.respond","target":{"type":"decision","id":"dec_original"},"payload":{"version":3,"option":"retry"}}`)
			want := exitOK
			if reject {
				want = exitError
			}
			if code != want || len(f.seen("POST /api/v1/ops")) != 1 {
				t.Fatalf("exit=%d want=%d calls=%d: %s", code, want, len(f.seen("POST /api/v1/ops")), te.errOut)
			}
			if reject && (!strings.Contains(te.errOut.String(), "stale_revision") || strings.Contains(te.errOut.String(), "unknown_outcome")) {
				t.Fatalf("business rejection changed: %s", te.errOut)
			}
		})
	}
}

func TestOwnerCopiedWritePathsDoNotRepeatUnknownSubmission(t *testing.T) {
	for _, args := range [][]string{
		{"repo", "register", "new", "--revision", "4", "--data", `{"remote_url":"https://example.com/repo.git"}`},
		{"category", "put", "review", "--revision", "4", "--data", `{"title":"Review","state":"active"}`},
		{"agents", "concurrency", "update", "pool", "--revision", "4", "--data", `{"name":"pool","min":1,"initial":1,"max":2}`},
	} {
		t.Run(args[0], func(t *testing.T) {
			f := newFakeServer(t)
			te := newTestEnv(t, f.srv.URL)
			if code := te.main(append(append([]string(nil), args...), "--dry-run")...); code != exitOK {
				t.Fatal(code, te.errOut)
			}
			saved := append([]byte(nil), te.out.Bytes()...)
			var original protocol.Envelope
			if err := json.Unmarshal(saved, &original); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(t.TempDir(), "original.json")
			if err := os.WriteFile(file, saved, 0o600); err != nil {
				t.Fatal(err)
			}
			te.out.Reset()
			te.errOut.Reset()
			f.on("POST /api/v1/ops", func(w http.ResponseWriter, _ *http.Request, body []byte) {
				var sent protocol.Envelope
				if err := json.Unmarshal(body, &sent); err != nil || sent.RequestID != original.RequestID || !sameCapabilityPayload(t, &sent, &original) {
					t.Error("frozen Envelope changed", sent, err)
				}
				connection, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_ = connection.Close()
			})
			readArgs := map[string][]string{
				"repo":     {"repo", "show", "new", "--json"},
				"category": {"category", "show", "review", "--json"},
				"agents":   {"agents", "concurrency", "show", "pool", "--json"},
			}[args[0]]
			readPath := map[string]string{"repo": "/api/v1/repos/new", "category": "/api/v1/categories/review", "agents": "/api/v1/concurrency-groups/pool"}[args[0]]
			f.answer("GET "+readPath, map[string]any{"id": original.Target.ID, "repo": original.Target.ID, "category_id": original.Target.ID, "revision": 5, "control_revision": 5})
			actual := append(append([]string(nil), args...), "--request-id", original.RequestID)
			if code := te.main(actual...); code != exitError || len(f.seen("POST /api/v1/ops")) != 1 || !strings.Contains(te.errOut.String(), "unknown_outcome") || !strings.Contains(te.errOut.String(), original.RequestID) {
				t.Fatalf("unknown write exit=%d calls=%d: %s", code, len(f.seen("POST /api/v1/ops")), te.errOut)
			}
			if code := te.main(readArgs...); code != exitOK {
				t.Fatal(code, te.errOut)
			}
			var view map[string]any
			if err := json.Unmarshal(te.out.Bytes(), &view); err != nil || view[map[string]string{"repo": "repo", "category": "category_id", "agents": "id"}[args[0]]] != original.Target.ID || len(f.seen("GET "+readPath)) != 1 {
				t.Fatal(view, err)
			}
			stillSaved, err := os.ReadFile(file)
			if err != nil || !bytes.Equal(saved, stillSaved) || len(f.seen("POST /api/v1/ops")) != 1 {
				t.Fatal("reconciliation changed the original Envelope or submitted again", err)
			}
		})
	}
}
