package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestIssueReuseOwnerHTTPOriginAndExactReplay(t *testing.T) {
	e := newEnv(t)
	e.configure()
	e.server.cfg.HostedMainAgent = true
	token := e.login(protocol.ScopeOwner)
	issue := createOwnerIssueHTTP(t, e, token, "")
	var origin protocol.IssueReuseStatus
	path := "/api/v1/issues/" + issue.IssueID + "/reuse-status"
	if code := e.do("GET", path, token, nil, &origin); code != 200 || origin.OriginObservation != "recorded" || origin.OriginalRequestID == "" {
		t.Fatal(code, origin)
	}
	if code := e.do("GET", path, "", nil, nil); code != http.StatusUnauthorized {
		t.Fatal(code)
	}
	code, submitted := e.op(token, e.envelope("conversation.submit_message", protocol.Target{Type: "conversation"}, nil, protocol.SubmitMessagePayload{Body: "This new input belongs to the original responsibility"}))
	if code != 200 || submitted.Operation == nil || submitted.Operation.Status != protocol.OpAccepted {
		t.Fatal(code, submitted)
	}
	var message struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(submitted.Operation.Result, &message); err != nil {
		t.Fatal(err)
	}
	var associations protocol.MessageAssociations
	if code := e.do("GET", "/api/v1/messages/"+message.MessageID+"/associations", token, nil, &associations); code != 200 {
		t.Fatal(code)
	}
	p := protocol.ReuseIssuePayload{IssueControlPayload: protocol.IssueControlPayload{IssueSourceReference: protocol.IssueSourceReference{SourceMessageID: message.MessageID, ExpectedSourceRevision: &associations.ContentRevision}, ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Explicitly reuse the original Issue"}, OriginalRequestID: origin.OriginalRequestID, ExpectedAssociationRevision: &associations.Revision}
	env := e.envelope("issues.reuse", protocol.Target{Type: "issue", ID: issue.IssueID}, &issue.Revision, p)
	code, result := e.op(token, env)
	if code != 200 || result.Operation == nil || result.Operation.Status != protocol.OpSucceeded {
		t.Fatal(code, result)
	}
	var reused protocol.IssueReuseResult
	if err := json.Unmarshal(result.Operation.Result, &reused); err != nil || reused.Issue.IssueID != issue.IssueID || !reused.Reused {
		t.Fatal(reused, err)
	}
	code, replay := e.op(token, env)
	if code != 200 || replay.Operation == nil || string(replay.Operation.Result) != string(result.Operation.Result) {
		t.Fatal(code, replay)
	}
}
