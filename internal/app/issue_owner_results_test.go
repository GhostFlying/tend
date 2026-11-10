package app

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func TestIssueOwnerRegistersActualResultWithoutWorkerTaskAndCloses(t *testing.T) {
	h, _, issue, run, _ := reviewManagerAssignment(t)
	delivery, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+run.Manager.ConversationID)
	if err != nil || !ok {
		t.Fatal("owner has no admitted trigger", ok, err)
	}
	turn := startHostedReview(t, h, run.Manager.ConversationID, delivery)
	principal := runPrincipal(run.RunID, protocol.RoleIssueManager)
	principal.Thread, principal.ConversationID = turn.ThreadID, run.Manager.ConversationID
	tasks, runs := h.count("tasks"), h.count("runs")
	uploadID := h.ids.New(protocol.PrefixRequest)
	original := "Background: a phone title overlapped its model. Investigation: the fixed button height caused it. Result: title height now follows content."
	uploaded, err := h.app.UploadDeliverableFile(t.Context(), runPrincipal(run.RunID, run.Role), uploadID, "findings.md", strings.NewReader(original))
	if err != nil || uploaded.Status != protocol.OpSucceeded {
		t.Fatal(uploaded, err)
	}
	var file protocol.ArtifactFile
	if err := json.Unmarshal(uploaded.Result, &file); err != nil {
		t.Fatal(err)
	}
	repeated, err := h.app.UploadDeliverableFile(t.Context(), runPrincipal(run.RunID, run.Role), uploadID, "findings.md", strings.NewReader(original))
	if err != nil || string(repeated.Result) != string(uploaded.Result) {
		t.Fatal("streamed owner upload lost idempotency", repeated, err)
	}
	p := protocol.UpdateIssueDeliveryPayload{Action: "submit_result", TaskID: run.TaskID, RunID: run.RunID, Result: &protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "The original phone layout was investigated and corrected."}, Files: []protocol.ArtifactFile{file}}, IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "The responsible owner performed and checked this bounded work itself"}}
	env := h.env("issues.update_delivery", protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(issue.Revision))
	command := HostedCoordinationCommand{Command: UpdateIssueDelivery{}, ExecutionID: turn.RequestID, EffectSeq: 1}
	op := h.exec(principal, env, command)
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var receipt struct {
		ArtifactID string `json:"artifact_id"`
		TaskID     string `json:"task_id"`
		RunID      string `json:"run_id"`
	}
	if err := json.Unmarshal(op.Result, &receipt); err != nil || receipt.ArtifactID == "" || receipt.TaskID != run.TaskID || receipt.RunID != run.RunID {
		t.Fatal(receipt, err)
	}
	if replay := h.exec(principal, env, command); string(replay.Result) != string(op.Result) {
		t.Fatal("owner result replay changed identity", replay)
	}
	if h.count("tasks") != tasks || h.count("runs") != runs || h.count("artifacts") != 1 || h.count("acceptances") != 0 || h.run(run.RunID).ObservedState == protocol.RunFinished {
		t.Fatal("result fabricated a worker, verdict or manager completion")
	}
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	if op := hostedEffect(t, h, principal, turn, 2, protocol.Target{Type: "issue", ID: issue.IssueID}, rev(issue.Revision), protocol.UpdateIssueDeliveryPayload{TaskID: run.TaskID, Action: "report", IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "This bounded self-checked report is the actual requested endpoint"}}, UpdateIssueDelivery{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var evidence protocol.IssueEvidence
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		evidence, err = store.CompletedTaskEvidence(t.Context(), tx.Q(), run.TaskID, "The responsible owner performed and checked this result")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if evidence.Kind != protocol.WorkItemEvidenceResult || evidence.Reference != receipt.ArtifactID {
		t.Fatal(evidence)
	}
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	close := protocol.IssueEndpointPayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch}, Closure: &protocol.IssueClosure{Kind: "completed", Reason: "Requested report and source are available", Evidence: []protocol.IssueEvidence{evidence}}}
	if op := hostedEffect(t, h, principal, turn, 3, protocol.Target{Type: "issue", ID: issue.IssueID}, rev(issue.Revision), close, CanonicalOwnerIssueCommand{Action: "close"}); op.Status != protocol.OpSucceeded {
		t.Fatal("self result could not close the actual Issue", op)
	}
	if h.count("tasks") != tasks || h.count("runs") != runs {
		t.Fatal("closure manufactured an execution")
	}
}

