package mainthread

import (
	"encoding/json"
	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
	"testing"
)

func TestReviewRecoverAlreadyPersistedCompactCompletion(t *testing.T) {
	e := newEnv(t, testkit.NewHarness())
	e.stop()
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO agent_compactions(operation_id,conversation_id,thread_id,status,source,created_at,updated_at) VALUES('manual-op','main','native-thread','running','manual',0,0)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.app.RecordRolloutEvent(t.Context(), "main", "native-thread", "compact-turn", "compact-item", string(harness.EventItemCompleted), json.RawMessage(`{"id":"compact-item","type":"contextCompaction"}`)); err != nil {
		t.Fatal(err)
	}
	d := app.Delivery{Operation: "conversation.compact", Payload: json.RawMessage(`{"operation_id":"manual-op"}`)}
	r := e.m.Reconcile(t.Context(), d)
	if r.Outcome != app.Delivered {
		t.Fatalf("durable completion was not reconciled: %+v", r)
	}
}

func TestReviewThreadRegistrationFailureMustDetachPublishedThread(t *testing.T) {
	e := newEnv(t, testkit.NewHarness())
	e.stop()
	if err := e.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `CREATE TRIGGER review_fail_registration BEFORE INSERT ON agent_thread_history BEGIN SELECT RAISE(FAIL,'registration write failed'); END;`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	err := e.m.serve(t.Context())
	if err == nil {
		t.Fatal("expected registration failure")
	}
	e.m.mu.Lock()
	stale := e.m.thread != nil
	e.m.mu.Unlock()
	if stale {
		t.Fatalf("published thread not detached after serve error: %v", err)
	}
}

func TestReviewStartupHandoffWaitsForCompactReconciliation(t *testing.T) {
	for _, status := range []string{"starting", "running", "unknown"} {
		t.Run(status, func(t *testing.T) {
			f := newHandoffFixture(t)
			if err := f.e.store.Write(t.Context(), func(tx *store.Tx) error {
				_, err := tx.Exec(t.Context(), `INSERT INTO agent_compactions(operation_id,conversation_id,thread_id,status,source,created_at,updated_at) VALUES('compact-before-restart','main',?,?,'manual',0,0)`, f.source.ID(), status)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := f.e.m.openManagedThread(t.Context(), f.session); err == nil {
				t.Fatal("startup migrated an unresolved native compact")
			}
			binding, ok, err := f.e.app.MainThreadBinding(t.Context(), "main")
			if err != nil || !ok || binding.ThreadID != f.source.ID() {
				t.Fatalf("startup lost the original compact thread: %+v, %v", binding, err)
			}
			if _, pending, err := f.e.app.PendingHandoff(t.Context(), "main"); err != nil || pending {
				t.Fatalf("startup prepared a handoff across unresolved compact: pending=%t err=%v", pending, err)
			}
			if err := f.e.app.RecordRolloutEvent(t.Context(), "main", f.source.ID(), "native-compact-turn", "native-compact-item", string(harness.EventItemCompleted), json.RawMessage(`{"id":"native-compact-item","type":"contextCompaction"}`)); err != nil {
				t.Fatal(err)
			}
			if done, err := f.e.app.ReconcileCompactEvidence(t.Context(), "compact-before-restart"); err != nil || done == "" {
				t.Fatalf("completion could not be reconciled: thread=%s err=%v", done, err)
			}
			if _, err := f.e.m.openManagedThread(t.Context(), f.session); err != nil {
				t.Fatalf("confirmed compact still prevents schema handoff: %v", err)
			}
		})
	}
}
