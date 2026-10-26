package app

import (
	"context"
	"encoding/json"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"testing"
)

func (h *harness) askChoice(w protocol.WorkItem, source string) protocol.WorkItemChoice {
	h.t.Helper()
	p := protocol.CreateWorkItemChoicePayload{Question: "Continue to deployment?", Options: []string{"Continue", "Later"}, Recommendation: "Continue", SourceID: source}
	op := h.exec(mainAgent, h.env("work_item_choices.create", workTarget(w.WorkItemID), p, rev(w.Revision)), CreateWorkItemChoice{})
	if op.Status != protocol.OpSucceeded {
		h.t.Fatal(op)
	}
	var c protocol.WorkItemChoice
	if err := json.Unmarshal(op.Result, &c); err != nil {
		h.t.Fatal(err)
	}
	return c
}
func TestWorkItemChoiceDedupAnswerAndStaleWithdrawal(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	source := h.ownerEvidence().Reference
	c := h.askChoice(w, source)
	again := h.askChoice(w, source)
	if c.ChoiceID != again.ChoiceID || h.count("work_item_choices") != 1 {
		t.Fatal("duplicate planning question")
	}
	env := h.env("work_item_choices.respond", protocol.Target{Type: "work_item_choice", ID: c.ChoiceID}, protocol.UpdateWorkItemChoicePayload{Answer: "Later"}, rev(1))
	cmd := UpdateWorkItemChoice{Action: "respond"}
	first := h.exec(owner, env, cmd)
	second := h.exec(owner, env, cmd)
	if first.Status != protocol.OpSucceeded || string(first.Result) != string(second.Result) {
		t.Fatal(first, second)
	}
	if h.count("runs") != 0 {
		t.Fatal("planning choice must not allocate execution")
	}
	expectWorkCode(t, h.exec(mainAgent, h.env("work_item_choices.withdraw", env.Target, protocol.UpdateWorkItemChoicePayload{}, rev(1)), UpdateWorkItemChoice{Action: "withdraw"}), protocol.CodeStaleRevision)
}
func TestWorkItemChoiceReplacementAndOwnerProvenance(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	source := h.ownerEvidence().Reference
	c := h.askChoice(w, source)
	target := protocol.Target{Type: "work_item_choice", ID: c.ChoiceID}
	cmd := UpdateWorkItemChoice{Action: "respond", FromMessage: true}
	expectWorkCode(t, h.exec(mainAgent, h.env(cmd.Operation(), target, protocol.UpdateWorkItemChoicePayload{Answer: "Continue", OwnerMessageID: c.MessageID}, rev(1)), cmd), protocol.CodeInvalidRequest)
	replacement := protocol.CreateWorkItemChoicePayload{Question: "Already merged; deploy now?", Options: []string{"Deploy", "Later"}, SourceID: source}
	env := h.env("work_item_choices.replace", target, protocol.UpdateWorkItemChoicePayload{Replacement: &replacement}, rev(1))
	env.Reason = "Provider confirmed merge"
	op := h.exec(mainAgent, env, UpdateWorkItemChoice{Action: "replace"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var old protocol.WorkItemChoice
	_ = json.Unmarshal(op.Result, &old)
	if old.Status != "superseded" || old.ReplacedBy == "" {
		t.Fatal(old)
	}
	var next protocol.WorkItemChoice
	err := h.store.Read(context.Background(), func(tx *store.ReadTx) error {
		var e error
		next, e = store.GetWorkItemChoice(context.Background(), tx.Q(), old.ReplacedBy)
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	if next.Supersedes != old.ChoiceID || next.Status != "open" {
		t.Fatal(next)
	}
	expectWorkCode(t, h.exec(owner, h.env("work_item_choices.respond", target, protocol.UpdateWorkItemChoicePayload{Answer: "Continue"}, rev(1)), UpdateWorkItemChoice{Action: "respond"}), protocol.CodeStaleRevision)
	op = h.exec(mainAgent, h.env(cmd.Operation(), protocol.Target{Type: "work_item_choice", ID: next.ChoiceID}, protocol.UpdateWorkItemChoicePayload{Answer: "Later", OwnerMessageID: source}, rev(1)), cmd)
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}

type choiceNotifier struct{ calls int }

func (n *choiceNotifier) Notify() { n.calls++ }

func TestChoiceAnswerNotifiesDispatcherExactlyOnce(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	c := h.askChoice(w, h.ownerEvidence().Reference)
	n := &choiceNotifier{}
	h.app.notifier = n
	env := h.env("work_item_choices.respond", protocol.Target{Type: "work_item_choice", ID: c.ChoiceID}, protocol.UpdateWorkItemChoicePayload{Answer: "Later"}, rev(c.Version))
	for range 2 {
		op := h.exec(owner, env, UpdateWorkItemChoice{Action: "respond"})
		if op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	if n.calls != 1 {
		t.Fatalf("dispatcher notifications=%d", n.calls)
	}
	if h.count("main_turns") != 0 {
		t.Fatal("application must enqueue, not start a native turn")
	}
}

func TestWorkItemChoiceAddonRelayPreservesProvenance(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	c := h.askChoice(w, h.ownerEvidence().Reference)
	cmd := UpdateWorkItemChoice{Action: "respond", FromMessage: true}
	target := protocol.Target{Type: "work_item_choice", ID: c.ChoiceID}
	payload := protocol.UpdateWorkItemChoicePayload{Answer: "Later, after I review the preview"}
	// A hosted agent cannot claim an unrecorded owner answer.
	failed := h.exec(mainAgent, h.env(cmd.Operation(), target, payload, rev(c.Version)), cmd)
	if failed.Status == protocol.OpSucceeded {
		t.Fatal("hosted caller relayed without an owner message")
	}
	addon := protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: "addon-client", Thread: "native-session", Agent: protocol.AgentCodex}
	env := h.env(cmd.Operation(), target, payload, rev(c.Version))
	op := h.exec(addon, env, cmd)
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var answer protocol.WorkItemChoice
	if err := json.Unmarshal(op.Result, &answer); err != nil {
		t.Fatal(err)
	}
	if answer.AnswerSourceKind != "addon_relay" || answer.Status != "answered" {
		t.Fatal(answer)
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		msg, err := store.GetMessage(t.Context(), tx.Q(), answer.AnswerSourceID)
		if err != nil {
			return err
		}
		var requestID string
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT request_id FROM conversation_messages WHERE message_id=?`, answer.AnswerSourceID).Scan(&requestID); err != nil {
			return err
		}
		if msg.Author != "main_agent" || requestID != env.RequestID || msg.ReplyTo != c.MessageID {
			t.Fatalf("unattributed relay: %+v", msg)
		}
		var actor string
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT actor_json FROM events WHERE request_id=? AND kind='work_item_choice.answered'`, env.RequestID).Scan(&actor); err != nil {
			return err
		}
		var principal protocol.Principal
		if err := json.Unmarshal([]byte(actor), &principal); err != nil {
			return err
		}
		if principal != addon {
			t.Fatalf("lost relay principal: %+v", principal)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	replay := h.exec(addon, env, cmd)
	if replay.Status != protocol.OpSucceeded || string(op.Result) != string(replay.Result) {
		t.Fatal(replay)
	}
}
