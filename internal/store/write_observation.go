package store

import (
	"context"
	"database/sql"
	"errors"
	"runtime"
	"time"
)

// WriteSource identifies a write without retaining its payload or SQL.
type WriteSource struct {
	Operation string
	RequestID string
	TraceID   string
}

type writeSourceKey struct{}

// WithWriteSource attaches validated request metadata to a write.
func WithWriteSource(ctx context.Context, source WriteSource) context.Context {
	return context.WithValue(ctx, writeSourceKey{}, source)
}

// WritePhase is a fixed diagnostic stage. Durations are inclusive: nested
// stages overlap their parents and must not be added together.
type WritePhase string

const (
	WriteReviewedContext WritePhase = "reviewed_context"
	WriteApply           WritePhase = "apply"
	WritePersistOutcome  WritePhase = "persist_outcome"
	WriteAttention       WritePhase = "attention_reconcile"
	WriteFollowup        WritePhase = "followup"
	WriteManagerRefresh  WritePhase = "manager_refresh"
	WriteMaintenance     WritePhase = "maintenance_settlement"
	WriteRequestInsert   WritePhase = "request_insert"
	WriteManagerTasks    WritePhase = "manager_tasks"
	WriteClaimCandidates WritePhase = "claim_candidates"
	WriteClaimExecutors  WritePhase = "claim_executors"
	WriteConcurrencySync WritePhase = "concurrency_sync"
	WriteConcurrencyWait WritePhase = "concurrency_waiting"
	WriteConcurrencyLoad WritePhase = "concurrency_load"
	WriteConcurrencyGC   WritePhase = "concurrency_history"
)

// WritePhaseDuration records an inclusive duration, not exclusive CPU time.
type WritePhaseDuration struct {
	Phase    WritePhase    `json:"phase"`
	Duration time.Duration `json:"duration_ns"`
}

// WriteObservation is emitted only after the transaction and connection end.
// Outcome is a safe category; raw errors and business data are never included.
type WriteObservation struct {
	Source  WriteSource
	Caller  string
	Stage   string
	Outcome string
	// ContextState is captured at an ErrTxDone failure boundary. It describes
	// cancellation state without replacing the returned error or claiming cause.
	ContextState          string
	Total                 time.Duration
	ConnectionWait        time.Duration
	Begin                 time.Duration
	Callback              time.Duration
	Commit                time.Duration
	Rollback              time.Duration
	PoolWaitCountTotal    int64
	PoolWaitDurationTotal time.Duration
	Phases                []WritePhaseDuration
}

type writeObserver struct {
	now     func() time.Time
	slow    time.Duration
	observe func(WriteObservation)
}

type writeCall struct {
	observer     *writeObserver
	observation  WriteObservation
	started      time.Time
	stageStarted time.Time
	callerPC     uintptr
}

func (s *Store) startWriteObservation(ctx context.Context) *writeCall {
	if s.observer == nil {
		return nil
	}
	started := s.observer.now()
	source, _ := ctx.Value(writeSourceKey{}).(WriteSource)
	c := &writeCall{observer: s.observer, observation: WriteObservation{Source: source, Stage: "connection_wait"}, started: started, stageStarted: started}
	if source.Operation == "" {
		var pcs [1]uintptr
		if runtime.Callers(3, pcs[:]) != 0 {
			c.callerPC = pcs[0]
		}
	}
	return c
}

func (c *writeCall) endStage(next string) {
	if c == nil {
		return
	}
	now := c.observer.now()
	d := now.Sub(c.stageStarted)
	switch c.observation.Stage {
	case "connection_wait":
		c.observation.ConnectionWait = d
	case "begin_immediate":
		c.observation.Begin = d
	case "callback":
		c.observation.Callback = d
	case "commit":
		c.observation.Commit = d
	}
	c.stageStarted = now
	if next != "" {
		c.observation.Stage = next
	}
}

func (c *writeCall) failedContext(ctx context.Context, err error) {
	if c == nil || !errors.Is(err, sql.ErrTxDone) {
		return
	}
	switch ctx.Err() {
	case context.Canceled:
		c.observation.ContextState = "cancelled"
	case context.DeadlineExceeded:
		c.observation.ContextState = "deadline_exceeded"
	}
}

// finish is deferred before connection/transaction cleanup, so it runs last.
func (s *Store) finishWriteObservation(c *writeCall, err error) {
	if c == nil {
		return
	}
	o := c.observation
	o.Total = c.observer.now().Sub(c.started)
	switch {
	case errors.Is(err, context.Canceled):
		o.Outcome = "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		o.Outcome = "deadline_exceeded"
	case err != nil:
		o.Outcome = "error"
	case o.Stage != "commit":
		o.Outcome = "aborted"
	default:
		o.Outcome = "ok"
	}
	if o.Total < c.observer.slow && o.Outcome != "cancelled" && o.Outcome != "deadline_exceeded" && o.ContextState == "" {
		return
	}
	if c.callerPC != 0 {
		frames := runtime.CallersFrames([]uintptr{c.callerPC})
		frame, _ := frames.Next()
		o.Caller = frame.Function
	}
	stats := s.write.Stats()
	o.PoolWaitCountTotal, o.PoolWaitDurationTotal = stats.WaitCount, stats.WaitDuration
	c.observer.observe(o)
}

// OpenOption configures an internal store boundary without changing ledger data.
type OpenOption func(*Store)

// WithWriteObserver enables slow/cancelled write summaries. now is a diagnostic
// clock, separate from the business clock. observe runs after releasing the
// connection and must return promptly. It cannot change transaction results.
func WithWriteObserver(now func() time.Time, slow time.Duration, observe func(WriteObservation)) OpenOption {
	return func(s *Store) {
		if now != nil && observe != nil {
			s.observer = &writeObserver{now: now, slow: slow, observe: observe}
		}
	}
}

// ObservePhase ends an inclusive fixed stage. Calling the returned function
// more than once has no effect. A Tx is used by its callback sequentially.
func (t *Tx) ObservePhase(phase WritePhase) func() {
	c := t.observation
	if c == nil {
		return func() {}
	}
	switch phase {
	case WriteReviewedContext, WriteApply, WritePersistOutcome, WriteAttention, WriteFollowup, WriteManagerRefresh, WriteMaintenance, WriteRequestInsert,
		WriteManagerTasks, WriteClaimCandidates, WriteClaimExecutors, WriteConcurrencySync, WriteConcurrencyWait, WriteConcurrencyLoad, WriteConcurrencyGC:
	default:
		return func() {}
	}
	started := c.observer.now()
	ended := false
	return func() {
		if ended {
			return
		}
		ended = true
		d := c.observer.now().Sub(started)
		for i := range c.observation.Phases {
			if c.observation.Phases[i].Phase == phase {
				c.observation.Phases[i].Duration += d
				return
			}
		}
		c.observation.Phases = append(c.observation.Phases, WritePhaseDuration{Phase: phase, Duration: d})
	}
}
