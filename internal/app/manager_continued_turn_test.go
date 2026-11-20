package app

import (
	"encoding/json"
	"testing"
	"time"

	nativeharness "github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/coordination"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestContinuedIssueOwnerReceiptsAndUrgentInput(t *testing.T) {
	h, _, issue, as, _ := reviewManagerAssignment(t)
	turn, _ := startHostedIssueReview(t, h, issue)
	event := func(seq int64, kind, id, root string, continued bool) {
		t.Helper()
		body, err := json.Marshal(coordination.Event{ThreadID: turn.ThreadID, RootTurnID: root, Continued: continued, Native: &nativeharness.Event{Kind: nativeharness.EventKind(kind), TurnID: id, TurnStatus: "completed"}})
		if err != nil {
			t.Fatal(err)
		}
		op := h.runAs(as, protocol.OpManagerNativeEvent, protocol.ManagerNativeEvent{Sequence: seq, Body: body}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeEvent})
		if op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	event(1, "turn_completed", turn.TurnID, "", true)
	current, waiting, _, err := h.app.NativeReceiptSource(t.Context(), as.Manager.ConversationID, turn.RequestID)
	if err != nil || !waiting || current != turn.TurnID {
		t.Fatal("first completion prematurely settled its logical target", current, waiting, err)
	}
	terminal := turn
	terminal.Status = store.TurnCompleted
	if err := h.app.ObserveNativeTurnTerminal(t.Context(), terminal); protocol.CodeOf(err) != protocol.CodeUnknownOutcome {
		t.Fatal("accepted root terminal during local start", err)
	}
	event(2, "turn_started", "continued-native-turn", turn.TurnID, false)
	current, waiting, _, err = h.app.NativeReceiptSource(t.Context(), as.Manager.ConversationID, turn.RequestID)
	if err != nil || waiting || current != "continued-native-turn" {
		t.Fatal("wrong native target after restart", current, waiting, err)
	}
	if err := h.app.ObserveNativeTurnTerminal(t.Context(), terminal); protocol.CodeOf(err) != protocol.CodeUnknownOutcome {
		t.Fatal("accepted earlier turn after child start", err)
	}
	source := queueMessage(t, h, "Urgent change to the current goal")
	linkIssueSteerSource(t, h, issue.IssueID, source)
	notice := steerOperation(t, h.exec(owner, urgentTestEnvelope(t, h, issue.IssueID, as.RunID, source, ""), SteerIssueOwner{}), "queued")
	dispatch, err := h.app.ClaimIssueOwnerSteer(t.Context(), turn.RequestID, as.RunID, turn.ThreadID, turn.TurnID, true)
	if err != nil || dispatch.MessageID != notice.MessageID {
		t.Fatal(dispatch, err)
	}
	raw, err := json.Marshal(coordination.Request{ThreadID: turn.ThreadID, TurnID: "continued-native-turn", Input: nativeharness.TurnInput{Text: dispatch.Text, UserMessageID: dispatch.MessageID}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.app.QueueManagerNative(t.Context(), as.RunID, "steer", raw)
	if err != nil {
		t.Fatalf("continued turn cannot receive the claimed urgent notice: %v", err)
	}
	if err := h.app.ObserveContinuationTerminal(t.Context(), terminal, "continued-native-turn"); err != nil {
		t.Fatal("could not reconcile the actual child terminal", err)
	}
}

func TestDeliveryExecutionRemainsCurrentAfterDisconnectedTTL(t *testing.T) {
	h := newHarness(t)
	_, task := h.createTask(owner, createTaskPayload{Title: "delegated delivery guard", Spec: testSpec})
	as, ok, _ := h.claim("linux-1")
	if !ok {
		t.Fatal("no claim")
	}
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	read := func() bool {
		var current bool
		err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			taskRow, err := store.GetTask(t.Context(), tx.Q(), task.TaskID)
			if err != nil {
				return err
			}
			current, err = currentDeliveryExecutionLease(t.Context(), tx.Q(), taskRow, h.clock.Now().UnixMilli())
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return current
	}
	if !read() {
		t.Fatal("fixture has no current execution before partition")
	}
	h.clock.Advance(DefaultLeaseTTL + time.Second)
	if expired, err := h.app.ExpireLeases(t.Context()); err != nil || expired != 0 {
		t.Fatal(expired, err)
	}
	if !h.lease(as.RunID).Current(h.clock.Now().UnixMilli()) {
		t.Fatal("delegated authority expired")
	}
	if !read() {
		t.Fatal("valid protocol-seven execution rejected by delivery lease guard solely after old TTL")
	}
}
