package cli

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestIssueReuseCLIOriginAndCompleteRecoveryPayload(t *testing.T) {
	f := newFakeServer(t)
	e := newTestEnv(t, f.srv.URL)
	origin := e.IDs.New(protocol.PrefixRequest)
	request := e.IDs.New(protocol.PrefixRequest)
	f.answer("GET /api/v1/issues/wi_original/reuse-status", protocol.IssueReuseStatus{OriginalRequestID: origin, OriginObservation: "recorded"})
	if code := e.main("issue", "reuse-status", "wi_original", "--json"); code != exitOK {
		t.Fatal(code, e.errOut.String())
	}
	e.out.Reset()
	payload := `{"original_request_id":"` + origin + `","source_message_id":"msg_owner","expected_source_revision":2,"expected_association_revision":0,"reason":"Continue the same responsibility"}`
	f.on("POST /api/v1/ops", func(w http.ResponseWriter, _ *http.Request, body []byte) {
		var env protocol.Envelope
		_ = json.Unmarshal(body, &env)
		var p protocol.ReuseIssuePayload
		_ = json.Unmarshal(env.Payload, &p)
		if env.Operation != "issues.reuse" || env.RequestID != request || env.Target.ID != "wi_original" || env.ExpectedControlRevision == nil || *env.ExpectedControlRevision != 7 || p.ExpectedAssignmentEpoch != 3 || p.OriginalRequestID != origin || p.SourceMessageID != "msg_owner" || p.ExpectedSourceRevision == nil || *p.ExpectedSourceRevision != 2 || (p.ExpectedAssociationRevision == nil || *p.ExpectedAssociationRevision != 0) {
			t.Error(env, p)
		}
		_ = json.NewEncoder(w).Encode(protocol.APIResponse{Operation: &protocol.Operation{RequestID: env.RequestID, Status: protocol.OpSucceeded, Result: json.RawMessage(`{"reused":true}`)}})
	})
	if code := e.main("issue", "reuse", "wi_original", "--request-id", request, "--revision", "7", "--epoch", "3", "--data", payload); code != exitOK {
		t.Fatal(code, e.errOut.String())
	}
	if len(f.seen("GET /api/v1/issues/wi_original")) != 0 || len(f.seen("POST /api/v1/ops")) != 1 {
		t.Fatal("recovery inferred new source or responsibility")
	}
}
