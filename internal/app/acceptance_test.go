package app

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
)

// toAcceptanceClaim drives a task through implementation and returns the
// claimed acceptance assignment.
func (h *harness) toAcceptanceClaim() (string, protocol.Assignment) {
	h.t.Helper()
	_, res := h.createTask(owner, createTaskPayload{Title: "a", Spec: testSpec})
	impl, _, _ := h.claim("linux-1")
	h.runAs(impl, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	if op := h.runAs(impl, protocol.OpRunsSubmitArtifact, testArtifact(h.task(res.TaskID).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		h.t.Fatalf("artifact: %+v", op.Error)
	}
	acc, ok, _ := h.claim("linux-1")
	if !ok || acc.Role != protocol.RoleAccept {
		h.t.Fatalf("acceptance claim = %+v", acc)
	}
	return res.TaskID, acc
}

func (h *harness) requestAction(taskID string, kind protocol.ActionKind, generation int64) protocol.Operation {
	h.t.Helper()
	return h.exec(owner, h.env("actions.create", taskTarget(taskID), protocol.CreateActionPayload{Kind: kind, Generation: generation},
		rev(h.task(taskID).ControlRevision)), CreateAction{})
}

func TestFault_AcceptanceRunFailure(t *testing.T) {
	h := newHarness(t)
	taskID, acc := h.toAcceptanceClaim()
	h.runAs(acc, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "sandbox crashed"}, Release{})

	got := h.task(taskID)
	if got.Lifecycle != protocol.TaskAwaitingAcceptance || got.BlockReason != protocol.BlockAcceptanceEnvUnavailable {
		t.Fatalf("task after acceptance run failure: %s / %s", got.Lifecycle, got.BlockReason)
	}
	if n := h.count("decisions"); n != 1 {
		t.Errorf("decisions = %d, want 1", n)
	}
	if _, ok, _ := h.claim("linux-1"); ok {
		t.Fatal("work was claimed while acceptance is blocked")
	}

	if op := h.requestAction(taskID, protocol.ActionAccept, 2); op.Status != protocol.OpSucceeded {
		t.Fatalf("retry acceptance: %+v", op.Error)
	}
	retry, ok, _ := h.claim("linux-1")
	if !ok || retry.Role != protocol.RoleAccept || retry.Generation != 2 {
		t.Fatalf("retried acceptance claim = %+v, %v", retry, ok)
	}
	if op := h.runAs(retry, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed, Evidence: json.RawMessage(`{}`)}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatalf("verdict: %+v", op.Error)
	}
	if got := h.task(taskID); got.Lifecycle != protocol.TaskCompleted {
		t.Errorf("task = %s, want completed", got.Lifecycle)
	}
}

func TestImplementRunFailureWaitsForDecision(t *testing.T) {
	h := newHarness(t)
	_, res := h.createTask(owner, createTaskPayload{Title: "a", Spec: testSpec})
	impl, _, _ := h.claim("linux-1")
	h.runAs(impl, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	h.runAs(impl, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "no_changes"}, Release{})

	got := h.task(res.TaskID)
	if got.Lifecycle != protocol.TaskQueued || got.BlockReason != protocol.BlockWaitingDecision {
		t.Fatalf("task after implement failure: %s / %s", got.Lifecycle, got.BlockReason)
	}
	if _, ok, _ := h.claim("linux-1"); ok {
		t.Fatal("work was claimed before the owner decided")
	}
	if op := h.requestAction(res.TaskID, protocol.ActionImplement, 2); op.Status != protocol.OpSucceeded {
		t.Fatalf("retry implementation: %+v", op.Error)
	}
	next, ok, _ := h.claim("linux-1")
	if !ok || next.Role != protocol.RoleImplement || next.Generation != 2 {
		t.Errorf("retried implementation claim = %+v, %v", next, ok)
	}
}

