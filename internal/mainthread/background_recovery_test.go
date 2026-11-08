package mainthread

import (
	"context"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

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
	conversation := createConsultationFixture(t, e).ID
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
	e.exec(owner, "conversation.submit_message", protocol.Target{Type: "conversation"}, protocol.SubmitMessagePayload{ConversationID: conversation, Body: "Wait through the upgrade gate"}, app.SubmitMessage{})

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
