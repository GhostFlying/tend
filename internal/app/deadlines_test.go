package app

import (
	"encoding/json"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"testing"
	"time"
)

func putDeadlineForTest(t *testing.T, h *harness, id string, revision *int64, p protocol.PutIssueDeadlinePayload) protocol.IssueDeadline {
	t.Helper()
	op := h.exec(owner, h.env("issues.deadlines_put", protocol.Target{Type: "deadline", ID: id}, p, revision), IssueDeadlineCommand{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var d protocol.IssueDeadline
	if err := json.Unmarshal(op.Result, &d); err != nil {
		t.Fatal(err)
	}
	return d
}
func deadlinePayloadForTest(h *harness, issue protocol.Issue, after time.Duration) protocol.PutIssueDeadlinePayload {
	due := h.clock.Now().Add(after).UnixMilli()
	return protocol.PutIssueDeadlinePayload{IssueID: issue.IssueID, Title: "Promised delivery", Nature: "user_commitment", Strength: "hard", DueAt: &due, Timezone: "UTC", Reason: "Owner sets the current commitment"}
}
func TestDeadlineDefaultWindowsRestartAndUnchangedSilence(t *testing.T) {
	h := hostedManagerHarness(t)
	h.app.followupsEnabled = true
	issue := createCanonicalOwnerTestIssue(t, h)
	p := deadlinePayloadForTest(h, issue, 30*time.Hour)
	d := putDeadlineForTest(t, h, "", nil, p)
	if len(h.occurrences(d.NearRuleID)) != 0 || len(h.occurrences(d.DueRuleID)) != 0 {
		t.Fatal("early notification")
	}
	h.clock.Advance(6 * time.Hour)
	h.sweepFollowups()
	if len(h.occurrences(d.NearRuleID)) != 1 {
		t.Fatal("near window lost")
	}
	h.sweepFollowups()
	if len(h.occurrences(d.NearRuleID)) != 1 {
		t.Fatal("unchanged sweep repeated notification")
	}
	beforeNear, beforeDue := d.NearRuleID, d.DueRuleID
	// Human responsibility changes do not rebuild a global planner's timer.
	assignHostedManager(t, h, issue)
	d2 := putDeadlineForTest(t, h, d.DeadlineID, rev(d.Revision), p)
	if d2.Revision != d.Revision || d2.NearRuleID != beforeNear || d2.DueRuleID != beforeDue {
		t.Fatal("assignment silently reset deadline", d2)
	}
	p.Title = "Updated wording, same promised time"
	d = putDeadlineForTest(t, h, d.DeadlineID, rev(d.Revision), p)
	if d.NearRuleID != beforeNear || d.DueRuleID != beforeDue {
		t.Fatal("wording restarted notification windows", d)
	}
	h.clock.Advance(25 * time.Hour)
	// Restart reuses the same schedule and keys; no polling agent is added.
	h.app = New(h.store, h.ids, h.clock.Now, WithHostedConfiguration(h.app.hostedConfiguration), WithFollowups(true))
	h.sweepFollowups()
	h.sweepFollowups()
	if len(h.occurrences(d.DueRuleID)) != 1 || len(h.occurrences(d.NearRuleID)) != 1 {
		t.Fatal("restart missed or duplicated windows")
	}
	h.clock.Advance(-time.Hour)
	h.sweepFollowups()
	if len(h.occurrences(d.DueRuleID)) != 1 {
		t.Fatal("clock rollback duplicated reminder")
	}
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var wrong int
		err := tx.Q().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM followup_occurrences f JOIN background_sources s ON s.occurrence_id=f.occurrence_id JOIN background_batches b ON b.batch_id=s.batch_id JOIN outbox o ON json_extract(o.payload_json,'$.message_id')=b.message_id WHERE f.rule_id IN (?,?) AND (b.work_item_id<>'' OR o.destination<>'main_thread')`, d.NearRuleID, d.DueRuleID).Scan(&wrong)
		if err == nil && wrong != 0 {
			t.Fatal("deadline routed to Issue manager")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
func TestDeadlineImmediateOverdueUnknownTimeAndPendingRevision(t *testing.T) {
	h := newHarness(t)
	h.app.followupsEnabled = true
	issue := createCanonicalOwnerTestIssue(t, h)
	d := putDeadlineForTest(t, h, "", nil, deadlinePayloadForTest(h, issue, 2*time.Hour))
	if len(h.occurrences(d.NearRuleID)) != 1 {
		t.Fatal("registration in window did not notify once")
	}
	p := deadlinePayloadForTest(h, issue, 50*time.Hour)
	next := putDeadlineForTest(t, h, d.DeadlineID, rev(d.Revision), p)
	if h.occurrences(d.NearRuleID)[0].Status != "cancelled" || len(h.occurrences(next.NearRuleID)) != 0 {
		t.Fatal("old pending reminder survived reschedule")
	}
	late := putDeadlineForTest(t, h, "", nil, deadlinePayloadForTest(h, issue, -time.Hour))
	if late.NearRuleID != "" || len(h.occurrences(late.DueRuleID)) != 1 {
		t.Fatal("overdue registered duplicate windows", late)
	}
	unknown := protocol.PutIssueDeadlinePayload{IssueID: issue.IssueID, Title: "Estimate not known", Nature: "estimate", Strength: "soft", Timezone: "Asia/Shanghai", Reason: "No reliable time or duration yet"}
	u := putDeadlineForTest(t, h, "", nil, unknown)
	if u.DueAt != nil || u.EstimateSeconds != nil || u.NearRuleID != "" || u.DueRuleID != "" {
		t.Fatal("invented time/estimate", u)
	}
}

func TestDeadlineSourceWithdrawalAndActualUnknownPreserveFacts(t *testing.T) {
	t.Run("withdrawn owner source", func(t *testing.T) {
		h := newHarness(t)
		h.app.followupsEnabled = true
		issue := createCanonicalOwnerTestIssue(t, h)
		source := queueMessage(t, h, "An external date, not an owner promise")
		p := deadlinePayloadForTest(h, issue, 2*time.Hour)
		p.Nature = "external"
		p.SourceMessageID = source
		p.ExpectedSourceRevision = rev(1)
		d := putDeadlineForTest(t, h, "", nil, p)
		q := readMessageQueue(t, h, "owner")
		if op := h.exec(owner, h.env("conversation.withdraw_message", protocol.Target{Type: "message", ID: source}, protocol.WithdrawMessagePayload{}, rev(q.Items[0].Revision)), WithdrawMessage{}); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
		if h.readFollowup(d.DueRuleID).Status != "paused" || h.occurrences(d.NearRuleID)[0].Status != "cancelled" {
			t.Fatal("withdrawn source kept live reminders")
		}
		h.clock.Advance(3 * time.Hour)
		h.sweepFollowups()
		if len(h.occurrences(d.DueRuleID)) != 0 {
			t.Fatal("withdrawn source became renewed authority")
		}
	})
	t.Run("native delivery unknown", func(t *testing.T) {
		h := newHarness(t)
		h.app.followupsEnabled = true
		issue := createCanonicalOwnerTestIssue(t, h)
		d := putDeadlineForTest(t, h, "", nil, deadlinePayloadForTest(h, issue, 2*time.Hour))
		delivery, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
		if err != nil || !ok {
			t.Fatal(delivery, ok, err)
		}
		if err := h.app.FinishDelivery(t.Context(), delivery, DeliveryResult{Outcome: Unknown, Reason: "Native receipt lost"}); err != nil {
			t.Fatal(err)
		}
		if h.occurrences(d.NearRuleID)[0].Status != "unknown" {
			t.Fatal("actual unknown delivery was hidden")
		}
		next := putDeadlineForTest(t, h, d.DeadlineID, rev(d.Revision), deadlinePayloadForTest(h, issue, 26*time.Hour))
		h.clock.Advance(3 * time.Hour)
		h.sweepFollowups()
		if h.occurrences(d.NearRuleID)[0].Status != "unknown" || len(h.occurrences(next.NearRuleID)) != 0 || h.readFollowup(next.NearRuleID).Status != "active" {
			t.Fatal("old unknown was cancelled or replayed across revision")
		}
	})
}

func TestDeadlinePausedIssueStillRemindsAndEndpointCancelsOwnPendingSources(t *testing.T) {
	h := newHarness(t)
	h.app.followupsEnabled = true
	first := createCanonicalOwnerTestIssue(t, h)
	second := createCanonicalOwnerTestIssue(t, h)
	a := putDeadlineForTest(t, h, "", nil, deadlinePayloadForTest(h, first, 30*time.Hour))
	b := putDeadlineForTest(t, h, "", nil, deadlinePayloadForTest(h, second, 30*time.Hour))
	first = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, first, "pause", canonicalOwnerControl{ExpectedAssignmentEpoch: first.AssignmentEpoch}))
	h.clock.Advance(6 * time.Hour)
	h.sweepFollowups()
	if len(h.occurrences(a.NearRuleID)) != 1 || len(h.occurrences(b.NearRuleID)) != 1 {
		t.Fatal("pausing dispatch suppressed deadline")
	}
	first = canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, first, "close", canonicalLifecycleClosure(first, protocol.WorkItemClosureCompleted)))
	h.sweepFollowups()
	if h.occurrences(a.NearRuleID)[0].Status != "cancelled" {
		t.Fatal("terminal Issue retained unclaimed near reminder", h.occurrences(a.NearRuleID))
	}
	if h.occurrences(b.NearRuleID)[0].Status == "cancelled" {
		t.Fatal("endpoint cancelled another Issue's reminder")
	}
}
