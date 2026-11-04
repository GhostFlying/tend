package mainthread

import (
	"context"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/store"
)

// Capture only failed synthetic tests, without changing their deadline or result.
func logNamedPoolFailure(t *testing.T, e *env, pool *BackgroundPool, requests map[string]string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	pool.mu.Lock()
	active := make(map[string]*Manager, len(pool.active))
	for id, m := range pool.active {
		active[id] = m
	}
	t.Logf("named pool active=%d remote=%d database=%d sessions=%d starts=%+v", len(pool.active), len(pool.remote), len(pool.database), e.h.Sessions(), e.h.Starts())
	pool.mu.Unlock()
	for id, m := range active {
		m.mu.Lock()
		ready := m.thread != nil
		m.mu.Unlock()
		t.Logf("named conversation=%s ready=%t", id, ready)
	}
	for id, request := range requests {
		logical, err := e.app.LogicalTurn(ctx, request)
		t.Logf("named conversation=%s request=%s logical_status=%s reason=%s repairs=%d logical_error=%v", id, request, logical.Status, logical.Reason, logical.RepairAttempts, err)
		err = e.store.Read(ctx, func(tx *store.ReadTx) error {
			head, headErr := store.HeadOutboxPrioritized(ctx, tx.Q(), "background_thread:"+id, app.OpHandleEvent)
			t.Logf("named conversation=%s head=%s request=%s operation=%s status=%s attempts=%d next=%d head_error=%v", id, head.OutboxID, head.RequestID, head.Operation, head.Status, head.Attempts, head.NextAttemptAt, headErr)
			var pending int
			if err := tx.Q().QueryRowContext(ctx, `SELECT COUNT(*) FROM native_turn_receipts_pending WHERE request_id=?`, request).Scan(&pending); err != nil {
				return err
			}
			turn, turnErr := store.GetMainTurn(ctx, tx.Q(), request)
			t.Logf("named request=%s native_status=%s thread=%s turn=%s pending=%d native_error=%v", request, turn.Status, turn.ThreadID, turn.TurnID, pending, turnErr)
			return nil
		})
		if err != nil {
			t.Logf("named failure snapshot error=%v", err)
		}
	}
}