func TestIssueOwnerResultPreservesExplicitHumanIndependentReview(t *testing.T) {
	h, runtime := managerClaimHarness(t)
	issue := policyIssue(t, h, true)
	op := h.exec(owner, h.env(protocol.OpIssueRequestManager, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.RequestIssueManagerPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Perform this explicitly reviewed responsibility"}, rev(issue.Revision)), RequestIssueManager{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	run := reviewClaimManager(t, h, runtime)
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	p := protocol.UpdateIssueDeliveryPayload{Action: "submit_result", TaskID: run.TaskID, RunID: run.RunID, Result: &protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "A real result is a fact, not an independent verdict."}}, IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Record the actual owner result while retaining the human quality requirement"}}
	if op := h.exec(owner, h.env("issues.update_delivery", protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(issue.Revision)), UpdateIssueDelivery{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	result := updateDeliveryForTest(t, h, issue.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: run.TaskID, Action: "report", IssueControlPayload: protocol.IssueControlPayload{Reason: "Attempt to settle before actual independent acceptance"}})
	if result.Error == nil || result.Error.Code != protocol.CodeInvalidTransition || h.count("acceptances") != 0 {
		t.Fatal("self result bypassed the human independent-review requirement", result)
	}
}

func TestIssueOwnerResultCannotBorrowAnotherProducerRun(t *testing.T) {
	h, _, _, run, _ := reviewManagerAssignment(t)
	other := createCanonicalOwnerTestIssue(t, h)
	p := protocol.UpdateIssueDeliveryPayload{Action: "submit_result", TaskID: run.TaskID, RunID: run.RunID, Result: &protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "Cannot transfer this producer to another responsibility"}}, IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: other.AssignmentEpoch, Reason: "Wrong producer"}}
	op, err := h.app.Execute(t.Context(), owner, h.env("issues.update_delivery", protocol.Target{Type: "issue", ID: other.IssueID}, p, rev(other.Revision)), UpdateIssueDelivery{})
	if err == nil {
		err = op.Error
	}
	if protocol.CodeOf(err) != protocol.CodeForbidden || h.count("artifacts") != 0 {
		t.Fatal(op)
	}
}

