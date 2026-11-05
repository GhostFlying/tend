package app

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func dependencyHarness(t *testing.T) (*harness, *protocol.RunnerRuntime) {
	t.Helper()
	h := newHarness(t)
	cfg := testConfig()
	cfg.Hosts[0].Slots = 2
	h.syncConfig(cfg)
	runtime := testRuntime("dependency-runner")
	runtime.Capabilities = append(runtime.Capabilities, protocol.CapabilityWorkerQuestions, protocol.CapabilityQuestionHandoffs)
	if op := h.versionedHello("linux-1", runtime); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	return h, runtime
}

func dependencyFixture(t *testing.T, h *harness, runtime *protocol.RunnerRuntime, legacy bool) continuationFixture {
	t.Helper()
	var task string
	if legacy {
		op, created := h.createTask(owner, createTaskPayload{Title: "Legacy handoff", Spec: testSpec})
		if op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		task = created.TaskID
	} else {
		task = createCanonicalTaskForTest(t, h, createCanonicalOwnerTestIssue(t, h))
	}
	as, ok := h.claimInputRun(runtime)
	if !ok || as.TaskID != task || !as.QuestionHandoffs {
		t.Fatal(as, ok)
	}
	if op := h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	return continuationFixture{h: h, runtime: runtime, task: task, as: as, q: askQuestion(t, h, as, scopeQuestion)}
}

func dependencyPolicy(f continuationFixture, task string) *protocol.DecisionContinuationPolicy {
	return &protocol.DecisionContinuationPolicy{Mode: protocol.DecisionDependency, Reason: "Wait for the exact task delivery", Dependency: &protocol.DecisionContinuationDependency{TaskID: task, SpecVersion: f.h.task(task).SpecVersion}}
}

func dependencyAnswerEnvelope(f continuationFixture, task string) *protocol.Envelope {
	return f.h.env(protocol.OpAnswerDecisionContinuation, protocol.Target{Type: "decision", ID: f.q.DecisionID}, protocol.AnswerDecisionPayload{Version: f.q.Version, Option: scopeQuestion.Options[0], Continuation: dependencyPolicy(f, task)}, rev(f.h.task(f.task).ControlRevision))
}

func TestDecisionContinuationDependencyRejectsMutualWaitsAtomically(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		for _, stopped := range []bool{false, true} {
			t.Run(fmt.Sprintf("legacy-%t/stopped-%t", legacy, stopped), func(t *testing.T) {
				h, runtime := dependencyHarness(t)
				a := dependencyFixture(t, h, runtime, legacy)
				if stopped {
					a.stop(t)
				}
				b := dependencyFixture(t, h, runtime, legacy)
				if stopped {
					b.stop(t)
				}
				a.answer(t, dependencyPolicy(a, b.task))
				before := a.view(t)
				env := dependencyAnswerEnvelope(b, a.task)
				cmd := AnswerDecision{TypedContinuation: true}
				op := h.exec(owner, env, cmd)
				if op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeInvalidRequest {
					t.Fatal("mutual wait admitted", op)
				}
				if replay := h.exec(owner, env, cmd); !reflect.DeepEqual(op, replay) {
					t.Fatal("cycle failure receipt changed", op, replay)
				}
				if got := a.view(t); !reflect.DeepEqual(got, before) {
					t.Fatal("other answer changed", before, got)
				}
				if got := b.view(t); got.Policy != nil || got.BlockedReason != "answer_pending" {
					t.Fatal("rejected answer/policy persisted", got)
				}
			})
		}
	}
}

func TestDecisionContinuationDependencyRejectsHistoricalTaskWaitCycle(t *testing.T) {
	h, runtime := dependencyHarness(t)
	a := dependencyFixture(t, h, runtime, false)
	a.stop(t)
	b := dependencyFixture(t, h, runtime, false)
	b.stop(t)
	// There is no current task-dependency write API; this is an existing ledger fact.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO task_dependencies(task_id,depends_on)VALUES(?,?)`, b.task, a.task)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	op := h.exec(owner, dependencyAnswerEnvelope(a, b.task), AnswerDecision{TypedContinuation: true})
	if op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeInvalidRequest {
		t.Fatal("mixed task wait cycle admitted", op)
	}
}

