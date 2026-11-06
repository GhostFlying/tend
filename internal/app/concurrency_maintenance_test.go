package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestConcurrencyMaintenanceWorkCadence(t *testing.T) {
	h, observed := newConcurrencyObservedHarness(t)
	concurrencyConfigure(t, h, "create", "shared", 0, protocol.ConfigureConcurrencyPayload{Name: "Shared", Min: 1, Initial: 1, Max: 1})
	*observed = nil
	var maintenance concurrencyMaintenance
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	for elapsed := time.Duration(0); elapsed <= time.Minute; elapsed += concurrencySampleInterval {
		if elapsed != 0 {
			h.clock.Advance(concurrencySampleInterval)
		}
		h.app.maintainConcurrency(t.Context(), log, &maintenance)
	}
	counts := map[store.WritePhase]int{}
	for _, observation := range *observed {
		if observation.Outcome != "ok" {
			t.Fatalf("maintenance failed: %+v", observation)
		}
		for _, phase := range observation.Phases {
			counts[phase.Phase]++
		}
	}
	if counts[store.WriteConcurrencySync] != 13 || counts[store.WriteConcurrencyWait] != 3 || counts[store.WriteConcurrencyGC] != 2 {
		t.Fatalf("one minute including startup: phases=%v", counts)
	}
	t.Logf("one minute including startup: samples=%d full_waiting_queries=%d history_transactions=%d", counts[store.WriteConcurrencySync], counts[store.WriteConcurrencyWait], counts[store.WriteConcurrencyGC])
}

func TestConcurrencySampleSkipsWaitingQuery(t *testing.T) {
	h := newHarness(t)
	concurrencyConfigure(t, h, "create", "shared", 0, protocol.ConfigureConcurrencyPayload{Name: "Shared", Min: 1, Initial: 1, Max: 1})
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `ALTER TABLE concurrency_waiters RENAME TO unavailable_waiters`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(concurrencySampleInterval)
	if err := h.app.tickConcurrency(t.Context(), false); err != nil {
		t.Fatalf("ordinary sample ran the unavailable waiting query: %v", err)
	}
	if err := h.app.tickConcurrency(t.Context(), true); err == nil {
		t.Fatal("demand refresh ignored the unavailable waiting query")
	}
}

func TestConcurrencyIncreaseRefreshesDemand(t *testing.T) {
	for _, demand := range []bool{false, true} {
		t.Run(fmt.Sprint(demand), func(t *testing.T) {
			h := newHarness(t)
			concurrencyConfigure(t, h, "create", "shared", 0, protocol.ConfigureConcurrencyPayload{Name: "Shared", Min: 1, Initial: 1, Max: 2})
			queueMessage(t, h, "new work")
			now := h.clock.Now().UnixMilli()
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				if _, err := tx.Exec(t.Context(), `INSERT INTO concurrency_bindings VALUES('env','worker','shared')`); err != nil {
					return err
				}
				source := protocol.ConcurrencySource{ExecutionID: "native", EnvironmentID: "env", RuntimeID: "worker", NativeSessionID: "session"}
				if err := putConcurrencyExecution(t.Context(), tx, source, "running", now); err != nil {
					return err
				}
				s, err := concurrencyState(t.Context(), tx.Q(), "shared")
				if err != nil {
					return err
				}
				s.Group.ObservationSince = now - domain.ConcurrencyWindow.Milliseconds()
				s.Group.Waiting = 1 // Previously observed demand, possibly now stale.
				s.LastSample, s.LastRunning, s.LastHealthy = now-5000, 1, true
				s.Intervals = []domain.ConcurrencyInterval{{Start: now - domain.ConcurrencySaturation.Milliseconds(), End: now - 5000}}
				s.Successes = []int64{now - 1000}
				if demand {
					if _, err := tx.Exec(t.Context(), `INSERT INTO concurrency_waiters SELECT request_id,'env','worker',? FROM outbox WHERE status='pending' LIMIT 1`, now); err != nil {
						return err
					}
				}
				return saveConcurrency(t.Context(), tx, s)
			}); err != nil {
				t.Fatal(err)
			}
			// The normal demand cadence is not due, but an increase requires
			// a fresh authoritative waiting calculation in this transaction.
			if err := h.app.tickConcurrency(t.Context(), false); err != nil {
				t.Fatal(err)
			}
			g, err := h.app.ConcurrencyGroup(t.Context(), "shared")
			want := int64(1)
			if demand {
				want = 2
			}
			if err != nil || g.Current != want || (g.Waiting > 0) != demand {
				t.Fatalf("demand=%v group=%+v err=%v", demand, g, err)
			}
		})
	}
}

