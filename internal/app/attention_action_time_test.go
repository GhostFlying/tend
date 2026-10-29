package app

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func changeAttentionTimeForTest(t *testing.T, h *harness, a protocol.Attention, at *int64, zone string) protocol.Attention {
	t.Helper()
	p := protocol.UpdateAttentionPayload{Title: a.Title, BlocksClose: a.BlocksClose, LatestActionAt: at, LatestActionTimezone: zone}
	op := h.exec(owner, h.env("attentions.update", protocol.Target{Type: "attention", ID: a.AttentionID}, p, rev(a.Revision)), AttentionCommand{Action: "update"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var next protocol.Attention
	if err := json.Unmarshal(op.Result, &next); err != nil {
		t.Fatal(err)
	}
	return next
}

func TestAttentionActionTimeNoIssueMainReminderAndPresentation(t *testing.T) {
	h := hostedManagerHarness(t)
	h.app.followupsEnabled = true
	due := h.clock.Now().Add(30 * time.Hour).UnixMilli()
	a := attentionForTest(t, h, protocol.CreateAttentionPayload{Kind: "action", Title: "Send the signed form", LatestActionAt: &due})
	if a.IssueID != "" || a.LatestActionNearRuleID == "" || a.LatestActionDueRuleID == "" || !a.LatestActionReminders {
		t.Fatal(a)
	}
	nearID, dueID := a.LatestActionNearRuleID, a.LatestActionDueRuleID
	op := h.exec(owner, h.env("attentions.present", protocol.Target{Type: "attention", ID: a.AttentionID}, map[string]bool{"read": true, "archived": true}, rev(a.Revision)), AttentionCommand{Action: "present"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	a = readAttentionForTest(t, h, a.AttentionID)
	a = changeAttentionTimeForTest(t, h, a, nil, "Asia/Shanghai")
	if a.Status != "open" || a.ReadAt == 0 || a.ArchivedAt == 0 || a.LatestActionNearRuleID != nearID || a.LatestActionDueRuleID != dueID || *a.LatestActionAt != due {
		t.Fatal("presentation rebuilt the business clock", a)
	}
	h.clock.Advance(6 * time.Hour)
	h.sweepFollowups()
	if len(h.occurrences(nearID)) != 1 || len(h.occurrences(dueID)) != 0 {
		t.Fatal("near window lost")
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var routed int
		err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM followup_occurrences f JOIN background_sources s ON s.occurrence_id=f.occurrence_id JOIN background_batches b ON b.batch_id=s.batch_id JOIN outbox o ON json_extract(o.payload_json,'$.message_id')=b.message_id WHERE f.rule_id=? AND b.work_item_id='' AND o.destination='main_thread'`, nearID).Scan(&routed)
		if err == nil && routed != 1 {
			t.Fatal("no-Issue reminder not delivered through original main batch", routed)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	reopenArchiveCombinationHarness(t, h)
	h.clock.Advance(25 * time.Hour)
	h.sweepFollowups()
	h.sweepFollowups()
	if len(h.occurrences(dueID)) != 1 || h.occurrences(nearID)[0].Status != "cancelled" {
		t.Fatal("restart duplicated due or retained unsent near", h.occurrences(nearID), h.occurrences(dueID))
	}
	env := h.env("attentions.confirm", protocol.Target{Type: "attention", ID: a.AttentionID}, protocol.ConfirmAttentionPayload{Summary: "Form sent"}, rev(a.Revision))
	if op = h.exec(owner, env, AttentionCommand{Action: "confirm"}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if replay := h.exec(owner, env, AttentionCommand{Action: "confirm"}); replay.Status != protocol.OpSucceeded || string(replay.Result) != string(op.Result) {
		t.Fatal("confirmation replay changed", replay)
	}
	if h.occurrences(dueID)[0].Status != "cancelled" || readAttentionForTest(t, h, a.AttentionID).Status != "resolved" {
		t.Fatal("confirmation lost original business meaning")
	}
}

func TestAttentionActionTimeOfflinePrepareResumesOnlyDue(t *testing.T) {
	h := newHarness(t)
	h.app.followupsEnabled = true
	due := h.clock.Now().Add(30 * time.Hour).UnixMilli()
	a := attentionForTest(t, h, protocol.CreateAttentionPayload{Kind: "information", Title: "Publication window", LatestActionAt: &due})
	p, err := h.app.ChangeArchivePreparation(t.Context(), protocol.ArchiveControlRequest{RequestID: h.ids.New(protocol.PrefixRequest)}, true)
	if err != nil {
		t.Fatal(err)
	}
	reopenArchiveCombinationHarness(t, h)
	h.clock.Advance(31 * time.Hour)
	events := h.count("events")
	h.sweepFollowups()
	if h.count("events") != events || len(h.occurrences(a.LatestActionDueRuleID)) != 0 || len(h.occurrences(a.LatestActionNearRuleID)) != 0 {
		t.Fatal("prepare emitted notification")
	}
	if _, err = h.app.ChangeArchivePreparation(t.Context(), protocol.ArchiveControlRequest{RequestID: h.ids.New(protocol.PrefixRequest), ExpectedControlRevision: p.Revision}, false); err != nil {
		t.Fatal(err)
	}
	h.sweepFollowups()
	reopenArchiveCombinationHarness(t, h)
	h.sweepFollowups()
	if len(h.occurrences(a.LatestActionNearRuleID)) != 0 || len(h.occurrences(a.LatestActionDueRuleID)) != 1 || h.readFollowup(a.LatestActionNearRuleID).Status != "finished" {
		t.Fatal("cross-window restart replayed near or duplicated due")
	}
	if readAttentionForTest(t, h, a.AttentionID).Status != "open" {
		t.Fatal("notification answered information")
	}
}

func TestAttentionActionTimeUnknownKeepsOriginalReceiptAndDefers(t *testing.T) {
	h := hostedManagerHarness(t)
	h.app.followupsEnabled = true
	due := h.clock.Now().Add(2 * time.Hour).UnixMilli()
	a := attentionForTest(t, h, protocol.CreateAttentionPayload{Kind: "action", Title: "Perform the owner action", LatestActionAt: &due})
	original := h.occurrences(a.LatestActionNearRuleID)[0]
	delivery, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
	if err != nil || !ok {
		t.Fatal(delivery, ok, err)
	}
	turn := startHostedReview(t, h, "main", delivery)
	if err = h.app.AwaitNativeReceipts(t.Context(), turn.ThreadID); err != nil {
		t.Fatal(err)
	}
	if err = h.app.SettleOpenTurns(t.Context(), turn.ThreadID); err != nil {
		t.Fatal(err)
	}
	if err = h.app.FinishDelivery(t.Context(), delivery, DeliveryResult{Outcome: Unknown}); err != nil {
		t.Fatal(err)
	}
	if got := h.occurrences(a.LatestActionNearRuleID)[0]; got.Status != "unknown" || got.OccurrenceID != original.OccurrenceID {
		t.Fatal(got)
	}
	nextAt := h.clock.Now().Add(26 * time.Hour).UnixMilli()
	next := changeAttentionTimeForTest(t, h, a, &nextAt, "")
	h.clock.Advance(3 * time.Hour)
	h.sweepFollowups()
	if len(h.occurrences(next.LatestActionNearRuleID)) != 0 || h.occurrences(a.LatestActionNearRuleID)[0].Status != "unknown" {
		t.Fatal("unknown replaced or cancelled")
	}
	var deferred int64
	if err = h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT next_due FROM followup_rules WHERE rule_id=?`, next.LatestActionNearRuleID).Scan(&deferred)
	}); err != nil {
		t.Fatal(err)
	}
	if deferred != h.clock.Now().Add(30*time.Second).UnixMilli() {
		t.Fatal("unknown did not use original persistent poll deadline", deferred)
	}
	reopenArchiveCombinationHarness(t, h)
	before := h.count("events")
	h.sweepFollowups()
	if h.count("events") != before {
		t.Fatal("restart forgot unknown backoff")
	}
	turn.Status, turn.NativeReconciled = store.TurnInterrupted, true
	if err = h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(31 * time.Second)
	h.sweepFollowups()
	h.sweepFollowups()
	if len(h.occurrences(next.LatestActionNearRuleID)) != 1 || h.occurrences(a.LatestActionNearRuleID)[0].OccurrenceID != original.OccurrenceID || h.occurrences(a.LatestActionNearRuleID)[0].Status == "unknown" {
		t.Fatal("original reconciliation did not release current reminder", h.occurrences(next.LatestActionNearRuleID), h.occurrences(a.LatestActionNearRuleID))
	}
}

func TestAttentionActionTimeAssignmentClearAndManagedRule(t *testing.T) {
	h := hostedManagerHarness(t)
	h.app.followupsEnabled = true
	issue := createCanonicalOwnerTestIssue(t, h)
	due := h.clock.Now().Add(30 * time.Hour).UnixMilli()
	a := attentionForTest(t, h, protocol.CreateAttentionPayload{IssueID: issue.IssueID, Kind: "action", Title: "Owner approval", LatestActionAt: &due})
	assignHostedManager(t, h, issue)
	current := readAttentionForTest(t, h, a.AttentionID)
	if current.LatestActionNearRuleID != a.LatestActionNearRuleID || current.LatestActionDueRuleID != a.LatestActionDueRuleID {
		t.Fatal("assignment reset clock")
	}
	if op := h.controlFollowup(h.readFollowup(a.LatestActionNearRuleID), "cancel", nil); op.Status != protocol.OpFailed || op.Error == nil || op.Error.Code != protocol.CodeInvalidTransition {
		t.Fatal("generic command controls Attention clock", op)
	}
	zero := int64(0)
	next := changeAttentionTimeForTest(t, h, a, &zero, "")
	if next.LatestActionAt != nil || next.LatestActionNearRuleID != "" || next.LatestActionDueRuleID != "" {
		t.Fatal("zero did not clear", next)
	}
	h.clock.Advance(31 * time.Hour)
	h.sweepFollowups()
	if len(h.occurrences(a.LatestActionNearRuleID)) != 0 || len(h.occurrences(a.LatestActionDueRuleID)) != 0 {
		t.Fatal("cleared clock emitted reminders")
	}
}

func TestAttentionActionTimeHostedPlannerAndOriginalDecisionAnswer(t *testing.T) {
	h := hostedManagerHarness(t)
	h.app.followupsEnabled = true
	_, delivery := h.say("Remind me to choose the report scope tomorrow")
	turn := startHostedReview(t, h, "main", delivery)
	due := h.clock.Now().Add(30 * time.Hour).UnixMilli()
	p := protocol.CreateAttentionPayload{Kind: "decision", Title: "Choose report scope", Options: []string{"summary", "full"}, LatestActionAt: &due}
	op := hostedEffect(t, h, mainAgent, turn, 1, protocol.Target{Type: "attention"}, nil, p, AttentionCommand{Action: "create"})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var a protocol.Attention
	if err := json.Unmarshal(op.Result, &a); err != nil {
		t.Fatal(err)
	}
	if a.LatestActionAt == nil || *a.LatestActionAt != due {
		t.Fatal("hosted input lost actual time", a)
	}
	// Referencing an existing question must not silently discard a clock update.
	changed := due + 1000
	op = h.exec(owner, h.env("attentions.create", protocol.Target{Type: "attention"}, protocol.CreateAttentionPayload{Kind: "decision", Title: a.Title, DecisionID: a.DecisionID, LatestActionAt: &changed}, nil), AttentionCommand{Action: "create"})
	if op.Status != protocol.OpFailed || op.Error == nil || op.Error.Code != protocol.CodeInvalidRequest {
		t.Fatal("existing identity silently ignored a different time", op)
	}
	// Answering the original decision (not the Attention facade) cancels its clock.
	op = h.exec(owner, h.env("decisions.respond", protocol.Target{Type: "decision", ID: a.DecisionID}, protocol.AnswerDecisionPayload{Version: a.QuestionVersion, Option: "summary"}, nil), AnswerDecision{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	current := readAttentionForTest(t, h, a.AttentionID)
	if current.Status != "resolved" || current.LatestActionNearRuleID != "" || h.readFollowup(a.LatestActionDueRuleID).Status != "cancelled" {
		t.Fatal("original answer did not settle the same Attention clock", current)
	}
	h.clock.Advance(31 * time.Hour)
	h.sweepFollowups()
	if len(h.occurrences(a.LatestActionDueRuleID)) != 0 {
		t.Fatal("answered original decision still prompted")
	}
}
