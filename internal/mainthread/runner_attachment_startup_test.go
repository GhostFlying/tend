package mainthread

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/coordination"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

type startupRunnerTransport struct {
	*runnerTransport
	reads     int
	processed chan struct{}
}

func (r *startupRunnerTransport) Events(ctx context.Context, cursor int64) ([]coordination.Event, error) {
	r.reads++
	if r.reads == 2 {
		close(r.processed)
	}
	return r.runnerTransport.Events(ctx, cursor)
}
func TestRunnerHistoricalEndRetainedBeforeAttachment(t *testing.T) {
	e := newEnv(t, testkit.NewHarness())
	e.stop()
	seedClosingReceiptMetadata(t, e, "completed", true)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	err := e.store.Write(ctx, func(tx *store.Tx) error {
		if err := tx.InsertRun(ctx, store.RunRow{RunID: "new-reader", TaskID: "task_cached", ActionID: "action_cached", Generation: 2, Role: protocol.RoleIssueManager, DesiredState: protocol.DesiredRun, ObservedState: protocol.RunUnknown}); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE runs SET native_thread_id='original-thread',native_thread_json=(SELECT native_thread_json FROM runs WHERE run_id='receipt-reader-run') WHERE run_id='new-reader'`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO manager_native_requests(request_id,run_id,method,payload_json,status,created_at) VALUES('new-close','new-reader','close','{}','pending',0)`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO manager_native_events(run_id,seq,body_json) VALUES('receipt-reader-run',1,'{"thread_id":"","disconnected":true}')`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	tr := &startupRunnerTransport{runnerTransport: &runnerTransport{app: e.app, run: "new-reader", origins: map[int64]managerEventOrigin{}}, processed: make(chan struct{})}
	s := coordination.New(ctx, tr)
	defer func() { cancel(); <-s.Done() }()
	select {
	case <-tr.processed:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	th, err := s.ResumeThread(ctx, "original-thread", harness.ThreadConfig{})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-th.Events():
		if event.Kind != harness.EventCoordinatorHistoryEnd || event.OriginRunID != "receipt-reader-run" || event.OriginSequence != 1 {
			t.Fatalf("wrong historical identity: %+v", event)
		}
		if err := th.(harness.EventAcknowledger).AcknowledgeEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("original historical end was dropped; unapplied events=%d", e.count(`SELECT COUNT(*) FROM manager_native_events WHERE applied=0`))
	}
	select {
	case <-s.Done():
		t.Fatal("historical boundary ended the current connection")
	default:
	}
	if e.count(`SELECT COUNT(*) FROM manager_native_events WHERE run_id='receipt-reader-run' AND seq=1 AND applied=1`) != 1 || e.count(`SELECT COUNT(*) FROM manager_native_requests`) != 2 {
		t.Fatal("history did not retain the exact original ACK or dispatched native work")
	}
}
func TestRunnerClosingTailRetainedBeforeAttachment(t *testing.T) {
	e := newEnv(t, testkit.NewHarness())
	e.stop()
	e.m.named = receiptOnlySelection()
	seedClosingReceiptMetadata(t, e, "completed", true)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	originalRequest := e.ids.New(protocol.PrefixRequest)
	if err := e.app.PutMainTurn(ctx, store.MainTurn{RequestID: originalRequest, MessageID: e.say("Retain the original native input"), ThreadID: "original-thread", TurnID: "original-turn", Status: store.TurnStarted}); err != nil {
		t.Fatal(err)
	}
	if err := e.app.SetMainThread(ctx, e.m.bindingInstance(), "original-thread"); err != nil {
		t.Fatal(err)
	}
	if err := e.store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO manager_native_events(run_id,seq,body_json) VALUES('receipt-reader-run',1,'{"thread_id":"original-thread","native":{"Kind":"request_usage"}}'),('receipt-reader-run',2,'{"thread_id":"","disconnected":true}')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE manager_native_requests SET status='started' WHERE request_id='close_cached'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	closePayload, _ := json.Marshal(protocol.ManagerNativeReply{ID: "close_cached", Result: json.RawMessage(`null`), LastSequence: 2})
	closeEnvelope := &protocol.Envelope{RequestID: e.ids.New(protocol.PrefixRequest), Operation: protocol.OpManagerNativeReply, Target: protocol.Target{Type: "run", ID: "receipt-reader-run"}, Payload: closePayload}
	reportClose := func() (protocol.Operation, error) {
		t.Helper()
		return e.app.Execute(ctx, protocol.Principal{Kind: protocol.PrincipalSystem}, closeEnvelope, app.ManagerNativeCommand{OperationName: protocol.OpManagerNativeReply})
	}
	if op, err := reportClose(); protocol.CodeOf(err) != protocol.CodeUnknownOutcome && (op.Error == nil || op.Error.Code != protocol.CodeUnknownOutcome) {
		t.Fatal("unapplied tail did not retain the original close guard", op, err)
	}
	stopped, _ := json.Marshal(protocol.ManagerStoppedPayload{Reason: "Original native process actually stopped", LastSequence: 2})
	op, err := e.app.Execute(ctx, protocol.Principal{Kind: protocol.PrincipalSystem}, &protocol.Envelope{RequestID: e.ids.New(protocol.PrefixRequest), Operation: protocol.OpManagerNativeStopped, Target: protocol.Target{Type: "run", ID: "receipt-reader-run"}, Payload: stopped}, app.ManagerNativeCommand{OperationName: protocol.OpManagerNativeStopped})
	if err != nil || op.Error != nil {
		t.Fatal(op, err)
	}
	s := coordination.New(ctx, &runnerTransport{app: e.app, run: "receipt-reader-run", origins: map[int64]managerEventOrigin{}})
	defer func() { cancel(); <-s.Done() }()
	select {
	case <-s.Done():
	case <-ctx.Done():
		t.Fatal("original event stream did not end", ctx.Err())
	}
	th, err := e.m.openThread(ctx, s)
	if err != nil {
		t.Fatalf("confirmed closing attachment cannot drain retained events (%d still unapplied): %v", e.count(`SELECT COUNT(*) FROM manager_native_events WHERE applied=0`), err)
	}
	applied := 0
	startupErr := drainStartupEvents(ctx, th.Events(), func(ctx context.Context, event harness.Event) error {
		applied++
		if event.OriginRunID != "receipt-reader-run" || event.OriginSequence != int64(applied) {
			t.Fatal("retained tail lost its original identity", event)
		}
		return th.(harness.EventAcknowledger).AcknowledgeEvent(ctx, event)
	}, func(ctx context.Context) error {
		_, err := th.(harness.ContextManager).ContextUsage(ctx)
		return err
	})
	if startupErr == nil {
		t.Fatal("ended startup was reported ready")
	}
	if applied != 2 {
		t.Fatal("startup observation preempted the retained tail", applied)
	}
	if op, err := reportClose(); err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal("original close could not settle after actual tail ACK", op, err)
	}
	if pending, err := e.app.NativeReceiptPending(ctx, originalRequest); err != nil || !pending || e.count(`SELECT COUNT(*) FROM agent_rollout_events WHERE kind='native_receipt'`) != 0 {
		t.Fatal("physical close fabricated the original business terminal", pending, err)
	}
	if _, err := th.StartTurn(ctx, harness.TurnInput{Text: "must not execute"}); err == nil {
		t.Fatal("ended attachment accepted new native input")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if e.count(`SELECT COUNT(*) FROM manager_native_requests`) != 1 || e.count(`SELECT COUNT(*) FROM manager_native_events WHERE applied=0`) != 0 {
		t.Fatal("local drain dispatched another native operation or lost its original ACK")
	}
}

func TestRunnerEndedAttachmentRejectsUnconfirmedMetadata(t *testing.T) {
	for _, test := range []struct {
		name, thread string
		closing      bool
	}{{"no_close", "original-thread", false}, {"different_thread", "another-thread", true}} {
		t.Run(test.name, func(t *testing.T) {
			e := newEnv(t, testkit.NewHarness())
			e.stop()
			seedClosingReceiptMetadata(t, e, "completed", test.closing)
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			if err := e.store.Write(ctx, func(tx *store.Tx) error {
				_, err := tx.Exec(ctx, `INSERT INTO manager_native_events(run_id,seq,body_json) VALUES('receipt-reader-run',1,'{"thread_id":"","disconnected":true}')`)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			before := e.count(`SELECT COUNT(*) FROM manager_native_requests`)
			s := coordination.New(ctx, &runnerTransport{app: e.app, run: "receipt-reader-run", origins: map[int64]managerEventOrigin{}})
			defer func() { cancel(); <-s.Done() }()
			select {
			case <-s.Done():
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if _, err := s.ResumeThread(ctx, test.thread, harness.ThreadConfig{}); !errors.Is(err, harness.ErrRejected) {
				t.Fatal("ended transport did not reject missing exact metadata", err)
			}
			if _, err := s.StartThread(ctx, harness.ThreadConfig{}); !errors.Is(err, harness.ErrRejected) {
				t.Fatal("ended transport started a new thread", err)
			}
			if e.count(`SELECT COUNT(*) FROM manager_native_requests`) != before {
				t.Fatal("local attachment dispatched native work")
			}
		})
	}
}
