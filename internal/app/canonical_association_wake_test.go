package app

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestNamedMainReplyClassificationDoesNotGenerateNewOwnerInput(t *testing.T) {
	for _, repair := range []bool{false, true} {
		name := "original"
		if repair {
			name = "repair"
		}
		t.Run(name, func(t *testing.T) { testNamedMainReplyClassificationWake(t, repair) })
	}
}
func testNamedMainReplyClassificationWake(t *testing.T, repair bool) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	source, delivery := h.say("Route my new material to the responsibility I own")
	main := startHostedReview(t, h, "main", delivery)
	principal := protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: "named-main", Thread: main.ThreadID}
	classify := func(message string, seq int64, actor protocol.Principal) protocol.Operation {
		t.Helper()
		var a protocol.MessageAssociations
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			var err error
			a, err = store.MessageAssociations(t.Context(), tx.Q(), message)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		payload := protocol.SetIssueMessageAssociationsPayload{Disposition: "linked", ContentRevision: a.ContentRevision, IssueIDs: []string{issue.IssueID}}
		var command Command = SetIssueMessageAssociations{}
		if actor.Kind == protocol.PrincipalMainAgent {
			command = HostedCoordinationCommand{Command: command, ExecutionID: main.RequestID, EffectSeq: seq}
		}
		op, err := h.app.Execute(t.Context(), actor, h.env(command.Operation(), protocol.Target{Type: "message", ID: message}, payload, rev(a.Revision)), command)
		if err != nil || op.Status != protocol.OpSucceeded {
			t.Fatal(op, err)
		}
		return op
	}
	notices := func(message string) int {
		t.Helper()
		var n int
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			return tx.Q().QueryRowContext(t.Context(), `SELECT count(*) FROM background_sources s JOIN background_batches b USING(batch_id) WHERE b.work_item_id=? AND s.message_id=? AND s.kind='conversation.associations_changed'`, issue.IssueID, message).Scan(&n)
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	classify(source, 1, principal)
	if notices(source) != 1 {
		t.Fatal("new owner material was not routed")
	}
	// The manager has already reviewed the actual owner input and is idle.
	managerDelivery, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:issue:"+issue.IssueID)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	manager := startHostedReview(t, h, "issue:"+issue.IssueID, managerDelivery)
	managerPrincipal := protocol.Principal{Kind: protocol.PrincipalBackgroundAgent, Subject: "manager", Thread: manager.ThreadID, ConversationID: "issue:" + issue.IssueID}
	if op := hostedEffect(t, h, managerPrincipal, manager, 1, protocol.Target{Type: "issue", ID: issue.IssueID}, nil, hostedReviewPayload(t, h, issue), ReviewHostedIssueContext{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := commitHostedReview(t, h, managerPrincipal, manager, 2); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	finishHostedReview(t, h, manager)
	report := recordAgentMessagePayload{Body: "I routed your material to the existing responsibility; no new owner instruction", ReplyTo: source, TurnID: main.TurnID, ItemID: "progress-reply", Status: protocol.AssistantCompleted}
	op, err := h.app.Execute(t.Context(), principal, h.env("conversation.record_agent_message", protocol.Target{Type: "conversation"}, report, nil), RecordAgentMessage{})
	if err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal(op, err)
	}
	var reply struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(op.Result, &reply); err != nil {
		t.Fatal(err)
	}
	if repair {
		original := main
		original.Status = store.TurnCompleted
		if err := h.app.PutMainTurn(t.Context(), original); err != nil {
			t.Fatal(err)
		}
		if err := h.app.FinishDelivery(t.Context(), delivery, DeliveryResult{Outcome: Delivered}); err != nil {
			t.Fatal(err)
		}
		resumed, next, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
		if err != nil {
			t.Fatal(err)
		}
		if !ok && next.After(h.clock.Now()) {
			h.clock.Advance(next.Sub(h.clock.Now()))
			resumed, _, ok, err = h.app.NextDelivery(t.Context(), MainThreadDestination)
		}
		if err != nil || !ok || resumed.Operation != "conversation.repair_turn" {
			t.Fatal("missing actual repair", resumed, ok, err)
		}
		main = startHostedReview(t, h, "main", resumed)
		var repairLogical, originalLogical string
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			if err := tx.Q().QueryRowContext(t.Context(), `SELECT logical_turn_id FROM logical_turn_executions WHERE request_id=?`, main.RequestID).Scan(&repairLogical); err != nil {
				return err
			}
			return tx.Q().QueryRowContext(t.Context(), `SELECT logical_turn_id FROM logical_turn_executions WHERE request_id=?`, original.RequestID).Scan(&originalLogical)
		}); err != nil {
			t.Fatal(err)
		}
		if repairLogical != originalLogical {
			t.Fatal("repair fixture lost original logical responsibility")
		}
	}
	classify(reply.MessageID, 2, principal)
	if notices(reply.MessageID) != 0 {
		t.Fatal("main's own narration became a new owner input")
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var pending int
		err := tx.Q().QueryRowContext(t.Context(), `SELECT count(*) FROM message_context_obligations WHERE work_item_id=? AND message_id=? AND needs_review=1`, issue.IssueID, reply.MessageID).Scan(&pending)
		if err == nil && pending != 0 {
			t.Fatal("bookkeeping manufactured an unscheduled technical source duty", pending)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// The current owner can explicitly introduce that same assistant material.
	classify(reply.MessageID, 0, owner)
	if notices(reply.MessageID) != 1 {
		t.Fatal("explicit owner source selection was not routed")
	}
	// Repeating bookkeeping cannot clear the owner's existing real duty.
	classify(reply.MessageID, 3, principal)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var pending int
		err := tx.Q().QueryRowContext(t.Context(), `SELECT count(*) FROM message_context_obligations WHERE work_item_id=? AND message_id=? AND needs_review=1`, issue.IssueID, reply.MessageID).Scan(&pending)
		if err == nil && pending != 1 {
			t.Fatal("bookkeeping erased the owner's explicit source obligation", pending)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