func TestFault_StaleRunAfterSpecChange(t *testing.T) {
	h := newHarness(t)
	_, res := h.createTask(owner, createTaskPayload{Title: "a", Spec: testSpec})
	old, _, _ := h.claim("linux-1")
	h.runAs(old, protocol.OpRunsReportStarted, nil, ReportRunStarted{})

	changed := testSpec
	changed.Constraints = []string{"keep the API"}
	h.exec(owner, h.env("tasks.update_intent", taskTarget(res.TaskID), updateIntentPayload{Spec: &changed}, rev(res.ControlRevision)), UpdateIntent{})

	hb, err := h.heartbeat(t.Context(), runner("linux-1"), []protocol.LeaseRef{{RunID: old.RunID, LeaseID: old.LeaseID}})
	if err != nil || hb.Runs[0].Desired != protocol.DesiredStopped {
		t.Fatalf("heartbeat for the outdated run = %+v, %v; want stopped", hb, err)
	}
	// The old run delivers anyway: kept as a fact, nothing follows.
	if op := h.runAs(old, protocol.OpRunsSubmitArtifact, testArtifact(h.task(res.TaskID).WorkspaceName), SubmitArtifact{}); op.Status != protocol.OpSucceeded {
		t.Fatalf("late artifact: %+v", op.Error)
	}
	got := h.task(res.TaskID)
	if got.Lifecycle != protocol.TaskQueued || got.SpecVersion != 2 || got.CurrentArtifactID != "" {
		t.Errorf("task after the late artifact: %+v", got)
	}
	next, ok, _ := h.claim("linux-1")
	if !ok || next.Role != protocol.RoleImplement || next.SpecVersion != 2 {
		t.Errorf("next claim = %+v, want the spec 2 implementation", next)
	}
}

func TestDecisions(t *testing.T) {
	h := newHarness(t)
	_, res := h.createTask(owner, createTaskPayload{Title: "a", Spec: testSpec})
	op := h.exec(mainAgent, h.env("decisions.open", protocol.Target{Type: "decision"}, openDecisionPayload{
		TaskID: res.TaskID, Question: "Which approach?", Options: []string{"first", "second"}, SourceMessageID: "msg_1",
	}, nil), OpenDecision{})
	var opened struct {
		DecisionID string `json:"decision_id"`
	}
	if op.Status != protocol.OpSucceeded || json.Unmarshal(op.Result, &opened) != nil {
		t.Fatalf("open: %+v", op)
	}
	target := protocol.Target{Type: "decision", ID: opened.DecisionID}
	answer := func(p protocol.Principal, cmd AnswerDecision, payload protocol.AnswerDecisionPayload) protocol.Operation {
		return h.exec(p, h.env(cmd.Operation(), target, payload, nil), cmd)
	}

	if op := answer(mainAgent, AnswerDecision{FromMessage: true}, protocol.AnswerDecisionPayload{Version: 1, Option: "second"}); op.Status != protocol.OpFailed {
		t.Errorf("resolve without a source message: %+v", op)
	}
	if op := answer(owner, AnswerDecision{}, protocol.AnswerDecisionPayload{Version: 1, Option: "third"}); op.Status != protocol.OpFailed {
		t.Errorf("answer outside the options: %+v", op)
	}
	if op := answer(owner, AnswerDecision{}, protocol.AnswerDecisionPayload{Version: 1, Option: "second"}); op.Status != protocol.OpSucceeded {
		t.Fatalf("answer: %+v", op.Error)
	}
	// Another client answers the same version: the same answer returns the
	// recorded result, a different one is stale.
	if op := answer(owner, AnswerDecision{}, protocol.AnswerDecisionPayload{Version: 1, Option: "second"}); op.Status != protocol.OpSucceeded {
		t.Errorf("same answer again: %+v", op.Error)
	}
	if op := answer(owner, AnswerDecision{}, protocol.AnswerDecisionPayload{Version: 1, Option: "first"}); op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeStaleRevision {
		t.Errorf("conflicting answer: %+v", op)
	}
	envl := h.env("decisions.respond", target, protocol.AnswerDecisionPayload{Version: 2, Option: "first"}, nil)
	if _, err := h.app.Execute(t.Context(), mainAgent, envl, AnswerDecision{}); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Errorf("main agent using decisions.respond: %v", err)
	}
}

func TestNextGenerationGetsFeedback(t *testing.T) {
	h := newHarness(t)
	_, acc := h.toAcceptanceClaim()
	h.runAs(acc, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	if op := h.runAs(acc, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictChangesRequired,
		Evidence: json.RawMessage(`{"log":"tests failed"}`), Issues: json.RawMessage(`["handle empty input"]`)}, SubmitAcceptance{}); op.Status != protocol.OpSucceeded {
		t.Fatalf("verdict: %+v", op.Error)
	}
	next, ok, _ := h.claim("linux-1")
	if !ok || next.Generation != 2 || next.Feedback == nil || string(next.Feedback.Issues) != `["handle empty input"]` {
		t.Errorf("second generation = %+v (feedback %+v)", next, next.Feedback)
	}
}
