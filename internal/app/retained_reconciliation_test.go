package app

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func retainedHelloFixture(t *testing.T) (*harness, protocol.Assignment) {
	t.Helper()
	h, as, plan, identity, _ := receiptRecoveryFixture(t)
	if _, err := h.app.RegisterCommittedMaintenance(t.Context(), plan, identity); err != nil {
		t.Fatal(err)
	}
	proof := protocol.CommittedMaintenanceReceipt{MaintenanceID: plan.MaintenanceID, RequestID: plan.RequestID, Previous: plan.Previous, Successor: plan.Successor, Process: identity, Generation: 9, Epoch: identity.LaunchID, FirstSequence: 1, SecondSequence: 2}
	if _, err := h.app.LinkCommittedMaintenance(t.Context(), proof); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.ChangeArchivePreparation(t.Context(), protocol.ArchiveControlRequest{RequestID: h.ids.New(protocol.PrefixRequest), ExpectedControlRevision: plan.PreparationRevision}, false); err != nil {
		t.Fatal(err)
	}
	return h, as
}

func retainedHelloRuntime(t *testing.T, h *harness, capability bool) *protocol.RunnerRuntime {
	t.Helper()
	var rt protocol.RunnerRuntime
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		old, err := store.GetHostRuntime(t.Context(), tx.Q(), "linux-1")
		rt = old.Runtime
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rt.InstanceID = "next-instance"
	rt.Generation++
	rt.Sequence = 1
	rt.Capabilities = append([]string(nil), rt.Capabilities...)
	if capability {
		rt.Capabilities = append(rt.Capabilities, protocol.CapabilityRetainedReconciliation)
	}
	return &rt
}

func retainedHello(t *testing.T, h *harness, runtime *protocol.RunnerRuntime, held ...protocol.HeldRun) (protocol.Operation, error) {
	t.Helper()
	return h.app.Execute(t.Context(), runner("linux-1"), h.env(protocol.OpRunnerHello, protocol.Target{Type: "host", ID: "linux-1"}, protocol.HelloPayload{Runtime: runtime, WorkspaceProtocol: 1, Worker: h.worker("linux-1"), Held: held}, nil), Hello{})
}

