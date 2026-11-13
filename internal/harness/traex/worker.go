package traex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/GhostFlying/tend/internal/codexenv"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

// WorkerOptions configure the workers a runner starts.
type WorkerOptions struct {
	// EnvironmentHomes is the runner-owned registry of historical rollout roots.
	EnvironmentHomes string

	BeforeStart func(context.Context, harness.WorkerSpec, harness.Thread, string) (string, error)

	// Acquire retains a run session independently of a worker attempt. The release
	// callback must prove execution stopped unless the attempt is held or paused.
	Acquire        func(context.Context, harness.WorkerSpec) (*Session, func(error), error)
	EnvironmentID  string
	StrictResume   bool
	ReuseUnstarted bool
	// StartupTimeout bounds the handshake and first turn request, not execution.
	// Zero uses one minute. Caller cancellation may impose a shorter deadline.
	StartupTimeout time.Duration

	// Session starts the app-server. Workers use the machine's own traex
	// installation and configuration (D38); leave Env without TRAE_HOME.
	Session Options
	// Model overrides the configured default model when set.
	Model      string
	Effort     string
	Acceptance *protocol.ExecutionProfile
	// Sandbox defaults to danger-full-access and ApprovalPolicy to never
	// (D39): workers run unattended.
	Sandbox        string
	ApprovalPolicy string
}

// ErrInterruptUnconfirmed marks a retained session whose stop request failed.
var ErrInterruptUnconfirmed = errors.New("traex: native interrupt was not confirmed")

// Workers implements harness.Workers with one app-server per run.
type Workers struct {
	opts WorkerOptions
	// start launches a session; tests replace it.
	start func(ctx context.Context, opts Options) (*Session, error)
}

var _ harness.Workers = (*Workers)(nil)

// NewWorkers returns Workers.
func NewWorkers(opts WorkerOptions) *Workers {
	if opts.StartupTimeout <= 0 {
		opts.StartupTimeout = time.Minute
	}
	if opts.Sandbox == "" {
		opts.Sandbox = "danger-full-access"
	}
	if opts.ApprovalPolicy == "" {
		opts.ApprovalPolicy = "never"
	}
	return &Workers{opts: opts, start: Start}
}

// worker is a running traex worker.
type worker struct {
	thread *thread
	turnID string
	events chan harness.WorkerEvent
	done   chan struct{}
	res    harness.WorkerResult
	err    error
}

type restoredVerdictWorker struct {
	events chan harness.WorkerEvent
	result harness.WorkerResult
}

func (w *restoredVerdictWorker) Events() <-chan harness.WorkerEvent  { return w.events }
func (w *restoredVerdictWorker) Wait() (harness.WorkerResult, error) { return w.result, nil }

var _ harness.LiveMessenger = (*worker)(nil)

func (w *worker) MessageTarget() (string, string) { return w.thread.ID(), w.turnID }

func (w *worker) SendMessage(ctx context.Context, text string) (protocol.RunMessageStatus, string) {
	select {
	case <-w.done:
		return protocol.RunMessageFailed, "worker_ended"
	default:
	}
	if ctx.Err() != nil {
		return protocol.RunMessageFailed, "run_control_changed"
	}
	err := w.thread.Steer(ctx, w.turnID, text)
	switch {
	case err == nil:
		return protocol.RunMessageAccepted, ""
	case errors.Is(err, errSteerIdentity):
		return protocol.RunMessageUnknown, "receipt_identity_mismatch"
	case errors.Is(err, harness.ErrRejected):
		if rpc, ok := errors.AsType[*RPCError](err); ok && rpc.Code == -32601 {
			return protocol.RunMessageUnsupported, "native_method_unsupported"
		}
		return protocol.RunMessageFailed, "native_rejected"
	default:
		return protocol.RunMessageUnknown, "native_outcome_unknown"
	}
}

func (w *worker) Events() <-chan harness.WorkerEvent { return w.events }

func (w *worker) Wait() (harness.WorkerResult, error) {
	<-w.done
	return w.res, w.err
}

