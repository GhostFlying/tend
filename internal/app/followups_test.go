package app

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func (h *harness) followup(w protocol.WorkItem, p protocol.FollowupSpec) protocol.FollowupRule {
	h.t.Helper()
	h.app.followupsEnabled = true
	if p.Purpose == "" {
		p.Purpose = "Recheck agreed work"
	}
	if p.Instruction == "" {
		p.Instruction = "Read facts and continue within the existing authority"
	}
	if p.Timezone == "" {
		p.Timezone = "UTC"
	}
	cmd := FollowupCommand{Action: "create"}
	op := h.exec(owner, h.env(cmd.Operation(), workTarget(w.WorkItemID), p, rev(w.Revision)), cmd)
	if op.Status != protocol.OpSucceeded {
		h.t.Fatalf("create followup: %+v", op)
	}
	var r protocol.FollowupRule
	if err := json.Unmarshal(op.Result, &r); err != nil {
		h.t.Fatal(err)
	}
	return r
}
func (h *harness) readFollowup(id string) protocol.FollowupRule {
	h.t.Helper()
	var r protocol.FollowupRule
	if err := h.store.Read(h.t.Context(), func(tx *store.ReadTx) error {
		var e error
		r, e = store.GetFollowup(h.t.Context(), tx.Q(), id)
		return e
	}); err != nil {
		h.t.Fatal(err)
	}
	return r
}
func (h *harness) occurrences(id string) []protocol.FollowupOccurrence {
	h.t.Helper()
	var out []protocol.FollowupOccurrence
	if err := h.store.Read(h.t.Context(), func(tx *store.ReadTx) error {
		var e error
		out, _, e = store.FollowupOccurrences(h.t.Context(), tx.Q(), id, "")
		return e
	}); err != nil {
		h.t.Fatal(err)
	}
	return out
}
func (h *harness) sweepFollowups() {
	h.t.Helper()
	cmd := SweepFollowups{}
	op := h.exec(protocol.Principal{Kind: protocol.PrincipalSystem}, h.env(cmd.Operation(), protocol.Target{Type: "followups"}, struct{}{}, nil), cmd)
	if op.Status != protocol.OpSucceeded {
		h.t.Fatal(op)
	}
}
func (h *harness) controlFollowup(r protocol.FollowupRule, action string, spec *protocol.FollowupSpec) protocol.Operation {
	h.t.Helper()
	cmd := FollowupCommand{Action: action}
	env := h.env(cmd.Operation(), protocol.Target{Type: "followup", ID: r.RuleID}, protocol.UpdateFollowupPayload{Spec: spec}, rev(r.Revision))
	env.Reason = "owner requested this change"
	return h.exec(owner, env, cmd)
}
func TestFollowupAtDurableDedupAndNoSlot(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	r := h.followup(w, protocol.FollowupSpec{Kind: "at", At: h.clock.Now().Add(time.Hour).UnixMilli()})
	h.sweepFollowups()
	if len(h.occurrences(r.RuleID)) != 0 {
		t.Fatal("early trigger")
	}
	h.clock.Advance(2 * time.Hour)
	h.app = New(h.store, h.ids, h.clock.Now, WithFollowups(true))
	h.sweepFollowups()
	h.sweepFollowups()
	if len(h.occurrences(r.RuleID)) != 1 || h.readFollowup(r.RuleID).Status != "finished" {
		t.Fatal("one-time trigger not durable")
	}
	if h.count("runs") != 0 || h.count("plan_actions") != 0 || h.count("main_turns") != 0 {
		t.Fatal("waiting or enqueue allocated execution")
	}
}
func TestFollowupConditionBeforeAndAfterRegistration(t *testing.T) {
	for _, answeredFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "after", true: "before"}[answeredFirst], func(t *testing.T) {
			h := newHarness(t)
			w := h.createWork(workPlan())
			choice := h.askChoice(w, h.ownerEvidence().Reference)
			answer := func() {
				cmd := UpdateWorkItemChoice{Action: "respond"}
				op := h.exec(owner, h.env(cmd.Operation(), protocol.Target{Type: "work_item_choice", ID: choice.ChoiceID}, protocol.UpdateWorkItemChoicePayload{Answer: "continue"}, rev(choice.Version)), cmd)
				if op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			if answeredFirst {
				answer()
			}
			r := h.followup(w, protocol.FollowupSpec{Kind: "condition", Condition: &protocol.FollowupCondition{Kind: "choice_answered", ChoiceID: choice.ChoiceID}})
			if !answeredFirst {
				if len(h.occurrences(r.RuleID)) != 0 {
					t.Fatal("unanswered condition fired")
				}
				answer()
			}
			h.sweepFollowups()
			if len(h.occurrences(r.RuleID)) != 1 {
				t.Fatal("condition did not fire exactly once")
			}
		})
	}
}
func TestFollowupPeriodicMissesCoalesceAndUnknownPauses(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	r := h.followup(w, protocol.FollowupSpec{Kind: "every", EverySeconds: 300, ExpiresAt: h.clock.Now().Add(24 * time.Hour).UnixMilli()})
	h.clock.Advance(3 * time.Hour)
	h.sweepFollowups()
	r = h.readFollowup(r.RuleID)
	if r.TriggerCount != 1 || r.NextAt != h.clock.Now().Add(5*time.Minute).UnixMilli() {
		t.Fatal(r)
	}
	h.clock.Advance(time.Hour)
	h.sweepFollowups()
	if len(h.occurrences(r.RuleID)) != 1 {
		t.Fatal("pending occurrence duplicated")
	}
	o := h.occurrences(r.RuleID)[0]
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, e := tx.Exec(t.Context(), `UPDATE outbox SET status='unknown' WHERE json_extract(payload_json,'$.message_id')=(SELECT b.message_id FROM background_sources s JOIN background_batches b ON b.batch_id=s.batch_id WHERE s.message_id=? LIMIT 1)`, o.MessageID)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(time.Hour)
	h.sweepFollowups()
	if h.readFollowup(r.RuleID).Status != "paused" || len(h.occurrences(r.RuleID)) != 1 {
		t.Fatal("unknown became retry")
	}
}
func TestFollowupPauseUpdateCancelAndClaimFence(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	r := h.followup(w, protocol.FollowupSpec{Kind: "every", EverySeconds: 300, ExpiresAt: h.clock.Now().Add(24 * time.Hour).UnixMilli()})
	h.clock.Advance(5 * time.Minute)
	h.sweepFollowups()
	r = h.readFollowup(r.RuleID)
	if op := h.controlFollowup(r, "pause", nil); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.occurrences(r.RuleID)[0].Status != "cancelled" {
		t.Fatal("unclaimed occurrence not cancelled")
	}
	r = h.readFollowup(r.RuleID)
	if op := h.controlFollowup(r, "resume", nil); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	r = h.readFollowup(r.RuleID)
	if r.TriggerCount != 1 {
		t.Fatal("resume reset count")
	}
	h.clock.Advance(5 * time.Minute)
	h.sweepFollowups()
	h.legacyMainBatches()
	d, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
	if err != nil || !ok {
		t.Fatalf("delivery %v %v", ok, err)
	}
	r = h.readFollowup(r.RuleID)
	if op := h.controlFollowup(r, "cancel", nil); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	_ = d
	occurrences := h.occurrences(r.RuleID)
	if occurrences[len(occurrences)-1].Status != "starting" {
		t.Fatal("claimed check was falsely cancelled", occurrences)
	}
}

