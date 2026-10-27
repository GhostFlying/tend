package traex

import (
	"context"
	"fmt"

	"github.com/GhostFlying/tend/internal/harness"
)

var _ harness.ThreadForker = (*Session)(nil)
var _ harness.ThreadLocation = (*thread)(nil)

const (
	methodThreadFork        = "thread/fork"
	methodThreadUnsubscribe = "thread/unsubscribe"
)

// ThreadForkParams creates a non-persisted fork. Paginated ephemeral forks
// require excludeTurns; their inherited history is still model-visible.
type ThreadForkParams struct {
	ExperimentalRawEvents bool           `json:"experimentalRawEvents"`
	ThreadID              string         `json:"threadId"`
	Ephemeral             bool           `json:"ephemeral"`
	ExcludeTurns          bool           `json:"excludeTurns"`
	Model                 string         `json:"model,omitempty"`
	Cwd                   string         `json:"cwd,omitempty"`
	ApprovalPolicy        string         `json:"approvalPolicy,omitempty"`
	ApprovalsReviewer     string         `json:"approvalsReviewer,omitempty"`
	Sandbox               string         `json:"sandbox,omitempty"`
	DeveloperInstructions string         `json:"developerInstructions,omitempty"`
	Config                map[string]any `json:"config,omitempty"`
}

// ThreadUnsubscribeParams drops this client's subscription, allowing native
// idle unloading. Ephemeral threads cannot be archived or deleted.
type ThreadUnsubscribeParams struct {
	ThreadID string `json:"threadId"`
}

// ThreadUnsubscribeResponse reports whether a subscription was removed.
type ThreadUnsubscribeResponse struct {
	Status string `json:"status"`
}

// ForkThread implements harness.ThreadForker. Inherited dynamic tools always
// use the supplied handler, never the source thread's handler.
func (s *Session) ForkThread(ctx context.Context, sourceThreadID string, cfg harness.ThreadConfig) (harness.Thread, error) {
	var res ThreadStartResponse
	if err := s.conn.call(ctx, methodThreadFork, ThreadForkParams{
		ThreadID: sourceThreadID, Ephemeral: true, ExcludeTurns: true, ExperimentalRawEvents: true,
		Model: cfg.Model, Cwd: cfg.Cwd, ApprovalPolicy: cfg.ApprovalPolicy,
		ApprovalsReviewer: cfg.ApprovalsReviewer, Sandbox: cfg.Sandbox,
		DeveloperInstructions: cfg.DeveloperInstructions, Config: cfg.Config,
	}, &res); err != nil {
		return nil, err
	}
	if res.Thread.ID == "" || res.Thread.ID == sourceThreadID {
		return nil, fmt.Errorf("traex: invalid isolated fork identity")
	}
	t := s.register(res.Thread.ID, cfg.OnToolCall)
	t.mu.Lock()
	t.fork = true
	t.mu.Unlock()
	t.setModelDefaults(res)
	t.setPermissionDefaults(res)
	t.setNetworkPolicy(cfg, res)
	t.setRolloutPath(res.Thread.Path)
	return t, nil
}

// ReleaseThread implements harness.ThreadForker. It deliberately never issues
// archive/delete: those APIs reject ephemeral threads and must not touch parents.
func (s *Session) ReleaseThread(ctx context.Context, threadID string) error {
	unlock, err := s.lockThreadLifecycle(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	t := s.thread(threadID)
	if t == nil {
		return fmt.Errorf("%w: thread is not an owned disposable fork", harness.ErrRejected)
	}
	t.mu.Lock()
	owned, released := t.fork, t.released
	t.mu.Unlock()
	if !owned {
		return fmt.Errorf("%w: thread is not an owned disposable fork", harness.ErrRejected)
	}
	if released {
		return nil
	}
	var res ThreadUnsubscribeResponse
	if err := s.conn.call(ctx, methodThreadUnsubscribe, ThreadUnsubscribeParams{ThreadID: threadID}, &res); err != nil {
		return err
	}
	switch res.Status {
	case "unsubscribed", "notSubscribed", "notLoaded":
	default:
		return fmt.Errorf("traex: unknown unsubscribe status %q", res.Status)
	}
	t.releaseLocal()
	return nil
}

func (t *thread) toolsReleased() bool { t.mu.Lock(); defer t.mu.Unlock(); return t.released }
func (t *thread) setRolloutPath(path *string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if path != nil {
		t.rolloutPath = *path
	}
}

// RolloutPath implements harness.ThreadLocation.
func (t *thread) RolloutPath() string { t.mu.Lock(); defer t.mu.Unlock(); return t.rolloutPath }

func (t *thread) toolHandler() harness.ToolHandler {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.released {
		return nil
	}
	return t.onTool
}

// lockThreadLifecycle is cancellable even while a prior native request is
// waiting for its response. Turns and event routing do not acquire this gate.
func (s *Session) lockThreadLifecycle(ctx context.Context) (func(), error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.stopped:
		return nil, fmt.Errorf("traex: session closed")
	case s.threadLifecycle <- struct{}{}:
		return func() { <-s.threadLifecycle }, nil
	}
}

func (t *thread) releaseLocal() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.released {
		t.released = true
		t.onTool = nil
		t.queue = nil
		close(t.releaseDone)
	}
}

func (t *thread) setToolHandler(handler harness.ToolHandler) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.released {
		t.onTool = handler
	}
}
