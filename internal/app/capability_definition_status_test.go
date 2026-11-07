package app

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestCapabilityQueriesExposeRetirementWithoutChangingFrozenMatching(t *testing.T) {
	h, runtime, pin := capabilitySetup(t)
	task := capabilityTask(t, h)
	original := capabilityInventory(t, h)
	before, err := h.app.CapabilityAgents(t.Context(), protocol.AgentCapabilityQuery{Limit: 1})
	if err != nil || len(before.Items) != 1 || before.Next == "" {
		t.Fatal(before, err)
	}
	active := before.Items[0]
	if len(active.DefinitionStatuses) != 1 || active.DefinitionStatuses[0].CurrentDefinition == nil || active.DefinitionStatuses[0].CurrentDefinition.State != "active" {
		t.Fatal("active definition not visible", active)
	}
	taskBefore, err := h.app.TaskCapabilityExplanation(t.Context(), task, 1, protocol.RoleImplement)
	if err != nil || taskBefore.State != "eligible" {
		t.Fatal(taskBefore, err)
	}
	taskPage, err := h.app.TaskCapabilityExplanationPage(t.Context(), task, 1, protocol.RoleImplement, protocol.AgentCapabilityQuery{Limit: 1})
	if err != nil || taskPage.Next == "" {
		t.Fatal(taskPage, err)
	}
	putTaskCapability(t, h, "image_gen", "retired", 1)
	if _, err := h.app.CapabilityAgents(t.Context(), protocol.AgentCapabilityQuery{Limit: 1, After: before.Next}); protocol.CodeOf(err) != protocol.CodeInvalidRequest {
		t.Fatal("definition change did not invalidate Agent cursor", err)
	}
	if _, err := h.app.TaskCapabilityExplanationPage(t.Context(), task, 1, protocol.RoleImplement, protocol.AgentCapabilityQuery{Limit: 1, After: taskPage.Next}); protocol.CodeOf(err) != protocol.CodeInvalidRequest {
		t.Fatal("definition change did not invalidate Task candidate cursor", err)
	}
	after, err := h.app.CapabilityAgents(t.Context(), protocol.AgentCapabilityQuery{Environment: "linux-1", Agent: "alpha"})
	if err != nil || len(after.Items) != 1 {
		t.Fatal(after, err)
	}
	retired := after.Items[0]
	if len(retired.DefinitionStatuses) != 1 || !reflect.DeepEqual(retired.Declared, active.Declared) || !reflect.DeepEqual(retired.Current, active.Current) || !reflect.DeepEqual(retired.Pin, active.Pin) {
		t.Fatal("current definition rewrote historical declaration or match", active, retired)
	}
	status := retired.DefinitionStatuses[0]
	if status.Declared != active.Declared[0] || status.CurrentDefinition == nil || status.CurrentDefinition.Revision != 2 || status.CurrentDefinition.State != "retired" || !strings.Contains(status.Explanation, "new references are rejected") {
		t.Fatal("retirement not explicit alongside historical declaration", status)
	}
	taskAfter, err := h.app.TaskCapabilityExplanation(t.Context(), task, 1, protocol.RoleImplement)
	if err != nil || taskAfter.State != taskBefore.State || !reflect.DeepEqual(taskAfter.Required, taskBefore.Required) || len(taskAfter.Candidates) != len(taskBefore.Candidates) {
		t.Fatal("projection changed frozen Task matching", taskBefore, taskAfter, err)
	}
	for i, candidate := range taskAfter.Candidates {
		if !reflect.DeepEqual(candidate.Current, taskBefore.Candidates[i].Current) || !reflect.DeepEqual(candidate.Pin, taskBefore.Candidates[i].Pin) {
			t.Fatal("projection changed candidate match or pin", candidate)
		}
		if len(candidate.Declared) > 0 && (len(candidate.DefinitionStatuses) != 1 || candidate.DefinitionStatuses[0].CurrentDefinition.State != "retired") {
			t.Fatal("Task candidate hid retirement", candidate)
		}
	}
	newSpec := taskCapabilitySpec()
	newSpec.AgentSelection.Implementation.RequiredCapabilities = active.Declared
	op, _ := h.createTask(owner, createTaskPayload{Title: "new retired reference", Spec: newSpec})
	if op.Status != protocol.OpFailed || op.Error == nil || op.Error.Code != protocol.CodeInvalidRequest {
		t.Fatal("new Task admitted retired reference", op)
	}
	// An already-frozen Task still claims its original exact reference and pin.
	op = h.exec(runner("linux-1"), h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{WorkspaceProtocol: 1, Runtime: runtime}, nil), Claim{})
	var claimed claimResult
	if err := json.Unmarshal(op.Result, &claimed); err != nil || op.Status != protocol.OpSucceeded || claimed.Assignment.Execution == nil || *claimed.Assignment.Execution != pin || claimed.Assignment.CapabilityBinding == nil {
		t.Fatal("retirement changed frozen claim", claimed, op, err)
	}
	var originalBinding protocol.RunCapabilityBinding
	var originalSpec string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		originalBinding, err = store.GetRunCapabilityBinding(t.Context(), tx.Q(), claimed.Assignment.RunID)
		if err != nil {
			return err
		}
		originalSpec, err = store.GetSpec(t.Context(), tx.Q(), task, 1)
		return err
	}); err != nil || originalBinding.Pin != pin || !reflect.DeepEqual(originalBinding.Required, active.Declared) {
		t.Fatal("frozen claim lost original declaration or pin", originalBinding, err)
	}
	if _, err := h.app.CapabilityAgents(t.Context(), protocol.AgentCapabilityQuery{}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.TaskCapabilityExplanation(t.Context(), task, 1, protocol.RoleImplement); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(capabilityInventory(t, h), original) {
		t.Fatal("read projection changed accepted inventory")
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		binding, err := store.GetRunCapabilityBinding(t.Context(), tx.Q(), claimed.Assignment.RunID)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(binding, originalBinding) {
			t.Fatal("read projection rewrote persisted binding", binding)
		}
		spec, err := store.GetSpec(t.Context(), tx.Q(), task, 1)
		if err == nil && spec != originalSpec {
			t.Fatal("read projection rewrote frozen spec")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