func TestIssueOwnerResultUsesOneActualIndependentReviewerReport(t *testing.T) {
	h, runtime, issue, producer, _ := reviewManagerAssignment(t)
	if op := policyChange(t, h, issue, true); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	if op := updateDeliveryForTest(t, h, issue.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: producer.TaskID, RunID: producer.RunID, Action: "submit_result", Result: &protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "Original background, investigation target and final bounded findings"}}, IndependentAcceptance: &protocol.IndependentAcceptanceRequest{Required: true, Reason: "An independent Agent must inspect this actual result"}, IssueControlPayload: protocol.IssueControlPayload{Reason: "Register the actual owner output before its independent review"}}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	original := h.task(producer.TaskID).CurrentArtifactID
	enableOwnerResultReviewer(t, h, runtime)
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	spec := testSpec
	spec.ReviewArtifactID = original
	spec.Delivery, spec.Deliverables, spec.Workspace = "", []string{domain.DeliveryReport}, domain.Workspace{Mode: domain.WorkspaceTemporary}
	spec.AgentSelection = &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer", AgentID: "linux-1/beta"}}
	p := canonicalOwnerTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Independently inspect the original owner findings", Spec: spec, IndependentAcceptance: &protocol.IndependentAcceptanceRequest{Required: false, Reason: "This independent review reports its actual verdict once"}}
	op := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(issue.Revision)), CanonicalOwnerTaskCreate{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var reviewTask taskResult
	if err := json.Unmarshal(op.Result, &reviewTask); err != nil {
		t.Fatal(err)
	}
	reviewer, ok := h.claimInputRun(runtime)
	if !ok || reviewer.TaskID != reviewTask.TaskID || reviewer.Execution == nil || reviewer.Execution.Identity == producer.Execution.Identity {
		t.Fatal("no independent real reviewer was selected", reviewer)
	}
	if op := h.runAs(reviewer, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.runAs(reviewer, protocol.OpRunsReportWorkspace, protocol.WorkspaceReport{BackendID: reviewer.Workspace.BackendID, Path: t.TempDir(), Baseline: json.RawMessage(`{"files":[]}`)}, ReportWorkspace{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.runAs(reviewer, protocol.OpRunsSubmitArtifact, protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "Inspected original background, findings and the retained evidence; the requested outcome is satisfied.", ReviewedArtifactID: original, Verdict: protocol.VerdictPassed}}, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	reviewArtifact := h.task(reviewTask.TaskID).CurrentArtifactID
	if op := updateDeliveryForTest(t, h, issue.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: producer.TaskID, Action: "report", ReviewerArtifactID: reviewArtifact, IssueControlPayload: protocol.IssueControlPayload{Reason: "Use the actual independent report to settle this result"}}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.count("acceptances") != 0 || h.count("tasks") != 2 || h.task(reviewTask.TaskID).Lifecycle != protocol.TaskCompleted {
		t.Fatal("a report review created another review or a synthetic verdict")
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		evidence, err := store.CompletedTaskEvidence(t.Context(), tx.Q(), producer.TaskID, "Actual result and independent review")
		if err != nil {
			return err
		}
		detail, err := views.Artifact(t.Context(), tx.Q(), original)
		if err != nil {
			return err
		}
		if evidence.Reference != original || detail.ReviewerArtifactID != reviewArtifact || !detail.ResultComplete || detail.CurrentCompletion {
			t.Fatal("review lost its real original or completed the continuing owner Task", evidence, detail)
		}
		results, err := views.IssueResults(t.Context(), tx.Q(), issue.IssueID, "")
		if err != nil {
			return err
		}
		for _, result := range results.Items {
			if result.Artifact.ArtifactID == original && result.ResultComplete && result.ReviewerArtifactID == reviewArtifact && len(result.Acceptances) == 0 {
				return nil
			}
		}
		t.Fatal("the real owner result was absent or shown as awaiting another acceptance", results)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func ownerBoundDeliveryFixture(t *testing.T, deploy bool) (*harness, protocol.Issue, protocol.Assignment, protocol.IssueDeliveryCandidate, func(string)) {
	t.Helper()
	h, runtime, issue, producer, _ := reviewManagerAssignment(t)
	enableOwnerResultReviewer(t, h, runtime)
	issue.Plan.Deliveries = []protocol.IssueDelivery{{DeliveryID: "pr", Provider: "github", Repository: "example/repo", Number: 17, URL: "https://example.invalid/repo/pull/17"}}
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "update", protocol.UpdateIssuePayload{Plan: issue.Plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch}))
	issue = setPRPolicyForTest(t, h, issue.IssueID, "manual")
	register := func(body string) {
		t.Helper()
		if op := updateDeliveryForTest(t, h, issue.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: producer.TaskID, RunID: producer.RunID, Action: "submit_result", Result: &protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: body}}, IssueControlPayload: protocol.IssueControlPayload{Reason: "Register actual bounded owner execution output"}}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	register("The owner implemented and checked the original bounded change")
	candidate := protocol.IssueDeliveryCandidate{DeliveryID: "pr", URL: issue.Plan.Deliveries[0].URL, HeadSHA: strings.Repeat("a", 40), BaseSHA: strings.Repeat("b", 40), TargetSHA: strings.Repeat("b", 40), ProviderState: "open", Validation: "Original independent repository evidence", DeploymentScope: "Merge the reviewed PR only"}
	if deploy {
		candidate.Deploy = true
		candidate.DeploymentScope = "Deploy the authorized result to its original server"
		candidate.Deployment = &protocol.IssueDeliveryDeployment{ServerInstance: "server", HostIDs: []string{}}
	}
	observeDeliveryForTest(t, h, issue.IssueID, candidate, "open", "")
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		observation, err := store.GetDeliveryObservation(t.Context(), tx.Q(), issue.IssueID, "pr")
		if err != nil {
			return err
		}
		observation.Observation.CI = &protocol.DeliveryCI{HeadSHA: candidate.HeadSHA, State: "success", Checks: []protocol.DeliveryCheck{{Name: "required", State: "completed", Conclusion: "success"}}}
		return putDeliveryObservationForTest(tx, t.Context(), observation)
	}); err != nil {
		t.Fatal(err)
	}
	sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr", TaskID: producer.TaskID}))
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	reviewSpec := testSpec
	reviewSpec.ReviewDeliveryID = "pr"
	reviewSpec.Delivery, reviewSpec.Deliverables, reviewSpec.Workspace = "", []string{domain.DeliveryReport}, domain.Workspace{Mode: domain.WorkspaceTemporary}
	reviewSpec.AgentSelection = &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer", AgentID: "linux-1/beta"}}
	op := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, canonicalOwnerTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Independently review the complete PR", Spec: reviewSpec, IndependentAcceptance: &protocol.IndependentAcceptanceRequest{Required: false, Reason: "The independent formal review reports once"}}, rev(issue.Revision)), CanonicalOwnerTaskCreate{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var reviewed taskResult
	if err := json.Unmarshal(op.Result, &reviewed); err != nil {
		t.Fatal(err)
	}
	review := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "start", DeliveryID: "pr", ReviewerTaskID: reviewed.TaskID}))
	executeSharedReviewForTest(t, h, runtime, review, reviewed.TaskID, "passed")
	sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
	if op := updateDeliveryForTest(t, h, issue.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: producer.TaskID, Action: "prepare", Candidate: &candidate, ExpectedTaskControlRevision: rev(h.task(producer.TaskID).ControlRevision), IssueControlPayload: protocol.IssueControlPayload{Reason: "Present the actual owner's merge candidate"}}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	duty := deliveryDutyForTest(t, h, issue.IssueID, producer.TaskID)
	answerDeliveryForTest(t, h, duty, "approve", "")
	if op := updateDeliveryForTest(t, h, issue.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: producer.TaskID, Action: "bind", ExecutionTaskID: producer.TaskID, ExpectedTaskControlRevision: rev(h.task(producer.TaskID).ControlRevision), IssueControlPayload: protocol.IssueControlPayload{Reason: "The actual current owner performs this authorized delivery itself"}}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	return h, issue, producer, candidate, register
}

