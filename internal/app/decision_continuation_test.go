package app

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestDecisionContinuationUntypedAnswerWaitsForExplicitAdmission(t *testing.T) {
	for _, wire := range []int64{protocol.PreviousRunnerProtocol, protocol.CurrentRunnerProtocol} {
		for _, answerFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("wire-%d/answer-first-%t", wire, answerFirst), func(t *testing.T) {
				h := newHarness(t)
				runtime := testRuntime("decision-continuation-runner")
				runtime.Protocol, runtime.ProtocolMin, runtime.ProtocolMax = wire, wire, wire
				runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityWorkerQuestions, protocol.CapabilityQuestionHandoffs)
				if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				issue := createCanonicalOwnerTestIssue(t, h)
				task := createCanonicalTaskForTest(t, h, issue)
				as, ok := h.claimInputRun(runtime)
				if !ok || !as.QuestionHandoffs {
					t.Fatal("handoff execution unavailable", as)
				}
				if op := h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				q := askQuestion(t, h, as, scopeQuestion)
				env := h.env("decisions.respond", protocol.Target{Type: "decision", ID: q.DecisionID},
					protocol.AnswerDecisionPayload{Version: q.Version, Option: scopeQuestion.Options[0],
						Text: "Wait for the dependency before continuing."}, nil)
				answer := func() {
					t.Helper()
					if op := h.exec(owner, env, AnswerDecision{}); op.Status != protocol.OpSucceeded {
						t.Fatal("recording an answer must remain legal", op)
					}
				}
				if answerFirst {
					answer()
				}
				if op := h.runAs(as, protocol.OpRunsYieldQuestion,
					protocol.YieldQuestionPayload{DecisionID: q.DecisionID, ThreadID: "original-session"}, YieldQuestion{}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				if !answerFirst {
					answer()
				}
				answer() // Reconcile the original request without inventing another action.
				if h.run(as.RunID).ObservedState != protocol.RunFinished || h.lease(as.RunID).Status != store.LeaseReleased {
					t.Fatal("waiting retained physical worker capacity")
				}
				if got := h.count("plan_actions WHERE effect_key='question_continuation|" + q.DecisionID + "'"); got != 0 {
					t.Errorf("an untyped answer manufactured %d continuation actions", got)
				}
				if next, ok := h.claimInputRun(runtime); ok {
					t.Fatalf("an answer without machine admission restarted work: run=%s action=%s", next.RunID, next.ActionID)
				}
				if state := h.executionTask(task).Execution.State; state != protocol.ExecutionWaitingOwner {
					t.Fatalf("durable waiting was lost after recording the answer: %s", state)
				}
			})
		}
	}
}

type continuationFixture struct {
	h       *harness
	runtime *protocol.RunnerRuntime
	task    string
	as      protocol.Assignment
	q       protocol.WorkerQuestion
}

func newContinuationFixture(t *testing.T, h *harness) continuationFixture {
	t.Helper()
	if h == nil {
		h = newHarness(t)
	}
	runtime := testRuntime("typed-continuation-runner")
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityWorkerQuestions, protocol.CapabilityQuestionHandoffs)
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	task := createCanonicalTaskForTest(t, h, createCanonicalOwnerTestIssue(t, h))
	as, ok := h.claimInputRun(runtime)
	if !ok || as.TaskID != task || !as.QuestionHandoffs {
		t.Fatal("no handoff assignment", as)
	}
	if op := h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	return continuationFixture{h: h, runtime: runtime, task: task, as: as, q: askQuestion(t, h, as, scopeQuestion)}
}

