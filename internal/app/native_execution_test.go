package app

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestNativeExecutionRequiresFrozenCentralRuntimeAndLiveLease(t *testing.T) {
	h, r, projection, _ := inventorySetup(t)
	projection.FormatVersion = 2
	projection.Runtimes[0].Kind = "central-traex-worker"
	projection.Runtimes[0].CLI = "traex"
	_, hash, err := protocol.CanonicalAgentInventory(projection)
	if err != nil {
		t.Fatal(err)
	}
	if op := h.exec(runner("linux-1"), inventoryRequest(h, r, projection, hash, 1, 0, "central-snapshot"), RegisterAgentInventory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	r.Capabilities = append(r.Capabilities, protocol.CapabilityAgentExecution)
	r.JournalVersion = 12
	for _, a := range projection.Agents {
		r.AgentExecutors = append(r.AgentExecutors, protocol.AgentExecutor{Available: true, Pin: protocol.RunExecutionPin{Identity: a.Identity, AgentRevision: a.Revision, Runtime: a.Binding, InventoryRevision: 1, SnapshotID: "central-snapshot", PublicSHA256: hash, Profile: protocol.ExecutionProfile{Model: a.RequestedModel}, Worker: protocol.WorkerOrca, BackendID: "orca"}})
	}
	r.Sequence++
	if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	spec := testSpec
	spec.AgentSelection = &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer"}, Acceptance: protocol.AgentRequirement{CategoryID: "reviewer"}}
	_, task := h.createTask(owner, createTaskPayload{Title: "central", Spec: spec})
	request := h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{WorkspaceProtocol: 1, Runtime: r}, nil)
	op := h.exec(runner("linux-1"), request, Claim{})
	var claimed claimResult
	if json.Unmarshal(op.Result, &claimed) != nil || claimed.Assignment.RunID == "" {
		t.Fatalf("claim: %+v %s", op, op.Result)
	}
	as := claimed.Assignment
	p := protocol.Principal{Kind: protocol.PrincipalRun, Subject: as.RunID, Role: as.Role}
	if d, err := h.app.NativeExecutionControl(t.Context(), p); err != nil || d != protocol.DesiredRun {
		t.Fatal(d, err)
	}
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	h.runAs(as, protocol.OpRunsReportTakeover, protocol.TakeoverPayload{Reason: "native input"}, ReportTakeover{})
	if d, err := h.app.NativeExecutionControl(t.Context(), p); err != nil || d != protocol.DesiredHeld {
		t.Fatal(d, err)
	}
	if _, err := h.app.NativeExecutionControl(t.Context(), runner("linux-1")); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("host gained run access", err)
	}
	h.clock.Advance(10 * time.Minute)
	if _, err := h.app.NativeExecutionControl(t.Context(), p); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("expired lease remained usable", err)
	}
	_ = task
}
func TestNativeExecutionRejectsLegacyLocalRun(t *testing.T) {
	h := newHarness(t)
	h.createTask(owner, createTaskPayload{Title: "local", Spec: testSpec})
	as, _, _ := h.claim("linux-1")
	p := protocol.Principal{Kind: protocol.PrincipalRun, Subject: as.RunID, Role: as.Role}
	if _, err := h.app.NativeExecutionControl(t.Context(), p); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal(err)
	}
}
