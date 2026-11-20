package app

import (
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestManagerHistorySelectionRequiresClosedOriginalRun(t *testing.T) {
	h, original, pending := stoppedRetainedManager(t)
	p := receiptReaderPayload(t, h, original)
	if op := h.exec(owner, h.env(protocol.OpReconcileManagerRun, taskTarget(original.TaskID), p, rev(h.task(original.TaskID).ControlRevision)), ReconcileManagerRun{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	ctx := t.Context()
	newer := h.run(original.RunID)
	newer.RunID = h.ids.New(protocol.PrefixRun)
	newer.Generation++
	newer.CreatedAt++
	if err := h.store.Write(ctx, func(tx *store.Tx) error {
		if err := tx.InsertRun(ctx, newer); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO manager_native_events(run_id,seq,body_json) VALUES(?,1,'{}')`, newer.RunID)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	assertSelection := func(want string, requiresHistory bool) {
		t.Helper()
		selected, named, err := h.app.NamedHostedSelection(ctx, original.Manager.ConversationID)
		if err != nil || !named || selected.RunnerRunID != want || selected.RequireReceiptReader != requiresHistory || selected.ReceiptOnly != requiresHistory {
			t.Fatalf("runner selection = (%+v, %v, %v), want run %q with history %v", selected, named, err, want, requiresHistory)
		}
		for _, id := range []string{original.RunID, newer.RunID} {
			if ended, err := h.app.ManagerTransportEnded(ctx, id); err != nil || ended != (id != want) {
				t.Fatalf("manager transport for %q: ended=%v, err=%v, selected=%q", id, ended, err, want)
			}
		}
	}
	write := func(query string, args ...any) {
		t.Helper()
		if err := h.store.Write(ctx, func(tx *store.Tx) error {
			_, err := tx.Exec(ctx, query, args...)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}

	// The event-only Run must have physical close proof for its current activity.
	assertSelection(original.RunID, true)
	write(`UPDATE runs SET native_closed_at=1,native_activity=2,native_close_activity=1 WHERE run_id=?`, newer.RunID)
	assertSelection(original.RunID, true)
	write(`UPDATE runs SET native_close_activity=native_activity WHERE run_id=?`, newer.RunID)
	assertSelection(newer.RunID, true)
	resolveRetainedBusinessReceipt(t, h, pending)
	assertSelection(newer.RunID, true)

	// A pending request without a corresponding execution is not a match.
	write(`INSERT INTO main_turns(request_id,message_id,thread_id,turn_id,status,updated_at,created_at)
		SELECT 'unmatched-test-request',message_id,thread_id,'other-turn',status,updated_at,created_at
		FROM main_turns WHERE request_id=?`, pending)
	write(`INSERT INTO native_turn_receipts_pending(request_id) VALUES('unmatched-test-request')`)
	write(`UPDATE manager_native_events SET applied=1 WHERE run_id=?`, newer.RunID)
	assertSelection("", false)
}
