package app

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func startAnswerReview(t *testing.T, h *harness, source string) store.MainTurn {
	t.Helper()
	var turn store.MainTurn
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT o.request_id,r.message_id FROM conversation_return_links r JOIN outbox o ON json_extract(o.payload_json,'$.message_id')=r.message_id WHERE r.source_message_id=? AND o.destination='main_thread'`, source).Scan(&turn.RequestID, &turn.MessageID)
	}); err != nil {
		t.Fatal(err)
	}
	turn.ThreadID = mainAgent.Thread
	turn.TurnID = "answer-" + turn.RequestID
	turn.Status = store.TurnStarted
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	state, err := h.app.LogicalTurn(t.Context(), turn.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range state.Messages {
		if m.MessageID == source {
			found = true
		}
	}
	if !found {
		t.Fatal("answer original absent from logical review", state)
	}
	return turn
}

func reviewDecisionAnswer(t *testing.T, h *harness, decision string) {
	t.Helper()
	var source string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT answered_message_id FROM decisions WHERE decision_id=?`, decision).Scan(&source)
	}); err != nil {
		t.Fatal(err)
	}
	turn := startAnswerReview(t, h, source)
	if h.associations(source).Disposition == "pending" {
		if op := h.associate(mainAgent, source); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	commitFixtureTurn(t, h, turn.RequestID)
	turn.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
}

