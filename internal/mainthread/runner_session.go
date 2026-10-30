package mainthread

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/coordination"
	"github.com/GhostFlying/tend/internal/protocol"
)

// RunnerSession reuses the coordinator and its source/authorization semantics;
// only the native harness lifecycle crosses the leased runner transport.
func RunnerSession(ctx context.Context, a *app.App, run string) harness.Session {
	// Close owns cancellation; this attachment can also consume retained
	// unacknowledged facts from earlier runs of the same responsibility.
	return coordination.New(context.WithoutCancel(ctx), &runnerTransport{app: a, run: run, origins: map[int64]managerEventOrigin{}})
}

type managerEventOrigin struct {
	run      string
	sequence int64
}

type runnerTransport struct {
	mu        sync.Mutex
	rowCursor int64
	sequence  int64
	origins   map[int64]managerEventOrigin
	app       *app.App
	run       string
}

func (t *runnerTransport) Call(ctx context.Context, method string, input, output any) error {
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
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wake:
		}
	}
}
func (t *runnerTransport) Events(ctx context.Context, _ int64) ([]coordination.Event, error) {
	for {
		wake := t.app.Changes().Wait()
		events, err := t.app.ManagerReplayEvents(ctx, t.run, t.rowCursor)
		if err != nil {
			return nil, err
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
				item.Historical = e.RunID != t.run
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
	turn, item := "", ""
	flush := e.Disconnected
	if e.Native != nil {
		turn = e.Native.TurnID
		item = e.Native.ItemID
		flush = flush || e.Native.Kind == harness.EventCoordinatorHistoryEnd || (e.Native.Kind == harness.EventItemCompleted && e.Native.ItemType == harness.ItemAgentMessage) || e.Native.Kind == harness.EventTurnCompleted
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
