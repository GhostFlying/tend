package remote

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

type retainedThread struct {
	harness.Thread
	events chan harness.Event
}

func TestUnconfirmedTaskContextCannotResumeWithExecutorStopProof(t *testing.T) {
	dir := t.TempDir()
	pin, _ := json.Marshal(workerState{Kind: "central-traex", RunID: "run", Thread: "original", Cwd: "/workspace", StateDir: dir, ProviderID: "folder:original", ExecutorStopped: true, BriefPending: true})
	workers := NewWorkers(WorkerOptions{Bin: "must-not-start"})
	defer workers.Close()
	_, err := workers.Start(t.Context(), harness.WorkerSpec{Lifetime: t.Context(), RunID: "run", Thread: string(pin), Dir: "/workspace", StateDir: dir, ProviderID: "folder:original", NativeDial: func(context.Context, string) (io.ReadWriteCloser, error) {
		t.Error("unconfirmed task context opened a new native transport")
		return nil, context.Canceled
	}})
	if err == nil || !strings.Contains(err.Error(), "reconciliation") {
		t.Fatalf("unconfirmed initial context was replayed: %v", err)
	}
}

func (t retainedThread) Events() <-chan harness.Event { return t.events }

func TestHandbackWaitsForRetainedUsageWithoutCancellingRun(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered := make(chan context.Context, 1)
	finish := make(chan struct{})
	events := make(chan harness.Event, 1)
	ex := &execution{ctx: ctx, stop: cancel, thread: retainedThread{events: events}, spec: harness.WorkerSpec{OnRetainedEvent: func(ctx context.Context, ev harness.WorkerEvent) error {
		if ev.Kind != "usage.request" || ev.NativeID != "request-owner" {
			t.Errorf("wrong retained fact: %+v", ev)
		}
		entered <- ctx
		<-finish
		return ctx.Err()
	}}}
	ex.startDrain()
	events <- harness.Event{RequestUsage: &protocol.TokenUsageSample{RequestID: "request-owner"}}
	var persistCtx context.Context
	select {
	case persistCtx = <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("retained usage was discarded")
	}
	ex.drainCancel()
	if persistCtx.Err() != nil {
		t.Error("handback cancelled an in-flight durable fact")
	}
	close(finish)
	select {
	case <-ex.drainDone:
	case <-time.After(3 * time.Second):
		t.Fatal("retained event drain did not stop")
	}
	if ctx.Err() != nil {
		t.Fatal("handback stopped the retained execution")
	}
}
