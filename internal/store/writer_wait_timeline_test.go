package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"
)

func TestWriterWaitTimelineSQLite(t *testing.T) {
	for _, tc := range []struct {
		name     string
		success  bool
		overlaps []time.Duration
		gaps     time.Duration
	}{
		{name: "long", overlaps: []time.Duration{200 * time.Millisecond}},
		{name: "short_series", overlaps: []time.Duration{20 * time.Millisecond, 50 * time.Millisecond, 50 * time.Millisecond}, gaps: 80 * time.Millisecond},
		{name: "slow_success", success: true, overlaps: []time.Duration{200 * time.Millisecond}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			guard, stop := context.WithTimeout(t.Context(), 10*time.Second)
			var writes sync.WaitGroup
			defer func() { stop(); writes.Wait() }()
			base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
			var elapsed atomic.Int64
			advance := func(ms int64) { elapsed.Store(ms * int64(time.Millisecond)) }
			now := func() time.Time { return base.Add(time.Duration(elapsed.Load())) }
			observed := make(chan WriteObservation, 8)
			reset := make(chan chan struct{})
			dsn := dsn(filepath.Join(t.TempDir(), "writers.db"), true)
			db, err := sql.Open("sqlite", dsn)
			if err != nil {
				t.Fatal(err)
			}
			if len(tc.overlaps) > 1 {
				wrapper := timelineResetConnector{dsn: dsn, driver: db.Driver(), selected: reset}
				db.Close()
				db = sql.OpenDB(wrapper)
			}
			db.SetMaxOpenConns(1)
			t.Cleanup(func() { db.Close() })
			s := &Store{write: db}
			if _, err := db.ExecContext(guard, "CREATE TABLE writes (value TEXT)"); err != nil {
				t.Fatal(err)
			}
			WithWriteObserver(now, time.Second, func(o WriteObservation) { observed <- o })(s)
			startHolder := func(operation string, seq int64) (entered <-chan error, release chan<- struct{}, done <-chan error) {
				started, released, finished := make(chan error, 1), make(chan struct{}), make(chan error, 1)
				writes.Go(func() {
					ctx := WithWriteSource(guard, WriteSource{Operation: operation, RequestID: fmt.Sprintf("request-%d", seq), TraceID: "short-trace"})
					ctx = WithWriteEvent(ctx, "holder-run", seq)
					finished <- s.Write(ctx, func(tx *Tx) error {
						if seq == 2 {
							tx.ObserveCloseRequest("native-close-id")
						}
						_, err := tx.Exec(ctx, "INSERT INTO writes VALUES (?)", "private-sql-parameter")
						started <- err
						select {
						case <-released:
						case <-guard.Done():
						}
						return err
					})
				})
				return started, released, finished
			}
			entered, release, done := startHolder("runner.usage", 1)
			if err := timelineReceive(t, guard, entered); err != nil {
				t.Fatal(err)
			}

			advance(100)
			waiter := WithWriteEvent(WithWriteSource(guard, WriteSource{Operation: "manager.event.ack", RequestID: "ack-request", TraceID: "ack-trace"}), "ack-run", 268)
			waiter, expire := context.WithCancel(waiter)
			defer expire()
			waiterDone := make(chan error, 1)
			waitCount := db.Stats().WaitCount
			writes.Go(func() {
				ctx := context.Context(waiter)
				if !tc.success {
					ctx = timelineDeadlineContext{Context: waiter, deadline: base.Add(300 * time.Millisecond)}
				}
				waiterDone <- s.Write(ctx, func(tx *Tx) error {
					if !tc.success {
						t.Error("expired waiter entered callback")
					}
					advance(1500)
					_, err := tx.Exec(ctx, "INSERT INTO writes VALUES (?)", "private-message-body")
					return err
				})
			})
			waitCount++
			timelineWaitForPool(t, guard, db, waitCount)

			// Make the named waiter the sole candidate for each handoff. At
			// the first two handoffs it encounters ErrBadConn during reset,
			// before sql.DB.Conn returns. Queue the next real SQLite writer
			// through a temporary extra pool slot while the discarded
			// connection is still in driver reset. Restore the one-slot
			// limit before the retry, while the next writer holds it.
			for i, operation := range []string{"manager.native_reply", "runner.hello"}[:len(tc.overlaps)-1] {
				advance(int64(120 + i*90))
				close(release)
				if err := timelineReceive(t, guard, done); err != nil {
					t.Fatal(err)
				}
				resetRelease := timelineReceive(t, guard, reset)
				advance(int64(160 + i*90))
				db.SetMaxOpenConns(2)
				entered, release, done = startHolder(operation, int64(i+2))
				if err := timelineReceive(t, guard, entered); err != nil {
					t.Fatal(err)
				}
				db.SetMaxOpenConns(1)
				close(resetRelease)
				waitCount++
				timelineWaitForPool(t, guard, db, waitCount)
			}

			advance(300)
			wantErr := error(context.DeadlineExceeded)
			if tc.success {
				close(release)
				wantErr = nil
			} else {
				expire()
			}
			if err := timelineReceive(t, guard, waiterDone); !errors.Is(err, wantErr) {
				t.Fatalf("waiter error = %v, want %v", err, wantErr)
			}
			o := timelineReceive(t, guard, observed)
			w := o.WaitTimeline
			if w == nil || o.ConnectionWait != 200*time.Millisecond || w.EndedAt.Sub(w.StartedAt) != o.ConnectionWait ||
				o.Source.RunID != "ack-run" || o.Source.EventSequence != 268 || w.HistoryTruncated || !w.HistoryFrom.Equal(w.StartedAt) ||
				w.MatchedHolders != len(tc.overlaps) || len(w.Holders) != len(tc.overlaps) || w.OmittedHolders != 0 ||
				w.ObservedHeld != o.ConnectionWait-tc.gaps || w.Unattributed != tc.gaps {
				t.Fatalf("wait summary = %+v, timeline = %+v", o, w)
			}
			for i, h := range w.Holders {
				if h.WaitHeld != tc.overlaps[i] || h.Source.RunID != "holder-run" || h.Source.EventSequence != int64(i+1) || h.ID == o.ID {
					t.Fatalf("holder %d = %+v", i, h)
				}
				if i < len(w.Holders)-1 || tc.success {
					if h.ReleasedBy.IsZero() || h.ReleaseStartedAt.Before(h.AcquiredAt) || h.ReleasedBy.Before(h.ReleaseStartedAt) {
						t.Fatalf("released holder bounds = %+v", h)
					}
				}
			}
			raw, err := json.Marshal(o)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "private-") || (len(tc.overlaps) > 1 && w.Holders[1].Source.CloseRequestID != "native-close-id") {
				t.Fatalf("redaction or close correlation failed: %s", raw)
			}
			if tc.success {
				if o.ActiveWriter != nil || o.ConnectionHeld < time.Second || o.Stage != "commit" || o.Outcome != "ok" {
					t.Fatalf("successful slow write = %+v", o)
				}
			} else {
				last := w.Holders[len(w.Holders)-1]
				if o.ActiveWriter == nil || o.ActiveWriter.ID != last.ID || !last.ReleasedBy.IsZero() || last.LastObservedStage != "callback" ||
					o.Stage != "connection_wait" || o.Outcome != "deadline_exceeded" || o.Begin != 0 || o.Callback != 0 || o.Commit != 0 || o.ConnectionHeld != 0 {
					t.Fatalf("timeout summary = %+v, last holder = %+v", o, last)
				}
				close(release)
			}
			if err := timelineReceive(t, guard, done); err != nil {
				t.Fatal(err)
			}
			t.Logf("wait_ns=%d own_hold_ns=%d longest_overlap_ns=%d timeline=%s", o.ConnectionWait, o.ConnectionHeld, w.LongestOverlap, raw)
		})
	}
}

