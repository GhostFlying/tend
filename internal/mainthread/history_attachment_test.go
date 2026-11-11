package mainthread

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

type preparedHistorySession struct{ harness.Session }

func (s preparedHistorySession) ResumeThread(ctx context.Context, id string, cfg harness.ThreadConfig) (harness.Thread, error) {
	th, err := s.Session.ResumeThread(ctx, id, cfg)
	if err != nil || cfg.Purpose != harness.ReceiptHistoryPurpose {
		return th, err
	}
	return &receiptThread{Thread: th, found: true, receipt: harness.TurnReceipt{ID: "original-turn", Status: "completed", Raw: json.RawMessage(`{"id":"original-turn","status":"completed","items":[]}`)}}, nil
}

func startPreparedHistory(t *testing.T, loop bool) (*env, context.CancelFunc, <-chan error) {
	t.Helper()
	e := newEnv(t, testkit.NewHarness())
	e.stop()
	msg := e.say("original native input")
	native, err := e.h.Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	base, err := native.StartThread(t.Context(), harness.ThreadConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.app.SetMainThread(t.Context(), e.m.bindingInstance(), base.ID()); err != nil {
		t.Fatal(err)
	}
	if err := native.Close(); err != nil {
		t.Fatal(err)
	}
	if err := e.app.PutMainTurn(t.Context(), store.MainTurn{RequestID: e.ids.New(protocol.PrefixRequest), MessageID: msg, ThreadID: base.ID(), TurnID: "original-turn", Status: store.TurnStarted}); err != nil {
		t.Fatal(err)
	}
	prepareArchive(t, e)
	e.m.cfg.Open = func(ctx context.Context) (harness.Session, error) {
		s, err := e.h.Open(ctx)
		return preparedHistorySession{s}, err
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		if loop {
			done <- e.m.Run(ctx)
		} else {
			done <- e.m.serve(ctx)
		}
	}()
	ready, stop := context.WithTimeout(ctx, 3*time.Second)
	defer stop()
	if _, err := e.m.waitThread(ready); err != nil {
		cancel()
		<-done
		t.Fatal(err)
	}
	return e, cancel, done
}

func TestPreparedHistoryReopensNormalExecutionAfterCancellation(t *testing.T) {
	e, cancel, done := startPreparedHistory(t, true)
	defer func() { cancel(); <-done }()
	p, err := e.app.ArchivePreparation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.app.ChangeArchivePreparation(t.Context(), protocol.ArchiveControlRequest{RequestID: e.ids.New(protocol.PrefixRequest), ExpectedControlRevision: p.Revision}, false); err != nil {
		t.Fatal(err)
	}
	eventually(t, "ordinary attachment replaces history", func() bool {
		e.m.mu.Lock()
		defer e.m.mu.Unlock()
		return e.m.thread != nil && !e.m.historyAttachment
	})
	msg := e.say("authorized continuation after cancellation")
	raw, _ := json.Marshal(app.HandleMessage{MessageID: msg})
	ctx, stop := context.WithTimeout(t.Context(), 3*time.Second)
	defer stop()
	result := e.m.Deliver(ctx, app.Delivery{RequestID: e.ids.New(protocol.PrefixRequest), Operation: app.OpHandleMessage, Payload: raw})
	if result.Outcome != app.Delivered || len(e.h.Starts()) != 1 {
		t.Fatal("normal continuation did not resume after history", result, e.h.Starts())
	}
}

func TestHistoryAttachmentObservesNativeSessionExit(t *testing.T) {
	e, cancel, done := startPreparedHistory(t, false)
	defer cancel()
	e.m.mu.Lock()
	s := e.m.session
	e.m.mu.Unlock()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("native exit lost its failure")
		}
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("history attachment stayed ready after native exit")
	}
}
