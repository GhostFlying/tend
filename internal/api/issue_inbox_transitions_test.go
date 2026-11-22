package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestIssueInboxLifecycleAndOriginalPersonAction(t *testing.T) {
	e := newEnv(t)
	e.configure()
	token := e.login(protocol.ScopeOwner)
	issue := admitOwnerIssueHTTP(t, e, token, createOwnerIssueHTTP(t, e, token, ""))
	other := createOwnerIssueHTTP(t, e, token, "")
	target := protocol.Target{Type: "issue", ID: issue.IssueID}

	read := func(id, filter string) protocol.AttentionPage {
		t.Helper()
		var p protocol.AttentionPage
		if status := e.do("GET", "/api/v1/inbox?issue_id="+id+"&"+filter, token, nil, &p); status != http.StatusOK {
			t.Fatal(status, p)
		}
		return p
	}
	var a protocol.Attention
	status, res := e.op(token, e.envelope("attentions.create", protocol.Target{Type: "attention"}, nil,
		protocol.CreateAttentionPayload{IssueID: issue.IssueID, Kind: "decision", Title: "Continue this Issue?", Options: []string{"Continue"}, BlocksClose: true}))
	if status != http.StatusOK || res.Operation == nil || res.Operation.Status != protocol.OpSucceeded || json.Unmarshal(res.Operation.Result, &a) != nil {
		t.Fatal(status, res)
	}
	if p := read(issue.IssueID, "actionable_only=true&status=open"); len(p.Items) != 1 || p.Items[0].AttentionID != a.AttentionID || p.Items[0].DecisionID != a.DecisionID {
		t.Fatal("original question must be the only user action", p)
	}
	if p := read(issue.IssueID, "kind=information"); len(p.Items) != 0 {
		t.Fatal("the original question also created an information notice", p)
	}
	status, res = e.op(token, e.envelope("attentions.respond", protocol.Target{Type: "attention", ID: a.AttentionID}, &a.Revision,
		protocol.RespondAttentionPayload{Version: a.QuestionVersion, Option: "Continue"}))
	if status != http.StatusOK || res.Operation == nil || res.Operation.Status != protocol.OpSucceeded {
		t.Fatal(status, res)
	}
	if p := read(issue.IssueID, "kind=decision"); len(p.Items) != 1 || p.Items[0].Status != "resolved" || p.Items[0].Decision.Status != "answered" {
		t.Fatal("the historical answer lost its original item", p)
	}
	beforeMessages := e.count("SELECT COUNT(*) FROM conversation_bindings WHERE object_type='attention'")
	close := e.envelope("issues.set_status", target, &issue.Revision,
		protocol.SetIssueStatusPayload{Status: protocol.IssueDone, ExpectedAssignmentEpoch: issue.AssignmentEpoch})
	closed := issueHTTP(t, e, token, close)
	// A refresh of the revision followed by the same business endpoint is a no-op.
	repeated := issueHTTP(t, e, token, e.envelope("issues.set_status", target, &closed.Revision,
		protocol.SetIssueStatusPayload{Status: protocol.IssueDone, ExpectedAssignmentEpoch: closed.AssignmentEpoch}))
	if repeated.Revision != closed.Revision {
		t.Fatal("repeating the endpoint changed the Issue revision", repeated)
	}
	cancelled := issueHTTP(t, e, token, e.envelope("issues.cancel", protocol.Target{Type: "issue", ID: other.IssueID}, &other.Revision,
		protocol.IssueControlPayload{ExpectedAssignmentEpoch: other.AssignmentEpoch}))

	for _, row := range []struct {
		issue protocol.Issue
		state string
	}{{closed, "closed"}, {cancelled, "cancelled"}} {
		p := read(row.issue.IssueID, "kind=information&status=open")
		if len(p.Items) != 1 {
			t.Fatal(row.issue.IssueID, p)
		}
		item := p.Items[0]
		transition := item.IssueTransition
		if transition == nil || transition.State != row.state || transition.IssueRevision != row.issue.Revision ||
			item.IssueTitle != row.issue.Plan.Title || item.CreatedAt != row.issue.UpdatedAt ||
			item.Historical || item.BlocksClose || item.CanDismiss || item.ReadAt != 0 || item.ArchivedAt != 0 {
			t.Fatal(item)
		}
		if p := read(row.issue.IssueID, "actionable_only=true&status=open&include_archived=true"); len(p.Items) != 0 {
			t.Fatal("terminal transition counted as work", p)
		}
		var detail protocol.AttentionView
		if code := e.do("GET", "/api/v1/attentions/"+item.AttentionID, token, nil, &detail); code != http.StatusOK || detail.IssueTransition == nil || detail.IssueID != row.issue.IssueID {
			t.Fatal(code, detail)
		}
		if code := e.do("GET", "/api/v1/attentions/"+item.AttentionID, "", nil, nil); code != http.StatusUnauthorized {
			t.Fatal("unauthenticated caller read the notice", code)
		}
		status, res := e.op(token, e.envelope("attentions.present", protocol.Target{Type: "attention", ID: item.AttentionID}, &item.Revision,
			protocol.PresentAttentionPayload{Read: new(bool)}))
		if status != http.StatusOK || res.Operation == nil || res.Operation.Error == nil || res.Operation.Error.Code != protocol.CodeInvalidTransition {
			t.Fatal("transition notice accepted a read mutation", status, res)
		}
		status, res = e.op(token, e.envelope("attentions.withdraw", protocol.Target{Type: "attention", ID: item.AttentionID}, &item.Revision,
			protocol.WithdrawAttentionPayload{Mode: protocol.AttentionWithdrawalNotNeeded}))
		if status != http.StatusOK || res.Operation == nil || res.Operation.Error == nil || res.Operation.Error.Code != protocol.CodeInvalidTransition {
			t.Fatal("transition notice accepted an ordinary action dismissal", status, res)
		}
	}
	if got := e.count("SELECT COUNT(*) FROM conversation_bindings WHERE object_type='attention'"); got != beforeMessages {
		t.Fatalf("terminal notices created attention messages: before %d, after %d", beforeMessages, got)
	}
}