// ReconcileAcceptanceVerdict reads only the original frozen result and owned
// close receipt. Native session creation and turn execution are excluded.
func (ws *Workers) ReconcileAcceptanceVerdict(ctx context.Context, spec harness.WorkerSpec) (harness.WorkerResult, error) {
	verdicts := harness.Verdicts{Spec: spec}
	err := verdicts.RestoreOwnedSessionContext(ctx, spec.Thread)
	if err != nil && !errors.Is(err, harness.ErrNoAcceptanceVerdict) && ws.SupportsOwnedWorkerStop() {
		// A runner crash can lose Wait before the existing close receipt is
		// recorded. Only an independently bound owned process can fill that
		// factual gap; an absent legacy identity or an altered receipt cannot.
		_, closeErr := os.Stat(filepath.Join(spec.StateDir, "owned-session-close.json"))
		_, processErr := os.Stat(filepath.Join(spec.StateDir, ownedProcessFile))
		if errors.Is(closeErr, os.ErrNotExist) && processErr == nil {
			if stopErr := ws.StopOwnedWorker(ctx, spec); stopErr != nil {
				return harness.WorkerResult{}, fmt.Errorf("%w: %w", harness.ErrRetainedResumePending, stopErr)
			}
			if closeErr := verdicts.RecordOwnedSessionClose(spec.Thread); closeErr != nil {
				return harness.WorkerResult{}, fmt.Errorf("%w: record independently confirmed original close: %w", harness.ErrRetainedResumePending, closeErr)
			}
			err = verdicts.RestoreOwnedSessionContext(ctx, spec.Thread)
		}
	}
	if err != nil {
		return harness.WorkerResult{}, err
	}
	return harness.WorkerResult{Verdict: verdicts.Verdict()}, nil
}

