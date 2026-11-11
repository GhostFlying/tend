package store

import (
	"context"
	"database/sql"
	"errors"
	"runtime"
	"sync"
	"time"
)

// WriteSource identifies a write without retaining its payload or SQL.
type WriteSource struct {
	Operation           string `json:"operation"`
	RequestID           string `json:"request_id"`
	TraceID             string `json:"trace_id"`
	RunID               string `json:"run_id,omitempty"`
	EventSequence       int64  `json:"event_sequence,omitempty"`
	AttachmentRunID     string `json:"attachment_run_id,omitempty"`
	CoordinatorSequence int64  `json:"coordinator_sequence,omitempty"`
}

type writeSourceKey struct{}

// WithWriteSource attaches validated request metadata to a write.
func WithWriteSource(ctx context.Context, source WriteSource) context.Context {
	return context.WithValue(ctx, writeSourceKey{}, source)
}

type writeEventKey struct{}
type writeEvent struct {
	run         string
	sequence    int64
	attachment  string
	coordinator int64
}

// WithWriteEvent carries ledger identity through nested request metadata. It is
// diagnostic context only and never supplies authorization or an applied cursor.
func WithWriteEvent(ctx context.Context, run string, sequence int64) context.Context {
	event, _ := ctx.Value(writeEventKey{}).(writeEvent)
	event.run, event.sequence = run, sequence
	return context.WithValue(ctx, writeEventKey{}, event)
}

// WithWriteAttachment distinguishes a reader's synthetic event cursor from
// the retained source Run/sequence, which may be unknown on other transports.
func WithWriteAttachment(ctx context.Context, run string, sequence int64) context.Context {
	event, _ := ctx.Value(writeEventKey{}).(writeEvent)
	event.attachment, event.coordinator = run, sequence
	return context.WithValue(ctx, writeEventKey{}, event)
}

// WithWriteOperation names an internal boundary without replacing an enclosing
// request or trace. Callers supply a fixed operation name, never input content.
func WithWriteOperation(ctx context.Context, operation string) context.Context {
	source, _ := ctx.Value(writeSourceKey{}).(WriteSource)
	source.Operation = operation
	return WithWriteSource(ctx, source)
}

// WritePhase is a fixed diagnostic stage. Durations are inclusive: nested
// stages overlap their parents and must not be added together.
type WritePhase string

const (
	WriteReviewedContext      WritePhase = "reviewed_context"
	WriteApply                WritePhase = "apply"
	WritePersistOutcome       WritePhase = "persist_outcome"
	WriteAttention            WritePhase = "attention_reconcile"
	WriteFollowup             WritePhase = "followup"
	WriteManagerRefresh       WritePhase = "manager_refresh"
	WriteMaintenance          WritePhase = "maintenance_settlement"
	WriteClaimRefresh         WritePhase = "claim_manager_refresh"
	WriteClaimCandidates      WritePhase = "claim_candidates"
	WriteClaimSelection       WritePhase = "claim_selection"
	WriteClaimCandidateQuery  WritePhase = "claim_candidate_query"
	WriteClaimCandidateScan   WritePhase = "claim_candidate_scan"
	WriteClaimCandidateDecode WritePhase = "claim_candidate_decode"
	WriteClaimManagerQuery    WritePhase = "claim_manager_query"
	WriteClaimManagerScan     WritePhase = "claim_manager_scan"
	WriteClaimManagerBuild    WritePhase = "claim_manager_build"
)

// WritePhaseDuration records an inclusive duration, not exclusive CPU time.
type WritePhaseDuration struct {
	Phase    WritePhase    `json:"phase"`
	Duration time.Duration `json:"duration_ns"`
}

// WriteObservation is emitted only after the transaction and connection end.
// Outcome is a safe category; raw errors and business data are never included.
type WriteObservation struct {
	ID          uint64
	StartedAt   time.Time
	FinishedAt  time.Time
	DeadlineAt  time.Time
	Source      WriteSource
	Caller      string
	CallerStack []string
	Stage       string
	Outcome     string
	// ActiveWriter is sampled when connection acquisition fails. It does not
	// identify all writers that occupied the connection during this wait.
	ActiveWriter         *ActiveWriteSnapshot
	WaitFailureSampledAt time.Time
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
	PoolOpenConnections   int
	PoolInUse             int
	PoolIdle              int
	Phases                []WritePhaseDuration
}

// ActiveWriteSnapshot describes one observed owner of this Store's connection
// at SampledAt. HeldFor is elapsed connection ownership, not SQLite lock or CPU
// time, and not the duration for which it blocked the failed waiter.
type ActiveWriteSnapshot struct {
	ID          uint64        `json:"write_id"`
	Source      WriteSource   `json:"source"`
	CallerStack []string      `json:"caller_stack"`
	AcquiredAt  time.Time     `json:"acquired_at"`
	SampledAt   time.Time     `json:"sampled_at"`
	HeldFor     time.Duration `json:"held_ns"`
	Stage       string        `json:"stage"`
}

