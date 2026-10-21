// Package harness abstracts the agent harnesses tend drives: the main
// agent's thread on the server and the workers runners start. The traex
// implementation lives in harness/traex; tests use the fakes in testkit.
package harness

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
)

// WorkerSpec describes one run's work.
type WorkerSpec struct {
	RunID string
	Role  protocol.RunRole
	// Dir is the workspace root; the worker starts there.
	Dir string
	// ScratchDir is where the worker may keep temporary copies.
	ScratchDir string
	// Spec is the immutable task spec version the run works on.
	Spec json.RawMessage
	// Artifact is what an acceptance run judges.
	Artifact json.RawMessage
	// Feedback is what the last acceptance asked to change, for an
	// implementation after a changes-required verdict.
	Feedback *protocol.Feedback
	// Attempt counts how often the worker was started for this run; it is
	// above 1 when a run resumes after fencing or a restart.
	Attempt int
	// Thread is the thread an earlier attempt of the run worked in. The
	// worker continues it when the harness can, and otherwise starts a
	// new thread with the full brief (D61).
	Thread string
	// OnThread, when set, is called with the worker's thread once it
	// exists and before the worker's first turn, so that the thread can be
	// recorded before any work happens in it. An error stops the worker
	// from starting.
	OnThread func(threadID string) error
}

// WorkerEvent is a fact the worker produced, in the harness's own terms.
type WorkerEvent struct {
	// NativeID is the harness's identity of the fact, such as an item ID.
	NativeID string
	Kind     string
	Body     json.RawMessage
}

// WorkerResult is how a worker ended.
type WorkerResult struct {
	// Verdict is an acceptance run's conclusion; nil when the worker gave
	// none, which counts as inconclusive.
	Verdict *protocol.VerdictPayload
	// Summary is the worker's own account of what it did. It is evidence,
	// never a verdict.
	Summary string
}

// Worker is a running worker.
type Worker interface {
	// Events delivers the worker's facts. It is closed when the worker
	// ends.
	Events() <-chan WorkerEvent
	// Wait blocks until the worker ends. An error means the worker failed
	// without a result.
	Wait() (WorkerResult, error)
}

// Causes a runner stops a worker with, wrapped in the cause of the context
// passed to Start. Any other cause, such as the runner shutting down, only
// stops the worker's writes to the workspace.
var (
	// ErrPaused stops a worker whose run will continue once its task is
	// resumed.
	ErrPaused = errors.New("the run is paused")
	// ErrStopped stops a worker whose run tend no longer wants.
	ErrStopped = errors.New("tend no longer wants the run")
)

// Workers starts workers. Cancelling the context passed to Start stops the
// worker and its writes to the workspace; Wait then returns the context's
// error.
type Workers interface {
	Start(ctx context.Context, spec WorkerSpec) (Worker, error)
}