func TestRetainedReconciliationNegotiatesWithoutChangingOriginalRun(t *testing.T) {
	for _, wire := range []int64{protocol.PreviousRunnerProtocol, protocol.CurrentRunnerProtocol} {
		t.Run(map[int64]string{protocol.PreviousRunnerProtocol: "previous", protocol.CurrentRunnerProtocol: "current"}[wire], func(t *testing.T) {
			h, original := retainedHelloFixture(t)
			beforeRun, beforeLease := h.run(original.RunID), h.lease(original.RunID)
			var beforePin *protocol.RunExecutionPin
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				var err error
				beforePin, err = store.GetRunExecution(t.Context(), tx.Q(), original.RunID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			rt := retainedHelloRuntime(t, h, true)
			rt.Protocol = wire
			rt.ProtocolMin, rt.ProtocolMax = wire, wire
			op, err := retainedHello(t, h, rt, protocol.HeldRun{RunID: original.RunID, LeaseID: original.LeaseID, Observed: protocol.RunRunning})
			if err != nil || op.Status != protocol.OpSucceeded {
				t.Fatal(op, err)
			}
			var response protocol.HelloResult
			if err := json.Unmarshal(op.Result, &response); err != nil {
				t.Fatal(err)
			}
			if len(response.Runs) != 1 || response.Runs[0].Verdict != protocol.VerdictRetain || response.Runs[0].RunID != original.RunID || len(response.Reassigned) != 0 || response.Control == nil {
				t.Fatal(response)
			}
			if !reflect.DeepEqual(beforeRun, h.run(original.RunID)) || !reflect.DeepEqual(beforeLease, h.lease(original.RunID)) {
				t.Fatal("retain renewed a lease or changed original business facts")
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				pin, err := store.GetRunExecution(t.Context(), tx.Q(), original.RunID)
				if err != nil {
					return err
				}
				if !reflect.DeepEqual(pin, beforePin) {
					t.Fatal("retain changed the original pin")
				}
				actual, err := store.GetHostRuntime(t.Context(), tx.Q(), "linux-1")
				if err == nil && (actual.Runtime.InstanceID != rt.InstanceID || actual.Runtime.Generation != rt.Generation) {
					t.Fatal("fresh runtime was not registered", actual)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRetainedReconciliationLegacyRemainsFailClosed(t *testing.T) {
	h, original := retainedHelloFixture(t)
	beforeRun, beforeLease := h.run(original.RunID), h.lease(original.RunID)
	for _, mode := range []string{"without-capability", "unsupported-protocol"} {
		rt := retainedHelloRuntime(t, h, mode == "unsupported-protocol")
		if mode == "unsupported-protocol" {
			rt.Protocol = protocol.PreviousRunnerProtocol - 1
		}
		op, err := retainedHello(t, h, rt, protocol.HeldRun{RunID: original.RunID, LeaseID: original.LeaseID, Observed: protocol.RunRunning})
		want := protocol.CodeUnknownOutcome
		if mode == "unsupported-protocol" {
			want = protocol.CodeInvalidRequest
		}
		if protocol.CodeOf(err) != want || len(op.Result) != 0 {
			t.Fatal("legacy peer received an unrecognized verdict", mode, op, err)
		}
	}
	if !reflect.DeepEqual(beforeRun, h.run(original.RunID)) || !reflect.DeepEqual(beforeLease, h.lease(original.RunID)) {
		t.Fatal("legacy rejection changed original run")
	}
}

func TestRetainedReconciliationMissingJournalDoesNotReassign(t *testing.T) {
	h, original := retainedHelloFixture(t)
	beforeRun, beforeLease := h.run(original.RunID), h.lease(original.RunID)
	op, err := retainedHello(t, h, retainedHelloRuntime(t, h, true))
	if err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal(op, err)
	}
	var response protocol.HelloResult
	if err := json.Unmarshal(op.Result, &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Reassigned) != 0 || !reflect.DeepEqual(beforeRun, h.run(original.RunID)) || !reflect.DeepEqual(beforeLease, h.lease(original.RunID)) {
		t.Fatal("missing original journal was treated as permission to replay", response)
	}
}

func TestRetainedReconciliationDoesNotBlockUnrelatedRun(t *testing.T) {
	h, original := retainedHelloFixture(t)
	h.createTask(owner, createTaskPayload{Title: "unrelated", Spec: testSpec})
	unrelated, ok, _ := h.claim("linux-1")
	if !ok {
		t.Fatal("unrelated run not allocated")
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		return tx.UpdateRunObserved(t.Context(), unrelated.RunID, protocol.RunUnknown, h.clock.Now().UnixMilli())
	}); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(time.Second)
	op, err := retainedHello(t, h, retainedHelloRuntime(t, h, true), protocol.HeldRun{RunID: original.RunID, LeaseID: original.LeaseID, Observed: protocol.RunRunning}, protocol.HeldRun{RunID: unrelated.RunID, LeaseID: unrelated.LeaseID, Observed: protocol.RunRunning})
	if err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal(op, err)
	}
	var response protocol.HelloResult
	if err := json.Unmarshal(op.Result, &response); err != nil {
		t.Fatal(err)
	}
	verdicts := map[string]string{}
	for _, v := range response.Runs {
		verdicts[v.RunID] = v.Verdict
	}
	if verdicts[original.RunID] != protocol.VerdictRetain || verdicts[unrelated.RunID] != protocol.VerdictContinue || h.run(original.RunID).ObservedState != protocol.RunUnknown || h.run(unrelated.RunID).ObservedState != protocol.RunRunning {
		t.Fatal("original uncertainty blocked unrelated reconciliation", response)
	}
	h.createTask(owner, createTaskPayload{Title: "must wait for occupied capacity", Spec: testSpec})
	if extra, ok, _ := h.claim("linux-1"); ok {
		t.Fatal("retained run stopped occupying its original host slot", extra)
	}
}

func TestRetainedReconciliationRejectsOldLeaseAndHonorsExplicitStop(t *testing.T) {
	for _, mode := range []string{"old-lease", "owner-stop"} {
		t.Run(mode, func(t *testing.T) {
			h, original := retainedHelloFixture(t)
			held := protocol.HeldRun{RunID: original.RunID, LeaseID: original.LeaseID, Observed: protocol.RunRunning}
			if mode == "old-lease" {
				held.LeaseID = "wrong-lease"
			} else if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE runs SET desired_state='stopped' WHERE run_id=?`, original.RunID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			op, err := retainedHello(t, h, retainedHelloRuntime(t, h, true), held)
			if err != nil || op.Status != protocol.OpSucceeded {
				t.Fatal(op, err)
			}
			var response protocol.HelloResult
			if err := json.Unmarshal(op.Result, &response); err != nil {
				t.Fatal(err)
			}
			if len(response.Runs) != 1 || response.Runs[0].Verdict != protocol.VerdictStop {
				t.Fatal("retention hid a stale lease or explicit business stop", response)
			}
		})
	}
}

func TestRetainedReconciliationHeartbeatDoesNotRenewOriginalLease(t *testing.T) {
	for _, wire := range []int64{protocol.CurrentRunnerProtocol, protocol.PreviousRunnerProtocol} {
		t.Run(string(rune('0'+wire)), func(t *testing.T) {
			h, as := retainedHelloFixture(t)
			runtime := retainedHelloRuntime(t, h, true)
			runtime.Protocol, runtime.ProtocolMin, runtime.ProtocolMax = wire, wire, wire
			if _, err := retainedHello(t, h, runtime, protocol.HeldRun{RunID: as.RunID, LeaseID: as.LeaseID, Observed: protocol.RunRunning}); err != nil {
				t.Fatal(err)
			}
			before := h.lease(as.RunID)
			beforeRun := h.run(as.RunID)
			h.clock.Advance(time.Second)
			runtime.Sequence++
			result, err := h.app.RuntimeHeartbeat(t.Context(), runner("linux-1"), protocol.HeartbeatPayload{Runtime: runtime, Runs: []protocol.LeaseRef{{RunID: as.RunID, LeaseID: as.LeaseID}}})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Runs) != 1 || result.Runs[0].Renewed || result.Runs[0].Desired != protocol.DesiredRun {
				t.Fatal("protected heartbeat changed control or renewed", result)
			}
			if after := h.lease(as.RunID); !reflect.DeepEqual(before, after) {
				t.Fatal("original lease extended", before, after)
			}
			if after := h.run(as.RunID); !reflect.DeepEqual(beforeRun, after) {
				t.Fatal("unknown run relabelled by heartbeat", beforeRun, after)
			}
		})
	}
}
