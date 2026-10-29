package mainthread

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func prepareArchive(t *testing.T, e *env) {
	t.Helper()
	if _, err := e.app.ChangeArchivePreparation(t.Context(), protocol.ArchiveControlRequest{RequestID: e.ids.New(protocol.PrefixRequest)}, true); err != nil {
		t.Fatal(err)
	}
}

func TestArchiveDrainDoesNotRequireUsableModelOrReadyThread(t *testing.T) {
	e := newEnv(t, testkit.NewHarness())
	e.stop()
	e.say("retain this unexecuted input")
	prepareArchive(t, e)
	var opens atomic.Int64
	e.m.cfg.Open = func(context.Context) (harness.Session, error) {
		opens.Add(1)
		return nil, errors.New("model unavailable")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 80*time.Millisecond)
	defer cancel()
	_ = e.m.Run(ctx)
	if opens.Load() != 0 {
		t.Fatal("preparation retried a model for purely pending work")
	}
	if ready, err := e.m.ArchiveDrainStatus(t.Context()); err != nil || !ready {
		t.Fatal("model availability blocked draining", ready, err)
	}
	if len(e.h.Starts()) != 0 {
		t.Fatal("pending owner input ran")
	}
}

func TestArchiveDrainObservesActualStartupAndItsFailure(t *testing.T) {
	e := newEnv(t, testkit.NewHarness())
	e.stop()
	started := make(chan struct{})
	release := make(chan struct{})
	e.m.cfg.Open = func(context.Context) (harness.Session, error) {
		close(started)
		<-release
		return nil, errors.New("catalog unavailable")
	}
	done := make(chan error, 1)
	go func() { done <- e.m.serve(t.Context()) }()
	<-started
	prepareArchive(t, e)
	if ready, err := e.m.ArchiveDrainStatus(t.Context()); err != nil || ready {
		t.Fatal("startup not counted", ready, err)
	}
	close(release)
	if err := <-done; err == nil {
		t.Fatal("startup should fail")
	}
	if ready, err := e.m.ArchiveDrainStatus(t.Context()); err != nil || !ready {
		t.Fatal("failed startup without external work stayed blocked", ready, err)
	}
}

func TestArchiveReceiptAttachUsesOriginalThreadWithoutReplacement(t *testing.T) {
	e := newEnv(t, testkit.NewHarness())
	e.stop()
	source := e.say("old execution")
	session, err := e.h.Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	thread, err := session.StartThread(t.Context(), harness.ThreadConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.app.SetMainThread(t.Context(), e.m.bindingInstance(), thread.ID()); err != nil {
		t.Fatal(err)
	}
	if err := e.app.PutMainTurn(t.Context(), store.MainTurn{RequestID: e.ids.New(protocol.PrefixRequest), MessageID: source, ThreadID: thread.ID(), TurnID: "known-native", Status: store.TurnStarted}); err != nil {
		t.Fatal(err)
	}
	prepareArchive(t, e)
	needed, err := e.app.ArchiveNeedsReceiptRead(t.Context(), "main")
	if err != nil || !needed {
		t.Fatal(needed, err)
	}
	resumed, err := e.m.openThread(t.Context(), session)
	if err != nil || resumed.ID() != thread.ID() || e.h.Resumed() != 1 {
		t.Fatal("did not resume exact retained thread", resumed, err)
	}
	if len(e.h.Starts()) != 0 {
		t.Fatal("receipt attachment executed a model")
	}
	e.m.cfg.Instance = "missing-original"
	if _, err := e.m.openThread(t.Context(), session); err == nil {
		t.Fatal("missing original thread was replaced")
	}
	if len(e.h.Starts()) != 0 {
		t.Fatal("missing original fallback executed a model")
	}
}

// A stopped background manager is reattached only to read its original receipt,
// while a different unexecuted conversation remains pending.
func TestArchiveBackgroundPoolReconcilesOriginalReceiptWithoutNewTurn(t *testing.T) {
	e := newEnv(t, testkit.NewHarness())
	e.stop()
	ctx := t.Context()
	session, err := e.h.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	base, err := session.StartThread(ctx, harness.ThreadConfig{})
	if err != nil {
		t.Fatal(err)
	}
	const conversation = "work-item:receipt"
	const request = "old-background-request"
	if err := e.store.Write(ctx, func(tx *store.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO agent_conversations(conversation_id,created_at) VALUES(?,0)`, conversation); err != nil {
			return err
		}
		if _, err := tx.InsertMessage(ctx, store.MessageRow{ConversationID: conversation, MessageID: "receipt-owner", Author: "owner", Body: "Retained original"}); err != nil {
			return err
		}
		if err := tx.PutMainThread(ctx, "receipt-instance", base.ID(), 0); err != nil {
			return err
		}
		if err := tx.PutMainTurn(ctx, store.MainTurn{RequestID: request, MessageID: "receipt-owner", ThreadID: base.ID(), TurnID: "known-native", Status: store.TurnStarted}, 0); err != nil {
			return err
		}
		if err := tx.InsertOutbox(ctx, store.OutboxRow{OutboxID: "receipt-outbox", RequestID: request, Destination: store.ConversationDestination(conversation), Operation: app.OpHandleMessage, PayloadJSON: `{"message_id":"receipt-owner"}`}); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE outbox SET status='sending',attempts=1 WHERE outbox_id='receipt-outbox'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	prepareArchive(t, e)
	var starts atomic.Int64
	wrapper := &archiveReadSession{Session: session, thread: &receiptThread{Thread: base, found: true, receipt: harness.TurnReceipt{ID: "known-native", Status: "completed", Raw: []byte(`{"id":"known-native","status":"completed","items":[]}`)}}, starts: &starts}
	created := make(chan struct{}, 1)
	pool := &BackgroundPool{App: e.app, Front: e.m, New: func(id string) (*Manager, error) {
		if id != conversation {
			t.Errorf("unexpected conversation %s", id)
		}
		created <- struct{}{}
		return New(e.app, e.m.context, e.m.tools, Config{Instance: "receipt-instance", ConversationID: id, Open: func(context.Context) (harness.Session, error) { return wrapper, nil }, Log: quiet()}), nil
	}}
	run, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); pool.Run(run) }()
	defer func() { cancel(); <-done }()
	select {
	case <-created:
	case <-time.After(3 * time.Second):
		t.Fatal("paused pool blocked original receipt attachment")
	}
	eventually(t, "receipt reconciled while paused", func() bool {
		return e.count(`SELECT COUNT(*) FROM outbox WHERE outbox_id='receipt-outbox' AND status='delivered'`) == 1
	})
	if starts.Load() != 0 || len(e.h.Starts()) != 0 {
		t.Fatal("read-only receipt launched a model or replacement thread")
	}
	eventually(t, "receipt process joined", func() bool { drained, err := pool.ArchiveDrainStatus(ctx); return drained && err == nil })
}

type archiveReadSession struct {
	harness.Session
	thread harness.Thread
	starts *atomic.Int64
}

func (s *archiveReadSession) StartThread(context.Context, harness.ThreadConfig) (harness.Thread, error) {
	s.starts.Add(1)
	return nil, errors.New("new thread is forbidden during receipt reconciliation")
}
func (s *archiveReadSession) ResumeThread(_ context.Context, id string, _ harness.ThreadConfig) (harness.Thread, error) {
	if id != s.thread.ID() {
		return nil, harness.ErrRejected
	}
	return s.thread, nil
}
