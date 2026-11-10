package app

import (
	"encoding/json"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/scheduler"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
	"testing"
)

func TestIssueAdmissionRegressionLegacyWorkerResumesAfterTodo(t *testing.T) {
	h, r, i := issueOwnerHarness(t)
	if op := createOwnerExecution(t, h, i, "implementer"); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	as := ownerWorkerClaim(t, h, r, protocol.RoleImplement)
	i = admissionCurrent(t, h, i.IssueID)
	i = issueAdmissionStatus(t, h, i, protocol.IssueBacklog, nil)
	i = issueAdmissionStatus(t, h, i, protocol.IssueTodo, nil)
	run := h.run(as.RunID)
	var action store.ActionRow
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var e error
		action, e = store.GetAction(t.Context(), tx.Q(), as.ActionID)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if desired := desiredFor(run, action, h.task(as.TaskID)); desired != protocol.DesiredRun {
		t.Fatalf("resumed legacy worker desired=%s, handoff=%+v, allowed=%v", desired, run.PauseHandoff, as.PauseHandoffs)
	}
}

func TestIssueAdmissionRegressionRetainedIssuePauseRestoresOnlyItsTaskControl(t *testing.T) {
	h, r := managerClaimHarness(t)
	i := createCanonicalOwnerTestIssue(t, h)
	op := h.exec(owner, h.env(protocol.OpIssueRequestManager, protocol.Target{Type: "issue", ID: i.IssueID}, protocol.RequestIssueManagerPayload{ExpectedAssignmentEpoch: i.AssignmentEpoch, Reason: "Retained manager"}, rev(i.Revision)), retainedManagerCommand{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	as := admissionClaim(t, h, r)
	i = admissionCurrent(t, h, i.IssueID)
	paused := h.exec(owner, h.env("issues.pause", protocol.Target{Type: "issue", ID: i.IssueID}, protocol.IssueControlPayload{ExpectedAssignmentEpoch: i.AssignmentEpoch, Reason: "Pause"}, rev(i.Revision)), CanonicalOwnerIssueCommand{Action: "pause"})
	if paused.Status != protocol.OpSucceeded {
		t.Fatal(paused)
	}
	// Exact task control projection stored by the base branch on Issue pause.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if _, e := tx.Exec(t.Context(), `DELETE FROM run_pause_handoffs WHERE run_id=?`, as.RunID); e != nil {
			return e
		}
		if _, e := tx.Exec(t.Context(), `UPDATE issue_manager_tasks SET issue_paused=1 WHERE task_id=?`, as.TaskID); e != nil {
			return e
		}
		_, e := tx.Exec(t.Context(), `UPDATE tasks SET control_mode='paused',control_revision=control_revision+1 WHERE task_id=?`, as.TaskID)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.app.InitializeIssueAdmission(t.Context()); err != nil {
		t.Fatal(err)
	}
	i = admissionCurrent(t, h, i.IssueID)
	resumed := h.exec(owner, h.env("issues.resume", protocol.Target{Type: "issue", ID: i.IssueID}, protocol.IssueControlPayload{ExpectedAssignmentEpoch: i.AssignmentEpoch, Reason: "Resume"}, rev(i.Revision)), CanonicalOwnerIssueCommand{Action: "resume"})
	if resumed.Status != protocol.OpSucceeded {
		t.Fatal(resumed)
	}
	if task := h.task(as.TaskID); task.ControlMode != protocol.ControlManaged {
		t.Fatalf("resumed Issue retained its old Issue-induced task control: %s", task.ControlMode)
	}
}

func TestIssueAdmissionRegressionReturningAssignedManagerToOwnerDoesNotReclaim(t *testing.T) {
	h, r, i := issueOwnerHarness(t)
	assignment := canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.assign", protocol.Target{Type: "issue", ID: i.IssueID}, protocol.IssueAssignmentPayload{ExpectedAssignmentEpoch: i.AssignmentEpoch, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: owner.Subject}, Reason: "Return this work to me"}, rev(i.Revision)), HostedIssueAssignment{}))
	i = canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.finish_assignment", protocol.Target{Type: "issue", ID: i.IssueID}, protocol.FinishIssueAssignmentPayload{ExpectedAssignmentEpoch: assignment.AssignmentEpoch, HandoffID: assignment.PendingHandoffID, Reason: "Confirmed old owner stopped"}, rev(assignment.Revision)), HostedIssueAssignment{Finish: true}))
	if i.Assignee.Kind != protocol.IssueAssigneeOwner {
		t.Fatal(i)
	}
	if as := admissionClaim(t, h, r); as != nil {
		t.Fatalf("owner's manual handback immediately reallocated to Agent: %+v", as)
	}
}

func TestIssueAdmissionRegressionDeferredHandbackChangesAssignee(t *testing.T) {
	h, _, i := issueOwnerHarness(t)
	i = canonicalLifecycleIssue(t, h.exec(owner, h.env(protocol.OpIssueRequestOwner, protocol.Target{Type: "issue", ID: i.IssueID}, protocol.RequestIssueManagerPayload{CategoryID: "implementer", ExpectedAssignmentEpoch: i.AssignmentEpoch, Replace: true, Reason: "Prepare replacement"}, rev(i.Revision)), RequestIssueOwner{}))
	i = canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.assign", protocol.Target{Type: "issue", ID: i.IssueID}, protocol.IssueAssignmentPayload{ExpectedAssignmentEpoch: i.AssignmentEpoch, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: owner.Subject}, Reason: "Cancel replacement and return work to me"}, rev(i.Revision)), HostedIssueAssignment{}))
	if i.Assignee.Kind != protocol.IssueAssigneeOwner || i.Assignee.Ref != owner.Subject {
		t.Fatalf("handback cleared selection but retained Agent assignee: %+v", i.IssueControl)
	}
}

func TestIssueAdmissionRegressionInitializerPreservesExplicitlyClearedSelection(t *testing.T) {
	h, _, i := issueOwnerHarness(t)
	i = canonicalLifecycleIssue(t, h.exec(owner, h.env(protocol.OpIssueRequestOwner, protocol.Target{Type: "issue", ID: i.IssueID}, protocol.RequestIssueManagerPayload{CategoryID: "implementer", ExpectedAssignmentEpoch: i.AssignmentEpoch, Replace: true, Reason: "Prepare replacement"}, rev(i.Revision)), RequestIssueOwner{}))
	i = canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.assign", protocol.Target{Type: "issue", ID: i.IssueID}, protocol.IssueAssignmentPayload{ExpectedAssignmentEpoch: i.AssignmentEpoch, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: owner.Subject}, Reason: "Clear selection"}, rev(i.Revision)), HostedIssueAssignment{}))
	if i.OwnerSelection != nil {
		t.Fatal("selection was not cleared")
	}
	if err := h.app.InitializeIssueAdmission(t.Context()); err != nil {
		t.Fatal(err)
	}
	i = admissionCurrent(t, h, i.IssueID)
	if i.OwnerSelection != nil {
		t.Fatalf("startup resurrected explicitly cleared selection: %+v", i.OwnerSelection)
	}
}
func TestIssueAdmissionRegressionFrozenReviewResumesAfterBacklog(t *testing.T) {
	h, r, i := issueOwnerHarness(t)
	r.Capabilities = append(r.Capabilities, protocol.CapabilityPauseHandoffs)
	r.Sequence++
	if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := createOwnerExecution(t, h, i, "implementer"); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	impl := ownerWorkerClaim(t, h, r, protocol.RoleImplement)
	if op := h.runAs(impl, protocol.OpRunsSubmitArtifact, testArtifact(h.task(impl.TaskID).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	accept := ownerWorkerClaim(t, h, r, protocol.RoleAccept)
	review := admissionCurrent(t, h, accept.Responsibility.IssueID)
	review = issueAdmissionStatus(t, h, review, protocol.IssueBacklog, nil)
	paused := h.run(accept.RunID)
	if paused.PauseHandoff == nil {
		t.Fatal("review pause was not recorded")
	}
	if op := h.runAs(accept, protocol.OpRunsYieldPause, protocol.YieldPausePayload{PauseHandoff: *paused.PauseHandoff}, YieldPause{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	issueAdmissionStatus(t, h, review, protocol.IssueTodo, nil)
	successor := ownerWorkerClaim(t, h, r, protocol.RoleAccept)
	if successor.RunID == accept.RunID || successor.TaskID != accept.TaskID || successor.SpecVersion != accept.SpecVersion || string(successor.Artifact) != string(accept.Artifact) || successor.Execution.Identity != accept.Execution.Identity {
		t.Fatal("review continuation replaced its frozen work", successor, accept)
	}
	if op := h.runAs(successor, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.task(accept.TaskID).Lifecycle != protocol.TaskCompleted {
		t.Fatal(h.task(accept.TaskID))
	}
}
func TestIssueAdmissionRegressionInitializerRetainsPendingReview(t *testing.T) {
	h, r, i := issueOwnerHarness(t)
	if op := createOwnerExecution(t, h, i, "implementer"); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	impl := ownerWorkerClaim(t, h, r, protocol.RoleImplement)
	var reviewID string
	if e := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if e := tx.Q().QueryRowContext(t.Context(), `SELECT review_issue_id FROM task_owner_contracts WHERE task_id=?`, impl.TaskID).Scan(&reviewID); e != nil {
			return e
		}
		_, e := tx.Exec(t.Context(), `UPDATE issue_controls SET admission_initialized=0 WHERE issue_id=?`, reviewID)
		return e
	}); e != nil {
		t.Fatal(e)
	}
	before := admissionCurrent(t, h, reviewID)
	if e := h.app.InitializeIssueAdmission(t.Context()); e != nil {
		t.Fatal(e)
	}
	after := admissionCurrent(t, h, reviewID)
	if after.Revision != before.Revision+1 {
		t.Fatal("initializer did not append its definition", before, after)
	}
	if op := h.runAs(impl, protocol.OpRunsSubmitArtifact, testArtifact(h.task(impl.TaskID).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var candidates []scheduler.Candidate
	if e := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		rows, e := store.ListClaimCandidates(t.Context(), tx.Q())
		if e != nil {
			return e
		}
		candidates, e = views.SchedulingCandidates(rows)
		if e != nil {
			return e
		}
		return views.ApplyIssueDependencies(t.Context(), tx.Q(), candidates)
	}); e != nil {
		t.Fatal(e)
	}
	for _, c := range candidates {
		if c.TaskID == impl.TaskID && c.Kind == protocol.ActionAccept && c.OwnerResponsibilityChanged {
			t.Fatalf("initializer alone invalidated pending review: revision %d->%d", before.Revision, after.Revision)
		}
	}
	ownerWorkerClaim(t, h, r, protocol.RoleAccept)
}

func TestIssueAdmissionRegressionReopenPreservesOwnerPolicy(t *testing.T) {
	h, r, i := issueOwnerHarness(t)
	if i.OwnerSelection == nil || i.OwnerSelection.OwnershipPolicy != protocol.IssueOwnerPolicy {
		t.Fatalf("fixture missing owner policy: %+v", i.OwnerSelection)
	}
	closed := canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, i, "close", canonicalLifecycleClosure(i, protocol.WorkItemClosureCompleted)))
	op := h.exec(owner, h.env("issues.reopen", protocol.Target{Type: "issue", ID: i.IssueID}, canonicalOwnerControl{ExpectedAssignmentEpoch: closed.AssignmentEpoch}, rev(closed.Revision)), CanonicalOwnerIssueCommand{Action: "reopen"})
	restored := canonicalLifecycleIssue(t, op)
	if restored.OwnerSelection == nil || restored.OwnerSelection.OwnershipPolicy != protocol.IssueOwnerPolicy {
		t.Fatalf("reopen changed owner contract: before=%+v after=%+v", i.OwnerSelection, restored.OwnerSelection)
	}
	restored = issueAdmissionStatus(t, h, restored, protocol.IssueTodo, nil)
	if as := admissionClaim(t, h, r); as == nil {
		t.Fatal("restored owner was not claimed")
	}
}

func TestIssueAdmissionRegressionCancelAfterPauseHandoffClaimsSettlement(t *testing.T) {
	h, r := pendingIssueOwnerHarness(t)
	r.Capabilities = append(r.Capabilities, protocol.CapabilityPauseHandoffs)
	i := issueAdmissionStatus(t, h, createBacklogTestIssue(t, h), protocol.IssueTodo, &protocol.IssueOwnerRequest{CategoryID: "implementer"})
	as := admissionClaim(t, h, r)
	i = issueAdmissionStatus(t, h, admissionCurrent(t, h, i.IssueID), protocol.IssueBacklog, nil)
	paused := h.run(as.RunID)
	if op := h.runAs(*as, protocol.OpRunsYieldPause, protocol.YieldPausePayload{PauseHandoff: *paused.PauseHandoff}, YieldPause{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	op := h.exec(owner, h.env("issues.cancel", protocol.Target{Type: "issue", ID: i.IssueID}, protocol.IssueControlPayload{ExpectedAssignmentEpoch: i.AssignmentEpoch, Reason: "Cancel stopped work"}, rev(i.Revision)), CanonicalOwnerIssueCommand{Action: "cancel"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	next := admissionClaim(t, h, r)
	if next == nil {
		var status string
		h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			return tx.Q().QueryRowContext(t.Context(), `SELECT status FROM plan_actions WHERE action_id=?`, as.ActionID).Scan(&status)
		})
		t.Fatalf("cancel cannot claim settlement coordination: old action=%s; issue state=%s", status, admissionCurrent(t, h, i.IssueID).State)
	}

	if next.Role != protocol.RoleIssueManager || next.RunID == as.RunID || next.TaskID != as.TaskID || next.PauseContinuation != nil {
		t.Fatal("cancel resumed business work instead of settlement", next)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var published bool
		err := tx.Q().QueryRowContext(t.Context(), `SELECT continuation_action_id IS NOT NULL FROM run_pause_handoffs WHERE run_id=?`, as.RunID).Scan(&published)
		if published {
			t.Fatal("cancel published a business pause continuation")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIssueAdmissionRegressionQuestionHistoryAcrossPause(t *testing.T) {
	h := newHarness(t)
	inventoryCategory(t, h, "pause-test-execution", 0, "active")
	r := testRuntime("mixed-handoff-runner")
	r.Capabilities = append(r.Capabilities, protocol.CapabilityWorkerQuestions, protocol.CapabilityQuestionHandoffs, protocol.CapabilityPauseHandoffs)
	if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	i := createCanonicalOwnerTestIssue(t, h)
	createCanonicalTaskForTest(t, h, i)
	as, ok := h.claimInputRun(r)
	if !ok {
		t.Fatal("no first run")
	}
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	q1 := askQuestion(t, h, as, scopeQuestion)
	if op := h.runAs(as, protocol.OpRunsYieldQuestion, protocol.YieldQuestionPayload{DecisionID: q1.DecisionID}, YieldQuestion{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := answerQuestion(t, h, q1, scopeQuestion.Options[0], ""); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	second, ok := h.claimInputRun(r)
	if !ok {
		t.Fatal("no second run")
	}
	h.runAs(second, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	i = issueAdmissionStatus(t, h, admissionCurrent(t, h, i.IssueID), protocol.IssueBacklog, nil)
	paused := h.run(second.RunID)
	if op := h.runAs(second, protocol.OpRunsYieldPause, protocol.YieldPausePayload{PauseHandoff: *paused.PauseHandoff}, YieldPause{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	i = issueAdmissionStatus(t, h, i, protocol.IssueTodo, nil)
	third, ok := h.claimInputRun(r)
	if !ok {
		t.Fatal("no paused successor")
	}
	if len(third.Questions) != 1 {
		t.Fatal("pause itself lost questions", third.Questions)
	}
	h.runAs(third, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	p2 := scopeQuestion
	p2.QuestionKey = "second-blocker"
	p2.Question = "Which evidence source?"
	q2 := askQuestion(t, h, third, p2)
	if op := h.runAs(third, protocol.OpRunsYieldQuestion, protocol.YieldQuestionPayload{DecisionID: q2.DecisionID}, YieldQuestion{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := answerQuestion(t, h, q2, p2.Options[0], ""); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	fourth, ok := h.claimInputRun(r)
	if !ok {
		t.Fatal("no final successor")
	}
	if len(fourth.Questions) != 2 {
		t.Fatalf("mixed pause/question chain lost original answer %s: got %+v", q1.DecisionID, fourth.Questions)
	}
}

func TestIssueAdmissionRegressionPauseContinuationRequiresNegotiation(t *testing.T) {
	h := newHarness(t)
	inventoryCategory(t, h, "pause-test-execution", 0, "active")
	r := testRuntime("pause-capable-runner")
	r.Capabilities = append(r.Capabilities, protocol.CapabilityPauseHandoffs)
	if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	i := createCanonicalOwnerTestIssue(t, h)
	createCanonicalTaskForTest(t, h, i)
	as, ok := h.claimInputRun(r)
	if !ok || !as.PauseHandoffs {
		t.Fatal(as)
	}
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	i = issueAdmissionStatus(t, h, admissionCurrent(t, h, i.IssueID), protocol.IssueBacklog, nil)
	paused := h.run(as.RunID)
	if op := h.runAs(as, protocol.OpRunsYieldPause, protocol.YieldPausePayload{PauseHandoff: *paused.PauseHandoff, ThreadID: "original-thread"}, YieldPause{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	i = issueAdmissionStatus(t, h, i, protocol.IssueTodo, nil)
	r.Capabilities = r.Capabilities[:len(r.Capabilities)-1]
	r.Sequence++
	if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
		t.Log("downgrade rejected", op)
		return
	}
	next, ok := h.claimInputRun(r)
	if ok {
		t.Fatalf("unnegotiated runner claimed pause successor: handoffs=%v continuation=%+v", next.PauseHandoffs, next.PauseContinuation)
	}
}

func TestIssueAdmissionRegressionInitializerRetainsWithdrawnAuthorization(t *testing.T) {
	h, r := managerClaimHarness(t)
	i := createCanonicalOwnerTestIssue(t, h)
	source := queueMessage(t, h, "Delegate by this source only")
	op := h.exec(owner, h.env(protocol.OpIssueRequestManager, protocol.Target{Type: "issue", ID: i.IssueID}, protocol.RequestIssueManagerPayload{SourceMessageID: source, ExpectedAssignmentEpoch: i.AssignmentEpoch, Reason: "exact original authorization"}, rev(i.Revision)), retainedManagerCommand{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var m store.IssueManagerTask
	if e := json.Unmarshal(op.Result, &m); e != nil {
		t.Fatal(e)
	}
	withdrawOwnerSource(t, h, source)
	if h.task(m.TaskID).Lifecycle != protocol.TaskCancelled {
		t.Fatal("fixture not withdrawn")
	}
	if e := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, e := tx.Exec(t.Context(), `UPDATE issue_controls SET admission_initialized=0 WHERE issue_id=?`, i.IssueID)
		return e
	}); e != nil {
		t.Fatal(e)
	}
	if e := h.app.InitializeIssueAdmission(t.Context()); e != nil {
		t.Fatal(e)
	}
	if as := admissionClaim(t, h, r); as != nil {
		t.Fatalf("withdrawn manager restarted: %+v", as)
	}
	i = admissionCurrent(t, h, i.IssueID)
	i = canonicalLifecycleIssue(t, h.exec(owner, h.env(protocol.OpIssueRequestManager, protocol.Target{Type: "issue", ID: i.IssueID}, protocol.RequestIssueManagerPayload{ExpectedAssignmentEpoch: i.AssignmentEpoch, Reason: "Explicitly admit a new responsibility after withdrawal"}, rev(i.Revision)), RequestIssueManager{}))
	if next := admissionClaim(t, h, r); next == nil || next.TaskID == m.TaskID {
		t.Fatal("explicit reauthorization did not admit a fresh responsibility", i, next)
	}
}
