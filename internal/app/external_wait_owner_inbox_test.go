package app

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func ownerInboxWaitFixture(t *testing.T) (*harness, *protocol.RunnerRuntime, protocol.Assignment, string) {
	t.Helper()
	h, runtime := managerClaimHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	op := h.exec(owner, h.env(protocol.OpIssueRequestManager, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.RequestIssueManagerPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Own the continuing goal"}, rev(issue.Revision)), RequestIssueManager{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	as := ownerClaim(t, h, runtime)
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	dependency := createCanonicalTaskForTest(t, h, issue)
	// Keep the unrelated external target unfinished without competing for the
	// next claim used to verify the owner's original Task continuation.
	if op := h.exec(owner, h.env("control.set_mode", taskTarget(dependency), protocol.SetControlModePayload{Mode: protocol.ControlPaused}, rev(h.task(dependency).ControlRevision)), SetControlMode{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	return h, runtime, as, dependency
}

func queueOwnerWaitInput(t *testing.T, h *harness, conversation string) (string, string) {
	t.Helper()
	op := h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{ConversationID: conversation, Body: "Apply this new owner input before continuing the external wait"}, nil), SubmitMessage{})
	if op.Status != protocol.OpAccepted {
		t.Fatal(op)
	}
	var result struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(op.Result, &result); err != nil {
		t.Fatal(err)
	}
	var request string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT request_id FROM outbox WHERE operation='main_thread.handle_message' AND json_extract(payload_json,'$.message_id')=?`, result.MessageID).Scan(&request)
	}); err != nil {
		t.Fatal(err)
	}
	return result.MessageID, request
}

func TestFault_ExternalWaitQueuedOwnerInputBeforeRegistration(t *testing.T) {
	h, runtime, as, dependency := ownerInboxWaitFixture(t)
	message, request := queueOwnerWaitInput(t, h, as.Manager.ConversationID)
	wait := waitForTask(t, h, as, dependency, 0)
	var eventSeq int64
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT event_seq FROM events WHERE object_type='message' AND object_id=? AND kind='conversation.message'`, message).Scan(&eventSeq)
	}); err != nil {
		t.Fatal(err)
	}
	if eventSeq > wait.InputEventWatermark {
		t.Fatal("fixture did not queue the owner input before wait registration", eventSeq, wait)
	}
	if err := h.app.SweepExternalWaits(t.Context()); err != nil {
		t.Fatal(err)
	}
	if h.lease(as.RunID).Status != store.LeaseActive || h.count("run_external_waits WHERE continuation_action_id IS NOT NULL") != 0 {
		t.Fatal("queued input bypassed physical stop")
	}
	stopOwnerClaim(t, h, as)
	h.clock.Advance(5 * time.Second)
	for range 2 {
		if err := h.app.SweepExternalWaits(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	waits, err := h.app.ExternalWaits(t.Context(), as.TaskID)
	if err != nil || len(waits) != 1 || waits[0].WaitID != wait.WaitID || waits[0].Status != "continued" || waits[0].WakeReason != "owner_input" {
		t.Fatal("pre-existing unprocessed owner input failed to wake its original Task", waits, err)
	}
	if h.count("run_external_waits WHERE continuation_action_id IS NOT NULL") != 1 || h.task(dependency).Lifecycle.Terminal() || h.lease(as.RunID).Status != store.LeaseReleased {
		t.Fatal("owner input duplicated the continuation or changed the external target")
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var status string
		var attempts, turns int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT status,attempts,(SELECT COUNT(*) FROM main_turns WHERE request_id=?) FROM outbox WHERE request_id=?`, request, request).Scan(&status, &attempts, &turns); err != nil {
			return err
		}
		if status != "pending" || attempts != 0 || turns != 0 {
			t.Fatal("waking fabricated consumption or changed the original input", status, attempts, turns)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	next := ownerClaim(t, h, runtime)
	if next.TaskID != as.TaskID || next.RunID == as.RunID || next.Generation != as.Generation || next.ExternalWaitContinuation == nil || next.ExternalWaitContinuation.WaitID != wait.WaitID {
		t.Fatal("input did not continue the original responsibility", next)
	}
	delivery, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+as.Manager.ConversationID)
	if err != nil || !ok || delivery.RequestID != request || delivery.Operation != OpHandleMessage {
		t.Fatal("original owner message was not admitted ahead of ordinary notices", delivery, ok, err)
	}
	turn := startHostedReview(t, h, as.Manager.ConversationID, delivery)
	if turn.MessageID != message {
		t.Fatal("continuation substituted another owner input", turn)
	}
	finishHostedReview(t, h, turn)
}

func TestExternalWaitOwnerInputDuringPhysicalStop(t *testing.T) {
	for _, linked := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-conversation", true: "active-association"}[linked], func(t *testing.T) {
			h, _, as, dependency := ownerInboxWaitFixture(t)
			wait := waitForTask(t, h, as, dependency, 0)
			conversation := as.Manager.ConversationID
			if linked {
				conversation = "main"
			}
			message, _ := queueOwnerWaitInput(t, h, conversation)
			if linked {
				if op := h.associate(owner, message, as.Manager.IssueID); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			if err := h.app.SweepExternalWaits(t.Context()); err != nil {
				t.Fatal(err)
			}
			if h.lease(as.RunID).Status != store.LeaseActive || h.count("run_external_waits WHERE continuation_action_id IS NOT NULL") != 0 {
				t.Fatal("new input released a live process")
			}
			stopOwnerClaim(t, h, as)
			h.clock.Advance(5 * time.Second)
			for range 2 {
				if err := h.app.SweepExternalWaits(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			waits, err := h.app.ExternalWaits(t.Context(), as.TaskID)
			if err != nil || len(waits) != 1 || waits[0].WaitID != wait.WaitID || waits[0].Status != "continued" || waits[0].WakeReason != "owner_input" || h.count("run_external_waits WHERE continuation_action_id IS NOT NULL") != 1 {
				t.Fatal("input during stop did not create exactly one continuation", waits, err)
			}
		})
	}
}

func TestExternalWaitOwnerInboxIgnoresIneligibleInputs(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*testing.T, *harness, protocol.Assignment)
	}{
		{"already-processed", func(t *testing.T, h *harness, as protocol.Assignment) {
			_, request := queueOwnerWaitInput(t, h, as.Manager.ConversationID)
			d, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+as.Manager.ConversationID)
			if err != nil || !ok || d.RequestID != request {
				t.Fatal(d, ok, err)
			}
			finishHostedReview(t, h, startHostedReview(t, h, as.Manager.ConversationID, d))
		}},
		{"native-completed-before-delivery-settled", func(t *testing.T, h *harness, as protocol.Assignment) {
			_, request := queueOwnerWaitInput(t, h, as.Manager.ConversationID)
			d, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+as.Manager.ConversationID)
			if err != nil || !ok || d.RequestID != request {
				t.Fatal(d, ok, err)
			}
			turn := startHostedReview(t, h, as.Manager.ConversationID, d)
			turn.Status = store.TurnCompleted
			if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
				t.Fatal(err)
			}
			if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Retry}); err != nil {
				t.Fatal(err)
			}
		}},
		{"withdrawn", func(t *testing.T, h *harness, as protocol.Assignment) {
			message, _ := queueOwnerWaitInput(t, h, as.Manager.ConversationID)
			var control protocol.MessageControl
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				var err error
				control, err = store.MessageControl(t.Context(), tx.Q(), message)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if op := h.exec(owner, h.env("conversation.withdraw_message", protocol.Target{Type: "message", ID: message}, protocol.WithdrawMessagePayload{}, rev(control.Revision)), WithdrawMessage{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
		}},
		{"other-issue", func(t *testing.T, h *harness, _ protocol.Assignment) {
			other := createCanonicalOwnerTestIssue(t, h)
			message, _ := queueOwnerWaitInput(t, h, "main")
			if op := h.associate(owner, message, other.IssueID); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
		}},
		{"removed-association", func(t *testing.T, h *harness, as protocol.Assignment) {
			message, _ := queueOwnerWaitInput(t, h, "main")
			if op := h.associate(owner, message, as.Manager.IssueID); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if op := h.associate(owner, message); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
		}},
		{"system-notice", func(t *testing.T, h *harness, as protocol.Assignment) {
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				message := h.ids.New(protocol.PrefixMessage)
				if _, err := tx.InsertMessage(t.Context(), store.MessageRow{MessageID: message, ConversationID: as.Manager.ConversationID, Author: "system", Body: "Ordinary progress"}); err != nil {
					return err
				}
				return tx.InsertOutbox(t.Context(), store.OutboxRow{OutboxID: h.ids.New(protocol.PrefixOutbox), RequestID: h.ids.New(protocol.PrefixRequest), Destination: "background_thread:" + as.Manager.ConversationID, Operation: OpHandleEvent, PayloadJSON: `{"message_id":"` + message + `"}`})
			}); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _, as, dependency := ownerInboxWaitFixture(t)
			tt.prepare(t, h, as)
			wait := waitForTask(t, h, as, dependency, 0)
			stopOwnerClaim(t, h, as)
			h.clock.Advance(5 * time.Second)
			if err := h.app.SweepExternalWaits(t.Context()); err != nil {
				t.Fatal(err)
			}
			waits, err := h.app.ExternalWaits(t.Context(), as.TaskID)
			if err != nil || len(waits) != 1 || waits[0].WaitID != wait.WaitID || waits[0].Status != "waiting" || h.count("run_external_waits WHERE continuation_action_id IS NOT NULL") != 0 {
				t.Fatal("ineligible input woke the external wait", waits, err)
			}
		})
	}
}

func TestExternalWaitOwnerInboxKeepsUncertainNativeInputFenced(t *testing.T) {
	h, _, as, dependency := ownerInboxWaitFixture(t)
	message, request := queueOwnerWaitInput(t, h, as.Manager.ConversationID)
	d, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+as.Manager.ConversationID)
	if err != nil || !ok || d.RequestID != request {
		t.Fatal(d, ok, err)
	}
	turn := startHostedReview(t, h, as.Manager.ConversationID, d)
	if err := h.app.AwaitNativeReceipts(t.Context(), turn.ThreadID); err != nil {
		t.Fatal(err)
	}
	if err := h.app.SettleOpenTurns(t.Context(), turn.ThreadID); err != nil {
		t.Fatal(err)
	}
	if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Unknown}); err != nil {
		t.Fatal(err)
	}
	waitForTask(t, h, as, dependency, 0)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		ready, err := externalWaitOwnerInputReady(t.Context(), tx.Q(), as.TaskID)
		if err == nil && ready {
			t.Fatal("unconfirmed native execution was treated as unsent owner input")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if next, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+as.Manager.ConversationID); err != nil || ok {
		t.Fatal("waiting input bypassed the original native receipt", next, ok, err)
	}
	if pending, err := h.app.NativeReceiptPending(t.Context(), request); err != nil || !pending {
		t.Fatal("external wait erased the original unknown receipt", pending, err)
	}
	if current, ok, err := h.app.MainTurn(t.Context(), request); err != nil || !ok || current.MessageID != message || current.TurnID != turn.TurnID || current.Status != store.TurnInterrupted {
		t.Fatal("external wait replaced the original uncertain turn", current, ok, err)
	}
}

func routeMainWaitInput(t *testing.T, h *harness, as protocol.Assignment, completed bool) (Delivery, store.MainTurn, string) {
	t.Helper()
	message, request := queueOwnerWaitInput(t, h, "main")
	d, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
	if err != nil || !ok || d.RequestID != request {
		t.Fatal(d, ok, err)
	}
	turn := store.MainTurn{RequestID: request, MessageID: message, ThreadID: "routing-main", Status: store.TurnStarting}
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	turn.Status, turn.TurnID = store.TurnStarted, "routing-main-turn"
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	if op := h.associate(owner, message, as.Manager.IssueID); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if completed {
		turn.Status = store.TurnCompleted
		if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
			t.Fatal(err)
		}
		if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Delivered}); err != nil {
			t.Fatal(err)
		}
	}
	var carrier string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT o.request_id FROM background_sources s JOIN background_batches b USING(batch_id) JOIN outbox o ON json_extract(o.payload_json,'$.message_id')=b.message_id WHERE b.work_item_id=? AND s.message_id=? AND o.destination=? AND o.operation='main_thread.handle_event' AND o.status='pending'`, as.Manager.IssueID, message, "background_thread:"+as.Manager.ConversationID).Scan(&carrier)
	}); err != nil {
		t.Fatal("original input has no pending delivery to its Issue owner", err)
	}
	if carrier == request {
		t.Fatal("main and Issue must retain their distinct delivery identities")
	}
	return d, turn, carrier
}

