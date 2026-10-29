package app

import (
	"encoding/json"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"testing"
)

func TestHostedManagerCannotConsumeQueuedOwnerSource(t *testing.T) {
	for _, action := range []string{"review", "task"} {
		t.Run(action, func(t *testing.T) {
			h := hostedManagerHarness(t)
			issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
			turn, manager := startHostedIssueReview(t, h, issue)
			queued := h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{ConversationID: "issue:" + issue.IssueID, Body: "QUEUED_OWNER_PRIVATE_CONSTRAINT"}, nil), SubmitMessage{})
			if queued.Status != protocol.OpAccepted {
				t.Fatal(queued)
			}
			var result struct {
				MessageID string `json:"message_id"`
			}
			if err := json.Unmarshal(queued.Result, &result); err != nil {
				t.Fatal(err)
			}
			a := h.associations(result.MessageID)
			if action == "review" {
				payload := hostedReviewPayload(t, h, issue)
				command := HostedCoordinationCommand{Command: ReviewHostedIssueContext{}, ExecutionID: turn.RequestID, EffectSeq: 1}
				_, err := h.app.Execute(t.Context(), manager, h.env(command.Operation(), protocol.Target{Type: "issue", ID: issue.IssueID}, payload, nil), command)
				if protocol.CodeOf(err) != protocol.CodeForbidden {
					t.Fatal("queued review admitted", err)
				}
			} else {
				payload := canonicalOwnerTaskPayload{canonicalOwnerIssueSource: canonicalOwnerIssueSource{SourceMessageID: result.MessageID, ExpectedSourceRevision: rev(a.ContentRevision)}, ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Queued source must wait", Spec: testSpec, Rating: protocol.RatingP1}
				command := HostedCoordinationCommand{Command: CanonicalOwnerTaskCreate{}, ExecutionID: turn.RequestID, EffectSeq: 1}
				_, err := h.app.Execute(t.Context(), manager, h.env(command.Operation(), protocol.Target{Type: "issue", ID: issue.IssueID}, payload, rev(issue.Revision)), command)
				if protocol.CodeOf(err) != protocol.CodeForbidden {
					t.Fatal("queued task source admitted", err)
				}
			}
		})
	}
}
func TestHostedManagerCommitsAdmittedShareBeforeQueuedSuccessor(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	turn, manager := startHostedIssueReview(t, h, issue)
	before := hostedReviewPayload(t, h, issue)
	submitted := h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{ConversationID: "issue:" + issue.IssueID, Body: "A later queued change must remain for its own input"}, nil), SubmitMessage{})
	var msg struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(submitted.Result, &msg); err != nil {
		t.Fatal(err)
	}
	before.ContextRevision = h.agentContext(issue.IssueID).Revision
	op := hostedEffect(t, h, manager, turn, 1, protocol.Target{Type: "issue", ID: issue.IssueID}, nil, before, ReviewHostedIssueContext{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var result struct {
		Complete bool `json:"complete"`
	}
	if err := json.Unmarshal(op.Result, &result); err != nil || !result.Complete {
		t.Fatal("queued successor blocked admitted review", op, err)
	}
	if op := commitHostedReview(t, h, manager, turn, 2); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	finishHostedReview(t, h, turn)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var pending, attached bool
		err := tx.Q().QueryRowContext(t.Context(), `SELECT EXISTS(SELECT 1 FROM message_context_obligations WHERE message_id=? AND work_item_id=? AND needs_review=1),EXISTS(SELECT 1 FROM logical_turn_messages WHERE logical_turn_id=? AND message_id=?)`, msg.MessageID, issue.IssueID, turn.RequestID, msg.MessageID).Scan(&pending, &attached)
		if !pending || attached {
			t.Fatal("queued responsibility was lost or consumed", pending, attached)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	delivery, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:issue:"+issue.IssueID)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	successor := startHostedReview(t, h, "issue:"+issue.IssueID, delivery)
	manager.Thread = successor.ThreadID
	if op := hostedEffect(t, h, manager, successor, 1, protocol.Target{Type: "issue", ID: issue.IssueID}, nil, hostedReviewPayload(t, h, issue), ReviewHostedIssueContext{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := commitHostedReview(t, h, manager, successor, 2); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	finishHostedReview(t, h, successor)
}
