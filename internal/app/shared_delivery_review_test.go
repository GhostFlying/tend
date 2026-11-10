package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func sharedReviewAction(t *testing.T, h *harness, issue string, p protocol.UpdateSharedDeliveryReviewPayload) protocol.Operation {
	t.Helper()
	i := canonicalTaskIssue(t, h, issue)
	p.ExpectedAssignmentEpoch = i.AssignmentEpoch
	if p.Reason == "" {
		p.Reason = "Review the complete linked PR delivery and preserve its exact evidence"
	}
	return h.exec(owner, h.env("issues.review_delivery", protocol.Target{Type: "issue", ID: issue}, p, rev(i.Revision)), ReviewIssueDelivery{})
}
func sharedReviewResult(t *testing.T, op protocol.Operation) protocol.SharedDeliveryReview {
	t.Helper()
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var r protocol.SharedDeliveryReview
	if err := json.Unmarshal(op.Result, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// Real named implement/accept runs establish the reusable original verdict.
func sharedReviewFixture(t *testing.T, evidenceOverride ...json.RawMessage) (*harness, *protocol.RunnerRuntime, protocol.Issue, string, protocol.IssueDeliveryCandidate, string) {
	t.Helper()
	h, r, p, _ := inventorySetup(t)
	p.Agents[0].Categories = []string{"implementer"}
	p.Agents[1].Categories = []string{"reviewer"}
	_, hash, err := protocol.CanonicalAgentInventory(p)
	if err != nil {
		t.Fatal(err)
	}
	if op := h.exec(runner("linux-1"), inventoryRequest(h, r, p, hash, 1, 0, "shared-review"), RegisterAgentInventory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	r.Capabilities = append(r.Capabilities, protocol.CapabilityAgentExecution)
	r.JournalVersion = 12
	for _, a := range p.Agents {
		r.AgentExecutors = append(r.AgentExecutors, protocol.AgentExecutor{Available: true, Pin: protocol.RunExecutionPin{Identity: a.Identity, AgentRevision: a.Revision, Runtime: a.Binding, InventoryRevision: 1, SnapshotID: "shared-review", PublicSHA256: hash, Profile: protocol.ExecutionProfile{Model: a.RequestedModel, Effort: a.RequestedEffort}, Worker: protocol.WorkerAppServer, BackendID: "git"}})
	}
	r.Sequence++
	if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	i := createCanonicalOwnerTestIssue(t, h)
	i.Plan.Deliveries = []protocol.IssueDelivery{{DeliveryID: "pr", Provider: "github", Repository: "example/repo", Number: 17, URL: "https://example.invalid/repo/pull/17"}}
	i = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, i, "update", protocol.UpdateIssuePayload{Plan: i.Plan, ExpectedAssignmentEpoch: i.AssignmentEpoch}))
	spec := protocol.OwnerTaskSpec{Goal: "Implement the complete PR delivery", Acceptance: []string{"Review the entire candidate under the repository convention"}, Delivery: "commit", Workspace: protocol.OwnerTaskWorkspace{Mounts: []protocol.OwnerTaskMount{{Repo: "tend"}}}, AgentSelection: &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer"}, Acceptance: protocol.AgentRequirement{CategoryID: "reviewer"}}}
	op := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: i.IssueID}, protocol.CreateIssueTaskPayload{Title: "Complete delivery", ExpectedAssignmentEpoch: i.AssignmentEpoch, Spec: spec, IndependentAcceptance: &protocol.IndependentAcceptanceRequest{Required: true, Reason: "Establish the original independent whole-PR verdict for later reuse"}}, rev(i.Revision)), CanonicalOwnerTaskCreate{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var created taskResult
	json.Unmarshal(op.Result, &created)
	impl, ok := h.claimInputRun(r)
	if !ok || impl.TaskID != created.TaskID {
		t.Fatal(impl)
	}
	if op := h.runAs(impl, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	artifact := testArtifact(h.task(created.TaskID).WorkspaceName)
	artifact.Mounts[0].Changed = true
	artifact.Mounts[0].CommitSHA = strings.Repeat("a", 40)
	if op := h.runAs(impl, protocol.OpRunsSubmitArtifact, artifact, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	accept, ok := h.claimInputRun(r)
	if !ok || accept.TaskID != created.TaskID || accept.Role != protocol.RoleAccept {
		t.Fatal(accept)
	}
	var requirements string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		requirements, err = store.SharedReviewRequirements(t.Context(), tx.Q(), protocol.SharedDeliveryReview{Links: []protocol.SharedDeliveryReviewLink{{IssueID: i.IssueID, DeliveryID: "pr", TaskID: created.TaskID}}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	evidence, _ := json.Marshal(map[string]any{"delivery_review": protocol.SharedReviewEvidence{Provider: "github", Repository: "example/repo", Number: 17, HeadSHA: strings.Repeat("a", 40), BaseSHA: strings.Repeat("b", 40), RequirementsDigest: requirements, RepositoryCheck: "independent formal whole-candidate repository review completed"}})
	if len(evidenceOverride) > 0 {
		evidence = evidenceOverride[0]
	}
	if op := h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed, Evidence: evidence}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	resource := h.resourceForTask(created.TaskID)
	if !h.releaseResource(created.TaskID, resource.WorkspaceID) {
		t.Fatal("release")
	}
	i = canonicalTaskIssue(t, h, i.IssueID)
	c := protocol.IssueDeliveryCandidate{DeliveryID: "pr", URL: i.Plan.Deliveries[0].URL, HeadSHA: strings.Repeat("a", 40), BaseSHA: strings.Repeat("b", 40), TargetSHA: strings.Repeat("b", 40), ProviderState: "open", Validation: "final checks passed for the reviewed head", DeploymentScope: "none; merge only"}
	observeDeliveryForTest(t, h, i.IssueID, c, "open", "")
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		o, err := store.GetDeliveryObservation(t.Context(), tx.Q(), i.IssueID, "pr")
		if err != nil {
			return err
		}
		o.Observation.CI = &protocol.DeliveryCI{HeadSHA: c.HeadSHA, State: "success", Checks: []protocol.DeliveryCheck{{Name: "required", State: "completed", Conclusion: "success"}, {Name: "conditional", State: "completed", Conclusion: "skipped"}}}
		return putDeliveryObservationForTest(tx, t.Context(), o)
	}); err != nil {
		t.Fatal(err)
	}
	var acceptance string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT acceptance_id FROM acceptances WHERE task_id=?`, created.TaskID).Scan(&acceptance)
	}); err != nil {
		t.Fatal(err)
	}
	return h, r, i, created.TaskID, c, acceptance
}

func importSharedReviewForTest(t *testing.T, h *harness, i protocol.Issue, task, acceptance string) protocol.SharedDeliveryReview {
	t.Helper()
	linked := sharedReviewResult(t, sharedReviewAction(t, h, i.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr", TaskID: task}))
	if linked.ReviewID == "" || linked.RequirementsDigest == "" || linked.HeadSHA == "" || linked.BaseSHA == "" {
		t.Fatal("link did not expose the exact instructions needed to create the real reviewer task", linked)
	}
	sharedReviewResult(t, sharedReviewAction(t, h, i.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "start", DeliveryID: "pr", ReviewerTaskID: task, AcceptanceID: acceptance}))
	return sharedReviewResult(t, sharedReviewAction(t, h, i.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
}

func setPRPolicyForTest(t *testing.T, h *harness, issue, mode string) protocol.Issue {
	t.Helper()
	i := canonicalTaskIssue(t, h, issue)
	i.Plan.PRPolicy = &protocol.IssuePRPolicy{Mode: mode, Repository: "example/repo", Reason: "Owner authorizes this Issue's explicit integration policy; deployment remains separate"}
	return canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, i, "update", protocol.UpdateIssuePayload{Plan: i.Plan, ExpectedAssignmentEpoch: i.AssignmentEpoch}))
}

func TestSharedDeliveryReviewReusesActualAcceptanceAndAutomaticMergeGrant(t *testing.T) {
	h, _, i, task, c, acceptance := sharedReviewFixture(t)
	r := importSharedReviewForTest(t, h, i, task, acceptance)
	if !r.Valid || r.Status != "passed" || len(r.Rounds) != 1 || r.Reviewer != "linux-1/beta" || r.Rounds[0].AcceptanceID != acceptance {
		t.Fatal(r)
	}
	i = setPRPolicyForTest(t, h, i.IssueID, "auto")
	d := prepareDeliveryForTest(t, h, i.IssueID, task, c)
	if d.Status != "authorized" || d.AttentionID != "" || d.Authorization == nil || d.Authorization.Mode != "auto" || h.count("attentions") != 0 {
		t.Fatal(d)
	}
	if op := createDeliveryExecutionForTest(t, h, i.IssueID, d); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	d = deliveryDutyForTest(t, h, i.IssueID, task)
	var snapshot deliveryExecutionSnapshot
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var raw string
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT candidate_json FROM issue_delivery_executions WHERE task_id=?`, d.ExecutionTaskID).Scan(&raw); err != nil {
			return err
		}
		return json.Unmarshal([]byte(raw), &snapshot)
	}); err != nil {
		t.Fatal(err)
	}
	if snapshot.Authorization == nil || snapshot.Authorization.Mode != "auto" || snapshot.HeadSHA != c.HeadSHA {
		t.Fatal(snapshot)
	}
	setPRPolicyForTest(t, h, i.IssueID, "manual")
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		current, err := store.GetCanonicalIssue(t.Context(), tx.Q(), i.IssueID)
		if err != nil {
			return err
		}
		return store.DeliveryGrantValid(t.Context(), tx.Q(), current, d)
	}); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("revoked policy remained valid", err)
	}
}

