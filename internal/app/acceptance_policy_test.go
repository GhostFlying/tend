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

func policyIssue(t *testing.T, h *harness, required bool) protocol.Issue {
	t.Helper()
	p := protocol.CreateIssuePayload{Plan: protocol.IssuePlan{Title: "Bounded investigation", Project: "default", Intent: "Record findings without inventing code changes"}, IndependentAcceptance: &protocol.IndependentAcceptanceRequest{Required: required, Reason: "Creator selected the review needed for this investigation"}}
	op := h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, p, nil), CanonicalOwnerIssueCommand{Action: "create"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var issue protocol.Issue
	if err := json.Unmarshal(op.Result, &issue); err != nil {
		t.Fatal(err)
	}
	return admitExistingIssueFixture(t, h, issue)
}
func policyReportTask(t *testing.T, h *harness, issue protocol.Issue) string {
	t.Helper()
	spec := testSpec
	spec.Delivery = ""
	spec.Deliverables = []string{domain.DeliveryReport}
	op := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, canonicalOwnerTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Read-only findings", Spec: spec}, rev(issue.Revision)), CanonicalOwnerTaskCreate{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var result struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(op.Result, &result); err != nil {
		t.Fatal(err)
	}
	return result.TaskID
}
func policyChange(t *testing.T, h *harness, issue protocol.Issue, required bool) protocol.Operation {
	t.Helper()
	return h.exec(owner, h.env("issues.set_acceptance_policy", protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.SetIssueAcceptancePolicyPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, IndependentAcceptanceRequest: protocol.IndependentAcceptanceRequest{Required: required, Reason: "Creator reviewed the future work requirements"}}, rev(issue.Revision)), SetIssueAcceptancePolicy{})
}
func TestCreatorPolicyReportCompletesWithHonestAtomicNotice(t *testing.T) {
	h := newDeliverableHarness(t)
	issue := policyIssue(t, h, false)
	task := policyReportTask(t, h, issue)
	// Later policy changes do not alter the creation snapshot of this task.
	if op := policyChange(t, h, canonicalTaskIssue(t, h, issue.IssueID), true); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	as, ok, _ := h.claimDeliverableRun()
	if !ok || as.TaskID != task {
		t.Fatal(as)
	}
	env := h.env(protocol.OpRunsSubmitArtifact, runTarget(as.RunID), protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "# Findings\nNo code changes were needed.", Evidence: []string{"Inspected both paths"}}}, nil)
	p := runPrincipal(as.RunID, as.Role)
	first := h.exec(p, env, SubmitArtifact{})
	if first.Status != protocol.OpSucceeded {
		t.Fatal(first)
	}
	replay := h.exec(p, env, SubmitArtifact{})
	if replay.Error == nil || replay.Error.Code != protocol.CodeInvalidTransition {
		t.Fatal("ended Run acquired a new result mutation", replay)
	}
	if h.task(task).Lifecycle != protocol.TaskCompleted || h.count("acceptances") != 0 {
		t.Fatal("report was not completed honestly", h.task(task))
	}
	artifactID := h.task(task).CurrentArtifactID
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		detail, err := views.Artifact(t.Context(), tx.Q(), artifactID)
		if err != nil {
			return err
		}
		if !detail.CurrentCompletion || detail.AcceptancePolicy == nil || detail.AcceptancePolicy.Required || len(detail.Acceptances) != 0 {
			t.Fatal("artifact read lost the frozen completion basis", detail)
		}
		results, err := views.IssueResults(t.Context(), tx.Q(), issue.IssueID, "")
		if err != nil {
			return err
		}
		if len(results.Items) != 1 || !reflect.DeepEqual(results.Items[0].AcceptancePolicy, detail.AcceptancePolicy) || !results.Items[0].CurrentCompletion {
			t.Fatal("Issue result used the current Issue policy instead of the Task snapshot", results)
		}
		var count int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT count(*) FROM plan_actions WHERE task_id=? AND kind='accept'`, task).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatal("waived report created acceptance work")
		}
		reviews, err := store.ListPendingCompletionReviews(t.Context(), tx.Q(), issue.IssueID)
		if err != nil {
			return err
		}
		if len(reviews) != 1 || reviews[0].AcceptanceID != "" || reviews[0].ArtifactID != artifactID {
			t.Fatal(reviews)
		}
		messages, err := store.ListConversation(t.Context(), tx.Q(), 0, 0, 100)
		if err != nil {
			return err
		}
		if len(messages) != 1 || !strings.Contains(messages[0].Body, "不要求独立验收") || strings.Contains(messages[0].Body, "独立验收通过") {
			t.Fatal(messages)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Historical result facts do not inherit completion from the current artifact.
	historicalArtifact := h.ids.New(protocol.PrefixArtifact)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		return tx.InsertArtifact(t.Context(), historicalArtifact, task, as.RunID, domain.DeliveryReport, `{"report":{"body":"Historical result fact"}}`, "historical-digest", h.clock.Now().UnixMilli())
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		detail, err := views.Artifact(t.Context(), tx.Q(), historicalArtifact)
		if err == nil && (detail.CurrentCompletion || detail.AcceptancePolicy == nil || detail.AcceptancePolicy.Required) {
			t.Fatal("a historical artifact inherited current completion", detail)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	current := canonicalTaskIssue(t, h, issue.IssueID)
	second := policyReportTask(t, h, current)
	next, ok, _ := h.claimDeliverableRun()
	if !ok || next.TaskID != second {
		t.Fatal(next)
	}
	if op := h.runAs(next, protocol.OpRunsSubmitArtifact, protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "Second findings"}}, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.task(second).Lifecycle != protocol.TaskAwaitingAcceptance {
		t.Fatal("required future task lost acceptance")
	}
	requiredArtifact := h.task(second).CurrentArtifactID
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		detail, err := views.Artifact(t.Context(), tx.Q(), requiredArtifact)
		if err == nil && (detail.CurrentCompletion || detail.AcceptancePolicy == nil || !detail.AcceptancePolicy.Required) {
			t.Fatal("required unfinished review was displayed as waived completion", detail)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCreatorPolicyMissingIssueRecordsAndReplaysNotFound(t *testing.T) {
	h := newHarness(t)
	payload := protocol.SetIssueAcceptancePolicyPayload{ExpectedAssignmentEpoch: 1, IndependentAcceptanceRequest: protocol.IndependentAcceptanceRequest{Required: false, Reason: "A bounded investigation"}}
	env := h.env("issues.set_acceptance_policy", protocol.Target{Type: "issue", ID: "wi_00000000000000000000000001"}, payload, rev(1))
	first := h.exec(owner, env, SetIssueAcceptancePolicy{})
	if first.Status != protocol.OpFailed || first.Error == nil || first.Error.Code != protocol.CodeNotFound {
		t.Fatal(first)
	}
	second := h.exec(owner, env, SetIssueAcceptancePolicy{})
	if !reflect.DeepEqual(first, second) {
		t.Fatal("deterministic failure did not replay", first, second)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var count int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM sqlite_schema WHERE name IN ('request_records','native_requests')`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatalf("generic request storage still present: %d", count)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCreatorPolicyRejectsWorkerAssigneeAndStaleChanges(t *testing.T) {
	h := newHarness(t)
	issue := policyIssue(t, h, true)
	payload := protocol.SetIssueAcceptancePolicyPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, IndependentAcceptanceRequest: protocol.IndependentAcceptanceRequest{Required: false, Reason: "attempt"}}
	env := h.env("issues.set_acceptance_policy", protocol.Target{Type: "issue", ID: issue.IssueID}, payload, rev(issue.Revision))
	if _, err := h.app.Execute(t.Context(), runPrincipal("run_attempt", protocol.RoleImplement), env, SetIssueAcceptancePolicy{}); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal(err)
	}
	first := h.exec(owner, env, SetIssueAcceptancePolicy{})
	if first.Status != protocol.OpSucceeded {
		t.Fatal(first)
	}
	replay := h.exec(owner, env, SetIssueAcceptancePolicy{})
	if replay.Error == nil || replay.Error.Code != protocol.CodeStaleRevision {
		t.Fatal("old policy CAS changed current facts", replay)
	}
	stale := policyChange(t, h, issue, true)
	if stale.Error == nil || stale.Error.Code != protocol.CodeStaleRevision {
		t.Fatal(stale)
	}
}