func TestIssueOwnerBoundDeliveryResultKeepsFrozenExecution(t *testing.T) {
	h, issue, producer, candidate, register := ownerBoundDeliveryFixture(t, false)
	frozen := deliveryDutyForTest(t, h, issue.IssueID, producer.TaskID)
	register("The authorized candidate has been merged and the real outcome has been checked")
	if current := deliveryDutyForTest(t, h, issue.IssueID, producer.TaskID); current.Revision != frozen.Revision || current.ExecutionTaskID != producer.TaskID || current.Status != "executing" || current.ArtifactID == frozen.ArtifactID {
		t.Fatal("a new result fact expired the existing owner's execution", frozen, current)
	}
	merged := candidate
	merged.TargetSHA = strings.Repeat("c", 40)
	var checks *protocol.DeliveryCI
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		observation, err := store.GetDeliveryObservation(t.Context(), tx.Q(), issue.IssueID, candidate.DeliveryID)
		checks = observation.Observation.CI
		return err
	}); err != nil {
		t.Fatal(err)
	}
	observeDeliveryForTest(t, h, issue.IssueID, merged, "merged", merged.TargetSHA)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		observation, err := store.GetDeliveryObservation(t.Context(), tx.Q(), issue.IssueID, candidate.DeliveryID)
		if err != nil {
			return err
		}
		observation.Observation.CI = checks // Same actual head, original check evidence.
		return putDeliveryObservationForTest(tx, t.Context(), observation)
	}); err != nil {
		t.Fatal(err)
	}
	if op := updateDeliveryForTest(t, h, issue.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: producer.TaskID, Action: "complete", IssueControlPayload: protocol.IssueControlPayload{Reason: "Settle the actual merge with its original authority and final owner result"}}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.count("tasks") != 2 || h.count("runs") != 2 || h.count("acceptances") != 0 {
		t.Fatal("direct owner delivery invented a worker Task or verdict")
	}
	settled := deliveryDutyForTest(t, h, issue.IssueID, producer.TaskID)
	oldArtifact := h.task(producer.TaskID).CurrentArtifactID
	register("The same owner continues the unchanged Issue goal with another bounded result")
	current := deliveryDutyForTest(t, h, issue.IssueID, producer.TaskID)
	if current.Status != "preparing" || current.Revision <= settled.Revision || current.Candidate != nil || current.Authorization != nil || current.ExecutionTaskID != "" || current.ArtifactID == oldArtifact {
		t.Fatal("new result inherited the settled candidate or authority", current)
	}
	if h.count("issue_delivery_execution_history") != 1 || h.count("issue_delivery_executions") != 0 {
		t.Fatal("settled binding was not archived before recording the new result")
	}
	read, err := h.app.ReadIssueMaterials(t.Context(), issue.IssueID, protocol.IssueMaterialsQuery{Kind: "delivery_authorization", ID: producer.RunID})
	if err != nil || read.DeliveryAuthorization == nil || !read.DeliveryAuthorization.Historical || read.DeliveryAuthorization.Valid || read.DeliveryAuthorization.Settlement.ArtifactID != oldArtifact || read.DeliveryAuthorization.RunID != producer.RunID {
		t.Fatal("old actual Run result lost its historical identity or became new authority", read, err)
	}
	// The provider is now merged. Use its current facts, not the old open-PR
	// snapshot used by the original probe, which correctly fails freshness.
	merged.ProviderState, merged.MergeSHA = "merged", merged.TargetSHA
	merged.Deploy = true
	merged.Deployment = &protocol.IssueDeliveryDeployment{ServerInstance: "server", HostIDs: []string{}}
	merged.DeploymentScope = "Prepare a separately authorized installation of the already merged result"
	if op := updateDeliveryForTest(t, h, issue.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: producer.TaskID, Action: "prepare", Candidate: &merged, ExpectedTaskControlRevision: rev(h.task(producer.TaskID).ControlRevision), IssueControlPayload: protocol.IssueControlPayload{Reason: "Continue this Issue's delivery under its existing responsibility"}}); op.Status != protocol.OpSucceeded {
		t.Fatal("settled owner delivery permanently blocked its next result", op)
	}
	next := deliveryDutyForTest(t, h, issue.IssueID, producer.TaskID)
	if next.Status != "awaiting_owner" || next.AttentionID == settled.AttentionID {
		t.Fatal("new manual candidate borrowed a historical approval", next)
	}
	bind := protocol.UpdateIssueDeliveryPayload{TaskID: producer.TaskID, Action: "bind", ExecutionTaskID: producer.TaskID, ExpectedTaskControlRevision: rev(h.task(producer.TaskID).ControlRevision), IssueControlPayload: protocol.IssueControlPayload{Reason: "Continue only with current actual authority"}}
	assertLiveDeliveryBindCode(t, h, issue.IssueID, bind, protocol.CodeForbidden)
	answerDeliveryForTest(t, h, next, "approve", "")
	if op := updateDeliveryForTest(t, h, issue.IssueID, bind); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	// No deployment was started. An explicit owner disposition ends that
	// optional scope without inventing installation evidence.
	endDeliveryForTest(t, h, issue.IssueID, producer.TaskID)
	register("A third immutable result in the same real owner Run")
	if h.count("issue_delivery_execution_history") != 2 || h.count("tasks") != 2 || h.count("runs") != 2 {
		t.Fatal("repeated same-Run settlements collided or manufactured execution")
	}
}

