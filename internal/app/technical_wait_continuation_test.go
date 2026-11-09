package app

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// Exercise a real same-Agent management handoff while keeping the original
// execution Task, stopped native question and frozen executor untouched.
func stoppedOwnerContinuation(t *testing.T) (continuationFixture, protocol.Issue) {
	t.Helper()
	h, runtime, issue := issueOwnerHarness(t)
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityWorkerQuestions, protocol.CapabilityQuestionHandoffs, protocol.CapabilitySupervisorQuestions)
	runtime.Sequence++
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	op := createOwnerExecution(t, h, issue, "implementer")
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var created taskResult
	if err := json.Unmarshal(op.Result, &created); err != nil {
		t.Fatal(err)
	}
	as := ownerWorkerClaim(t, h, runtime, protocol.RoleImplement)
	if op := h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	f := continuationFixture{h: h, runtime: runtime, task: created.TaskID, as: as, q: askQuestion(t, h, as, scopeQuestion)}
	f.answer(t, &protocol.DecisionContinuationPolicy{Mode: protocol.DecisionWait, Reason: "Wait for the existing candidate review"})
	f.stop(t)
	return f, canonicalTaskIssue(t, h, issue.IssueID)
}

func replaceContinuationManager(t *testing.T, f continuationFixture, issue protocol.Issue, different ...bool) protocol.Issue {
	t.Helper()
	issue = canonicalTaskIssue(t, f.h, issue.IssueID)
	agent, category := issue.Assignee.Ref, "implementer"
	if len(different) > 0 && different[0] {
		agent, category = "linux-1/beta", "reviewer"
	}
	op := f.h.exec(owner, f.h.env(protocol.OpIssueRequestOwner, protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.RequestIssueManagerPayload{CategoryID: category, AgentID: agent, Replace: true, ExpectedAssignmentEpoch: issue.AssignmentEpoch, Reason: "Restore the same responsible Agent after maintenance"}, rev(issue.Revision)), RequestIssueOwner{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	as := ownerClaim(t, f.h, f.runtime)
	if as.Role != protocol.RoleIssueManager {
		t.Fatal("replacement manager did not claim", as)
	}
	stopOwnerClaim(t, f.h, as)
	return canonicalTaskIssue(t, f.h, issue.IssueID)
}

func TestStoppedResponsibilityAdoptionResumesAndClaimsOriginalTask(t *testing.T) {
	f, issue := stoppedOwnerContinuation(t)
	beforeTask, beforeRun := f.h.task(f.task), f.h.run(f.as.RunID)
	var frozen store.TaskOwnerContract
	if err := f.h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		frozen, err = store.GetTaskOwnerContract(t.Context(), tx.Q(), f.task)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	issue = replaceContinuationManager(t, f, issue)
	if issue.AssignmentEpoch != frozen.AssignmentEpoch+2 {
		t.Fatal("fixture did not reproduce management epoch transition", issue)
	}
	v := f.view(t)
	payload := map[string]any{"expected_revision": v.Revision, "expected_assignment_epoch": issue.AssignmentEpoch, "reason": "The same responsible Agent explicitly continues the unchanged stopped Task"}
	env := f.h.env(protocol.OpResumeDecisionContinuation, protocol.Target{Type: "decision", ID: f.q.DecisionID}, payload, rev(v.TaskControlRevision))
	op := f.h.exec(owner, env, ResumeDecisionContinuation{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal("same-Agent stopped responsibility could not resume", op)
	}
	if again := f.h.exec(owner, env, ResumeDecisionContinuation{}); !reflect.DeepEqual(again, op) {
		t.Fatal("resume did not retain its exact receipt", again, op)
	}
	next := ownerWorkerClaim(t, f.h, f.runtime, protocol.RoleImplement)
	if next.TaskID != f.task || next.Generation != f.as.Generation || next.SpecVersion != f.as.SpecVersion || next.QuestionContinuation == nil || next.Responsibility == nil || next.Responsibility.AssignmentEpoch != issue.AssignmentEpoch || !reflect.DeepEqual(next.Execution, f.as.Execution) {
		t.Fatal("successor lost original execution or current responsibility", next)
	}
	if got := f.h.task(f.task); got.SpecVersion != beforeTask.SpecVersion || got.ControlRevision != beforeTask.ControlRevision || got.ImplementGeneration != beforeTask.ImplementGeneration {
		t.Fatal("adoption rewrote the Task agreement", beforeTask, got)
	}
	if err := f.h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		current, err := store.RunIssueResponsibilityCurrent(t.Context(), tx.Q(), f.as.RunID)
		if err != nil {
			return err
		}
		if current {
			t.Fatal("adoption renewed an old Run's execution authority")
		}
		var epoch int64
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT assignment_epoch FROM frozen_task_execution_contracts WHERE task_id=?`, f.task).Scan(&epoch); err != nil {
			return err
		}
		if epoch != frozen.AssignmentEpoch {
			t.Fatal("adoption rewrote frozen publishing epoch", epoch)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := f.h.run(f.as.RunID); !reflect.DeepEqual(got, beforeRun) {
		t.Fatal("adoption rewrote original Run", beforeRun, got)
	}
	if n := f.h.countWhere(`SELECT COUNT(*) FROM plan_actions WHERE effect_key=?`, "question_continuation|"+f.q.DecisionID); n != 1 {
		t.Fatal("nonunique continuation", n)
	}
	if op := f.h.runAs(next, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := f.h.runAs(next, protocol.OpRunsSubmitArtifact, testArtifact(f.h.task(f.task).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatal("continued result was fenced", op)
	}
}

func TestAgentTechnicalWaitResumesThroughNamedHostedTransport(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	runtime := testRuntime("technical-wait")
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityWorkerQuestions, protocol.CapabilityQuestionHandoffs, protocol.CapabilitySupervisorQuestions)
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	task := createCanonicalTaskForTest(t, h, issue)
	as, ok := h.claimInputRun(runtime)
	if !ok {
		t.Fatal("no worker")
	}
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	q := askQuestion(t, h, as, scopeQuestion)
	var attention string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT attention_id FROM attentions WHERE decision_id=?`, q.DecisionID).Scan(&attention)
	}); err != nil {
		t.Fatal(err)
	}
	a := readAttentionForTest(t, h, attention)
	turn, principal := startHostedIssueReview(t, h, issue)
	p := protocol.HandleAttentionPayload{ExpectedAssignmentEpoch: a.Responsibility.AssignmentEpoch, Version: q.Version, Option: scopeQuestion.Options[0], Reason: "I own this technical dependency", Continuation: &protocol.DecisionContinuationPolicy{Mode: protocol.DecisionWait, Reason: "Await existing checks"}}
	if op := hostedEffect(t, h, principal, turn, 1, protocol.Target{Type: "attention", ID: attention}, rev(a.Responsibility.Revision), p, HandleAttention{}); op.Status != protocol.OpSucceeded {
		t.Fatal("Agent could not record its technical wait", op)
	}
	f := continuationFixture{h: h, runtime: runtime, task: task, as: as, q: q}
	f.stop(t)
	// Same actual admitted coordinator, no substituted owner source.
	v := f.view(t)
	payload := protocol.ResumeDecisionContinuationPayload{ExpectedRevision: v.Revision}
	if op := hostedEffect(t, h, principal, turn, 2, protocol.Target{Type: "decision", ID: q.DecisionID}, rev(v.TaskControlRevision), payload, ResumeDecisionContinuation{FromMessage: true}); op.Status != protocol.OpSucceeded {
		t.Fatal("Agent could not release its own wait without owner reapproval", op)
	}
}