func TestCreatorPolicyNamedOwnerHasNoReviewChildButRetainsExecutionPin(t *testing.T) {
	h, r, issue := issueOwnerHarness(t)
	if op := policyChange(t, h, issue, false); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	before := h.count("issue_controls")
	spec := testSpec
	spec.AgentSelection = &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer"}}
	payload := canonicalOwnerTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Only an implementation adapter is needed", Spec: spec}
	op := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, payload, rev(issue.Revision)), CanonicalOwnerTaskCreate{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var result struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(op.Result, &result); err != nil {
		t.Fatal(err)
	}
	if h.count("issue_controls") != before || h.count("task_owner_contracts") != 0 || h.count("creator_task_owner_contracts") != 1 {
		t.Fatal("waived task manufactured review responsibility")
	}
	as := ownerWorkerClaim(t, h, r, protocol.RoleImplement)
	if as.Execution == nil || as.Execution.Identity.LocalID != "alpha" {
		t.Fatal("lost owner pin", as)
	}
	if op := h.runAs(as, protocol.OpRunsSubmitArtifact, testArtifact(h.task(result.TaskID).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.task(result.TaskID).Lifecycle != protocol.TaskCompleted {
		t.Fatal("owner result did not finish")
	}
	if op := policyChange(t, h, canonicalTaskIssue(t, h, issue.IssueID), true); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	current := canonicalTaskIssue(t, h, issue.IssueID)
	payload.ExpectedAssignmentEpoch = current.AssignmentEpoch
	rejected := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, payload, rev(current.Revision)), CanonicalOwnerTaskCreate{})
	if rejected.Error == nil || rejected.Error.Code != protocol.CodeInvalidRequest {
		t.Fatal("required policy admitted an absent reviewer", rejected)
	}
}

