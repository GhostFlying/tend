package coordination

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
)

type startupTailTransport struct {
	batch   []Event
	reads   atomic.Int64
	drained chan struct{}
}

func (r *startupTailTransport) Events(ctx context.Context, _ int64) ([]Event, error) {
	if r.reads.Add(1) == 1 {
		return r.batch, nil
	}
	if r.reads.Load() == 2 {
		close(r.drained)
	}
	<-ctx.Done()
	return nil, ctx.Err()
}
func (r *startupTailTransport) Call(_ context.Context, method string, _ any, out any) error {
	if method == "resume" {
		b, _ := json.Marshal(ThreadInfo{ID: "retained-thread"})
		return json.Unmarshal(b, out)
	}
	return nil
}

type projectedStartupTailTransport struct {
	*startupTailTransport
	projected atomic.Int64
	resultRPC atomic.Int64
	acked     atomic.Int64
}

func (r *projectedStartupTailTransport) ProjectsHistoricalCallback(_ context.Context, e Event) bool {
	if e.Historical && e.SourceRunID == "original-run" && e.SourceSequence == 99 {
		r.projected.Add(1)
		return true
	}
	return false
}

func (r *projectedStartupTailTransport) Call(ctx context.Context, method string, in, out any) error {
	if method == "tool_result" {
		r.resultRPC.Add(1)
		return harness.ErrRejected
	}
	return r.startupTailTransport.Call(ctx, method, in, out)
}

func (r *projectedStartupTailTransport) Acknowledge(context.Context, Event) error {
	r.acked.Add(1)
	return nil
}

func TestHistoricalCallbackBeforeAttachmentUsesProjectionGuard(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	tr := &projectedStartupTailTransport{startupTailTransport: &startupTailTransport{
		drained: make(chan struct{}),
		batch: []Event{{
			Sequence: 1, ThreadID: "retained-thread", Historical: true, SourceRunID: "original-run", SourceSequence: 99,
			Call: &harness.ToolCall{ThreadID: "retained-thread", TurnID: "original-turn", CallID: "original-call"},
		}},
	}}
	s := New(ctx, tr)
	t.Cleanup(func() { _ = s.Close() })
	select {
	case <-tr.drained:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err := s.ResumeThread(ctx, "retained-thread", harness.ThreadConfig{}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if calls, acks := tr.resultRPC.Load(), tr.acked.Load(); tr.projected.Load() != 1 || calls != 0 || acks != 0 {
		t.Fatalf("buffered historical callback: projected=%d new result calls=%d acknowledgements=%d", tr.projected.Load(), calls, acks)
	}
}

func TestHistoricalConnectionEndBeforeAttachment(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	tr := &startupTailTransport{batch: []Event{{Sequence: 1, Historical: true, Disconnected: true, SourceRunID: "original-run", SourceSequence: 99}}, drained: make(chan struct{})}
	s := New(ctx, tr)
	defer s.Close()
	select {
	case <-tr.drained:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	th, err := s.ResumeThread(ctx, "retained-thread", harness.ThreadConfig{})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case e, ok := <-th.Events():
		if !ok || e.Kind != harness.EventCoordinatorHistoryEnd {
			t.Fatalf("missing history boundary: %+v, %v", e, ok)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("historical connection end consumed before attachment was lost")
	}
}
func TestLiveConnectionEndBeforeAttachment(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	tr := &startupTailTransport{batch: []Event{
		{Sequence: 1, ThreadID: "retained-thread", Native: &harness.Event{Kind: harness.EventItemCompleted, ItemID: "retained-tail"}},
		{Sequence: 2, Disconnected: true},
	}, drained: make(chan struct{})}
	s := New(ctx, tr)
	defer s.Close()
	// Wait until the startup backlog was consumed, independently of whether the
	// old implementation retained it or the candidate canceled the session.
	deadline := time.NewTicker(time.Millisecond)
	defer deadline.Stop()
	for {
		s.mu.Lock()
		ready := len(s.orphans[""]) > 0 || s.ctx.Err() != nil
		s.mu.Unlock()
		if ready {
			break
		}
		select {
		case <-deadline.C:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	th, err := s.ResumeThread(ctx, "retained-thread", harness.ThreadConfig{})
	if err != nil {
		t.Fatalf("cannot attach to drain original queued tail: %v", err)
	}
	for _, kind := range []harness.EventKind{harness.EventItemCompleted, harness.EventCoordinatorDisconnected} {
		select {
		case e, ok := <-th.Events():
			if !ok || e.Kind != kind {
				t.Fatalf("lost retained event: %+v, %v", e, ok)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}
