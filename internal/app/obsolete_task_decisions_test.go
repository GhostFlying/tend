package app

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func readTaskDecision(t *testing.T, h *harness, id string) store.DecisionRow {
	t.Helper()
	var d store.DecisionRow
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		d, err = store.GetDecision(t.Context(), tx.Q(), id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestObsoleteTaskDecisionCancellationRetiresOnlyOriginalQuestion(t *testing.T) {
	h := newHarness(t)
	task, old := h.failImplementation()
	_, current := h.failImplementation()
	cancel := h.env("tasks.cancel", taskTarget(task), map[string]any{}, rev(h.task(task).ControlRevision))
	op := h.exec(owner, cancel, CancelTask{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	after := readTaskDecision(t, h, old.DecisionID)
	if after.Status != store.DecisionCancelled || after.Version != old.Version+1 || after.AnswerJSON != "" {
		t.Fatalf("old question not retired: %+v", after)
	}
	if got := readTaskDecision(t, h, current.DecisionID); !reflect.DeepEqual(got, current) {
		t.Fatalf("independent current question changed: %+v", got)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		page, err := views.Attentions(t.Context(), tx.Q(), views.AttentionFilter{IncludeArchived: true})
		if err != nil {
			return err
		}
		found := map[string]string{}
		for _, a := range page.Items {
			found[a.DecisionID] = a.Status
		}
		if found[old.DecisionID] != "withdrawn" || found[current.DecisionID] != "open" {
			t.Fatalf("attention lifecycle: %+v", found)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	replay := h.exec(owner, cancel, CancelTask{})
	if replay.Status != protocol.OpFailed || protocol.CodeOf(replay.Error) != protocol.CodeStaleRevision {
		t.Fatalf("cancel retry bypassed CAS: %+v %+v", op, replay)
	}
	if got := readTaskDecision(t, h, old.DecisionID); !reflect.DeepEqual(got, after) {
		t.Fatal("replay advanced question twice")
	}
	late := h.env("decisions.respond", protocol.Target{Type: "decision", ID: old.DecisionID}, protocol.AnswerDecisionPayload{Version: old.Version, Option: "retry the implementation"}, nil)
	failed := h.exec(owner, late, AnswerDecision{})
	if failed.Status != protocol.OpFailed || failed.Error.Code != protocol.CodeStaleRevision {
		t.Fatal(failed)
	}
	if got := h.exec(owner, late, AnswerDecision{}); !reflect.DeepEqual(got, failed) {
		t.Fatal("late answer retry lost original failure")
	}
	if got := readTaskDecision(t, h, current.DecisionID); !reflect.DeepEqual(got, current) {
		t.Fatal("late answer changed new responsibility")
	}
}

func TestObsoleteTaskDecisionRejectsLegacyTerminalBeforeAnswerSideEffects(t *testing.T) {
	for _, state := range []protocol.TaskLifecycle{protocol.TaskCancelled, protocol.TaskCompleted} {
		t.Run(string(state), func(t *testing.T) {
			h := newHarness(t)
			task, d := h.failImplementation()
			// Simulate an older persisted task whose ordinary question was not retired.
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE tasks SET lifecycle=? WHERE task_id=?`, state, task)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			var beforeMessages int
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				return tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM conversation_messages`).Scan(&beforeMessages)
			}); err != nil {
				t.Fatal(err)
			}
			beforeTask := h.task(task)
			env := h.env("decisions.respond", protocol.Target{Type: "decision", ID: d.DecisionID}, protocol.AnswerDecisionPayload{Version: d.Version, Option: "retry the implementation"}, nil)
			op := h.exec(owner, env, AnswerDecision{})
			if op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeStaleRevision {
				t.Fatal(op)
			}
			if got := readTaskDecision(t, h, d.DecisionID); !reflect.DeepEqual(got, d) {
				t.Fatalf("rejected answer changed history: %+v", got)
			}
			if got := h.task(task); got != beforeTask {
				t.Fatal("late answer changed terminal task")
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				var n int
				err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM conversation_messages`).Scan(&n)
				if n != beforeMessages {
					t.Fatal("late answer created message")
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestObsoleteTaskDecisionPreservesAcceptedAnswerAndUnscopedQuestion(t *testing.T) {
	h := newHarness(t)
	task, d := h.failImplementation()
	env := h.env("decisions.respond", protocol.Target{Type: "decision", ID: d.DecisionID}, protocol.AnswerDecisionPayload{Version: d.Version, Option: "revise the spec"}, nil)
	accepted := h.exec(owner, env, AnswerDecision{})
	if accepted.Status != protocol.OpSucceeded {
		t.Fatal(accepted)
	}
	before := readTaskDecision(t, h, d.DecisionID)
	unscoped := h.exec(mainAgent, h.env("decisions.open", protocol.Target{Type: "decision"}, openDecisionPayload{TaskID: task, Question: "Keep the product scope?", Options: []string{"yes", "no"}}, nil), OpenDecision{})
	if unscoped.Status != protocol.OpSucceeded {
		t.Fatal(unscoped)
	}
	var opened struct {
		DecisionID string `json:"decision_id"`
	}
	if err := json.Unmarshal(unscoped.Result, &opened); err != nil {
		t.Fatal(err)
	}
	if op := h.exec(owner, h.env("tasks.cancel", taskTarget(task), map[string]any{}, rev(h.task(task).ControlRevision)), CancelTask{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if got := readTaskDecision(t, h, d.DecisionID); !reflect.DeepEqual(got, before) {
		t.Fatal("accepted answer overwritten")
	}
	if got := h.exec(owner, env, AnswerDecision{}); !reflect.DeepEqual(got, accepted) {
		t.Fatal("original accepted request not replayed")
	}
	if got := h.answer(before, "revise the spec"); got.Version != before.Version {
		t.Fatal("accepted answer history unavailable")
	}
	if got := readTaskDecision(t, h, opened.DecisionID); got.Status != store.DecisionOpen {
		t.Fatal("unscoped product question cancelled")
	}
}
