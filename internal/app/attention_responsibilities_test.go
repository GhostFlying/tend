package app

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func problemAttention(t *testing.T, h *harness, issue protocol.Issue) protocol.Attention {
	t.Helper()
	return attentionForTest(t, h, protocol.CreateAttentionPayload{IssueID: issue.IssueID, Kind: "decision", Title: "Which existing approach should continue?", Options: []string{"continue", "stop"}})
}

func supervisorHandle(t *testing.T, h *harness, issue protocol.Issue, a protocol.Attention, escalate bool, option string) protocol.Operation {
	t.Helper()
	turn, principal := startHostedIssueReview(t, h, issue)
	defer finishHostedReview(t, h, turn)
	r := a.Responsibility
	p := protocol.HandleAttentionPayload{ExpectedAssignmentEpoch: r.AssignmentEpoch, Version: a.QuestionVersion, Option: option, Reason: "Reviewed the original evidence and attempted remedies"}
	cmd := HandleAttention{Escalate: escalate}
	op, err := h.app.Execute(t.Context(), principal, h.env(cmd.Operation(), protocol.Target{Type: "attention", ID: a.AttentionID}, p, rev(r.Revision)), HostedCoordinationCommand{Command: cmd, ExecutionID: turn.RequestID, EffectSeq: 1})
	if err != nil {
		return protocol.Operation{Status: protocol.OpFailed, Error: protocol.Errorf(protocol.CodeOf(err), "rejected supervisor operation")}
	}
	return op
}

