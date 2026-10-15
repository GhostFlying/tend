package traex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

// WorkerOptions configure the workers a runner starts.
type WorkerOptions struct {
	// Session starts the app-server. Workers use the machine's own traex
	// installation and configuration (D38); leave Env without TRAE_HOME.
	Session Options
	// Model overrides the configured default model when set.
	Model string
	// Sandbox defaults to danger-full-access and ApprovalPolicy to never
	// (D39): workers run unattended.
	Sandbox        string
	ApprovalPolicy string
}

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
	events chan harness.WorkerEvent
	done   chan struct{}
	res    harness.WorkerResult
	err    error
}

func (w *worker) Events() <-chan harness.WorkerEvent { return w.events }

func (w *worker) Wait() (harness.WorkerResult, error) {
	<-w.done
	return w.res, w.err
}

// Start implements harness.Workers.
func (ws *Workers) Start(ctx context.Context, spec harness.WorkerSpec) (harness.Worker, error) {
	opts := ws.opts.Session
	opts.Dir = spec.Dir
	opts.Env = append(append([]string(nil), opts.Env...), "TEND_SCRATCH="+spec.ScratchDir)
	s, err := ws.start(ctx, opts)
	if err != nil {
		return nil, err
	}
	instructions, input := harness.Brief(spec)
	var (
		mu      sync.Mutex
		verdict *protocol.VerdictPayload
	)
	cfg := harness.ThreadConfig{
		Cwd: spec.Dir, Model: ws.opts.Model, Sandbox: ws.opts.Sandbox, ApprovalPolicy: ws.opts.ApprovalPolicy,
		DeveloperInstructions: instructions,
	}
	if spec.Role == protocol.RoleAccept {
		cfg.Tools = []harness.ToolSpec{harness.VerdictToolSpec}
		cfg.OnToolCall = func(_ context.Context, c harness.ToolCall) harness.ToolResult {
			if c.Tool != harness.VerdictTool {
				return harness.ToolResult{Text: "unknown tool"}
			}
			var v protocol.VerdictPayload
			if err := json.Unmarshal(c.Arguments, &v); err != nil || !v.Verdict.Valid() {
				return harness.ToolResult{Text: "the verdict must be passed, changes_required, blocked or inconclusive"}
			}
			mu.Lock()
			verdict = &v
			mu.Unlock()
			return harness.ToolResult{Success: true, Text: "verdict recorded"}
		}
	}
	th, err := s.StartThread(ctx, cfg)
	if err != nil {
		_ = s.Close()
		return nil, err
	}
	turnID, err := th.StartTurn(ctx, harness.TurnInput{Text: input})
	if err != nil {
		_ = s.Close()
		return nil, err
	}
	w := &worker{events: make(chan harness.WorkerEvent), done: make(chan struct{})}
	go func() {
		defer close(w.done)
		defer close(w.events)
		defer s.Close()
		var summary string
		for {
			select {
			case <-ctx.Done():
				// Stop the turn, then the process with everything it
				// started (procx), before reporting.
				stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				_ = th.Interrupt(stopCtx, turnID)
				cancel()
				w.err = context.Cause(ctx)
				return
			case ev, ok := <-th.Events():
				if !ok {
					w.err = errors.New("traex: the worker's app-server exited")
					return
				}
				if ev.TurnID != turnID {
					continue
				}
				switch ev.Kind {
				case harness.EventItemCompleted:
					if ev.ItemType == harness.ItemAgentMessage && ev.Text != "" {
						summary = ev.Text
					}
					select {
					case w.events <- harness.WorkerEvent{NativeID: ev.ItemID, Kind: "item.completed", Body: ev.Raw}:
					case <-ctx.Done():
					}
				case harness.EventTurnCompleted:
					mu.Lock()
					w.res = harness.WorkerResult{Verdict: verdict, Summary: summary}
					mu.Unlock()
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
