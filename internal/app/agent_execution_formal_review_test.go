package app

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestNamedPromotionPreservesQueuedLegacyWorkspaceContract(t *testing.T) {
	h, r, p, _ := inventorySetup(t)
	p.Agents[0].Categories = []string{"implementer"}
	p.Agents[1].Categories = []string{"reviewer"}
	_, hash, err := protocol.CanonicalAgentInventory(p)
	if err != nil {
		t.Fatal(err)
	}
	if op := h.exec(runner("linux-1"), inventoryRequest(h, r, p, hash, 1, 0, "promotion"), RegisterAgentInventory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	r.Capabilities = append(r.Capabilities, protocol.CapabilityAgentExecution)
	r.JournalVersion = 12
	for _, agent := range p.Agents {
		r.AgentExecutors = append(r.AgentExecutors, protocol.AgentExecutor{Available: true, Pin: protocol.RunExecutionPin{Identity: agent.Identity, AgentRevision: agent.Revision, Runtime: agent.Binding, InventoryRevision: 1, SnapshotID: "promotion", PublicSHA256: hash, Profile: protocol.ExecutionProfile{Model: agent.RequestedModel, Effort: agent.RequestedEffort}, Worker: protocol.WorkerAppServer, BackendID: "git"}})
	}
	r.Sequence++
	if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	issue := createCanonicalOwnerTestIssue(t, h)
	create := func(title string, spec protocol.OwnerTaskSpec, rating protocol.Rating) string {
		t.Helper()
		issue = canonicalTaskIssue(t, h, issue.IssueID)
		op := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.CreateIssueTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: title, Spec: spec, Rating: rating}, rev(issue.Revision)), CanonicalOwnerTaskCreate{})
		if op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		var created struct {
			TaskID string `json:"task_id"`
		}
		if err := json.Unmarshal(op.Result, &created); err != nil {
			t.Fatal(err)
		}
		return created.TaskID
	}
	spec := protocol.OwnerTaskSpec{Goal: "Investigate same retained state", Acceptance: []string{"Independent evidence"}, Deliverables: []string{domain.DeliveryReport}, Workspace: protocol.OwnerTaskWorkspace{Mode: domain.WorkspaceTemporary}}
	source := create("source", spec, protocol.RatingP1)
	first, ok := h.claimInputRun(r)
	if !ok || first.TaskID != source {
		t.Fatal(first)
	}
	directory := t.TempDir()
	report := func(run protocol.Assignment) {
		t.Helper()
		if op := h.runAs(run, protocol.OpRunsReportWorkspace, protocol.WorkspaceReport{Path: directory, Baseline: json.RawMessage(`{"files":[]}`)}, ReportWorkspace{}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	finish := func(run protocol.Assignment) {
		t.Helper()
		if op := h.runAs(run, protocol.OpRunsSubmitArtifact, protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "Evidence"}}, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		accept, ok := h.claimInputRun(r)
		if !ok || accept.TaskID != run.TaskID || accept.Role != protocol.RoleAccept {
			t.Fatal(accept)
		}
		if op := h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		if !h.releaseResource(run.TaskID, run.Workspace.ID) {
			t.Fatal("stopped resource not released")
		}
	}
	report(first)
	finish(first)
	spec.Workspace = protocol.OwnerTaskWorkspace{Mode: domain.WorkspaceReuse, SourceTaskID: source}
	legacy := create("queued legacy reuse", spec, protocol.RatingP1)
	setMode := func(mode protocol.ControlMode) {
		t.Helper()
		task := h.task(legacy)
		if op := h.exec(owner, h.env("control.set_mode", taskTarget(legacy), protocol.SetControlModePayload{Mode: mode}, rev(task.ControlRevision)), SetControlMode{}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	setMode(protocol.ControlPaused)
	spec.AgentSelection = &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer"}, Acceptance: protocol.AgentRequirement{CategoryID: "reviewer"}}
	named := create("named high priority reuse", spec, protocol.RatingP0)
	if assignment, ok := h.claimInputRun(r); ok {
		t.Fatalf("named promotion invalidated unfinished legacy binding: %+v", assignment)
	}
	if resource := h.resourceForTask(named); resource.BackendID != "" {
		t.Fatal("blocked promotion changed backend", resource)
	}
	if h.storedSpec(legacy, 1).AgentSelection != nil {
		t.Fatal("legacy contract silently acquired named selection")
	}
	setMode(protocol.ControlManaged)
	old, ok := h.claimInputRun(r)
	if !ok || old.TaskID != legacy || old.Execution != nil || old.Workspace.BackendID != "" {
		t.Fatalf("legacy task no longer runnable on original default: %+v", old)
	}
	report(old)
	finish(old)
	next, ok := h.claimInputRun(r)
	if !ok || next.TaskID != named || next.Execution == nil || next.Workspace.BackendID != "git" {
		t.Fatalf("promotion stayed blocked after legacy contract ended: %+v", next)
	}
}

func TestLegacyCleanupLimitSkipsUnsupportedNamedResourcesBeforeSelection(t *testing.T) {
	h := newDeliverableHarness(t)
	var namedIDs []string
	var legacyID string
	for i := range 3 {
		task := h.createReportTask("retained", temporaryReportSpec())
		run, ok, _ := h.claimDeliverableRun()
		if !ok {
			t.Fatal("missing run")
		}
		h.reportDirectory(run, t.TempDir())
		h.completeReport(run)
		if !h.releaseResource(task.TaskID, run.Workspace.ID) {
			t.Fatal("resource not released")
		}
		if i < 2 {
			namedIDs = append(namedIDs, run.Workspace.ID)
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error { return tx.BindWorkspaceBackend(t.Context(), run.Workspace.ID, "git") }); err != nil {
				t.Fatal(err)
			}
		} else {
			legacyID = run.Workspace.ID
		}
		h.clock.Advance(time.Millisecond)
	}
	h.clock.Advance(8 * 24 * time.Hour)
	reserve := func(named bool) protocol.WorkspaceCleanupResult {
		t.Helper()
		op := h.exec(runner("linux-1"), h.env(protocol.OpWorkspaceCleanup, protocol.Target{Type: "host", ID: "linux-1"}, protocol.WorkspaceCleanupRequest{Limit: 1, AgentExecution: named}, nil), ReserveWorkspaceCleanup{})
		if op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		var result protocol.WorkspaceCleanupResult
		if err := json.Unmarshal(op.Result, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	old := reserve(false)
	if len(old.Workspaces) != 1 || old.Workspaces[0].WorkspaceID != legacyID {
		t.Fatal("unsupported older named resources exhausted the legacy cleanup batch", old)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		for _, id := range namedIDs {
			resource, err := store.GetWorkspace(t.Context(), tx.Q(), id)
			if err != nil {
				return err
			}
			if resource.Status != store.WorkspaceRetained || resource.CleanupID != "" {
				t.Fatal("legacy client reserved unsupported resource", resource)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	runtime := testRuntime("cleanup-supported")
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityAgentExecution)
	runtime.JournalVersion = 12
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	current := reserve(true)
	if len(current.Workspaces) != 1 || !strings.Contains(current.Workspaces[0].BackendID, "git") || current.Workspaces[0].WorkspaceID != namedIDs[0] {
		t.Fatal("capable client cannot reserve retained named resource", current)
	}
}
