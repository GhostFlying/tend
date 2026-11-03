package cli

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestQuestionInputsCLIExactMetadataCASAndOriginalRequest(t *testing.T) {
	f := newFakeServer(t)
	e := newTestEnv(t, f.srv.URL)
	want := protocol.QuestionInputPage{DecisionID: "question", Revision: 0, QuestionVersion: 2, Original: &protocol.QuestionInputScope{RunID: "original-run", LeaseID: "original-lease"}, NextStep: "Relay exact metadata to the original worker", Grants: []protocol.QuestionInputGrant{}}
	f.on("GET /api/v1/decisions/question/inputs", func(w http.ResponseWriter, _ *http.Request, _ []byte) { _ = json.NewEncoder(w).Encode(want) })
	if code := e.main("question-inputs", "show", "question", "--json"); code != exitOK {
		t.Fatal(code, e.errOut.String())
	}
	var actual protocol.QuestionInputPage
	if err := json.Unmarshal(e.out.Bytes(), &actual); err != nil || !reflect.DeepEqual(actual, want) {
		t.Fatal(actual, err)
	}
	sourceRevision := int64(1)
	p := protocol.QuestionInputPayload{Version: 2, RunID: "original-run", LeaseID: "original-lease", FileIDs: []string{"original-file"}, IssueSourceReference: protocol.IssueSourceReference{SourceMessageID: "owner-reply", ExpectedSourceRevision: &sourceRevision}, Reason: "Owner supplies the exact question input"}
	data, _ := json.Marshal(p)
	request := "req_01J00000000000000000000000"
	f.on("POST /api/v1/ops", func(w http.ResponseWriter, _ *http.Request, body []byte) {
		var env protocol.Envelope
		if err := json.Unmarshal(body, &env); err != nil {
			t.Fatal(err)
		}
		var actual protocol.QuestionInputPayload
		if err := json.Unmarshal(env.Payload, &actual); err != nil || !reflect.DeepEqual(actual, p) || env.RequestID != request || env.Operation != "decisions.grant_inputs" || env.Target != (protocol.Target{Type: "decision", ID: "question"}) || env.ExpectedControlRevision == nil || *env.ExpectedControlRevision != 0 {
			t.Fatal(env, actual, err)
		}
		_ = json.NewEncoder(w).Encode(protocol.APIResponse{Operation: &protocol.Operation{RequestID: request, Status: protocol.OpSucceeded, Result: json.RawMessage(`{"revision":1,"grants":[]}`)}})
	})
	for range 2 {
		if code := e.main("question-inputs", "grant", "question", "--revision", "0", "--request-id", request, "--data", string(data), "--json"); code != exitOK {
			t.Fatal(code, e.errOut.String())
		}
	}
	calls := f.seen("POST /api/v1/ops")
	if len(calls) != 2 || string(calls[0].body) != string(calls[1].body) {
		t.Fatal("retry changed original payload", calls)
	}
}