func TestDecisionContinuationDependencyBlocksLaterCanonicalIssueCycle(t *testing.T) {
	h, runtime := dependencyHarness(t)
	a := dependencyFixture(t, h, runtime, false)
	a.stop(t)
	b := dependencyFixture(t, h, runtime, false)
	b.stop(t)
	a.answer(t, dependencyPolicy(a, b.task))
	var ai, bi string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		ai, err = store.GetTaskIssueOwner(t.Context(), tx.Q(), a.task)
		if err != nil {
			return err
		}
		bi, err = store.GetTaskIssueOwner(t.Context(), tx.Q(), b.task)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	issue := canonicalTaskIssue(t, h, bi)
	payload := struct {
		canonicalOwnerControl
		DependsOn []string `json:"depends_on"`
	}{canonicalOwnerControl{ExpectedAssignmentEpoch: issue.AssignmentEpoch}, []string{ai}}
	op := canonicalLifecycleControl(t, h, issue, "dependencies", payload)
	if op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeInvalidRequest {
		t.Fatal("later issue wait closed a continuation cycle", op)
	}
	if got := canonicalTaskIssue(t, h, bi); got.Revision != issue.Revision || len(got.DependsOn) != 0 {
		t.Fatal("rejected issue graph persisted", got)
	}
}

func TestDecisionContinuationDependencyBlocksLaterLegacyPlanCycle(t *testing.T) {
	h, runtime := dependencyHarness(t)
	a := dependencyFixture(t, h, runtime, true)
	a.stop(t)
	b := dependencyFixture(t, h, runtime, true)
	b.stop(t)
	a.answer(t, dependencyPolicy(a, b.task))
	plan := taskCyclePlan()
	plan.Stages = plan.Stages[:2]
	plan.Stages[0].TaskIDs = []string{a.task}
	plan.Stages[1].TaskIDs = []string{b.task}
	before := h.count("work_items")
	op := h.exec(owner, h.env("work_items.create", workTarget(""), protocol.CreateWorkItemPayload{IssuePlan: plan}, nil), CreateIssue{})
	if op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeInvalidRequest {
		t.Fatal("later legacy stage wait closed a continuation cycle", op)
	}
	if h.count("work_items") != before {
		t.Fatal("rejected legacy plan persisted")
	}
}

func TestDecisionContinuationDependencyAcceptsAcyclicChain(t *testing.T) {
	h, runtime := dependencyHarness(t)
	a := dependencyFixture(t, h, runtime, false)
	a.stop(t)
	b := dependencyFixture(t, h, runtime, false)
	b.stop(t)
	c := dependencyFixture(t, h, runtime, false)
	c.stop(t)
	a.answer(t, dependencyPolicy(a, b.task))
	b.answer(t, dependencyPolicy(b, c.task))
	if a.view(t).State != "waiting" || b.view(t).State != "waiting" {
		t.Fatal(a.view(t), b.view(t))
	}
}

func TestDecisionContinuationDependencyRejectsMixedCanonicalAndLegacyCycle(t *testing.T) {
	h, runtime := dependencyHarness(t)
	a := dependencyFixture(t, h, runtime, false)
	a.stop(t)
	b := dependencyFixture(t, h, runtime, true)
	b.stop(t)
	c := dependencyFixture(t, h, runtime, true)
	c.stop(t)
	d := dependencyFixture(t, h, runtime, false)
	d.stop(t)
	a.answer(t, dependencyPolicy(a, b.task))
	c.answer(t, dependencyPolicy(c, d.task))
	// B waits for legacy stage C. The remaining path runs through canonical Issues.
	plan := taskCyclePlan()
	plan.Stages = plan.Stages[:2]
	plan.Stages[0].TaskIDs = []string{c.task}
	plan.Stages[1].TaskIDs = []string{b.task}
	if op := h.exec(owner, h.env("work_items.create", workTarget(""), protocol.CreateWorkItemPayload{IssuePlan: plan}, nil), CreateIssue{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var ai, di string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		ai, err = store.GetTaskIssueOwner(t.Context(), tx.Q(), a.task)
		if err != nil {
			return err
		}
		di, err = store.GetTaskIssueOwner(t.Context(), tx.Q(), d.task)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	issue := canonicalTaskIssue(t, h, di)
	payload := struct {
		canonicalOwnerControl
		DependsOn []string `json:"depends_on"`
	}{canonicalOwnerControl{ExpectedAssignmentEpoch: issue.AssignmentEpoch}, []string{ai}}
	op := canonicalLifecycleControl(t, h, issue, "dependencies", payload)
	if op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeInvalidRequest {
		t.Fatal("combined canonical/stage continuation cycle admitted", op)
	}
}
