package app

import (
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// Reopen the actual temporary fixture database, not just the App wrapper.
func reopenArchiveCombinationHarness(t *testing.T, h *harness) {
	t.Helper()
	var sequence int
	var name, path string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `PRAGMA database_list`).Scan(&sequence, &name, &path)
	}); err != nil {
		t.Fatal(err)
	}
	if path == "" {
		t.Fatal("fixture database has no durable path")
	}
	if err := h.store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(t.Context(), path, h.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	h.store = db
	h.app = New(db, h.ids, h.clock.Now, WithHostedConfiguration(h.app.hostedConfiguration), WithFollowups(h.app.followupsEnabled))
}

func TestArchivePrepareRetainsOrdinaryFencedReceiptPolling(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	turn, _ := startHostedIssueReview(t, h, issue)
	canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.assign", protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.IssueAssignmentPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: owner.Subject}, Reason: "Fence this manager without discarding its pending receipt"}, rev(issue.Revision)), HostedIssueAssignment{}))
	if err := h.app.AwaitNativeReceipts(t.Context(), turn.ThreadID); err != nil {
		t.Fatal(err)
	}
	if err := h.app.SettleOpenTurns(t.Context(), turn.ThreadID); err != nil {
		t.Fatal(err)
	}
	inflight, err := h.app.InFlightDeliveries(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, delivery := range inflight {
		if delivery.RequestID == turn.RequestID {
			if err := h.app.FinishDelivery(t.Context(), delivery, DeliveryResult{Outcome: Unknown}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if h.count("agent_recoveries") != 0 {
		t.Fatal("fixture unexpectedly relies on a recovery record")
	}
	if _, err := h.app.ChangeArchivePreparation(t.Context(), protocol.ArchiveControlRequest{RequestID: h.ids.New(protocol.PrefixRequest)}, true); err != nil {
		t.Fatal(err)
	}
	reopenArchiveCombinationHarness(t, h)
	conversation := "issue:" + issue.IssueID
	ids, err := h.app.PendingAgentConversations(t.Context())
	if err != nil || !hasConversation(ids, conversation) {
		t.Fatal("prepare hid an ordinary receipt independently of dispatch", ids, err)
	}
	if needed, err := h.app.ArchiveNeedsReceiptRead(t.Context(), conversation); err != nil || !needed {
		t.Fatal("prepare cannot attach the retained receipt", needed, err)
	}
	if _, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+conversation); err != nil || ok {
		t.Fatal("prepare admitted a new native delivery", ok, err)
	}
	if pending, err := h.app.DeferAgentReceiptRead(t.Context(), conversation); err != nil || !pending {
		t.Fatal(pending, err)
	}
	reopenArchiveCombinationHarness(t, h)
	ids, err = h.app.PendingAgentConversations(t.Context())
	if err != nil || hasConversation(ids, conversation) {
		t.Fatal("prepare restart lost the 30-second receipt poll deadline", ids, err)
	}
	h.clock.Advance(31 * time.Second)
	ids, err = h.app.PendingAgentConversations(t.Context())
	if err != nil || !hasConversation(ids, conversation) {
		t.Fatal("ordinary receipt did not become due during prepare", ids, err)
	}
	before := h.count("hosted_executions")
	turn.Status, turn.NativeReconciled = store.TurnInterrupted, true
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal("prepare rejected the original native receipt fact", err)
	}
	if h.count("native_turn_receipts_pending") != 0 || h.count("hosted_executions") != before {
		t.Fatal("receipt reconciliation started a replacement execution")
	}
	if _, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+conversation); err != nil || ok {
		t.Fatal("receipt reconciliation reopened native dispatch", ok, err)
	}
}

func TestArchiveDeadlinePrepareRestartRetainsOriginalOccurrences(t *testing.T) {
	for _, state := range []string{"never-issued", "queued", "claimed-unknown"} {
		t.Run(state, func(t *testing.T) {
			h := newHarness(t)
			h.app.followupsEnabled = true
			issue := createCanonicalOwnerTestIssue(t, h)
			d := putDeadlineForTest(t, h, "", nil, deadlinePayloadForTest(h, issue, 30*time.Hour))
			var original protocol.FollowupOccurrence
			if state != "never-issued" {
				h.clock.Advance(6 * time.Hour)
				h.sweepFollowups()
				original = h.occurrences(d.NearRuleID)[0]
				if state == "claimed-unknown" {
					delivery, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
					if err != nil || !ok {
						t.Fatal(delivery, ok, err)
					}
					if err := h.app.FinishDelivery(t.Context(), delivery, DeliveryResult{Outcome: Unknown}); err != nil {
						t.Fatal(err)
					}
					original = h.occurrences(d.NearRuleID)[0]
				}
			}
			if _, err := h.app.ChangeArchivePreparation(t.Context(), protocol.ArchiveControlRequest{RequestID: h.ids.New(protocol.PrefixRequest)}, true); err != nil {
				t.Fatal(err)
			}
			reopenArchiveCombinationHarness(t, h)
			if state == "never-issued" {
				h.clock.Advance(31 * time.Hour)
			} else {
				h.clock.Advance(25 * time.Hour)
			}
			before := h.count("events")
			h.sweepFollowups()
			h.sweepFollowups()
			if h.count("events") != before || len(h.occurrences(d.DueRuleID)) != 0 {
				t.Fatal("prepare admitted a deadline occurrence")
			}
			near := h.occurrences(d.NearRuleID)
			if state == "never-issued" && len(near) != 0 || state != "never-issued" && (len(near) != 1 || near[0] != original) {
				t.Fatal("prepare rewrote an original reminder", original, near)
			}
			if _, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination); err != nil || ok {
				t.Fatal("prepare started a deadline delivery", ok, err)
			}
			if _, err := h.app.ChangeArchivePreparation(t.Context(), protocol.ArchiveControlRequest{RequestID: h.ids.New(protocol.PrefixRequest), ExpectedControlRevision: 1}, false); err != nil {
				t.Fatal(err)
			}
			h.sweepFollowups()
			h.sweepFollowups()
			near, due := h.occurrences(d.NearRuleID), h.occurrences(d.DueRuleID)
			switch state {
			case "never-issued":
				if len(near) != 0 || len(due) != 1 {
					t.Fatal("resumed sweep replayed an obsolete near window", near, due)
				}
			case "queued":
				if len(near) != 1 || near[0].OccurrenceID != original.OccurrenceID || near[0].Status != "cancelled" || len(due) != 1 {
					t.Fatal("resumed sweep lost queued history or due notice", near, due)
				}
			case "claimed-unknown":
				if len(near) != 1 || near[0] != original || len(due) != 0 {
					t.Fatal("resumed sweep rewrote unknown or duplicated its delivery", original, near, due)
				}
			}
			before = h.count("events")
			reopenArchiveCombinationHarness(t, h)
			h.sweepFollowups()
			if h.count("events") != before {
				t.Fatal("restart repeated an unchanged deadline notification")
			}
		})
	}
}
