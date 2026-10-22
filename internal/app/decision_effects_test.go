package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// failImplementation creates a task whose first implementation fails, so
// tend opens a decision about it.
func (h *harness) failImplementation() (string, store.DecisionRow) {
	h.t.Helper()
	_, res := h.createTask(owner, createTaskPayload{Title: "a", Spec: testSpec})
	impl, _, _ := h.claim("linux-1")
	h.runAs(impl, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	h.runAs(impl, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "no_changes"}, Release{})
	return res.TaskID, h.openDecision(res.TaskID)
}

func (h *harness) openDecision(taskID string) store.DecisionRow {
	h.t.Helper()
	var ds []store.DecisionRow
	if err := h.store.Read(context.Background(), func(tx *store.ReadTx) error {
		var err error
		ds, err = store.ListDecisions(context.Background(), tx.Q(), taskID)
		return err
	}); err != nil || len(ds) == 0 || ds[len(ds)-1].Status != store.DecisionOpen {
		h.t.Fatalf("decisions of %s = %+v, %v", taskID, ds, err)
	}
	return ds[len(ds)-1]
}

func (h *harness) answer(d store.DecisionRow, option string) protocol.AnswerResult {
	h.t.Helper()
	op := h.exec(owner, h.env("decisions.respond", protocol.Target{Type: "decision", ID: d.DecisionID},
		protocol.AnswerDecisionPayload{Version: d.Version, Option: option}, nil), AnswerDecision{})
	var res protocol.AnswerResult
	if op.Status != protocol.OpSucceeded || json.Unmarshal(op.Result, &res) != nil {
		h.t.Fatalf("answer %q: %+v", option, op)
	}
	return res
}

func TestDecisionEffects(t *testing.T) {
	t.Run("retry the implementation", func(t *testing.T) {
		h := newHarness(t)
		taskID, d := h.failImplementation()
		res := h.answer(d, "retry the implementation")
		if res.Effect == nil || !res.Effect.Applied || res.Effect.Effect != protocol.EffectImplement {
			t.Fatalf("effect = %+v", res.Effect)
		}
		if got := h.task(taskID); got.BlockReason != protocol.BlockNone || got.ImplementGeneration != 2 {
			t.Errorf("task = %+v, want generation 2 unblocked", got)
		}
		next, ok, _ := h.claim("linux-1")
		if !ok || next.Role != protocol.RoleImplement || next.Generation != 2 {
			t.Errorf("claim after the answer = %+v, %v", next, ok)
		}
		// The answer is told before what it caused.
		answered, requested := h.events("decision.answered"), h.events("task.implement_requested")
		if len(answered) != 1 || len(requested) != 1 || answered[0].Seq > requested[0].Seq {
			t.Errorf("events: answered %+v, requested %+v", answered, requested)
		}
	})

	t.Run("cancel the task", func(t *testing.T) {
		h := newHarness(t)
		taskID, d := h.failImplementation()
		if res := h.answer(d, "cancel the task"); res.Effect == nil || !res.Effect.Applied || res.Effect.Effect != protocol.EffectCancel {
			t.Fatalf("effect = %+v", res.Effect)
		}
		if got := h.task(taskID); got.Lifecycle != protocol.TaskCancelled {
			t.Errorf("task = %s, want cancelled", got.Lifecycle)
		}
	})

	t.Run("retry acceptance", func(t *testing.T) {
		h := newHarness(t)
		taskID, acc := h.toAcceptanceClaim()
		h.runAs(acc, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "sandbox crashed"}, Release{})
		if res := h.answer(h.openDecision(taskID), "retry acceptance"); res.Effect == nil || !res.Effect.Applied {
			t.Fatalf("effect = %+v", res.Effect)
		}
		retry, ok, _ := h.claim("linux-1")
		if !ok || retry.Role != protocol.RoleAccept || retry.Generation != 2 {
			t.Errorf("claim after the answer = %+v, %v", retry, ok)
		}
	})

	t.Run("an option that needs judgment", func(t *testing.T) {
		h := newHarness(t)
		taskID, d := h.failImplementation()
		if res := h.answer(d, "revise the spec"); res.Effect != nil {
			t.Errorf("effect = %+v, want none", res.Effect)
		}
		if got := h.task(taskID); got.BlockReason != protocol.BlockWaitingDecision {
			t.Errorf("task = %+v, want still waiting", got)
		}
	})

	t.Run("the task changed after the question", func(t *testing.T) {
		h := newHarness(t)
		taskID, d := h.failImplementation()
		if op := h.exec(owner, h.env("control.set_mode", taskTarget(taskID), protocol.SetControlModePayload{Mode: protocol.ControlPaused},
			rev(h.task(taskID).ControlRevision)), SetControlMode{}); op.Status != protocol.OpSucceeded {
			t.Fatalf("pause: %+v", op.Error)
		}
		before := h.task(taskID)
		res := h.answer(d, "retry the implementation")
		if res.Effect == nil || res.Effect.Applied || !strings.Contains(res.Effect.Reason, "changed after the question") {
			t.Fatalf("effect = %+v, want not applied", res.Effect)
		}
		if got := h.task(taskID); got != before {
			t.Errorf("task changed: %+v -> %+v", before, got)
		}
		// Answering the same again returns what was recorded.
		if again := h.answer(d, "retry the implementation"); again.Effect == nil || *again.Effect != *res.Effect || again.Version != res.Version {
			t.Errorf("same answer again = %+v, want %+v", again, res)
		}
	})

	t.Run("a planner's decision", func(t *testing.T) {
		h := newHarness(t)
		_, res := h.createTask(owner, createTaskPayload{Title: "a", Spec: testSpec})
		h.exec(mainAgent, h.env("decisions.open", protocol.Target{Type: "decision"}, openDecisionPayload{
			TaskID: res.TaskID, Question: "Cancel it?", Options: []string{"cancel the task", "keep it"}, SourceMessageID: "msg_1",
		}, nil), OpenDecision{})
		if got := h.answer(h.openDecision(res.TaskID), "cancel the task"); got.Effect != nil {
			t.Errorf("effect = %+v, want none", got.Effect)
		}
		if got := h.task(res.TaskID); got.Lifecycle == protocol.TaskCancelled {
			t.Error("a planner's option was carried out")
		}
	})
}

