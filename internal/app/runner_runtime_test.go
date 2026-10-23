package app

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func testRuntime(instance string) *protocol.RunnerRuntime {
	return &protocol.RunnerRuntime{InstanceID: instance, Sequence: 1, Protocol: 1, ProtocolMin: 0, ProtocolMax: 1, Mode: protocol.RunnerNormal, Capabilities: []string{protocol.CapabilityRunnerHeartbeat, protocol.CapabilityRunnerMaintenance}}
}
func (h *harness) versionedHello(host string, r *protocol.RunnerRuntime) protocol.Operation {
	// A fake process allocates its durable generation when first registered.
	if r != nil && r.Generation == 0 {
		r.Generation = 1
		if err := h.store.Read(context.Background(), func(tx *store.ReadTx) error {
			if old, err := store.GetHostRuntime(context.Background(), tx.Q(), host); err == nil {
				if old.Runtime.InstanceID == r.InstanceID {
					r.Generation = old.Runtime.Generation
				} else {
					r.Generation = old.Runtime.Generation + 1
				}
			}
			return nil
		}); err != nil {
			h.t.Fatal(err)
		}
	}
	op, err := h.app.Execute(context.Background(), runner(host), h.env(protocol.OpRunnerHello, protocol.Target{Type: "host", ID: host}, protocol.HelloPayload{Worker: h.worker(host), WorkspaceProtocol: 1, Runtime: r}, nil), Hello{})
	if err != nil {
		return protocol.Operation{Status: protocol.OpFailed, Error: &protocol.Error{Code: protocol.CodeOf(err), Message: err.Error()}}
	}
	return op
}
func TestRunnerPresenceOrdersInstancesAndExpires(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	r := testRuntime("first")
	if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	h.clock.Advance(61 * time.Second)
	if n, err := h.app.SweepRunnerPresence(ctx); err != nil || n != 1 {
		t.Fatalf("sweep %d %v", n, err)
	}
	if _, err := h.app.RuntimeHeartbeat(ctx, runner("linux-1"), protocol.HeartbeatPayload{Runtime: r}); protocol.CodeOf(err) != protocol.CodeConflictPayload {
		t.Fatalf("stale: %v", err)
	}
	r.Sequence++
	if _, err := h.app.RuntimeHeartbeat(ctx, runner("linux-1"), protocol.HeartbeatPayload{Runtime: r}); err != nil {
		t.Fatal(err)
	}
	next := testRuntime("second")
	if op := h.versionedHello("linux-1", next); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	r.Sequence++
	if op := h.versionedHello("linux-1", r); op.Status != protocol.OpFailed {
		t.Fatalf("resurrected: %+v", op)
	}
	if _, err := h.app.Heartbeat(ctx, runner("linux-1"), nil); err == nil {
		t.Fatal("legacy heartbeat replaced versioned identity")
	}
	if err := h.store.Read(ctx, func(tx *store.ReadTx) error {
		snap, err := views.Snapshot(ctx, tx.Q(), nil, "")
		if err != nil {
			return err
		}
		if snap.Hosts[0].Online == nil || !*snap.Hosts[0].Online || snap.Hosts[0].ProtocolVersion == nil {
			t.Fatalf("view: %+v", snap.Hosts)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestMaintenanceBlocksNewClaimsButAllowsReceiptReplay(t *testing.T) {
	h := newHarness(t)
	r := testRuntime("first")
	h.versionedHello("linux-1", r)
	h.createTask(owner, createTaskPayload{Title: "existing", Spec: testSpec})
	env := h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{Worker: h.worker("linux-1"), WorkspaceProtocol: 1, Runtime: r}, nil)
	first, ok := h.claimWith("linux-1", env)
	if !ok || first.Protocol != 1 {
		t.Fatalf("assignment: %+v %v", first, ok)
	}
	op := h.exec(owner, h.env("hosts.maintenance", protocol.Target{Type: "host", ID: "linux-1"}, map[string]any{"mode": protocol.RunnerMaintenance}, rev(0)), HostMaintenance{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	h.createTask(owner, createTaskPayload{Title: "queued", Spec: testSpec})
	replay, ok := h.claimWith("linux-1", env)
	if !ok || replay.RunID != first.RunID {
		t.Fatal("receipt lost during drain")
	}
	r.Sequence++
	heartbeat, err := h.app.RuntimeHeartbeat(context.Background(), runner("linux-1"), protocol.HeartbeatPayload{Runtime: r, Runs: []protocol.LeaseRef{{RunID: first.RunID, LeaseID: first.LeaseID}}})
	if err != nil || heartbeat.Control == nil || heartbeat.Control.Mode != protocol.RunnerMaintenance || !heartbeat.Runs[0].Renewed {
		t.Fatalf("heartbeat: %+v %v", heartbeat, err)
	}
	blocked := h.exec(runner("linux-1"), h.env(protocol.OpRunnerClaim, env.Target, protocol.ClaimPayload{Worker: h.worker("linux-1"), Runtime: r, WorkspaceProtocol: 1}, nil), Claim{})
	var result struct {
		Assignment *protocol.Assignment `json:"assignment"`
	}
	_ = json.Unmarshal(blocked.Result, &result)
	if blocked.Status != protocol.OpSucceeded || result.Assignment != nil {
		t.Fatalf("claimed during maintenance %+v", blocked)
	}
}

func TestUnsupportedRuntimeRejectedBeforeAllocation(t *testing.T) {
	h := newHarness(t)
	r := testRuntime("future")
	r.Protocol = 2
	r.ProtocolMax = 2
	if op := h.versionedHello("linux-1", r); op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeInvalidRequest {
		t.Fatalf("future: %+v", op)
	}
	if h.count("runs") != 0 {
		t.Fatal("allocated unsupported run")
	}
}