func TestStoppedResponsibilityAdoptionRejectsUnprovedTransfers(t *testing.T) {
	for _, scenario := range []string{"missing_epoch", "missing_reason", "wrong_epoch", "stale_control", "unconfirmed_stop", "unknown_run", "active_lease", "different_pin", "different_agent", "dependency_reconcile", "another_live_run"} {
		t.Run(scenario, func(t *testing.T) {
			f, issue := stoppedOwnerContinuation(t)
			issue = replaceContinuationManager(t, f, issue, scenario == "different_agent")
			v := f.view(t)
			if !v.ResponsibilityAdoptionRequired || v.CurrentAssignmentEpoch != issue.AssignmentEpoch {
				t.Fatal("missing explicit adoption projection", v)
			}
			p := protocol.ResumeDecisionContinuationPayload{ExpectedRevision: v.Revision, ExpectedAssignmentEpoch: rev(issue.AssignmentEpoch), Reason: "Continue the same stopped responsibility"}
			expected := rev(v.TaskControlRevision)
			cmd := ResumeDecisionContinuation{}
			switch scenario {
			case "missing_epoch":
				p.ExpectedAssignmentEpoch = nil
			case "missing_reason":
				p.Reason = ""
			case "wrong_epoch":
				p.ExpectedAssignmentEpoch = rev(issue.AssignmentEpoch - 1)
			case "stale_control":
				expected = rev(v.TaskControlRevision + 1)
			case "dependency_reconcile":
				cmd.Reconcile = true
			default:
				if err := f.h.store.Write(t.Context(), func(tx *store.Tx) error {
					switch scenario {
					case "unconfirmed_stop":
						_, err := tx.Exec(t.Context(), `UPDATE run_question_handoffs SET stopped=0 WHERE run_id=?`, f.as.RunID)
						return err
					case "unknown_run":
						return tx.UpdateRunObserved(t.Context(), f.as.RunID, protocol.RunUnknown, f.h.clock.Now().UnixMilli())
					case "active_lease":
						_, err := tx.Exec(t.Context(), `UPDATE leases SET status='active' WHERE run_id=?`, f.as.RunID)
						return err
					case "different_pin":
						_, err := tx.Exec(t.Context(), `UPDATE issue_manager_tasks SET pin_json=json_set(pin_json,'$.profile.effort','low') WHERE issue_id=? AND state='assigned'`, issue.IssueID)
						return err
					case "another_live_run":
						r := f.h.run(f.as.RunID)
						a, err := store.GetAction(t.Context(), tx.Q(), r.ActionID)
						if err != nil {
							return err
						}
						a.ActionID = f.h.ids.New(protocol.PrefixAction)
						a.EffectKey = "competing execution"
						if err := tx.InsertAction(t.Context(), a); err != nil {
							return err
						}
						r.RunID = f.h.ids.New(protocol.PrefixRun)
						r.ActionID = a.ActionID
						r.ObservedState = protocol.RunRunning
						return tx.InsertRun(t.Context(), r)
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			env := f.h.env(cmd.Operation(), protocol.Target{Type: "decision", ID: f.q.DecisionID}, p, expected)
			op, err := f.h.app.Execute(t.Context(), owner, env, cmd)
			want := protocol.CodeStaleRevision
			switch scenario {
			case "unconfirmed_stop", "unknown_run", "active_lease", "another_live_run":
				want = protocol.CodeUnknownOutcome
			case "dependency_reconcile":
				want = protocol.CodeInvalidRequest
			}
			if got := technicalWaitErrorCode(op, err); got != want {
				t.Fatal("incorrect transfer rejection", scenario, want, got, op, err)
			}
			if n := f.h.count("task_execution_responsibility_adoptions"); n != 0 {
				t.Fatal("failed transfer persisted an adoption", n)
			}
			if n := f.h.countWhere(`SELECT COUNT(*) FROM plan_actions WHERE effect_key=?`, "question_continuation|"+f.q.DecisionID); n != 0 {
				t.Fatal("failed transfer created a successor", n)
			}
		})
	}
}

func TestTechnicalWaitSourceAndScopeBoundaries(t *testing.T) {
	for _, scenario := range []string{"main_supervisor", "human_answer", "unknown_actor", "other_issue"} {
		t.Run(scenario, func(t *testing.T) {
			h := hostedManagerHarness(t)
			issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
			runtime := testRuntime("technical-boundary")
			runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityWorkerQuestions, protocol.CapabilityQuestionHandoffs, protocol.CapabilitySupervisorQuestions)
			if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			task := createCanonicalTaskForTest(t, h, issue)
			as, ok := h.claimInputRun(runtime)
			if !ok {
				t.Fatal("no worker")
			}
			h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
			q := askQuestion(t, h, as, scopeQuestion)
			f := continuationFixture{h: h, runtime: runtime, task: task, as: as, q: q}
			turn, principal := startHostedIssueReview(t, h, issue)
			if scenario == "human_answer" {
				f.answer(t, &protocol.DecisionContinuationPolicy{Mode: protocol.DecisionWait, Reason: "Actual human wait"})
			} else {
				var id string
				if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
					return tx.Q().QueryRowContext(t.Context(), `SELECT attention_id FROM attentions WHERE decision_id=?`, q.DecisionID).Scan(&id)
				}); err != nil {
					t.Fatal(err)
				}
				a := readAttentionForTest(t, h, id)
				if scenario == "main_supervisor" {
					if op := hostedEffect(t, h, principal, turn, 1, protocol.Target{Type: "attention", ID: id}, rev(a.Responsibility.Revision), protocol.HandleAttentionPayload{ExpectedAssignmentEpoch: a.Responsibility.AssignmentEpoch, Reason: "Escalate the existing technical dependency"}, HandleAttention{Escalate: true}); op.Status != protocol.OpSucceeded {
						t.Fatal(op)
					}
					a = readAttentionForTest(t, h, id)
					_, delivery := h.say("Inspect the already escalated technical dependency")
					turn = startHostedReview(t, h, "main", delivery)
					principal = mainAgent
				}
				p := protocol.HandleAttentionPayload{ExpectedAssignmentEpoch: a.Responsibility.AssignmentEpoch, Version: q.Version, Option: scopeQuestion.Options[0], Reason: "Wait only for the current checks", Continuation: &protocol.DecisionContinuationPolicy{Mode: protocol.DecisionWait, Reason: "Await check result"}}
				seq := int64(1)
				if op := hostedEffect(t, h, principal, turn, seq, protocol.Target{Type: "attention", ID: id}, rev(a.Responsibility.Revision), p, HandleAttention{}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			if scenario == "unknown_actor" {
				if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE decisions SET answer_json=json_remove(answer_json,'$.actor') WHERE decision_id=?`, q.DecisionID)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			f.stop(t)
			if scenario == "other_issue" {
				other := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
				turn, principal = startHostedIssueReview(t, h, other)
			}
			v := f.view(t)
			cmd := ResumeDecisionContinuation{FromMessage: true}
			op, err := h.app.Execute(t.Context(), principal, h.env(cmd.Operation(), protocol.Target{Type: "decision", ID: q.DecisionID}, protocol.ResumeDecisionContinuationPayload{ExpectedRevision: v.Revision}, rev(v.TaskControlRevision)), HostedCoordinationCommand{Command: cmd, ExecutionID: turn.RequestID, EffectSeq: 2})
			if scenario == "main_supervisor" {
				if err != nil || op.Status != protocol.OpSucceeded {
					t.Fatal("actual main supervisor could not release its Agent wait", op, err)
				}
			} else if got := technicalWaitErrorCode(op, err); got != protocol.CodeForbidden {
				t.Fatal("incorrect source/scope refusal", got, op, err)
			}
		})
	}
}

func technicalWaitErrorCode(op protocol.Operation, err error) protocol.Code {
	if err != nil {
		return protocol.CodeOf(err)
	}
	if op.Error != nil {
		return op.Error.Code
	}
	return ""
}

func TestAgentTechnicalWaitMissingTargetIsTyped(t *testing.T) {
	h := hostedManagerHarness(t)
	_, delivery := h.say("Inspect this technical scheduling request")
	turn := startHostedReview(t, h, "main", delivery)
	cmd := ResumeDecisionContinuation{FromMessage: true}
	target := protocol.Target{Type: "decision", ID: h.ids.New(protocol.PrefixDecision)}
	env := h.env(cmd.Operation(), target, protocol.ResumeDecisionContinuationPayload{ExpectedRevision: 1}, rev(1))
	op, err := h.app.Execute(t.Context(), mainAgent, env, HostedCoordinationCommand{Command: cmd, ExecutionID: turn.RequestID, EffectSeq: 1})
	if code := technicalWaitErrorCode(op, err); code != protocol.CodeNotFound {
		t.Fatal("missing original decision became an internal tool error", op, err)
	}
}
