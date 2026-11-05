package app

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestRetainedExecutionOriginalPinSurvivesCurrentInventoryChange(t *testing.T) {
	h, as, rt := ordinaryRetainedRunFixture(t)
	ctx := t.Context()
	p := continuationPayload(t, h, as.RunID)
	pin := p.Execution
	rt.Capabilities = append(rt.Capabilities, protocol.CapabilityAgentContinuation)
	rt.ContinuationBackend = pin.BackendID
	current := pin
	current.AgentRevision++
	current.InventoryRevision++
	current.SnapshotID = "replacement-snapshot"
	current.Runtime.Revision++
	rt.AgentExecutors = []protocol.AgentExecutor{{Pin: current, Available: true}}
	rt.AgentContinuations = []protocol.AgentExecutor{{Pin: pin, Available: true}}
	rt.Sequence++
	first, err := retainedHello(t, h, rt, protocol.HeldRun{RunID: as.RunID, LeaseID: as.LeaseID, Observed: protocol.RunInterrupted})
	if err != nil {
		t.Fatal(err)
	}
	var hello protocol.HelloResult
	if err = json.Unmarshal(first.Result, &hello); err != nil || len(hello.Runs) != 1 || hello.Runs[0].Verdict != protocol.VerdictRetain {
		t.Fatal("inventory change implicitly resumed original", hello, err)
	}
	status, err := h.app.RetainedRunReconciliation(ctx, as.RunID)
	if err != nil || !status.CanContinue || status.Identity.Execution != pin {
		t.Fatal("exact original continuation pin was rejected after current inventory changed", status.Blockers, err)
	}
	before := h.task(as.TaskID).ControlRevision
	env := h.env(protocol.OpContinueRetainedRun, runTarget(as.RunID), p, rev(before))
	op := h.exec(owner, env, ContinueRetainedRun{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal("original-pin authorization", op)
	}
	rt.Sequence++
	activated, err := retainedHello(t, h, rt, protocol.HeldRun{RunID: as.RunID, LeaseID: as.LeaseID, Observed: protocol.RunInterrupted})
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(activated.Result, &hello); err != nil || len(hello.Runs) != 1 || hello.Runs[0].Verdict != protocol.VerdictContinueRetained {
		t.Fatal("same-journal Hello failed to resume original pin", hello, err)
	}
	rt.Sequence++
	hb, err := h.app.RuntimeHeartbeat(ctx, runner("linux-1"), protocol.HeartbeatPayload{Runtime: rt, Runs: []protocol.LeaseRef{{RunID: as.RunID, LeaseID: as.LeaseID}}})
	if err != nil || len(hb.Runs) != 1 || !hb.Runs[0].Renewed {
		t.Fatal("inventory change broke activated original lease renewal", hb, err)
	}
	if err = h.store.Read(ctx, func(tx *store.ReadTx) error {
		saved, e := store.GetRunExecution(ctx, tx.Q(), as.RunID)
		if e == nil && !reflect.DeepEqual(saved, &pin) {
			t.Fatal("original execution was replaced", saved)
		}
		return e
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRetainedExecutionContinuationAvailabilityRequiresExactNegotiatedPin(t *testing.T) {
	h, as, _ := ordinaryRetainedRunFixture(t)
	ctx := t.Context()
	pin := continuationPayload(t, h, as.RunID).Execution
	for _, kind := range []string{"unavailable", "wrong-pin", "missing-capability", "wrong-backend"} {
		t.Run(kind, func(t *testing.T) {
			if err := h.store.Write(ctx, func(tx *store.Tx) error {
				r, e := store.GetHostRuntime(ctx, tx.Q(), "linux-1")
				if e != nil {
					return e
				}
				r.Runtime.Capabilities = append(r.Runtime.Capabilities, protocol.CapabilityAgentContinuation)
				r.Runtime.AgentExecutors = nil
				r.Runtime.ContinuationBackend = pin.BackendID
				r.Runtime.AgentContinuations = []protocol.AgentExecutor{{Pin: pin, Available: true}}
				switch kind {
				case "unavailable":
					r.Runtime.AgentContinuations[0].Available = false
				case "wrong-pin":
					r.Runtime.AgentContinuations[0].Pin.InventoryRevision++
				case "missing-capability":
					r.Runtime.Capabilities = slices.DeleteFunc(r.Runtime.Capabilities, func(s string) bool { return s == protocol.CapabilityAgentContinuation })
				case "wrong-backend":
					r.Runtime.ContinuationBackend = "git"
				}
				return tx.PutHostRuntime(ctx, "linux-1", r)
			}); err != nil {
				t.Fatal(err)
			}
			state, err := h.app.RetainedRunReconciliation(ctx, as.RunID)
			if err != nil || state.CanContinue || !slices.Contains(state.Blockers, "original_executor_unavailable") {
				t.Fatal("unavailable or unnegotiated continuation authorized original run", kind, state, err)
			}
		})
	}
}