func TestFollowupReplacementAndCloseCancelUnclaimedInput(t *testing.T) {
	h := newHarness(t)
	plan := workPlan()
	plan.Stages = nil
	w := h.createWork(plan)
	r := h.followup(w, protocol.FollowupSpec{Kind: "every", EverySeconds: 300, ExpiresAt: h.clock.Now().Add(time.Hour).UnixMilli()})
	h.clock.Advance(5 * time.Minute)
	h.sweepFollowups()
	r = h.readFollowup(r.RuleID)
	replacement := r.FollowupSpec
	replacement.EverySeconds = 600
	if op := h.controlFollowup(r, "update", &replacement); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.occurrences(r.RuleID)[0].Status != "cancelled" {
		t.Fatal("old revision remained queued")
	}
	if h.readFollowup(r.RuleID).TriggerCount != 1 {
		t.Fatal("update reset trigger budget")
	}
	once := h.followup(w, protocol.FollowupSpec{Kind: "at", At: h.clock.Now().UnixMilli()})
	if once.Status != "finished" {
		t.Fatal("expected queued one-time rule to finish scheduling")
	}
	evidence := h.ownerEvidence()
	close := protocol.CloseWorkItemPayload{Closure: protocol.WorkItemClosure{Kind: "completed", Reason: "owner endpoint reached", Evidence: []protocol.WorkItemEvidence{evidence}}}
	op := h.exec(owner, h.env("work_items.close", workTarget(w.WorkItemID), close, rev(w.Revision)), CloseWorkItem{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.readFollowup(once.RuleID).Status != "cancelled" || h.occurrences(once.RuleID)[0].Status != "cancelled" {
		t.Fatal("close left one-time input queued")
	}
	if h.readFollowup(r.RuleID).Status != "cancelled" {
		t.Fatal("close left active rules")
	}
	w = h.work(w.WorkItemID)
	op = h.exec(owner, h.env("work_items.reopen", workTarget(w.WorkItemID), protocol.ReopenWorkItemPayload{Reason: "new scope"}, rev(w.Revision)), ReopenWorkItem{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	h.clock.Advance(time.Hour)
	h.sweepFollowups()
	if len(h.occurrences(r.RuleID)) != 1 {
		t.Fatal("reopening restarted old rule")
	}
}
func TestFollowupSnoozeExpiryAndManualSchedule(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	r := h.followup(w, protocol.FollowupSpec{Kind: "every", EverySeconds: 300, ExpiresAt: h.clock.Now().Add(time.Hour).UnixMilli()})
	if op := h.controlFollowup(r, "trigger", nil); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	next := h.readFollowup(r.RuleID)
	if next.NextAt != r.NextAt || next.TriggerCount != 1 {
		t.Fatal("manual check reset the schedule or failed to count its occurrence")
	}
	op := h.exec(owner, h.env("work_items.update", workTarget(w.WorkItemID), protocol.UpdateWorkItemPayload{WorkItemPlan: w.WorkItemPlan, Status: "snoozed"}, rev(w.Revision)), UpdateWorkItem{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	h.clock.Advance(10 * time.Minute)
	h.sweepFollowups()
	if len(h.occurrences(r.RuleID)) != 1 {
		t.Fatal("snooze triggered periodic rule")
	}
	w = h.work(w.WorkItemID)
	once := h.followup(w, protocol.FollowupSpec{Kind: "at", At: h.clock.Now().UnixMilli()})
	if len(h.occurrences(once.RuleID)) != 1 {
		t.Fatal("explicit one-time snooze check missing")
	}
}

func TestFollowupAtomicFactRollbackAndReplacementInvalidation(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	source := h.ownerEvidence().Reference
	choice := h.askChoice(w, source)
	r := h.followup(w, protocol.FollowupSpec{Kind: "condition", Condition: &protocol.FollowupCondition{Kind: "choice_answered", ChoiceID: choice.ChoiceID}})
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, e := tx.Exec(t.Context(), `CREATE TRIGGER fail_followup BEFORE INSERT ON followup_occurrences BEGIN SELECT RAISE(ABORT,'injected'); END;`)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	cmd := UpdateWorkItemChoice{Action: "respond"}
	env := h.env(cmd.Operation(), protocol.Target{Type: "work_item_choice", ID: choice.ChoiceID}, protocol.UpdateWorkItemChoicePayload{Answer: "continue"}, rev(choice.Version))
	if _, err := h.app.Execute(t.Context(), owner, env, cmd); err == nil {
		t.Fatal("injected failure did not roll back")
	}
	if len(h.occurrences(r.RuleID)) != 0 {
		t.Fatal("partial occurrence committed")
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		c, e := store.GetWorkItemChoice(t.Context(), tx.Q(), choice.ChoiceID)
		if e != nil {
			return e
		}
		if c.Status != "open" {
			t.Fatal("answer committed without trigger")
		}
		_, e = tx.Exec(t.Context(), `DROP TRIGGER fail_followup`)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	replace := UpdateWorkItemChoice{Action: "replace"}
	replacement := protocol.UpdateWorkItemChoicePayload{Replacement: &protocol.CreateWorkItemChoicePayload{Question: "changed facts", SourceID: source}}
	change := h.env(replace.Operation(), env.Target, replacement, rev(choice.Version))
	change.Reason = "facts changed"
	if op := h.exec(owner, change, replace); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if h.readFollowup(r.RuleID).Status != "paused" || len(h.occurrences(r.RuleID)) != 0 {
		t.Fatal("replaced target was followed automatically")
	}
}

func TestFollowupFrozenTaskSetDoesNotTreatCancellationAsCompletion(t *testing.T) {
	h := newHarness(t)
	_, task := h.createTask(owner, createTaskPayload{Title: "first", Spec: testSpec})
	plan := workPlan()
	plan.Stages[0].TaskIDs = []string{task.TaskID}
	w := h.createWork(plan)
	r := h.followup(w, protocol.FollowupSpec{Kind: "condition", Condition: &protocol.FollowupCondition{Kind: "tasks_completed", TaskIDs: []string{task.TaskID}}})
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, e := tx.Exec(t.Context(), `UPDATE tasks SET lifecycle='cancelled' WHERE task_id=?`, task.TaskID)
		if e != nil {
			return e
		}
		x := &Exec{Env: h.env("test.fact", protocol.Target{Type: "task", ID: task.TaskID}, nil, nil), Principal: owner, Now: h.clock.Now(), IDs: h.ids, FollowupsEnabled: true}
		_, e = followupEvents(t.Context(), x, tx, []Event{{ObjectType: "task", ObjectID: task.TaskID}})
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if len(h.occurrences(r.RuleID)) != 0 {
		t.Fatal("cancelled is not completed")
	}
	_, extra := h.createTask(owner, createTaskPayload{Title: "later", Spec: testSpec})
	op := h.exec(owner, h.env("work_items.link_task", workTarget(w.WorkItemID), protocol.LinkWorkItemTaskPayload{TaskID: extra.TaskID, StageIDs: []string{"investigate"}}, rev(w.Revision)), LinkWorkItemTask{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, e := tx.Exec(t.Context(), `UPDATE tasks SET lifecycle='completed' WHERE task_id=?`, task.TaskID)
		if e != nil {
			return e
		}
		x := &Exec{Env: h.env("test.fact", protocol.Target{Type: "task", ID: task.TaskID}, nil, nil), Principal: owner, Now: h.clock.Now(), IDs: h.ids, FollowupsEnabled: true}
		_, e = followupEvents(t.Context(), x, tx, []Event{{ObjectType: "task", ObjectID: task.TaskID}})
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if len(h.occurrences(r.RuleID)) != 1 {
		t.Fatal("new task unexpectedly changed frozen condition")
	}
}
func TestFollowupDeliveryObservationRequiresExactKnownState(t *testing.T) {
	h := newHarness(t)
	w := observerItem(t, h)
	observerHello(t, h, "linux-1", "observer")
	r := h.followup(w, protocol.FollowupSpec{Kind: "condition", Condition: &protocol.FollowupCondition{Kind: "delivery_state", DeliveryID: "delivery-1", DeliveryState: "merged"}})
	assignment := pollDelivery(t, h, "linux-1", "observer")[0]
	if !accepted(t, reportDelivery(t, h, "linux-1", "observer", assignment, "", "authentication_required")) {
		t.Fatal("report rejected")
	}
	if len(h.occurrences(r.RuleID)) != 0 {
		t.Fatal("unknown observation satisfied condition")
	}
	h.clock.Advance(time.Hour)
	assignments := pollDelivery(t, h, "linux-1", "observer")
	if len(assignments) != 1 {
		t.Fatal(assignments)
	}
	if !accepted(t, reportDelivery(t, h, "linux-1", "observer", assignments[0], "closed", "")) {
		t.Fatal("report rejected")
	}
	if len(h.occurrences(r.RuleID)) != 0 {
		t.Fatal("closed incorrectly matched merged")
	}
}

func TestFollowupTaskCompletionAndDecisionFactsCommitWithWake(t *testing.T) {
	h := newHarness(t)
	taskID, accept := h.toAwaitingAcceptance()
	plan := workPlan()
	plan.Stages[0].TaskIDs = []string{taskID}
	w := h.createWork(plan)
	r := h.followup(w, protocol.FollowupSpec{Kind: "condition", Condition: &protocol.FollowupCondition{Kind: "tasks_completed", TaskIDs: []string{taskID}}})
	if op := h.submitVerdict(accept, protocol.VerdictPassed); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if len(h.occurrences(r.RuleID)) != 1 {
		t.Fatal("accepted completion did not wake subscribed coordinator")
	}
	op := h.exec(owner, h.env("decisions.open", protocol.Target{Type: "decision"}, openDecisionPayload{TaskID: taskID, Question: "Continue?"}, nil), OpenDecision{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var result struct {
		DecisionID string `json:"decision_id"`
	}
	if err := json.Unmarshal(op.Result, &result); err != nil {
		t.Fatal(err)
	}
	decision := h.followup(w, protocol.FollowupSpec{Kind: "condition", Condition: &protocol.FollowupCondition{Kind: "decision_answered", DecisionID: result.DecisionID}})
	answered := h.exec(owner, h.env((AnswerDecision{}).Operation(), protocol.Target{Type: "decision", ID: result.DecisionID}, protocol.AnswerDecisionPayload{Version: 1, Text: "Continue existing goal"}, nil), AnswerDecision{})
	if answered.Status != protocol.OpSucceeded {
		t.Fatal(answered)
	}
	if len(h.occurrences(decision.RuleID)) != 1 {
		t.Fatal("decision answer did not wake subscribed coordinator")
	}
}
func TestFollowupConcurrentSweepsAndClockRollback(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	r := h.followup(w, protocol.FollowupSpec{Kind: "at", At: h.clock.Now().Add(time.Hour).UnixMilli()})
	h.clock.Advance(time.Hour)
	cmd := SweepFollowups{}
	envs := make([]*protocol.Envelope, 8)
	for i := range envs {
		envs[i] = h.env(cmd.Operation(), protocol.Target{Type: "followups"}, struct{}{}, nil)
	}
	failures := make(chan error, len(envs))
	var wg sync.WaitGroup
	for _, env := range envs {
		wg.Go(func() {
			_, err := h.app.Execute(t.Context(), protocol.Principal{Kind: protocol.PrincipalSystem}, env, cmd)
			failures <- err
		})
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(h.occurrences(r.RuleID)) != 1 {
		t.Fatal("concurrent sweeps duplicated wake")
	}
	h.clock.Advance(-2 * time.Hour)
	h.sweepFollowups()
	h.clock.Advance(3 * time.Hour)
	h.sweepFollowups()
	if len(h.occurrences(r.RuleID)) != 1 {
		t.Fatal("clock rollback duplicated once-only rule")
	}
}
