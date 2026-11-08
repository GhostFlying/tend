package app

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func TestFutureReviewerDoesNotBlockCurrentClaimOrCreateWaitNotice(t *testing.T) {
	h, runtime, _ := capabilitySetup(t)
	spec := testSpec
	spec.AgentSelection = &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer"}, Acceptance: protocol.AgentRequirement{CategoryID: "reviewer", RequiredCapabilities: []protocol.CapabilityReference{{CapabilityID: "image_gen", DefinitionRevision: 1}}}}
	_, task := h.createTask(owner, createTaskPayload{Title: "Implement before the future reviewer becomes available", Spec: spec})
	h.clock.Advance(11 * time.Minute)
	runtime.Sequence++
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if err := h.app.SweepCapabilityWaits(t.Context()); err != nil {
		t.Fatal(err)
	}
	if h.count("capability_wait_notices") != 0 {
		t.Fatal("future reviewer created a current execution blocker")
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		implementation, err := views.TaskCapabilities(t.Context(), tx.Q(), task.TaskID, 1, protocol.RoleImplement, h.clock.Now().UnixMilli())
		if err != nil {
			return err
		}
		if implementation.State != "eligible" {
			t.Fatal("explanation still requires the future reviewer", implementation)
		}
		review, err := views.TaskCapabilities(t.Context(), tx.Q(), task.TaskID, 1, protocol.RoleAccept, h.clock.Now().UnixMilli())
		if err == nil && review.State == "eligible" {
			t.Fatal("future reviewer limitation was hidden", review)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	op := h.exec(runner("linux-1"), h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{Runtime: runtime, WorkspaceProtocol: 1}, nil), Claim{})
	var result claimResult
	if err := json.Unmarshal(op.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.Assignment.TaskID != task.TaskID || result.Assignment.Role != protocol.RoleImplement || result.Assignment.Execution == nil || result.Assignment.Execution.Identity.LocalID != "alpha" {
		t.Fatal("current implementation did not start", result.Assignment)
	}
}
