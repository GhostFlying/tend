package app

import (
	"context"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestWorkItemChoiceMustBeResolvedBeforeClosure(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	c := h.askChoice(w, h.ownerEvidence().Reference)
	check := func() error {
		return h.store.Read(context.Background(), func(tx *store.ReadTx) error {
			return issueCloseGuard(context.Background(), tx.Q(), w.WorkItemID)
		})
	}
	if check() == nil {
		t.Fatal("unanswered next step allowed closure")
	}
	env := h.env("work_item_choices.withdraw", protocol.Target{Type: "work_item_choice", ID: c.ChoiceID}, protocol.UpdateWorkItemChoicePayload{}, rev(c.Version))
	env.Reason = "Owner already authorized the next stage in conversation"
	if op := h.exec(mainAgent, env, UpdateWorkItemChoice{Action: "withdraw"}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if err := check(); err != nil {
		t.Fatal(err)
	}
}

func TestWorkItemChoiceAnswerCanSupplyClosureEvidence(t *testing.T) {
	for _, relay := range []bool{false, true} {
		t.Run(map[bool]string{false: "owner", true: "hosted"}[relay], func(t *testing.T) {
			h := newHarness(t)
			plan := workPlan()
			plan.Stages = nil
			w := h.createWork(plan)
			c := h.askChoice(w, h.ownerEvidence().Reference)
			closeWith := func(item protocol.WorkItem, reference string) protocol.Operation {
				p := protocol.CloseWorkItemPayload{Closure: protocol.WorkItemClosure{Kind: "declined", Reason: "Owner declined further work", Evidence: []protocol.WorkItemEvidence{{Kind: "owner_decision", Reference: reference, Summary: "Owner asked to close this concern"}}}}
				return h.exec(mainAgent, h.env("work_items.close", workTarget(item.WorkItemID), p, rev(item.Revision)), CloseIssue{})
			}
			expectWorkCode(t, closeWith(w, c.ChoiceID), protocol.CodeInvalidTransition)
			principal := owner
			if relay {
				principal = mainAgent
			}
			cmd := UpdateWorkItemChoice{Action: "respond", FromMessage: relay}
			op := h.exec(principal, h.env(cmd.Operation(), protocol.Target{Type: "work_item_choice", ID: c.ChoiceID}, protocol.UpdateWorkItemChoicePayload{Answer: "No further work; close this item", OwnerMessageID: h.ownerEvidence().Reference}, rev(c.Version)), cmd)
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			other := h.createWork(plan)
			expectWorkCode(t, closeWith(other, c.ChoiceID), protocol.CodeInvalidRequest)
			if op := closeWith(h.work(w.WorkItemID), c.ChoiceID); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
		})
	}
}