// Start implements harness.Workers.
func (ws *Workers) Start(ctx context.Context, spec harness.WorkerSpec) (harness.Worker, error) {
	startupCtx, stopStartup := context.WithTimeout(ctx, ws.opts.StartupTimeout)
	defer stopStartup()
	verdicts := harness.Verdicts{StateDir: spec.StateDir, Spec: spec}
	if spec.Role == protocol.RoleAccept {
		if err := verdicts.RestoreContext(ctx); err != nil {
			return nil, err
		}
		if spec.AcceptanceEvidenceFiles && verdicts.Verdict() != nil {
			if err := verdicts.ConfirmOwnedSessionClose(); err != nil {
				return nil, fmt.Errorf("%w: original acceptance session closure is unconfirmed: %w", harness.ErrRetainedResumePending, err)
			}
			events := make(chan harness.WorkerEvent)
			close(events)
			return &restoredVerdictWorker{events: events, result: harness.WorkerResult{Verdict: verdicts.Verdict()}}, nil
		}
	}
	profile := harness.WorkerProfile(spec, ws.opts.Model, ws.opts.Effort, ws.opts.Acceptance)
	opts := ws.opts.Session
	opts.Dir = spec.Dir
	owned, err := ws.recordOwnedWorkerProcess(spec, &opts)
	if err != nil {
		return nil, err
	}
	previousRun, previousThread := spec.StoppedPredecessor()
	if previousRun != "" && previousThread != "" && spec.PredecessorStateDir != "" {
		opts.Environment, err = codexenv.InheritExecution(startupCtx, spec.StateDir, "run:"+spec.RunID, spec.PredecessorStateDir, "run:"+previousRun)
		if errors.Is(err, os.ErrNotExist) && spec.OptionalHistory() && spec.RetainedResume == nil {
			spec.Thread = ""
			opts.Environment, err = codexenv.BindExecution(startupCtx, spec.StateDir, "run:"+spec.RunID, ws.opts.Session.Environment, false)
		}
	} else {
		if previousRun != "" && previousThread != "" && opts.Environment != nil {
			if spec.OptionalHistory() {
				spec.Thread = ""
			} else {
				return nil, fmt.Errorf("%w: isolated continuation needs original execution state", harness.ErrWorkerNotStarted)
			}
		}
		opts.Environment, err = codexenv.BindExecution(startupCtx, spec.StateDir, "run:"+spec.RunID, opts.Environment, spec.Thread != "" || spec.RetainedResume != nil)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: environment setup: %w", harness.ErrWorkerNotStarted, err)
	}
	if err = codexenv.RecordSessionHome(startupCtx, ws.opts.EnvironmentHomes, opts.Environment); err != nil {
		return nil, fmt.Errorf("%w: environment setup: %w", harness.ErrWorkerNotStarted, err)
	}
	if opts.Environment != nil {
		opts.EnvironmentExecution = "run:" + spec.RunID
	}
	// The worker may inherit the owner's tend hooks and MCP server from
	// the user-level configuration; it is not the owner's agent (D45).
	opts.Env = append(append([]string(nil), opts.Env...), "TEND_SCRATCH="+spec.ScratchDir, protocol.AddonEnv+"=off")
	var s *Session
	var release func(error)
	if ws.opts.Acquire != nil {
		s, release, err = ws.opts.Acquire(startupCtx, spec)
	} else {
		s, err = ws.start(startupCtx, opts)
		release = func(error) {
			if s != nil {
				if err := s.Close(); err != nil {
					s.log.Warn("owned worker session close failed", "run_id", spec.RunID, "err", err)
				}
			}
		}
	}

	if err != nil {
		return nil, err
	}
	instructions, input := harness.Brief(spec)
	results := harness.Results{Spec: spec}
	if err := results.Restore(); err != nil {
		release(err)
		return nil, err
	}
	cfg := harness.ThreadConfig{LegacyRawUsage: spec.LegacyRawUsage,
		EnvironmentID: ws.opts.EnvironmentID, Cwd: spec.Dir, Model: profile.Model, Sandbox: ws.opts.Sandbox, ApprovalPolicy: ws.opts.ApprovalPolicy,
		DeveloperInstructions: instructions,
		Tools:                 []harness.ToolSpec{harness.ResultToolSpec}, OnToolCall: results.Handle,
	}
	if spec.Role == protocol.RoleAccept {
		cfg.Tools = []harness.ToolSpec{harness.VerdictToolFor(spec)}
		cfg.OnToolCall = func(ctx context.Context, c harness.ToolCall) harness.ToolResult {
			if c.Tool == harness.VerdictTool {
				return verdicts.Handle(ctx, c)
			}
			return results.Handle(ctx, c)
		}
	}
	if harness.HasObservation(spec) {
		cfg.Tools = append(cfg.Tools, harness.ReadObservationToolSpec)
	}
	if spec.ReadContext != nil {
		cfg.Tools = append(cfg.Tools, harness.ReadContextToolSpec)
	}
	if spec.DownloadFile != nil {
		cfg.Tools = append(cfg.Tools, harness.DownloadFileToolSpec)
	}
	if spec.WaitExternal != nil {
		cfg.Tools = append(cfg.Tools, harness.WaitExternalToolSpec)
	}
	if spec.AskOwner != nil {
		cfg.Tools = append(cfg.Tools, questionTool(spec))
	}
	th, err := ws.thread(startupCtx, s, spec, cfg)
	if err != nil {
		release(err)
		return nil, err
	}
	if owned != nil && owned.Process != (ownedProcessIdentity{}) {
		owned.ThreadID = th.ID()
		if err := saveOwnedWorkerProcess(spec, *owned); err != nil {
			release(err)
			return nil, err
		}
	}
	if spec.OnUsageSource != nil {
		if native, ok := th.(harness.NativeUsageThread); ok {
			if err := spec.OnUsageSource(startupCtx, native.UsageSource()); err != nil {
				release(err)
				return nil, err
			}
		}
	}
	if spec.OnThread != nil {
		if err := spec.OnThread(th.ID()); err != nil {
			release(err)
			return nil, err
		}
	}
	if spec.OnNativeSource != nil {
		source := protocol.RolloutPreviewSource{SessionID: th.ID()}
		if location, ok := th.(harness.ThreadLocation); ok {
			source.Path = location.RolloutPath()
		}
		if err := spec.OnNativeSource(startupCtx, source); err != nil {
			release(err)
			return nil, err
		}
	}
	if th.ID() == spec.Thread {
		input = harness.Continuation(spec)
		if spec.HandBack {
			input = harness.HandBack(spec)
		}
	}
	if ws.opts.BeforeStart != nil {
		input, err = ws.opts.BeforeStart(startupCtx, spec, th, input)
		if err != nil {
			release(err)
			return nil, err
		}
	}
	if spec.BeforeUsageDispatch != nil {
		if native, ok := th.(harness.NativeUsageThread); ok {
			if err := spec.BeforeUsageDispatch(startupCtx, native.UsageSource()); err != nil {
				release(err)
				return nil, err
			}
		}
	}
	turnID, err := th.StartTurn(startupCtx, harness.TurnInput{Text: input, Model: profile.Model, Effort: profile.Effort})
	if err != nil {
		release(err)
		return nil, err
	}
	if spec.OnStartupEvent != nil && ws.opts.Acquire == nil {
		body, marshalErr := json.Marshal(protocol.NativeIdentity{Provider: "appserver", SessionID: th.ID(), TurnID: turnID, State: "known", ControllerObservation: "managed"})
		if marshalErr != nil {
			release(marshalErr)
			return nil, marshalErr
		}
		if err := spec.OnStartupEvent(startupCtx, harness.WorkerEvent{Kind: protocol.EventNativeIdentity, Body: body}); err != nil {
			release(err)
			return nil, err
		}
	}
	w := &worker{thread: th.(*thread), turnID: turnID, events: make(chan harness.WorkerEvent), done: make(chan struct{})}
	go func() {
		defer close(w.done)
		defer close(w.events)
		defer func() {
			release(w.err)
			if spec.Role != protocol.RoleAccept || !spec.AcceptanceEvidenceFiles || ws.opts.Acquire != nil {
				return
			}
			if !s.ownedSessionCloseSucceeded() {
				if verdicts.Verdict() != nil {
					w.err = fmt.Errorf("%w: original owned acceptance session close was not confirmed", harness.ErrRetainedResumePending)
				}
				return
			}
			if err := verdicts.RecordOwnedSessionClose(); err != nil {
				s.log.Warn("owned acceptance session close receipt failed", "run_id", spec.RunID, "err", err)
				if verdicts.Verdict() != nil {
					w.err = fmt.Errorf("%w: record original owned acceptance session close: %w", harness.ErrRetainedResumePending, err)
				}
			}
		}()
		var summary string
	events:
		for {
			select {
			case <-ctx.Done():
				break events
			case ev, ok := <-th.Events():
				if !ok {
					w.err = errors.New("traex: the worker's app-server exited")
					return
				}
				if ev.RequestUsage != nil {
					body, err := json.Marshal(ev.RequestUsage)
					if err != nil {
						w.err = err
						return
					}
					select {
					case w.events <- harness.WorkerEvent{NativeID: ev.RequestUsage.RequestID, Kind: "usage.request", Body: body}:
					case <-ctx.Done():
						break events
					}
					continue
				}
				if ev.Kind == harness.EventChildRollout {
					select {
					case w.events <- childRolloutEvent(ev):
					case <-ctx.Done():
						break events
					}
					continue
				}

				if ev.TurnID != turnID {
					continue
				}

				if spec.ConcurrencyFeedback && (ev.Kind == harness.EventTurnStarted || ev.Kind == harness.EventTurnError || ev.Kind == harness.EventTurnCompleted) {
					f := protocol.ConcurrencyFeedback{SessionID: th.ID(), TurnID: ev.TurnID, Failure: ev.Failure}
					switch ev.Kind {
					case harness.EventTurnStarted:
						f.Kind = "running"
					case harness.EventTurnError:
						f.Kind = "congestion"
					case harness.EventTurnCompleted:
						if ev.TurnStatus == "completed" {
							f.Kind = "success"
						} else {
							f.Kind = "congestion"
						}
					}
					raw, _ := json.Marshal(f)
					select {
					case w.events <- harness.WorkerEvent{Kind: "worker.concurrency", Body: raw}:
					case <-ctx.Done():
						break events
					}
				}
				switch ev.Kind {
				case harness.EventTurnStarted, harness.EventItemStarted:
					select {
					case w.events <- nativeRolloutEvent(th.ID(), ev):
					case <-ctx.Done():
						break events
					}
				case harness.EventItemCompleted:
					if ev.ItemType == harness.ItemAgentMessage && ev.Text != "" {
						summary = ev.Text
					}
					completed := completedRolloutEvent(th.ID(), ev)
					select {
					case w.events <- completed:
					case <-ctx.Done():
						break events
					}
				case harness.EventTurnCompleted:
					select {
					case w.events <- nativeRolloutEvent(th.ID(), ev):
					case <-ctx.Done():
						break events
					}
					if spec.Role == protocol.RoleAccept && spec.AcceptanceEvidenceFiles {
						if err := verdicts.RestoreContext(ctx); err != nil {
							w.err = err
							return
						}
					}
					w.res = harness.WorkerResult{Verdict: verdicts.Verdict(), Result: results.Result(), Summary: summary}
					if ev.TurnStatus != "completed" {
						w.err = fmt.Errorf("traex: the worker's turn ended %s", ev.TurnStatus)
					}
					return
				}
			}
		}
		// Stop the turn, then the process with everything it
		// started (procx), before reporting.
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		var interruptErr error
		if ws.opts.Acquire == nil || !errors.Is(context.Cause(ctx), harness.ErrHeld) {
			interruptErr = th.Interrupt(stopCtx, turnID)
		}
		cancel()
		w.err = context.Cause(ctx)
		if ws.opts.Acquire != nil && errors.Is(w.err, harness.ErrPaused) && interruptErr != nil {
			w.err = errors.Join(w.err, ErrInterruptUnconfirmed, interruptErr)
		}
	}()
	return w, nil
}

