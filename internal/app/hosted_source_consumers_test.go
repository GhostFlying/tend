package app

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestHostedMainClassificationRequiresAdmittedTarget(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	admitted, delivery := h.say("Classify this delivered material")
	turn := startHostedReview(t, h, "main", delivery)
	principal := protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: "named-main", Thread: turn.ThreadID}
	queued := queueMessage(t, h, "Later owner material must stay queued")
	classify := func(id string, seq int64) (protocol.Operation, error) {
		a := h.associations(id)
		command := HostedCoordinationCommand{Command: SetIssueMessageAssociations{}, ExecutionID: turn.RequestID, EffectSeq: seq}
		return h.app.Execute(t.Context(), principal, h.env(command.Operation(), protocol.Target{Type: "message", ID: id}, protocol.SetIssueMessageAssociationsPayload{Disposition: "linked", ContentRevision: a.ContentRevision, IssueIDs: []string{issue.IssueID}}, rev(a.Revision)), command)
	}
	if op, err := classify(queued, 1); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatalf("queued target classified: %+v %v", op, err)
	}
	if a := h.associations(queued); len(a.Items) != 0 {
		t.Fatal("rejected target acquired responsibility", a)
	}
	if op, err := classify(admitted, 1); err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal(op, err)
	}
	// A current owner command remains sufficient to explicitly classify material.
	if op := h.associate(owner, queued, issue.IssueID); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}

func TestHostedNestedOwnerEvidenceRequiresUsableSource(t *testing.T) {
	for _, state := range []string{"queued", "withdrawn", "admitted"} {
		t.Run(state, func(t *testing.T) {
			h := hostedManagerHarness(t)
			issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
			turn, principal := startHostedIssueReview(t, h, issue)
			messageID := turn.MessageID
			if state != "admitted" {
				submitted := h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{ConversationID: principal.ConversationID, Body: "I confirm this result"}, nil), SubmitMessage{})
				var message struct {
					MessageID string `json:"message_id"`
				}
				if err := json.Unmarshal(submitted.Result, &message); err != nil {
					t.Fatal(err)
				}
				messageID = message.MessageID
			}
			if state == "withdrawn" {
				var control protocol.MessageControl
				if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
					var err error
					control, err = store.MessageControl(t.Context(), tx.Q(), messageID)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				if op := h.exec(owner, h.env("conversation.withdraw_message", protocol.Target{Type: "message", ID: messageID}, protocol.WithdrawMessagePayload{}, rev(control.Revision)), WithdrawMessage{}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			issue = canonicalTaskIssue(t, h, issue.IssueID)
			plan := issue.Plan
			plan.Stages = []protocol.IssueStage{{StageID: "report", Title: "Report", Status: protocol.WorkItemStageDone, Evidence: []protocol.IssueEvidence{{Kind: protocol.WorkItemEvidenceOwnerDecision, Reference: messageID, Summary: "Owner confirms the report"}}}}
			command := HostedCoordinationCommand{Command: CanonicalOwnerIssueCommand{Action: "update"}, ExecutionID: turn.RequestID, EffectSeq: 1}
			op, err := h.app.Execute(t.Context(), principal, h.env(command.Operation(), protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.UpdateIssuePayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Plan: plan}, rev(issue.Revision)), command)
			if state == "admitted" {
				if err != nil || op.Status != protocol.OpSucceeded {
					t.Fatal(op, err)
				}
				return
			}
			if protocol.CodeOf(err) != protocol.CodeForbidden {
				t.Fatalf("%s nested evidence accepted: %+v %v", state, op, err)
			}
			if got := canonicalTaskIssue(t, h, issue.IssueID); got.Revision != issue.Revision {
				t.Fatal("rejected evidence changed definition", got)
			}
			// The authenticated owner can still make an explicit current decision.
			plan.Stages[0].Evidence[0].Reference = "current_request"
			if op := h.exec(owner, h.env("issues.update", protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.UpdateIssuePayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Plan: plan}, rev(issue.Revision)), CanonicalOwnerIssueCommand{Action: "update"}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
		})
	}
}

func TestOwnerLegacyAssociationCorrectionWakesCanonicalManagers(t *testing.T) {
	h := hostedManagerHarness(t)
	first := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	second := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	settleBatch := func(issue protocol.Issue) {
		t.Helper()
		conversation := "issue:" + issue.IssueID
		delivery, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+conversation)
		if err != nil || !ok {
			t.Fatal("missing actual manager batch", ok, err)
		}
		turn := startHostedReview(t, h, conversation, delivery)
		principal := protocol.Principal{Kind: protocol.PrincipalBackgroundAgent, Subject: "manager", Thread: turn.ThreadID, ConversationID: conversation}
		if op := hostedEffect(t, h, principal, turn, 1, protocol.Target{Type: "issue", ID: issue.IssueID}, nil, hostedReviewPayload(t, h, issue), ReviewHostedIssueContext{}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		if op := commitHostedReview(t, h, principal, turn, 2); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		finishHostedReview(t, h, turn)
	}
	settleBatch(first)
	settleBatch(second)
	source, delivery := h.say("Historical admitted owner material")
	startHostedReview(t, h, "main", delivery)
	count := func(issue string) int {
		t.Helper()
		var n int
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			return tx.Q().QueryRowContext(t.Context(), `SELECT count(*) FROM background_sources s JOIN background_batches b USING(batch_id) WHERE b.work_item_id=? AND s.message_id=? AND s.kind='conversation.associations_changed'`, issue, source).Scan(&n)
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if op := h.associate(owner, source, first.IssueID); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	settleBatch(first)
	if pending, err := h.app.PendingAgentConversations(t.Context()); err != nil || len(pending) != 0 {
		t.Fatal("managers not idle before owner correction", pending, err)
	}
	beforeFirst, beforeSecond := count(first.IssueID), count(second.IssueID)
	a := h.associations(source)
	env := h.env("conversation.set_associations", protocol.Target{Type: "message", ID: source}, protocol.SetMessageAssociationsPayload{Disposition: "linked", ContentRevision: a.ContentRevision, WorkItemIDs: []string{second.IssueID}}, rev(a.Revision))
	if op := h.exec(owner, env, SetMessageAssociations{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if count(first.IssueID) != beforeFirst+1 || count(second.IssueID) != beforeSecond+1 {
		t.Fatal("owner correction did not notify old and new responsible agents", count(first.IssueID), count(second.IssueID))
	}
	for _, issue := range []protocol.Issue{first, second} {
		delivery, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:issue:"+issue.IssueID)
		if err != nil || !ok || delivery.Operation != "main_thread.handle_event" {
			t.Fatal("corrected responsibility not serviceable", delivery, ok, err)
		}
	}
	h.exec(owner, env, SetMessageAssociations{})
	if count(first.IssueID) != beforeFirst+1 || count(second.IssueID) != beforeSecond+1 {
		t.Fatal("replay duplicated fanout")
	}
}
