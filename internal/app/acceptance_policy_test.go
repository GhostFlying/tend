package app

import (
	"bytes"
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
	return issue
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
	if !bytes.Equal(first.Result, replay.Result) {
		t.Fatal("result replay changed")
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
		receipt, err := views.Request(t.Context(), tx.Q(), "owner", env.RequestID)
		if err == nil && (receipt.Status != protocol.OpFailed || receipt.Error == nil || receipt.Error.Code != protocol.CodeNotFound) {
			t.Fatal(receipt)
		}
		return err
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
	if !bytes.Equal(first.Result, replay.Result) {
		t.Fatal("replay changed")
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

func TestCreatorPolicyChildAssigneeCannotReplaceCreatingResponsibility(t *testing.T) {
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
	workerTurn, assignee := startHostedIssueReview(t, h, child)
	change.ExpectedAssignmentEpoch = child.AssignmentEpoch
	change.Required = false
	env := h.env("issues.set_acceptance_policy", protocol.Target{Type: "issue", ID: child.IssueID}, change, rev(child.Revision))
	if _, err := h.app.Execute(t.Context(), assignee, env, HostedCoordinationCommand{Command: SetIssueAcceptancePolicy{}, ExecutionID: workerTurn.RequestID, EffectSeq: 1}); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("the current assignee inherited creator authority", err)
	}
	if !canonicalTaskIssue(t, h, child.IssueID).AcceptancePolicy.Required {
		t.Fatal("a rejected assignee changed the policy")
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
