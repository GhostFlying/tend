package traex

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestDelegatedCommitOnlyCompletion(t *testing.T) {
	started := make(chan string, 4)
	var count atomic.Int32
	ws, servers := fakeWorkers(t, func(f *fakeServer) {
		f.on(methodTurnStart, func(json.RawMessage) any {
			id := fmt.Sprintf("turn-%d", count.Add(1))
			started <- id
			return map[string]any{"turn": map[string]string{"id": id}}
		})
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	w, err := ws.Start(ctx, harness.WorkerSpec{Delegated: true, Role: protocol.RoleImplement, Dir: "/w", Spec: json.RawMessage(`{"deliverables":["commit"]}`)})
	if err != nil {
		t.Fatal(err)
	}
	f := <-servers
	first := <-started
	done := make(chan struct{})
	go func() {
		for range w.Events() {
		}
		close(done)
	}()
	defer func() { cancel(); <-done; w.Wait() }()
	f.send(map[string]any{"method": notifyTurnCompleted, "params": map[string]any{"threadId": "th_w", "turn": map[string]any{"id": first, "status": "completed"}}})
	select {
	case next := <-started:
		t.Fatalf("commit-only worker launched unexpected %s after completed %s", next, first)
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("commit-only completion did not return")
	}
}