func TestMessageChoosesDecisionOption(t *testing.T) {
	h := newHarness(t)
	_, d := h.failImplementation()
	say := func(c protocol.DecisionChoice) protocol.Operation {
		t.Helper()
		return h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "message"},
			protocol.SubmitMessagePayload{Body: "about the task: " + c.Option, Decision: &c}, nil), SubmitMessage{})
	}
	refused := []struct {
		name string
		c    protocol.DecisionChoice
		code protocol.Code
	}{
		{"an option tend carries out", protocol.DecisionChoice{DecisionID: d.DecisionID, Version: d.Version, Option: "retry the implementation"}, protocol.CodeInvalidRequest},
		{"no such option", protocol.DecisionChoice{DecisionID: d.DecisionID, Version: d.Version, Option: "something else"}, protocol.CodeInvalidRequest},
		{"an older version", protocol.DecisionChoice{DecisionID: d.DecisionID, Version: d.Version - 1, Option: "revise the spec"}, protocol.CodeStaleRevision},
		{"no such decision", protocol.DecisionChoice{DecisionID: "dec_x", Version: 1, Option: "revise the spec"}, protocol.CodeNotFound},
	}
	for _, r := range refused {
		if op := say(r.c); op.Status != protocol.OpFailed || op.Error == nil || op.Error.Code != r.code {
			t.Errorf("%s: %+v, want %s", r.name, op, r.code)
		}
	}
	c := protocol.DecisionChoice{DecisionID: d.DecisionID, Version: d.Version, Option: "revise the spec"}
	op := say(c)
	var res struct {
		MessageID string `json:"message_id"`
	}
	if op.Status != protocol.OpAccepted || json.Unmarshal(op.Result, &res) != nil {
		t.Fatalf("choice = %+v", op)
	}
	var m store.MessageView
	var bound int
	if err := h.store.Read(context.Background(), func(tx *store.ReadTx) error {
		var err error
		if m, err = store.GetMessage(context.Background(), tx.Q(), res.MessageID); err != nil {
			return err
		}
		return tx.Q().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM conversation_bindings WHERE message_id = ? AND object_id = ?`,
			res.MessageID, d.DecisionID).Scan(&bound)
	}); err != nil {
		t.Fatal(err)
	}
	var got protocol.DecisionChoice
	if json.Unmarshal([]byte(m.DecisionJSON), &got) != nil || got != c || bound != 1 {
		t.Errorf("message keeps %q, bound %d; want the choice and a binding", m.DecisionJSON, bound)
	}
	// The decision itself waits for the main agent to record the answer.
	if d2 := h.openDecision(d.TaskID); d2.Version != d.Version {
		t.Errorf("decision moved to version %d", d2.Version)
	}
}