const writeCallerLimit = 8

// writeDiagnostics belongs to one Store. Its IDs and active snapshot have no
// business meaning. Never hold mu across database, callback, or logging calls.
type writeDiagnostics struct {
	mu     sync.Mutex
	next   uint64
	active activeWrite
}

type activeWrite struct {
	id       uint64
	source   WriteSource
	acquired time.Time
	stage    string
	pcs      [writeCallerLimit]uintptr
	pcCount  int
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
	diagnostics  *writeDiagnostics
	pcs          [writeCallerLimit]uintptr
	pcCount      int
}

func (s *Store) startWriteObservation(ctx context.Context) *writeCall {
	if s.observer == nil {
		return nil
	}
	started := s.observer.now()
	source, _ := ctx.Value(writeSourceKey{}).(WriteSource)
	if event, ok := ctx.Value(writeEventKey{}).(writeEvent); ok {
		source.RunID, source.EventSequence = event.run, event.sequence
		source.AttachmentRunID, source.CoordinatorSequence = event.attachment, event.coordinator
	}
	s.writeDiagnostics.mu.Lock()
	s.writeDiagnostics.next++
	id := s.writeDiagnostics.next
	s.writeDiagnostics.mu.Unlock()
	deadline, _ := ctx.Deadline()
	c := &writeCall{observer: s.observer, observation: WriteObservation{ID: id, StartedAt: started, DeadlineAt: deadline, Source: source, Stage: "connection_wait"}, started: started, stageStarted: started, diagnostics: &s.writeDiagnostics}
	c.pcCount = runtime.Callers(3, c.pcs[:])
	return c
}

func (s *Store) acquiredWrite(c *writeCall) {
	if c == nil {
		return
	}
	d := c.diagnostics
	d.mu.Lock()
	d.active = activeWrite{id: c.observation.ID, source: c.observation.Source, acquired: c.stageStarted, stage: c.observation.Stage, pcs: c.pcs, pcCount: c.pcCount}
	d.mu.Unlock()
}

func (s *Store) releasedWrite(c *writeCall) {
	if c == nil {
		return
	}
	d := c.diagnostics
	d.mu.Lock()
	// Another writer may already have acquired the returned connection.
	if d.active.id == c.observation.ID {
		d.active = activeWrite{}
	}
	d.mu.Unlock()
}

func (s *Store) failedWriteWait(c *writeCall) {
	if c == nil {
		return
	}
	sampled := c.observer.now()
	c.observation.WaitFailureSampledAt = sampled
	d := c.diagnostics
	d.mu.Lock()
	active := d.active
	d.mu.Unlock()
	if active.id != 0 && !active.acquired.After(sampled) {
		c.observation.ActiveWriter = &ActiveWriteSnapshot{ID: active.id, Source: active.source, CallerStack: writeCallers(active.pcs[:active.pcCount]), AcquiredAt: active.acquired, SampledAt: sampled, HeldFor: sampled.Sub(active.acquired), Stage: active.stage}
	}
}

func writeCallers(pcs []uintptr) []string {
	var names []string
	frames := runtime.CallersFrames(pcs)
	for {
		frame, more := frames.Next()
		if frame.Function != "" {
			names = append(names, frame.Function)
		}
		if !more || len(names) == writeCallerLimit {
			return names
		}
	}
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
	c.setLiveStage(c.observation.Stage)
}

// setLiveStage describes current connection cleanup without changing the
// terminal failure stage or its already measured callback/commit duration.
func (c *writeCall) setLiveStage(stage string) {
	if c == nil {
		return
	}
	c.diagnostics.mu.Lock()
	if c.diagnostics.active.id == c.observation.ID {
		c.diagnostics.active.stage = stage
	}
	c.diagnostics.mu.Unlock()
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
	o.FinishedAt = c.observer.now()
	o.Total = o.FinishedAt.Sub(c.started)
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
	o.CallerStack = writeCallers(c.pcs[:c.pcCount])
	if len(o.CallerStack) > 0 {
		o.Caller = o.CallerStack[0]
	}
	stats := s.write.Stats()
	o.PoolWaitCountTotal, o.PoolWaitDurationTotal = stats.WaitCount, stats.WaitDuration
	o.PoolOpenConnections, o.PoolInUse, o.PoolIdle = stats.OpenConnections, stats.InUse, stats.Idle
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
	case WriteReviewedContext, WriteApply, WritePersistOutcome, WriteAttention, WriteFollowup, WriteManagerRefresh, WriteMaintenance, WriteClaimRefresh, WriteClaimCandidates, WriteClaimSelection, WriteClaimCandidateQuery, WriteClaimCandidateScan, WriteClaimCandidateDecode, WriteClaimManagerQuery, WriteClaimManagerScan, WriteClaimManagerBuild:
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
