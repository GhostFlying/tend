package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func TestObservationCapabilityQueueAndClaims(t *testing.T) {
	h := newDeliverableHarness(t)
	spec := testSpec
	spec.Observation = &protocol.ObservationScope{Deployment: true}
	_, task := h.createTask(owner, createTaskPayload{Title: "observe", Spec: spec})
	checkQueue := func(wantUnsupported bool) {
		t.Helper()
		err := h.store.Read(context.Background(), func(tx *store.ReadTx) error {
			detail, err := views.Task(context.Background(), tx.Q(), task.TaskID)
			if err != nil {
				return err
			}
			if detail.Execution == nil || detail.Execution.QueueReason == nil {
				t.Fatalf("missing execution queue projection: %+v", detail.Execution)
			}
			if strings.Contains(detail.Execution.QueueReason.Message, "scoped worker observation") != wantUnsupported {
				t.Fatalf("execution queue reason: %+v", detail.Execution.QueueReason)
			}
			entries, err := Queue(context.Background(), tx.Q(), h.app.QueueOrder())
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if entry.TaskID == task.TaskID {
					if strings.Contains(entry.Unplaceable, "scoped worker observation") != wantUnsupported {
						t.Fatalf("queue reason %q", entry.Unplaceable)
					}
					return nil
				}
			}
			t.Fatal("observation task missing from queue")
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	checkQueue(true)
	runtime := testRuntime("capable")
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityWorkerObservation)
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	checkQueue(false)
	// Gate on this claim, not a stale stored observation from an older executable.
	runtime.Capabilities = nil
	if _, ok := h.claimInputRun(runtime); ok {
		t.Fatal("claim without capability used stored capability")
	}
	_, plain := h.createTask(owner, createTaskPayload{Title: "plain", Spec: testSpec})
	as, ok := h.claimInputRun(runtime)
	if !ok || as.TaskID != plain.TaskID {
		t.Fatal("capability gating broke old no-observation tasks")
	}
}

func TestObservationRecoveryRequiresOriginalCapability(t *testing.T) {
	h := newDeliverableHarness(t)
	spec := testSpec
	spec.Observation = &protocol.ObservationScope{Deployment: true}
	h.createTask(owner, createTaskPayload{Title: "observe", Spec: spec})
	runtime := testRuntime("capable")
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityWorkerObservation)
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	as, ok := h.claimInputRun(runtime)
	if !ok {
		t.Fatal("no observation run")
	}
	for _, held := range [][]protocol.HeldRun{nil, {{RunID: as.RunID, LeaseID: as.LeaseID, Observed: protocol.RunRunning}}} {
		old := testRuntime("older-binary")
		old.Generation = runtime.Generation + 1
		env := h.env(protocol.OpRunnerHello, protocol.Target{Type: "host", ID: "linux-1"}, protocol.HelloPayload{WorkspaceProtocol: 1, Runtime: old, Held: held}, nil)
		_, err := h.app.Execute(context.Background(), runner("linux-1"), env, Hello{})
		if protocol.CodeOf(err) != protocol.CodeInvalidRequest {
			t.Fatalf("unsupported recovery: %v", err)
		}
	}
	// A capable restart can recover the same unacknowledged assignment.
	resumed := testRuntime("capable-restart")
	resumed.Capabilities = append(resumed.Capabilities, protocol.CapabilityWorkerObservation)
	op := h.versionedHello("linux-1", resumed)
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var result protocol.HelloResult
	if err := json.Unmarshal(op.Result, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Reassigned) != 1 || result.Reassigned[0].RunID != as.RunID {
		t.Fatalf("observation reassignment lost: %+v", result)
	}
}

func TestObservationScopeUpdateWaitsForCapableRunner(t *testing.T) {
	h := newDeliverableHarness(t)
	_, task := h.createTask(owner, createTaskPayload{Title: "inspect", Spec: testSpec})
	spec := testSpec
	spec.Observation = &protocol.ObservationScope{Deployment: true}
	op := h.exec(owner, h.env("tasks.update_intent", taskTarget(task.TaskID), updateIntentPayload{Spec: &spec}, rev(task.ControlRevision)), UpdateIntent{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if _, ok := h.claimInputRun(nil); ok {
		t.Fatal("legacy runner claimed updated observation scope")
	}
	runtime := testRuntime("updated-capable")
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityWorkerObservation)
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	as, ok := h.claimInputRun(runtime)
	if !ok || as.TaskID != task.TaskID || as.SpecVersion != task.SpecVersion+1 {
		t.Fatalf("updated scoped assignment: %+v, %v", as, ok)
	}
}
