package app

import (
	"encoding/json"
	"testing"

	nativeharness "github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/coordination"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

type urgentQueueFixture struct {
	*harness
	issue        protocol.Issue
	assignment   protocol.Assignment
	turn         store.MainTurn
	conversation string
}

func newUrgentQueueFixture(t *testing.T) urgentQueueFixture {
	t.Helper()
	h, _, issue, assignment, _ := reviewManagerAssignment(t)
	conversation := assignment.Manager.ConversationID
	delivery, _, claimed, err := h.app.NextDelivery(t.Context(), store.ConversationDestination(conversation))
	if err != nil || !claimed || delivery.Operation != OpHandleEvent {
		t.Fatal(delivery, claimed, err)
	}
	turn := startHostedReview(t, h, conversation, delivery)
	return urgentQueueFixture{h, issue, assignment, turn, conversation}
}

func (f urgentQueueFixture) submit(t *testing.T, text string) string {
	t.Helper()
	op := f.exec(owner, f.env("conversation.submit_message", protocol.Target{Type: "message"},
		protocol.SubmitMessagePayload{ConversationID: f.conversation, Body: text}, nil), SubmitMessage{})
	if op.Status != protocol.OpAccepted {
		t.Fatal(op)
	}
	return messageID(t, op)
}

func (f urgentQueueFixture) steer(t *testing.T, source string) protocol.IssueOwnerSteerView {
	t.Helper()
	env := urgentTestEnvelope(t, f.harness, f.issue.IssueID, f.assignment.RunID, source, "")
	return steerOperation(t, f.exec(owner, env, SteerIssueOwner{}), "queued")
}

func (f urgentQueueFixture) claim(t *testing.T, notice string) IssueOwnerSteerDispatch {
	t.Helper()
	d, err := f.app.ClaimIssueOwnerSteer(t.Context(), f.turn.RequestID, f.assignment.RunID, f.turn.ThreadID, f.turn.TurnID, true)
	if err != nil || d.MessageID != notice {
		t.Fatal(d, err)
	}
	return d
}

func (f urgentQueueFixture) consume(t *testing.T, d IssueOwnerSteerDispatch) {
	t.Helper()
	if err := f.app.ConsumeIssueOwnerSteer(t.Context(), f.conversation, f.turn.ThreadID, f.turn.TurnID, d.Text, "observed-native-user-item"); err != nil {
		t.Fatal(err)
	}
}

func (f urgentQueueFixture) queue(t *testing.T) protocol.ConversationQueuePage {
	t.Helper()
	var page protocol.ConversationQueuePage
	if err := f.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		page, err = views.ConversationQueue(t.Context(), tx.Q(), "owner", 0, 50, f.conversation)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return page
}

func (f urgentQueueFixture) next(t *testing.T, message string) Delivery {
	t.Helper()
	d, _, claimed, err := f.app.NextDelivery(t.Context(), store.ConversationDestination(f.conversation))
	if err != nil || claimed != (message != "") {
		t.Fatalf("expected ordinary delivery of %q, got %+v (claimed %t): %v", message, d, claimed, err)
	}
	if claimed {
		var p HandleMessage
		if err := json.Unmarshal(d.Payload, &p); err != nil || d.Operation != OpHandleMessage || p.MessageID != message {
			t.Fatal(d, p, err)
		}
	}
	return d
}

func TestIssueOwnerUrgentConsumptionSatisfiesOrdinaryQueueAndDispatch(t *testing.T) {
	f := newUrgentQueueFixture(t)
	const body = "An original owner message already consumed by urgent delivery"
	original := f.submit(t, body)
	other := f.submit(t, "This ordinary message still needs its own turn")
	notice := f.steer(t, original)
	before := f.queue(t)
	assertPending := func() {
		t.Helper()
		page := f.queue(t)
		if page.Total != 2 || page.Items[0].Item.MessageID != original || page.Items[0].Status != "queued" || !page.Items[0].CanWithdraw {
			t.Fatal(page)
		}
		if err := f.store.Read(t.Context(), func(tx *store.ReadTx) error {
			if err := requireHostedSourceAdmitted(t.Context(), tx.Q(), original); protocol.CodeOf(err) != protocol.CodeForbidden {
				t.Fatalf("unconfirmed source admitted: %v", err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	assertPending()
	d := f.claim(t, notice.MessageID)
	assertPending()
	if err := f.app.RecordIssueOwnerSteerAnswer(t.Context(), notice.MessageID, "accepted", "waiting_safe_boundary"); err != nil {
		t.Fatal(err)
	}
	assertPending()
	f.consume(t, d)
	if page := f.queue(t); page.Total != 1 || len(page.Items) != 1 || page.Items[0].Item.MessageID != other || !page.Items[0].CanWithdraw || page.InterruptibleMessageID != f.turn.MessageID {
		t.Fatal(page)
	}
	withdraw := f.exec(owner, f.env("conversation.withdraw_message", protocol.Target{Type: "message", ID: original},
		protocol.WithdrawMessagePayload{}, rev(before.Items[0].Revision)), WithdrawMessage{})
	expectWorkCode(t, withdraw, protocol.CodeInvalidTransition)
	if err := f.store.Read(t.Context(), func(tx *store.ReadTx) error {
		block, err := views.TranscriptBlock(t.Context(), tx.Q(), original, 0, 50)
		if err != nil {
			return err
		}
		item := block.Trigger
		r := item.ConsumedIssueOwnerSteer
		if item.State != protocol.MessageConsumed || item.Body != body || item.IssueOwnerSteer != nil || item.Control.WithdrawnAt != 0 ||
			r == nil || r.SourceMessageID != original || r.SourceRevision != 1 || r.MessageID != notice.MessageID ||
			r.RunID != f.assignment.RunID || r.NativeItemID != "observed-native-user-item" || r.Status != "consumed" {
			t.Fatal(item)
		}
		if err := requireHostedSourceAdmitted(t.Context(), tx.Q(), original); err != nil {
			return err
		}
		if err := store.HideUndeliveredOwner(t.Context(), tx.Q(), &item); err != nil || item.Body != body {
			t.Fatal(item, err)
		}
		return communicationSourceAllowed(t.Context(), tx.Q(), f.turn.RequestID, f.conversation, notice.MessageID)
	}); err != nil {
		t.Fatal(err)
	}
	source := f.associations(original)
	review := hostedReviewPayload(t, f.harness, f.issue)
	review.Sources = []protocol.LogicalTurnReview{{MessageID: original, ContentRevision: source.ContentRevision, AssociationRevision: source.Revision}}
	manager := protocol.Principal{Kind: protocol.PrincipalRun, Subject: f.assignment.RunID,
		Role: protocol.RoleIssueManager, Thread: f.turn.ThreadID, ConversationID: f.conversation}
	if op := hostedEffect(t, f.harness, manager, f.turn, 1, protocol.Target{Type: "issue", ID: f.issue.IssueID}, nil, review, ReviewHostedIssueContext{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if err := f.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return communicationSourceAllowed(t.Context(), tx.Q(), f.turn.RequestID, f.conversation, original)
	}); err != nil {
		t.Fatal(err)
	}
	finishHostedReview(t, f.harness, f.turn)
	otherTurn := startHostedReview(t, f.harness, f.conversation, f.next(t, other))
	finishHostedReview(t, f.harness, otherTurn)
	f.next(t, "")
	if err := f.store.Read(t.Context(), func(tx *store.ReadTx) error {
		ready, err := externalWaitOwnerInputReady(t.Context(), tx.Q(), f.assignment.TaskID)
		if ready {
			t.Fatal("a consumed original still wakes an external wait as new owner input")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIssueOwnerUrgentQueueLateConsumptionFencesClaimedOrdinaryStart(t *testing.T) {
	f := newUrgentQueueFixture(t)
	original := f.submit(t, "An actual original with a late urgent receipt")
	notice := f.steer(t, original)
	d := f.claim(t, notice.MessageID)
	finishHostedReview(t, f.harness, f.turn)
	if err := f.app.RecordIssueOwnerSteerAnswer(t.Context(), notice.MessageID, "accepted", "late native acknowledgement"); err != nil {
		t.Fatal(err)
	}
	if page := f.queue(t); page.Total != 1 || page.Items[0].Status != "queued" {
		t.Fatal("acknowledgement without a user item must not settle ordinary delivery", page)
	}
	ordinary := f.next(t, original)
	f.consume(t, d)
	if page := f.queue(t); page.Total != 0 || page.Active != nil {
		t.Fatal("previously claimed ordinary original still shown as waiting", page)
	}
	start := store.MainTurn{RequestID: ordinary.RequestID, MessageID: original, ThreadID: f.turn.ThreadID, Status: store.TurnStarting}
	if err := f.app.PutMainTurn(t.Context(), start); protocol.CodeOf(err) != protocol.CodeInvalidTransition {
		t.Fatalf("late receipt did not fence the claimed native start: %v", err)
	}
	if err := f.app.FinishDelivery(t.Context(), ordinary, DeliveryResult{Outcome: Retry}); err != nil {
		t.Fatal(err)
	}
	f.next(t, "")
	// Owner originals are immutable through the public edit surface. Advance
	// the version through the ledger's actual body-change trigger to exercise it.
	if err := f.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE conversation_messages SET body='Corrected owner intent' WHERE message_id=?`, original)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if page := f.queue(t); page.Total != 1 || !page.Items[0].CanWithdraw || page.Items[0].Item.State != protocol.MessageQueued || page.Items[0].Item.ConsumedIssueOwnerSteer != nil {
		t.Fatal("older receipt overrode a new source version", page)
	}
	if err := f.store.Read(t.Context(), func(tx *store.ReadTx) error {
		if err := requireHostedSourceAdmitted(t.Context(), tx.Q(), original); protocol.CodeOf(err) != protocol.CodeForbidden {
			t.Fatalf("an earlier receipt admitted the new version: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	newVersion := f.next(t, original)
	if newVersion.RequestID != ordinary.RequestID || newVersion.Attempt != ordinary.Attempt+1 {
		t.Fatal("original ordinary delivery identity was discarded", newVersion)
	}
	finishHostedReview(t, f.harness, startHostedReview(t, f.harness, f.conversation, newVersion))
}

func TestIssueOwnerUrgentQueueFallbackKeepsOriginalDispatch(t *testing.T) {
	for _, result := range []string{"unknown", "rejected"} {
		t.Run(result, func(t *testing.T) {
			f := newUrgentQueueFixture(t)
			original := f.submit(t, "Still pending ordinary delivery")
			notice := f.steer(t, original)
			d := f.claim(t, notice.MessageID)
			if err := f.app.RecordIssueOwnerSteerAnswer(t.Context(), notice.MessageID, result, "observed response"); err != nil {
				t.Fatal(err)
			}
			finishHostedReview(t, f.harness, f.turn)
			if result == "rejected" {
				// A definitely rejected urgent input cannot be promoted by an item
				// reported after rejection.
				f.consume(t, d)
			}
			if page := f.queue(t); page.Total != 1 || !page.Items[0].CanWithdraw || page.Items[0].Item.ConsumedIssueOwnerSteer != nil {
				t.Fatal(page)
			}
			ordinaryTurn := startHostedReview(t, f.harness, f.conversation, f.next(t, original))
			if result == "unknown" {
				// Receipt after the ordinary native start is recorded without
				// changing the already-started turn or its ability to finish.
				f.consume(t, d)
				if page := f.queue(t); page.Total != 0 || page.Active == nil || page.Active.Status != "processing" || page.InterruptibleMessageID != original {
					t.Fatal(page)
				}
			}
			finishHostedReview(t, f.harness, ordinaryTurn)
			f.next(t, "")
		})
	}
}

func TestIssueOwnerUrgentQueueFencesOnlyUnreleasedManagerNativeTurn(t *testing.T) {
	for _, test := range []struct {
		name     string
		released bool
		adopted  bool
	}{{name: "pending"}, {name: "released", released: true}, {name: "adopted", adopted: true}} {
		t.Run(test.name, func(t *testing.T) {
			f := newUrgentQueueFixture(t)
			ctx := t.Context()
			original := f.submit(t, "An urgent receipt can arrive while the next native turn is queued")
			notice := f.steer(t, original)
			urgent := f.claim(t, notice.MessageID)
			if test.adopted {
				original := f.turn
				original.Status = store.TurnFailed
				original.Failure = &protocol.AgentFailure{Code: "rate_limit_exceeded", HTTPStatus: 429}
				if err := f.app.PutMainTurn(ctx, original); err != nil {
					t.Fatal(err)
				}
				inflight, err := f.app.InFlightDeliveries(ctx)
				if err != nil {
					t.Fatal(err)
				}
				for _, d := range inflight {
					if d.RequestID == original.RequestID {
						if err := f.app.FinishDelivery(ctx, d, DeliveryResult{Outcome: Delivered}); err != nil {
							t.Fatal(err)
						}
					}
				}
			} else {
				finishHostedReview(t, f.harness, f.turn)
			}
			delivery := f.next(t, original)
			selected, named, err := f.app.NamedHostedSelection(ctx, f.conversation)
			if err != nil || !named {
				t.Fatal(selected, named, err)
			}
			starting := store.MainTurn{RequestID: delivery.RequestID, MessageID: original, ThreadID: f.turn.ThreadID, Status: store.TurnStarting, Hosted: &selected}
			if err := f.app.PutMainTurn(ctx, starting); err != nil {
				t.Fatal(err)
			}
			payload, err := json.Marshal(coordination.Request{ThreadID: starting.ThreadID, Input: nativeharness.TurnInput{Text: "Original", UserMessageID: original}})
			if err != nil {
				t.Fatal(err)
			}
			request, err := f.app.QueueManagerNative(ctx, f.assignment.RunID, "turn", payload)
			if err != nil {
				t.Fatal(err)
			}
			claim := func() protocol.ManagerNativeRequest {
				t.Helper()
				op := f.runAs(f.assignment, protocol.OpManagerNativeClaim, protocol.ManagerNativeClaimPayload{}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeClaim})
				var got protocol.ManagerNativeRequest
				if err := json.Unmarshal(op.Result, &got); err != nil || op.Error != nil {
					t.Fatal(op, err)
				}
				return got
			}
			if test.released && claim().ID != request {
				t.Fatal("the ordinary request was not released before the late receipt")
			}
			f.consume(t, urgent)
			got := claim()
			reply, ready, err := f.app.ManagerNativeResult(ctx, f.assignment.RunID, request)
			if err != nil || (test.released && (ready || got.ID != request)) || (!test.released && (got.ID != "" || !ready || !reply.Rejected)) {
				t.Fatal(got, reply, ready, err)
			}
			if test.adopted {
				starting.Status = store.TurnRejected
				if err := f.app.PutMainTurn(ctx, starting); err != nil {
					t.Fatal(err)
				}
				if err := f.app.FinishDelivery(ctx, delivery, DeliveryResult{Outcome: Retry}); err != nil {
					t.Fatal(err)
				}
				r, found, err := f.app.AgentRecovery(ctx, f.turn.RequestID)
				if err != nil || !found || r.Status != "blocked" || r.Reason == "" {
					t.Fatal(r, found, err)
				}
				if err := f.store.Read(ctx, func(tx *store.ReadTx) error {
					for _, id := range []string{f.turn.RequestID, starting.RequestID} {
						state, err := store.LogicalTurn(ctx, tx.Q(), id)
						if err != nil || state.Status != "needs_review" {
							t.Fatal(state, err)
						}
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				followup := f.submit(t, "Continue the retained work")
				followupTurn := startHostedReview(t, f.harness, f.conversation, f.next(t, followup))
				if err := f.store.Read(ctx, func(tx *store.ReadTx) error {
					var inherited bool
					err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM logical_turn_executions e JOIN logical_turn_messages m USING(logical_turn_id) WHERE e.request_id=? AND m.message_id=?)`, followupTurn.RequestID, f.turn.MessageID).Scan(&inherited)
					if err != nil || !inherited {
						t.Fatal("the follow-up could not inherit the retained source", err)
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				finishHostedReview(t, f.harness, followupTurn)
			}
		})
	}
}

func TestIssueOwnerUrgentReceiptPreservesOrdinaryReplyState(t *testing.T) {
	f := newUrgentQueueFixture(t)
	original := f.submit(t, "Finish the ordinary reply before the urgent receipt arrives")
	notice := f.steer(t, original)
	urgent := f.claim(t, notice.MessageID)
	finishHostedReview(t, f.harness, f.turn)
	ordinary := startHostedReview(t, f.harness, f.conversation, f.next(t, original))
	finishHostedReview(t, f.harness, ordinary)
	f.consume(t, urgent)
	if err := f.store.Read(t.Context(), func(tx *store.ReadTx) error {
		block, err := views.TranscriptBlock(t.Context(), tx.Q(), original, 0, 50)
		if err != nil {
			return err
		}
		item := block.Trigger
		if item.State != protocol.MessageReplied || item.ConsumedIssueOwnerSteer == nil || item.ConsumedIssueOwnerSteer.MessageID != notice.MessageID {
			t.Fatal(item)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