func TestCreatorPolicyHostedSourceIsServerFrozen(t *testing.T) {
	h := hostedManagerHarness(t)
	source, delivery := h.say("Investigate this read-only goal without a redundant reviewer")
	turn := startHostedReview(t, h, "main", delivery)
	payload := delegationPayload(source)
	payload.ExpectedSourceRevision = rev(1)
	payload.IndependentAcceptance = &protocol.IndependentAcceptanceRequest{Required: false, Reason: "A bounded read-only investigation with a durable report is sufficient"}
	op := hostedEffect(t, h, mainAgent, turn, 1, protocol.Target{Type: "issue"}, nil, payload, CanonicalOwnerIssueCommand{Action: "create"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var issue protocol.Issue
	if err := json.Unmarshal(op.Result, &issue); err != nil {
		t.Fatal(err)
	}
	if issue.AcceptancePolicy == nil || issue.AcceptancePolicy.Required || issue.AcceptancePolicy.Creator.Kind != "agent" || issue.AcceptancePolicy.Creator.Agent == nil || issue.AcceptancePolicy.Creator.OwnerSubject != "" {
		t.Fatal(issue.AcceptancePolicy)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		execution, err := store.GetHostedExecution(t.Context(), tx.Q(), turn.RequestID)
		if err != nil {
			return err
		}
		if *issue.AcceptancePolicy.Creator.Agent != execution.Selection.Identity {
			t.Fatal("creator came from payload rather than admitted execution")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	next := protocol.SetIssueAcceptancePolicyPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, IndependentAcceptanceRequest: protocol.IndependentAcceptanceRequest{Required: true, Reason: "Creator explicitly requires another review for future tasks"}}
	changed := hostedEffect(t, h, mainAgent, turn, 2, protocol.Target{Type: "issue", ID: issue.IssueID}, rev(issue.Revision), next, SetIssueAcceptancePolicy{})
	if changed.Status != protocol.OpSucceeded {
		t.Fatal(changed)
	}
	saved := canonicalTaskIssue(t, h, issue.IssueID)
	if !saved.AcceptancePolicy.Required || !reflect.DeepEqual(saved.AcceptancePolicy.Creator, issue.AcceptancePolicy.Creator) {
		t.Fatal(saved.AcceptancePolicy)
	}
}

func TestResponsibleAgentCanChangeOptionalReviewChoice(t *testing.T) {
	h := hostedManagerHarness(t)
	parent := assignHostedManager(t, h, policyIssue(t, h, true))
	turn, creator := startHostedIssueReview(t, h, parent)
	payload := protocol.CreateIssuePayload{ParentIssueID: parent.IssueID, ExpectedParentRevision: rev(parent.Revision), Plan: protocol.IssuePlan{Title: "Read-only child", Project: "default", Intent: "Publish bounded findings"}, IndependentAcceptance: &protocol.IndependentAcceptanceRequest{Required: false, Reason: "The creating manager requires a report without redundant review"}}
	created := hostedEffect(t, h, creator, turn, 1, protocol.Target{Type: "issue"}, nil, payload, CanonicalOwnerIssueCommand{Action: "create"})
	if created.Status != protocol.OpSucceeded {
		t.Fatal(created)
	}
	var child protocol.Issue
	if err := json.Unmarshal(created.Result, &child); err != nil {
		t.Fatal(err)
	}
	change := protocol.SetIssueAcceptancePolicyPayload{ExpectedAssignmentEpoch: child.AssignmentEpoch, IndependentAcceptanceRequest: protocol.IndependentAcceptanceRequest{Required: true, Reason: "The creator now requires review for future tasks"}}
	changed := hostedEffect(t, h, creator, turn, 2, protocol.Target{Type: "issue", ID: child.IssueID}, rev(child.Revision), change, SetIssueAcceptancePolicy{})
	if changed.Status != protocol.OpSucceeded {
		t.Fatal(changed)
	}
	child = canonicalTaskIssue(t, h, child.IssueID)
	if child.Assignee.Ref != "server-hosted/planner" {
		child = assignHostedManager(t, h, child)
	}
	if child.AssignmentState != "active" {
		t.Fatal("the child has no active executor", child)
	}
	if child.BoardStatus() == protocol.IssueBacklog {
		child = admitExistingIssueFixture(t, h, child)
	}
	workerTurn, assignee := startHostedIssueReview(t, h, child)
	change.ExpectedAssignmentEpoch = child.AssignmentEpoch
	change.Required = false
	env := h.env("issues.set_acceptance_policy", protocol.Target{Type: "issue", ID: child.IssueID}, change, rev(child.Revision))
	if op, err := h.app.Execute(t.Context(), assignee, env, HostedCoordinationCommand{Command: SetIssueAcceptancePolicy{}, ExecutionID: workerTurn.RequestID, EffectSeq: 1}); err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal("responsible Agent could not revise optional review", op, err)
	}
	if canonicalTaskIssue(t, h, child.IssueID).AcceptancePolicy.Required {
		t.Fatal("optional review remained mandatory")
	}
}

func TestCreatorPolicyRequiresExplicitChoiceAndPreservesPendingAcceptance(t *testing.T) {
	h := newDeliverableHarness(t)
	for _, choice := range []any{map[string]any{"reason": "missing required"}, map[string]any{"required": nil, "reason": "null required"}} {
		p := map[string]any{"plan": protocol.IssuePlan{Title: "No implicit waiver", Project: "default", Intent: "Record explicit choice"}, "independent_acceptance": choice}
		op := h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, p, nil), CanonicalOwnerIssueCommand{Action: "create"})
		if op.Error == nil || op.Error.Code != protocol.CodeInvalidRequest {
			t.Fatal(op)
		}
	}
	issue := policyIssue(t, h, true)
	task := policyReportTask(t, h, issue)
	as, ok, _ := h.claimDeliverableRun()
	if !ok {
		t.Fatal(as)
	}
	if op := h.runAs(as, protocol.OpRunsSubmitArtifact, protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "Current investigation"}}, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := policyChange(t, h, canonicalTaskIssue(t, h, issue.IssueID), false); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.task(task).Lifecycle != protocol.TaskAwaitingAcceptance {
		t.Fatal("policy cancelled old acceptance")
	}
	review, ok, _ := h.claimDeliverableRun()
	if !ok || review.Role != protocol.RoleAccept {
		t.Fatal("frozen acceptance disappeared", review)
	}
	if op := h.runAs(review, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.count("acceptances") != 1 {
		t.Fatal("old real verdict was rewritten")
	}
}

