package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestCreatorAcceptancePolicyCLIUsesNamedControlAndExactReplay(t *testing.T) {
	f := newFakeServer(t)
	te := newTestEnv(t, f.srv.URL)
	input := `{"required":false,"reason":"A bounded investigation only needs its durable report"}`
	if code := te.main("issue", "set-acceptance-policy", "wi_target", "--revision", "7", "--epoch", "3", "--data", input, "--dry-run"); code != exitOK {
		t.Fatal(code, te.errOut.String())
	}
	var original protocol.Envelope
	if err := json.Unmarshal(te.out.Bytes(), &original); err != nil {
		t.Fatal(err)
	}
	if original.Operation != "issues.set_acceptance_policy" || original.ExpectedControlRevision == nil || *original.ExpectedControlRevision != 7 {
		t.Fatal(original)
	}
	var payload protocol.SetIssueAcceptancePolicyPayload
	if err := json.Unmarshal(original.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Required || payload.ExpectedAssignmentEpoch != 3 || payload.Reason == "" || len(f.requests) != 0 {
		t.Fatal(payload, f.requests)
	}
	path := filepath.Join(t.TempDir(), "original.json")
	if err := os.WriteFile(path, te.out.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	f.on("POST /api/v1/ops", func(w http.ResponseWriter, _ *http.Request, body []byte) {
		var received protocol.Envelope
		_ = json.Unmarshal(body, &received)
		before, _ := protocol.PayloadHash(&original)
		after, _ := protocol.PayloadHash(&received)
		if before != after || original.RequestID != received.RequestID {
			t.Error("policy retry changed its original identity or inputs")
		}
		_ = json.NewEncoder(w).Encode(protocol.APIResponse{Operation: &protocol.Operation{RequestID: received.RequestID, Status: protocol.OpSucceeded, Result: json.RawMessage(`{"issue_id":"wi_target"}`)}})
	})
	te.out.Reset()
	te.errOut.Reset()
	if code := te.main("operation", "submit", "--file", path); code != exitOK {
		t.Fatal(code, te.errOut.String())
	}
	if len(f.requests) != 1 {
		t.Fatal("policy retry refreshed control or retried implicitly", f.requests)
	}
	te.out.Reset()
	if code := te.main("issue", "set-acceptance-policy", "--help"); code != exitOK || !strings.Contains(te.out.String(), "future tasks only") || !strings.Contains(te.out.String(), "required") {
		t.Fatal(code, te.out.String())
	}
}
