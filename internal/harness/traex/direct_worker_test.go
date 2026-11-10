package traex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestFault_StrictWorkerResumeDoesNotCreateReplacement(t *testing.T) {
	ws, servers := fakeWorkers(t)
	ws.opts.StrictResume = true
	_, err := ws.Start(t.Context(), harness.WorkerSpec{Role: protocol.RoleImplement, Thread: "original", OnThread: func(string) error {
		t.Error("failed resume changed the recorded thread")
		return nil
	}})
	f := <-servers
	if !errors.Is(err, harness.ErrRejected) || f.params(methodThreadResume) == nil || f.params(methodThreadStart) != nil || f.params(methodTurnStart) != nil {
		t.Fatal("failed resume started replacement work", err)
	}
}

func TestFault_WorkerStartupHandshakeHasDeadline(t *testing.T) {
	ws := NewWorkers(WorkerOptions{StartupTimeout: time.Millisecond})
	ws.start = func(ctx context.Context, _ Options) (*Session, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("startup has no deadline")
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if _, err := ws.Start(t.Context(), harness.WorkerSpec{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("unresponsive handshake did not return its deadline", err)
	}
}

func TestDirectWorkerRecordsNativeIdentityWithoutShorteningExecution(t *testing.T) {
	ws, servers := fakeWorkers(t)
	start := ws.start
	var startup context.Context
	ws.start = func(ctx context.Context, options Options) (*Session, error) {
		startup = ctx
		return start(ctx, options)
	}
	var identity protocol.NativeIdentity
	worker, err := ws.Start(t.Context(), harness.WorkerSpec{Role: protocol.RoleImplement, OnStartupEvent: func(_ context.Context, event harness.WorkerEvent) error {
		if event.Kind != protocol.EventNativeIdentity {
			t.Fatal(event)
		}
		return json.Unmarshal(event.Body, &identity)
	}})
	if err != nil {
		t.Fatal(err)
	}
	<-startup.Done()
	if identity.Provider != "appserver" || identity.SessionID != "th_w" || identity.TurnID != "tu_w" || identity.State != "known" {
		t.Fatal(identity)
	}
	f := <-servers
	f.send(map[string]any{"method": notifyTurnCompleted, "params": map[string]any{"threadId": "th_w", "turn": map[string]string{"id": "tu_w", "status": "completed"}}})
	for range worker.Events() {
	}
	if _, err := worker.Wait(); err != nil {
		t.Fatal("startup deadline ended a healthy running worker", err)
	}
}

func TestStoppedTaskSessionMissingStartsFreshWithRebuiltContext(t *testing.T) {
	ws, servers := fakeWorkers(t)
	ws.opts.StrictResume = true
	w, err := ws.Start(t.Context(), harness.WorkerSpec{RunID: "new", Role: protocol.RoleImplement, Thread: "original", Spec: json.RawMessage(`{"goal":"original issue","scope":"CURRENT_RECONSTRUCTED_CONTEXT"}`), SessionContinuation: &protocol.SessionContinuation{RunID: "stopped", ThreadID: "original"}})
	if err != nil {
		t.Fatal(err)
	}
	f := <-servers
	if f.params(methodThreadResume) == nil || f.params(methodThreadStart) == nil || f.params(methodTurnStart) == nil {
		t.Fatal("missing optional history did not start fresh")
	}
	if !bytes.Contains(f.params(methodTurnStart), []byte("CURRENT_RECONSTRUCTED_CONTEXT")) {
		t.Fatal("fresh thread lacks rebuilt context")
	}
	f.send(map[string]any{"method": notifyTurnCompleted, "params": map[string]any{"threadId": "th_w", "turn": map[string]string{"id": "tu_w", "status": "completed"}}})
	for range w.Events() {
	}
	if _, err := w.Wait(); err != nil {
		t.Fatal(err)
	}
}
