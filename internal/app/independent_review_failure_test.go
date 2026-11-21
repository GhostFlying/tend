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

func independentReviewFailureFixture(t *testing.T, assigneeAgent bool) (*harness, *protocol.RunnerRuntime, protocol.Issue, string) {
	t.Helper()
	h := hostedManagerHarness(t)
	runtime := testRuntime("independent-review")
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	i := createCanonicalOwnerTestIssue(t, h)
	i.Plan.Deliveries = []protocol.IssueDelivery{{DeliveryID: "pr", Provider: "github", Repository: "example/repo", Number: 17, URL: "https://example.invalid/repo/pull/17"}}
	i = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, i, "update", protocol.UpdateIssuePayload{Plan: i.Plan, ExpectedAssignmentEpoch: i.AssignmentEpoch}))
	if assigneeAgent {
		i = assignHostedManager(t, h, i)
	}
	op, task := createIndependentReviewFailureTask(t, h, i)
	if op.Status != protocol.OpSucceeded || task == "" {
		t.Fatal(op)
	}
	return h, runtime, i, task
}

func createIndependentReviewFailureTask(t *testing.T, h *harness, i protocol.Issue) (protocol.Operation, string) {
	t.Helper()
	i = canonicalTaskIssue(t, h, i.IssueID)
	spec := protocol.OwnerTaskSpec{
		Goal: "Independently review the linked PR", ReviewDeliveryID: "pr",
		Acceptance: []string{"Submit actual independent findings"}, Deliverables: []string{domain.DeliveryReport},
		Workspace: protocol.OwnerTaskWorkspace{Mode: domain.WorkspaceTemporary},
	}
	env := h.env("issues.create_task", protocol.Target{Type: "issue", ID: i.IssueID}, protocol.CreateIssueTaskPayload{
		Title: "Review the linked candidate", ExpectedAssignmentEpoch: i.AssignmentEpoch, Spec: spec,
	}, rev(i.Revision))
	op := h.exec(owner, env, CanonicalOwnerTaskCreate{})
	var res taskResult
	if op.Status == protocol.OpSucceeded && json.Unmarshal(op.Result, &res) != nil {
		t.Fatal(op)
	}
	return op, res.TaskID
}

