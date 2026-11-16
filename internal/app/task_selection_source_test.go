package app

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestTaskSelectionSourceReplacesForgedOwnerAndPreservesLegacyFilters(t *testing.T) {
	h := newDeliverableHarness(t)
	spec := testSpec
	spec.SelectionSource = &protocol.TaskSelectionSource{Actor: owner, RequestID: "forged", OwnerPin: "forged/owner"}
	spec.AcceptanceExecution = &protocol.ExecutionProfile{Model: "review", Effort: "xhigh"}
	op, task := h.createTask(mainAgent, createTaskPayload{Title: "Legacy model selection", Spec: spec})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	stored := h.storedSpec(task.TaskID, 1)
	source := stored.SelectionSource
	if source == nil || source.Actor.Kind != protocol.PrincipalMainAgent || source.RequestID != op.RequestID || source.OwnerPin != "" || source.AcceptanceExecution == nil || *source.AcceptanceExecution != *spec.AcceptanceExecution {
		t.Fatal("forged owner provenance or silently lost legacy profile", source)
	}
	// Audit-only changes cannot create a new spec or retry work.
	stored.SelectionSource = &protocol.TaskSelectionSource{Actor: owner}
	noChange := h.exec(owner, h.env("tasks.update_intent", taskTarget(task.TaskID), updateIntentPayload{Spec: &stored}, rev(h.task(task.TaskID).ControlRevision)), UpdateIntent{})
	if noChange.Status != protocol.OpSucceeded || h.task(task.TaskID).SpecVersion != 1 || h.storedSpec(task.TaskID, 1).SelectionSource.Actor.Kind != protocol.PrincipalMainAgent {
		t.Fatal("metadata alone changed the agreement", noChange)
	}
	stored.Goal = "Revised by the authenticated human"
	changed := h.exec(owner, h.env("tasks.update_intent", taskTarget(task.TaskID), updateIntentPayload{Spec: &stored}, rev(h.task(task.TaskID).ControlRevision)), UpdateIntent{})
	if changed.Status != protocol.OpSucceeded || h.storedSpec(task.TaskID, 2).SelectionSource.Actor.Kind != protocol.PrincipalOwner {
		t.Fatal("human refresh attribution missing", changed)
	}
}

func TestCategoryOnlyOwnerExecutionSeparatesInputFromSystemPin(t *testing.T) {
	h, r, issue := issueOwnerHarness(t)
	op := createOwnerExecution(t, h, issue, "implementer")
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var task taskResult
	if err := json.Unmarshal(op.Result, &task); err != nil {
		t.Fatal(err)
	}
	stored := h.storedSpec(task.TaskID, 1)
	source := stored.SelectionSource
	if source == nil || source.Actor.Kind != protocol.PrincipalOwner || source.OwnerPin != issue.Assignee.Ref || source.HostID != "" || source.AgentSelection.Implementation.AgentID != "" || source.AgentSelection.Implementation.Model != "" || stored.AgentSelection.Implementation.AgentID != issue.Assignee.Ref {
		t.Fatal("system pin was attributed to author input", source, stored.AgentSelection)
	}
	as := ownerWorkerClaim(t, h, r, protocol.RoleImplement)
	if as.Execution == nil || as.Execution.Identity.LocalID != "alpha" {
		t.Fatal("category-only execution lost the retained owner", as)
	}
	var wire map[string]any
	_ = json.Unmarshal(as.Spec, &wire)
	if wire["selection_source"] != nil {
		t.Fatal("server-only selection metadata leaked into runner wire")
	}
}
