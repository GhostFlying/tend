package traex

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

// fakeWorkers returns Workers that talk to a fake app-server.
func fakeWorkers(t *testing.T) (*Workers, chan *fakeServer) {
	servers := make(chan *fakeServer, 1)
	w := NewWorkers(WorkerOptions{})
	w.start = func(ctx context.Context, _ Options) (*Session, error) {
		f, s := newFake(t)
		f.on(methodThreadStart, func(json.RawMessage) any { return map[string]any{"thread": map[string]any{"id": "th_w"}} })
		f.on(methodTurnStart, func(json.RawMessage) any { return map[string]any{"turn": map[string]any{"id": "tu_w"}} })
		f.on(methodTurnInterrupt, func(json.RawMessage) any { return map[string]any{} })
		servers <- f
		return s, nil
	}
	return w, servers
}

func TestAcceptanceWorkerReportsVerdict(t *testing.T) {
	ws, servers := fakeWorkers(t)
	w, err := ws.Start(context.Background(), harness.WorkerSpec{Role: protocol.RoleAccept, Dir: "/w", Spec: json.RawMessage(`{"goal":"g","acceptance":["a"]}`)})
	if err != nil {
		t.Fatal(err)
	}
	f := <-servers
	var start ThreadStartParams
	_ = json.Unmarshal(f.params(methodThreadStart), &start)
	if start.Sandbox != "danger-full-access" || start.ApprovalPolicy != "never" || len(start.DynamicTools) != 1 || start.DynamicTools[0].Name != harness.VerdictTool {
		t.Errorf("thread/start = %s", f.params(methodThreadStart))
	}
	f.send(map[string]any{"id": 5, "method": methodToolCall, "params": map[string]any{"threadId": "th_w", "turnId": "tu_w", "callId": "c", "tool": harness.VerdictTool,
		"arguments": map[string]any{"verdict": "passed", "evidence": map[string]any{"tests": "ok"}}}})
	<-f.answers
	f.send(map[string]any{"method": notifyItemCompleted, "params": map[string]any{"threadId": "th_w", "turnId": "tu_w", "item": map[string]any{"id": "i1", "type": "agentMessage", "text": "All good."}}})
	f.send(map[string]any{"method": notifyTurnCompleted, "params": map[string]any{"threadId": "th_w", "turn": map[string]any{"id": "tu_w", "status": "completed"}}})
	var events int
	for range w.Events() {
		events++
	}
	res, err := w.Wait()
	if err != nil || res.Verdict == nil || res.Verdict.Verdict != protocol.VerdictPassed || res.Summary != "All good." || events != 1 {
		t.Errorf("result = %+v, %v (events %d)", res, err, events)
	}
}

func TestStoppedWorkerInterruptsTurn(t *testing.T) {
	ws, servers := fakeWorkers(t)
	ctx, cancel := context.WithCancelCause(context.Background())
	w, err := ws.Start(ctx, harness.WorkerSpec{Role: protocol.RoleImplement, Dir: "/w", Spec: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	f := <-servers
	fence := errors.New("fenced")
	cancel(fence)
	done := make(chan struct{})
	go func() {
		for range w.Events() {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the worker did not stop")
	}
	if _, err := w.Wait(); !errors.Is(err, fence) {
		t.Errorf("Wait = %v, want the cancellation cause", err)
	}
	if f.params(methodTurnInterrupt) == nil {
		t.Error("the turn was not interrupted")
	}
}