func TestIssueOwnerNewResultCannotEraseUnknownOriginalUpgrade(t *testing.T) {
	h, issue, producer, _, _ := ownerBoundDeliveryFixture(t, true)
	// The updater owns these external observations. An ended logical duty does
	// not turn an unresolved server receipt into a completed installation.
	batch := store.UpgradeRow{DeliveryTaskID: producer.TaskID, ServerInstance: "server", View: protocol.UpgradeView{UpgradeID: "owner-original-install", ReleaseID: "owner-release", Revision: 1, Status: "attention", Phase: "server_reconcile", CreatedAt: h.clock.Now(), UpdatedAt: h.clock.Now(), Hosts: []protocol.UpgradeHostView{}}}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error { return tx.PutUpgrade(t.Context(), batch) }); err != nil {
		t.Fatal(err)
	}
	frozen := deliveryDutyForTest(t, h, issue.IssueID, producer.TaskID)
	currentResult := protocol.UpdateIssueDeliveryPayload{TaskID: producer.TaskID, RunID: producer.RunID, Action: "submit_result", Result: &protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "The original installation is still being reconciled; do not repeat it.", Evidence: []string{"upgrade:" + batch.View.UpgradeID}}}, IssueControlPayload: protocol.IssueControlPayload{Reason: "Record a real progress result without replacing the active frozen execution"}}
	if op := updateDeliveryForTest(t, h, issue.IssueID, currentResult); op.Status != protocol.OpSucceeded {
		t.Fatal("original reconciliation progress could not be recorded", op)
	}
	active := deliveryDutyForTest(t, h, issue.IssueID, producer.TaskID)
	if active.Revision != frozen.Revision || active.Status != "executing" || active.ExecutionTaskID != frozen.ExecutionTaskID || !reflect.DeepEqual(active.Candidate, frozen.Candidate) || !reflect.DeepEqual(active.Authorization, frozen.Authorization) || h.count("issue_delivery_execution_history") != 0 {
		t.Fatal("progress replaced the active frozen candidate or authority", active)
	}
	endDeliveryForTest(t, h, issue.IssueID, producer.TaskID)
	ended := deliveryDutyForTest(t, h, issue.IssueID, producer.TaskID)
	previousArtifact, artifacts := h.task(producer.TaskID).CurrentArtifactID, h.count("artifacts")
	p := protocol.UpdateIssueDeliveryPayload{TaskID: producer.TaskID, RunID: producer.RunID, Action: "submit_result", Result: &protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "Continue the original Issue with the next bounded result."}}, IssueControlPayload: protocol.IssueControlPayload{Reason: "Continue only after the original external operation has settled"}}
	current := canonicalTaskIssue(t, h, issue.IssueID)
	p.ExpectedAssignmentEpoch = current.AssignmentEpoch
	op, err := h.app.Execute(t.Context(), owner, h.env("issues.update_delivery", protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(current.Revision)), UpdateIssueDelivery{})
	if err == nil {
		err = op.Error
	}
	if protocol.CodeOf(err) != protocol.CodeUnknownOutcome || h.count("artifacts") != artifacts || h.task(producer.TaskID).CurrentArtifactID != previousArtifact || h.count("issue_delivery_execution_history") != 0 || !reflect.DeepEqual(ended, deliveryDutyForTest(t, h, issue.IssueID, producer.TaskID)) {
		t.Fatal("new result erased the unknown original installation or committed partial facts", op, err)
	}
	batch.View.Status, batch.View.Phase = "completed", "completed"
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error { return tx.PutUpgrade(t.Context(), batch) }); err != nil {
		t.Fatal(err)
	}
	if op := updateDeliveryForTest(t, h, issue.IssueID, p); op.Status != protocol.OpSucceeded {
		t.Fatal("settled external operation still blocked the real owner's next result", op)
	}
	read, err := h.app.ReadIssueMaterials(t.Context(), issue.IssueID, protocol.IssueMaterialsQuery{Kind: "delivery_authorization", ID: producer.RunID})
	if err != nil || read.DeliveryAuthorization == nil || !read.DeliveryAuthorization.Historical || read.DeliveryAuthorization.Valid || read.DeliveryAuthorization.DutyStatus != "ended" || read.DeliveryAuthorization.UpgradeID != batch.View.UpgradeID || read.DeliveryAuthorization.Settlement.ArtifactID != previousArtifact || read.DeliveryAuthorization.Settlement.Eligible {
		t.Fatal("original ended installation lost its identity or became completion/execution authority", read, err)
	}
}

