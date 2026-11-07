package app

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func sharedProfileReviewAction(t *testing.T, h *harness, issue string, p protocol.UpdateSharedDeliveryReviewPayload) protocol.Operation {
	t.Helper()
	i := canonicalTaskIssue(t, h, issue)
	p.ExpectedAssignmentEpoch = i.AssignmentEpoch
	if p.Reason == "" {
		p.Reason = "Verify the actual formal reviewer execution profile"
	}
	op, err := h.app.Execute(t.Context(), owner, h.env("issues.review_delivery", protocol.Target{Type: "issue", ID: issue}, p, rev(i.Revision)), ReviewIssueDelivery{})
	if err != nil {
		return protocol.Operation{Status: protocol.OpFailed, Error: &protocol.Error{Code: protocol.CodeOf(err)}}
	}
	return op
}

func sharedProfileSourceTask(t *testing.T, h *harness, issue protocol.Issue, required protocol.AgentRequirement) string {
	t.Helper()
	spec := protocol.OwnerTaskSpec{Goal: "Contribute a result with an explicit formal review profile", Acceptance: []string{"The complete PR satisfies the formal review requirements"}, Deliverables: []string{domain.DeliveryReport}, Workspace: protocol.OwnerTaskWorkspace{Mode: domain.WorkspaceTemporary}, AgentSelection: &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer", AgentID: "linux-1/alpha"}, Acceptance: required}}
	op := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.CreateIssueTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Contributing source", Spec: spec}, rev(issue.Revision)), CanonicalOwnerTaskCreate{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var result taskResult
	if err := json.Unmarshal(op.Result, &result); err != nil {
		t.Fatal(err)
	}
	return result.TaskID
}

func completeSharedProfileSource(t *testing.T, h *harness, runtime *protocol.RunnerRuntime, task string) {
	t.Helper()
	run, ok := h.claimInputRun(runtime)
	if !ok || run.TaskID != task {
		t.Fatal(run)
	}
	if op := h.runAs(run, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.runAs(run, protocol.OpRunsReportWorkspace, protocol.WorkspaceReport{BackendID: run.Workspace.BackendID, Path: t.TempDir(), Baseline: json.RawMessage(`{"files":[]}`)}, ReportWorkspace{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.runAs(run, protocol.OpRunsSubmitArtifact, protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "The linked result is ready for the shared formal PR review"}}, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	resource := h.resourceForTask(task)
	if !h.releaseResource(task, resource.WorkspaceID) {
		t.Fatal("release source")
	}
}

func TestSharedReviewProfileChecksActualAgentDefaults(t *testing.T) {
	for _, tc := range []struct {
		name, model, effort string
		allowed             bool
	}{
		{"model-and-effort-mismatch", "required-model", "xhigh", false},
		{"model-mismatch", "required-model", "", false},
		{"effort-mismatch", "", "xhigh", false},
		{"matching-defaults", "requested-model", "high", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, runtime, issue, _, _, _ := sharedReviewFixture(t)
			issue = setPRPolicyForTest(t, h, issue.IssueID, "manual")
			source := sharedProfileSourceTask(t, h, issue, protocol.AgentRequirement{CategoryID: "reviewer", Model: tc.model, Effort: tc.effort})
			completeSharedProfileSource(t, h, runtime, source)
			sharedReviewResult(t, sharedProfileReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr", TaskID: source}))
			reviewer := createSharedReviewerTaskForTest(t, h, issue.IssueID)
			selector := h.storedSpec(reviewer, 1).AgentSelection.Implementation
			if selector.Model != "" || selector.Effort != "" {
				t.Fatal("fixture must retain normal Agent defaults", selector)
			}
			op := sharedProfileReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "start", DeliveryID: "pr", ReviewerTaskID: reviewer})
			if !tc.allowed {
				expectWorkCode(t, op, protocol.CodeForbidden)
				return
			}
			r := sharedReviewResult(t, op)
			if r.Rounds[0].ExecutionRequirements == nil || len(r.Rounds[0].ExecutionRequirements.Profiles) != 1 {
				t.Fatal("round did not freeze its actual formal requirements", r)
			}
			executeSharedReviewForTest(t, h, runtime, r, reviewer, "passed")
			r = sharedReviewResult(t, sharedProfileReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
			if !r.Valid {
				t.Fatal(r)
			}
		})
	}
}

func changeSharedReviewerDefaults(t *testing.T, h *harness, runtime *protocol.RunnerRuntime, profile protocol.ExecutionProfile) {
	t.Helper()
	var inventory store.AgentInventoryRow
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		inventory, err = store.GetAgentInventory(t.Context(), tx.Q(), "linux-1")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	projection := inventory.Projection
	for n := range projection.Agents {
		if projection.Agents[n].Identity.LocalID == "beta" {
			projection.Agents[n].Revision++
			projection.Agents[n].RequestedModel, projection.Agents[n].RequestedEffort = profile.Model, profile.Effort
		}
	}
	_, hash, err := protocol.CanonicalAgentInventory(projection)
	if err != nil {
		t.Fatal(err)
	}
	localRevision := inventory.Receipt.LocalRevision + 1
	snapshot := "profile-changed-" + strconv.FormatInt(localRevision, 10)
	runtime.Sequence++
	if op := h.exec(runner("linux-1"), inventoryRequest(h, runtime, projection, hash, localRevision, inventory.Receipt.ServerRevision, snapshot), RegisterAgentInventory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	runtime.AgentExecutors = nil
	for _, agent := range projection.Agents {
		runtime.AgentExecutors = append(runtime.AgentExecutors, protocol.AgentExecutor{Available: true, Pin: protocol.RunExecutionPin{Identity: agent.Identity, AgentRevision: agent.Revision, Runtime: agent.Binding, InventoryRevision: localRevision, SnapshotID: snapshot, PublicSHA256: hash, Profile: protocol.ExecutionProfile{Model: agent.RequestedModel, Effort: agent.RequestedEffort}, Worker: protocol.WorkerAppServer, BackendID: "git"}})
	}
	runtime.Sequence++
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}

func TestSharedReviewProfileChecksFrozenRunAfterDefaultsChange(t *testing.T) {
	for _, history := range []string{"frozen-requirements", "historical-missing-requirements"} {
		t.Run(history, func(t *testing.T) {
			h, runtime, issue, _, _, _ := sharedReviewFixture(t)
			issue = setPRPolicyForTest(t, h, issue.IssueID, "manual")
			source := sharedProfileSourceTask(t, h, issue, protocol.AgentRequirement{CategoryID: "reviewer", Model: "requested-model", Effort: "high"})
			completeSharedProfileSource(t, h, runtime, source)
			sharedReviewResult(t, sharedProfileReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr", TaskID: source}))
			reviewer := createSharedReviewerTaskForTest(t, h, issue.IssueID)
			r := sharedReviewResult(t, sharedProfileReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "start", DeliveryID: "pr", ReviewerTaskID: reviewer}))
			if history == "historical-missing-requirements" {
				// An older binary persisted this active round before optional execution
				// requirements existed. Preserve its actual result and prove validity
				// from real run facts instead of treating nil as an empty requirements set.
				r.Rounds[0].ExecutionRequirements = nil
				r.Revision++
				if err := h.store.Write(t.Context(), func(tx *store.Tx) error { return tx.PutSharedDeliveryReview(t.Context(), r, "req_historical_profile") }); err != nil {
					t.Fatal(err)
				}
			}
			// A real newer inventory changes the otherwise unconstrained review Task's
			// execution defaults after binding. Its actual run must still meet the round.
			changeSharedReviewerDefaults(t, h, runtime, protocol.ExecutionProfile{Model: "different-model", Effort: "medium"})
			executeSharedReviewForTest(t, h, runtime, r, reviewer, "passed")
			r = sharedReviewResult(t, sharedProfileReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
			if r.Valid || r.Status != "passed" || r.Rounds[0].Result == nil || r.Rounds[0].RunID == "" || r.InvalidReason == "" {
				t.Fatal("actual mismatched run was promoted or stranded instead of retained as inapplicable", r)
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				changed, err := store.SharedDeliveryReviewChanged(t.Context(), tx.Q(), &r)
				if err == nil && !changed {
					t.Fatal("a known profile mismatch was classified as provider uncertainty")
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			changeSharedReviewerDefaults(t, h, runtime, protocol.ExecutionProfile{Model: "requested-model", Effort: "high"})
			second := createSharedReviewerTaskForTest(t, h, issue.IssueID)
			r = sharedReviewResult(t, sharedProfileReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "recheck", DeliveryID: "pr", ReviewerTaskID: second}))
			executeSharedReviewForTest(t, h, runtime, r, second, "passed")
			r = sharedReviewResult(t, sharedProfileReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
			if !r.Valid || len(r.Rounds) != 2 || r.Rounds[0].RunID == r.Rounds[1].RunID || r.Rounds[0].Reviewer != r.Rounds[1].Reviewer {
				t.Fatal("the original reviewer could not recover within the existing recheck budget", r)
			}
		})
	}
}

func TestSharedReviewProfileImportUsesHistoricalRun(t *testing.T) {
	h, runtime, issue, original, _, acceptance := sharedReviewFixture(t)
	issue = setPRPolicyForTest(t, h, issue.IssueID, "manual")
	source := sharedProfileSourceTask(t, h, issue, protocol.AgentRequirement{CategoryID: "reviewer", Model: "different-model", Effort: "xhigh"})
	sharedReviewResult(t, sharedProfileReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr", TaskID: source}))
	changeSharedReviewerDefaults(t, h, runtime, protocol.ExecutionProfile{Model: "different-model", Effort: "xhigh"})
	// The current Agent now matches, but the original acceptance was actually
	// executed with requested-model/high and cannot be relabeled as the new model.
	expectWorkCode(t, sharedProfileReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "start", DeliveryID: "pr", ReviewerTaskID: original, AcceptanceID: acceptance}), protocol.CodeForbidden)
}

func TestSharedReviewProfileDigestAndFrozenEmptyRequirements(t *testing.T) {
	h, _, issue, original, _, acceptance := sharedReviewFixture(t)
	issue = setPRPolicyForTest(t, h, issue.IssueID, "manual")
	plain := sharedProfileSourceTask(t, h, issue, protocol.AgentRequirement{CategoryID: "reviewer"})
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	constrained := sharedProfileSourceTask(t, h, issue, protocol.AgentRequirement{CategoryID: "reviewer", Model: "requested-model", Effort: "xhigh"})
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	otherCategory := sharedProfileSourceTask(t, h, issue, protocol.AgentRequirement{CategoryID: "implementer"})
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	namedReviewer := sharedProfileSourceTask(t, h, issue, protocol.AgentRequirement{CategoryID: "reviewer", AgentID: "linux-1/beta"})
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		digest := func(task string) (string, error) {
			return store.SharedReviewRequirements(t.Context(), tx.Q(), protocol.SharedDeliveryReview{Links: []protocol.SharedDeliveryReviewLink{{IssueID: issue.IssueID, DeliveryID: "pr", TaskID: task}}})
		}
		before, err := digest(plain)
		if err != nil {
			return err
		}
		for _, task := range []string{constrained, otherCategory, namedReviewer} {
			after, err := digest(task)
			if err != nil {
				return err
			}
			if before == after {
				t.Fatal("a formal reviewer model, category or identity requirement did not change the requirements digest", task)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sharedReviewResult(t, sharedProfileReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr", TaskID: original}))
	r := sharedReviewResult(t, sharedProfileReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "start", DeliveryID: "pr", ReviewerTaskID: original, AcceptanceID: acceptance}))
	var retained protocol.SharedDeliveryReview
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &retained); err != nil {
		t.Fatal(err)
	}
	requirements := retained.Rounds[0].ExecutionRequirements
	if requirements == nil || requirements.Profiles == nil || len(requirements.Profiles) != 0 {
		t.Fatal("a frozen empty requirement set became historical unknown after JSON roundtrip", string(raw))
	}
}