type timelineDeadlineContext struct {
	context.Context
	deadline time.Time
}

func (c timelineDeadlineContext) Deadline() (time.Time, bool) { return c.deadline, true }
func (c timelineDeadlineContext) Err() error {
	if c.Context.Err() != nil {
		return context.DeadlineExceeded
	}
	return nil
}

type timelineResetConnector struct {
	dsn      string
	driver   driver.Driver
	selected chan chan struct{}
}

func (c timelineResetConnector) Driver() driver.Driver { return c.driver }

func (c timelineResetConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.driver.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return timelineResetConn{Conn: conn, selected: c.selected}, nil
}

type timelineResetConn struct {
	driver.Conn
	selected chan chan struct{}
}

func (c timelineResetConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}

func (c timelineResetConn) ResetSession(ctx context.Context) error {
	if _, waiting := ctx.(timelineDeadlineContext); !waiting {
		return nil
	}
	released := make(chan struct{})
	select {
	case c.selected <- released:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-released:
		return driver.ErrBadConn
	case <-ctx.Done():
		return ctx.Err()
	}
}

func timelineReceive[T any](t *testing.T, ctx context.Context, value <-chan T) T {
	t.Helper()
	select {
	case v := <-value:
		return v
	case <-ctx.Done():
		t.Fatal("test did not reach the expected write boundary")
		var zero T
		return zero
	}
}