func TestConcurrencyMaintenanceCadenceAndRetry(t *testing.T) {
	h := newHarness(t)
	concurrencyConfigure(t, h, "create", "shared", 0, protocol.ConfigureConcurrencyPayload{Name: "Shared", Min: 1, Initial: 1, Max: 1})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	var maintenance concurrencyMaintenance
	h.app.maintainConcurrency(t.Context(), log, &maintenance)
	firstWaiting, firstHistory := maintenance.waitingAt, maintenance.historyAt
	if firstWaiting.IsZero() || firstHistory.IsZero() {
		t.Fatal("startup skipped maintenance")
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `ALTER TABLE concurrency_waiters RENAME TO unavailable_waiters`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(concurrencySampleInterval)
	h.app.maintainConcurrency(t.Context(), log, &maintenance)
	if maintenance.waitingAt != firstWaiting || maintenance.historyAt != firstHistory {
		t.Fatal("ordinary sample repeated maintenance")
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		s, err := concurrencyState(t.Context(), tx.Q(), "shared")
		if err == nil && s.LastSample != h.clock.Now().UnixMilli() {
			t.Error("waiting work prevented the ordinary sample")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(concurrencyWaitingInterval - concurrencySampleInterval)
	h.app.maintainConcurrency(t.Context(), log, &maintenance)
	if maintenance.waitingAt != firstWaiting {
		t.Fatal("failed demand refresh advanced its cadence")
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `ALTER TABLE unavailable_waiters RENAME TO concurrency_waiters`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(concurrencySampleInterval)
	h.app.maintainConcurrency(t.Context(), log, &maintenance)
	if maintenance.waitingAt != h.clock.Now() || maintenance.historyAt != firstHistory {
		t.Fatal("demand retry or history cadence changed")
	}
	h.clock.Set(firstWaiting.Add(-time.Second))
	h.app.maintainConcurrency(t.Context(), log, &maintenance)
	if maintenance.waitingAt != h.clock.Now() || maintenance.historyAt != h.clock.Now() {
		t.Fatal("clock rollback did not refresh maintenance")
	}
}

func TestConcurrencyHistoryFailureDoesNotRollBackSample(t *testing.T) {
	h := newHarness(t)
	concurrencyConfigure(t, h, "create", "shared", 0, protocol.ConfigureConcurrencyPayload{Name: "Shared", Min: 1, Initial: 1, Max: 1})
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO concurrency_changes(group_id,at,reason,snapshot_json) VALUES('shared',1,'old','{}');
CREATE TEMP TRIGGER refuse_history_cleanup BEFORE DELETE ON concurrency_changes BEGIN SELECT RAISE(ABORT,'cleanup unavailable'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var maintenance concurrencyMaintenance
	h.clock.Advance(concurrencySampleInterval)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h.app.maintainConcurrency(t.Context(), log, &maintenance)
	if maintenance.waitingAt != h.clock.Now() || !maintenance.historyAt.IsZero() {
		t.Fatal("cleanup failure changed sample or cleanup cadence")
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		s, err := concurrencyState(t.Context(), tx.Q(), "shared")
		if err == nil && s.LastSample != h.clock.Now().UnixMilli() {
			t.Error("cleanup failure rolled back the sample")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `DROP TRIGGER refuse_history_cleanup`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(concurrencySampleInterval)
	h.app.maintainConcurrency(t.Context(), log, &maintenance)
	if maintenance.historyAt != h.clock.Now() {
		t.Fatal("cleanup was not retried after failure")
	}
}

func TestConcurrencyHistoryCleanupIsBounded(t *testing.T) {
	h := newHarness(t)
	concurrencyConfigure(t, h, "create", "shared", 0, protocol.ConfigureConcurrencyPayload{Name: "Shared", Min: 1, Initial: 1, Max: 1})
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		for i := 0; i <= concurrencyHistoryBatch; i++ {
			if _, err := tx.Exec(t.Context(), `INSERT INTO concurrency_feedback VALUES(?,'shared',0,1,'{"native":"old"}')`, fmt.Sprintf("feedback-%d", i)); err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO concurrency_changes(group_id,at,reason,snapshot_json) VALUES('shared',1,'old','{}')`); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []int{1, 0} {
		if err := h.app.cleanConcurrencyHistory(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			var feedback, pending, changes int
			if err := tx.Q().QueryRowContext(t.Context(), `SELECT count(*),sum(source_json<>'{}') FROM concurrency_feedback`).Scan(&feedback, &pending); err != nil {
				return err
			}
			if err := tx.Q().QueryRowContext(t.Context(), `SELECT count(*) FROM concurrency_changes WHERE at=1`).Scan(&changes); err != nil {
				return err
			}
			if feedback != concurrencyHistoryBatch+1 || pending != want || changes != want {
				t.Errorf("feedback identities=%d pending=%d changes=%d want pending=%d", feedback, pending, changes, want)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConcurrencyLoopStopsWithContext(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.app.RunConcurrency(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("controller waited for its next maintenance tick after cancellation")
	}
}