func childProblemIssue(t *testing.T, h *harness, parent protocol.Issue) protocol.Issue {
	t.Helper()
	parent = canonicalTaskIssue(t, h, parent.IssueID)
	p := protocol.CreateIssuePayload{ParentIssueID: parent.IssueID, ExpectedParentRevision: rev(parent.Revision), Plan: protocol.IssuePlan{Title: "Bounded child responsibility", Project: parent.Plan.Project, Intent: "Investigate the original problem", Stages: []protocol.IssueStage{}}}
	op := h.exec(owner, h.env("issues.create", protocol.Target{Type: "issue"}, p, nil), CanonicalOwnerIssueCommand{Action: "create"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var child protocol.Issue
	if err := json.Unmarshal(op.Result, &child); err != nil {
		t.Fatal(err)
	}
	return assignHostedManager(t, h, child)
}

func assertHumanProblemCount(t *testing.T, h *harness, want int) {
	t.Helper()
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		page, err := views.Attentions(t.Context(), tx.Q(), views.AttentionFilter{Status: "open", Recipient: "owner", ActionableOnly: true})
		if err != nil {
			return err
		}
		if len(page.Items) != want {
			t.Fatalf("human Inbox has %d problems, want %d: %+v", len(page.Items), want, page)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestResponsibilityEscalatesOneParentThenMainBeforeHuman(t *testing.T) {
	h := hostedManagerHarness(t)
	parent := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	child := childProblemIssue(t, h, parent)
	a := problemAttention(t, h, child)
	assertHumanProblemCount(t, h, 0)
	if a.Responsibility == nil || a.Responsibility.IssueID != child.IssueID {
		t.Fatal(a)
	}
	if op := supervisorHandle(t, h, child, a, true, ""); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	a = readAttentionForTest(t, h, a.AttentionID)
	if a.Responsibility.IssueID != parent.IssueID || len(a.Responsibility.History) != 1 {
		t.Fatal(a)
	}
	assertHumanProblemCount(t, h, 0)
	// Even the same Agent ref cannot handle a parent's problem from its child.
	outsider := childProblemIssue(t, h, parent)
	if op := supervisorHandle(t, h, outsider, a, false, "continue"); op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeForbidden {
		t.Fatal("child bypassed current recipient", op)
	}
	if op := supervisorHandle(t, h, parent, a, true, ""); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	a = readAttentionForTest(t, h, a.AttentionID)
	if a.Responsibility.RecipientKind != "main" || a.Responsibility.IssueID != "" {
		t.Fatal(a)
	}
	assertHumanProblemCount(t, h, 0)
	_, delivery := h.say("Inspect the problem already escalated to the top coordinator")
	turn := startHostedReview(t, h, "main", delivery)
	p := protocol.HandleAttentionPayload{ExpectedAssignmentEpoch: 0, Reason: "The top coordinator cannot resolve this without a user choice"}
	op := hostedEffect(t, h, mainAgent, turn, 1, protocol.Target{Type: "attention", ID: a.AttentionID}, rev(a.Responsibility.Revision), p, HandleAttention{Escalate: true})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	a = readAttentionForTest(t, h, a.AttentionID)
	if a.Responsibility.RecipientKind != "owner" || len(a.Responsibility.History) != 3 {
		t.Fatal(a)
	}
	assertHumanProblemCount(t, h, 1)
}

func TestResponsibilityStopsAtHumanIntermediateOwner(t *testing.T) {
	h := hostedManagerHarness(t)
	parent := createCanonicalOwnerTestIssue(t, h)
	child := childProblemIssue(t, h, parent)
	a := problemAttention(t, h, child)
	if op := supervisorHandle(t, h, child, a, true, ""); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	a = readAttentionForTest(t, h, a.AttentionID)
	if a.Responsibility.RecipientKind != "owner" || a.Responsibility.IssueID != parent.IssueID {
		t.Fatal(a)
	}
	assertHumanProblemCount(t, h, 1)
}

func TestResponsibilityOwnerResolvesBudgetWithoutHumanAuthorization(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	spec := testSpec
	spec.MaxImplementAttempts = 1
	p := canonicalOwnerTaskPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Title: "Collect investigation evidence", Spec: spec, IndependentAcceptance: &protocol.IndependentAcceptanceRequest{Required: true, Reason: "Exercise the owner recovery decision after actual review findings"}}
	op := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(issue.Revision)), CanonicalOwnerTaskCreate{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var created struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(op.Result, &created); err != nil {
		t.Fatal(err)
	}
	as, ok, _ := h.claim("linux-1")
	if !ok {
		t.Fatal("no implementation")
	}
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	h.runAs(as, protocol.OpRunsSubmitArtifact, testArtifact(h.task(created.TaskID).WorkspaceName), SubmitArtifact{})
	acc, ok, _ := h.claim("linux-1")
	if !ok {
		t.Fatal("no acceptance")
	}
	h.runAs(acc, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	if op = h.runAs(acc, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictChangesRequired, Issues: json.RawMessage(`["Capture actual scroll position"]`)}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var id string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT attention_id FROM attentions WHERE json_extract(body_json,'$.task_id')=? AND status='open'`, created.TaskID).Scan(&id)
	}); err != nil {
		t.Fatal(err)
	}
	a := readAttentionForTest(t, h, id)
	assertHumanProblemCount(t, h, 0)
	if got := h.executionTask(created.TaskID); got.Execution.State != protocol.ExecutionWaitingSupervisor {
		t.Fatal("internal problem still presented as human wait", got)
	}
	if op = supervisorHandle(t, h, issue, a, false, "raise the budget"); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	task := h.task(created.TaskID)
	if task.BlockReason != protocol.BlockNone || task.SpecVersion != 1 || task.ImplementGeneration != 2 {
		t.Fatal("original task did not continue", task)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		d, err := store.GetDecision(t.Context(), tx.Q(), a.DecisionID)
		if err != nil {
			return err
		}
		var answer decisionAnswer
		if err = json.Unmarshal([]byte(d.AnswerJSON), &answer); err != nil {
			return err
		}
		if answer.Actor == nil || answer.Actor.Kind == protocol.PrincipalOwner {
			t.Fatal("Agent answer impersonated user", answer)
		}
		m, err := store.GetMessage(t.Context(), tx.Q(), d.AnsweredMessageID)
		if err != nil {
			return err
		}
		if m.Author != "agent" {
			t.Fatal(m)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertHumanProblemCount(t, h, 0)
}

func TestResponsibilityWorkerContractNegotiationAndRunPreservation(t *testing.T) {
	for _, supervisor := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "supervisor"}[supervisor], func(t *testing.T) {
			h := hostedManagerHarness(t)
			runtime := testRuntime("supervisor-worker")
			runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityWorkerQuestions)
			if supervisor {
				runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilitySupervisorQuestions)
			}
			if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
			task := createCanonicalTaskForTest(t, h, issue)
			as, ok := h.claimInputRun(runtime)
			if !ok || as.SupervisorQuestions != supervisor {
				t.Fatal(as, ok)
			}
			h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
			question := askQuestion(t, h, as, scopeQuestion)
			var id string
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				return tx.Q().QueryRowContext(t.Context(), `SELECT attention_id FROM attentions WHERE decision_id=?`, question.DecisionID).Scan(&id)
			}); err != nil {
				t.Fatal(err)
			}
			a := readAttentionForTest(t, h, id)
			assertHumanProblemCount(t, h, 0)
			op := supervisorHandle(t, h, issue, a, false, scopeQuestion.Options[0])
			if !supervisor {
				if op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeInvalidTransition {
					t.Fatal("old worker silently accepted Agent authority", op)
				}
				return
			}
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			received, err := h.app.WorkerQuestion(t.Context(), runPrincipal(as.RunID, as.Role), question.DecisionID)
			if err != nil || received.Status != store.DecisionAnswered || received.RunID != as.RunID || received.ActionID != as.ActionID {
				t.Fatal(received, err)
			}
			if h.lease(as.RunID).Status != store.LeaseActive || h.run(as.RunID).ObservedState != protocol.RunRunning || h.task(task).SpecVersion != as.SpecVersion {
				t.Fatal("answer changed the original run or lease")
			}
		})
	}
}

func TestResponsibilityReplayReturnsReceiptAfterEscalationAndRejectsStaleCAS(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	a := problemAttention(t, h, issue)
	turn, principal := startHostedIssueReview(t, h, issue)
	cmd := HostedCoordinationCommand{Command: HandleAttention{Escalate: true}, ExecutionID: turn.RequestID, EffectSeq: 1}
	env := h.env("attentions.escalate", protocol.Target{Type: "attention", ID: a.AttentionID}, protocol.HandleAttentionPayload{ExpectedAssignmentEpoch: a.Responsibility.AssignmentEpoch, Reason: "Investigated; ask superior"}, rev(a.Responsibility.Revision))
	first := h.exec(principal, env, cmd)
	if first.Status != protocol.OpSucceeded {
		t.Fatal(first)
	}
	replay := h.exec(principal, env, cmd)
	if replay.Error == nil || replay.Error.Code != protocol.CodeDuplicateEffect {
		t.Fatal("same coordination effect executed again", replay)
	}
	current := readAttentionForTest(t, h, a.AttentionID)
	stale := h.exec(owner, h.env("attentions.escalate", env.Target, protocol.HandleAttentionPayload{Reason: "Stale follow-up"}, rev(a.Responsibility.Revision)), HandleAttention{Escalate: true})
	if stale.Status != protocol.OpFailed || stale.Error.Code != protocol.CodeStaleRevision {
		t.Fatal(stale)
	}
	if after := readAttentionForTest(t, h, a.AttentionID); after.Responsibility.Revision != current.Responsibility.Revision {
		t.Fatal("stale request changed route", after)
	}
}

func TestResponsibilityReparentReconcilesToCurrentChain(t *testing.T) {
	h := hostedManagerHarness(t)
	parent := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	other := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	child := childProblemIssue(t, h, parent)
	a := problemAttention(t, h, child)
	if op := supervisorHandle(t, h, child, a, true, ""); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	child = canonicalTaskIssue(t, h, child.IssueID)
	other = canonicalTaskIssue(t, h, other.IssueID)
	p := struct {
		canonicalOwnerControl
		ParentIssueID          string `json:"parent_issue_id"`
		ExpectedParentRevision *int64 `json:"expected_parent_revision"`
	}{canonicalOwnerControl{ExpectedAssignmentEpoch: child.AssignmentEpoch, Reason: "Move bounded responsibility"}, other.IssueID, rev(other.Revision)}
	canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, child, "reparent", p))
	a = readAttentionForTest(t, h, a.AttentionID)
	if a.Responsibility.IssueID != child.IssueID {
		t.Fatal("old ancestor retained problem", a)
	}
	if op := supervisorHandle(t, h, parent, a, false, "continue"); op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeForbidden {
		t.Fatal("former parent retained permission", op)
	}
	if op := supervisorHandle(t, h, child, a, true, ""); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	a = readAttentionForTest(t, h, a.AttentionID)
	if a.Responsibility.IssueID != other.IssueID {
		t.Fatal(a)
	}
	assertHumanProblemCount(t, h, 0)
}

func TestResponsibilityHandoffRebindsEpochWithoutDuplicateProblem(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	a := problemAttention(t, h, issue)
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	p := protocol.IssueAssignmentPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: owner.Subject}, Reason: "Human explicitly takes responsibility"}
	issue = canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.assign", protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(issue.Revision)), HostedIssueAssignment{}))
	finish := protocol.FinishIssueAssignmentPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, HandoffID: issue.PendingHandoffID, Reason: "No active execution remains"}
	issue = canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.finish_assignment", protocol.Target{Type: "issue", ID: issue.IssueID}, finish, rev(issue.Revision)), HostedIssueAssignment{Finish: true}))
	current := readAttentionForTest(t, h, a.AttentionID)
	if current.Responsibility.RecipientKind != "owner" || current.Responsibility.AssignmentEpoch != issue.AssignmentEpoch || current.DecisionID != a.DecisionID {
		t.Fatal(current)
	}
	assertHumanProblemCount(t, h, 1)
}

func TestResponsibilityReviewRunQuestionBelongsToIndependentReviewChild(t *testing.T) {
	h, r, issue := issueOwnerHarness(t)
	r.Capabilities = append(r.Capabilities, protocol.CapabilityWorkerQuestions, protocol.CapabilitySupervisorQuestions)
	r.Sequence++
	if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	task := ownerArtifact(t, h, r, issue)
	as := ownerWorkerClaim(t, h, r, protocol.RoleAccept)
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	q := askQuestion(t, h, as, scopeQuestion)
	contract := ownerContract(t, h, task.TaskID)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		a, found, err := attentionForDecision(t.Context(), tx.Q(), q.DecisionID)
		if err != nil {
			return err
		}
		if !found || a.Responsibility == nil || a.Responsibility.OriginIssueID != contract.ReviewIssueID || a.Responsibility.IssueID != contract.ReviewIssueID || a.IssueID == issue.IssueID {
			t.Fatal("review bypassed its independent child responsibility", a)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertHumanProblemCount(t, h, 0)
}

func TestResponsibilityNewParentRetiresFormerTopHumanQuestion(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	parent := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	a := problemAttention(t, h, issue)
	for range 2 {
		r := a.Responsibility
		op := h.exec(owner, h.env("attentions.escalate", protocol.Target{Type: "attention", ID: a.AttentionID}, protocol.HandleAttentionPayload{ExpectedAssignmentEpoch: r.AssignmentEpoch, Reason: "No available remedy in this responsibility"}, rev(r.Revision)), HandleAttention{Escalate: true})
		if op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		a = readAttentionForTest(t, h, a.AttentionID)
	}
	assertHumanProblemCount(t, h, 1)
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	parent = canonicalTaskIssue(t, h, parent.IssueID)
	p := struct {
		canonicalOwnerControl
		ParentIssueID          string `json:"parent_issue_id"`
		ExpectedParentRevision *int64 `json:"expected_parent_revision"`
	}{canonicalOwnerControl{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "New parent can investigate"}, parent.IssueID, rev(parent.Revision)}
	canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "reparent", p))
	a = readAttentionForTest(t, h, a.AttentionID)
	if a.Responsibility.RecipientKind != "agent" || a.Responsibility.IssueID != issue.IssueID {
		t.Fatal("obsolete top question still requests a human", a)
	}
	assertHumanProblemCount(t, h, 0)
}

func TestResponsibilityAgentAnswerNeverProvesHumanCompletion(t *testing.T) {
	for _, kind := range []string{"decision", "action"} {
		t.Run(kind, func(t *testing.T) {
			h := hostedManagerHarness(t)
			issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
			a := attentionForTest(t, h, protocol.CreateAttentionPayload{IssueID: issue.IssueID, Kind: kind, Title: "Review bounded evidence", Options: map[string][]string{"decision": {"continue"}}[kind]})
			turn, principal := startHostedIssueReview(t, h, issue)
			p := protocol.HandleAttentionPayload{ExpectedAssignmentEpoch: a.Responsibility.AssignmentEpoch, Version: a.QuestionVersion, Text: "Completed the authorized technical check", Reason: "Checked the original evidence"}
			if kind == "decision" {
				p.Option = "continue"
			}
			op := h.exec(principal, h.env("attentions.resolve", protocol.Target{Type: "attention", ID: a.AttentionID}, p, rev(a.Responsibility.Revision)), HostedCoordinationCommand{Command: HandleAttention{}, ExecutionID: turn.RequestID, EffectSeq: 1})
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				w, err := store.GetIssue(t.Context(), tx.Q(), issue.IssueID)
				if err != nil {
					return err
				}
				for _, reference := range []string{a.AttentionID, a.DecisionID} {
					if reference == "" {
						continue
					}
					evidence := []protocol.IssueEvidence{{Kind: protocol.WorkItemEvidenceOwnerDecision, Reference: reference, Summary: "Attempt to reuse Agent answer as human confirmation"}}
					x := &Exec{Principal: principal, Now: h.clock.Now()}
					if err := validateHostedIssueEvidence(t.Context(), x, tx.Q(), w, nil, evidence); err == nil {
						t.Fatalf("Agent answer %s became human evidence", reference)
					}
					if err := validateIssueEvidenceReferences(t.Context(), x, tx.Q(), w, evidence, nil, "evidence"); err == nil {
						t.Fatalf("raw validator accepted Agent answer %s as human evidence", reference)
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestResponsibilityFullHostNeverOversubscribes(t *testing.T) {
	h, r, issue := issueOwnerHarness(t)
	cfg := testConfig()
	cfg.Hosts[0].Slots = 1
	h.syncConfig(cfg)
	r.Capabilities = append(r.Capabilities, protocol.CapabilityWorkerQuestions, protocol.CapabilitySupervisorQuestions)
	r.Sequence++
	if op := h.versionedHello("linux-1", r); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := createOwnerExecution(t, h, issue, "implementer"); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	as := ownerWorkerClaim(t, h, r, protocol.RoleImplement)
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	askQuestion(t, h, as, scopeQuestion)
	if next := ownerClaim(t, h, r); next.RunID != "" {
		t.Fatal("blocked execution exceeded configured capacity", next)
	}
	if h.lease(as.RunID).Status != store.LeaseActive {
		t.Fatal("legacy execution lost its real lease")
	}
}

func TestResponsibilityRunRecoveryRejectsCapabilityDowngradeBeforeAnyQuestion(t *testing.T) {
	h := hostedManagerHarness(t)
	runtime := testRuntime("supervisor-recovery")
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityWorkerQuestions, protocol.CapabilityWorkerQuestionContext, protocol.CapabilitySupervisorQuestions)
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	createCanonicalTaskForTest(t, h, issue)
	as, ok := h.claimInputRun(runtime)
	if !ok || !as.SupervisorQuestions {
		t.Fatal(as, ok)
	}
	downgraded := *runtime
	downgraded.Capabilities = []string{protocol.CapabilityWorkerQuestions, protocol.CapabilityWorkerQuestionContext}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		err := checkQuestionRunRecovery(t.Context(), tx.Q(), "linux-1", &downgraded)
		if protocol.CodeOf(err) != protocol.CodeInvalidRequest {
			t.Fatal("capability downgrade accepted frozen Agent-answer contract", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestResponsibilityPausedBacklogDoesNotStarveActiveProblems(t *testing.T) {
	h := hostedManagerHarness(t)
	paused := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	paused = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, paused, "pause", canonicalOwnerControl{ExpectedAssignmentEpoch: paused.AssignmentEpoch, Reason: "Wait for external evidence"}))
	for range 100 {
		problemAttention(t, h, paused)
	}
	active := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	a := problemAttention(t, h, active)
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var notified int64
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT notified_revision FROM attention_responsibilities WHERE attention_id=?`, a.AttentionID).Scan(&notified); err != nil {
			return err
		}
		if notified != a.Responsibility.Revision {
			t.Fatal("inactive first page starved active problem", notified, a)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestResponsibilityRevisedActionFencesOldResolution(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	a := attentionForTest(t, h, protocol.CreateAttentionPayload{IssueID: issue.IssueID, Kind: "action", Title: "Inspect one file"})
	p := protocol.UpdateAttentionPayload{Title: "Inspect every affected file"}
	if op := h.exec(owner, h.env("attentions.update", protocol.Target{Type: "attention", ID: a.AttentionID}, p, rev(a.Revision)), AttentionCommand{Action: "update"}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	turn, principal := startHostedIssueReview(t, h, issue)
	resolve := protocol.HandleAttentionPayload{ExpectedAssignmentEpoch: a.Responsibility.AssignmentEpoch, Text: "Inspected the original one file", Reason: "Original evidence"}
	op := h.exec(principal, h.env("attentions.resolve", protocol.Target{Type: "attention", ID: a.AttentionID}, resolve, rev(a.Responsibility.Revision)), HostedCoordinationCommand{Command: HandleAttention{}, ExecutionID: turn.RequestID, EffectSeq: 1})
	if op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeStaleRevision {
		t.Fatal("old evidence resolved a changed action", op)
	}
	current := readAttentionForTest(t, h, a.AttentionID)
	if current.Status != "open" || current.Responsibility.Revision <= a.Responsibility.Revision {
		t.Fatal(current)
	}
}

func TestResponsibilityRevisedHumanQuestionHasCurrentAnswerControls(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	issue = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "pause", canonicalOwnerControl{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Pause execution without disabling actual human questions"}))
	a := attentionForTest(t, h, protocol.CreateAttentionPayload{IssueID: issue.IssueID, Kind: "decision", Title: "Original question", Options: []string{"old"}})
	p := protocol.UpdateAttentionPayload{Title: "Revised question", Options: []string{"new"}, DecisionVersion: 1}
	if op := h.exec(owner, h.env("attentions.update", protocol.Target{Type: "attention", ID: a.AttentionID}, p, rev(a.Revision)), AttentionCommand{Action: "update"}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	page, err := h.app.MessagingMessages(t.Context(), 0, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range page.Items {
		if m.Interaction != nil && m.Interaction.ObjectID == a.DecisionID && m.Interaction.State == "open" && m.Interaction.Version == 2 && m.Interaction.Question == p.Title {
			return
		}
	}
	t.Fatal("no current answer controls for revised human question", page)
}

func TestResponsibilityHumanControlsRecoverAfterInternalRoundTrip(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	a := problemAttention(t, h, issue)
	original := imQuestion(t, h, a.DecisionID)
	issue = assignHostedManager(t, h, canonicalTaskIssue(t, h, issue.IssueID))
	if m, err := h.app.MessagingMessage(t.Context(), original.MessageID); err != nil || m.Interaction == nil || m.Interaction.State != "superseded" {
		t.Fatal(m, err)
	}
	a = readAttentionForTest(t, h, a.AttentionID)
	for a.Responsibility.RecipientKind != "owner" {
		r := a.Responsibility
		p := protocol.HandleAttentionPayload{ExpectedAssignmentEpoch: r.AssignmentEpoch, Reason: "Cannot decide this remaining human requirement"}
		if op := h.exec(owner, h.env("attentions.escalate", protocol.Target{Type: "attention", ID: a.AttentionID}, p, rev(r.Revision)), HandleAttention{Escalate: true}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		a = readAttentionForTest(t, h, a.AttentionID)
	}
	m, err := h.app.MessagingMessage(t.Context(), original.MessageID)
	if err != nil || m.Interaction == nil || m.Interaction.State != "open" {
		t.Fatal("unchanged original human question stayed permanently superseded", m, err)
	}
}

func TestResponsibilityAcceptanceEnvironmentFailureBelongsToReviewChild(t *testing.T) {
	h, r, issue := issueOwnerHarness(t)
	task := ownerArtifact(t, h, r, issue)
	accept := ownerWorkerClaim(t, h, r, protocol.RoleAccept)
	h.runAs(accept, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	if op := h.runAs(accept, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictBlocked, Issues: json.RawMessage(`["Review environment unavailable"]`)}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	review := ownerContract(t, h, task.TaskID).ReviewIssueID
	d := h.openDecisions(task.TaskID)[0]
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		a, found, err := attentionForDecision(t.Context(), tx.Q(), d.DecisionID)
		if err != nil {
			return err
		}
		if !found || a.Responsibility == nil || a.Responsibility.OriginIssueID != review {
			t.Fatal("acceptance problem bypassed its independent review Issue", a)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func transferProblemIssueToHuman(t *testing.T, h *harness, issue protocol.Issue) protocol.Issue {
	t.Helper()
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	p := protocol.IssueAssignmentPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: owner.Subject}, Reason: "Human takes responsibility"}
	issue = canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.assign", protocol.Target{Type: "issue", ID: issue.IssueID}, p, rev(issue.Revision)), HostedIssueAssignment{}))
	finish := protocol.FinishIssueAssignmentPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, HandoffID: issue.PendingHandoffID, Reason: "Prior coordination has stopped"}
	return canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.finish_assignment", protocol.Target{Type: "issue", ID: issue.IssueID}, finish, rev(issue.Revision)), HostedIssueAssignment{Finish: true}))
}

func TestResponsibilityHumanHandoffInterruptsAlreadyEscalatedChain(t *testing.T) {
	for _, intermediate := range []bool{false, true} {
		t.Run(map[bool]string{false: "origin", true: "intermediate"}[intermediate], func(t *testing.T) {
			h := hostedManagerHarness(t)
			root := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
			parent := childProblemIssue(t, h, root)
			child := childProblemIssue(t, h, parent)
			a := problemAttention(t, h, child)
			if op := supervisorHandle(t, h, child, a, true, ""); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if intermediate {
				a = readAttentionForTest(t, h, a.AttentionID)
				if op := supervisorHandle(t, h, parent, a, true, ""); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			changed := child
			if intermediate {
				changed = parent
			}
			changed = transferProblemIssueToHuman(t, h, changed)
			a = readAttentionForTest(t, h, a.AttentionID)
			if a.Responsibility.RecipientKind != "owner" || a.Responsibility.IssueID != changed.IssueID || a.Responsibility.AssignmentEpoch != changed.AssignmentEpoch {
				t.Fatal("human handoff did not interrupt existing delegation", a)
			}
			if op := supervisorHandle(t, h, root, a, false, "continue"); op.Status != protocol.OpFailed {
				t.Fatal("Agent bypassed human intervention", op)
			}
			assertHumanProblemCount(t, h, 1)
		})
	}
}

func TestResponsibilityUpdateReceiptContainsCommittedRevision(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	a := attentionForTest(t, h, protocol.CreateAttentionPayload{IssueID: issue.IssueID, Kind: "action", Title: "Original bounded check"})
	op := h.exec(owner, h.env("attentions.update", protocol.Target{Type: "attention", ID: a.AttentionID}, protocol.UpdateAttentionPayload{Title: "Revised bounded check"}, rev(a.Revision)), AttentionCommand{Action: "update"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var returned protocol.Attention
	if err := json.Unmarshal(op.Result, &returned); err != nil {
		t.Fatal(err)
	}
	current := readAttentionForTest(t, h, a.AttentionID)
	if returned.Responsibility.Revision != current.Responsibility.Revision || returned.Responsibility.Revision <= a.Responsibility.Revision {
		t.Fatal("successful receipt returned stale control", returned, current)
	}
}

func TestResponsibilityActualHumanRunQuestionOverridesUnrelatedInternalProblem(t *testing.T) {
	h := hostedManagerHarness(t)
	runtime := testRuntime("mixed-questions")
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityWorkerQuestions)
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	task := createCanonicalTaskForTest(t, h, issue)
	as, ok := h.claimInputRun(runtime)
	if !ok {
		t.Fatal("no execution")
	}
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	question := askQuestion(t, h, as, scopeQuestion)
	var a protocol.Attention
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		a, _, err = attentionForDecision(t.Context(), tx.Q(), question.DecisionID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		r := a.Responsibility
		p := protocol.HandleAttentionPayload{ExpectedAssignmentEpoch: r.AssignmentEpoch, Reason: "The original run requires a human answer"}
		if op := h.exec(owner, h.env("attentions.escalate", protocol.Target{Type: "attention", ID: a.AttentionID}, p, rev(r.Revision)), HandleAttention{Escalate: true}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		a = readAttentionForTest(t, h, a.AttentionID)
	}
	attentionForTest(t, h, protocol.CreateAttentionPayload{IssueID: issue.IssueID, TaskID: task, Kind: "decision", Title: "Nonblocking internal investigation", Options: []string{"continue"}})
	if state := h.executionTask(task).Execution.State; state != protocol.ExecutionWaitingOwner {
		t.Fatal("unrelated problem hid actual human answer requirement", state)
	}
}

func TestResponsibilityReparentWithinSameAncestorInvalidatesRoute(t *testing.T) {
	h := hostedManagerHarness(t)
	root := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	child := childProblemIssue(t, h, root)
	other := childProblemIssue(t, h, root)
	a := problemAttention(t, h, child)
	if op := supervisorHandle(t, h, child, a, true, ""); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	a = readAttentionForTest(t, h, a.AttentionID)
	before := a.Responsibility.Revision
	child = canonicalTaskIssue(t, h, child.IssueID)
	other = canonicalTaskIssue(t, h, other.IssueID)
	p := struct {
		canonicalOwnerControl
		ParentIssueID          string `json:"parent_issue_id"`
		ExpectedParentRevision *int64 `json:"expected_parent_revision"`
	}{canonicalOwnerControl{ExpectedAssignmentEpoch: child.AssignmentEpoch, Reason: "Move under new immediate parent"}, other.IssueID, rev(other.Revision)}
	canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, child, "reparent", p))
	current := readAttentionForTest(t, h, a.AttentionID)
	if current.Responsibility.Revision == before && current.Responsibility.IssueID == root.IssueID {
		t.Errorf("reparent preserved old escalation and CAS: %+v", current.Responsibility)
	}
	if op := supervisorHandle(t, h, root, a, false, "continue"); op.Status == protocol.OpSucceeded {
		t.Error("old ancestor answered despite new unconsulted immediate parent")
	}
}

func TestResponsibilityWithdrawWakesQuestionOrigin(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	a := attentionForTest(t, h, protocol.CreateAttentionPayload{IssueID: issue.IssueID, Kind: "action", Title: "Perform bounded check"})
	before := h.count("background_sources")
	env := h.env("attentions.withdraw", protocol.Target{Type: "attention", ID: a.AttentionID}, nil, rev(a.Revision))
	env.Reason = "This check is no longer necessary"
	if op := h.exec(owner, env, AttentionCommand{Action: "withdraw"}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if after := h.count("background_sources"); after == before {
		t.Fatalf("withdrawal created no wake for the responsible Issue (sources stayed at %d)", before)
	}
}

func TestResponsibilityAgentAnswersQuestionStillDisabledHumanControlStatus(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	a := problemAttention(t, h, issue)
	original := imQuestion(t, h, a.DecisionID)
	issue = assignHostedManager(t, h, canonicalTaskIssue(t, h, issue.IssueID))
	a = readAttentionForTest(t, h, a.AttentionID)
	if op := supervisorHandle(t, h, issue, a, false, "continue"); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	m, err := h.app.MessagingMessage(t.Context(), original.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if m.Interaction != nil && m.Interaction.State != "answered" {
		t.Errorf("original human card does not show actual answer after agent resolution: %+v", m.Interaction)
	}
}

func TestResponsibilityHumanHandoffUpdatesDeliveredTelegramQuestion(t *testing.T) {
	h := hostedManagerHarness(t)
	bindIMTelegram(t, h)
	issue := createCanonicalOwnerTestIssue(t, h)
	a := problemAttention(t, h, issue)
	original := imQuestion(t, h, a.DecisionID)
	for {
		delivery, _, ok, err := h.app.NextDelivery(t.Context(), MessagingDestination)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		if err = h.app.FinishDelivery(t.Context(), delivery, DeliveryResult{Outcome: Delivered, ExternalMessageID: 99}); err != nil {
			t.Fatal(err)
		}
	}
	_ = assignHostedManager(t, h, canonicalTaskIssue(t, h, issue.IssueID))
	current, err := h.app.MessagingMessage(t.Context(), original.MessageID)
	if err != nil || current.Interaction.State != "superseded" {
		t.Fatal(current, err)
	}
	update, _, ok, err := h.app.NextDelivery(t.Context(), MessagingInteractionDestination)
	if err != nil || !ok {
		t.Fatalf("human question became internal without editing delivered Telegram card: found=%v err=%v update=%+v", ok, err, update)
	}
}

func TestResponsibilityOwnerActionResolutionWakesMain(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	a := attentionForTest(t, h, protocol.CreateAttentionPayload{IssueID: issue.IssueID, Kind: "action", Title: "Complete a manual action"})
	before := h.count("background_sources")
	op := h.exec(owner, h.env("attentions.confirm", protocol.Target{Type: "attention", ID: a.AttentionID}, protocol.ConfirmAttentionPayload{Summary: "Manual action complete"}, rev(a.Revision)), AttentionCommand{Action: "confirm"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if after := h.count("background_sources"); after <= before {
		t.Fatalf("human completion created no followup wake: sources %d -> %d", before, after)
	}
}

func TestResponsibilityHistoricalReviewQuestionAnswerWakesReviewOrigin(t *testing.T) {
	h, runtime, issue := issueOwnerHarness(t)
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityWorkerQuestions)
	runtime.Sequence++
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	ownerArtifact(t, h, runtime, issue)
	as := ownerWorkerClaim(t, h, runtime, protocol.RoleAccept)
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	question := askQuestion(t, h, as, scopeQuestion)
	var a protocol.Attention
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		a, _, err = attentionForDecision(t.Context(), tx.Q(), question.DecisionID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	origin := a.Responsibility.OriginIssueID
	// A pre-migration Attention keeps its original implementation-Issue reference,
	// while the new responsibility points to the historical review child.
	a.IssueID = issue.IssueID
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		raw, _ := json.Marshal(a)
		_, err := tx.Exec(t.Context(), `UPDATE attentions SET issue_id=?,body_json=? WHERE attention_id=?`, a.IssueID, string(raw), a.AttentionID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for a.Responsibility.RecipientKind != "owner" {
		r := a.Responsibility
		op := h.exec(owner, h.env("attentions.escalate", protocol.Target{Type: "attention", ID: a.AttentionID}, protocol.HandleAttentionPayload{ExpectedAssignmentEpoch: r.AssignmentEpoch, Reason: "Needs human judgment"}, rev(r.Revision)), HandleAttention{Escalate: true})
		if op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		a = readAttentionForTest(t, h, a.AttentionID)
	}
	op := h.exec(owner, h.env("decisions.respond", protocol.Target{Type: "decision", ID: a.DecisionID}, protocol.AnswerDecisionPayload{Version: question.Version, Option: scopeQuestion.Options[0]}, nil), AnswerDecision{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var originWakes, implementationWakes int
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM background_sources s JOIN background_batches b USING(batch_id) WHERE b.work_item_id=? AND s.kind='decision.answered'`, origin).Scan(&originWakes); err != nil {
			return err
		}
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM background_sources s JOIN background_batches b USING(batch_id) WHERE b.work_item_id=? AND s.kind='decision.answered'`, issue.IssueID).Scan(&implementationWakes); err != nil {
			return err
		}
		if originWakes == 0 {
			t.Errorf("historical review answer went to implementation, not origin: review wakes=%d implementation wakes=%d", originWakes, implementationWakes)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestResponsibilityIntermediateAgentHandoffInvalidatesOldEscalation(t *testing.T) {
	h := hostedManagerHarness(t)
	c := hostedConfigurationFixture()
	c.Revision = 2
	other := c.Definitions.Agents[0]
	other.LocalID = "replacement"
	other.DisplayName = "Replacement supervisor"
	c.Definitions.Agents = append(c.Definitions.Agents, other)
	op, prepared := acceptHostedFixture(t, h, c, 1)
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	h.app = New(h.store, h.ids, h.clock.Now, WithHostedConfiguration(prepared))
	root := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	parent := childProblemIssue(t, h, root)
	child := childProblemIssue(t, h, parent)
	a := problemAttention(t, h, child)
	if op := supervisorHandle(t, h, child, a, true, ""); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	a = readAttentionForTest(t, h, a.AttentionID)
	if op := supervisorHandle(t, h, parent, a, true, ""); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	a = readAttentionForTest(t, h, a.AttentionID)
	old := a
	parent = canonicalTaskIssue(t, h, parent.IssueID)
	p := protocol.IssueAssignmentPayload{ExpectedAssignmentEpoch: parent.AssignmentEpoch, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeAgent, Ref: "server-hosted/replacement"}, Reason: "New supervisor takes responsibility"}
	parent = canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.assign", protocol.Target{Type: "issue", ID: parent.IssueID}, p, rev(parent.Revision)), HostedIssueAssignment{}))
	parent = canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.finish_assignment", protocol.Target{Type: "issue", ID: parent.IssueID}, protocol.FinishIssueAssignmentPayload{ExpectedAssignmentEpoch: parent.AssignmentEpoch, HandoffID: parent.PendingHandoffID, Reason: "Previous supervisor stopped"}, rev(parent.Revision)), HostedIssueAssignment{Finish: true}))
	a = readAttentionForTest(t, h, a.AttentionID)
	if a.Responsibility.Revision == old.Responsibility.Revision {
		t.Errorf("intermediate handoff retained old responsibility CAS: recipient=%s epoch=%d", a.Responsibility.IssueID, a.Responsibility.AssignmentEpoch)
	}
	if op := supervisorHandle(t, h, root, old, false, "continue"); op.Status == protocol.OpSucceeded {
		t.Error("old escalated route can still resolve without new intermediate supervisor")
	}
}
