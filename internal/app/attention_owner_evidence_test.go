package app

import (
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestHostedManagerCloseUsesActualOwnerAttention(t *testing.T) {
	for _, tc := range []struct {
		name, kind, finish string
		other, allowed     bool
	}{
		{name: "confirmed-action", kind: "action", finish: "confirm", allowed: true},
		{name: "answered-decision", kind: "decision", finish: "respond", allowed: true},
		{name: "unconfirmed-action", kind: "action"},
		{name: "withdrawn-action", kind: "action", finish: "withdraw"},
		{name: "information", kind: "information"},
		{name: "other-issue", kind: "action", finish: "confirm", other: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := hostedManagerHarness(t)
			issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
			target := issue
			if tc.other {
				target = createCanonicalOwnerTestIssue(t, h)
			}
			p := protocol.CreateAttentionPayload{IssueID: target.IssueID, Kind: tc.kind, Title: "Confirm receipt of the delegated result"}
			if tc.kind == "decision" {
				p.Options = []string{"received", "not-received"}
			}
			attention := attentionForTest(t, h, p)
			if tc.finish != "" {
				var payload any
				switch tc.finish {
				case "confirm":
					payload = protocol.ConfirmAttentionPayload{Summary: "I received the actual result"}
				case "respond":
					payload = protocol.RespondAttentionPayload{Version: 1, Option: "received"}
				case "withdraw":
					payload = map[string]string{"reason": "This action is no longer requested"}
				}
				env := h.env("attentions."+tc.finish, protocol.Target{Type: "attention", ID: attention.AttentionID}, payload, rev(attention.Revision))
				if tc.finish == "withdraw" {
					env.Reason = "This action is no longer requested"
				}
				op := h.exec(owner, env, AttentionCommand{Action: tc.finish})
				if op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			turn, manager := startHostedIssueReview(t, h, issue)
			if op := hostedEffect(t, h, manager, turn, 1, protocol.Target{Type: "issue", ID: issue.IssueID}, nil, hostedReviewPayload(t, h, issue), ReviewHostedIssueContext{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			issue = canonicalTaskIssue(t, h, issue.IssueID)
			// Source review has already completed: no duplicate dispositions or context CAS.
			close := protocol.IssueEndpointPayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch}, Closure: &protocol.IssueClosure{Kind: protocol.WorkItemClosureCompleted, Reason: "Owner confirmed the original action after result review", Evidence: []protocol.IssueEvidence{{Kind: protocol.WorkItemEvidenceOwnerDecision, Reference: attention.AttentionID, Summary: "The original owner Attention is resolved"}}}}
			cmd := HostedCoordinationCommand{Command: CanonicalOwnerIssueCommand{Action: "close"}, ExecutionID: turn.RequestID, EffectSeq: 2}
			op, err := h.app.Execute(t.Context(), manager, h.env(cmd.Operation(), protocol.Target{Type: "issue", ID: issue.IssueID}, close, rev(issue.Revision)), cmd)
			if tc.allowed {
				if err != nil || op.Status != protocol.OpSucceeded {
					t.Fatal("actual resolved Attention was not usable as owner evidence", op, err)
				}
				if canonicalTaskIssue(t, h, issue.IssueID).State != protocol.IssueClosed {
					t.Fatal("manager did not close its Issue")
				}
			} else if err == nil && op.Status == protocol.OpSucceeded {
				t.Fatal("non-owner fact became completion evidence", tc.name)
			}
		})
	}
}