func TestFault_IndependentReviewFailureRequiresCurrentOwnerAndLimitsRetries(t *testing.T) {
	h, runtime, issue, task := independentReviewFailureFixture(t, true)
	reason := protocol.WorkerFailureReason(&protocol.AgentFailure{Code: "server_overloaded", HTTPStatus: 503})
	var failed []string
	for n := 0; n <= independentReviewRetryLimit; n++ {
		run, ok := h.claimInputRun(runtime)
		if !ok || run.TaskID != task || run.Role != protocol.RoleImplement || run.Generation != int64(n+1) {
			t.Fatal("expected the same independent review Task", run, ok)
		}
		failed = append(failed, run.RunID)
		if started := h.runAs(run, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); started.Status != protocol.OpSucceeded {
			t.Fatal(started)
		}
		for range 2 {
			op := h.runAs(run, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: reason}, Release{})
			if op.Status != protocol.OpSucceeded || h.run(run.RunID).ObservedState != protocol.RunFailed || h.lease(run.RunID).Status != store.LeaseReleased {
				t.Fatal("failed Run did not settle once", op, run)
			}
		}
		if _, ok := h.claimInputRun(runtime); ok {
			t.Fatal("failure created an unapproved successor")
		}
		if h.count("independent_review_failures") != n+1 {
			t.Fatal("failure announcement or duplicate changes created another settlement")
		}
		if n == independentReviewRetryLimit {
			break
		}
		if notice := h.countWhere(`SELECT COUNT(*) FROM background_sources s JOIN background_batches b USING(batch_id)
			WHERE s.kind='task.independent_review_failed' AND b.work_item_id=?`, issue.IssueID); notice != n+1 {
			t.Fatal("failure did not route once to its Issue", notice)
		}
		p := protocol.CreateActionPayload{Kind: protocol.ActionImplement, Generation: int64(n + 2)}
		revision := h.task(task).ControlRevision
		if op, err := h.app.Execute(t.Context(), owner, h.env("actions.create", taskTarget(task), p, rev(revision)), CreateAction{}); protocol.CodeOf(err) != protocol.CodeForbidden {
			t.Fatal("a principal outside the named responsibility authorized a retry", op, err)
		}
		current := canonicalTaskIssue(t, h, issue.IssueID)
		turn, principal := startHostedIssueReview(t, h, current)
		cmd := HostedCoordinationCommand{Command: CreateAction{}, ExecutionID: turn.RequestID, EffectSeq: 1}
		env := h.env(cmd.Operation(), taskTarget(task), p, rev(revision))
		env.Reason = "Retry after the provider capacity incident recovered"
		if op := h.exec(principal, env, cmd); op.Status != protocol.OpSucceeded {
			t.Fatal("the current named owner could not explicitly retry", op)
		}
		if op := h.exec(principal, env, cmd); op.Error == nil || op.Error.Code != protocol.CodeDuplicateEffect {
			t.Fatal("the original decision receipt did not identify its committed effect", op)
		}
		if got := h.countWhere(`SELECT COUNT(*) FROM independent_review_failures WHERE task_id=? AND retry_action_id IS NOT NULL`, task); got != n+1 {
			t.Fatal("duplicate approval spent retry budget", got)
		}
		future := protocol.CreateActionPayload{Kind: protocol.ActionImplement, Generation: int64(n + 3)}
		if op := hostedEffect(t, h, principal, turn, 2, taskTarget(task), rev(h.task(task).ControlRevision), future, CreateAction{}); op.Error == nil || op.Error.Code != protocol.CodeInvalidTransition {
			t.Fatal("a second approval overtook the active retry", op)
		}
		turn.Status = store.TurnCompleted
		if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
			t.Fatal(err)
		}
		pending, err := h.app.InFlightDeliveries(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range pending {
			if d.RequestID == turn.RequestID {
				if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Delivered}); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if len(failed) != 4 || h.countWhere(`SELECT COUNT(*) FROM conversation_messages WHERE conversation_id='main' AND body LIKE '%重试额度已用完%'`) != 1 {
		t.Fatal("expected exactly one user escalation after three retries", failed)
	}
	p := protocol.CreateActionPayload{Kind: protocol.ActionImplement, Generation: 5}
	current := canonicalTaskIssue(t, h, issue.IssueID)
	turn, principal := startHostedIssueReview(t, h, current)
	if op := hostedEffect(t, h, principal, turn, 1, taskTarget(task), rev(h.task(task).ControlRevision), p, CreateAction{}); op.Error == nil || op.Error.Code != protocol.CodeInvalidTransition {
		t.Fatal("fourth retry was admitted", op)
	}
}

func TestFault_IndependentReviewProviderRejectionRetainsReasonAndBlocksRetry(t *testing.T) {
	h, runtime, _, task := independentReviewFailureFixture(t, false)
	run, ok := h.claimInputRun(runtime)
	if !ok || run.TaskID != task {
		t.Fatal(run, ok)
	}
	reason := protocol.WorkerFailureReason(&protocol.AgentFailure{Code: "cyberPolicy"})
	op := h.runAs(run, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: reason}, Release{})
	if op.Status != protocol.OpSucceeded || h.run(run.RunID).ObservedState != protocol.RunFailed || h.lease(run.RunID).Status != store.LeaseReleased {
		t.Fatal(op)
	}
	if op := h.requestAction(task, protocol.ActionImplement, 2); op.Error == nil || op.Error.Code != protocol.CodeInvalidTransition {
		t.Fatal("provider rejection became retryable", op)
	}
	if _, ok := h.claimInputRun(runtime); ok {
		t.Fatal("provider rejection created a successor")
	}
	if got := h.countWhere(`SELECT COUNT(*) FROM conversation_messages WHERE conversation_id='main' AND body LIKE ?`, "%"+strings.Join([]string{run.RunID, "cyberPolicy"}, "%")+"%"); got != 1 {
		t.Fatal("one owner notice must preserve the original Run and typed rejection", got)
	}
}

