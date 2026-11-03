package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestCreatorAcceptancePolicyOwnerAPIAndHistoricalRead(t *testing.T) {
	e := newEnv(t)
	token := e.login(protocol.ScopeOwner)
	p := protocol.CreateIssuePayload{Plan: protocol.IssuePlan{Title: "Read-only investigation", Project: "default", Intent: "Save a checked report"}, IndependentAcceptance: &protocol.IndependentAcceptanceRequest{Required: false, Reason: "Owner explicitly chooses no separate review"}}
	issue := issueHTTP(t, e, token, e.envelope("issues.create", protocol.Target{Type: "issue"}, nil, p))
	if issue.AcceptancePolicy == nil || issue.AcceptancePolicy.Required || issue.AcceptancePolicy.Creator.Kind != "owner" || issue.AcceptancePolicy.RequestID == "" {
		t.Fatal(issue)
	}
	original := *issue.AcceptancePolicy
	policy := protocol.SetIssueAcceptancePolicyPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, IndependentAcceptanceRequest: protocol.IndependentAcceptanceRequest{Required: true, Reason: "Future work requires another reviewer"}}
	changed := issueHTTP(t, e, token, e.envelope("issues.set_acceptance_policy", protocol.Target{Type: "issue", ID: issue.IssueID}, &issue.Revision, policy))
	if changed.AcceptancePolicy == nil || !changed.AcceptancePolicy.Required || changed.AcceptancePolicy.Creator.OwnerSubject != original.Creator.OwnerSubject {
		t.Fatal(changed)
	}
	var historical protocol.IssueView
	if status := e.do("GET", "/api/v1/issues/"+issue.IssueID+"?revision=1", token, nil, &historical); status != http.StatusOK || historical.AcceptancePolicy == nil || historical.AcceptancePolicy.Required {
		t.Fatal(status, historical)
	}
	raw, _ := json.Marshal(p)
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	fields["creator"] = map[string]any{"kind": "agent", "agent": "forged"}
	status, op := e.op(token, e.envelope("issues.create", protocol.Target{Type: "issue"}, nil, fields))
	if status == http.StatusOK && (op.Operation == nil || op.Operation.Status == protocol.OpSucceeded) {
		t.Fatal("payload impersonated creator", status, op)
	}
}

func TestCreatorAcceptancePolicyMissingTargetIsDeterministic(t *testing.T) {
	e := newEnv(t)
	token := e.login(protocol.ScopeOwner)
	revision := int64(1)
	payload := protocol.SetIssueAcceptancePolicyPayload{ExpectedAssignmentEpoch: 1, IndependentAcceptanceRequest: protocol.IndependentAcceptanceRequest{Required: false, Reason: "Read-only findings"}}
	env := e.envelope("issues.set_acceptance_policy", protocol.Target{Type: "issue", ID: "wi_00000000000000000000000001"}, &revision, payload)
	for range 2 {
		status, result := e.op(token, env)
		if status != http.StatusOK || result.Operation == nil || result.Operation.Status != protocol.OpFailed || result.Operation.Error == nil || result.Operation.Error.Code != protocol.CodeNotFound {
			t.Fatal(status, result)
		}
	}
}
