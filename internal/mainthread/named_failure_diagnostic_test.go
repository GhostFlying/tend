package mainthread

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/testkit"
)

// Capture only failed synthetic tests, without changing their deadline or result.
func logNamedPoolFailure(t *testing.T, e *env, pool *BackgroundPool, requests map[string]string) {
	t.Helper()
	if e.trace != nil {
		e.trace.log(t)
	} else {
		logNamedGoroutines(t)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if !pool.mu.TryLock() {
		t.Log("named pool snapshot unavailable: pool lock is busy")
		return
	}
	active := make(map[string]*Manager, len(pool.active))
	for id, m := range pool.active {
		active[id] = m
	}
	t.Logf("named pool active=%d remote=%d database=%d sessions=%d starts=%+v", len(pool.active), len(pool.remote), len(pool.database), e.h.Sessions(), e.h.Starts())
	pool.mu.Unlock()
	for id, m := range active {
		if !m.mu.TryLock() {
			t.Logf("named conversation=%s readiness unavailable: manager lock is busy", id)
			continue
		}
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

func TestNamedFailureDiagnosticDoesNotWaitForPoolOrManagerLocks(t *testing.T) {
	for _, lock := range []string{"pool", "manager"} {
		t.Run(lock, func(t *testing.T) {
			m := &Manager{}
			pool := &BackgroundPool{active: map[string]*Manager{"synthetic-conversation": m}}
			var unlock func()
			if lock == "pool" {
				pool.mu.Lock()
				unlock = pool.mu.Unlock
			} else {
				m.mu.Lock()
				unlock = m.mu.Unlock
			}
			done := make(chan struct{})
			go func() { defer close(done); logNamedPoolFailure(t, &env{h: testkit.NewHarness()}, pool, nil) }()
			select {
			case <-done:
				unlock()
			case <-time.After(time.Second):
				unlock()
				<-done
				t.Fatal("failure diagnostic waited for a lock held by the failed execution")
			}
		})
	}
}

// Each synthetic env owns its bounded observations. Capture never writes logs
// or holds the database writer; successful tests emit nothing.
type namedFailureTrace struct {
	mu                    sync.Mutex
	started               time.Time
	tools                 [64]namedToolPhase
	writes                [128]store.WriteObservation
	toolCount, writeCount int
	logged                bool
}
type namedToolPhase struct {
	At                        time.Duration
	Thread, Turn, Tool, Phase string
}

func newNamedFailureTrace() *namedFailureTrace { return &namedFailureTrace{started: time.Now()} }
func (d *namedFailureTrace) tool(thread, turn, tool string) func() {
	record := func(phase string) {
		d.mu.Lock()
		d.tools[d.toolCount%len(d.tools)] = namedToolPhase{At: time.Since(d.started), Thread: thread, Turn: turn, Tool: tool, Phase: phase}
		d.toolCount++
		d.mu.Unlock()
	}
	record("enter")
	return func() { record("exit") }
}
func (d *namedFailureTrace) write(o store.WriteObservation) {
	d.mu.Lock()
	d.writes[d.writeCount%len(d.writes)] = o
	d.writeCount++
	d.mu.Unlock()
}
func (d *namedFailureTrace) log(t *testing.T) {
	t.Helper()
	if !d.mu.TryLock() {
		t.Log("named phase observations unavailable: capture lock busy")
		logNamedGoroutines(t)
		return
	}
	if d.logged {
		d.mu.Unlock()
		return
	}
	d.logged = true
	tools, writes, toolCount, writeCount := d.tools, d.writes, d.toolCount, d.writeCount
	d.mu.Unlock()
	for i := max(0, toolCount-len(tools)); i < toolCount; i++ {
		t.Logf("named tool phase=%+v", tools[i%len(tools)])
	}
	for i := max(0, writeCount-len(writes)); i < writeCount; i++ {
		o := writes[i%len(writes)]
		t.Logf("named writer caller=%s operation=%s stage=%s outcome=%s total=%s connection_wait=%s callback=%s", o.Caller, o.Source.Operation, o.Stage, o.Outcome, o.Total, o.ConnectionWait, o.Callback)
	}
	logNamedGoroutines(t)
}
func logNamedGoroutines(t *testing.T) {
	t.Helper()
	stack := make([]byte, 256<<10)
	n := runtime.Stack(stack, true)
	t.Logf("named goroutines bytes=%d bounded=%t\n%s", n, n == len(stack), stack[:n])
}
