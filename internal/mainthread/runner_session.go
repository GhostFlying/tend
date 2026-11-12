package mainthread

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/coordination"
	"github.com/GhostFlying/tend/internal/protocol"
)

// RunnerSession reuses the coordinator and its source/authorization semantics;
// only the native harness lifecycle crosses the leased runner transport.
// An optional shutdown context is the lifetime of an in-process runner, never
// the attachment or delivery context, which can end before native close.
func RunnerSession(ctx context.Context, a *app.App, run string, shutdown ...context.Context) harness.Session {
	var lifetime context.Context
	if len(shutdown) > 0 {
		lifetime = shutdown[0]
	}
	// Close owns cancellation; this attachment can also consume retained
	// unacknowledged facts from earlier runs of the same responsibility.
	return coordination.New(context.WithoutCancel(ctx), &runnerTransport{attachment: lifetime, app: a, run: run, origins: map[int64]managerEventOrigin{}})
}

type managerEventOrigin struct {
	run      string
	sequence int64
}

type runnerTransport struct {
	attachment  context.Context
	mu          sync.Mutex
	rowCursor   int64
	sequence    int64
	origins     map[int64]managerEventOrigin
	app         *app.App
	run         string
	streamEnded bool
}

func (t *runnerTransport) Call(ctx context.Context, method string, input, output any) error {
	// A stopped attachment cannot deliver a native close. Leave any original
	// queued request unresolved for reconciliation; do not infer physical stop.
	var attachmentStopped <-chan struct{}
	if method == "close" && t.attachment != nil {
		if err := t.attachment.Err(); err != nil {
			return err
		}
		attachmentStopped = t.attachment.Done()
	}

	if method == "models" || method == "resume" {
		var thread string
		if request, ok := input.(coordination.Request); ok {
			thread = request.ThreadID
		}
		cached, err := t.app.ManagerClosingAttachment(ctx, t.run, method, thread)
		if err != nil {
			return err
		}
		if len(cached) > 0 {
			if output != nil {
				return json.Unmarshal(cached, output)
			}
			return nil
		}
	}
	t.mu.Lock()
	streamEnded := t.streamEnded
	t.mu.Unlock()
	if streamEnded && method != "close" {
		return fmt.Errorf("%w: native event transport ended; only retained closing metadata is available", harness.ErrRejected)
	}
	ended, err := t.app.ManagerRunEnded(ctx, t.run)
	if err != nil {
		return err
	}
	if ended {
		if method == "close" {
			return nil
		}
		return fmt.Errorf("%w: manager run ended", harness.ErrRejected)
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return err
	}
	id, err := t.app.QueueManagerNative(ctx, t.run, method, raw)
	if err != nil {
		switch protocol.CodeOf(err) {
		case protocol.CodeForbidden, protocol.CodeInvalidTransition, protocol.CodeInvalidRequest, protocol.CodeStaleRevision, protocol.CodeNotFound:
			return fmt.Errorf("%w: native command was not queued: %w", harness.ErrRejected, err)
		}
		return err
	}
	return t.awaitReply(ctx, method, id, output, attachmentStopped)
}