func TestFault_IndependentReviewReplacementCannotResetFailedObligation(t *testing.T) {
	h, runtime, i, task := independentReviewFailureFixture(t, false)
	setCandidate := func(head string) {
		t.Helper()
		err := h.store.Write(t.Context(), func(tx *store.Tx) error {
			_, err := tx.PutDeliveryObservationFact(t.Context(), i.Plan.Deliveries[0], protocol.DeliveryObservation{
				State: "open", HeadSHA: head, BaseSHA: strings.Repeat("a", 40),
			})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	setCandidate(strings.Repeat("b", 40))
	run, ok := h.claimInputRun(runtime)
	if !ok || run.TaskID != task {
		t.Fatal(run, ok)
	}
	h.clock.Advance(time.Millisecond)
	op, pending := createIndependentReviewFailureTask(t, h, i)
	if op.Status != protocol.OpSucceeded || pending == "" {
		t.Fatal(op)
	}
	reason := protocol.WorkerFailureReason(&protocol.AgentFailure{Code: "server_overloaded"})
	if op := h.runAs(run, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: reason}, Release{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if _, ok := h.claimInputRun(runtime); ok {
		t.Fatal("ordinary scheduling started a queued replacement against the failed candidate")
	}
	if op, _ := createIndependentReviewFailureTask(t, h, i); op.Error == nil || op.Error.Code != protocol.CodeInvalidTransition {
		t.Fatal("creation reset the original failure", op)
	}
	setCandidate(strings.Repeat("c", 40))
	replacement, ok := h.claimInputRun(runtime)
	if !ok || replacement.TaskID != pending || replacement.RunID == run.RunID {
		t.Fatal("a distinctly observed candidate did not permit new work", replacement, ok)
	}
}

func TestFault_IndependentReviewExplicitNewCandidateKeepsOriginalFailure(t *testing.T) {
	h, runtime, issue, task := independentReviewFailureFixture(t, false)
	observe := func(head string) {
		t.Helper()
		if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
			_, err := tx.PutDeliveryObservationFact(t.Context(), issue.Plan.Deliveries[0], protocol.DeliveryObservation{
				State: "open", HeadSHA: head, BaseSHA: strings.Repeat("a", 40),
			})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	oldHead, newHead := strings.Repeat("b", 40), strings.Repeat("c", 40)
	observe(oldHead)
	run, ok := h.claimInputRun(runtime)
	if !ok || run.TaskID != task {
		t.Fatal(run, ok)
	}
	reason := protocol.WorkerFailureReason(&protocol.AgentFailure{Code: "cyberPolicy"})
	if op := h.runAs(run, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: reason}, Release{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var spec protocol.OwnerTaskSpec
	if err := json.Unmarshal(run.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	spec.Scope = "Review the newly observed candidate for the same PR"
	change := protocol.UpdateIssueTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Spec: &spec}
	if op := h.exec(owner, h.env("issues.update_task", taskTarget(task), change, rev(h.task(task).ControlRevision)), CanonicalOwnerTaskUpdate{}); op.Error == nil || op.Error.Code != protocol.CodeInvalidTransition {
		t.Fatal("rewriting the same failed candidate admitted new work", op)
	}
	observe(newHead)
	if op := h.exec(owner, h.env("issues.update_task", taskTarget(task), change, rev(h.task(task).ControlRevision)), CanonicalOwnerTaskUpdate{}); op.Status != protocol.OpSucceeded {
		t.Fatal("explicit agreement for a verified new candidate could not be admitted", op)
	}
	observe(oldHead)
	if _, ok := h.claimInputRun(runtime); ok {
		t.Fatal("a provider rollback restored the rejected execution")
	}
	observe(newHead)
	next, ok := h.claimInputRun(runtime)
	if !ok || next.TaskID != task || next.SpecVersion != run.SpecVersion+1 || h.run(run.RunID).ObservedState != protocol.RunFailed {
		t.Fatal("a genuine new candidate rewrote the original failure", next, ok, run)
	}
}

func TestFault_IndependentReviewApprovedRetryRecordsItsActualReport(t *testing.T) {
	h, runtime, issue, source, _, _ := sharedReviewFixture(t)
	sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr", TaskID: source}))
	task := createSharedReviewerTaskForTest(t, h, issue.IssueID)
	review := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "start", DeliveryID: "pr", ReviewerTaskID: task}))
	original, ok := h.claimInputRun(runtime)
	if !ok || original.TaskID != task {
		t.Fatal(original, ok)
	}
	reason := protocol.WorkerFailureReason(&protocol.AgentFailure{Code: "server_overloaded"})
	if op := h.runAs(original, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: reason}, Release{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	approval := h.env("actions.create", taskTarget(task), protocol.CreateActionPayload{Kind: protocol.ActionImplement, Generation: 2}, rev(h.task(task).ControlRevision))
	if op := h.exec(owner, approval, CreateAction{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.exec(owner, approval, CreateAction{}); op.Error == nil || op.Error.Code != protocol.CodeDuplicateEffect {
		t.Fatal("an unknown retry response did not name its original committed action", op)
	}
	run, ok := h.claimInputRun(runtime)
	if !ok || run.TaskID != task || run.RunID == original.RunID {
		t.Fatal(run, ok)
	}
	submitSharedReviewForTest(t, h, run, review, "changes_requested")
	recorded := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
	if len(recorded.Rounds) != 1 || recorded.Rounds[0].RunID != run.RunID || recorded.Rounds[0].Result == nil ||
		len(recorded.Rounds[0].Result.Findings) != 1 || recorded.Status != "changes_requested" ||
		h.run(original.RunID).ObservedState != protocol.RunFailed {
		t.Fatal("registration lost failure history or replaced the retry's real findings", recorded)
	}
}

func TestFault_IndependentReviewHandoffRetainsOriginalRetryBudget(t *testing.T) {
	for _, tt := range []struct {
		name                 string
		approveBeforeHandoff bool
	}{
		{name: "failed original"},
		{name: "unclaimed approval", approveBeforeHandoff: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			testIndependentReviewHandoff(t, tt.approveBeforeHandoff)
		})
	}
}

func testIndependentReviewHandoff(t *testing.T, approveBeforeHandoff bool) {
	t.Helper()
	h, runtime, issue := issueOwnerHarness(t, func(p *protocol.AgentInventoryProjection) {
		p.Agents[1].Categories = []string{"reviewer", "implementer"}
	})
	issue.Plan.Deliveries = []protocol.IssueDelivery{{DeliveryID: "pr", Provider: "github", Repository: "example/repo", Number: 17, URL: "https://example.invalid/repo/pull/17"}}
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "update", protocol.UpdateIssuePayload{
		Plan: issue.Plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch,
	}))
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.PutDeliveryObservationFact(t.Context(), issue.Plan.Deliveries[0], protocol.DeliveryObservation{
			State: "open", HeadSHA: strings.Repeat("a", 40), BaseSHA: strings.Repeat("b", 40),
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	sourceOp := createOwnerExecution(t, h, issue, "implementer")
	var source taskResult
	if sourceOp.Status != protocol.OpSucceeded || json.Unmarshal(sourceOp.Result, &source) != nil {
		t.Fatal(sourceOp)
	}
	if op := h.exec(owner, h.env("control.set_mode", taskTarget(source.TaskID), protocol.SetControlModePayload{Mode: protocol.ControlPaused}, rev(h.task(source.TaskID).ControlRevision)), SetControlMode{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "link", DeliveryID: "pr", TaskID: source.TaskID}))
	create := func(current protocol.Issue, category, followUp string) (protocol.Operation, string) {
		t.Helper()
		p := protocol.CreateIssueTaskPayload{Title: "Independently review the exact PR candidate", ExpectedAssignmentEpoch: current.AssignmentEpoch,
			Spec: protocol.OwnerTaskSpec{Goal: "Review the complete candidate", ReviewDeliveryID: "pr", Acceptance: []string{"Record the immutable whole-PR report"},
				Deliverables: []string{domain.DeliveryReport}, Workspace: protocol.OwnerTaskWorkspace{Mode: domain.WorkspaceTemporary},
				AgentSelection: &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: category, AgentID: "linux-1/beta"}}},
			IndependentAcceptance: &protocol.IndependentAcceptanceRequest{Required: false, Reason: "The original independent reviewer submits the whole-PR report"},
		}
		if followUp != "" {
			p.Relations = []protocol.TaskRelation{{TaskID: followUp, Kind: protocol.RelationFollowUp}}
		}
		op, err := h.app.Execute(t.Context(), owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: current.IssueID}, p, rev(current.Revision)), CanonicalOwnerTaskCreate{})
		if err != nil {
			return protocol.Operation{Status: protocol.OpFailed, Error: protocol.Errorf(protocol.CodeOf(err), "%s", err)}, ""
		}
		var result taskResult
		if op.Status == protocol.OpSucceeded && json.Unmarshal(op.Result, &result) != nil {
			t.Fatal(op)
		}
		return op, result.TaskID
	}
	original, task := create(canonicalTaskIssue(t, h, issue.IssueID), "reviewer", "")
	if original.Status != protocol.OpSucceeded || task == "" {
		t.Fatal(original)
	}
	run := ownerWorkerClaim(t, h, runtime, protocol.RoleImplement)
	if run.TaskID != task {
		t.Fatal(run)
	}
	review := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "start", DeliveryID: "pr", ReviewerTaskID: task}))
	if review.Rounds[0].ReviewerProfile == nil {
		t.Fatal("the original Run did not freeze a reviewer profile", review)
	}
	typed := protocol.WorkerFailureReason(&protocol.AgentFailure{Code: "server_overloaded"})
	if op := h.runAs(run, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: typed}, Release{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var originalApproval string
	if approveBeforeHandoff {
		originalApproval = approveIndependentReviewWithManager(t, h, runtime, task)
	}
	current := handoffCollaboratorIssue(t, h, issue.IssueID)
	finish := protocol.FinishIssueAssignmentPayload{ExpectedAssignmentEpoch: current.AssignmentEpoch, HandoffID: current.PendingHandoffID,
		Reason: "Original review execution and physical stop are settled"}
	current = canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.finish_assignment", protocol.Target{Type: "issue", ID: issue.IssueID}, finish,
		rev(current.Revision)), HostedIssueAssignment{Finish: true}))
	if op := h.requestAction(task, protocol.ActionImplement, 2); op.Error == nil || op.Error.Code != protocol.CodeStaleRevision {
		t.Fatal("a successor acquired the original owner's Task", op)
	}
	if op, _ := create(current, "reviewer", ""); op.Error == nil || op.Error.Code != protocol.CodeInvalidTransition {
		t.Fatal("an unrelated Task restarted the original candidate", op)
	}
	if op, _ := create(current, "implementer", task); op.Error == nil || op.Error.Code != protocol.CodeForbidden {
		t.Fatal("the original reviewer identity used an unqualified execution category", op)
	}
	if !approveBeforeHandoff {
		changeSharedReviewerDefaults(t, h, runtime, protocol.ExecutionProfile{Model: "different-model", Effort: "medium"})
		if op, _ := create(current, "reviewer", task); op.Error == nil || op.Error.Code != protocol.CodeForbidden {
			t.Fatal("the same reviewer could replace the frozen profile", op)
		}
		changeSharedReviewerDefaults(t, h, runtime, *review.Rounds[0].ReviewerProfile)
	}
	retry, replacement := create(current, "reviewer", task)
	if retry.Status != protocol.OpSucceeded || replacement == "" || replacement == task {
		t.Fatal("the new current owner could not explicitly recover the original obligation", retry)
	}
	if originalApproval != "" && h.countWhere(`SELECT COUNT(*) FROM plan_actions old JOIN plan_actions replacement
		ON json_extract(replacement.trigger_json,'$.original_review_retry_action_id')=old.action_id
		WHERE old.action_id=? AND old.status IN ('superseded','cancelled')
		  AND NOT EXISTS (SELECT 1 FROM runs r WHERE r.action_id=old.action_id)`, originalApproval) != 1 {
		t.Fatal("handoff lost the superseded approval or started its original action", originalApproval)
	}
	if !approveBeforeHandoff {
		if op := h.exec(owner, h.env("tasks.cancel", taskTarget(task), map[string]any{}, rev(h.task(task).ControlRevision)), CancelTask{}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		if resource := h.resourceForTask(task); !h.releaseResource(task, resource.WorkspaceID) {
			t.Fatal("the original Task workspace was not released")
		}
	}
	// A responsibility handoff returns the Issue to backlog. Re-admit it
	// through its ordinary board control before the runner can claim work.
	moveBoardIssue(t, h, canonicalTaskIssue(t, h, current.IssueID), protocol.IssueTodo)
	as := ownerWorkerClaim(t, h, runtime, protocol.RoleImplement)
	if as.TaskID != replacement || as.RunID == run.RunID || as.Responsibility != nil && as.Responsibility.AssignmentEpoch == run.Responsibility.AssignmentEpoch {
		t.Fatal("the retry used the wrong Task or responsibility", as, run)
	}
	if !approveBeforeHandoff {
		wait := protocol.UpdateSharedDeliveryReviewPayload{Action: "wait", DeliveryID: "pr", ExpectedReviewRevision: &review.Revision}
		if op := sharedReviewAction(t, h, issue.IssueID, wait); op.Error == nil || op.Error.Code != protocol.CodeInvalidTransition {
			t.Fatal("the original Task release allowed waiting during its approved replacement Run", op)
		}
		submitSharedReviewForTest(t, h, as, review, "changes_requested")
		if op := sharedReviewAction(t, h, issue.IssueID, wait); op.Error == nil || op.Error.Code != protocol.CodeInvalidTransition {
			t.Fatal("waiting could hide the qualified reviewer's submitted result", op)
		}
		recorded := sharedReviewResult(t, sharedReviewAction(t, h, issue.IssueID, protocol.UpdateSharedDeliveryReviewPayload{Action: "record", DeliveryID: "pr"}))
		if len(recorded.Rounds) != 1 || recorded.Rounds[0].RunID != as.RunID || recorded.Rounds[0].TaskID != replacement || recorded.Status != "changes_requested" {
			t.Fatal("the valid replacement report did not settle the existing round", recorded)
		}
		if recorded.Rounds[0].ReviewerProfile == nil || *recorded.Rounds[0].ReviewerProfile != *review.Rounds[0].ReviewerProfile {
			t.Fatal("registration changed the original qualified profile", recorded)
		}
		return
	}
	if op := h.runAs(as, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: typed}, Release{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if got := h.countWhere(`SELECT COUNT(*) FROM independent_review_failures WHERE run_id=? AND root_run_id=? AND retries=1`, as.RunID, run.RunID); got != 1 {
		t.Fatal("ownership transfer reset the original retry budget", got)
	}
}

func approveIndependentReviewWithManager(t *testing.T, h *harness, runtime *protocol.RunnerRuntime, task string) string {
	t.Helper()
	retry := protocol.CreateActionPayload{Kind: protocol.ActionImplement, Generation: 2}
	op := runIndependentReviewOwnerEffect(t, h, runtime, task, retry, CreateAction{})
	var approval taskResult
	if op.Status != protocol.OpSucceeded || json.Unmarshal(op.Result, &approval) != nil || len(approval.Actions) != 1 {
		t.Fatal(op)
	}
	return approval.Actions[0].ActionID
}

func runIndependentReviewOwnerEffect(t *testing.T, h *harness, runtime *protocol.RunnerRuntime, task string, payload any, command Command) protocol.Operation {
	t.Helper()
	manager := ownerClaim(t, h, runtime)
	if manager.Role != protocol.RoleIssueManager {
		t.Fatal(manager)
	}
	delivery, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+manager.Manager.ConversationID)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	turn := startHostedReview(t, h, manager.Manager.ConversationID, delivery)
	principal := runPrincipal(manager.RunID, manager.Role)
	principal.Thread, principal.ConversationID = turn.ThreadID, manager.Manager.ConversationID
	op := hostedEffect(t, h, principal, turn, 1, taskTarget(task), rev(h.task(task).ControlRevision), payload, command)
	turn.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	if err := h.app.FinishDelivery(t.Context(), delivery, DeliveryResult{Outcome: Delivered}); err != nil {
		t.Fatal(err)
	}
	stopOwnerClaim(t, h, manager)
	return op
}