func TestFault_ExternalWaitRoutedMainOwnerInput(t *testing.T) {
	for _, completed := range []bool{false, true} {
		t.Run(map[bool]string{false: "main-sending", true: "main-delivered"}[completed], func(t *testing.T) {
			h, _, as, dependency := ownerInboxWaitFixture(t)
			wait := waitForTask(t, h, as, dependency, 0)
			mainDelivery, mainTurn, carrier := routeMainWaitInput(t, h, as, completed)
			if err := h.app.SweepExternalWaits(t.Context()); err != nil {
				t.Fatal(err)
			}
			if h.lease(as.RunID).Status != store.LeaseActive || h.count("run_external_waits WHERE continuation_action_id IS NOT NULL") != 0 {
				t.Fatal("routing owner input released a live Issue process")
			}
			stopOwnerClaim(t, h, as)
			h.clock.Advance(5 * time.Second)
			for range 2 {
				if err := h.app.SweepExternalWaits(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			waits, err := h.app.ExternalWaits(t.Context(), as.TaskID)
			if err != nil || len(waits) != 1 || waits[0].WaitID != wait.WaitID || waits[0].Status != "continued" || waits[0].WakeReason != "owner_input" || h.count("run_external_waits WHERE continuation_action_id IS NOT NULL") != 1 {
				t.Fatal("main admission hid the pending Issue-owner input", waits, err)
			}
			if current, found, err := h.app.MainTurn(t.Context(), mainDelivery.RequestID); err != nil || !found || current != mainTurn {
				t.Fatal("Issue wake changed the original main turn", current, found, err)
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				var status string
				var attempts int
				if err := tx.Q().QueryRowContext(t.Context(), `SELECT status,attempts FROM outbox WHERE request_id=?`, carrier).Scan(&status, &attempts); err != nil {
					return err
				}
				if status != "pending" || attempts != 0 {
					t.Fatal("waking fabricated Issue delivery or consumption", status, attempts)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestExternalWaitRoutedInputUsesIssueAdmission(t *testing.T) {
	for _, state := range []string{"completed", "unknown", "rejected"} {
		t.Run(state, func(t *testing.T) {
			h, _, as, dependency := ownerInboxWaitFixture(t)
			_, _, carrier := routeMainWaitInput(t, h, as, true)
			d, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+as.Manager.ConversationID)
			if err != nil || !ok || d.RequestID != carrier {
				t.Fatal("fixture did not claim the Issue's actual carrier", d, ok, err)
			}
			if state == "rejected" {
				var payload HandleMessage
				if err := json.Unmarshal(d.Payload, &payload); err != nil {
					t.Fatal(err)
				}
				selected, named, err := h.app.NamedHostedSelection(t.Context(), as.Manager.ConversationID)
				if err != nil || !named {
					t.Fatal(named, err)
				}
				turn := store.MainTurn{RequestID: carrier, MessageID: payload.MessageID, ThreadID: "rejected-Issue-input", Hosted: &selected, Status: store.TurnStarting}
				if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
					t.Fatal(err)
				}
				turn.Status = store.TurnRejected
				if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
					t.Fatal(err)
				}
				if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Retry}); err != nil {
					t.Fatal(err)
				}
			} else {
				turn := startHostedReview(t, h, as.Manager.ConversationID, d)
				if state == "completed" {
					finishHostedReview(t, h, turn)
				} else {
					if err := h.app.AwaitNativeReceipts(t.Context(), turn.ThreadID); err != nil {
						t.Fatal(err)
					}
					if err := h.app.SettleOpenTurns(t.Context(), turn.ThreadID); err != nil {
						t.Fatal(err)
					}
					if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Unknown}); err != nil {
						t.Fatal(err)
					}
				}
			}
			waitForTask(t, h, as, dependency, 0)
			if state == "unknown" {
				if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
					ready, err := externalWaitOwnerInputReady(t.Context(), tx.Q(), as.TaskID)
					if err == nil && ready {
						t.Fatal("main completion bypassed the Issue's uncertain native carrier")
					}
					return err
				}); err != nil {
					t.Fatal(err)
				}
				if pending, err := h.app.NativeReceiptPending(t.Context(), carrier); err != nil || !pending {
					t.Fatal("Issue's original receipt fence was lost", pending, err)
				}
				if next, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+as.Manager.ConversationID); err != nil || ok {
					t.Fatal("uncertain Issue carrier was replayed", next, ok, err)
				}
				return
			}
			stopOwnerClaim(t, h, as)
			h.clock.Advance(5 * time.Second)
			if err := h.app.SweepExternalWaits(t.Context()); err != nil {
				t.Fatal(err)
			}
			waits, err := h.app.ExternalWaits(t.Context(), as.TaskID)
			want := "waiting"
			if state == "rejected" {
				want = "continued"
			}
			if err != nil || len(waits) != 1 || waits[0].Status != want {
				t.Fatal("Issue delivery admission was replaced with main's consumption", waits, err)
			}
		})
	}
}
