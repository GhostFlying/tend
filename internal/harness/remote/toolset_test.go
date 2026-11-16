package remote

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/harness/traex"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestRemoteToolsetSwitchRequiresOriginalStoppedAndSettledEdge(t *testing.T) {
	spec := harness.WorkerSpec{RunID: "next", Dir: "workspace", ProviderID: "provider", StateDir: "next-state", SessionContinuation: &protocol.SessionContinuation{RunID: "original", ThreadID: "thread"}}
	for _, hash := range []string{"", "old", traex.WorkerToolsetHash(spec)} {
		state := workerState{Kind: "central-traex", RunID: "original", Cwd: spec.Dir, ProviderID: spec.ProviderID, StateDir: "original-state", Thread: "thread", ToolsetHash: hash, ExecutorStopped: true, BriefRecorded: true}
		raw, _ := json.Marshal(state)
		spec.Thread = string(raw)
		rebound, err := rebindQuestionThread(spec)
		if err != nil {
			t.Fatal(err)
		}
		if (hash == traex.WorkerToolsetHash(spec)) != (rebound != "") {
			t.Fatal("schema mismatch did not select fresh brief", hash, rebound)
		}
		for _, pending := range []string{"running", "unknown", "pending-brief"} {
			invalid := state
			switch pending {
			case "running":
				invalid.ExecutorStopped = false
			case "unknown":
				invalid.Reconcile = true
			case "pending-brief":
				invalid.BriefPending = true
			}
			raw, _ = json.Marshal(invalid)
			spec.Thread = string(raw)
			if _, err = rebindQuestionThread(spec); err == nil {
				t.Fatal("unsettled predecessor became fresh work", pending)
			}
		}
	}
}

func TestRemoteUnknownCurrentToolsetDoesNotOpenExecutor(t *testing.T) {
	dir := t.TempDir()
	pin, _ := json.Marshal(workerState{Kind: "central-traex", RunID: "original", Cwd: dir, StateDir: dir, ProviderID: "provider", Thread: "thread", ExecutorStopped: true, BriefRecorded: true})
	ws := NewWorkers(WorkerOptions{Bin: "must-not-execute"})
	defer ws.Close()
	_, err := ws.Start(t.Context(), harness.WorkerSpec{RunID: "original", Dir: dir, StateDir: dir, ProviderID: "provider", Thread: string(pin), Lifetime: t.Context(), NativeDial: func(context.Context, string) (io.ReadWriteCloser, error) {
		t.Fatal("unknown toolset opened native transport")
		return nil, nil
	}})
	if !errors.Is(err, harness.ErrRetainedResumePending) {
		t.Fatal("unknown current session was not retained", err)
	}
}
