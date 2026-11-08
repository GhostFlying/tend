package app

import (
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestClosedIssueOriginalRunnerTurnMayFinishSafeReceipts(t *testing.T) {
	h, _, issue, as := cancellationManager(t)
	turn, _ := startHostedIssueReview(t, h, issue)
	principal := runPrincipal(as.RunID, protocol.RoleIssueManager)
	principal.Thread = turn.ThreadID
	principal.ConversationID = as.Manager.ConversationID
	if op := hostedEffect(t, h, principal, turn, 1, protocol.Target{Type: "issue", ID: issue.IssueID}, nil, hostedReviewPayload(t, h, issue), ReviewHostedIssueContext{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	close := protocol.IssueEndpointPayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch}, Closure: &protocol.IssueClosure{Kind: "declined", Reason: "The responsible Agent records the authorized endpoint", Evidence: []protocol.IssueEvidence{{Kind: "owner_decision", Reference: turn.MessageID, Summary: "The admitted owner requested this responsibility"}}}}
	if op := hostedEffect(t, h, principal, turn, 2, protocol.Target{Type: "issue", ID: issue.IssueID}, rev(issue.Revision), close, CanonicalOwnerIssueCommand{Action: "close"}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	if issue.State != protocol.IssueClosed {
		t.Fatal(issue)
	}

	for _, field := range []string{"thread", "run", "conversation"} {
		wrong := principal
		switch field {
		case "thread":
			wrong.Thread = "different-thread"
		case "run":
			wrong.Subject = "different-run"
		case "conversation":
			wrong.ConversationID = "issue:different"
		}
		command := HostedCoordinationCommand{Command: ReviewHostedIssueContext{}, ExecutionID: turn.RequestID, EffectSeq: 3}
		op, err := h.app.Execute(t.Context(), wrong, h.env(command.Operation(), protocol.Target{Type: "issue", ID: issue.IssueID}, hostedReviewPayload(t, h, issue), nil), command)
		if err == nil {
			err = op.Error
		}
		if protocol.CodeOf(err) != protocol.CodeForbidden {
			t.Fatal("closed receipt escaped original admission", field, op, err)
		}
	}
	if op := hostedEffect(t, h, principal, turn, 3, protocol.Target{Type: "issue", ID: issue.IssueID}, nil, hostedReviewPayload(t, h, issue), ReviewHostedIssueContext{}); op.Status != protocol.OpSucceeded {
		t.Fatal("closure stranded the original admitted turn context receipt", op)
	}
	reply := protocol.CoordinationReply{ReplyTo: turn.MessageID, Body: "The authorized responsibility has ended; prior unknowns remain.", Purpose: "final", Disposition: "linked", WorkItemIDs: []string{issue.IssueID}, Contexts: []protocol.LogicalContextReview{{WorkItemID: issue.IssueID, Revision: h.agentContext(issue.IssueID).Revision}}, ReviewedKinds: allFactKinds}
	if op := hostedEffect(t, h, principal, turn, 4, protocol.Target{Type: "logical_turn", ID: turn.RequestID}, nil, reply, CoordinationReply{}); op.Status != protocol.OpSucceeded {
		t.Fatal("closure blocked the final receipt", op)
	}
	// Publication updates the source context: acknowledge it, then commit only this turn.
	if op := hostedEffect(t, h, principal, turn, 5, protocol.Target{Type: "issue", ID: issue.IssueID}, nil, hostedReviewPayload(t, h, issue), ReviewHostedIssueContext{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := commitHostedReview(t, h, principal, turn, 6); op.Status != protocol.OpSucceeded {
		t.Fatal("closure blocked the admitted turn commit", op)
	}
	for _, command := range []Command{CanonicalOwnerTaskCreate{}, CanonicalOwnerIssueCommand{Action: "reopen"}, HostedIssueContextFact{}} {
		op, err := h.app.Execute(t.Context(), principal, h.env(command.Operation(), protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Do not reopen business from a receipt-only turn"}, rev(issue.Revision)), HostedCoordinationCommand{Command: command, ExecutionID: turn.RequestID, EffectSeq: 7})
		if err == nil {
			err = op.Error
		}
		if protocol.CodeOf(err) != protocol.CodeForbidden {
			t.Fatal("safe settlement admitted business", command.Operation(), op, err)
		}
	}
	if final := canonicalTaskIssue(t, h, issue.IssueID); final.State != protocol.IssueClosed || final.Revision != issue.Revision {
		t.Fatal("turn receipts reopened or revised the business endpoint", final)
	}
}