func TestFault_IndependentReviewRetryRepairsConfirmedPrelaunchFailure(t *testing.T) {
	h, runtime, issue, initial := preparationAssignment(t)
	if op := h.runAs(initial, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "stopped by tend"}, Release{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	issue.Plan.Deliveries = []protocol.IssueDelivery{{DeliveryID: "pr", Provider: "github", Repository: "example/repo", Number: 17, URL: "https://example.invalid/repo/pull/17"}}
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "update", protocol.UpdateIssuePayload{
		Plan: issue.Plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch,
	}))
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.PutDeliveryObservationFact(t.Context(), issue.Plan.Deliveries[0], protocol.DeliveryObservation{
			State: "open", HeadSHA: strings.Repeat("a", 40), BaseSHA: strings.Repeat("b", 40),
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var spec protocol.OwnerTaskSpec
	if err := json.Unmarshal(initial.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	spec.AgentSelection = &protocol.TaskAgentSelection{
		Implementation: protocol.AgentRequirement{CategoryID: "implementer"}, Acceptance: protocol.AgentRequirement{CategoryID: "reviewer"},
	}
	spec.ReviewDeliveryID, spec.Delivery = "pr", ""
	spec.Deliverables = []string{domain.DeliveryCommit, domain.DeliveryReport}
	change := protocol.UpdateIssueTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Spec: &spec}
	if op := h.exec(owner, h.env("issues.update_task", taskTarget(initial.TaskID), change, rev(h.task(initial.TaskID).ControlRevision)), CanonicalOwnerTaskUpdate{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	original := ownerWorkerClaim(t, h, runtime, protocol.RoleImplement)
	typed := protocol.WorkerFailureReason(&protocol.AgentFailure{Code: "server_overloaded"})
	if op := h.runAs(original, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: typed}, Release{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	approvedAction := approveIndependentReviewWithManager(t, h, runtime, original.TaskID)
	preparation := ownerWorkerClaim(t, h, runtime, protocol.RoleImplement)
	if op := h.runAs(preparation, protocol.OpRunsRelease, preparationPayload(preparation), Release{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.task(preparation.TaskID).BlockReason != protocol.BlockPreparationRequired {
		t.Fatal("the retry did not reach the prelaunch preparation block", h.task(preparation.TaskID))
	}
	spec.Workspace.IncludeExistingChanges = true
	op := runIndependentReviewOwnerEffect(t, h, runtime, preparation.TaskID, change, CanonicalOwnerTaskUpdate{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal("the current Issue owner could not apply the confirmed preparation fix", op)
	}
	corrected := ownerWorkerClaim(t, h, runtime, protocol.RoleImplement)
	if corrected.TaskID != original.TaskID || corrected.RunID == preparation.RunID || corrected.SpecVersion <= preparation.SpecVersion || corrected.Workspace.ID != preparation.Workspace.ID {
		t.Fatal("the correction moved the original Task, workspace or prior evidence", corrected)
	}
	if h.countWhere(`SELECT COUNT(*) FROM runs r JOIN plan_actions a USING(action_id)
		WHERE r.run_id=? AND json_extract(a.trigger_json,'$.original_review_retry_action_id')=? AND json_extract(a.trigger_json,'$.review_retry_preparation_run_id')=?`,
		corrected.RunID, approvedAction, preparation.RunID) != 1 {
		t.Fatal("the corrected action lost the original approval or preparation receipt")
	}
	if op := h.runAs(corrected, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: typed}, Release{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.countWhere(`SELECT COUNT(*) FROM independent_review_failures WHERE run_id=? AND root_run_id=? AND retries=1`, corrected.RunID, original.RunID) != 1 {
		t.Fatal("workspace correction charged or reset the original retry budget")
	}
}

func TestFault_IndependentReviewFirstProviderObservationKeepsTheRetry(t *testing.T) {
	h, runtime, issue, task := independentReviewFailureFixture(t, false)
	run, ok := h.claimInputRun(runtime)
	if !ok || run.TaskID != task {
		t.Fatal(run, ok)
	}
	typed := protocol.WorkerFailureReason(&protocol.AgentFailure{Code: "server_overloaded"})
	if op := h.runAs(run, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: typed}, Release{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.PutDeliveryObservationFact(t.Context(), issue.Plan.Deliveries[0], protocol.DeliveryObservation{
			State: "open", HeadSHA: strings.Repeat("a", 40), BaseSHA: strings.Repeat("b", 40),
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if op := h.requestAction(task, protocol.ActionImplement, 2); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	next, ok := h.claimInputRun(runtime)
	if !ok || next.TaskID != task || next.RunID == run.RunID || next.Generation != 2 {
		t.Fatal("first provider observation stranded its authorized retry", next, ok)
	}
	if got := h.countWhere(`SELECT COUNT(*) FROM independent_review_run_targets WHERE run_id=? AND head_sha='' AND base_sha=''`, run.RunID); got != 1 {
		t.Fatal("a later observation rewrote the original unknown candidate", got)
	}
}
