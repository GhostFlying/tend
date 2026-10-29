package app

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestFollowupRegressionCancelledFollowupVisibility(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	r := h.followup(w, protocol.FollowupSpec{Kind: "at", At: h.clock.Now().UnixMilli(), Instruction: "CANCELLED_FOLLOWUP_INSTRUCTION"})
	o := h.occurrences(r.RuleID)[0]
	if op := h.controlFollowup(r, "cancel", nil); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.occurrences(r.RuleID)[0].Status != "cancelled" {
		t.Fatal("setup failed")
	}
	err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		m, err := store.GetMessage(t.Context(), tx.Q(), o.MessageID)
		if err != nil {
			return err
		}
		it := protocol.ConversationItem{ConversationContent: protocol.ConversationContent{MessageID: m.MessageID, Author: m.Author, Body: m.Body}}
		if err := store.HideUndeliveredOwner(t.Context(), tx.Q(), &it); err != nil {
			return err
		}
		if strings.Contains(it.Body, "CANCELLED_FOLLOWUP_INSTRUCTION") {
			t.Errorf("cancelled instruction still reaches conversation_read and full snapshot visibility filter: %s", it.Body)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestFollowupRegressionCreatePlanningReplay(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	h.app.followupsEnabled = true
	source := h.ownerEvidence().Reference
	p := protocol.FollowupSpec{Kind: "at", At: h.clock.Now().Add(time.Hour).UnixMilli(), Purpose: "check", Instruction: "read facts", Timezone: "UTC", SourceID: source}
	cmd := FollowupCommand{Action: "create"}
	for range 2 {
		op := h.exec(mainAgent, h.env(cmd.Operation(), workTarget(w.WorkItemID), p, rev(w.Revision)), cmd)
		if op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	if got := h.count("followup_rules"); got != 1 {
		t.Errorf("replaying same hosted planning input made %d rules", got)
	}
	h.clock.Advance(time.Hour)
	h.sweepFollowups()
	if got := h.count("followup_occurrences"); got != 1 {
		t.Errorf("same planning input queued %d separate wakes", got)
	}
}

func TestFollowupRegressionPeriodicClockRollbackResume(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	r := h.followup(w, protocol.FollowupSpec{Kind: "every", EverySeconds: 300, ExpiresAt: h.clock.Now().Add(24 * time.Hour).UnixMilli()})
	h.clock.Advance(5 * time.Minute)
	h.sweepFollowups()
	h.legacyMainBatches()
	d, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
	if err != nil || !ok {
		t.Fatal(err, ok)
	}
	if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Delivered}); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(-5 * time.Minute)
	r = h.readFollowup(r.RuleID)
	if op := h.controlFollowup(r, "pause", nil); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	r = h.readFollowup(r.RuleID)
	if op := h.controlFollowup(r, "resume", nil); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	h.clock.Advance(5 * time.Minute)
	cmd := SweepFollowups{}
	op, err := h.app.Execute(t.Context(), protocol.Principal{Kind: protocol.PrincipalSystem}, h.env(cmd.Operation(), protocol.Target{Type: "followups"}, struct{}{}, nil), cmd)
	if err != nil {
		t.Fatalf("sweep stuck after rollback and resume: %v", err)
	}
	if op.Error != nil {
		t.Fatal(op)
	}
	if got := len(h.occurrences(r.RuleID)); got != 1 {
		t.Fatalf("repeated planned slot: %d", got)
	}
}

func TestFollowupRegressionOneTimeInvalidationAfterExpiry(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	source := h.ownerEvidence().Reference
	choice := h.askChoice(w, source)
	r := h.followup(w, protocol.FollowupSpec{Kind: "condition", ExpiresAt: h.clock.Now().Add(time.Hour).UnixMilli(), Condition: &protocol.FollowupCondition{Kind: "choice_answered", ChoiceID: choice.ChoiceID}})
	h.clock.Advance(time.Hour)
	h.sweepFollowups()
	if len(h.occurrences(r.RuleID)) != 1 {
		t.Fatal("setup")
	}
	cmd := UpdateWorkItemChoice{Action: "replace"}
	env := h.env(cmd.Operation(), protocol.Target{Type: "work_item_choice", ID: choice.ChoiceID}, protocol.UpdateWorkItemChoicePayload{Replacement: &protocol.CreateWorkItemChoicePayload{Question: "new question", SourceID: source}}, rev(choice.Version))
	env.Reason = "facts changed"
	if op := h.exec(owner, env, cmd); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	r = h.readFollowup(r.RuleID)
	b, _ := json.Marshal(r)
	if r.Status != "paused" || h.occurrences(r.RuleID)[0].Status != "cancelled" {
		t.Errorf("replaced target left unclaimed old occurrence executable: %s; occurrence=%s", b, h.occurrences(r.RuleID)[0].Status)
	}
}

func TestFollowupRegressionSnoozedExpiryStarvesOtherRules(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	op := h.exec(owner, h.env("work_items.update", workTarget(w.WorkItemID), protocol.UpdateWorkItemPayload{IssuePlan: w.IssuePlan, Status: "snoozed"}, rev(w.Revision)), UpdateIssue{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	w = h.work(w.WorkItemID)
	for range 100 {
		h.followup(w, protocol.FollowupSpec{Kind: "every", EverySeconds: 86400, ExpiresAt: h.clock.Now().Add(time.Hour).UnixMilli()})
	}
	due := h.followup(w, protocol.FollowupSpec{Kind: "at", At: h.clock.Now().Add(2 * time.Hour).UnixMilli()})
	h.clock.Advance(2 * time.Hour)
	for range 3 {
		h.sweepFollowups()
		h.clock.Advance(time.Minute)
	}
	if len(h.occurrences(due.RuleID)) != 1 {
		t.Errorf("due one-time followup never fires: 100 snoozed expired rules monopolize the sweep limit until tomorrow")
	}
}

func TestFollowupManualOneTimeDoesNotScheduleAgain(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	r := h.followup(w, protocol.FollowupSpec{Kind: "at", At: h.clock.Now().Add(time.Hour).UnixMilli()})
	if op := h.controlFollowup(r, "trigger", nil); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	h.legacyMainBatches()
	d, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
	if err != nil || !ok {
		t.Fatal(err, ok)
	}
	if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Delivered}); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(2 * time.Hour)
	h.sweepFollowups()
	if len(h.occurrences(r.RuleID)) != 1 || h.readFollowup(r.RuleID).Status != "finished" {
		t.Fatal("manual check repeated automatically")
	}
}
