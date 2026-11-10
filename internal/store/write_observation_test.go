package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func stepDiagnosticClock(hook func(int)) func() time.Time {
	var ticks int
	return func() time.Time {
		ticks++
		if hook != nil {
			hook(ticks)
		}
		return time.Unix(0, int64(ticks)*int64(time.Millisecond))
	}
}

func TestWriteObservationCancelledPoolWait(t *testing.T) {
	s, _ := openTest(t)
	conn, err := s.write.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var observations []WriteObservation
	s.observer = &writeObserver{now: stepDiagnosticClock(nil), slow: time.Hour, observe: func(o WriteObservation) {
		observations = append(observations, o)
	}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	before := s.write.Stats().WaitCount
	done := make(chan error, 1)
	go func() {
		done <- s.Write(ctx, func(*Tx) error { t.Error("cancelled waiter entered callback"); return nil })
	}()
	deadline := time.Now().Add(5 * time.Second)
	for s.write.Stats().WaitCount == before {
		if time.Now().After(deadline) {
			t.Fatal("write did not wait for connection")
		}
		runtime.Gosched()
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) || !strings.HasPrefix(err.Error(), "store: begin write: ") {
		t.Fatalf("Write error = %v", err)
	}
	if len(observations) != 1 {
		t.Fatalf("observations = %d, want 1", len(observations))
	}
	o := observations[0]
	if o.Stage != "connection_wait" || o.Outcome != "cancelled" || o.ConnectionWait != time.Millisecond || o.Begin != 0 || o.Callback != 0 {
		t.Fatalf("pool observation = %+v", o)
	}
	if o.Caller == "" {
		t.Fatal("direct Write caller was not identified")
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(t.Context(), func(*Tx) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestWriteObservationCancelledBeginAfterConnection(t *testing.T) {
	db := sql.OpenDB(cancelBeginConnector{})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	s := &Store{write: db}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var observations []WriteObservation
	s.observer = &writeObserver{now: stepDiagnosticClock(func(tick int) {
		// Cancel after acquiring the connection, before calling BeginTx.
		if tick == 2 {
			cancel()
		}
	}), slow: time.Hour, observe: func(o WriteObservation) {
		if s.write.Stats().InUse != 0 {
			t.Error("observer retained writer connection")
		}
		observations = append(observations, o)
	}}
	err := s.Write(ctx, func(*Tx) error { t.Error("cancelled BEGIN entered callback"); return nil })
	if !errors.Is(err, context.Canceled) || !strings.HasPrefix(err.Error(), "store: begin write: ") {
		t.Fatalf("Write error = %v", err)
	}
	if len(observations) != 1 {
		t.Fatalf("observations = %d, want 1", len(observations))
	}
	o := observations[0]
	if o.Stage != "begin_immediate" || o.Outcome != "cancelled" || o.ConnectionWait != time.Millisecond || o.Begin != time.Millisecond || o.Callback != 0 {
		t.Fatalf("BEGIN observation = %+v", o)
	}
}

// This driver isolates a cancellable BEGIN from the already acquired pool
// connection. SQLite may instead finish BEGIN and cancel at a later statement.
type cancelBeginConnector struct{}

func (cancelBeginConnector) Connect(context.Context) (driver.Conn, error) {
	return cancelBeginConn{}, nil
}
func (cancelBeginConnector) Driver() driver.Driver { return cancelBeginDriver{} }

type cancelBeginDriver struct{}

func (cancelBeginDriver) Open(string) (driver.Conn, error) { return cancelBeginConn{}, nil }

type cancelBeginConn struct{}

func (cancelBeginConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (cancelBeginConn) Close() error              { return nil }
func (cancelBeginConn) Begin() (driver.Tx, error) { return nil, errors.New("unexpected Begin") }
func (cancelBeginConn) BeginTx(ctx context.Context, _ driver.TxOptions) (driver.Tx, error) {
	return nil, ctx.Err()
}

func TestWriteObservationSQLiteBeginContention(t *testing.T) {
	s, path := openTest(t)
	// Keep the real SQLite failure bounded without changing production's timeout.
	if _, err := s.write.ExecContext(t.Context(), "PRAGMA busy_timeout=1"); err != nil {
		t.Fatal(err)
	}
	other, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	held, err := other.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Rollback()
	var observations []WriteObservation
	s.observer = &writeObserver{now: stepDiagnosticClock(nil), slow: time.Millisecond, observe: func(o WriteObservation) {
		if s.write.Stats().InUse != 0 {
			t.Error("BEGIN failure retained pool connection")
		}
		observations = append(observations, o)
	}}
	err = s.Write(t.Context(), func(*Tx) error { t.Error("blocked BEGIN entered callback"); return nil })
	if err == nil || !strings.HasPrefix(err.Error(), "store: begin write: ") {
		t.Fatalf("Write error = %v", err)
	}
	if len(observations) != 1 || observations[0].Stage != "begin_immediate" || observations[0].Begin != time.Millisecond || observations[0].Callback != 0 || observations[0].Outcome != "error" {
		t.Fatalf("SQLite BEGIN observation = %+v", observations)
	}
}

func TestWriteObservationPanicReleasesConnection(t *testing.T) {
	s, _ := openTest(t)
	var observation WriteObservation
	s.observer = &writeObserver{now: stepDiagnosticClock(nil), slow: time.Millisecond, observe: func(o WriteObservation) {
		if s.write.Stats().InUse != 0 {
			t.Error("panic retained pool connection")
		}
		observation = o
	}}
	func() {
		defer func() {
			if got := recover(); got != "test panic" {
				t.Errorf("panic = %v", got)
			}
		}()
		_ = s.Write(t.Context(), func(*Tx) error { panic("test panic") })
	}()
	if observation.Outcome != "aborted" || observation.Stage != "callback" {
		t.Fatalf("panic observation = %+v", observation)
	}
	if err := s.Write(t.Context(), func(*Tx) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestWriteObservationLifecycleAndInclusivePhases(t *testing.T) {
	for _, outcome := range []string{"success", "callback_error", "commit_error"} {
		t.Run(outcome, func(t *testing.T) {
			s, _ := openTest(t)
			var observations []WriteObservation
			s.observer = &writeObserver{now: stepDiagnosticClock(nil), slow: time.Millisecond, observe: func(o WriteObservation) {
				if s.write.Stats().InUse != 0 {
					t.Error("observer ran before connection release")
				}
				observations = append(observations, o)
			}}
			failure := errors.New("private callback error must not enter observation")
			source := WriteSource{Operation: "test.operation", RequestID: "test-request", TraceID: "test-trace"}
			err := s.Write(WithWriteSource(t.Context(), source), func(tx *Tx) error {
				end := tx.ObservePhase(WritePersistOutcome)
				defer end()
				child := tx.ObservePhase(WriteAttention)
				child()
				child()
				switch outcome {
				case "callback_error":
					return failure
				case "commit_error":
					return tx.tx.Rollback()
				}
				return nil
			})
			if outcome == "success" && err != nil || outcome == "callback_error" && !errors.Is(err, failure) || outcome == "commit_error" && !errors.Is(err, sql.ErrTxDone) {
				t.Fatalf("Write error = %v", err)
			}
			if len(observations) != 1 {
				t.Fatalf("observations = %d, want 1", len(observations))
			}
			o := observations[0]
			if o.Source != source || len(o.Phases) != 2 || o.Phases[0].Phase != WriteAttention || o.Phases[0].Duration != time.Millisecond || o.Phases[1].Phase != WritePersistOutcome || o.Phases[1].Duration != 3*time.Millisecond {
				t.Fatalf("inclusive phases/source = %+v", o)
			}
			if o.Callback == 0 || (outcome == "callback_error") != (o.Rollback > 0 && o.Commit == 0) {
				t.Fatalf("lifecycle = %+v", o)
			}
		})
	}
}

func TestWriteObservationFastSuccessIsQuiet(t *testing.T) {
	s, _ := openTest(t)
	s.observer = &writeObserver{now: stepDiagnosticClock(nil), slow: time.Hour, observe: func(WriteObservation) { t.Error("fast write emitted summary") }}
	if err := s.Write(t.Context(), func(*Tx) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestWriteObservationObserverCanWriteAfterRelease(t *testing.T) {
	s, _ := openTest(t)
	var entered bool
	s.observer = &writeObserver{now: stepDiagnosticClock(nil), slow: time.Millisecond, observe: func(WriteObservation) {
		if entered {
			return
		}
		entered = true
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		if err := s.Write(ctx, func(*Tx) error { return nil }); err != nil {
			t.Errorf("observer reentrant Write: %v", err)
		}
	}}
	if err := s.Write(t.Context(), func(*Tx) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if !entered {
		t.Fatal("observer did not run")
	}
}

func TestWriteObservationAutoRollbackCancellation(t *testing.T) {
	db, err := sql.Open("sqlite", dsn(filepath.Join(t.TempDir(), "cancel.db"), true))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	s := &Store{write: db}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var observations []WriteObservation
	s.observer = &writeObserver{now: stepDiagnosticClock(nil), slow: time.Hour, observe: func(o WriteObservation) {
		observations = append(observations, o)
	}}
	err = s.Write(ctx, func(tx *Tx) error {
		cancel()
		deadline := time.Now().Add(5 * time.Second)
		for {
			_, err := tx.Exec(context.Background(), "SELECT 1")
			if errors.Is(err, sql.ErrTxDone) {
				return nil
			}
			if time.Now().After(deadline) {
				t.Fatal("automatic rollback did not end transaction")
			}
			runtime.Gosched()
		}
	})
	if !errors.Is(err, sql.ErrTxDone) || errors.Is(err, context.Canceled) {
		t.Fatalf("original Commit error chain changed: %v", err)
	}
	if len(observations) != 1 {
		t.Fatalf("fast auto-rollback cancellation observations = %d, want 1", len(observations))
	}
	if o := observations[0]; o.Stage != "commit" || o.Outcome != "error" || o.ContextState != "cancelled" {
		t.Fatalf("auto-rollback context state = %+v", o)
	}
}

func TestWriteObservationSuccessfulCommitThenCancel(t *testing.T) {
	s, _ := openTest(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var observation WriteObservation
	s.observer = &writeObserver{now: stepDiagnosticClock(func(tick int) {
		// With no nested phases the sixth tick is after commit and release.
		if tick == 6 {
			cancel()
		}
	}), slow: time.Millisecond, observe: func(o WriteObservation) { observation = o }}
	if err := s.Write(ctx, func(*Tx) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() == nil || observation.Outcome != "ok" || observation.ContextState != "" {
		t.Fatalf("post-commit cancel = %+v, ctx=%v", observation, ctx.Err())
	}
}

//go:noinline
func diagnosticWriteWrapper(s *Store, ctx context.Context, fn func(*Tx) error) error {
	return s.Write(ctx, fn)
}

func TestWriteObservationTimeoutSnapshotsCurrentWriter(t *testing.T) {
	s, _ := openTest(t)
	observations := make(chan WriteObservation, 16)
	s.observer = &writeObserver{now: time.Now, slow: 0, observe: func(o WriteObservation) { observations <- o }}
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	done := make(chan error, 1)
	holderJoined := false
	defer func() {
		unblock()
		if !holderJoined {
			<-done
		}
	}()
	source := WriteSource{Operation: "test.held", RequestID: "held-request", TraceID: "held-trace"}
	heldContext := WithWriteEvent(WithWriteSource(t.Context(), source), "held-run", 7)
	go func() {
		done <- diagnosticWriteWrapper(s, heldContext, func(*Tx) error { close(entered); <-release; return nil })
	}()
	<-entered
	const waiters = 4
	var waits sync.WaitGroup
	for range waiters {
		waits.Go(func() {
			ctx, cancel := context.WithTimeout(WithWriteEvent(WithWriteSource(t.Context(), WriteSource{Operation: "test.wait", RequestID: "wait-request", TraceID: "wait-trace"}), "wait-run", 14), 50*time.Millisecond)
			defer cancel()
			if err := diagnosticWriteWrapper(s, ctx, func(*Tx) error { t.Error("timed out waiter entered callback"); return nil }); !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("wait error = %v", err)
			}
		})
	}
	waits.Wait()
	ids := map[uint64]bool{}
	for range waiters {
		o := <-observations
		if o.Stage != "connection_wait" || o.Outcome != "deadline_exceeded" || o.Begin != 0 || o.Callback != 0 || o.Commit != 0 || o.Rollback != 0 {
			t.Fatalf("timeout stages = %+v", o)
		}
		if o.Source.RunID != "wait-run" || o.Source.EventSequence != 14 || o.Source.RequestID != "wait-request" || o.Source.TraceID != "wait-trace" || o.DeadlineAt.IsZero() || o.StartedAt.IsZero() || o.FinishedAt.Before(o.DeadlineAt) {
			t.Fatalf("wait identity/deadline = %+v", o)
		}
		a := o.ActiveWriter
		if a == nil || a.ID != 1 || a.Source.Operation != source.Operation || a.Source.RunID != "held-run" || a.Source.EventSequence != 7 || a.Source.RequestID != "held-request" || a.Stage != "callback" || a.HeldFor <= 0 || !a.SampledAt.Equal(o.WaitFailureSampledAt) || a.AcquiredAt.After(a.SampledAt) {
			t.Fatalf("active snapshot = %+v; waiter = %+v", a, o)
		}
		if o.ID == a.ID || ids[o.ID] || o.PoolInUse != 1 || o.PoolOpenConnections != 1 || len(o.CallerStack) > writeCallerLimit || len(a.CallerStack) > writeCallerLimit {
			t.Fatalf("IDs/pool/bounds = %+v", o)
		}
		ids[o.ID] = true
		for _, stack := range [][]string{o.CallerStack, a.CallerStack} {
			if !strings.Contains(strings.Join(stack, "\n"), "diagnosticWriteWrapper") || !strings.Contains(strings.Join(stack, "\n"), "TestWriteObservationTimeoutSnapshotsCurrentWriter") {
				t.Fatalf("actual caller missing from stack: %v", stack)
			}
		}
	}
	unblock()
	if err := <-done; err != nil {
		holderJoined = true
		t.Fatal(err)
	}
	holderJoined = true
	holder := <-observations
	if holder.ID != 1 || holder.ActiveWriter != nil || holder.PoolInUse != 0 {
		t.Fatalf("holder summary = %+v", holder)
	}
	// A connection held outside Store.Write has no observed owner. The previous
	// writer must not be reported as the owner of this later wait.
	conn, err := s.write.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.Write(ctx, func(*Tx) error { t.Error("cancelled write ran"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	after := <-observations
	if after.ActiveWriter != nil {
		t.Fatalf("released writer leaked into later snapshot: %+v", after)
	}
}

func TestWriteObservationStoreIDsAndPayloadPrivacy(t *testing.T) {
	for range 2 {
		s, _ := openTest(t)
		var o WriteObservation
		s.observer = &writeObserver{now: time.Now, slow: 0, observe: func(v WriteObservation) { o = v }}
		secret := errors.New("private SQL and credential must not appear")
		if err := s.Write(t.Context(), func(*Tx) error { return secret }); !errors.Is(err, secret) {
			t.Fatal(err)
		}
		if o.ID != 1 {
			t.Fatalf("ID shared with another Store: %+v", o)
		}
		raw, err := json.Marshal(o)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), secret.Error()) || strings.Contains(string(raw), "write_observation_test.go") {
			t.Fatalf("private payload/path in summary: %s", raw)
		}
	}
}

type diagnosticRollbackGate struct{ entered, release chan struct{} }
type diagnosticRollbackConnector struct{ gate *diagnosticRollbackGate }

func (c diagnosticRollbackConnector) Connect(context.Context) (driver.Conn, error) {
	return diagnosticRollbackConn{gate: c.gate}, nil
}
func (diagnosticRollbackConnector) Driver() driver.Driver { return cancelBeginDriver{} }

type diagnosticRollbackConn struct {
	cancelBeginConn
	gate *diagnosticRollbackGate
}

func (c diagnosticRollbackConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return diagnosticRollbackTx{gate: c.gate}, nil
}

type diagnosticRollbackTx struct{ gate *diagnosticRollbackGate }

func (diagnosticRollbackTx) Commit() error     { return nil }
func (x diagnosticRollbackTx) Rollback() error { close(x.gate.entered); <-x.gate.release; return nil }

func TestWriteObservationWaiterSeesRollbackCleanup(t *testing.T) {
	for _, ending := range []string{"callback_error", "panic"} {
		t.Run(ending, func(t *testing.T) {
			gate := &diagnosticRollbackGate{entered: make(chan struct{}), release: make(chan struct{})}
			db := sql.OpenDB(diagnosticRollbackConnector{gate: gate})
			db.SetMaxOpenConns(1)
			defer db.Close()
			observed := make(chan WriteObservation, 4)
			s := &Store{write: db, observer: &writeObserver{now: time.Now, slow: 0, observe: func(o WriteObservation) { observed <- o }}}
			original := errors.New("original callback failure")
			type result struct {
				err      error
				panicked any
			}
			done := make(chan result, 1)
			go func() {
				r := result{}
				defer func() { r.panicked = recover(); done <- r }()
				r.err = s.Write(t.Context(), func(*Tx) error {
					if ending == "panic" {
						panic(original)
					}
					return original
				})
			}()
			joined := false
			var once sync.Once
			release := func() { once.Do(func() { close(gate.release) }) }
			defer func() {
				release()
				if !joined {
					<-done
				}
			}()
			<-gate.entered
			ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
			defer cancel()
			if err := s.Write(ctx, func(*Tx) error { t.Error("waiter entered callback during rollback"); return nil }); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
			waiter := <-observed
			if waiter.ActiveWriter == nil || waiter.ActiveWriter.Stage != "rollback_cleanup" || waiter.Stage != "connection_wait" {
				t.Fatalf("live rollback misattributed: %+v", waiter)
			}
			release()
			r := <-done
			joined = true
			owner := <-observed
			if owner.Stage != "callback" {
				t.Fatalf("live stage replaced terminal failure stage: %+v", owner)
			}
			if ending == "callback_error" {
				if !errors.Is(r.err, original) || r.panicked != nil || owner.Outcome != "error" || owner.Rollback <= 0 {
					t.Fatalf("callback error/timing changed: result=%+v observation=%+v", r, owner)
				}
			} else if r.panicked != original || r.err != nil || owner.Outcome != "aborted" {
				t.Fatalf("panic identity/observation changed: result=%+v observation=%+v", r, owner)
			}
			if owner.PoolInUse != 0 {
				t.Fatalf("observer ran before connection release: %+v", owner)
			}
		})
	}
}
