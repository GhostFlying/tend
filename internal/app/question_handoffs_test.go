package app

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestQuestionHandoffHumanOwnerResumes(t *testing.T) {
	h := newHarness(t)
	runtime := testRuntime("human-handoff-runner")
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityWorkerQuestions, protocol.CapabilityQuestionHandoffs)
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	issue := createCanonicalOwnerTestIssue(t, h)
	task := createCanonicalTaskForTest(t, h, issue)
	as, ok := h.claimInputRun(runtime)
	if !ok || !as.QuestionHandoffs {
		t.Fatal(as)
	}
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	q := askQuestion(t, h, as, scopeQuestion)
	if op := h.runAs(as, protocol.OpRunsYieldQuestion, protocol.YieldQuestionPayload{DecisionID: q.DecisionID}, YieldQuestion{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := answerQuestion(t, h, q, scopeQuestion.Options[0], ""); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	next, ok := h.claimInputRun(runtime)
	if !ok || next.TaskID != task || next.QuestionContinuation == nil {
		t.Fatalf("human answer did not resume frozen agreement: %+v, %+v", next, h.executionTask(task))
	}
}

func TestQuestionHandoffResumesExactAgreement(t *testing.T) {
	for _, role := range []protocol.RunRole{protocol.RoleImplement, protocol.RoleAccept} {
		for _, answerFirst := range []bool{false, true} {
			t.Run(string(role)+map[bool]string{false: "/exit-first", true: "/answer-first"}[answerFirst], func(t *testing.T) {
				h := hostedManagerHarness(t)
				cfg := testConfig()
				cfg.Hosts[0].Slots = 1
				h.syncConfig(cfg)
				runtime := testRuntime("handoff-runner")
				runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityWorkerQuestions, protocol.CapabilitySupervisorQuestions, protocol.CapabilityQuestionHandoffs)
				if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
				taskID := createCanonicalTaskForTest(t, h, issue)
				as, ok := h.claimInputRun(runtime)
				if !ok || !as.QuestionHandoffs {
					t.Fatal(as)
				}
				h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
				if role == protocol.RoleAccept {
					if op := h.runAs(as, protocol.OpRunsSubmitArtifact, testArtifact(h.task(taskID).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
						t.Fatal(op)
					}
					as, ok = h.claimInputRun(runtime)
					if !ok || as.Role != role {
						t.Fatal(as)
					}
					h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
				}
				old := *runtime
				old.Capabilities = []string{protocol.CapabilityWorkerQuestions, protocol.CapabilitySupervisorQuestions, protocol.CapabilityWorkerQuestionContext}
				old.Sequence++
				if op := h.versionedHello("linux-1", &old); op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeInvalidRequest {
					t.Fatal("frozen handoff silently downgraded", op)
				}
				before := h.task(as.TaskID)
				q := askQuestion(t, h, as, scopeQuestion)
				if h.run(as.RunID).ObservedState != protocol.RunRunning || h.lease(as.RunID).Status != store.LeaseActive {
					t.Fatal("question alone fabricated process exit")
				}
				if _, ok := h.claimInputRun(runtime); ok {
					t.Fatal("handoff allocated a second slot before process exit")
				}
				if answerFirst {
					if op := answerHandoff(t, h, issue, q); op.Status != protocol.OpSucceeded {
						t.Fatal(op)
					}
					if _, ok := h.claimInputRun(runtime); ok {
						t.Fatal("early answer bypassed source process")
					}
				}
				envelope := h.env(protocol.OpRunsYieldQuestion, runTarget(as.RunID), protocol.YieldQuestionPayload{DecisionID: q.DecisionID, ThreadID: "original-session"}, nil)
				op, err := h.app.Execute(t.Context(), runPrincipal(as.RunID, as.Role), envelope, YieldQuestion{})
				if err != nil || op.Status != protocol.OpSucceeded {
					t.Fatal(op, err)
				}
				replay, err := h.app.Execute(t.Context(), runPrincipal(as.RunID, as.Role), envelope, YieldQuestion{})
				if err != nil || string(replay.Result) != string(op.Result) {
					t.Fatal("lost exit receipt did not replay", replay, err)
				}
				if h.run(as.RunID).ObservedState != protocol.RunFinished || h.lease(as.RunID).Status != store.LeaseReleased {
					t.Fatal("handoff retained execution capacity")
				}
				if !answerFirst {
					if state := h.executionTask(as.TaskID).Execution.State; state != protocol.ExecutionWaitingSupervisor {
						t.Fatal("stopped handoff disappeared from execution views", state)
					}
					if _, ok := h.claimInputRun(runtime); ok {
						t.Fatal("unanswered handoff resumed")
					}
					if op := answerHandoff(t, h, issue, q); op.Status != protocol.OpSucceeded {
						t.Fatal(op)
					}
				}
				next, ok := h.claimInputRun(runtime)
				if !ok || next.RunID == as.RunID || next.ActionID == as.ActionID || next.Generation != as.Generation || next.Role != as.Role || next.SpecVersion != as.SpecVersion || next.Workspace.ID != as.Workspace.ID {
					t.Fatal("handoff changed the delivery agreement", as, next)
				}
				if h.task(as.TaskID) != before {
					t.Fatal("question consumed an attempt or changed task state", before, h.task(as.TaskID))
				}
				if next.QuestionContinuation == nil || next.QuestionContinuation.RunID != as.RunID || next.QuestionContinuation.ThreadID != "original-session" || len(next.Questions) != 1 || next.Questions[0].DecisionID != q.DecisionID || next.Questions[0].Status != store.DecisionAnswered {
					t.Fatal("continuation lost original problem or context", next)
				}
				if _, ok := h.claimInputRun(runtime); ok {
					t.Fatal("duplicate continuation")
				}
				if op := h.runAs(next, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				followup := scopeQuestion
				followup.QuestionKey = "different-blocker"
				followup.Question = "Which authorized evidence source should continue?"
				second := askQuestion(t, h, next, followup)
				if op := h.runAs(next, protocol.OpRunsYieldQuestion, protocol.YieldQuestionPayload{DecisionID: second.DecisionID, ThreadID: "second-session"}, YieldQuestion{}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				if op := answerHandoff(t, h, issue, second); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				third, ok := h.claimInputRun(runtime)
				if !ok || third.Generation != as.Generation || len(third.Questions) != 2 || third.Questions[0].DecisionID != q.DecisionID || third.Questions[1].DecisionID != second.DecisionID {
					t.Fatal("multiple handoffs lost context or consumed a task attempt", third)
				}

				if op := h.runAs(third, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				blocked := followup
				blocked.QuestionKey = "cancelled-question"
				cancelled := askQuestion(t, h, third, blocked)
				if op := h.runAs(third, protocol.OpRunsYieldQuestion, protocol.YieldQuestionPayload{DecisionID: cancelled.DecisionID}, YieldQuestion{}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				if op := h.exec(owner, h.env("tasks.cancel", taskTarget(as.TaskID), map[string]any{}, rev(h.task(as.TaskID).ControlRevision)), CancelTask{}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				if op := answerHandoff(t, h, issue, cancelled); op.Status != protocol.OpFailed {
					t.Fatal("cancelled handoff accepted an answer", op)
				}
				if _, ok := h.claimInputRun(runtime); ok {
					t.Fatal("cancelled agreement resumed")
				}

			})
		}
	}
}

func TestQuestionHandoffOwnerExecutionAutomaticallyAsksParent(t *testing.T) {
	h, r, issue := issueOwnerHarness(t)
	cfg := testConfig()
	cfg.Hosts[0].Slots = 1
	h.syncConfig(cfg)
	parent := createCanonicalOwnerTestIssue(t, h)
	request := protocol.RequestIssueManagerPayload{CategoryID: "implementer", AgentID: "linux-1/alpha", ExpectedAssignmentEpoch: parent.AssignmentEpoch, Reason: "Accept upper Issue responsibility"}
	if op := h.exec(owner, h.env(protocol.OpIssueRequestOwner, protocol.Target{Type: "issue", ID: parent.IssueID}, request, rev(parent.Revision)), RequestIssueOwner{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	stopOwnerClaim(t, h, ownerClaim(t, h, r))
	parent = canonicalTaskIssue(t, h, parent.IssueID)
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	parent = canonicalTaskIssue(t, h, parent.IssueID)
	p := struct {
		canonicalOwnerControl
		ParentIssueID          string `json:"parent_issue_id"`
		ExpectedParentRevision *int64 `json:"expected_parent_revision"`
	}{canonicalOwnerControl{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Use the actual parent responsibility"}, parent.IssueID, rev(parent.Revision)}
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "reparent", p))

	r.Capabilities = append(r.Capabilities, protocol.CapabilityWorkerQuestions, protocol.CapabilitySupervisorQuestions, protocol.CapabilityQuestionHandoffs)
	r.Sequence++
	if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := createOwnerExecution(t, h, issue, "implementer"); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	as := ownerWorkerClaim(t, h, r, protocol.RoleImplement)
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	q := askQuestion(t, h, as, scopeQuestion)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var raw string
		err := tx.Q().QueryRowContext(t.Context(), `SELECT r.body_json FROM attention_responsibilities r JOIN attentions a USING(attention_id) WHERE a.decision_id=?`, q.DecisionID).Scan(&raw)
		if err != nil {
			return err
		}
		var routing protocol.AttentionResponsibility
		if err := json.Unmarshal([]byte(raw), &routing); err != nil {
			return err
		}
		if routing.OriginIssueID != issue.IssueID || routing.IssueID != parent.IssueID || routing.RecipientKind != "agent" {
			t.Fatal("execution did not ask its actual parent owner", routing)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if next := ownerClaim(t, h, r); next.RunID != "" {
		t.Fatal("upper owner exceeded source capacity", next)
	}
	if op := h.runAs(as, protocol.OpRunsYieldQuestion, protocol.YieldQuestionPayload{DecisionID: q.DecisionID}, YieldQuestion{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	found := false
	for range 5 {
		next := ownerClaim(t, h, r)
		if next.RunID == "" {
			break
		}
		if next.Manager == nil {
			t.Fatal("unanswered source resumed", next)
		}
		if next.Manager.IssueID == parent.IssueID {
			found = true
		}
		if another := ownerClaim(t, h, r); another.RunID != "" {
			t.Fatal("parallel owner execution exceeded one slot", another)
		}
		stopOwnerClaim(t, h, next)
		if found {
			break
		}
	}
	if !found {
		t.Fatal("source exit did not admit the upper Issue owner")
	}
}

func answerHandoff(t *testing.T, h *harness, issue protocol.Issue, q protocol.WorkerQuestion) protocol.Operation {
	t.Helper()
	var id string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT attention_id FROM attentions WHERE decision_id=?`, q.DecisionID).Scan(&id)
	}); err != nil {
		t.Fatal(err)
	}
	return supervisorHandle(t, h, issue, readAttentionForTest(t, h, id), false, scopeQuestion.Options[0])
}

func TestQuestionHandoffRetainsNamedExecutor(t *testing.T) {
	h, r, p, _ := inventorySetup(t)
	p.Agents[0].Categories = []string{"implementer"}
	p.Agents[1].Categories = []string{"reviewer"}
	_, hash, err := protocol.CanonicalAgentInventory(p)
	if err != nil {
		t.Fatal(err)
	}
	if op := h.exec(runner("linux-1"), inventoryRequest(h, r, p, hash, 1, 0, "handoff-snapshot"), RegisterAgentInventory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	r.Capabilities = append(r.Capabilities, protocol.CapabilityAgentExecution, protocol.CapabilityAgentContinuation, protocol.CapabilityWorkerQuestions, protocol.CapabilitySupervisorQuestions, protocol.CapabilityQuestionHandoffs)
	r.JournalVersion = 13
	for _, a := range p.Agents {
		r.AgentExecutors = append(r.AgentExecutors, protocol.AgentExecutor{Available: true, Pin: protocol.RunExecutionPin{Identity: a.Identity, AgentRevision: a.Revision, Runtime: a.Binding, InventoryRevision: 1, SnapshotID: "handoff-snapshot", PublicSHA256: hash, Profile: protocol.ExecutionProfile{Model: a.RequestedModel, Effort: a.RequestedEffort}, Worker: protocol.WorkerAppServer, BackendID: "git"}})
	}
	r.Sequence++
	if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	spec := testSpec
	spec.AgentSelection = &protocol.TaskAgentSelection{Implementation: protocol.AgentRequirement{CategoryID: "implementer"}, Acceptance: protocol.AgentRequirement{CategoryID: "reviewer"}}
	issue := createCanonicalOwnerTestIssue(t, h)
	if op := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, canonicalOwnerTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "named handoff", Spec: spec}, rev(issue.Revision)), CanonicalOwnerTaskCreate{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	as, ok := h.claimInputRun(r)
	if !ok || as.Execution == nil {
		t.Fatal(as)
	}
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	q := askQuestion(t, h, as, scopeQuestion)
	if op := h.runAs(as, protocol.OpRunsYieldQuestion, protocol.YieldQuestionPayload{DecisionID: q.DecisionID}, YieldQuestion{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		pins, e := store.RequiredAgentExecutions(t.Context(), tx.Q(), "linux-1")
		if e != nil {
			return e
		}
		if !slices.Contains(pins, *as.Execution) {
			t.Errorf("stopped pending handoff lost executor retention: %+v", pins)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Retain the requested snapshot even after it leaves the fresh inventory.
	r.AgentContinuations = []protocol.AgentExecutor{{Available: true, Pin: *as.Execution}}
	p.Agents = p.Agents[1:]
	_, hash, err = protocol.CanonicalAgentInventory(p)
	if err != nil {
		t.Fatal(err)
	}
	if op := h.exec(runner("linux-1"), inventoryRequest(h, r, p, hash, 2, 1, "handoff-new-snapshot"), RegisterAgentInventory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	r.AgentExecutors = nil
	a := p.Agents[0]
	r.AgentExecutors = append(r.AgentExecutors, protocol.AgentExecutor{Available: true, Pin: protocol.RunExecutionPin{Identity: a.Identity, AgentRevision: a.Revision, Runtime: a.Binding, InventoryRevision: 2, SnapshotID: "handoff-new-snapshot", PublicSHA256: hash, Profile: protocol.ExecutionProfile{Model: a.RequestedModel, Effort: a.RequestedEffort}, Worker: protocol.WorkerAppServer, BackendID: "git"}})
	r.Sequence++
	if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.exec(owner, h.env("decisions.respond", protocol.Target{Type: "decision", ID: q.DecisionID}, protocol.AnswerDecisionPayload{Version: q.Version, Option: scopeQuestion.Options[0]}, nil), AnswerDecision{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	op := h.exec(runner("linux-1"), h.env(protocol.OpRunnerClaim, protocol.Target{Type: "host", ID: "linux-1"}, protocol.ClaimPayload{Runtime: r, WorkspaceProtocol: 1}, nil), Claim{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var claimed claimResult
	if err := json.Unmarshal(op.Result, &claimed); err != nil {
		t.Fatal(err)
	}
	if claimed.Assignment.RunID == "" {
		t.Fatal("answered handoff cannot claim its successor after source definition removal")
	}
}

func TestQuestionHandoffLegacySingleSlotHasReachableUpperResponsibility(t *testing.T) {
	h, r, issue := issueOwnerHarness(t)
	cfg := testConfig()
	cfg.Hosts[0].Slots = 1
	h.syncConfig(cfg)
	r.Capabilities = append(r.Capabilities, protocol.CapabilityWorkerQuestions)
	r.Sequence++
	if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := createOwnerExecution(t, h, issue, "implementer"); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	as := ownerWorkerClaim(t, h, r, protocol.RoleImplement)
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	before := h.count("background_sources")
	q := askQuestion(t, h, as, scopeQuestion)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		a, found, err := attentionForDecision(t.Context(), tx.Q(), q.DecisionID)
		if err != nil {
			return err
		}
		if !found || a.Responsibility == nil || a.Responsibility.RecipientKind != "main" || len(a.Responsibility.History) != 1 || a.Responsibility.History[0].IssueID != issue.IssueID {
			t.Fatal("legacy single-slot question has no reachable supervisor", a)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if h.count("background_sources") <= before {
		t.Fatal("upper coordinator was not durably woken")
	}
	if h.lease(as.RunID).Status != store.LeaseActive || as.QuestionHandoffs {
		t.Fatal("legacy run contract was changed")
	}
	if next := ownerClaim(t, h, r); next.RunID != "" {
		t.Fatal("legacy compatibility exceeded configured slots", next)
	}
	assertHumanProblemCount(t, h, 0)
}

func TestFault_QuestionContinuationRejectsReassignedReviewEpoch(t *testing.T) {
	h, runtime, issue := issueOwnerHarness(t)
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityWorkerQuestions, protocol.CapabilitySupervisorQuestions, protocol.CapabilityQuestionHandoffs)
	runtime.Sequence++
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	task := ownerArtifact(t, h, runtime, issue)
	as := ownerWorkerClaim(t, h, runtime, protocol.RoleAccept)
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	question := askQuestion(t, h, as, scopeQuestion)
	if op := h.runAs(as, protocol.OpRunsYieldQuestion, protocol.YieldQuestionPayload{DecisionID: question.DecisionID}, YieldQuestion{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := answerQuestion(t, h, question, scopeQuestion.Options[0], ""); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	review := canonicalTaskIssue(t, h, ownerContract(t, h, task.TaskID).ReviewIssueID)
	review = transferProblemIssueToHuman(t, h, review)
	request := protocol.RequestIssueManagerPayload{CategoryID: "reviewer", AgentID: "linux-1/beta", ExpectedAssignmentEpoch: review.AssignmentEpoch, Reason: "Reassign review under a new responsibility epoch"}
	if op := h.exec(owner, h.env(protocol.OpIssueRequestOwner, protocol.Target{Type: "issue", ID: review.IssueID}, request, rev(review.Revision)), RequestIssueOwner{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	view := h.executionTask(task.TaskID)
	if view.Execution == nil || view.Execution.QueueReason == nil || !strings.Contains(view.Execution.QueueReason.Message, "Issue responsibility changed") {
		t.Fatalf("stale review continuation is not fenced: %+v", view.Execution)
	}
	for range 3 {
		next := ownerClaim(t, h, runtime)
		if next.RunID == "" {
			return
		}
		if next.Role == protocol.RoleIssueManager {
			stopOwnerClaim(t, h, next)
			continue
		}
		t.Fatalf("old answer crossed responsibility epochs: old=%+v next=%+v", as.Responsibility, next.Responsibility)
	}
	if count := h.count("run_question_handoffs WHERE successor_run_id IS NOT NULL"); count != 0 {
		t.Fatal("stale answer created a successor", count)
	}
}
