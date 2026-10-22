package traex

import (
	"context"
	"encoding/json"
	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"testing"
	"time"
)

func TestAppserverWorkerClosesSessionBeforeWaitCompletes(t *testing.T) {
	ws := NewWorkers(WorkerOptions{})
	entered, allow := make(chan struct{}), make(chan struct{})
	var f *fakeServer
	ws.start = func(context.Context, Options) (*Session, error) {
		var s *Session
		f, s = newFake(t)
		f.on(methodThreadStart, func(json.RawMessage) any { return map[string]any{"thread": map[string]any{"id": "th_native"}} })
		f.on(methodTurnStart, func(json.RawMessage) any { return map[string]any{"turn": map[string]any{"id": "turn"}} })
		stop := s.stop
		s.stop = func() error { close(entered); <-allow; return stop() }
		return s, nil
	}
	w, err := ws.Start(t.Context(), harness.WorkerSpec{Role: protocol.RoleImplement, Spec: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	f.send(map[string]any{"method": notifyTurnCompleted, "params": map[string]any{"threadId": "th_native", "turn": map[string]any{"id": "turn", "status": "completed"}}})
	waited := make(chan error, 1)
	go func() { _, err := w.Wait(); waited <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("session close not entered")
	}
	select {
	case err := <-waited:
		t.Fatalf("Wait returned before session Close completed: %v", err)
	default:
	}
	close(allow)
	select {
	case err := <-waited:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Wait did not complete after session Close")
	}
}

func TestAppserverHistoricalProofRejectsExternalUnknownAndHeldThreads(t *testing.T) {
	for _, thread := range []string{"", `{"worktree":"orca","terminal":"term","pane":"pane"}`, `{"worktree":`, "native thread", "\"uuid\""} {
		if err := ReconcileCompletedSession(t.Context(), thread, false, 100); err == nil {
			t.Fatalf("unknown native format accepted: %q", thread)
		}
	}
	if err := ReconcileCompletedSession(t.Context(), "th_native", true, 100); err == nil {
		t.Fatal("held session accepted")
	}
	if err := ReconcileCompletedSession(t.Context(), "th_native", false, 0); err == nil {
		t.Fatal("unknown completion accepted")
	}
	if err := ReconcileCompletedSession(t.Context(), "th_native", false, 100); err != nil {
		t.Fatal(err)
	}
	for _, thread := range []string{"th_native", `{"worktree":"orca"}`, "invalid", "00000000-0000-7000-8000-000000000001 trailing"} {
		if IsLegacyThreadID(thread) {
			t.Fatalf("unknown legacy format classified appserver: %q", thread)
		}
	}
	if !IsLegacyThreadID("00000000-0000-7000-8000-000000000001") {
		t.Fatal("canonical native UUID not recognized")
	}
}