func enableOwnerResultReviewer(t *testing.T, h *harness, runtime *protocol.RunnerRuntime) {
	t.Helper()
	// Advertise a different, already registered real Agent on the same runner.
	// Its identity is frozen by the ordinary claim, not written onto a result.
	var inventory store.AgentInventoryRow
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		inventory, err = store.GetAgentInventory(t.Context(), tx.Q(), "linux-1")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	a := inventory.Projection.Agents[1]
	runtime.AgentExecutors = append(runtime.AgentExecutors, protocol.AgentExecutor{Available: true, Pin: protocol.RunExecutionPin{Identity: a.Identity, AgentRevision: a.Revision, Runtime: a.Binding, InventoryRevision: inventory.Receipt.LocalRevision, SnapshotID: inventory.Receipt.SnapshotID, PublicSHA256: inventory.Receipt.PublicSHA256, Profile: protocol.ExecutionProfile{Model: a.RequestedModel, Effort: a.RequestedEffort}, Worker: protocol.WorkerAppServer, BackendID: "git"}})
	runtime.Sequence++
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}

func TestExplicitReviewTargetDoesNotRecursivelyInheritButKeepsExtraReview(t *testing.T) {
	h, _, issue, producer, _ := reviewManagerAssignment(t)
	if op := policyChange(t, h, issue, true); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := updateDeliveryForTest(t, h, issue.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: producer.TaskID, RunID: producer.RunID, Action: "submit_result", Result: &protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "Original findings requiring an actual independent report"}}, IssueControlPayload: protocol.IssueControlPayload{Reason: "Record the original owner findings"}}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	original := h.task(producer.TaskID).CurrentArtifactID
	for _, extra := range []bool{false, true} {
		issue = canonicalTaskIssue(t, h, issue.IssueID)
		spec := testSpec
		spec.ReviewArtifactID, spec.Delivery, spec.Deliverables = original, "", []string{domain.DeliveryReport}
		p := canonicalOwnerTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Review the original findings", Spec: spec}
		if extra {
			p.IndependentAcceptance = &protocol.IndependentAcceptanceRequest{Required: true, Reason: "The user explicitly requires an additional acceptance of this review report itself"}
		}
		op := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(issue.Revision)), CanonicalOwnerTaskCreate{})
		if op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		var created taskResult
		if err := json.Unmarshal(op.Result, &created); err != nil {
			t.Fatal(err)
		}
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			policy, err := store.TaskAcceptancePolicy(t.Context(), tx.Q(), created.TaskID)
			if err == nil && (policy == nil || policy.Required != extra || policy.RequestID != op.RequestID) {
				t.Fatal("review target lost its own frozen quality agreement", extra, policy)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
}