func workerItemEvent(ev harness.Event) harness.WorkerEvent {
	return harness.WorkerEvent{NativeID: ev.ItemID, Kind: "item.completed", Body: redactedRolloutPayload("", ev)}
}

func redactedRolloutPayload(threadID string, ev harness.Event) json.RawMessage {
	if ev.ItemType == harness.ItemUserMessage {
		return json.RawMessage(`{"type":"userMessage"}`)
	}
	if ev.Kind == harness.EventTurnStarted || ev.Kind == harness.EventTurnCompleted {
		raw, _ := json.Marshal(map[string]any{"threadId": threadID, "turn": map[string]string{"id": ev.TurnID, "status": ev.TurnStatus}})
		return raw
	}
	return ev.Raw
}

func childRolloutEvent(ev harness.Event) harness.WorkerEvent {
	var child struct {
		Thread string            `json:"thread_id"`
		Turn   string            `json:"turn_id"`
		Kind   harness.EventKind `json:"kind"`
		Data   json.RawMessage   `json:"data,omitempty"`
	}
	if json.Unmarshal(ev.Raw, &child) != nil {
		return harness.WorkerEvent{Kind: "native.child", Body: json.RawMessage(`{"kind":"unavailable"}`)}
	}
	var identity struct {
		Type string `json:"type"`
		Turn struct {
			Status string `json:"status"`
		} `json:"turn"`
	}
	if json.Unmarshal(child.Data, &identity) != nil {
		child.Data = nil
	} else {
		child.Data = redactedRolloutPayload(child.Thread, harness.Event{Kind: child.Kind, TurnID: child.Turn, ItemType: identity.Type, TurnStatus: identity.Turn.Status, Raw: child.Data})
	}
	raw, _ := json.Marshal(child)
	return harness.WorkerEvent{Kind: "native.child", Body: raw}
}