func TestPanelChoiceAnswerRequiresOriginalContextReview(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	h.prepareAgent(w.WorkItemID)
	c := h.askChoice(w, h.ownerEvidence().Reference)
	op := h.exec(owner, h.env("work_item_choices.respond", protocol.Target{Type: "work_item_choice", ID: c.ChoiceID}, protocol.UpdateWorkItemChoicePayload{Answer: "Later"}, rev(c.Version)), UpdateWorkItemChoice{Action: "respond"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var answer protocol.WorkItemChoice
	if err := json.Unmarshal(op.Result, &answer); err != nil {
		t.Fatal(err)
	}
	a := h.associations(answer.AnswerSourceID)
	if a.Disposition != "linked" || a.FactsReviewed || len(a.Items) != 1 || a.Items[0].WorkItemID != w.WorkItemID {
		t.Fatal(a)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		ready, err := store.WorkItemContextReady(t.Context(), tx.Q(), w.WorkItemID)
		if ready {
			t.Error("unreviewed answer released background context")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	turn := startAnswerReview(t, h, answer.AnswerSourceID)
	commitFixtureTurn(t, h, turn.RequestID)
	if !h.associations(answer.AnswerSourceID).FactsReviewed {
		t.Fatal("answer was not reviewed")
	}
}

func TestOrdinaryDecisionAnswerBlocksNewWorkUntilReviewed(t *testing.T) {
	h := newHarness(t)
	_, d := h.failImplementation()
	op := h.exec(owner, h.env("decisions.respond", protocol.Target{Type: "decision", ID: d.DecisionID}, protocol.AnswerDecisionPayload{Version: d.Version, Option: "retry the implementation", Text: "Keep the existing constraints"}, nil), AnswerDecision{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if _, ok, _ := h.claim("linux-1"); ok {
		t.Fatal("unreviewed owner answer released new work")
	}
	reviewDecisionAnswer(t, h, d.DecisionID)
	if _, ok, _ := h.claim("linux-1"); !ok {
		t.Fatal("reviewed answer did not release work")
	}
}

func TestWorkerAnswerAndSystemNoticesRetainWorkItemAssociation(t *testing.T) {
	h, as, _ := questionRun(t, protocol.RoleImplement)
	q := askQuestion(t, h, as, scopeQuestion)
	if op := answerQuestion(t, h, q, "renderer only", ""); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var answer string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT answered_message_id FROM decisions WHERE decision_id=?`, q.DecisionID).Scan(&answer)
	}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{q.QuestionMessageID, answer} {
		a := h.associations(id)
		if a.Disposition != "linked" || len(a.Items) == 0 {
			t.Fatal(id, a)
		}
	}
	startAnswerReview(t, h, answer)
	if h.associations(answer).FactsReviewed {
		t.Fatal("native admission falsely reviewed facts")
	}
}

func TestStructuredOwnerEffectReviewIsNarrowAndIdempotent(t *testing.T) {
	for _, mode := range []string{"structured", "text", "existing-source", "other-pending"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t)
			task, d := h.failImplementation()
			p := protocol.AnswerDecisionPayload{Version: d.Version, Option: "retry the implementation"}
			principal := owner
			cmd := AnswerDecision{}
			if mode == "text" {
				p.Text = "Please preserve the original constraint"
			}
			if mode == "existing-source" {
				p.SourceMessageID = h.ownerEvidence().Reference
			}
			if mode == "other-pending" {
				var item string
				if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
					return tx.Q().QueryRowContext(t.Context(), `SELECT work_item_id FROM work_item_task_links WHERE task_id=?`, task).Scan(&item)
				}); err != nil {
					t.Fatal(err)
				}
				if op := h.associate(mainAgent, h.ownerEvidence().Reference, item); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			env := h.env(cmd.Operation(), protocol.Target{Type: "decision", ID: d.DecisionID}, p, nil)
			op := h.exec(principal, env, cmd)
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			var source string
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				return tx.Q().QueryRowContext(t.Context(), `SELECT answered_message_id FROM decisions WHERE decision_id=?`, d.DecisionID).Scan(&source)
			}); err != nil {
				t.Fatal(err)
			}
			reviewed := mode == "structured" || mode == "other-pending"
			a := h.associations(source)
			if a.FactsReviewed != reviewed || a.Disposition != "linked" {
				t.Fatal(a)
			}
			facts := h.count("work_item_context_facts")
			if reviewed && facts != 1 || !reviewed && facts != 0 {
				t.Fatalf("unexpected program facts %d", facts)
			}
			if again := h.exec(principal, env, cmd); again.Status != protocol.OpSucceeded || h.count("work_item_context_facts") != facts {
				t.Fatal("replay duplicated program review", again)
			}
			_, claimed, _ := h.claim("linux-1")
			if claimed != (mode == "structured") {
				t.Fatalf("claim=%v for %s", claimed, mode)
			}
		})
	}
}

func TestAnswerReturnLinkUsesOriginalBackgroundConversation(t *testing.T) {
	for _, kind := range []string{"decision", "choice"} {
		t.Run(kind, func(t *testing.T) {
			h := newHarness(t)
			task, d := h.failImplementation()
			var item string
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				return tx.Q().QueryRowContext(t.Context(), `SELECT work_item_id FROM work_item_task_links WHERE task_id=?`, task).Scan(&item)
			}); err != nil {
				t.Fatal(err)
			}
			op := h.exec(owner, h.env("conversation.open_background", workTarget(item), struct{}{}, nil), OpenAgentConversation{})
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			conversation := "work-item:" + item
			source := h.ids.New(protocol.PrefixMessage)
			if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
				_, err := tx.InsertMessage(t.Context(), store.MessageRow{MessageID: source, Author: "owner", Body: "Please retry within the original scope", ConversationID: conversation})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if kind == "decision" {
				op = h.exec(owner, h.env("decisions.respond", protocol.Target{Type: "decision", ID: d.DecisionID}, protocol.AnswerDecisionPayload{Version: d.Version, Option: "retry the implementation", SourceMessageID: source}, nil), AnswerDecision{})
			} else {
				c := h.askChoice(h.work(item), source)
				cmd := UpdateWorkItemChoice{Action: "respond", FromMessage: true}
				op = h.exec(owner, h.env(cmd.Operation(), protocol.Target{Type: "work_item_choice", ID: c.ChoiceID}, protocol.UpdateWorkItemChoicePayload{Answer: "Later", OwnerMessageID: source}, rev(c.Version)), cmd)
			}
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			var linkedConversation string
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				return tx.Q().QueryRowContext(t.Context(), `SELECT conversation_id FROM conversation_return_links WHERE source_message_id=?`, source).Scan(&linkedConversation)
			}); err != nil {
				t.Fatal(err)
			}
			if linkedConversation != conversation {
				t.Fatal(linkedConversation)
			}
			startAnswerReview(t, h, source)
		})
	}
}

func TestTaskBindingCannotOverrideOwnerUnassigned(t *testing.T) {
	h := newHarness(t)
	task, _ := h.failImplementation()
	source := h.ownerEvidence().Reference
	if op := h.associate(owner, source); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	err := h.store.Write(t.Context(), func(tx *store.Tx) error { return tx.BindMessage(t.Context(), source, "task", task, "source") })
	if protocol.CodeOf(err) != protocol.CodeInvalidRequest {
		t.Fatal(err)
	}
	if a := h.associations(source); a.Disposition != "unassigned" || len(a.Items) != 0 {
		t.Fatal(a)
	}
}
