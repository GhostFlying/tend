package traex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

// WorkerOptions configure the workers a runner starts.
type WorkerOptions struct {
	BeforeStart func(context.Context, harness.WorkerSpec, harness.Thread, string) (string, error)

	// Acquire retains a run session independently of a worker attempt. The release
	// callback must prove execution stopped unless the attempt is held or paused.
	Acquire        func(context.Context, harness.WorkerSpec) (*Session, func(error), error)
	EnvironmentID  string
	StrictResume   bool
	ReuseUnstarted bool

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

// Start implements harness.Workers.
func (ws *Workers) Start(ctx context.Context, spec harness.WorkerSpec) (harness.Worker, error) {
	profile := harness.WorkerProfile(spec, ws.opts.Model, ws.opts.Effort, ws.opts.Acceptance)
	opts := ws.opts.Session
	opts.Dir = spec.Dir
	// The worker may inherit the owner's tend hooks and MCP server from
	// the user-level configuration; it is not the owner's agent (D45).
	opts.Env = append(append([]string(nil), opts.Env...), "TEND_SCRATCH="+spec.ScratchDir, protocol.AddonEnv+"=off")
	var s *Session
	var release func(error)
	var err error
	if ws.opts.Acquire != nil {
		s, release, err = ws.opts.Acquire(ctx, spec)
	} else {
		s, err = ws.start(ctx, opts)
		release = func(error) {
			if s != nil {
				_ = s.Close()
			}
		}
	}

	if err != nil {
		return nil, err
	}
	instructions, input := harness.Brief(spec)
	verdicts := harness.Verdicts{StateDir: spec.StateDir}
	if spec.Role == protocol.RoleAccept {
		if err := verdicts.Restore(); err != nil {
			release(err)
			return nil, err
		}
	}
	results := harness.Results{Spec: spec}
	if err := results.Restore(); err != nil {
		release(err)
		return nil, err
	}
	cfg := harness.ThreadConfig{
		EnvironmentID: ws.opts.EnvironmentID, Cwd: spec.Dir, Model: profile.Model, Sandbox: ws.opts.Sandbox, ApprovalPolicy: ws.opts.ApprovalPolicy,
		DeveloperInstructions: instructions,
		Tools:                 []harness.ToolSpec{harness.ResultToolSpec}, OnToolCall: results.Handle,
	}
	if spec.Role == protocol.RoleAccept {
		cfg.Tools = []harness.ToolSpec{harness.VerdictToolSpec}
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
	if spec.DownloadFile != nil {
		cfg.Tools = append(cfg.Tools, harness.DownloadFileToolSpec)
	}
	if spec.AskOwner != nil {
		cfg.Tools = append(cfg.Tools, harness.OwnerQuestionTool(spec.QuestionContext))
	}
	th, err := ws.thread(ctx, s, spec, cfg)
	if err != nil {
		release(err)
		return nil, err
	}
	if spec.OnThread != nil {
		if err := spec.OnThread(th.ID()); err != nil {
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
		input, err = ws.opts.BeforeStart(ctx, spec, th, input)
		if err != nil {
			release(err)
			return nil, err
		}
	}
	turnID, err := th.StartTurn(ctx, harness.TurnInput{Text: input, Model: profile.Model, Effort: profile.Effort})
	if err != nil {
		release(err)
		return nil, err
	}
	w := &worker{thread: th.(*thread), turnID: turnID, events: make(chan harness.WorkerEvent), done: make(chan struct{})}
	go func() {
		defer close(w.done)
		defer close(w.events)
		defer func() { release(w.err) }()
		var summary string
		for {
			select {
			case <-ctx.Done():
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
				return
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
						return
					}
					continue
				}
				if ev.Kind == harness.EventChildRollout {
					select {
					case w.events <- childRolloutEvent(ev):
					case <-ctx.Done():
						return
					}
					continue
				}

				if ev.TurnID != turnID {
					continue
				}
				switch ev.Kind {
				case harness.EventTurnStarted, harness.EventItemStarted:
					select {
					case w.events <- nativeRolloutEvent(th.ID(), ev):
					case <-ctx.Done():
						return
					}
				case harness.EventItemCompleted:
					if ev.ItemType == harness.ItemAgentMessage && ev.Text != "" {
						summary = ev.Text
					}
					completed := completedRolloutEvent(th.ID(), ev)
					select {
					case w.events <- completed:
					case <-ctx.Done():
					}
				case harness.EventTurnCompleted:
					select {
					case w.events <- nativeRolloutEvent(th.ID(), ev):
					case <-ctx.Done():
						return
					}
					w.res = harness.WorkerResult{Verdict: verdicts.Verdict(), Result: results.Result(), Summary: summary}
					if ev.TurnStatus != "completed" {
						w.err = fmt.Errorf("traex: the worker's turn ended %s", ev.TurnStatus)
					}
					return
				}
			}
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
		if ctx.Err() != nil || ws.opts.StrictResume {
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