func TestSharedDeliveryReviewScopeAssessmentAndSamePRReuse(t *testing.T) {
	h, _, i, task, _, acceptance := sharedReviewFixture(t)
	r := importSharedReviewForTest(t, h, i, task, acceptance)
	other := createCanonicalOwnerTestIssue(t, h)
	other.Plan.Deliveries = i.Plan.Deliveries
	other = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, other, "update", protocol.UpdateIssuePayload{Plan: other.Plan, ExpectedAssignmentEpoch: other.AssignmentEpoch}))
	var c protocol.IssueDeliveryCandidate
	c.DeliveryID = "pr"
	c.HeadSHA = r.HeadSHA
	c.BaseSHA = r.BaseSHA
	c.TargetSHA = r.BaseSHA
	c.URL = i.Plan.Deliveries[0].URL
	observeDeliveryForTest(t, h, other.IssueID, c, "open", "")
	joined := sharedReviewResult(t, sharedReviewAction(t, h, other.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr"}))
	if joined.ReviewID != r.ReviewID || len(joined.Rounds) != 1 || h.count("shared_delivery_reviews") != 1 {
		t.Fatal(joined)
	}
	assessed := sharedReviewResult(t, sharedReviewAction(t, h, other.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "assess", DeliveryID: "pr", IssueControlPayload: protocol.IssueControlPayload{Reason: "Original whole-PR result covers this unchanged responsibility"}}))
	if !assessed.Valid || len(assessed.Rounds) != 1 {
		t.Fatal(assessed)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		page, err := views.SharedDeliveryReviews(t.Context(), tx.Q(), other.IssueID, "pr", "", 1)
		if err == nil && (len(page.Items) != 1 || len(page.Items[0].Links) != 2 || page.Items[0].ReviewID != r.ReviewID) {
			t.Fatal(page)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIssuePRPolicyFutureTasksUseSharedReviewWithoutChangingHistory(t *testing.T) {
	h, _, i, oldTask, _, _ := sharedReviewFixture(t)
	i = setPRPolicyForTest(t, h, i.IssueID, "manual")
	op := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: i.IssueID}, canonicalOwnerTaskPayload{ExpectedAssignmentEpoch: i.AssignmentEpoch, Title: "Future ordinary work", Spec: testSpec}, rev(i.Revision)), CanonicalOwnerTaskCreate{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var created taskResult
	if err := json.Unmarshal(op.Result, &created); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		old, err := store.TaskAcceptancePolicy(t.Context(), tx.Q(), oldTask)
		if err != nil {
			return err
		}
		future, err := store.TaskAcceptancePolicy(t.Context(), tx.Q(), created.TaskID)
		if err != nil {
			return err
		}
		if old != nil && !old.Required || future == nil || future.Required {
			t.Fatal(old, future)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIssuePRPolicyRetainsExplicitIndependentAcceptance(t *testing.T) {
	for _, origin := range []string{"creation", "later-policy"} {
		t.Run(origin, func(t *testing.T) {
			h := newDeliverableHarness(t)
			var i protocol.Issue
			if origin == "creation" {
				i = policyIssue(t, h, true)
			} else {
				i = createCanonicalOwnerTestIssue(t, h)
				if op := policyChange(t, h, i, true); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			i = setPRPolicyForTest(t, h, i.IssueID, "manual")
			task := policyReportTask(t, h, i)
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				p, err := store.TaskAcceptancePolicy(t.Context(), tx.Q(), task)
				if err != nil {
					return err
				}
				if p == nil || !p.Required || p.RequestID != i.AcceptancePolicy.RequestID || p.DecidedBy.Kind != i.AcceptancePolicy.DecidedBy.Kind {
					t.Fatal("explicit independent acceptance was silently replaced", p)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSharedDeliveryReviewRequiresIndependentIssueTarget(t *testing.T) {
	h, _, i, source, _, _ := sharedReviewFixture(t)
	i = canonicalTaskIssue(t, h, i.IssueID)
	p := protocol.UpdateSharedDeliveryReviewPayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: i.AssignmentEpoch, Reason: "Reject malformed review target"}, Action: "link", DeliveryID: "pr", TaskID: source}
	op := h.exec(owner, h.env("issues.review_delivery", protocol.Target{Type: "task", ID: i.IssueID}, p, rev(i.Revision)), ReviewIssueDelivery{})
	expectWorkCode(t, op, protocol.CodeInvalidRequest)
	if h.count("shared_delivery_reviews") != 0 || h.count("shared_delivery_review_links") != 0 {
		t.Fatal("malformed target changed shared review")
	}
}

func createSharedReviewerTaskForTest(t *testing.T, h *harness, issue string) string {
	t.Helper()
	i := canonicalTaskIssue(t, h, issue)
	spec := protocol.OwnerTaskSpec{Goal: "Review the complete linked PR candidate using the repository convention", Acceptance: []string{"Submit exact candidate, requirements, repository check and findings in report.shared_review"}, Deliverables: []string{domain.DeliveryReport}, Workspace: protocol.OwnerTaskWorkspace{Mode: domain.WorkspaceTemporary}, AgentSelection: &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "reviewer", AgentID: "linux-1/beta"}, Acceptance: protocol.AgentRequirement{CategoryID: "reviewer"}}}
	payload := protocol.CreateIssueTaskPayload{ExpectedAssignmentEpoch: i.AssignmentEpoch, Title: "Whole PR formal review", Spec: spec}
	payload.IndependentAcceptance = &protocol.IndependentAcceptanceRequest{Required: false, Reason: "The original reviewer produces the immutable formal report; no separate review-of-review is needed"}
	op := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issue}, payload, rev(i.Revision)), CanonicalOwnerTaskCreate{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var created taskResult
	if err := json.Unmarshal(op.Result, &created); err != nil {
		t.Fatal(err)
	}
	return created.TaskID
}

func executeSharedReviewForTest(t *testing.T, h *harness, runtime *protocol.RunnerRuntime, review protocol.SharedDeliveryReview, task, verdict string) {
	t.Helper()
	run, ok := h.claimInputRun(runtime)
	if !ok || run.TaskID != task || run.Execution == nil || run.Execution.Identity.LocalID != "beta" {
		t.Fatal(run)
	}
	if op := h.runAs(run, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	path := h.resourceForTask(task).Path
	if path == "" {
		path = t.TempDir()
	}
	if op := h.runAs(run, protocol.OpRunsReportWorkspace, protocol.WorkspaceReport{BackendID: run.Workspace.BackendID, Path: path, Baseline: json.RawMessage(`{"files":[]}`)}, ReportWorkspace{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	result := &protocol.SharedReviewResult{ReviewID: review.ReviewID, HeadSHA: review.HeadSHA, BaseSHA: review.BaseSHA, RequirementsDigest: review.RequirementsDigest, Verdict: verdict, RepositoryCheck: "Formal independent repository review of the whole candidate and affected delta"}
	if verdict == "changes_requested" {
		result.Findings = []protocol.SharedReviewFinding{{ID: "F1", Priority: "P2", Body: "The candidate's requested invariant is not established"}}
	}
	if op := h.runAs(run, protocol.OpRunsSubmitArtifact, protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "Whole PR review evidence", SharedReview: result}}, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.task(task).Lifecycle != protocol.TaskCompleted {
		t.Fatal("a formal review report scheduled another duplicate source review", h.task(task))
	}
}

func TestSharedDeliveryReviewFocusedRecheckRetainsResponsibleFindings(t *testing.T) {
	for _, finalVerdict := range []string{"passed", "changes_requested"} {
		t.Run(finalVerdict, func(t *testing.T) {
			h, runtime, i, source, c, _ := sharedReviewFixture(t)
			i = setPRPolicyForTest(t, h, i.IssueID, "manual")
			sharedReviewResult(t, sharedReviewAction(t, h, i.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr", TaskID: source}))
			first := createSharedReviewerTaskForTest(t, h, i.IssueID)
			r := sharedReviewResult(t, sharedReviewAction(t, h, i.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "start", DeliveryID: "pr", ReviewerTaskID: first}))
			executeSharedReviewForTest(t, h, runtime, r, first, "changes_requested")
			r = sharedReviewResult(t, sharedReviewAction(t, h, i.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
			if r.Status != "changes_requested" || len(r.Rounds) != 1 {
				t.Fatal(r)
			}
			expectWorkCode(t, sharedReviewAction(t, h, i.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "recheck", DeliveryID: "pr", ReviewerTaskID: first}), protocol.CodeInvalidTransition)
			sharedReviewResult(t, sharedReviewAction(t, h, i.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "resolve", DeliveryID: "pr", Dispositions: []protocol.SharedReviewDisposition{{FindingID: "F1", Action: "fixed", Reason: "Consolidated fix and affected-path validation"}}}))
			second := createSharedReviewerTaskForTest(t, h, i.IssueID)
			c.HeadSHA = strings.Repeat("c", 40)
			observeDeliveryForTest(t, h, i.IssueID, c, "open", "")
			r = sharedReviewResult(t, sharedReviewAction(t, h, i.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "recheck", DeliveryID: "pr", ReviewerTaskID: second}))
			if r.Reviewer != "linux-1/beta" || len(r.Rounds) != 2 || r.Rounds[0].HeadSHA == r.Rounds[1].HeadSHA {
				t.Fatal(r)
			}
			executeSharedReviewForTest(t, h, runtime, r, second, finalVerdict)
			r = sharedReviewResult(t, sharedReviewAction(t, h, i.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
			if finalVerdict == "passed" {
				if !r.Valid || r.Status != "passed" {
					t.Fatal(r)
				}
			} else {
				if r.Status != "changes_requested" || r.Valid {
					t.Fatal(r)
				}
				var notices int
				if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
					return tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM background_sources WHERE kind='delivery.review_escalated'`).Scan(&notices)
				}); err != nil || notices != 0 {
					t.Fatal("default review count created a human escalation", notices, err)
				}
			}
			expectWorkCode(t, sharedReviewAction(t, h, i.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "start", DeliveryID: "pr", ReviewerTaskID: second}), protocol.CodeInvalidTransition)
		})
	}
}

func TestIssuePRPolicyAgentCannotGrantAutomaticMergeWithoutOwnerSource(t *testing.T) {
	h := hostedManagerHarness(t)
	i := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	turn, principal := startHostedIssueReview(t, h, i)
	i.Plan.PRPolicy = &protocol.IssuePRPolicy{Mode: "auto", Repository: "example/repo", Reason: "Agent tries to enlarge its own authority"}
	p := protocol.UpdateIssuePayload{Plan: i.Plan, ExpectedAssignmentEpoch: i.AssignmentEpoch}
	c := HostedCoordinationCommand{Command: CanonicalOwnerIssueCommand{Action: "update"}, ExecutionID: turn.RequestID, EffectSeq: 1}
	op, err := h.app.Execute(t.Context(), principal, h.env(c.Operation(), protocol.Target{Type: "issue", ID: i.IssueID}, p, rev(i.Revision)), c)
	if err == nil {
		err = op.Error
	}
	if protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal(op, err)
	}
	if canonicalTaskIssue(t, h, i.IssueID).Plan.PRPolicy != nil {
		t.Fatal("unauthorized policy persisted")
	}
}

// Existing escalated rows remain readable and can be continued by the current
// responsibility, without rewriting their old authorization history.
func TestSharedDeliveryReviewLegacyEscalationAllowsResponsibleRecheck(t *testing.T) {
	h, runtime, i, source, _, _ := sharedReviewFixture(t)
	setPRPolicyForTest(t, h, i.IssueID, "manual")
	sharedReviewResult(t, sharedReviewAction(t, h, i.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr", TaskID: source}))
	task := createSharedReviewerTaskForTest(t, h, i.IssueID)
	r := sharedReviewResult(t, sharedReviewAction(t, h, i.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "start", DeliveryID: "pr", ReviewerTaskID: task}))
	executeSharedReviewForTest(t, h, runtime, r, task, "changes_requested")
	r = sharedReviewResult(t, sharedReviewAction(t, h, i.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
	// This is a persisted legacy coordination status, not a fabricated verdict.
	r.Status = "escalated"
	r.Revision++
	r.BudgetAuthorizations = []protocol.SharedReviewBudgetAuthorization{{RequestID: "original_external_request", Round: 2, Reason: "Historical owner authorization remains part of the ledger"}}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error { return tx.PutSharedDeliveryReview(t.Context(), r, "legacy_fixture") }); err != nil {
		t.Fatal(err)
	}
	sharedReviewResult(t, sharedReviewAction(t, h, i.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "resolve", DeliveryID: "pr", Dispositions: []protocol.SharedReviewDisposition{{FindingID: "F1", Action: "fixed", Reason: "Current responsibility resolved the retained finding"}}}))
	next := createSharedReviewerTaskForTest(t, h, i.IssueID)
	r = sharedReviewResult(t, sharedReviewAction(t, h, i.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "recheck", DeliveryID: "pr", ReviewerTaskID: next}))
	executeSharedReviewForTest(t, h, runtime, r, next, "passed")
	r = sharedReviewResult(t, sharedReviewAction(t, h, i.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
	if !r.Valid || len(r.Rounds) != 2 || len(r.BudgetAuthorizations) != 1 || r.BudgetAuthorizations[0].RequestID != "original_external_request" {
		t.Fatal("legacy recovery rewrote history or required another grant", r)
	}
}

func TestSharedDeliveryAutomaticMergeCompletesFromServerObservationWithoutAnotherReview(t *testing.T) {
	h, runtime, i, source, c, acceptance := sharedReviewFixture(t)
	importSharedReviewForTest(t, h, i, source, acceptance)
	h.app.serverDeliveryObservations = true
	i = setPRPolicyForTest(t, h, i.IssueID, "auto")
	d := prepareDeliveryForTest(t, h, i.IssueID, source, c)
	i = canonicalTaskIssue(t, h, i.IssueID)
	spec := protocol.OwnerTaskSpec{Goal: "Execute the authorized exact merge and retain its receipt", Acceptance: []string{"Verify server provider identity and completed merge"}, Deliverables: []string{domain.DeliveryReport}, Workspace: protocol.OwnerTaskWorkspace{Mode: domain.WorkspaceTemporary}, AgentSelection: &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer", AgentID: "linux-1/alpha"}}}
	op := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: i.IssueID}, protocol.CreateIssueTaskPayload{ExpectedAssignmentEpoch: i.AssignmentEpoch, Title: "Apply authorized integration", Spec: spec, DeliveryAuthorization: &protocol.IssueDeliveryAuthorization{TaskID: source, Revision: d.Revision}}, rev(i.Revision)), CanonicalOwnerTaskCreate{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var execution taskResult
	json.Unmarshal(op.Result, &execution)
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityWorkerObservation)
	runtime.Sequence++
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	run, ok := h.claimInputRun(runtime)
	if !ok || run.TaskID != execution.TaskID {
		t.Fatal(run)
	}
	if op := h.runAs(run, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.runAs(run, protocol.OpRunsReportWorkspace, protocol.WorkspaceReport{BackendID: run.Workspace.BackendID, Path: t.TempDir(), Baseline: json.RawMessage(`{"files":[]}`)}, ReportWorkspace{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	query, err := h.app.ClaimServerDelivery(t.Context())
	if err != nil || query == nil {
		t.Fatal(query, err)
	}
	merge := strings.Repeat("d", 40)
	if err := h.app.RecordServerDelivery(t.Context(), query.ID, &protocol.DeliverySnapshot{State: "merged", HeadSHA: c.HeadSHA, BaseSHA: c.BaseSHA, TargetSHA: merge, MergeSHA: merge, TargetContainsMerge: true, URL: c.URL}, ""); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		current, err := deliveryTaskCurrent(t.Context(), tx.Q(), run.TaskID)
		if err == nil && !current {
			t.Fatal("legal merge transition made original execution inapplicable")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if op := h.runAs(run, protocol.OpRunsSubmitArtifact, protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "Merged only the frozen authorized candidate; provider receipt and target lineage were read from server"}}, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.task(execution.TaskID).Lifecycle != protocol.TaskCompleted {
		t.Fatal("integration scheduled another duplicate PR source review", h.task(execution.TaskID))
	}
	var serverObservation store.DeliveryObservationRow
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		var err error
		serverObservation, err = store.GetDeliveryObservation(t.Context(), tx.Q(), i.IssueID, c.DeliveryID)
		if err != nil {
			return err
		}
		older := serverObservation
		older.Observation.Observer = "runner"
		return putDeliveryObservationForTest(tx, t.Context(), older)
	}); err != nil {
		t.Fatal(err)
	}
	op = updateDeliveryForTest(t, h, i.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: source, Action: "complete", IssueControlPayload: protocol.IssueControlPayload{Reason: "An old runner receipt cannot establish server verification"}})
	expectWorkCode(t, op, protocol.CodeInvalidTransition)
	if duty := deliveryDutyForTest(t, h, i.IssueID, source); duty.Status != "executing" {
		t.Fatal("unverified provider receipt settled execution", duty)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		return putDeliveryObservationForTest(tx, t.Context(), serverObservation)
	}); err != nil {
		t.Fatal(err)
	}
	op = updateDeliveryForTest(t, h, i.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: source, Action: "complete", IssueControlPayload: protocol.IssueControlPayload{Reason: "Server verified the authorized merged head and target; complete the original delivery obligation"}})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if duty := deliveryDutyForTest(t, h, i.IssueID, source); duty.Status != "complete" || duty.AttentionID != "" || h.count("attentions") != 0 {
		t.Fatal(duty)
	}
	if h.count("acceptances") != 1 {
		t.Fatal("shared reviewer result was duplicated", h.count("acceptances"))
	}
}

func TestSharedDeliveryCIRequiresActualSuccessForReviewedHead(t *testing.T) {
	for _, state := range []string{"failure", "unknown", "pending", "skipped", "wrong-head", "truncated", "missing", "unknown-check", "unknown-conclusion"} {
		t.Run(state, func(t *testing.T) {
			h, _, i, source, c, acceptance := sharedReviewFixture(t)
			importSharedReviewForTest(t, h, i, source, acceptance)
			setPRPolicyForTest(t, h, i.IssueID, "auto")
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				o, err := store.GetDeliveryObservation(t.Context(), tx.Q(), i.IssueID, "pr")
				if err != nil {
					return err
				}
				ci := o.Observation.CI
				switch state {
				case "wrong-head":
					ci.HeadSHA = strings.Repeat("f", 40)
				case "truncated":
					ci.Truncated = true
				case "missing":
					o.Observation.CI = nil
				case "skipped":
					ci.Checks = []protocol.DeliveryCheck{{Name: "conditional", State: "completed", Conclusion: "skipped"}}
				case "unknown-check":
					ci.Checks = append(ci.Checks, protocol.DeliveryCheck{Name: "unresolved", State: "unknown"})
				case "unknown-conclusion":
					ci.Checks = append(ci.Checks, protocol.DeliveryCheck{Name: "unresolved", State: "completed", Conclusion: "unknown"})
				default:
					ci.State = state
				}
				return putDeliveryObservationForTest(tx, t.Context(), o)
			}); err != nil {
				t.Fatal(err)
			}
			op := updateDeliveryForTest(t, h, i.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: source, Action: "prepare", Candidate: &c, IssueControlPayload: protocol.IssueControlPayload{Reason: "Validation prose cannot override real failing or unknown CI"}})
			expectWorkCode(t, op, protocol.CodeInvalidTransition)
			if h.count("issue_delivery_executions") != 0 {
				t.Fatal("CI failure acquired an executable merge")
			}
		})
	}
}

func TestSharedDeliveryNarrowAcceptanceNeedsOriginalReviewerEvidence(t *testing.T) {
	h, runtime, i, source, _, acceptance := sharedReviewFixture(t, json.RawMessage(`{"unit_test":"helper only; no repository-wide review was performed"}`))
	sharedReviewResult(t, sharedReviewAction(t, h, i.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr", TaskID: source}))
	r := sharedReviewResult(t, sharedReviewAction(t, h, i.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "start", DeliveryID: "pr", ReviewerTaskID: source, AcceptanceID: acceptance}))
	expectWorkCode(t, sharedReviewAction(t, h, i.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}), protocol.CodeInvalidRequest)
	if r.Valid || r.Status != "reviewing" || len(r.Rounds) != 1 || r.Rounds[0].AcceptanceID != acceptance {
		t.Fatal(r)
	}
	// A focused real report from the original reviewer can supply missing scope
	// evidence without rewriting the original Task/verdict or spending recheck.
	setPRPolicyForTest(t, h, i.IssueID, "manual")
	supplement := createSharedReviewerTaskForTest(t, h, i.IssueID)
	r = sharedReviewResult(t, sharedReviewAction(t, h, i.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "supplement", DeliveryID: "pr", ReviewerTaskID: supplement}))
	executeSharedReviewForTest(t, h, runtime, r, supplement, "passed")
	r = sharedReviewResult(t, sharedReviewAction(t, h, i.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
	if !r.Valid || len(r.Rounds) != 1 || r.Rounds[0].TaskID != source || r.Rounds[0].AcceptanceID != acceptance || r.Rounds[0].SupplementTaskID != supplement || r.Rounds[0].SupplementArtifactID == "" {
		t.Fatal(r)
	}
}

func TestSharedDeliveryUnchangedPassedReviewDoesNotConsumeRecheck(t *testing.T) {
	h, _, i, source, c, acceptance := sharedReviewFixture(t)
	r := importSharedReviewForTest(t, h, i, source, acceptance)
	next := sharedReviewResult(t, sharedReviewAction(t, h, i.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "recheck", DeliveryID: "pr", ReviewerTaskID: source, AcceptanceID: acceptance}))
	if len(next.Rounds) != 1 || next.Revision != r.Revision || !next.Valid {
		t.Fatal("unchanged valid review consumed its only fix/recheck", next)
	}
	c.HeadSHA = strings.Repeat("e", 40)
	observeDeliveryForTest(t, h, i.IssueID, c, "open", "")
	expectWorkCode(t, sharedReviewAction(t, h, i.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "recheck", DeliveryID: "pr", ReviewerTaskID: source, AcceptanceID: acceptance}), protocol.CodeInvalidTransition)
}

func TestSharedDeliveryReviewRetainsActualBusinessConstraintChanges(t *testing.T) {
	h, _, i, source, _, acceptance := sharedReviewFixture(t)
	r := importSharedReviewForTest(t, h, i, source, acceptance)
	i = canonicalTaskIssue(t, h, i.IssueID)
	i.Plan.Background = append(i.Plan.Background, protocol.SharedFact{Kind: protocol.SharedFactUserConstraint, Text: "Do not add a public endpoint", Source: "Owner's current scope"})
	i = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, i, "update", protocol.UpdateIssuePayload{Plan: i.Plan, ExpectedAssignmentEpoch: i.AssignmentEpoch}))
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		current, err := store.SharedDeliveryReviewForTarget(t.Context(), tx.Q(), i.Plan.Deliveries[0])
		if err != nil {
			return err
		}
		if err := store.SharedDeliveryReviewCurrent(t.Context(), tx.Q(), &current); err != nil {
			return err
		}
		if current.Valid || current.Rounds[0].Result.RequirementsDigest != r.RequirementsDigest {
			t.Fatal("new business constraint was ignored or old review rewritten", current)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r = sharedReviewResult(t, sharedReviewAction(t, h, i.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "assess", DeliveryID: "pr", IssueControlPayload: protocol.IssueControlPayload{Reason: "Original reviewed candidate already preserves this endpoint boundary; retain the real review result"}}))
	if !r.Valid || len(r.Rounds) != 1 || r.AssessmentReason == "" {
		t.Fatal(r)
	}
}

func TestIssuePRPolicyClarifiesOnceAcrossNewCandidates(t *testing.T) {
	h, _, i, source, c, _ := sharedReviewFixture(t)
	first := updateDeliveryForTest(t, h, i.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: source, Action: "prepare", Candidate: &c, IssueControlPayload: protocol.IssueControlPayload{Reason: "Prepare a new PR while integration policy is unresolved"}})
	if first.Status != protocol.OpSucceeded {
		t.Fatal(first)
	}
	d := deliveryDutyForTest(t, h, i.IssueID, source)
	if d.Status != "preparing" || d.PolicyAttentionID == "" || d.AttentionID != "" || d.Authorization != nil {
		t.Fatal(d)
	}
	c.HeadSHA = strings.Repeat("f", 40)
	observeDeliveryForTest(t, h, i.IssueID, c, "open", "")
	if op := updateDeliveryForTest(t, h, i.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: source, Action: "prepare", Candidate: &c, IssueControlPayload: protocol.IssueControlPayload{Reason: "Refresh candidate without asking another strategy question"}}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	next := deliveryDutyForTest(t, h, i.IssueID, source)
	if h.count("attentions") != 1 || next.PolicyAttentionID != d.PolicyAttentionID || next.Authorization != nil {
		t.Fatal(next)
	}
	expectWorkCode(t, createDeliveryExecutionForTest(t, h, i.IssueID, next), protocol.CodeStaleRevision)
	setPRPolicyForTest(t, h, i.IssueID, "manual")
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		a, err := store.GetAttention(t.Context(), tx.Q(), d.PolicyAttentionID)
		if err == nil && a.Status != "withdrawn" {
			t.Fatal("configured policy left obsolete strategy question open", a)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
