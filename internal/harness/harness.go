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

// MountBaseline separates this task's starting commit from resource history.
type MountBaseline struct {
	Path         string
	StartHEAD    string
	ResourceBase string
}

// WorkerSpec describes one run's work.
type WorkerSpec struct {
	Execution       *protocol.ExecutionProfile
	QuestionContext bool
	ReceiveMessages func(context.Context) (*protocol.RunMessage, error)
	AckMessage      func(context.Context, string) error
	AskOwner        func(context.Context, protocol.AskOwnerPayload) (protocol.WorkerQuestion, error)
	Questions       []protocol.WorkerQuestion
	Baseline        []MountBaseline
	RunID           string
	ProviderID      string
	StateDir        string
	UploadFile      func(context.Context, string, string) (protocol.ArtifactFile, error)
	DownloadFile    func(context.Context, string) (string, error)
	ReadObservation func(context.Context) (protocol.RunObservation, error)
	Role            protocol.RunRole
	// Dir is the workspace root; the worker starts there.
	Dir string
	// Guide describes the workspace when Dir is inside the only
	// repository, where tend writes no AGENTS.md (D65); empty otherwise.
	Guide string
	// ScratchDir is where the worker may keep temporary copies.
	ScratchDir string
	// Spec is the immutable task spec version the run works on.
	Spec json.RawMessage
	// Relations provide task provenance without inheriting another workspace.
	Relations []protocol.TaskRelationView
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
	// new thread with the full brief (D61). A harness may keep more than
	// an ID here, such as how to find an agent that outlives the worker.
	Thread string
	// OnThread, when set, is called with the worker's thread once it
	// exists and before the worker's first turn, so that the thread can be
	// recorded before any work happens in it, and again whenever the
	// harness changes it. An error stops the worker from starting.
	OnThread func(threadID string) error
	// OnStartupEvent persists startup facts before Start returns.
	OnStartupEvent func(context.Context, WorkerEvent) error
	// OnTakeover, when set, is called when the worker notices that the
	// owner works in its agent (D67). The worker then neither judges nor
	// touches the agent until it is stopped, normally with ErrHeld.
	OnTakeover func(reason string) error
	// HandBack says the owner took the run's agent over and has handed
	// it back: the worker tells the agent so before anything else.
	HandBack bool
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
	Result *protocol.ArtifactPayload

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

// ErrWorkerNotStarted marks a Start failure which proves no worker was
// created, resumed or adopted, and no earlier attempt can still own one.
// A runner may persist stopped proof before releasing that failed run.
// Ordinary Start errors provide no such process-lifetime guarantee.
var ErrWorkerNotStarted = errors.New("the worker was not started")

// Causes a runner stops a worker with, wrapped in the cause of the context
// passed to Start. Any other cause, such as the runner shutting down, only
// stops the worker's writes to the workspace.
var (
	// ErrPaused stops a worker whose run will continue once its task is
	// resumed.
	ErrPaused = errors.New("the run is paused")
	// ErrStopped stops a worker whose run tend no longer wants.
	ErrStopped = errors.New("tend no longer wants the run")
	// ErrHeld stops a worker whose run the owner has taken over (D67): an
	// agent that outlives the worker is left to the owner untouched.
	ErrHeld = errors.New("the owner has taken the run over")
)

// Releaser is implemented by Workers whose agents outlive the worker
// (D66). Before a runner gives up a run without a result, it calls Release
// with the run's thread, so that no agent keeps working on a run tend has
// let go, even when no worker attempt is running. Release may run more
// than once for a run.
type Releaser interface {
	Release(ctx context.Context, runID, thread string) error
}

// HistoricalReleaser refuses to stop a terminal that has passed to its owner.
type HistoricalReleaser interface {
	ReleaseHistorical(ctx context.Context, runID, thread string, held bool, completedAt int64) error
}

// FinalizingReleaser checks ownership before stopping a failed or legacy run.
// intentAt is the immutable time its final request was first journaled.
type FinalizingReleaser interface {
	ReleaseFinalizing(ctx context.Context, runID, thread string, intentAt int64, handBack bool, saveThread func(string) error) error
}

// CompletedReleaser verifies that a saved result still owns its external terminal.
type CompletedReleaser interface {
	ReleaseCompleted(ctx context.Context, runID, thread string, completedAt int64, handBack bool) error
}

// Workers starts workers. Cancelling the context passed to Start stops the
// worker and its writes to the workspace; Wait then returns the context's
// error.
type Workers interface {
	Start(ctx context.Context, spec WorkerSpec) (Worker, error)
}
