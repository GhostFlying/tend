package app

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func activeSteerEnv(t *testing.T) (*harness, Delivery) {
	t.Helper()
	h := newHarness(t)
	first := queueMessage(t, h, "original")
	d, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err := h.app.PutMainTurn(t.Context(), store.MainTurn{RequestID: d.RequestID, MessageID: first, ThreadID: "native", TurnID: "turn", Status: store.TurnStarted}); err != nil {
		t.Fatal(err)
	}
	return h, d
}

func TestMainSteerAdmissionOrderWithdrawalAndDecisionSource(t *testing.T) {
	h, active := activeSteerEnv(t)
	first := queueMessage(t, h, "withdraw this")
	q := readMessageQueue(t, h, "owner")
	for _, it := range q.Items {
		if it.Item.MessageID == first {
			op := h.exec(owner, h.env("conversation.withdraw_message", protocol.Target{Type: "message", ID: first}, protocol.WithdrawMessagePayload{}, rev(it.Revision)), WithdrawMessage{})
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
		}
	}
	second := queueMessage(t, h, "keep this")
	d, ok, err := h.app.ClaimMainSteer(t.Context(), active.RequestID, "native", "turn")
	if err != nil || !ok {
		t.Fatal(err)
	}
	var p HandleMessage
	_ = json.Unmarshal(d.Payload, &p)
	if p.MessageID != second {
		t.Fatal("withdrawn or unordered input claimed")
	}
	if _, ok, err := h.app.ClaimMainSteer(t.Context(), active.RequestID, "native", "turn"); err != nil || ok {
		t.Fatal("same steer claimed twice")
	}
	for _, it := range readMessageQueue(t, h, "owner").Items {
		if it.Item.MessageID == second && it.CanWithdraw {
			t.Fatal("claimed steer remains withdrawable")
		}
	}
	rec, _, _ := h.app.MainSteer(t.Context(), d.RequestID)
	rec.InputText = "exact source"
	if err := h.app.UpdateMainSteer(t.Context(), rec); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := h.app.ConsumeMainSteer(t.Context(), "other", "turn", rec.InputText, "item"); err != nil || ok {
		t.Fatal("foreign session consumed input")
	}
	if _, ok, err := h.app.ConsumeMainSteer(t.Context(), "native", "turn", rec.InputText, "item"); err != nil || !ok {
		t.Fatal(err)
	}
	// The user-item receipt can precede a lost RPC reply; its proof must win.
	if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Unknown}); err != nil {
		t.Fatal(err)
	}
	for _, it := range readMessageQueue(t, h, "owner").Items {
		if it.Item.MessageID == second {
			t.Fatal("consumed steer left pending")
		}
	}
	// Native completion does not discharge unreviewed owner input.
	if err := h.app.SettleMainSteers(t.Context(), "native", "turn", store.TurnCompleted); err != nil {
		t.Fatal(err)
	}
	_ = h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		row, err := store.GetConversationItem(t.Context(), tx.Q(), second)
		if err != nil {
			return err
		}
		if row.State != string(protocol.MessageProcessing) {
			t.Errorf("unreviewed supplement completed early: state=%s", row.State)
		}
		return nil
	})
}

func TestMainSteerDoesNotCrossHandoffOrSteerSystemEvents(t *testing.T) {
	h, active := activeSteerEnv(t)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		return tx.InsertOutbox(t.Context(), store.OutboxRow{OutboxID: h.ids.New(protocol.PrefixOutbox), RequestID: h.ids.New(protocol.PrefixRequest), Destination: MainThreadDestination, Operation: "conversation.handoff", PayloadJSON: `{}`})
	}); err != nil {
		t.Fatal(err)
	}
	queueMessage(t, h, "after handoff")
	if _, ok, err := h.app.ClaimMainSteer(t.Context(), active.RequestID, "native", "turn"); err != nil || ok {
		t.Fatal("owner input crossed handoff")
	}
	if _, ok, err := h.app.ClaimMainSteer(context.Background(), active.RequestID, "native", "different"); err != nil || ok {
		t.Fatal("wrong current turn accepted")
	}
}