func timelineWaitForPool(t *testing.T, ctx context.Context, db *sql.DB, count int64) {
	t.Helper()
	for db.Stats().WaitCount < count {
		if ctx.Err() != nil {
			t.Fatalf("pool wait count = %d, want %d", db.Stats().WaitCount, count)
		}
		runtime.Gosched()
	}
}

func TestWriterWaitTimelineBounds(t *testing.T) {
	start := time.Unix(0, 0)
	stamp := func(ms int) time.Time { return start.Add(time.Duration(ms) * time.Millisecond) }
	d := writeDiagnostics{history: new([writerHistoryCapacity]WriterWaitHolder)}
	for i := 1; i <= writerHistoryCapacity+6; i++ {
		d.historyAt++
		held := 1
		if i == 16 {
			held = 3
		}
		*d.historyAtSlot(d.historyAt) = WriterWaitHolder{
			ID: uint64(i), AcquiredAt: stamp(i * 4), ReleaseStartedAt: stamp(i*4 + held), ReleasedBy: stamp(i*4 + held + 1),
		}
	}
	end := stamp(int(d.historyAt)*4 + 5)
	d.active = activeWrite{id: d.historyAt + 1, acquired: end.Add(time.Millisecond)}
	w := d.waitTimeline(start, end)
	if !w.HistoryTruncated || !w.HistoryFrom.Equal(stamp(7*4)) || w.MatchedHolders != writerHistoryCapacity ||
		w.OmittedHolders != writerHistoryCapacity-writerWaitHolderLimit || len(w.Holders) != writerWaitHolderLimit ||
		w.ObservedHeld != (writerHistoryCapacity+2)*time.Millisecond || w.LongestOverlap != 3*time.Millisecond ||
		w.Holders[0].ID != 16 || w.Holders[len(w.Holders)-1].ID != d.historyAt || w.ObservedHeld+w.Unattributed != end.Sub(start) {
		t.Fatalf("bounded wait = %+v", w)
	}
	// A stalled release receipt can bound when handoff occurred, but it
	// never extends that writer's confirmed occupancy into its successor.
	last := d.historyAtSlot(d.historyAt)
	last.ReleasedBy = time.Time{}
	w = d.waitTimeline(last.ReleaseStartedAt, end.Add(3*time.Millisecond))
	if w.HistoryTruncated || w.MatchedHolders != 2 || w.Holders[0].WaitHeld != 0 ||
		!w.Holders[0].ReleasedBy.IsZero() || w.ObservedHeld != 2*time.Millisecond {
		t.Fatalf("unconfirmed handoff = %+v", w)
	}
	t.Logf("ring records=%d slots_bytes=%d log entries=%d", writerHistoryCapacity, unsafe.Sizeof(*d.history), writerWaitHolderLimit)
}

func BenchmarkWriteObservationFastPath(b *testing.B) {
	db, err := sql.Open("sqlite", dsn(filepath.Join(b.TempDir(), "bench.db"), true))
	if err != nil {
		b.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	b.Cleanup(func() { db.Close() })
	s := &Store{write: db}
	ctx := WithWriteSource(b.Context(), WriteSource{Operation: "manager.event.ack", RunID: "bench-run", EventSequence: 268})
	WithWriteObserver(time.Now, time.Hour, func(WriteObservation) { b.Error("unexpected slow write") })(s)
	write := func(*Tx) error { return nil }
	if err := s.Write(ctx, write); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if err := s.Write(ctx, write); err != nil {
			b.Fatal(err)
		}
	}
}
