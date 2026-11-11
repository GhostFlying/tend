package app

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestBusinessDecisionRetryReturnsCurrentAnsweredState(t *testing.T) {
	h := newHarness(t)
	env := h.env("decisions.open", protocol.Target{Type: "decision"}, openDecisionPayload{Question: "Which result?", Options: []string{"continue", "stop"}}, nil)
	first := h.exec(owner, env, OpenDecision{})
	if first.Status != protocol.OpSucceeded {
		t.Fatal(first)
	}
	answer := h.env("decisions.respond", protocol.Target{Type: "decision", ID: env.Target.ID}, protocol.AnswerDecisionPayload{Version: 1, Option: "stop"}, nil)
	if got := h.exec(owner, answer, AnswerDecision{}); got.Status != protocol.OpSucceeded {
		t.Fatal(got)
	}
	before := h.count("events")
	retry := *env
	retry.RequestID = h.ids.New(protocol.PrefixRequest)
	got := h.exec(owner, &retry, OpenDecision{})
	var result struct {
		DecisionID string `json:"decision_id"`
		Version    int64  `json:"version"`
	}
	if got.Status != protocol.OpSucceeded || json.Unmarshal(got.Result, &result) != nil || result.DecisionID != env.Target.ID || result.Version != 2 || h.count("decisions") != 1 || h.count("events") != before {
		t.Fatal(got, result)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		d, err := store.GetDecision(t.Context(), tx.Q(), env.Target.ID)
		if err == nil && d.Status != store.DecisionAnswered {
			t.Fatal(d)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	retry.Payload = json.RawMessage(`{"question":"Different goal","options":["continue","stop"]}`)
	if got, err := h.app.Execute(t.Context(), owner, &retry, OpenDecision{}); protocol.CodeOf(err) != protocol.CodeConflictPayload {
		t.Fatal("identity was reused for a new question", got, err)
	}
}

func TestBusinessAttentionRetryKeepsActualResolution(t *testing.T) {
	h := newHarness(t)
	env := h.env("attentions.create", protocol.Target{Type: "attention"}, protocol.CreateAttentionPayload{Kind: "action", Title: "Finish original action"}, nil)
	first := h.exec(owner, env, AttentionCommand{Action: "create"})
	var a protocol.Attention
	if first.Status != protocol.OpSucceeded || json.Unmarshal(first.Result, &a) != nil {
		t.Fatal(first)
	}
	if op := h.exec(owner, h.env("attentions.confirm", protocol.Target{Type: "attention", ID: a.AttentionID}, protocol.ConfirmAttentionPayload{Summary: "Finished"}, rev(a.Revision)), AttentionCommand{Action: "confirm"}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	env.RequestID = h.ids.New(protocol.PrefixRequest)
	op := h.exec(owner, env, AttentionCommand{Action: "create"})
	if op.Status != protocol.OpSucceeded || json.Unmarshal(op.Result, &a) != nil || a.Status != "resolved" || a.Revision != 2 || h.count("attentions") != 1 {
		t.Fatal(op, a)
	}
}

func TestBusinessConsultationRetryDoesNotDependOnCurrentConfiguration(t *testing.T) {
	h := hostedManagerHarness(t)
	env := h.env("conversation.consult", protocol.Target{Type: "conversation"}, protocol.CreateConsultationPayload{AgentRef: "server-hosted/planner", Title: "Original question"}, nil)
	first := h.exec(owner, env, CreateConsultation{})
	if first.Status != protocol.OpSucceeded {
		t.Fatal(first)
	}
	h.app.hostedConfiguration = nil
	env.RequestID = h.ids.New(protocol.PrefixRequest)
	again := h.exec(owner, env, CreateConsultation{})
	if again.Status != protocol.OpSucceeded || string(again.Result) != string(first.Result) || h.count("consultation_conversations") != 1 {
		t.Fatal(again)
	}
}

func TestLegacyClaimReadsActualAssignmentBeforeAllocating(t *testing.T) {
	h := newHarness(t)
	h.createTask(owner, createTaskPayload{Title: "Original goal", Spec: testSpec})
	as, ok, env := h.claim("linux-1")
	if !ok {
		t.Fatal("no assignment")
	}
	id, err := h.app.AssignedRun(t.Context(), "linux-1", env.RequestID)
	if err != nil || id != as.RunID {
		t.Fatal(id, err)
	}
	id, err = h.app.AssignedRun(t.Context(), "other-host", env.RequestID)
	if err != nil || id != "" {
		t.Fatal("another host obtained assignment", id, err)
	}
	if id, err := h.app.AssignedRun(t.Context(), "linux-1", h.ids.New(protocol.PrefixRequest)); err != nil || id != "" {
		t.Fatal(id, err)
	}
	if h.count("runs") != 1 {
		t.Fatal("read allocated work")
	}
}

func TestTaskBusinessIdentityCannotCrossPrimaryIssue(t *testing.T) {
	h := newHarness(t)
	firstIssue := createCanonicalOwnerTestIssue(t, h)
	payload := canonicalOwnerTaskPayload{TaskID: h.ids.New(protocol.PrefixTask), ExpectedAssignmentEpoch: firstIssue.AssignmentEpoch, Title: "Existing responsibility", Spec: testSpec}
	first := h.exec(owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: firstIssue.IssueID}, payload, rev(firstIssue.Revision)), CanonicalOwnerTaskCreate{})
	if first.Status != protocol.OpSucceeded {
		t.Fatal(first)
	}
	secondIssue := createCanonicalOwnerTestIssue(t, h)
	payload.ExpectedAssignmentEpoch = secondIssue.AssignmentEpoch
	before := h.count("tasks")
	second, err := h.app.Execute(t.Context(), owner, h.env("issues.create_task", protocol.Target{Type: "issue", ID: secondIssue.IssueID}, payload, rev(secondIssue.Revision)), CanonicalOwnerTaskCreate{})
	if protocol.CodeOf(err) != protocol.CodeForbidden || h.count("tasks") != before {
		t.Fatal("existing Task escaped its primary responsibility", second, err)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		primary, err := store.GetTaskIssueOwner(t.Context(), tx.Q(), payload.TaskID)
		if err == nil && primary != firstIssue.IssueID {
			t.Fatal("original Task ownership changed", primary)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