// thread continues the thread of an earlier attempt of the run, or starts
// a new one when there is none or it cannot be resumed, for example
// because its rollout is gone (D61).
func (ws *Workers) thread(ctx context.Context, s *Session, spec harness.WorkerSpec, cfg harness.ThreadConfig) (harness.Thread, error) {
	if spec.Thread != "" {
		if ws.opts.ReuseUnstarted {
			return s.AttachUnstartedThread(ctx, spec.Thread, cfg.OnToolCall)
		}
		th, err := s.ResumeThread(ctx, spec.Thread, cfg)
		if err == nil {
			return th, nil
		}
		optionalContinuation := spec.RetainedResume == nil && spec.OptionalHistory()
		if ctx.Err() != nil || ws.opts.StrictResume && !optionalContinuation || !errors.Is(err, harness.ErrRejected) {
			return nil, err
		}
		if log := ws.opts.Session.Log; log != nil {
			log.InfoContext(ctx, "could not continue the worker's thread; starting over with the brief",
				"run_id", spec.RunID, "thread_id", spec.Thread, "err", err)
		}
	}
	return s.StartThread(ctx, cfg)
}

func nativeRolloutEvent(threadID string, ev harness.Event) harness.WorkerEvent {
	ev.Raw = redactedRolloutPayload(threadID, ev)
	body, _ := json.Marshal(struct {
		ThreadID string          `json:"thread_id"`
		TurnID   string          `json:"turn_id"`
		ItemID   string          `json:"item_id"`
		Data     json.RawMessage `json:"data,omitempty"`
	}{threadID, ev.TurnID, ev.ItemID, ev.Raw})
	return harness.WorkerEvent{NativeID: ev.ItemID, Kind: "native." + string(ev.Kind), Body: body}
}

// Completion fields stay at the root for existing CLI and server readers.
// Optional attribution lets newer readers pair lifecycle facts without
// changing the opaque event contract or requiring a newer runner protocol.
func completedRolloutEvent(threadID string, ev harness.Event) harness.WorkerEvent {
	base := workerItemEvent(ev)
	var body map[string]json.RawMessage
	if json.Unmarshal(base.Body, &body) != nil || body == nil {
		return harness.WorkerEvent{NativeID: ev.ItemID, Kind: "item.completed", Body: base.Body}
	}
	for key, value := range map[string]string{"thread_id": threadID, "turn_id": ev.TurnID, "item_id": ev.ItemID} {
		body[key], _ = json.Marshal(value)
	}
	raw, _ := json.Marshal(body)
	return harness.WorkerEvent{NativeID: ev.ItemID, Kind: "item.completed", Body: raw}
}

func questionTool(spec harness.WorkerSpec) harness.ToolSpec {
	if spec.QuestionHandoffs {
		return harness.HandoffQuestionTool(spec.QuestionContext)
	}
	if spec.SupervisorQuestions {
		return harness.SupervisorQuestionTool(spec.QuestionContext)
	}
	return harness.OwnerQuestionTool(spec.QuestionContext)
}
