package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
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
