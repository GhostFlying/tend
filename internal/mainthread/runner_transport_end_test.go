package mainthread

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/coordination"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestRunnerTransportPhysicalEndDrainsTailWithoutCompletingBusiness(t *testing.T) {
	e := newEnv(t, testkit.NewHarness())
	e.stop()
	seedClosingReceiptMetadata(t, e, "completed", true)
	message := e.say("original native input")
	request := e.ids.New(protocol.PrefixRequest)
	if err := e.app.PutMainTurn(t.Context(), store.MainTurn{RequestID: request, MessageID: message, ThreadID: "original-thread", TurnID: "original-turn", Status: store.TurnStarted}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	session := RunnerSession(ctx, e.app, "receipt-reader-run")
	thread, err := session.ResumeThread(ctx, "original-thread", harness.ThreadConfig{})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(coordination.Event{ThreadID: "original-thread", Native: &harness.Event{Kind: harness.EventItemStarted, TurnID: "original-turn", ItemID: "last-original-item"}})
	principal := protocol.Principal{Kind: protocol.PrincipalSystem}
	report := func(operation string, payload any) {
		t.Helper()
		raw, _ := json.Marshal(payload)
		op, err := e.app.Execute(ctx, principal, &protocol.Envelope{RequestID: e.ids.New(protocol.PrefixRequest), Operation: operation, Target: protocol.Target{Type: "run", ID: "receipt-reader-run"}, Payload: raw}, app.ManagerNativeCommand{OperationName: operation})
		if err != nil || op.Error != nil {
			t.Fatal(op, err)
		}
	}
	// Event delivery and physical-stop receipts race with the proxy's empty
	// reads. The real application enforces the final cursor before stopping.
	report(protocol.OpManagerNativeEvent, protocol.ManagerNativeEvent{Sequence: 1, Body: body})
	report(protocol.OpManagerNativeStopped, protocol.ManagerStoppedPayload{Reason: "Original process physically stopped", LastSequence: 1})
	select {
	case event := <-thread.Events():
		if event.ItemID != "last-original-item" || event.OriginRunID != "receipt-reader-run" || event.OriginSequence != 1 {
			t.Fatal("final source identity was lost", event)
		}
		if err := thread.(harness.EventAcknowledger).AcknowledgeEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("physical end lost the durable tail")
	}
	select {
	case <-session.Done():
	case <-time.After(300 * time.Millisecond):
		t.Fatal("ended carrier retained its observer indefinitely")
	}
	turn, ok, err := e.app.MainTurn(ctx, request)
	if err != nil || !ok || turn.Status != store.TurnStarted || e.count(`SELECT COUNT(*) FROM agent_rollout_events WHERE kind='native_receipt'`) != 0 {
		t.Fatal("transport EOF fabricated a native terminal", turn, err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if e.count(`SELECT COUNT(*) FROM manager_native_requests WHERE run_id='receipt-reader-run' AND method='close'`) != 1 {
		t.Fatal("observer exit duplicated native close")
	}
}

func TestRunnerTransportUnknownRemainsLiveWithoutInventingEvents(t *testing.T) {
	e := newEnv(t, testkit.NewHarness())
	e.stop()
	seedClosingReceiptMetadata(t, e, "completed", true)
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	_, err := (&runnerTransport{app: e.app, run: "receipt-reader-run"}).Events(ctx, 0)
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) {
		t.Fatal("unknown carrier was declared ended", err)
	}
}

func TestRunnerTransportPendingReadEndsWithItsPhysicalCarrier(t *testing.T) {
	e := newEnv(t, testkit.NewHarness())
	e.stop()
	seedClosingReceiptMetadata(t, e, "completed", false)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	session := RunnerSession(ctx, e.app, "receipt-reader-run")
	result := make(chan error, 1)
	go func() { _, err := session.(harness.ModelCatalog).Models(ctx); result <- err }()
	// There is no native provider response. The exact original physical stop
	// must release this observational caller as well as the event subscriber.
	raw, _ := json.Marshal(protocol.ManagerStoppedPayload{Reason: "Original process stopped", LastSequence: 0})
	op, err := e.app.Execute(ctx, protocol.Principal{Kind: protocol.PrincipalSystem}, &protocol.Envelope{RequestID: e.ids.New(protocol.PrefixRequest), Operation: protocol.OpManagerNativeStopped, Target: protocol.Target{Type: "run", ID: "receipt-reader-run"}, Payload: raw}, app.ManagerNativeCommand{OperationName: protocol.OpManagerNativeStopped})
	if err != nil || op.Error != nil {
		t.Fatal(op, err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, harness.ErrRejected) {
			t.Fatal("physical end did not reject the undelivered read", err)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("observational call kept an ended carrier attached")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestUpgradeDrainSeparatesPhysicalWorkFromModelReadiness(t *testing.T) {
	e := newEnv(t, testkit.NewHarness())
	e.stop()
	e.m.mu.Lock()
	e.m.session = nil
	e.m.thread = nil
	e.m.mu.Unlock()
	if drained, err := e.m.UpgradeDrainStatus(t.Context()); err != nil || !drained {
		t.Fatal("unused model prevented physical drain", drained, err)
	}
	e.m.mu.Lock()
	e.m.archiveStarting = true
	e.m.mu.Unlock()
	if drained, err := e.m.UpgradeDrainStatus(t.Context()); err != nil || drained {
		t.Fatal("actual startup disappeared from drain", drained, err)
	}
}

func TestRunnerTransportQueuedCloseEndRetainsUnknownOutcome(t *testing.T) {
	e := newEnv(t, testkit.NewHarness())
	e.stop()
	seedClosingReceiptMetadata(t, e, "completed", true)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	transport := &runnerTransport{app: e.app, run: "receipt-reader-run"}
	result := make(chan error, 1)
	// This exact request was queued by the fixture before the physical end.
	// Exercise the same post-admission waiter used by Call, not its separate
	// pre-admission "already stopped" fast path.
	go func() { result <- transport.awaitReply(ctx, "close", "close_cached", nil, nil) }()
	if err := e.store.Write(ctx, func(tx *store.Tx) error {
		return tx.UpdateRunObserved(ctx, "receipt-reader-run", protocol.RunFinished, e.m.cfg.Now().UnixMilli())
	}); err != nil {
		t.Fatal(err)
	}
	e.app.Changes().Notify()
	select {
	case err := <-result:
		if err == nil || errors.Is(err, harness.ErrRejected) {
			t.Fatal("lost physical close became a safe rejection", err)
		}
	case <-ctx.Done():
		t.Fatal("ended carrier retained pending business caller")
	}
	if e.count(`SELECT COUNT(*) FROM manager_native_requests WHERE run_id='receipt-reader-run' AND method='close' AND status IN ('pending','started','unknown')`) != 1 {
		t.Fatal("native operation was rewritten or resent")
	}
}
