package app

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

func TestAcceptanceExecutionCapabilityAndAssignment(t *testing.T) {
	h := newDeliverableHarness(t)
	spec := testSpec
	spec.AcceptanceExecution = &protocol.ExecutionProfile{Model: "review-model", Effort: "xhigh"}
	_, task := h.createTask(owner, createTaskPayload{Title: "independent review", Spec: spec})
	rt := testRuntime("selection")
	if op := h.versionedHello("linux-1", rt); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	as, ok := h.claimInputRun(rt)
	if !ok || as.TaskID != task.TaskID || as.Role != protocol.RoleImplement || as.AcceptanceExecution != nil {
		t.Fatalf("implementation selection: %+v", as)
	}
	workspace := as.Workspace
	var wire map[string]any
	_ = json.Unmarshal(as.Spec, &wire)
	if _, exists := wire["acceptance_execution"]; exists {
		t.Fatal("selection leaked into legacy spec")
	}
	if op := h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.runAs(as, protocol.OpRunsSubmitArtifact, testArtifact(h.task(task.TaskID).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	runs := h.count("runs")
	if assignment, ok := h.claimInputRun(rt); ok {
		t.Fatalf("runner without acceptance capability claimed the actual review: %+v", assignment)
	}
	if h.count("runs") != runs || h.task(task.TaskID).Lifecycle != protocol.TaskAwaitingAcceptance {
		t.Fatal("unsupported acceptance consumed or rewrote the pending review", h.task(task.TaskID))
	}
	rt.Capabilities = append(rt.Capabilities, protocol.CapabilityAcceptanceExecution)
	as, ok = h.claimInputRun(rt)
	if !ok || as.TaskID != task.TaskID || as.Role != protocol.RoleAccept || as.AcceptanceExecution == nil || *as.AcceptanceExecution != *spec.AcceptanceExecution || as.Workspace.ID != workspace.ID || as.Workspace.Kind != workspace.Kind || as.Workspace.BackendID != workspace.BackendID {
		t.Fatalf("acceptance selection: %+v", as)
	}
	old := testRuntime("older-runner")
	old.Generation = rt.Generation + 1
	env := h.env(protocol.OpRunnerHello, protocol.Target{Type: "host", ID: "linux-1"}, protocol.HelloPayload{WorkspaceProtocol: 1, Runtime: old}, nil)
	if _, err := h.app.Execute(t.Context(), runner("linux-1"), env, Hello{}); protocol.CodeOf(err) != protocol.CodeInvalidRequest {
		t.Fatalf("unsupported recovery: %v", err)
	}
}
