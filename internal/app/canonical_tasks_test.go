package app

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func createCanonicalTaskForTest(t *testing.T, h *harness, issue protocol.Issue) string {
	t.Helper()
	payload := canonicalOwnerTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Investigate the vague goal", Spec: testSpec, Rating: protocol.RatingP1}
	payload.Spec.ImplementationContext = "PRIVATE_OWNER_IMPLEMENTATION_REASONING"
	cmd := CanonicalOwnerTaskCreate{}
	op := h.exec(owner, h.env(cmd.Operation(), protocol.Target{Type: "issue", ID: issue.IssueID}, payload, rev(issue.Revision)), cmd)
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

func TestCanonicalTaskCreateFreezesOwnerSpecAtBaselineZero(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	task := createCanonicalTaskForTest(t, h, issue)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		primary, err := store.GetTaskIssueOwner(t.Context(), tx.Q(), task)
		if err != nil {
			return err
		}
		if primary != issue.IssueID {
			t.Fatal("missing immutable primary")
		}
		var baseline int64
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT baseline_revision FROM work_item_contexts WHERE work_item_id=?`, issue.IssueID).Scan(&baseline); err != nil {
			return err
		}
		if baseline != 0 {
			t.Fatal("owner task admission invented global readiness")
		}
		saved, err := store.GetCanonicalIssue(t.Context(), tx.Q(), issue.IssueID)
		if err != nil {
			return err
		}
		if len(saved.Plan.Stages) != 0 || saved.Revision != issue.Revision+1 {
			t.Fatalf("membership=%+v", saved)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	spec := h.storedSpec(task, 1)
	if len(spec.SharedContext) != 1 || spec.SharedContext[0].WorkItemID != issue.IssueID || spec.SharedContext[0].ContextDigest == "" || spec.SharedContext[0].Revision != issue.Revision {
		t.Fatalf("frozen=%+v", spec.SharedContext)
	}
}

func TestCanonicalTaskOwnerAdmissionDoesNotWaitForMainBaseline(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	// This actual source duty is retained; the owner's task spec authorizes a
	// bounded investigation without waiting for main's technical planning.
	source, _ := h.say("Investigate the unclear scope")
	if op := h.associate(owner, source, issue.IssueID); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	task := createCanonicalTaskForTest(t, h, issue)
	assignment, ok, _ := h.claim("linux-1")
	if !ok || assignment.TaskID != task || assignment.Role != protocol.RoleImplement {
		t.Fatalf("owner task did not claim: %+v %v", assignment, ok)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var pending int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT needs_review FROM message_context_obligations WHERE message_id=? AND work_item_id=?`, source, issue.IssueID).Scan(&pending); err != nil {
			return err
		}
		if pending != 1 {
			t.Fatal("task admission cleared issue source review")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func canonicalTaskIssue(t *testing.T, h *harness, id string) protocol.Issue {
	t.Helper()
	var issue protocol.Issue
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		issue, err = store.GetCanonicalIssue(t.Context(), tx.Q(), id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return issue
}
func TestCanonicalTaskOwnerExecutionAcceptanceReviewAndClosure(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	task := createCanonicalTaskForTest(t, h, issue)
	implementation, ok, _ := h.claim("linux-1")
	if !ok || implementation.TaskID != task || !strings.Contains(string(implementation.Spec), "PRIVATE_OWNER_IMPLEMENTATION_REASONING") {
		t.Fatal(implementation)
	}
	h.runAs(implementation, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	if op := h.runAs(implementation, protocol.OpRunsSubmitArtifact, testArtifact(h.task(task).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	acceptance, ok, claimOp := h.claim("linux-1")
	if !ok || acceptance.TaskID != task || acceptance.Role != protocol.RoleAccept || acceptance.RunID == implementation.RunID {
		t.Fatalf("independent acceptance=%+v op=%+v task=%+v", acceptance, claimOp, h.task(task))
	}
	if strings.Contains(string(acceptance.Spec), "PRIVATE_OWNER_IMPLEMENTATION_REASONING") {
		t.Fatal("implementation rationale leaked to independent acceptance")
	}
	if op := h.runAs(acceptance, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var pending []protocol.WorkItemCompletionReview
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		pending, err = store.ListPendingCompletionReviews(t.Context(), tx.Q(), issue.IssueID)
		if err != nil {
			return err
		}
		if len(pending) != 1 || pending[0].TaskID != task || pending[0].AcceptanceID == "" {
			t.Fatalf("owning review=%+v", pending)
		}
		var managers int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT count(*) FROM agent_conversations WHERE work_item_id=?`, issue.IssueID).Scan(&managers); err != nil {
			return err
		}
		if managers != 0 {
			t.Fatal("worker completion activated old background runtime")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	blockedClosure := canonicalLifecycleClosure(issue, protocol.WorkItemClosureCompleted)
	blockedClosure.CompletionReviews = []protocol.CompletionReviewAcknowledgement{{EventID: pending[0].EventID, Reason: "Owner checked the accepted result"}}
	blocked := canonicalLifecycleControl(t, h, issue, "close", blockedClosure)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		reviews, err := store.ListPendingCompletionReviews(t.Context(), tx.Q(), issue.IssueID)
		if len(reviews) != 1 {
			t.Fatal("failed endpoint leaked completion acknowledgement")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	expectWorkCode(t, blocked, protocol.CodeInvalidTransition)
	resource := h.resourceForTask(task)
	if !h.releaseResource(task, resource.WorkspaceID) {
		t.Fatal("settled worker workspace did not release")
	}
	payload := struct {
		canonicalOwnerControl
		Reviews []protocol.CompletionReviewAcknowledgement `json:"reviews"`
	}{canonicalOwnerControl: canonicalOwnerControl{ExpectedAssignmentEpoch: issue.AssignmentEpoch}, Reviews: []protocol.CompletionReviewAcknowledgement{{EventID: pending[0].EventID, Reason: "Owner checked the accepted deliverable and remaining scope"}}}
	env := h.env("issues.review_completions", protocol.Target{Type: "issue", ID: issue.IssueID}, payload, rev(issue.Revision))
	beforeRevision := issue.Revision
	beforePlanHistory, beforeControlHistory := h.count("work_item_revisions"), h.count("issue_control_revisions")
	beforeReviewEvents := len(h.events("issue.completions_reviewed"))
	reviewed := h.exec(owner, env, CanonicalOwnerIssueCommand{Action: "review_completions"})
	issue = canonicalLifecycleIssue(t, reviewed)
	if issue.Revision != beforeRevision+1 || h.count("work_item_revisions") != beforePlanHistory+1 || h.count("issue_control_revisions") != beforeControlHistory+1 || len(h.events("issue.completions_reviewed")) != beforeReviewEvents+1 {
		t.Fatalf("completion review was treated as no-op: revision=%d want=%d plan/control histories=%d/%d review events=%d", issue.Revision, beforeRevision+1, h.count("work_item_revisions"), h.count("issue_control_revisions"), len(h.events("issue.completions_reviewed")))
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var revision int64
		err := tx.Q().QueryRowContext(t.Context(), `SELECT reviewed_revision FROM work_item_completion_reviews WHERE work_item_id=? AND event_id=?`, issue.IssueID, pending[0].EventID).Scan(&revision)
		if err == nil && revision != issue.Revision {
			t.Fatalf("review points to non-current revision %d vs %d", revision, issue.Revision)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "close", canonicalLifecycleClosure(issue, protocol.WorkItemClosureCompleted)))
	beforeReplayEvents, beforeReplayPlan, beforeReplayControl := len(h.events("issue.completions_reviewed")), h.count("work_item_revisions"), h.count("issue_control_revisions")
	replay := h.exec(owner, env, CanonicalOwnerIssueCommand{Action: "review_completions"})
	if len(h.events("issue.completions_reviewed")) != beforeReplayEvents || h.count("work_item_revisions") != beforeReplayPlan || h.count("issue_control_revisions") != beforeReplayControl {
		t.Fatal("completion replay duplicated revision/history/event")
	}
	if replay.Status != protocol.OpSucceeded || string(replay.Result) != string(reviewed.Result) {
		t.Fatal("review receipt did not survive closure")
	}
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "reopen", canonicalOwnerControl{ExpectedAssignmentEpoch: issue.AssignmentEpoch}))
	plan := issue.Plan
	plan.Title = "Next owner planning round"
	updated := h.exec(owner, h.env("issues.update", protocol.Target{Type: "issue", ID: issue.IssueID}, canonicalOwnerIssueUpdate{Plan: plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch}, rev(issue.Revision)), CanonicalOwnerIssueCommand{Action: "update"})
	if updated.Status != protocol.OpSucceeded || h.task(task).Lifecycle != protocol.TaskCompleted {
		t.Fatal("completed primary permanently sealed the issue", updated)
	}
}

func TestCanonicalTaskOwnerCancellationWaitsForActualStopAndResourceRelease(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	task := createCanonicalTaskForTest(t, h, issue)
	run, ok, _ := h.claim("linux-1")
	if !ok {
		t.Fatal("no worker claim")
	}
	h.runAs(run, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "cancel", canonicalOwnerControl{ExpectedAssignmentEpoch: issue.AssignmentEpoch}))
	if h.task(task).Lifecycle != protocol.TaskCancelled {
		t.Fatal("issue cancellation did not use task cancellation")
	}
	finish := canonicalOwnerEndpoint{canonicalOwnerControl: canonicalOwnerControl{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Owner ends this responsibility"}}
	expectWorkCode(t, canonicalLifecycleControl(t, h, issue, "finish_cancel", finish), protocol.CodeInvalidTransition)
	if op := h.runAs(run, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "worker stopped for owner cancellation"}, Release{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	resource := h.resourceForTask(task)
	if !h.releaseResource(task, resource.WorkspaceID) {
		t.Fatal("stopped resource did not release")
	}
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "finish_cancel", finish))
	if issue.State != protocol.IssueCancelled {
		t.Fatal(issue)
	}
}

func TestCanonicalTaskPauseKeepsActiveAgreementAndGatesAcceptance(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	task := createCanonicalTaskForTest(t, h, issue)
	run, ok, _ := h.claim("linux-1")
	if !ok {
		t.Fatal("no implementation")
	}
	h.runAs(run, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	original := h.task(task)
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "pause", canonicalOwnerControl{ExpectedAssignmentEpoch: issue.AssignmentEpoch}))
	current := h.task(task)
	if current.ControlRevision != original.ControlRevision || current.ControlMode != original.ControlMode || current.ImplementGeneration != original.ImplementGeneration {
		t.Fatal("issue pause changed worker agreement")
	}
	if op := h.runAs(run, protocol.OpRunsSubmitArtifact, testArtifact(current.WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.task(task).Lifecycle != protocol.TaskAwaitingAcceptance {
		t.Fatal("paused issue rewrote an in-flight implementation result")
	}
	if _, ok, _ := h.claim("linux-1"); ok {
		t.Fatal("paused issue admitted acceptance")
	}
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "resume", canonicalOwnerControl{ExpectedAssignmentEpoch: issue.AssignmentEpoch}))
	accept, ok, _ := h.claim("linux-1")
	if !ok || accept.Role != protocol.RoleAccept || accept.SpecVersion != run.SpecVersion {
		t.Fatalf("resume=%+v", accept)
	}
}

func TestCanonicalTaskCreateReplayStaleRollbackAndPlannerIsolation(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	payload := canonicalOwnerTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Owner investigation", Spec: testSpec, Rating: protocol.RatingP1}
	cmd := CanonicalOwnerTaskCreate{}
	env := h.env(cmd.Operation(), protocol.Target{Type: "issue", ID: issue.IssueID}, payload, rev(issue.Revision))
	created := h.exec(owner, env, cmd)
	if created.Status != protocol.OpSucceeded {
		t.Fatal(created)
	}
	var result struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(created.Result, &result); err != nil {
		t.Fatal(err)
	}
	before := h.count("tasks")
	expectWorkCode(t, h.exec(owner, h.env(cmd.Operation(), env.Target, payload, rev(issue.Revision)), cmd), protocol.CodeStaleRevision)
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	payload.ExpectedAssignmentEpoch++
	expectWorkCode(t, h.exec(owner, h.env(cmd.Operation(), env.Target, payload, rev(issue.Revision)), cmd), protocol.CodeStaleRevision)
	if h.count("tasks") != before {
		t.Fatal("rejected create left task")
	}
	replay := h.exec(owner, env, cmd)
	if replay.Status != protocol.OpSucceeded || string(replay.Result) != string(created.Result) || h.count("tasks") != before {
		t.Fatal("create replay duplicated or returned new state")
	}
	if _, err := h.app.Execute(t.Context(), mainAgent, env, cmd); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatalf("create reauthorization: %v", err)
	}
	target := protocol.Target{Type: "task", ID: result.TaskID}
	task := h.task(result.TaskID)
	for _, command := range []struct {
		cmd     Command
		payload any
	}{
		{UpdateIntent{}, updateIntentPayload{Spec: &testSpec}},
		{CancelTask{}, nil},
		{SetControlMode{}, protocol.SetControlModePayload{Mode: protocol.ControlHumanControl}},
		{CreateAction{}, protocol.CreateActionPayload{Kind: protocol.ActionImplement, Generation: 2}},
	} {
		if _, err := h.app.Execute(t.Context(), mainAgent, h.env(command.cmd.Operation(), target, command.payload, rev(task.ControlRevision)), command.cmd); protocol.CodeOf(err) != protocol.CodeForbidden {
			t.Fatalf("%s: %v", command.cmd.Operation(), err)
		}
	}
	payload.ExpectedAssignmentEpoch = issue.AssignmentEpoch
	payload.StageIDs = []string{"missing"}
	expectWorkCode(t, h.exec(owner, h.env(cmd.Operation(), env.Target, payload, rev(issue.Revision)), cmd), protocol.CodeInvalidRequest)
	if h.count("tasks") != before {
		t.Fatal("bad stage did not rollback create")
	}
}

func TestCanonicalTaskMetadataRevisionDoesNotFenceFrozenClaim(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	task := createCanonicalTaskForTest(t, h, issue)
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	plan := issue.Plan
	plan.Title = "Prioritized investigation"
	issue = canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.update", protocol.Target{Type: "issue", ID: issue.IssueID}, canonicalOwnerIssueUpdate{Plan: plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch}, rev(issue.Revision)), CanonicalOwnerIssueCommand{Action: "update"}))
	run, ok, _ := h.claim("linux-1")
	if !ok || run.TaskID != task || run.SpecVersion != 1 {
		t.Fatalf("metadata invalidated contract: %+v", run)
	}
}

func TestCanonicalTaskMixedIssueAndTaskDependencies(t *testing.T) {
	for _, cycle := range []bool{false, true} {
		t.Run(map[bool]string{false: "parent_task_order_is_legal", true: "explicit_parent_wait_closes_cycle"}[cycle], func(t *testing.T) {
			h := newHarness(t)
			parent := createCanonicalOwnerTestIssue(t, h)
			cp := canonicalOwnerIssueCreate{Plan: parent.Plan, ParentIssueID: parent.IssueID}
			child := canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, cp, nil), CanonicalOwnerIssueCommand{Action: "create"}))
			pt := createCanonicalTaskForTest(t, h, parent)
			ct := createCanonicalTaskForTest(t, h, child)
			// Historical task prerequisites are enforced today but have no write API.
			// This is a store-fact fixture, not a new task-dependency product endpoint.
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				_, err := tx.Exec(t.Context(), `INSERT INTO task_dependencies(task_id,depends_on)VALUES(?,?)`, ct, pt)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			parent = canonicalTaskIssue(t, h, parent.IssueID)
			deps := []string{}
			if cycle {
				deps = []string{child.IssueID}
			}
			p := struct {
				canonicalOwnerControl
				DependsOn []string `json:"depends_on"`
			}{canonicalOwnerControl{ExpectedAssignmentEpoch: parent.AssignmentEpoch}, deps}
			op := canonicalLifecycleControl(t, h, parent, "dependencies", p)
			if cycle {
				expectWorkCode(t, op, protocol.CodeInvalidRequest)
				saved := canonicalTaskIssue(t, h, parent.IssueID)
				if len(saved.DependsOn) != 0 || saved.Revision != parent.Revision {
					t.Fatal("waiting-cycle write persisted")
				}
			} else if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			run, ok, _ := h.claim("linux-1")
			if !ok || run.TaskID != pt {
				t.Fatalf("valid ordering did not claim parent task: %+v", run)
			}
		})
	}
}

func TestCanonicalTaskParentDependsOnChildDoesNotBlockChildClaim(t *testing.T) {
	h := newHarness(t)
	parent := createCanonicalOwnerTestIssue(t, h)
	child := canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, canonicalOwnerIssueCreate{Plan: parent.Plan, ParentIssueID: parent.IssueID}, nil), CanonicalOwnerIssueCommand{Action: "create"}))
	pt := createCanonicalTaskForTest(t, h, parent)
	ct := createCanonicalTaskForTest(t, h, child)
	parent = canonicalTaskIssue(t, h, parent.IssueID)
	p := struct {
		canonicalOwnerControl
		DependsOn []string `json:"depends_on"`
	}{canonicalOwnerControl{ExpectedAssignmentEpoch: parent.AssignmentEpoch}, []string{child.IssueID}}
	parent = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, parent, "dependencies", p))
	run, ok, _ := h.claim("linux-1")
	if !ok || run.TaskID != ct || run.TaskID == pt {
		t.Fatalf("ancestor prerequisites leaked onto child: %+v", run)
	}
	h.runAs(run, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	if op := h.runAs(run, protocol.OpRunsSubmitArtifact, testArtifact(h.task(ct).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	accept, ok, _ := h.claim("linux-1")
	if !ok || accept.TaskID != ct {
		t.Fatal("child acceptance blocked by parent's prerequisite")
	}
	if op := h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	resource := h.resourceForTask(ct)
	if !h.releaseResource(ct, resource.WorkspaceID) {
		t.Fatal("release")
	}
	child = canonicalTaskIssue(t, h, child.IssueID)
	var reviews []protocol.WorkItemCompletionReview
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		reviews, err = store.ListPendingCompletionReviews(t.Context(), tx.Q(), child.IssueID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	close := canonicalLifecycleClosure(child, protocol.WorkItemClosureCompleted)
	close.CompletionReviews = []protocol.CompletionReviewAcknowledgement{{EventID: reviews[0].EventID, Reason: "Child accepted result verified"}}
	child = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, child, "close", close))
	run, ok, _ = h.claim("linux-1")
	if !ok || run.TaskID != pt {
		t.Fatalf("achieved child did not release parent task: %+v", run)
	}
}

func TestCanonicalTaskSourceCorrectionGatesNextClaimNotRunningResult(t *testing.T) {
	h := newHarness(t)
	source, turn := startReviewTurn(t, h)
	legacy := h.createWork(workPlan())
	report := recordAgentMessagePayload{Body: "Global API constraint draft", ReplyTo: source, TurnID: turn.TurnID, ItemID: "constraint", Status: protocol.AssistantStreaming}
	op := h.exec(mainAgent, h.env("conversation.record_agent_message", protocol.Target{Type: "conversation"}, report, nil), RecordAgentMessage{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var message struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(op.Result, &message); err != nil {
		t.Fatal(err)
	}
	h.associate(mainAgent, source)
	h.associate(mainAgent, message.MessageID)
	fact := protocol.ContextFact{Scope: "global", Kind: "constraint", Body: "Preserve the public API", SourceMessageID: message.MessageID}
	if op := addLegacyGuardFact(t, h, legacy.WorkItemID, turn.RequestID, fact); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	// The global fact was admitted before canonical issues existed, through its
	// real app command. No SQL invents an authorization or source correction.
	issue := createCanonicalOwnerTestIssue(t, h)
	task := createCanonicalTaskForTest(t, h, issue)
	run, ok, _ := h.claim("linux-1")
	if !ok {
		t.Fatal("owner-approved source did not claim")
	}
	h.runAs(run, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	report.Body = "Corrected global API constraint"
	report.Status = protocol.AssistantCompleted
	if op := h.exec(mainAgent, h.env("conversation.record_agent_message", protocol.Target{Type: "conversation"}, report, nil), RecordAgentMessage{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.runAs(run, protocol.OpRunsSubmitArtifact, testArtifact(h.task(task).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if current := h.task(task); current.Lifecycle != protocol.TaskAwaitingAcceptance || current.ControlRevision != 1 || current.ControlMode != protocol.ControlManaged {
		t.Fatalf("source change rewrote running agreement: %+v", current)
	}
	if _, ok, _ := h.claim("linux-1"); ok {
		t.Fatal("stale source snapshot admitted a new acceptance")
	}
	oldArtifact := h.task(task).CurrentArtifactID
	next := testSpec
	current := h.task(task)
	updated := h.exec(owner, h.env("tasks.update_intent", taskTarget(task), updateIntentPayload{Spec: &next}, rev(current.ControlRevision)), UpdateIntent{})
	if updated.Status != protocol.OpSucceeded {
		t.Fatal(updated)
	}
	run, ok, _ = h.claim("linux-1")
	if !ok || run.TaskID != task || run.SpecVersion != 2 || run.Role != protocol.RoleImplement {
		t.Fatalf("explicit owner revision did not admit fresh agreement: %+v", run)
	}
	if h.task(task).CurrentArtifactID != "" {
		t.Fatal("new spec reused the old artifact")
	}
	h.runAs(run, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	if op := h.runAs(run, protocol.OpRunsSubmitArtifact, testArtifact(h.task(task).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if current := h.task(task); current.CurrentArtifactID == oldArtifact || current.CurrentArtifactID == "" || current.SpecVersion != 2 {
		t.Fatal("spec2 did not get its own implementation artifact", current)
	}
	accept, ok, _ := h.claim("linux-1")
	if !ok || accept.Role != protocol.RoleAccept || accept.SpecVersion != 2 {
		t.Fatalf("fresh acceptance=%+v", accept)
	}
	if op := h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if current := h.task(task); current.Lifecycle != protocol.TaskCompleted || current.SpecVersion != 2 || current.CurrentArtifactID == oldArtifact {
		t.Fatal("fresh agreement did not complete independently", current)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		reviews, err := store.ListPendingCompletionReviews(t.Context(), tx.Q(), issue.IssueID)
		if len(reviews) != 1 {
			t.Fatalf("old stale result produced completion review: %+v", reviews)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCanonicalTaskWaitingWorkerKeepsIdentityAndReceivesOwnerAnswer(t *testing.T) {
	h := newHarness(t)
	runtime := testRuntime("canonical-question-runner")
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityWorkerQuestions)
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	issue := createCanonicalOwnerTestIssue(t, h)
	task := createCanonicalTaskForTest(t, h, issue)
	run, ok := h.claimInputRun(runtime)
	if !ok || !run.WorkerQuestions {
		t.Fatal(run)
	}
	h.runAs(run, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	before := h.task(task)
	lease := h.lease(run.RunID)
	workspace := h.resourceForTask(task)
	question := askQuestion(t, h, run, scopeQuestion)
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "pause", canonicalOwnerControl{ExpectedAssignmentEpoch: issue.AssignmentEpoch}))
	if op := answerQuestion(t, h, question, "renderer only", ""); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	answer, err := h.app.WorkerQuestion(t.Context(), runPrincipal(run.RunID, run.Role), question.DecisionID)
	if err != nil || answer.Status != store.DecisionAnswered {
		t.Fatalf("original worker answer=%+v %v", answer, err)
	}
	if h.task(task) != before || h.lease(run.RunID) != lease || h.resourceForTask(task).OccupiedBy != workspace.OccupiedBy || h.run(run.RunID).ObservedState != protocol.RunRunning {
		t.Fatal("waiting/answer/pause released or reassigned active execution")
	}
	if n := h.count("agent_conversations WHERE work_item_id='" + issue.IssueID + "'"); n != 0 {
		t.Fatal("owner answer opened a legacy manager")
	}
}

func TestCanonicalTaskUnknownBlocksCancelAndReparentUntilRealReconcile(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	other := createCanonicalOwnerTestIssue(t, h)
	task := createCanonicalTaskForTest(t, h, issue)
	run, ok, _ := h.claim("linux-1")
	if !ok {
		t.Fatal("no implementation")
	}
	h.runAs(run, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	h.clock.Advance(DefaultLeaseTTL + time.Second)
	if n, err := h.app.ExpireLeases(t.Context()); err != nil || n != 1 {
		t.Fatalf("expire %d %v", n, err)
	}
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	rp := struct {
		canonicalOwnerControl
		ParentIssueID string `json:"parent_issue_id"`
	}{canonicalOwnerControl{ExpectedAssignmentEpoch: issue.AssignmentEpoch}, other.IssueID}
	expectWorkCode(t, canonicalLifecycleControl(t, h, issue, "reparent", rp), protocol.CodeInvalidTransition)
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "cancel", canonicalOwnerControl{ExpectedAssignmentEpoch: issue.AssignmentEpoch}))
	end := canonicalOwnerEndpoint{canonicalOwnerControl: canonicalOwnerControl{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Stop this responsibility"}}
	expectWorkCode(t, canonicalLifecycleControl(t, h, issue, "finish_cancel", end), protocol.CodeInvalidTransition)
	if _, ok, _ := h.claim("linux-1"); ok {
		t.Fatal("unknown task was replayed")
	}
	res := h.hello("linux-1", protocol.HeldRun{RunID: run.RunID, LeaseID: run.LeaseID, Observed: protocol.RunRunning})
	if len(res.Runs) != 1 || res.Runs[0].Verdict != protocol.VerdictStop {
		t.Fatal(res)
	}
	if op := h.runAs(run, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "cancel acknowledged"}, Release{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	resource := h.resourceForTask(task)
	if !h.releaseResource(task, resource.WorkspaceID) {
		t.Fatal("cancelled workspace retained")
	}
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "finish_cancel", end))
	if issue.State != protocol.IssueCancelled {
		t.Fatal(issue)
	}
}

func TestCanonicalTaskOptionalStageKeepsExplicitPrerequisites(t *testing.T) {
	h := newHarness(t)
	p := canonicalOwnerIssueCreate{Plan: protocol.IssuePlan{Title: "Lightweight steps", Project: "default", Intent: "Investigate then implement", Stages: []protocol.IssueStage{{StageID: "investigate", Title: "Investigation", Status: protocol.WorkItemStagePlanned}, {StageID: "implement", Title: "Implementation", Status: protocol.WorkItemStagePlanned, DependsOn: []string{"investigate"}}}}}
	issue := canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, p, nil), CanonicalOwnerIssueCommand{Action: "create"}))
	payload := canonicalOwnerTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Explicit later step", Spec: testSpec, Rating: protocol.RatingP1, StageIDs: []string{"implement"}}
	op := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, payload, rev(issue.Revision)), CanonicalOwnerTaskCreate{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var task struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(op.Result, &task); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := h.claim("linux-1"); ok {
		t.Fatal("explicit stage prerequisite ignored")
	}
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	plan := issue.Plan
	plan.Stages[0].Status = protocol.WorkItemStageDone
	plan.Stages[0].Evidence = []protocol.IssueEvidence{{Kind: protocol.WorkItemEvidenceOwnerDecision, Reference: "current_request", Summary: "Owner completed the investigation"}}
	up := canonicalOwnerIssueUpdate{Plan: plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch, TaskDispositions: []protocol.IssueTaskDisposition{{TaskID: task.TaskID, SpecVersion: 1, Action: protocol.WorkItemTaskContinueExisting, Reason: "Keep the explicit implementation agreement"}}}
	op = h.exec(owner, h.env("issues.update", protocol.Target{Type: "issue", ID: issue.IssueID}, up, rev(issue.Revision)), CanonicalOwnerIssueCommand{Action: "update"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	run, ok, _ := h.claim("linux-1")
	if !ok || run.TaskID != task.TaskID {
		t.Fatalf("completed stage did not release explicit task: %+v", run)
	}
}

func TestCanonicalTaskSecondaryLegacyLinkGetsEvidenceNotCompletionResponsibility(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	task := createCanonicalTaskForTest(t, h, issue)
	legacy := h.createWork(workPlan())
	link := protocol.LinkWorkItemTaskPayload{TaskID: task, StageIDs: []string{"investigate"}}
	if op := h.exec(owner, h.env("work_items.link_task", workTarget(legacy.WorkItemID), link, rev(legacy.Revision)), LinkIssueTask{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	run, ok, _ := h.claim("linux-1")
	if !ok {
		t.Fatal("no run")
	}
	h.runAs(run, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	if op := h.runAs(run, protocol.OpRunsSubmitArtifact, testArtifact(h.task(task).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	accept, ok, _ := h.claim("linux-1")
	if !ok {
		t.Fatal("no acceptance")
	}
	if op := h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		if err := tx.EnsureCompletionReviews(t.Context(), legacy.WorkItemID); err != nil {
			return err
		}
		if err := tx.EnsureCompletionReviews(t.Context(), issue.IssueID); err != nil {
			return err
		}
		refs, err := store.ListPendingCompletionReviews(t.Context(), tx.Q(), legacy.WorkItemID)
		if err != nil {
			return err
		}
		if len(refs) != 0 {
			t.Fatalf("secondary completion duties=%+v", refs)
		}
		primary, err := store.ListPendingCompletionReviews(t.Context(), tx.Q(), issue.IssueID)
		if err != nil {
			return err
		}
		if len(primary) != 1 {
			t.Fatalf("primary completion duties=%+v", primary)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if n := h.count("background_sources"); n != 0 {
		t.Fatal("canonical task completion woke legacy evidence-only manager")
	}
}

func TestCanonicalTaskReparentRequiresExplicitInheritedContextRefreshForNewClaim(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	parent := createCanonicalOwnerTestIssue(t, h)
	task := createCanonicalTaskForTest(t, h, issue)
	plan := parent.Plan
	plan.Intent = "Preserve parent scope constraint"
	parent = canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.update", protocol.Target{Type: "issue", ID: parent.IssueID}, canonicalOwnerIssueUpdate{Plan: plan, ExpectedAssignmentEpoch: parent.AssignmentEpoch}, rev(parent.Revision)), CanonicalOwnerIssueCommand{Action: "update"}))
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	payload := struct {
		canonicalOwnerControl
		ParentIssueID string `json:"parent_issue_id"`
	}{canonicalOwnerControl{ExpectedAssignmentEpoch: issue.AssignmentEpoch}, parent.IssueID}
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "reparent", payload))
	if _, ok, _ := h.claim("linux-1"); ok {
		t.Fatal("new inherited context silently skipped the frozen task agreement")
	}
	spec := testSpec
	current := h.task(task)
	if op := h.exec(owner, h.env("tasks.update_intent", taskTarget(task), updateIntentPayload{Spec: &spec}, rev(current.ControlRevision)), UpdateIntent{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	run, ok, _ := h.claim("linux-1")
	if !ok || run.TaskID != task || run.SpecVersion != 2 || !strings.Contains(string(run.Spec), "Preserve parent scope constraint") {
		t.Fatalf("owner-refreshed inherited context=%+v", run)
	}
}

func TestCanonicalTaskOwnerRequiresFormalAcceptanceCapabilityBeforeImplementation(t *testing.T) {
	h := newDeliverableHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	spec := testSpec
	spec.AcceptanceExecution = &protocol.ExecutionProfile{Model: "formal-review-model", Effort: "xhigh"}
	payload := canonicalOwnerTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Owner full delivery", Spec: spec, Rating: protocol.RatingP1}
	op := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, payload, rev(issue.Revision)), CanonicalOwnerTaskCreate{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	runtime := testRuntime("canonical-formal-acceptance")
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if _, ok := h.claimInputRun(runtime); ok {
		t.Fatal("implementation host cannot honor the formal acceptance profile")
	}
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityAcceptanceExecution)
	run, ok := h.claimInputRun(runtime)
	if !ok || run.Role != protocol.RoleImplement || run.AcceptanceExecution != nil {
		t.Fatal(run)
	}
	h.runAs(run, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	if op := h.runAs(run, protocol.OpRunsSubmitArtifact, testArtifact(h.task(run.TaskID).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	accept, ok := h.claimInputRun(runtime)
	if !ok || accept.Role != protocol.RoleAccept || accept.AcceptanceExecution == nil || *accept.AcceptanceExecution != *spec.AcceptanceExecution {
		t.Fatalf("formal acceptance=%+v", accept)
	}
	if h.run(run.RunID).HostID != h.run(accept.RunID).HostID {
		t.Fatal("acceptance moved the workspace to a different host")
	}
	if op := h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}

func TestCanonicalTaskUpdatedDispositionConsumesActualIssueRevision(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	task := createCanonicalTaskForTest(t, h, issue)
	spec := testSpec
	spec.Goal = "Updated execution agreement"
	if op := h.exec(owner, h.env("tasks.update_intent", taskTarget(task), updateIntentPayload{Spec: &spec}, rev(h.task(task).ControlRevision)), UpdateIntent{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	plan := issue.Plan
	plan.Intent = "Revised owner intent"
	payload := canonicalOwnerIssueUpdate{Plan: plan, ExpectedAssignmentEpoch: issue.AssignmentEpoch, TaskDispositions: []protocol.IssueTaskDisposition{{TaskID: task, SpecVersion: 2, Action: protocol.WorkItemTaskUpdated, Reason: "Task spec2 explicitly updated"}}}
	issue = canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.update", protocol.Target{Type: "issue", ID: issue.IssueID}, payload, rev(issue.Revision)), CanonicalOwnerIssueCommand{Action: "update"}))
	payload.Plan = issue.Plan
	payload.Plan.Title = "A metadata-only edit"
	payload.TaskDispositions = nil
	issue = canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.update", protocol.Target{Type: "issue", ID: issue.IssueID}, payload, rev(issue.Revision)), CanonicalOwnerIssueCommand{Action: "update"}))
	payload.Plan = issue.Plan
	payload.Plan.Intent = "Another execution scope"
	payload.TaskDispositions = []protocol.IssueTaskDisposition{{TaskID: task, SpecVersion: 2, Action: protocol.WorkItemTaskUpdated, Reason: "Attempt to reuse the prior task update"}}
	expectWorkCode(t, h.exec(owner, h.env("issues.update", protocol.Target{Type: "issue", ID: issue.IssueID}, payload, rev(issue.Revision)), CanonicalOwnerIssueCommand{Action: "update"}), protocol.CodeInvalidTransition)
	if current := canonicalTaskIssue(t, h, issue.IssueID); current.Revision != issue.Revision {
		t.Fatal("reused task update advanced issue revision")
	}
}

func TestCanonicalTaskCannotAttachNewWorkToCompletedStage(t *testing.T) {
	h := newHarness(t)
	payload := canonicalOwnerIssueCreate{Plan: protocol.IssuePlan{Title: "Settled step", Project: "default", Intent: "Owner completed the step", Stages: []protocol.IssueStage{{StageID: "done", Title: "Done", Status: protocol.WorkItemStageDone, Evidence: []protocol.IssueEvidence{{Kind: protocol.WorkItemEvidenceOwnerDecision, Reference: "current_request", Summary: "Owner completed the step"}}}}}}
	issue := canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, payload, nil), CanonicalOwnerIssueCommand{Action: "create"}))
	create := canonicalOwnerTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "New work", Spec: testSpec, StageIDs: []string{"done"}}
	expectWorkCode(t, h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, create, rev(issue.Revision)), CanonicalOwnerTaskCreate{}), protocol.CodeInvalidRequest)
	if h.count("tasks") != 0 {
		t.Fatal("done-stage admission created task")
	}
}

func TestCanonicalTaskAncestorSemanticEditsRequireExplicitContractRefresh(t *testing.T) {
	for _, kind := range []string{"title", "intent", "background", "active_intent"} {
		t.Run(kind, func(t *testing.T) {
			h := newHarness(t)
			parent := createCanonicalOwnerTestIssue(t, h)
			child := canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, canonicalOwnerIssueCreate{Plan: parent.Plan, ParentIssueID: parent.IssueID}, nil), CanonicalOwnerIssueCommand{Action: "create"}))
			task := createCanonicalTaskForTest(t, h, child)
			var active protocol.Assignment
			if kind == "active_intent" {
				run, ok, _ := h.claim("linux-1")
				if !ok {
					t.Fatal("no original run")
				}
				active = run
				h.runAs(run, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
			}
			plan := parent.Plan
			switch kind {
			case "title":
				plan.Title = "Metadata-only parent title"
			case "background":
				plan.Background = []protocol.SharedFact{{Kind: protocol.SharedFactUserConstraint, Text: "NEW_PARENT_CONSTRAINT", Source: "Owner brief"}}
			default:
				plan.Intent = "NEW_PARENT_GOAL"
			}
			parent = canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.update", protocol.Target{Type: "issue", ID: parent.IssueID}, canonicalOwnerIssueUpdate{Plan: plan, ExpectedAssignmentEpoch: parent.AssignmentEpoch}, rev(parent.Revision)), CanonicalOwnerIssueCommand{Action: "update"}))
			if kind == "active_intent" {
				if op := h.runAs(active, protocol.OpRunsSubmitArtifact, testArtifact(h.task(task).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				if current := h.task(task); current.Lifecycle != protocol.TaskAwaitingAcceptance || current.ControlRevision != 1 || current.ControlMode != protocol.ControlManaged {
					t.Fatal("ancestor edit rewrote active contract", current)
				}
			}
			run, ok, _ := h.claim("linux-1")
			if kind == "title" {
				if !ok || run.TaskID != task || run.SpecVersion != 1 {
					t.Fatalf("parent title blocked original contract: %+v", run)
				}
				return
			}
			if ok {
				t.Fatalf("changed ancestor %s admitted stale inherited contract: %+v", kind, run)
			}
			next := testSpec
			current := h.task(task)
			if op := h.exec(owner, h.env("tasks.update_intent", taskTarget(task), updateIntentPayload{Spec: &next}, rev(current.ControlRevision)), UpdateIntent{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			run, ok, _ = h.claim("linux-1")
			expected := "NEW_PARENT_GOAL"
			if kind == "background" {
				expected = "NEW_PARENT_CONSTRAINT"
			}
			if !ok || run.TaskID != task || run.SpecVersion != 2 || run.Role != protocol.RoleImplement || !strings.Contains(string(run.Spec), expected) {
				t.Fatalf("explicit refreshed inherited contract: %+v", run)
			}
			if h.task(task).CurrentArtifactID != "" {
				t.Fatal("new inherited contract reused old artifact")
			}
			h.runAs(run, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
			if op := h.runAs(run, protocol.OpRunsSubmitArtifact, testArtifact(h.task(task).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			accept, ok, _ := h.claim("linux-1")
			if !ok || accept.Role != protocol.RoleAccept || accept.SpecVersion != 2 {
				t.Fatalf("fresh inherited acceptance=%+v", accept)
			}
			if op := h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if h.task(task).Lifecycle != protocol.TaskCompleted {
				t.Fatal("fresh inherited contract did not complete")
			}
		})
	}
}