func (f continuationFixture) stop(t *testing.T) {
	t.Helper()
	if op := f.h.runAs(f.as, protocol.OpRunsYieldQuestion, protocol.YieldQuestionPayload{DecisionID: f.q.DecisionID, ThreadID: "frozen-native-session"}, YieldQuestion{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}

func (f continuationFixture) answer(t *testing.T, policy *protocol.DecisionContinuationPolicy) *protocol.Envelope {
	t.Helper()
	env := f.h.env("decisions.respond", protocol.Target{Type: "decision", ID: f.q.DecisionID}, protocol.AnswerDecisionPayload{Version: f.q.Version, Option: scopeQuestion.Options[0], Text: "The answer is recorded independently of scheduling.", Continuation: policy}, nil)
	if op := f.h.exec(owner, env, AnswerDecision{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	return env
}

func (f continuationFixture) view(t *testing.T) protocol.DecisionContinuationView {
	t.Helper()
	v, e := f.h.app.ReadDecisionContinuation(t.Context(), f.q.DecisionID)
	if e != nil {
		t.Fatal(e)
	}
	return v
}

func (f continuationFixture) resumeEnvelope(t *testing.T, reconcile bool) (*protocol.Envelope, ResumeDecisionContinuation) {
	t.Helper()
	v := f.view(t)
	cmd := ResumeDecisionContinuation{Reconcile: reconcile}
	return f.h.env(cmd.Operation(), protocol.Target{Type: "decision", ID: f.q.DecisionID}, protocol.ResumeDecisionContinuationPayload{ExpectedRevision: v.Revision}, rev(v.TaskControlRevision)), cmd
}

func TestDecisionContinuationTypedNowPreservesStoppedOriginalAgreement(t *testing.T) {
	f := newContinuationFixture(t, nil)
	before := f.h.task(f.task)
	f.answer(t, &protocol.DecisionContinuationPolicy{Mode: protocol.DecisionContinueNow})
	if v := f.view(t); v.State != "ready" || v.BlockedReason != "original_process_not_stopped" {
		t.Fatal(v)
	}
	if n := f.h.count("plan_actions WHERE effect_key='question_continuation|" + f.q.DecisionID + "'"); n != 0 {
		t.Fatal("running process bypassed", n)
	}
	f.stop(t)
	next, ok := f.h.claimInputRun(f.runtime)
	if !ok || next.TaskID != f.task || next.Generation != f.as.Generation || next.SpecVersion != f.as.SpecVersion || next.Role != f.as.Role || next.Workspace.ID != f.as.Workspace.ID || next.QuestionContinuation == nil || next.QuestionContinuation.ThreadID != "frozen-native-session" {
		t.Fatal(next)
	}
	if got := f.h.task(f.task); got != before {
		t.Fatal("continuation changed agreement", got, before)
	}
}

func TestDecisionContinuationWaitPersistsAndIndependentResumeReplays(t *testing.T) {
	f := newContinuationFixture(t, nil)
	answer := f.answer(t, &protocol.DecisionContinuationPolicy{Mode: protocol.DecisionWait, Reason: "Await explicit continuation under the standing responsibility"})
	f.stop(t)
	before := f.view(t)
	// Reconstruct the App over the durable ledger as a new server process does.
	var database string
	if err := f.h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var sequence int
		var name string
		return tx.Q().QueryRowContext(t.Context(), "PRAGMA database_list").Scan(&sequence, &name, &database)
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.h.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(t.Context(), database, f.h.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	f.h.store = reopened
	f.h.app = New(reopened, f.h.ids, f.h.clock.Now)
	if got := f.view(t); !reflect.DeepEqual(got, before) {
		t.Fatal("restart lost continuation", before, got)
	}
	bad, cmd := f.resumeEnvelope(t, true)
	if op := f.h.exec(owner, bad, cmd); op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeInvalidTransition {
		t.Fatal("reconcile silently converted explicit wait", op)
	}
	stale, cmd := f.resumeEnvelope(t, false)
	stale.ExpectedControlRevision = rev(before.TaskControlRevision + 1)
	if op := f.h.exec(owner, stale, cmd); op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeStaleRevision {
		t.Fatal(op)
	}
	env, cmd := f.resumeEnvelope(t, false)
	op := f.h.exec(owner, env, cmd)
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	replay := f.h.exec(owner, env, cmd)
	if string(replay.Result) != string(op.Result) {
		t.Fatal("original resume did not replay", op, replay)
	}
	if original := f.h.exec(owner, answer, AnswerDecision{}); original.Status != protocol.OpSucceeded {
		t.Fatal(original)
	}
	after := f.view(t)
	if after.AnswerRequestID != answer.RequestID || after.ResumeRequestID != env.RequestID || after.State != "continued" {
		t.Fatal(after)
	}
	if n := f.h.count("plan_actions WHERE effect_key='question_continuation|" + f.q.DecisionID + "'"); n != 1 {
		t.Fatal("duplicate continuation", n)
	}
	if _, ok := f.h.claimInputRun(f.runtime); !ok {
		t.Fatal("explicit resume did not schedule")
	}
	if _, ok := f.h.claimInputRun(f.runtime); ok {
		t.Fatal("duplicate successor")
	}
}

func TestDecisionContinuationAnswerCannotRewritePolicy(t *testing.T) {
	f := newContinuationFixture(t, nil)
	f.stop(t)
	f.answer(t, &protocol.DecisionContinuationPolicy{Mode: protocol.DecisionWait, Reason: "Dependency is not established"})
	env := f.h.env("decisions.respond", protocol.Target{Type: "decision", ID: f.q.DecisionID}, protocol.AnswerDecisionPayload{Version: f.q.Version, Option: scopeQuestion.Options[0], Text: "The answer is recorded independently of scheduling.", Continuation: &protocol.DecisionContinuationPolicy{Mode: protocol.DecisionContinueNow}}, nil)
	if op := f.h.exec(owner, env, AnswerDecision{}); op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeStaleRevision {
		t.Fatal("answer replay changed scheduling permission", op)
	}
	if got := f.view(t); got.State != "waiting" || got.Policy.Mode != protocol.DecisionWait {
		t.Fatal(got)
	}
}

func TestDecisionContinuationLegacyAnswerRequiresExplicitRevisionZero(t *testing.T) {
	f := newContinuationFixture(t, nil)
	f.stop(t)
	f.answer(t, nil)
	// Historical answers predate the new independent policy table.
	if err := f.h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `DELETE FROM decision_continuations WHERE decision_id=?`, f.q.DecisionID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	v := f.view(t)
	if v.Revision != 0 || v.BlockedReason != "legacy_answer_requires_explicit_resume" {
		t.Fatal(v)
	}
	for _, raw := range []string{`{}`, `{"expected_revision":null}`, `{"expected_revision":-1}`} {
		env := f.h.env(protocol.OpResumeDecisionContinuation, protocol.Target{Type: "decision", ID: f.q.DecisionID}, nil, rev(v.TaskControlRevision))
		env.Payload = []byte(raw)
		if op := f.h.exec(owner, env, ResumeDecisionContinuation{}); op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeInvalidRequest {
			t.Fatal(raw, op)
		}
	}
	env, cmd := f.resumeEnvelope(t, false)
	if op := f.h.exec(owner, env, cmd); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if got := f.view(t); got.AnswerRequestID != "" || got.ResumeRequestID != env.RequestID || got.State != "continued" {
		t.Fatal("legacy answer identity was invented", got)
	}
}

func TestDecisionContinuationDependencyUsesRealCompletionAndClaimRechecks(t *testing.T) {
	h := newHarness(t)
	dependency, accept := h.toAwaitingAcceptance()
	f := newContinuationFixture(t, h)
	f.stop(t)
	f.answer(t, &protocol.DecisionContinuationPolicy{Mode: protocol.DecisionDependency, Reason: "Wait for the frozen dependency delivery", Dependency: &protocol.DecisionContinuationDependency{TaskID: dependency, SpecVersion: h.task(dependency).SpecVersion}})
	env, cmd := f.resumeEnvelope(t, true)
	if op := h.exec(owner, env, cmd); op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeInvalidTransition {
		t.Fatal("caller invented readiness", op)
	}
	if v := f.view(t); v.BlockedReason != "dependency_not_completed" || v.State != "waiting" {
		t.Fatal(v)
	}
	if op := h.submitVerdict(accept, protocol.VerdictPassed); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	v := f.view(t)
	if v.State != "continued" || v.ContinuationActionID == "" {
		t.Fatal("actual completion failed to wake", v)
	}
	// A superseded dependency cannot admit an already queued continuation.
	// Isolated fixture mutation models a later authoritative spec revision.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE tasks SET spec_version=spec_version+1 WHERE task_id=?`, dependency)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if v := f.view(t); v.BlockedReason != "dependency_spec_changed" {
		t.Fatal(v)
	}
	if next, ok := h.claimInputRun(f.runtime); ok {
		t.Fatal("stale dependency admitted successor", next)
	}
	if state := h.executionTask(f.task).Execution.State; state != protocol.ExecutionWaitingSupervisor {
		t.Fatal("blocked queued dependency disappeared", state)
	}
}

func TestDecisionContinuationChangedParentContractDoesNotResume(t *testing.T) {
	f := newContinuationFixture(t, nil)
	f.stop(t)
	f.answer(t, nil)
	if op := f.h.exec(owner, f.h.env("tasks.cancel", taskTarget(f.task), map[string]any{}, rev(f.h.task(f.task).ControlRevision)), CancelTask{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	env, cmd := f.resumeEnvelope(t, false)
	if op := f.h.exec(owner, env, cmd); op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeStaleRevision {
		t.Fatal("cancelled agreement resumed", op)
	}
	if n := f.h.count("plan_actions WHERE effect_key='question_continuation|" + f.q.DecisionID + "'"); n != 0 {
		t.Fatal(n)
	}
}

func TestDecisionContinuationRejectsManagerDependencyIncludingHistoricalPolicy(t *testing.T) {
	f := newContinuationFixture(t, nil)
	f.stop(t)
	_, created := f.h.createTask(mainAgent, createTaskPayload{Title: "Coordination responsibility", Spec: testSpec})
	if err := f.h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE tasks SET kind='issue_manager' WHERE task_id=?`, created.TaskID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	policy := &protocol.DecisionContinuationPolicy{Mode: protocol.DecisionDependency, Reason: "Await coordination", Dependency: &protocol.DecisionContinuationDependency{TaskID: created.TaskID, SpecVersion: 1}}
	env := f.h.env("decisions.respond", protocol.Target{Type: "decision", ID: f.q.DecisionID}, protocol.AnswerDecisionPayload{Version: f.q.Version, Option: scopeQuestion.Options[0], Continuation: policy}, nil)
	if op := f.h.exec(owner, env, AnswerDecision{}); op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeInvalidRequest {
		t.Fatal("unsupported manager dependency admitted", op)
	}
	f.answer(t, nil)
	if err := f.h.store.Write(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.Exec(t.Context(), `DELETE FROM decision_continuations WHERE decision_id=?`, f.q.DecisionID); err != nil {
			return err
		}
		return tx.PutDecisionContinuation(t.Context(), store.DecisionContinuationRow{DecisionID: f.q.DecisionID, Revision: 1, Policy: *policy, State: "waiting"})
	}); err != nil {
		t.Fatal(err)
	}
	if v := f.view(t); v.BlockedReason != "dependency_kind_unsupported" || v.State != "waiting" {
		t.Fatal("historical unsupported policy claimed ready", v)
	}
	reconcile, cmd := f.resumeEnvelope(t, true)
	if op := f.h.exec(owner, reconcile, cmd); op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeInvalidTransition {
		t.Fatal(op)
	}
}

func TestDecisionContinuationConcurrentIndependentResumeCreatesOneSuccessor(t *testing.T) {
	f := newContinuationFixture(t, nil)
	f.stop(t)
	f.answer(t, nil)
	a, cmd := f.resumeEnvelope(t, false)
	b, _ := f.resumeEnvelope(t, false)
	type result struct {
		op  protocol.Operation
		err error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	for _, env := range []*protocol.Envelope{a, b} {
		go func() { <-start; op, err := f.h.app.Execute(t.Context(), owner, env, cmd); results <- result{op, err} }()
	}
	close(start)
	success, stale := 0, 0
	for range 2 {
		r := <-results
		if r.err != nil {
			t.Fatal(r.err)
		}
		if r.op.Status == protocol.OpSucceeded {
			success++
		} else if r.op.Error != nil && r.op.Error.Code == protocol.CodeStaleRevision {
			stale++
		} else {
			t.Fatal(r.op)
		}
	}
	if success != 1 || stale != 1 || f.h.count("plan_actions WHERE effect_key='question_continuation|"+f.q.DecisionID+"'") != 1 {
		t.Fatal(success, stale, f.view(t))
	}
}

func TestDecisionContinuationDependencyCompletionBeforeStopStillNeedsOriginalReceipt(t *testing.T) {
	h := newHarness(t)
	dependency, accept := h.toAwaitingAcceptance()
	f := newContinuationFixture(t, h)
	f.answer(t, &protocol.DecisionContinuationPolicy{Mode: protocol.DecisionDependency, Reason: "Await original delivery", Dependency: &protocol.DecisionContinuationDependency{TaskID: dependency, SpecVersion: h.task(dependency).SpecVersion}})
	if op := h.submitVerdict(accept, protocol.VerdictPassed); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if v := f.view(t); v.State != "ready" || v.BlockedReason != "original_process_not_stopped" || v.ContinuationActionID != "" {
		t.Fatal(v)
	}
	f.stop(t)
	if next, ok := h.claimInputRun(f.runtime); !ok || next.TaskID != f.task || next.QuestionContinuation == nil {
		t.Fatal(next, ok)
	}
}

func TestDecisionContinuationDependencyCompletionCannotReviveCancelledParent(t *testing.T) {
	h := newHarness(t)
	dependency, accept := h.toAwaitingAcceptance()
	f := newContinuationFixture(t, h)
	f.stop(t)
	f.answer(t, &protocol.DecisionContinuationPolicy{Mode: protocol.DecisionDependency, Reason: "Await original delivery", Dependency: &protocol.DecisionContinuationDependency{TaskID: dependency, SpecVersion: h.task(dependency).SpecVersion}})
	if op := h.exec(owner, h.env("tasks.cancel", taskTarget(f.task), map[string]any{}, rev(h.task(f.task).ControlRevision)), CancelTask{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.submitVerdict(accept, protocol.VerdictPassed); op.Status != protocol.OpSucceeded || h.task(dependency).Lifecycle != protocol.TaskCompleted {
		t.Fatal("obsolete waiter rolled back real completion", op)
	}
	if n := h.count("plan_actions WHERE effect_key='question_continuation|" + f.q.DecisionID + "'"); n != 0 {
		t.Fatal("completed dependency revived cancelled parent", n)
	}
}
