package coordination

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/runner/journal"
)

type failedContinuedThread struct{ *receiptNativeThread }

func (t failedContinuedThread) FindTurn(_ context.Context, _ string, id string) (harness.TurnReceipt, bool, error) {
	return harness.TurnReceipt{ID: id, Status: "failed"}, true, nil
}

func TestLocalQueuedContinuationDoesNotHideFailedPredecessor(t *testing.T) {
	j, err := journal.Open(t.Context(), filepath.Join(t.TempDir(), "journal.db"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if err = j.AcceptAssignment(t.Context(), protocol.Assignment{RunID: "run", LeaseID: "lease", RunToken: "token", Protocol: 7}, ""); err != nil {
		t.Fatal(err)
	}
	local := journal.LocalTurns{Journal: j, RunID: "run"}
	th := failedContinuedThread{&receiptNativeThread{events: make(chan harness.Event)}}
	if err = local.Queue(t.Context(), harness.ToolCall{ThreadID: th.ID(), TurnID: "root", CallID: "continue"}, "next step"); err != nil {
		t.Fatal(err)
	}
	d := Driver{LocalTurns: local, threads: map[string]harness.Thread{th.ID(): th}, Emit: func(context.Context, Event) error { return nil }}
	if err = d.forwardNative(t.Context(), th, harness.Event{Kind: harness.EventTurnCompleted, TurnID: "root", TurnStatus: "failed"}, true); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(Request{ThreadID: th.ID(), TurnID: "root"})
	out, err := d.Handle(t.Context(), "find", raw)
	if err != nil {
		t.Fatal(err)
	}
	got := out.(FindResult)
	if !got.Found || got.Receipt.Status != "failed" {
		t.Fatalf("known failed predecessor hidden by a continuation that never started: %+v", got)
	}
}