func TestNewOwnerInputWaitsForTheStoppedNativeTurn(t *testing.T) {
	h, active := activeSteerEnv(t)
	original, _, err := h.app.MainTurn(t.Context(), active.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	op := h.exec(owner, h.env("conversation.interrupt_reply", protocol.Target{Type: "conversation"}, protocol.InterruptReplyPayload{MessageID: original.MessageID}, nil), InterruptReply{})
	if op.Status != protocol.OpAccepted {
		t.Fatal(op)
	}
	next := queueMessage(t, h, "fresh request after stopping")
	if d, ok, err := h.app.ClaimMainSteer(t.Context(), active.RequestID, "native", "turn"); err != nil || ok {
		t.Fatal("stopping turn absorbed a fresh request", d, ok, err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var attempts, steers int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT attempts FROM outbox WHERE destination=? AND operation=? AND json_extract(payload_json,'$.message_id')=?`, MainThreadDestination, OpHandleMessage, next).Scan(&attempts); err != nil {
			return err
		}
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM main_steers WHERE message_id=?`, next).Scan(&steers); err != nil {
			return err
		}
		if attempts != 0 || steers != 0 {
			t.Fatalf("fresh queued input was claimed: attempts=%d steers=%d", attempts, steers)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	original.Status = store.TurnInterrupted
	if err := h.app.PutMainTurn(t.Context(), original); err != nil {
		t.Fatal(err)
	}
	if err := h.app.FinishDelivery(t.Context(), active, DeliveryResult{Outcome: Delivered}); err != nil {
		t.Fatal(err)
	}
	d, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
	if err != nil || !ok || d.Operation != OpHandleMessage {
		t.Fatal(d, ok, err)
	}
	var p HandleMessage
	if err := json.Unmarshal(d.Payload, &p); err != nil || p.MessageID != next {
		t.Fatal(p, err)
	}
}

func TestExecutionScopeKeepsEventReviewWithHistoricalAuditParent(t *testing.T) {
	h := newHarness(t)
	work := h.createWork(workPlan())
	oldMessage := queueMessage(t, h, "old owner input")
	currentMessage := queueMessage(t, h, "current owner input")
	oldRequest, currentRequest := h.ids.New(protocol.PrefixRequest), h.ids.New(protocol.PrefixRequest)
	for _, turn := range []store.MainTurn{
		{RequestID: oldRequest, MessageID: oldMessage, ThreadID: "native", TurnID: "old", Status: store.TurnStarted},
		{RequestID: currentRequest, MessageID: currentMessage, ThreadID: "native", TurnID: "current", Status: store.TurnStarted},
	} {
		if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
			t.Fatal(err)
		}
	}
	work.Title = "Revised within the current execution"
	env := h.env("work_items.update", workTarget(work.WorkItemID), protocol.UpdateWorkItemPayload{WorkItemPlan: work.WorkItemPlan}, rev(work.Revision))
	env.ParentRequestID = oldRequest
	principal := protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: "main", Thread: "native"}
	if op := h.exec(principal, env, WithExecutionRequest(UpdateWorkItem{}, currentRequest)); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	state, err := h.app.LogicalTurn(t.Context(), currentRequest)
	if err != nil || len(state.Contexts) != 1 || state.Contexts[0].WorkItemID != work.WorkItemID {
		t.Fatalf("event review escaped current execution: %+v, %v", state, err)
	}
	old, err := h.app.LogicalTurn(t.Context(), oldRequest)
	if err != nil || len(old.Contexts) != 0 {
		t.Fatalf("audit parent acquired current event review: %+v, %v", old, err)
	}
}

func consumedSteerFixture(t *testing.T, h *harness, active Delivery) string {
	t.Helper()
	id := queueMessage(t, h, "supplement needing formal review")
	d, ok, err := h.app.ClaimMainSteer(t.Context(), active.RequestID, "native", "turn")
	if err != nil || !ok {
		t.Fatal(d, ok, err)
	}
	m, ok, err := h.app.MainSteer(t.Context(), d.RequestID)
	if err != nil || !ok {
		t.Fatal(m, ok, err)
	}
	m.InputText = "exact native supplement"
	if err := h.app.UpdateMainSteer(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := h.app.ConsumeMainSteer(t.Context(), "native", "turn", m.InputText, "native-user-item"); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Delivered}); err != nil {
		t.Fatal(err)
	}
	return id
}
func steerStatusFixture(t *testing.T, h *harness, id, want string) {
	t.Helper()
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var got string
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT status FROM main_steers WHERE message_id=?`, id).Scan(&got); err != nil {
			return err
		}
		if got != want {
			t.Fatalf("steer status=%s want %s", got, want)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
func TestMainSteerRestartPreservesCompletedAndUncertainNativeFacts(t *testing.T) {
	for _, completedBeforeRestart := range []bool{true, false} {
		name := "uncertain native outcome"
		if completedBeforeRestart {
			name = "completed native outcome"
		}
		t.Run(name, func(t *testing.T) {
			h, active := activeSteerEnv(t)
			supplement := consumedSteerFixture(t, h, active)
			rec, ok, err := h.app.MainTurn(t.Context(), active.RequestID)
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			if completedBeforeRestart {
				rec.Status = store.TurnCompleted
				if err := h.app.PutMainTurn(t.Context(), rec); err != nil {
					t.Fatal(err)
				}
				if err := h.app.FinishDelivery(t.Context(), active, DeliveryResult{Outcome: Delivered}); err != nil {
					t.Fatal(err)
				}
			}
			if err := h.app.SettleOpenTurns(t.Context(), rec.ThreadID); err != nil {
				t.Fatal(err)
			}
			if !completedBeforeRestart {
				if err := h.app.SettleMainSteers(t.Context(), rec.ThreadID, rec.TurnID, store.TurnInterrupted); err != nil {
					t.Fatal(err)
				}
			}
			steerStatusFixture(t, h, supplement, "consumed")
			if !completedBeforeRestart {
				if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
					item, err := store.GetConversationItem(t.Context(), tx.Q(), supplement)
					if err == nil && item.State != string(protocol.MessageUnknown) {
						t.Fatalf("uncertain native execution lost unknown state: %+v", item)
					}
					return err
				}); err != nil {
					t.Fatal(err)
				}
				rec.Status = store.TurnCompleted
				rec.NativeReconciled = true
				if err := h.app.PutMainTurn(t.Context(), rec); err != nil {
					t.Fatal(err)
				}
			}
			repair, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
			if err != nil || !ok || repair.Operation != "conversation.repair_turn" {
				t.Fatal(repair, ok, err)
			}
			r := store.MainTurn{RequestID: repair.RequestID, MessageID: rec.MessageID, ThreadID: rec.ThreadID, TurnID: "repair", Status: store.TurnStarted}
			if err := h.app.PutMainTurn(t.Context(), r); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{rec.MessageID, supplement} {
				if op := h.associate(mainAgent, id); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			commitFixtureTurn(t, h, r.RequestID)
			r.Status = store.TurnCompleted
			if err := h.app.PutMainTurn(t.Context(), r); err != nil {
				t.Fatal(err)
			}
			steerStatusFixture(t, h, supplement, "completed")
		})
	}
}

func TestSupplementStopCancelsQueuedAndInterruptsRunningRepair(t *testing.T) {
	for _, running := range []bool{false, true} {
		name := "queued"
		if running {
			name = "running"
		}
		t.Run(name, func(t *testing.T) {
			h, active := activeSteerEnv(t)
			supplement := consumedSteerFixture(t, h, active)
			original, _, err := h.app.MainTurn(t.Context(), active.RequestID)
			if err != nil {
				t.Fatal(err)
			}
			original.Status = store.TurnCompleted
			if err := h.app.PutMainTurn(t.Context(), original); err != nil {
				t.Fatal(err)
			}
			if err := h.app.FinishDelivery(t.Context(), active, DeliveryResult{Outcome: Delivered}); err != nil {
				t.Fatal(err)
			}
			if running {
				d, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
				if err != nil || !ok || d.Operation != "conversation.repair_turn" {
					t.Fatal(d, ok, err)
				}
				if err := h.app.PutMainTurn(t.Context(), store.MainTurn{RequestID: d.RequestID, MessageID: original.MessageID, ThreadID: original.ThreadID, TurnID: "metadata-repair", Status: store.TurnStarted}); err != nil {
					t.Fatal(err)
				}
			}
			op := h.exec(owner, h.env("conversation.interrupt_reply", protocol.Target{Type: "conversation"}, protocol.InterruptReplyPayload{MessageID: supplement}, nil), InterruptReply{})
			if op.Status != protocol.OpSucceeded && op.Status != protocol.OpAccepted {
				t.Fatal(op)
			}
			state, err := h.app.LogicalTurn(t.Context(), active.RequestID)
			if err != nil || state.Status != "stopped" {
				t.Fatal(state, err)
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				var pending, interrupts int
				if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM outbox WHERE operation='conversation.repair_turn' AND status='pending'`).Scan(&pending); err != nil {
					return err
				}
				if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM outbox WHERE operation='main_thread.interrupt_turn' AND status='pending' AND json_extract(payload_json,'$.message_id')=?`, original.MessageID).Scan(&interrupts); err != nil {
					return err
				}
				want := 0
				if running {
					want = 1
				}
				if pending != 0 || interrupts != want {
					t.Fatalf("pending repairs=%d interrupts=%d want=%d", pending, interrupts, want)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestMainSteerSettlesAcrossRepeatedClarificationAdoption(t *testing.T) {
	h, active := activeSteerEnv(t)
	supplement := consumedSteerFixture(t, h, active)
	original, ok, err := h.app.MainTurn(t.Context(), active.RequestID)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	previous := original
	requests := []string{original.RequestID}
	for step := 0; step < 2; step++ {
		wait := AwaitTurnClarification{RequestID: previous.RequestID}
		if op := h.exec(mainAgent, h.env(wait.Operation(), protocol.Target{Type: "logical_turn", ID: previous.RequestID}, protocol.TurnClarification{Question: "Confirm the remaining scope"}, nil), wait); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		previous.Status = store.TurnCompleted
		if err := h.app.PutMainTurn(t.Context(), previous); err != nil {
			t.Fatal(err)
		}
		if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
			_, err := tx.Exec(t.Context(), `UPDATE outbox SET status='delivered' WHERE request_id=?`, previous.RequestID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		_, next := startReviewTurn(t, h)
		next.TurnID = "successor-" + next.RequestID
		if err := h.app.PutMainTurn(t.Context(), next); err != nil {
			t.Fatal(err)
		}
		state, err := h.app.LogicalTurn(t.Context(), next.RequestID)
		if err != nil {
			t.Fatal(err)
		}
		var revision int64
		for _, pending := range state.Awaiting {
			if pending.LogicalTurnID == previous.RequestID {
				revision = pending.Revision
			}
		}
		if revision == 0 {
			t.Fatal("missing predecessor clarification", state)
		}
		adopt := AdoptClarification{RequestID: next.RequestID}
		if op := h.exec(mainAgent, h.env(adopt.Operation(), protocol.Target{Type: "logical_turn", ID: previous.RequestID}, protocol.AdoptClarificationPayload{}, rev(revision)), adopt); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		previous = next
		requests = append(requests, next.RequestID)
	}
	state, err := h.app.LogicalTurn(t.Context(), previous.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range state.Messages {
		if op := h.associate(mainAgent, message.MessageID); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	commitFixtureTurn(t, h, previous.RequestID)
	previous.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), previous); err != nil {
		t.Fatal(err)
	}
	for _, request := range requests {
		state, err := h.app.LogicalTurn(t.Context(), request)
		if err != nil || state.Status != "completed" {
			t.Fatal(state, err)
		}
	}
	steerStatusFixture(t, h, supplement, "completed")
}

func TestLateMainSteerConsumptionReopensOnlyCompletedReview(t *testing.T) {
	for _, prior := range []string{"completed", "stopped", "awaiting_clarification"} {
		t.Run(prior, func(t *testing.T) {
			h, active := activeSteerEnv(t)
			supplement := queueMessage(t, h, "late supplement")
			d, ok, err := h.app.ClaimMainSteer(t.Context(), active.RequestID, "native", "turn")
			if err != nil || !ok {
				t.Fatal(d, ok, err)
			}
			steer, _, err := h.app.MainSteer(t.Context(), d.RequestID)
			if err != nil {
				t.Fatal(err)
			}
			steer.InputText, steer.Status = "late native input", "accepted"
			if err := h.app.UpdateMainSteer(t.Context(), steer); err != nil {
				t.Fatal(err)
			}
			if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Delivered}); err != nil {
				t.Fatal(err)
			}
			original, _, err := h.app.MainTurn(t.Context(), active.RequestID)
			if err != nil {
				t.Fatal(err)
			}
			if op := h.associate(mainAgent, original.MessageID); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			commitFixtureTurn(t, h, original.RequestID)
			original.Status = store.TurnCompleted
			if err := h.app.PutMainTurn(t.Context(), original); err != nil {
				t.Fatal(err)
			}
			if err := h.app.FinishDelivery(t.Context(), active, DeliveryResult{Outcome: Delivered}); err != nil {
				t.Fatal(err)
			}
			if prior != "completed" {
				if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE logical_turns SET status=? WHERE logical_turn_id=(SELECT logical_turn_id FROM logical_turn_executions WHERE request_id=?)`, prior, active.RequestID)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			if _, ok, err := h.app.ConsumeMainSteer(t.Context(), "native", "turn", steer.InputText, "late-user"); err != nil || !ok {
				t.Fatal(ok, err)
			}
			state, err := h.app.LogicalTurn(t.Context(), active.RequestID)
			want := prior
			if prior == "completed" {
				want = "pending_repair"
			}
			if err != nil || state.Status != want || len(state.Messages) != 2 {
				t.Fatal(state, err)
			}
			wantSteer := "consumed"
			if prior == "stopped" {
				wantSteer = "interrupted"
			}
			steerStatusFixture(t, h, supplement, wantSteer)
			if prior == "stopped" {
				if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
					row, err := store.GetConversationItem(t.Context(), tx.Q(), supplement)
					if err == nil && row.State != string(protocol.MessageInterrupted) {
						t.Fatalf("late receipt revived stopped input: %+v", row)
					}
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			if got := h.associations(supplement); got.Disposition != "pending" || got.FactsReviewed {
				t.Fatal(got)
			}
			repair, _, claimed, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
			if err != nil || claimed != (prior == "completed") {
				t.Fatal(repair, claimed, err)
			}
			if claimed && repair.Operation != "conversation.repair_turn" {
				t.Fatal(repair)
			}
			if _, ok, err := h.app.ConsumeMainSteer(t.Context(), "native", "turn", steer.InputText, "late-user"); err != nil || ok {
				t.Fatal("receipt replay admitted twice", ok, err)
			}
		})
	}
}