func (t *runnerTransport) awaitReply(ctx context.Context, method, id string, output any, attachmentStopped <-chan struct{}) error {
	for {
		wake := t.app.Changes().Wait()
		reply, ready, err := t.app.ManagerNativeResult(ctx, t.run, id)
		if err != nil {
			return err
		}
		if ready {
			if reply.Error != "" {
				if reply.Rejected {
					return fmt.Errorf("%w: %s", harness.ErrRejected, reply.Error)
				}
				return errors.New(reply.Error)
			}
			if method == "close" {
				// The committed close reply already released the run. Unknown
				// business receipts continue in a separate read-only run.
				return nil
			}
			if output != nil {
				return json.Unmarshal(reply.Result, output)
			}
			return nil
		}
		ended, err := t.app.ManagerRunEnded(ctx, t.run)
		if err != nil {
			return err
		}
		if ended {
			if method == "models" || method == "find" || method == "usage" {
				return fmt.Errorf("%w: manager run ended before its observational reply", harness.ErrRejected)
			}
			// A queued mutation may have executed before the carrier ended.
			// EOF releases this waiter, but cannot turn its lost reply into a
			// definite rejection or authorize another native execution.
			return errors.New("manager run ended before its native reply; reconcile the original operation")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-attachmentStopped:
			return t.attachment.Err()
		case <-wake:
		}
	}
}
func (t *runnerTransport) Events(ctx context.Context, _ int64) (eventsOut []coordination.Event, eventErr error) {
	defer func() {
		if eventErr != nil {
			t.mu.Lock()
			t.streamEnded = true
			t.mu.Unlock()
		}
	}()
	for {
		wake := t.app.Changes().Wait()
		events, err := t.app.ManagerReplayEvents(ctx, t.run, t.rowCursor)
		if err != nil {
			return nil, err
		}
		if len(events) == 0 {
			ended, err := t.app.ManagerRunEnded(ctx, t.run)
			if err != nil {
				return nil, err
			}
			if ended {
				// A final event may have committed between the first empty read
				// and the physical end. Terminal Runs cannot append new events.
				events, err = t.app.ManagerReplayEvents(ctx, t.run, t.rowCursor)
				if err != nil {
					return nil, err
				}
				if len(events) == 0 {
					return nil, io.EOF
				}
			}
		}
		if len(events) > 0 {
			out := make([]coordination.Event, 0, len(events))
			for _, e := range events {
				var item coordination.Event
				if err = json.Unmarshal(e.Body, &item); err != nil {
					return nil, err
				}
				t.sequence++
				item.Sequence = t.sequence
				item.SourceRunID = e.RunID
				item.SourceSequence = e.Sequence
				item.Historical = e.RunID != t.run
				if item.Disconnected && !item.Historical {
					t.mu.Lock()
					t.streamEnded = true
					t.mu.Unlock()
				}
				t.rowCursor = e.Cursor
				t.mu.Lock()
				if t.origins == nil {
					t.origins = map[int64]managerEventOrigin{}
				}
				t.origins[item.Sequence] = managerEventOrigin{run: e.RunID, sequence: e.Sequence}
				t.mu.Unlock()
				out = append(out, item)
			}
			return out, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-wake:
		case <-time.After(time.Second):
		}
	}
}

func (t *runnerTransport) Acknowledge(ctx context.Context, e coordination.Event) error {
	flush := e.Disconnected
	if e.Native != nil {
		flush = flush || e.Native.Kind == harness.EventCoordinatorHistoryEnd || (e.Native.Kind == harness.EventItemCompleted && e.Native.ItemType == harness.ItemAgentMessage) || e.Native.Kind == harness.EventTurnCompleted
	}
	return t.acknowledge(ctx, e, flush)
}

func (t *runnerTransport) AcknowledgeReplyPrefix(ctx context.Context, e coordination.Event, consumed []int64) error {
	if e.Native == nil || e.Native.Kind != harness.EventAgentMessageDelta {
		return errors.New("reply prefix requires its original delta identity")
	}
	t.mu.Lock()
	origins := map[string][]int64{}
	lastConsumed := false
	for _, sequence := range consumed {
		origin, ok := t.origins[sequence]
		if !ok {
			t.mu.Unlock()
			return errors.New("reply prefix has no original receipt identity")
		}
		origins[origin.run] = append(origins[origin.run], origin.sequence)
		lastConsumed = lastConsumed || sequence == e.Sequence
	}
	t.mu.Unlock()
	if !lastConsumed {
		return errors.New("reply prefix did not consume its last delta")
	}
	for run, sequences := range origins {
		var last int64
		for _, sequence := range sequences {
			last = max(last, sequence)
		}
		if err := t.app.AcknowledgeManagerReplyPrefix(ctx, run, last, e.ThreadID, e.Native.TurnID, e.Native.ItemID, e.Native.Text, sequences); err != nil {
			return err
		}
	}
	return nil
}

func (t *runnerTransport) acknowledge(ctx context.Context, e coordination.Event, flush bool) error {
	turn, item := "", ""
	if e.Native != nil {
		turn, item = e.Native.TurnID, e.Native.ItemID
	}
	t.mu.Lock()
	origin, ok := t.origins[e.Sequence]
	t.mu.Unlock()
	if !ok {
		return errors.New("manager event has no original receipt identity")
	}
	return t.app.AcknowledgeManagerEvent(ctx, origin.run, origin.sequence, e.ThreadID, turn, item, flush)
}

func (t *runnerTransport) ReplayToolResult(ctx context.Context, e coordination.Event) (harness.ToolResult, error) {
	if e.Call == nil || !e.Historical {
		return harness.ToolResult{}, errors.New("not a historical callback")
	}
	raw, err := t.app.ManagerRetainedToolResult(ctx, t.run, e.SourceRunID, e.Call.ThreadID, e.Call.TurnID, e.Call.CallID)
	if err != nil {
		return harness.ToolResult{}, err
	}
	if len(raw) == 0 {
		return harness.ToolResult{Text: "Original callback result requires reconciliation; no tool was executed"}, nil
	}
	var result harness.ToolResult
	err = json.Unmarshal(raw, &result)
	return result, err
}
