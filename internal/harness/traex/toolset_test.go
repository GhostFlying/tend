package traex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/GhostFlying/tend/internal/harness"
	"github.com/GhostFlying/tend/internal/protocol"
)

func TestWorkerUnknownOrChangedSchemaDoesNotResumeUnsettledRun(t *testing.T) {
	for _, identity := range []string{"", "old-schema"} {
		t.Run(identity, func(t *testing.T) {
			spec := harness.WorkerSpec{RunID: "original", StateDir: t.TempDir(), Dir: t.TempDir(), Thread: "original-thread", ThreadToolsetHash: identity, Role: protocol.RoleImplement}
			if identity != "" {
				raw, _ := json.Marshal(workerToolset{RunID: spec.RunID, ThreadID: spec.Thread, Hash: identity})
				if err := os.WriteFile(filepath.Join(spec.StateDir, workerToolsetFile), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			ws := NewWorkers(WorkerOptions{})
			starts := 0
			ws.start = func(context.Context, Options) (*Session, error) { starts++; return nil, errors.New("must not start") }
			if _, err := ws.Start(t.Context(), spec); !errors.Is(err, harness.ErrRetainedResumePending) || starts != 0 {
				t.Fatal("unsettled native binding was replaced or resumed", err, starts)
			}
			if _, err := os.Stat(filepath.Join(spec.StateDir, ownedProcessFile)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("schema check overwrote original execution evidence", err)
			}
		})
	}
}

func TestStoppedWorkerSchemaSwitchRebuildsCompleteCurrentBrief(t *testing.T) {
	for _, identity := range []string{"", "old-schema"} {
		t.Run(identity, func(t *testing.T) {
			ws, servers := fakeWorkers(t)
			previous := t.TempDir()
			original, _ := json.Marshal(workerToolset{RunID: "stopped-run", ThreadID: "original", Hash: identity})
			if err := os.WriteFile(filepath.Join(previous, workerToolsetFile), original, 0600); err != nil {
				t.Fatal(err)
			}
			spec := harness.WorkerSpec{RunID: "successor", Role: protocol.RoleImplement, StateDir: t.TempDir(), PredecessorStateDir: previous, Thread: "original", Spec: json.RawMessage(`{"goal":"CURRENT_GOAL","scope":"CURRENT_SCOPE","constraints":["CURRENT_CONSTRAINT"],"acceptance":["CURRENT_ACCEPTANCE"]}`), SessionContinuation: &protocol.SessionContinuation{RunID: "stopped-run", ThreadID: "original"}, Feedback: &protocol.Feedback{Issues: json.RawMessage(`["RETAINED_FEEDBACK"]`)}}
			spec.ReadContext = func(context.Context, protocol.IssueMaterialsQuery) (protocol.RunContext, error) {
				return protocol.RunContext{}, nil
			}
			worker, err := ws.Start(t.Context(), spec)
			if err != nil {
				t.Fatal(err)
			}
			f := <-servers
			if f.params(methodThreadResume) != nil || f.params(methodThreadStart) == nil {
				t.Fatal("obsolete schema was resumed")
			}
			for _, fact := range []string{"CURRENT_GOAL", "CURRENT_SCOPE", "CURRENT_CONSTRAINT", "CURRENT_ACCEPTANCE", "RETAINED_FEEDBACK"} {
				if !bytes.Contains(f.params(methodTurnStart), []byte(fact)) {
					t.Fatal("fresh session lost durable context", fact)
				}
			}
			var start ThreadStartParams
			if err = json.Unmarshal(f.params(methodThreadStart), &start); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, tool := range start.DynamicTools {
				if tool.Name == harness.ReadContextTool {
					found = true
				}
			}
			if !found {
				t.Fatal("fresh session did not receive current tool schema")
			}
			saved, err := os.ReadFile(filepath.Join(previous, workerToolsetFile))
			if err != nil || !bytes.Equal(saved, original) {
				t.Fatal("predecessor history was rewritten", err)
			}
			raw, err := os.ReadFile(filepath.Join(spec.StateDir, workerToolsetFile))
			var pin workerToolset
			if err != nil || json.Unmarshal(raw, &pin) != nil || pin.Hash != WorkerToolsetHash(spec) || pin.ThreadID != "th_w" {
				t.Fatal("current tool identity was not persisted", pin, err)
			}
			f.send(map[string]any{"method": notifyTurnCompleted, "params": map[string]any{"threadId": "th_w", "turn": map[string]string{"id": "tu_w", "status": "completed"}}})
			for range worker.Events() {
			}
			if _, err = worker.Wait(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestWorkerToolsetIdentityIncludesSchemaAndDescriptions(t *testing.T) {
	old := harness.WorkerSpec{Role: protocol.RoleAccept}
	current := old
	current.AcceptanceEvidenceFiles = true
	if WorkerToolsetHash(old) == WorkerToolsetHash(current) {
		t.Fatal("changed submit_verdict schema reused its old identity")
	}
	if err := saveWorkerToolset(harness.WorkerSpec{RunID: "original", StateDir: t.TempDir()}, "thread"); err != nil {
		t.Fatal(err)
	}
}
