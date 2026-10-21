package traex

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

// fakeWorkers returns Workers that talk to a fake app-server; setup adds
// to what the server answers.
func fakeWorkers(t *testing.T, setup ...func(*fakeServer)) (*Workers, chan *fakeServer) {
	servers := make(chan *fakeServer, 1)
	w := NewWorkers(WorkerOptions{})
	w.start = func(ctx context.Context, opts Options) (*Session, error) {
		// A worker never acts as the owner's agent (D45).
		if !slices.Contains(opts.Env, protocol.AddonEnv+"=off") {
			t.Errorf("worker environment %v does not turn the agent add-on off", opts.Env)
		}
		f, s := newFake(t)
		f.on(methodThreadStart, func(json.RawMessage) any { return map[string]any{"thread": map[string]any{"id": "th_w"}} })
		f.on(methodTurnStart, func(json.RawMessage) any { return map[string]any{"turn": map[string]any{"id": "tu_w"}} })
		f.on(methodTurnInterrupt, func(json.RawMessage) any { return map[string]any{} })
		for _, fn := range setup {
			fn(f)
		}
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

// turnText returns the text of the turn a fake server was asked to start.
func turnText(t *testing.T, f *fakeServer) string {
	t.Helper()
	var p TurnStartParams
	if err := json.Unmarshal(f.params(methodTurnStart), &p); err != nil || len(p.Input) != 1 {
		t.Fatalf("turn/start = %s (%v)", f.params(methodTurnStart), err)
	}
	return p.Input[0].Text
}

// recordThread returns an OnThread that keeps the thread, checking that
// no turn has started in it yet, and the server it should check.
func recordThread(t *testing.T, servers chan *fakeServer) (func(string) error, *string, chan *fakeServer) {
	var thread string
	seen := make(chan *fakeServer, 1)
	return func(id string) error {
		f := <-servers
		if f.params(methodTurnStart) != nil {
			t.Error("the thread was reported after its turn started")
		}
		thread = id
		seen <- f
		return nil
	}, &thread, seen
}

func TestWorkerContinuesItsThread(t *testing.T) {
	ws, servers := fakeWorkers(t, func(f *fakeServer) {
		f.on(methodThreadResume, func(json.RawMessage) any { return map[string]any{"thread": map[string]any{"id": "th_old"}} })
	})
	onThread, thread, seen := recordThread(t, servers)
	spec := harness.WorkerSpec{Role: protocol.RoleImplement, Dir: "/w", Spec: json.RawMessage(`{"goal":"g"}`), Attempt: 2, Thread: "th_old", OnThread: onThread}
	if _, err := ws.Start(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	f := <-seen
	var resume ThreadResumeParams
	if err := json.Unmarshal(f.params(methodThreadResume), &resume); err != nil || resume.ThreadID != "th_old" || f.params(methodThreadStart) != nil {
		t.Errorf("thread/resume = %s, thread/start = %s", f.params(methodThreadResume), f.params(methodThreadStart))
	}
	if got := turnText(t, f); got != harness.Continuation(spec) || *thread != "th_old" {
		t.Errorf("continued with %q in %s", got, *thread)
	}
}

func TestFault_ThreadResumeFailsFallsBackToBrief(t *testing.T) {
	// The fake app-server knows no thread/resume, as when the rollout is
	// gone or traex changed.
	ws, servers := fakeWorkers(t)
	onThread, thread, seen := recordThread(t, servers)
	spec := harness.WorkerSpec{Role: protocol.RoleImplement, Dir: "/w", Spec: json.RawMessage(`{"goal":"g"}`), Attempt: 2, Thread: "th_old", OnThread: onThread}
	if _, err := ws.Start(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	f := <-seen
	_, brief := harness.Brief(spec)
	if got := turnText(t, f); got != brief || f.params(methodThreadStart) == nil || *thread != "th_w" {
		t.Errorf("fell back to %q in %s", got, *thread)
	}
}

func TestWorkerDoesNotStartAnUnrecordedThread(t *testing.T) {
	ws, servers := fakeWorkers(t)
	failure := errors.New("journal is full")
	_, err := ws.Start(context.Background(), harness.WorkerSpec{Role: protocol.RoleImplement, Dir: "/w", Spec: json.RawMessage(`{}`),
		OnThread: func(string) error { return failure }})
	if f := <-servers; !errors.Is(err, failure) || f.params(methodTurnStart) != nil {
		t.Errorf("Start = %v, turn/start = %s", err, f.params(methodTurnStart))
	}
}
