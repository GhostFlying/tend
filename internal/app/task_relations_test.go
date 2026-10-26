package app

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func relationDetail(t *testing.T, h *harness, id string) protocol.TaskDetail {
	t.Helper()
	var d protocol.TaskDetail
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error { var err error; d, err = views.Task(t.Context(), tx.Q(), id); return err }); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestFollowUpPreservesCompletedTaskAndReadsBothDirections(t *testing.T) {
	h := newHarness(t)
	original, acc := h.toAcceptanceClaim()
	h.runAs(acc, protocol.OpAcceptancesSubmit, protocol.VerdictPayload{Verdict: protocol.VerdictPassed}, SubmitAcceptance{})
	before := h.task(original)
	refs := []protocol.TaskRelation{{Kind: protocol.RelationFollowUp, TaskID: original}}
	env := h.env("tasks.create", protocol.Target{Type: "task"}, createTaskPayload{Title: "fix manually discovered issue", Spec: testSpec, Relations: refs}, nil)
	first := h.exec(owner, env, CreateTask{})
	if first.Status != protocol.OpSucceeded {
		t.Fatal(first)
	}
	var result taskResult
	if err := json.Unmarshal(first.Result, &result); err != nil {
		t.Fatal(err)
	}
	replay := h.exec(owner, env, CreateTask{})
	if string(first.Result) != string(replay.Result) {
		t.Fatal("create replay changed result")
	}
	if got := h.task(original); got != before {
		t.Fatalf("follow-up changed original: %+v", got)
	}
	follow := relationDetail(t, h, result.TaskID)
	parent := relationDetail(t, h, original)
	if follow.Lifecycle != protocol.TaskQueued || follow.SpecVersion != 1 || len(follow.Relations) != 1 || follow.Relations[0].Direction != protocol.RelationOutgoing || follow.Relations[0].Task.TaskID != original {
		t.Fatalf("follow-up = %+v", follow)
	}
	if len(parent.Relations) != 1 || parent.Relations[0].Direction != protocol.RelationIncoming || parent.Relations[0].Task.TaskID != follow.TaskID {
		t.Fatalf("backlinks = %+v", parent.Relations)
	}
	if follow.Workspace == parent.Workspace {
		t.Fatal("follow-up inherited the original workspace")
	}
	// Even completed tasks may maintain references; this is metadata only.
	related := []protocol.TaskRelation{{Kind: protocol.RelationRelated, TaskID: follow.TaskID}}
	change := h.env("tasks.update_intent", taskTarget(original), updateIntentPayload{Relations: &related}, rev(before.ControlRevision))
	op := h.exec(owner, change, UpdateIntent{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	after := h.task(original)
	if after.Lifecycle != protocol.TaskCompleted || after.SpecVersion != before.SpecVersion || after.CurrentArtifactID != before.CurrentArtifactID || after.ControlRevision != before.ControlRevision+1 {
		t.Fatalf("metadata reopened completion: %+v", after)
	}
	nActions := h.count("plan_actions")
	h.exec(owner, h.env("tasks.update_intent", taskTarget(original), updateIntentPayload{Relations: &related}, rev(after.ControlRevision)), UpdateIntent{})
	if h.task(original).ControlRevision != after.ControlRevision || h.count("plan_actions") != nActions {
		t.Fatal("identical references changed task/work")
	}
	empty := []protocol.TaskRelation{}
	stale := h.exec(owner, h.env("tasks.update_intent", taskTarget(original), updateIntentPayload{Relations: &empty}, rev(before.ControlRevision)), UpdateIntent{})
	if stale.Status != protocol.OpFailed || stale.Error.Code != protocol.CodeStaleRevision {
		t.Fatalf("stale metadata write = %+v", stale)
	}
	clear := h.exec(owner, h.env("tasks.update_intent", taskTarget(original), updateIntentPayload{Relations: &empty}, rev(after.ControlRevision)), UpdateIntent{})
	if clear.Status != protocol.OpSucceeded {
		t.Fatal(clear)
	}
	// Clearing one's declarations does not delete references declared by others.
	parent = relationDetail(t, h, original)
	if len(parent.Relations) != 1 || parent.Relations[0].Direction != protocol.RelationIncoming {
		t.Fatalf("clearing removed backlinks: %+v", parent.Relations)
	}
	if h.count("plan_actions") != nActions || h.count("conversation_messages WHERE author='system'") != 2 {
		t.Fatal("relations scheduled work or repeated the completion notice")
	}
	as, ok, _ := h.claim("linux-1")
	if !ok || as.TaskID != follow.TaskID || len(as.Relations) != 1 || as.Relations[0].Task.TaskID != original {
		t.Fatalf("assignment lost follow-up context: %+v", as)
	}

}

func TestTaskRelationsRejectInvalidReferencesAndCycles(t *testing.T) {
	h := newHarness(t)
	_, a := h.createTask(owner, createTaskPayload{Title: "A", Spec: testSpec})
	_, b := h.createTask(owner, createTaskPayload{Title: "B", Spec: testSpec, Relations: []protocol.TaskRelation{{Kind: protocol.RelationFollowUp, TaskID: a.TaskID}}})
	_, c := h.createTask(owner, createTaskPayload{Title: "C", Spec: testSpec, Relations: []protocol.TaskRelation{{Kind: protocol.RelationFollowUp, TaskID: b.TaskID}}})
	cases := []struct {
		name string
		refs []protocol.TaskRelation
		code protocol.Code
	}{
		{"self", []protocol.TaskRelation{{Kind: protocol.RelationRelated, TaskID: a.TaskID}}, protocol.CodeInvalidRequest},
		{"missing", []protocol.TaskRelation{{Kind: protocol.RelationRelated, TaskID: h.ids.New(protocol.PrefixTask)}}, protocol.CodeNotFound},
		{"malformed", []protocol.TaskRelation{{Kind: protocol.RelationRelated, TaskID: "bad"}}, protocol.CodeInvalidRequest},
		{"kind", []protocol.TaskRelation{{Kind: "depends_on", TaskID: b.TaskID}}, protocol.CodeInvalidRequest},
		{"duplicate", []protocol.TaskRelation{{Kind: protocol.RelationRelated, TaskID: b.TaskID}, {Kind: protocol.RelationRelated, TaskID: b.TaskID}}, protocol.CodeInvalidRequest},
		{"cycle", []protocol.TaskRelation{{Kind: protocol.RelationFollowUp, TaskID: c.TaskID}}, protocol.CodeInvalidRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := h.task(a.TaskID)
			op := h.exec(owner, h.env("tasks.update_intent", taskTarget(a.TaskID), updateIntentPayload{Relations: &tc.refs}, rev(before.ControlRevision)), UpdateIntent{})
			if op.Status != protocol.OpFailed || op.Error.Code != tc.code {
				t.Fatalf("invalid references: %+v", op)
			}
			if got := h.task(a.TaskID); got != before {
				t.Fatal("invalid reference changed task")
			}
		})
	}
	// A missing reference rolls back a new task and its implementation action.
	nTasks, nActions := h.count("tasks"), h.count("plan_actions")
	op, _ := h.createTask(owner, createTaskPayload{Title: "invalid", Spec: testSpec, Relations: cases[1].refs})
	if op.Status != protocol.OpFailed || h.count("tasks") != nTasks || h.count("plan_actions") != nActions {
		t.Fatal("failed creation left a partial task")
	}
}

func TestFault_TaskRelationsFailureRollsBackCreationAndUpdate(t *testing.T) {
	h := newHarness(t)
	_, parent := h.createTask(owner, createTaskPayload{Title: "parent", Spec: testSpec})
	_, child := h.createTask(owner, createTaskPayload{Title: "child", Spec: testSpec})
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `CREATE TRIGGER reject_relation BEFORE INSERT ON task_relations BEGIN SELECT RAISE(ABORT, 'relation unavailable'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	refs := []protocol.TaskRelation{{Kind: protocol.RelationFollowUp, TaskID: parent.TaskID}}
	before := h.task(child.TaskID)
	nTasks, nActions := h.count("tasks"), h.count("plan_actions")
	create := h.env("tasks.create", protocol.Target{Type: "task"}, createTaskPayload{Title: "followup", Spec: testSpec, Relations: refs}, nil)
	if _, err := h.app.Execute(t.Context(), owner, create, CreateTask{}); err == nil {
		t.Fatal("creation succeeded despite failed reference")
	}
	if h.count("tasks") != nTasks || h.count("plan_actions") != nActions {
		t.Fatal("failed relation left a partial new task")
	}
	update := h.env("tasks.update_intent", taskTarget(child.TaskID), updateIntentPayload{Relations: &refs}, rev(before.ControlRevision))
	if _, err := h.app.Execute(t.Context(), owner, update, UpdateIntent{}); err == nil {
		t.Fatal("update succeeded despite failed reference")
	}
	if h.task(child.TaskID) != before {
		t.Fatal("failed relation advanced task revision")
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error { _, err := tx.Exec(t.Context(), `DROP TRIGGER reject_relation`); return err }); err != nil {
		t.Fatal(err)
	}
	if op := h.exec(owner, create, CreateTask{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := h.exec(owner, update, UpdateIntent{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}