func TestFault_CreatorPolicyNoticeFailureRollsBackReport(t *testing.T) {
	h := newDeliverableHarness(t)
	issue := policyIssue(t, h, false)
	task := policyReportTask(t, h, issue)
	as, ok, _ := h.claimDeliverableRun()
	if !ok {
		t.Fatal(as)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `CREATE TRIGGER reject_waived_notice BEFORE INSERT ON conversation_messages WHEN NEW.author='system' BEGIN SELECT RAISE(ABORT,'notice unavailable'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	env := h.env(protocol.OpRunsSubmitArtifact, runTarget(as.RunID), protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "Evidence must remain atomic"}}, nil)
	if _, err := h.app.Execute(t.Context(), runPrincipal(as.RunID, as.Role), env, SubmitArtifact{}); err == nil {
		t.Fatal("notice failure was ignored")
	}
	if h.task(task).Lifecycle == protocol.TaskCompleted || h.count("artifacts") != 0 || h.count("artifact_completion_reviews") != 0 || h.count("issue_delivery_duties") != 0 {
		t.Fatal("failed completion leaked facts")
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `DROP TRIGGER reject_waived_notice`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if op := h.exec(runPrincipal(as.RunID, as.Role), env, SubmitArtifact{}); op.Status != protocol.OpSucceeded || h.task(task).Lifecycle != protocol.TaskCompleted {
		t.Fatal(op)
	}
	duty := deliveryDutyForTest(t, h, issue.IssueID, task)
	if duty.AcceptanceID != "" || duty.ArtifactID == "" || duty.Status != "preparing" {
		t.Fatal(duty)
	}
	if op := updateDeliveryForTest(t, h, issue.IssueID, protocol.UpdateIssueDeliveryPayload{TaskID: task, Action: "report", IssueControlPayload: protocol.IssueControlPayload{Reason: "Bounded report is the actual requested delivery"}}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	duty = deliveryDutyForTest(t, h, issue.IssueID, task)
	if duty.Status != "report" || len(duty.Evidence) != 1 || duty.Evidence[0].Kind != protocol.WorkItemEvidenceResult {
		t.Fatal(duty)
	}
}

func TestCreatorPolicyResultEvidenceCannotPretendAcceptanceOrCrossIssue(t *testing.T) {
	h := newDeliverableHarness(t)
	issue := policyIssue(t, h, false)
	other := policyIssue(t, h, false)
	task := policyReportTask(t, h, issue)
	as, ok, _ := h.claimDeliverableRun()
	if !ok {
		t.Fatal(as)
	}
	if op := h.runAs(as, protocol.OpRunsSubmitArtifact, protocol.ArtifactPayload{Report: &protocol.ResultReport{Body: "The actual scoped report"}}, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	artifact := h.task(task).CurrentArtifactID
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		x := &Exec{Principal: owner}
		for _, tc := range []struct {
			issue string
			kind  protocol.IssueEvidenceKind
			valid bool
		}{{issue.IssueID, protocol.WorkItemEvidenceResult, true}, {other.IssueID, protocol.WorkItemEvidenceResult, false}, {issue.IssueID, protocol.WorkItemEvidenceAcceptance, false}} {
			err := validateIssueEvidenceReferences(t.Context(), x, tx.Q(), protocol.WorkItem{WorkItemID: tc.issue}, []protocol.IssueEvidence{{Kind: tc.kind, Reference: artifact, Summary: "Frozen completed result"}}, nil, "evidence")
			if (err == nil) != tc.valid {
				t.Fatalf("scope=%s kind=%s err=%v", tc.issue, tc.kind, err)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCreatorPolicyAgentQuestionViewKeepsPauseAndRejectsReplacedEpoch(t *testing.T) {
	h, runtime, issue := issueOwnerHarness(t)
	if op := policyChange(t, h, issue, false); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityWorkerQuestions)
	runtime.Sequence++
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	spec := testSpec
	spec.AgentSelection = &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer"}}
	payload := canonicalOwnerTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Investigate under the creator policy", Spec: spec}
	if op := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, payload, rev(issue.Revision)), CanonicalOwnerTaskCreate{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	as := ownerWorkerClaim(t, h, runtime, protocol.RoleImplement)
	if as.Execution == nil || !as.WorkerQuestions {
		t.Fatalf("missing frozen worker/question capability: %+v", as)
	}
	if op := h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	question := askQuestion(t, h, as, scopeQuestion)
	if h.count("creator_task_owner_contracts") != 1 || h.count("task_owner_contracts") != 0 {
		t.Fatal("fixture did not use the waived contract")
	}
	read := func() protocol.AgentIssueRun {
		t.Helper()
		var result protocol.AgentIssueRun
		found := false
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			page, err := views.AgentIssues(t.Context(), tx.Q(), views.AgentIssueFilter{Identity: as.Execution.Identity})
			if err != nil {
				return err
			}
			for _, item := range page.Items {
				for _, run := range item.Runs {
					if run.Run.RunID == as.RunID {
						result, found = run, true
					}
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if !found {
			t.Fatal("lost original frozen Run association")
		}
		return result
	}
	if got := read(); !got.PendingDecision || got.TaskOpenQuestions != 1 {
		t.Fatalf("original question: %+v", got)
	}
	before := h.task(as.TaskID)
	if op := h.exec(owner, h.env("control.set_mode", taskTarget(as.TaskID), protocol.SetControlModePayload{Mode: protocol.ControlPaused}, rev(before.ControlRevision)), SetControlMode{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.task(as.TaskID).ControlRevision <= before.ControlRevision {
		t.Fatal("pause did not exercise a changed control revision")
	}
	if got := read(); !got.PendingDecision || got.Pin.Identity != as.Execution.Identity {
		t.Fatalf("pause invalidated original responsibility: %+v", got)
	}
	// Keep the old question and Run as historical facts while simulating an
	// independently persisted successor ownership epoch, not an owner answer.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE issue_controls SET assignment_epoch=assignment_epoch+1 WHERE issue_id=?`, issue.IssueID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got := read(); got.PendingDecision || got.TaskOpenQuestions != 1 {
		t.Fatalf("replaced responsibility masqueraded as actionable: %+v", got)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		d, err := store.GetDecision(t.Context(), tx.Q(), question.DecisionID)
		if err == nil && d.Status != store.DecisionOpen {
			t.Fatal("read changed the original question")
		}
		policy, policyErr := store.TaskAcceptancePolicy(t.Context(), tx.Q(), as.TaskID)
		if policyErr != nil {
			return policyErr
		}
		if policy == nil || policy.Required {
			t.Fatal("view changed the frozen creator waiver")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCreatorPolicyHistoricalClassificationDoesNotAuthorizeWaivedIssue(t *testing.T) {
	h := hostedManagerHarness(t)
	source, previousDelivery := h.say("Historical material may be read, not reused as a new owner instruction")
	previous := startHostedReview(t, h, "main", previousDelivery)
	finishHostedReview(t, h, previous)
	_, currentDelivery := h.say("Read the existing ledger only")
	current := startHostedReview(t, h, "main", currentDelivery)
	association := h.associations(source)
	classified := hostedEffect(t, h, mainAgent, current, 1, protocol.Target{Type: "message", ID: source}, rev(association.Revision), protocol.SetIssueMessageAssociationsPayload{Disposition: "unassigned", ContentRevision: association.ContentRevision}, SetIssueMessageAssociations{})
	if classified.Status != protocol.OpSucceeded {
		t.Fatal(classified)
	}
	payload := delegationPayload(source)
	payload.ExpectedSourceRevision = rev(association.ContentRevision)
	payload.IndependentAcceptance = &protocol.IndependentAcceptanceRequest{Required: false, Reason: "A legal durable report does not need a separate reviewer"}
	beforeIssues, beforePolicies, beforeTasks := h.count("issue_controls"), h.count("issue_acceptance_policies"), h.count("tasks")
	cmd := HostedCoordinationCommand{Command: CanonicalOwnerIssueCommand{Action: "create"}, ExecutionID: current.RequestID, EffectSeq: 2}
	op, err := h.app.Execute(t.Context(), mainAgent, h.env(cmd.Operation(), protocol.Target{Type: "issue"}, payload, nil), cmd)
	if protocol.CodeOf(err) != protocol.CodeForbidden && (op.Error == nil || op.Error.Code != protocol.CodeForbidden) {
		t.Fatal("historical review membership authorized a waived Issue", op, err)
	}
	if h.count("issue_controls") != beforeIssues || h.count("issue_acceptance_policies") != beforePolicies || h.count("tasks") != beforeTasks {
		t.Fatal("source rejection leaked an Issue, creator policy or Task")
	}
}

func TestCreatorPolicyWaiverDoesNotRestoreRevokedInputAuthority(t *testing.T) {
	h := hostedManagerHarness(t)
	source := createCanonicalOwnerTestIssue(t, h)
	task := createCanonicalTaskForTest(t, h, source)
	producer, ok, op := h.claim("linux-1")
	if !ok || producer.TaskID != task {
		t.Fatal(producer, op)
	}
	file, _ := h.uploadResultFile(producer, "evidence.txt", []byte("original immutable evidence"))
	artifactPayload := testArtifact(h.task(task).WorkspaceName)
	artifactPayload.Files = []protocol.ArtifactFile{file}
	if op := h.runAs(producer, protocol.OpRunsSubmitArtifact, artifactPayload, SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	artifactID := h.task(task).CurrentArtifactID
	accept, ok, op := h.claim("linux-1")
	if !ok || accept.Role != protocol.RoleAccept {
		t.Fatal(accept, op)
	}
	if op := h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	target := assignHostedManager(t, h, policyIssue(t, h, false))
	var artifact store.ArtifactRow
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		artifact, err = store.GetArtifact(t.Context(), tx.Q(), artifactID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	granted := h.exec(owner, h.env(protocol.OpGrantArtifactShare, protocol.Target{Type: "issue", ID: target.IssueID}, protocol.GrantArtifactSharePayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: target.AssignmentEpoch, Reason: "Allow exact report input"}, ArtifactID: artifactID, RefSHA256: store.ArtifactRefSHA256(artifact.RefJSON)}, rev(target.Revision)), ArtifactShareCommand{})
	var share protocol.ArtifactShare
	if granted.Status != protocol.OpSucceeded || json.Unmarshal(granted.Result, &share) != nil {
		t.Fatal(granted)
	}
	target = canonicalTaskIssue(t, h, target.IssueID)
	revoked := h.exec(owner, h.env(protocol.OpRevokeArtifactShare, protocol.Target{Type: "issue", ID: target.IssueID}, protocol.RevokeArtifactSharePayload{IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: target.AssignmentEpoch, Reason: "Revoke new original input access"}, ShareID: share.ShareID, ExpectedShareRevision: share.Revision}, rev(target.Revision)), ArtifactShareCommand{Revoke: true})
	if revoked.Status != protocol.OpSucceeded {
		t.Fatal(revoked)
	}
	target = canonicalTaskIssue(t, h, target.IssueID)
	turn, manager := startHostedIssueReview(t, h, target)
	spec := testSpec
	spec.Inputs = []protocol.TaskInput{{FileID: file.FileID}}
	command := HostedCoordinationCommand{Command: CanonicalOwnerTaskCreate{}, ExecutionID: turn.RequestID, EffectSeq: 1}
	payload := canonicalOwnerTaskPayload{ExpectedAssignmentEpoch: target.AssignmentEpoch, Title: "Waiver does not grant source access", Spec: spec, Rating: protocol.RatingP1}
	request := h.env(command.Operation(), protocol.Target{Type: "issue", ID: target.IssueID}, payload, rev(target.Revision))
	tasks, specs, contracts := h.count("tasks"), h.count("task_specs"), h.count("creator_task_owner_contracts")
	denied := h.exec(manager, request, command)
	expectWorkCode(t, denied, protocol.CodeInvalidRequest)
	if replay := h.exec(manager, request, command); !reflect.DeepEqual(denied, replay) {
		t.Fatal("waived input rejection replay changed", denied, replay)
	}
	if h.count("tasks") != tasks || h.count("task_specs") != specs || h.count("creator_task_owner_contracts") != contracts {
		t.Fatal("revoked input left a partial waived contract")
	}
	payload.Spec.Inputs = nil
	command.EffectSeq++
	lawful := h.exec(manager, h.env(command.Operation(), protocol.Target{Type: "issue", ID: target.IssueID}, payload, rev(target.Revision)), command)
	if lawful.Status != protocol.OpSucceeded {
		t.Fatal("lawful waived task was rejected", lawful)
	}
	var created struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(lawful.Result, &created); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		policy, err := store.TaskAcceptancePolicy(t.Context(), tx.Q(), created.TaskID)
		if err == nil && (policy == nil || policy.Required) {
			t.Fatal("waived snapshot was not retained", policy)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestHumanIssueReviewSurvivesResponsibleAgentEndorsement(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, policyIssue(t, h, true))
	turn, principal := startHostedIssueReview(t, h, issue)
	change := protocol.SetIssueAcceptancePolicyPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, IndependentAcceptanceRequest: protocol.IndependentAcceptanceRequest{Required: true, Reason: "Retain the human review requirement"}}
	op := hostedEffect(t, h, principal, turn, 1, protocol.Target{Type: "issue", ID: issue.IssueID}, rev(issue.Revision), change, SetIssueAcceptancePolicy{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	if issue.AcceptancePolicy.DecidedBy.Kind != "owner" {
		t.Fatal("Agent endorsement erased the human requirement", issue.AcceptancePolicy)
	}
	change.Required = false
	change.Reason = "Agent prefers self-check"
	env := h.env("issues.set_acceptance_policy", protocol.Target{Type: "issue", ID: issue.IssueID}, change, rev(issue.Revision))
	op, err := h.app.Execute(t.Context(), principal, env, HostedCoordinationCommand{Command: SetIssueAcceptancePolicy{}, ExecutionID: turn.RequestID, EffectSeq: 2})
	if protocol.CodeOf(err) != protocol.CodeForbidden && (op.Error == nil || op.Error.Code != protocol.CodeForbidden) {
		t.Fatal("Agent waived the human requirement", op, err)
	}
}

func TestRegressionServerCancelRequiresDurableCommit(t *testing.T) {
	h, c := upgradeHarness(t)
	if op := startPreparedTestUpgrade(h, "probe-cancel", c); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	upgradeTick(h, ServerObservation{MainDrained: true, BinarySHA256: strings.Repeat("a", 64)})
	for _, host := range []string{"linux-1", "linux-2"} {
		upgradeAck(h, host, protocol.RunnerMaintenance)
	}
	b := protocol.UpgradeBinding{UpgradeID: "probe-cancel", ReleaseID: c.Manifest.ReleaseID, ManifestSHA256: c.ManifestSHA256, ArtifactSHA256: c.ServerHash, InstanceID: c.ServerInstance, ProcessInstanceID: "new-server"}
	if g, err := h.app.ServerUpgradeAuthorize(t.Context(), b); err != nil || !g.Allowed {
		t.Fatal(g, err)
	}
	upgradeTick(h, ServerObservation{InstanceID: c.ServerInstance, ProcessInstanceID: b.ProcessInstanceID, BinarySHA256: c.ServerHash, Healthy: true})
	before := upgradeRead(h)
	if before.ServerCommitted != nil || before.View.Phase != "server_checking" {
		t.Fatal("bad setup", before)
	}
	op, err := h.app.Execute(t.Context(), owner, h.env("upgrades.control", protocol.Target{Type: "upgrade", ID: b.UpgradeID}, map[string]string{"action": "cancel"}, rev(before.View.Revision)), ControlUpgrade{})
	if err == nil && op.Status == protocol.OpSucceeded {
		t.Fatalf("cancel released uncommitted candidate: phase=%s, paused=%v, control=%+v", upgradeRead(h).View.Phase, func() bool { v, _ := h.app.UpgradePausesMain(t.Context()); return v }(), upgradeCtl(h, "linux-1"))
	}
}
func TestRegressionAgentIssueRequirementIsInherited(t *testing.T) {
	h := hostedManagerHarness(t)
	source, delivery := h.say("Investigate with independent review")
	turn := startHostedReview(t, h, "main", delivery)
	payload := delegationPayload(source)
	payload.ExpectedSourceRevision = rev(1)
	payload.IndependentAcceptance = &protocol.IndependentAcceptanceRequest{Required: true, Reason: "The responsible Agent chooses independent review"}
	op := hostedEffect(t, h, mainAgent, turn, 1, protocol.Target{Type: "issue"}, nil, payload, CanonicalOwnerIssueCommand{Action: "create"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var issue protocol.Issue
	if err := json.Unmarshal(op.Result, &issue); err != nil {
		t.Fatal(err)
	}
	issue = admitExistingIssueFixture(t, h, issue)
	created := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, canonicalOwnerTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Inherited review requirement", Spec: testSpec}, rev(issue.Revision)), CanonicalOwnerTaskCreate{})
	if created.Status != protocol.OpSucceeded {
		t.Fatal(created)
	}
	var result struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(created.Result, &result); err != nil {
		t.Fatal(err)
	}
	task := result.TaskID
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		policy, err := store.TaskAcceptancePolicy(t.Context(), tx.Q(), task)
		if err == nil && (policy == nil || !policy.Required) {
			t.Errorf("explicit Agent Issue review was discarded: Issue=%+v Task=%+v", issue.AcceptancePolicy, policy)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
func TestRegressionEnableOptionalReviewCreatesUsableReviewer(t *testing.T) {
	h, r, issue := issueOwnerHarness(t)
	if op := policyChange(t, h, issue, false); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	spec := testSpec
	spec.AgentSelection = &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer"}, Acceptance: protocol.AgentRequirement{CategoryID: "reviewer"}}
	input := canonicalOwnerTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Optional review", Spec: spec}
	op := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, input, rev(issue.Revision)), CanonicalOwnerTaskCreate{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var task struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(op.Result, &task); err != nil {
		t.Fatal(err)
	}
	change := protocol.UpdateIssueTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, IndependentAcceptance: &protocol.IndependentAcceptanceRequest{Required: true, Reason: "Now request independent review"}}
	op = h.exec(owner, h.env("issues.update_task", taskTarget(task.TaskID), change, rev(h.task(task.TaskID).ControlRevision)), CanonicalOwnerTaskUpdate{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	as := ownerWorkerClaim(t, h, r, protocol.RoleImplement)
	if op := h.runAs(as, protocol.OpRunsSubmitArtifact, testArtifact(h.task(task.TaskID).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	accept := ownerWorkerClaim(t, h, r, protocol.RoleAccept)
	if op := h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.task(task.TaskID).Lifecycle != protocol.TaskCompleted {
		t.Fatal("requested review did not complete the original task")
	}
}

func TestRegressionDatabaseOnlyHistoryRemainsAttachable(t *testing.T) {
	h, _, _, as, _ := reviewManagerAssignment(t)
	event := protocol.ManagerNativeEvent{Sequence: 1, Body: json.RawMessage(`{"thread_id":"native","native":{"Kind":"request_usage"}}`)}
	if op := h.runAs(as, protocol.OpManagerNativeEvent, event, ManagerNativeCommand{OperationName: protocol.OpManagerNativeEvent}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.runAs(as, protocol.OpManagerNativeStopped, protocol.ManagerStoppedPayload{Reason: "original process exited", LastSequence: 1}, ManagerNativeCommand{OperationName: protocol.OpManagerNativeStopped}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.countWhere(`SELECT COUNT(*) FROM native_turn_receipts_pending`) != 0 {
		t.Fatal("fixture unexpectedly has a native receipt")
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		m, err := store.GetIssueManagerTask(t.Context(), tx.Q(), as.TaskID)
		if err != nil {
			return err
		}
		pending, err := views.ManagerReceiptPending(t.Context(), tx.Q(), m)
		if err != nil {
			return err
		}
		original, err := managerHistoryRun(t.Context(), tx.Q(), as.TaskID)
		if err != nil {
			return err
		}
		t.Logf("pending=%v history=%q run=%+v", pending, original, h.run(as.RunID))
		if pending && original == "" {
			t.Error("database-only replay lost every eligible transport")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestReviewEnableRequiresReviewerSelection(t *testing.T) {
	h, _, issue := issueOwnerHarness(t)
	spec := testSpec
	spec.AgentSelection = &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer"}}
	op := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, canonicalOwnerTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "No reviewer selector", Spec: spec}, rev(issue.Revision)), CanonicalOwnerTaskCreate{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var task taskResult
	if err := json.Unmarshal(op.Result, &task); err != nil {
		t.Fatal(err)
	}
	change := protocol.UpdateIssueTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, IndependentAcceptance: &protocol.IndependentAcceptanceRequest{Required: true, Reason: "Add a reviewer"}}
	op = h.exec(owner, h.env("issues.update_task", taskTarget(task.TaskID), change, rev(h.task(task.TaskID).ControlRevision)), CanonicalOwnerTaskUpdate{})
	if op.Status == protocol.OpSucceeded {
		t.Fatal("enabled review although stored spec fails ValidateForAcceptance(true):", spec.ValidateForAcceptance(true))
	}
}

func TestDeliveryReviewRetainsTaskChoiceAfterIssueDefaultChanges(t *testing.T) {
	for _, required := range []bool{false, true} {
		name := "self-check"
		if required {
			name = "independent-review"
		}
		t.Run(name, func(t *testing.T) {
			h := hostedManagerHarness(t)
			issue := policyIssue(t, h, required)
			task := createCanonicalTaskForTest(t, h, issue)
			issue = canonicalTaskIssue(t, h, issue.IssueID)
			if !required {
				choice := protocol.UpdateIssueTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, IndependentAcceptance: &protocol.IndependentAcceptanceRequest{Required: false, Reason: "Owner selects self-check for this task"}}
				if op := h.exec(owner, h.env("issues.update_task", taskTarget(task), choice, rev(h.task(task).ControlRevision)), CanonicalOwnerTaskUpdate{}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			check := func() {
				t.Helper()
				err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
					current, err := store.GetCanonicalIssue(t.Context(), tx.Q(), issue.IssueID)
					if err != nil {
						return err
					}
					policy, err := store.TaskAcceptancePolicy(t.Context(), tx.Q(), task)
					if err != nil {
						return err
					}
					if policy == nil || policy.Required != required {
						t.Fatalf("Task review choice changed: %+v", policy)
					}
					return store.RequireDeliveryDutyReview(t.Context(), tx.Q(), current, protocol.IssueDeliveryDuty{TaskID: task, Candidate: &protocol.IssueDeliveryCandidate{DeliveryID: "pr-original"}})
				})
				if required {
					if protocol.CodeOf(err) != protocol.CodeInvalidTransition {
						t.Fatalf("required Task lost its review gate: %v", err)
					}
				} else if err != nil {
					t.Fatalf("self-check Task acquired a review gate: %v", err)
				}
			}
			check()
			if op := policyChange(t, h, canonicalTaskIssue(t, h, issue.IssueID), !required); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			check()
		})
	}
}

func TestReviewWithdrawnBeforeResultSettlesGeneratedResponsibility(t *testing.T) {
	h, r, issue := issueOwnerHarness(t)
	op := createOwnerExecution(t, h, issue, "implementer")
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var task taskResult
	if err := json.Unmarshal(op.Result, &task); err != nil {
		t.Fatal(err)
	}
	as := ownerWorkerClaim(t, h, r, protocol.RoleImplement)
	change := protocol.UpdateIssueTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, IndependentAcceptance: &protocol.IndependentAcceptanceRequest{Required: false, Reason: "Owner withdraws optional review"}}
	op = h.exec(owner, h.env("issues.update_task", taskTarget(task.TaskID), change, rev(h.task(task.TaskID).ControlRevision)), CanonicalOwnerTaskUpdate{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	op = h.runAs(as, protocol.OpRunsSubmitArtifact, testArtifact(h.task(task.TaskID).WorkspaceName), SubmitArtifact{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.task(task.TaskID).Lifecycle != protocol.TaskCompleted {
		t.Fatal("task not completed", h.task(task.TaskID))
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		c, err := store.GetTaskOwnerContract(t.Context(), tx.Q(), task.TaskID)
		if err != nil {
			return err
		}
		child, err := store.GetCanonicalIssue(t.Context(), tx.Q(), c.ReviewIssueID)
		if err == nil && child.State != protocol.IssueCancelled {
			t.Errorf("unused review child still %s/%s", child.State, child.WorkStatus)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
