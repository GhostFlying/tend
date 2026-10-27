package mainthread

import (
	"context"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

func TestBackgroundRecoveryWaitsForStartupSettlement(t *testing.T) {
	e := newEnv(t, testkit.NewHarness())
	e.stop()
	const id = "work-item:recovery"
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO agent_conversations(conversation_id,created_at) VALUES(?,0)`, id); err != nil {
			return err
		}
		if _, err := tx.InsertMessage(t.Context(), store.MessageRow{ConversationID: id, MessageID: "bg-message", Author: "owner", Body: "inspect"}); err != nil {
			return err
		}
		if err := tx.PutMainThread(t.Context(), "background-recovery", "old-thread", 0); err != nil {
			return err
		}
		if err := tx.PutMainTurn(t.Context(), store.MainTurn{RequestID: "bg-request", MessageID: "bg-message", ThreadID: "old-thread", TurnID: "old-turn", Status: store.TurnStarted}, 0); err != nil {
			return err
		}
		if err := tx.InsertOutbox(t.Context(), store.OutboxRow{OutboxID: "bg-outbox", RequestID: "bg-request", Destination: store.ConversationDestination(id), Operation: app.OpHandleMessage, PayloadJSON: `{"message_id":"bg-message"}`}); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE outbox SET status='sending',attempts=1 WHERE outbox_id='bg-outbox'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	m := New(e.app, e.m.context, e.m.tools, Config{Instance: "background-recovery", ConversationID: id, Log: quiet(), Open: func(ctx context.Context) (harness.Session, error) {
		close(entered)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return e.h.Open(ctx)
		}
	}})
	p := BackgroundPool{App: e.app}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); p.drain(ctx, id, m) }()
	t.Cleanup(func() { cancel(); <-done })
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("recovery did not start")
	}
	select {
	case <-done:
		t.Fatal("delivery acknowledged before startup settled the old turn")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("recovery did not drain")
	}
	rec, _, err := e.app.MainTurn(t.Context(), "bg-request")
	if err != nil || rec.Status != store.TurnInterrupted {
		t.Fatalf("abandoned turn: %+v, %v", rec, err)
	}
}

func TestBackgroundControlReconcileDoesNotRedeliverInterrupt(t *testing.T) {
	e := newEnv(t, testkit.NewHarness())
	e.stop()
	for _, id := range []string{"main", "work-item:recovery"} {
		if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
			if _, err := tx.Exec(t.Context(), `INSERT OR IGNORE INTO agent_conversations(conversation_id,created_at) VALUES(?,0)`, id); err != nil {
				return err
			}
			if _, err := tx.InsertMessage(t.Context(), store.MessageRow{ConversationID: id, MessageID: id, Author: "owner", Body: "inspect"}); err != nil {
				return err
			}
			return tx.PutMainTurn(t.Context(), store.MainTurn{RequestID: id, MessageID: id, ThreadID: "old-thread", TurnID: "old-turn", Status: store.TurnStarted}, 0)
		}); err != nil {
			t.Fatal(err)
		}
		d := app.Delivery{Payload: []byte(`{"message_id":"` + id + `"}`)}
		p := BackgroundPool{App: e.app, Front: e.m}
		if got := p.Reconcile(t.Context(), d); got.Outcome != app.Unknown {
			t.Fatalf("%s reconciliation = %+v", id, got)
		}
	}
}

func TestUpgradeGateStopsCompactAndBackgroundAdmission(t *testing.T) {
	e := newEnv(t, testkit.NewHarness())
	e.stop()
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		return tx.PutUpgrade(t.Context(), store.UpgradeRow{View: protocol.UpgradeView{UpgradeID: "upgrade", ReleaseID: "release", Revision: 1, Status: "running", Phase: "server_draining"}})
	}); err != nil {
		t.Fatal(err)
	}
	// The gate must be checked before waiting for a native thread or reading
	// the compact request, otherwise this delivery can begin after drain.
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	r := e.m.Deliver(ctx, app.Delivery{Operation: "conversation.compact", Payload: []byte(`{"operation_id":"queued"}`)})
	if r.Outcome != app.Retry || r.Reason != "mainthread: waiting for server upgrade" {
		t.Fatalf("compact crossed upgrade gate: %+v", r)
	}
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO work_items(work_item_id,project,revision,status,body_json,created_at,updated_at) SELECT 'queued',project,1,'open','{}',0,0 FROM projects LIMIT 1`); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE work_item_contexts SET baseline_revision=1 WHERE work_item_id='queued'`); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO agent_conversations(conversation_id,work_item_id,created_at) VALUES('work-item:queued','queued',0)`); err != nil {
			return err
		}
		return tx.InsertOutbox(t.Context(), store.OutboxRow{OutboxID: "queued", RequestID: "queued", Destination: store.ConversationDestination("work-item:queued"), Operation: app.OpHandleMessage, PayloadJSON: `{}`})
	}); err != nil {
		t.Fatal(err)
	}
	ids, err := e.app.PendingAgentConversations(t.Context())
	if err != nil || len(ids) != 1 {
		t.Fatal(ids, err)
	}
	started := make(chan struct{}, 1)
	p := BackgroundPool{App: e.app, New: func(string) (*Manager, error) { started <- struct{}{}; return e.m, nil }}
	run, stop := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer stop()
	p.Run(run)
	select {
	case <-started:
		t.Fatal("background process started during upgrade")
	default:
	}
	p.active = map[string]*Manager{"background": e.m}
	if drained, err := p.UpgradeDrainStatus(t.Context()); err != nil || drained {
		t.Fatal("active process reported drained", drained, err)
	}
	delete(p.active, "background")
	if drained, err := p.UpgradeDrainStatus(t.Context()); err != nil || !drained {
		t.Fatal(drained, err)
	}
}
