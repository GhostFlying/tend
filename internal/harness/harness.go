// Package harness abstracts the agent harnesses tend drives: the main
// agent's thread on the server and the workers runners start. The traex
// implementation lives in harness/traex; tests use the fakes in testkit.
package harness

import (
	"context"
	"encoding/json"

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

// Workers starts workers. Cancelling the context passed to Start stops the
// worker and its writes to the workspace; Wait then returns the context's
// error.
type Workers interface {
	Start(ctx context.Context, spec WorkerSpec) (Worker, error)
}
