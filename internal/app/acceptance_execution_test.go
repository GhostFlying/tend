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
	if as, ok := h.claimInputRun(rt); ok {
		t.Fatalf("old runner claimed selected model: %+v", as)
	}
	rt.Capabilities = append(rt.Capabilities, protocol.CapabilityAcceptanceExecution)
	as, ok := h.claimInputRun(rt)
	if !ok || as.TaskID != task.TaskID || as.AcceptanceExecution != nil {
		t.Fatalf("implementation selection: %+v", as)
	}
	var wire map[string]any
	_ = json.Unmarshal(as.Spec, &wire)
	if _, exists := wire["acceptance_execution"]; exists {
		t.Fatal("selection leaked into legacy spec")
	}
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	if op := h.runAs(as, protocol.OpRunsSubmitArtifact, testArtifact(h.task(task.TaskID).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	as, ok = h.claimInputRun(rt)
	if !ok || as.Role != protocol.RoleAccept || as.AcceptanceExecution == nil || *as.AcceptanceExecution != *spec.AcceptanceExecution {
		t.Fatalf("acceptance selection: %+v", as)
	}
	old := testRuntime("older-runner")
	old.Generation = rt.Generation + 1
	env := h.env(protocol.OpRunnerHello, protocol.Target{Type: "host", ID: "linux-1"}, protocol.HelloPayload{WorkspaceProtocol: 1, Runtime: old}, nil)
	if _, err := h.app.Execute(t.Context(), runner("linux-1"), env, Hello{}); protocol.CodeOf(err) != protocol.CodeInvalidRequest {
		t.Fatalf("unsupported recovery: %v", err)
	}
}
