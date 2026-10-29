package app

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"github.com/GhostFlying/tend/internal/views"
)

func requireHostedForbidden(t *testing.T, h *harness, p protocol.Principal, turn store.MainTurn, seq int64, target protocol.Target, expected *int64, payload any, command Command, reason string) {
	t.Helper()
	env := h.env(command.Operation(), target, payload, expected)
	env.Reason = reason
	op, err := h.app.Execute(t.Context(), p, env, HostedCoordinationCommand{Command: command, ExecutionID: turn.RequestID, EffectSeq: seq})
	if protocol.CodeOf(err) == protocol.CodeForbidden || op.Error != nil && op.Error.Code == protocol.CodeForbidden {
		return
	}
	t.Fatal("delegated operation unexpectedly allowed", op, err)
}

func TestDeadlineExistingMeaningConstrainsDelegatedChanges(t *testing.T) {
	for _, kind := range []string{"external", "user_commitment"} {
		t.Run("manager-"+kind, func(t *testing.T) {
			h := hostedManagerHarness(t)
			issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
			p := deadlinePayloadForTest(h, issue, 30*time.Hour)
			p.Nature = kind
			d := putDeadlineForTest(t, h, "", nil, p)
			turn, principal := startHostedIssueReview(t, h, issue)
			p.Nature = "estimate"
			due := h.clock.Now().Add(60 * time.Hour).UnixMilli()
			p.DueAt = &due
			requireHostedForbidden(t, h, principal, turn, 1, protocol.Target{Type: "deadline", ID: d.DeadlineID}, rev(d.Revision), p, IssueDeadlineCommand{}, "")
		})
	}
	for _, cancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "main-downgrade", true: "main-cancel"}[cancel], func(t *testing.T) {
			h := hostedManagerHarness(t)
			issue := createCanonicalOwnerTestIssue(t, h)
			p := deadlinePayloadForTest(h, issue, 30*time.Hour)
			d := putDeadlineForTest(t, h, "", nil, p)
			_, delivery := h.say("Review current commitments")
			turn := startHostedReview(t, h, "main", delivery)
			p.Nature = "external"
			var payload any = p
			if cancel {
				payload = struct{}{}
			}
			requireHostedForbidden(t, h, mainAgent, turn, 1, protocol.Target{Type: "deadline", ID: d.DeadlineID}, rev(d.Revision), payload, IssueDeadlineCommand{Cancel: cancel}, "Change the current deadline")
		})
	}
}

func TestDeadlineRestartPastBothWindowsEmitsOnlyDue(t *testing.T) {
	h := hostedManagerHarness(t)
	h.app.followupsEnabled = true
	issue := createCanonicalOwnerTestIssue(t, h)
	d := putDeadlineForTest(t, h, "", nil, deadlinePayloadForTest(h, issue, 30*time.Hour))
	h.clock.Advance(31 * time.Hour)
	h.app = New(h.store, h.ids, h.clock.Now, WithHostedConfiguration(h.app.hostedConfiguration), WithFollowups(true))
	h.sweepFollowups()
	h.sweepFollowups()
	if len(h.occurrences(d.NearRuleID)) != 0 || len(h.occurrences(d.DueRuleID)) != 1 {
		t.Fatal("restart replayed obsolete near window", h.occurrences(d.NearRuleID), h.occurrences(d.DueRuleID))
	}
}

func TestPlannerAttentionUsesOriginalBusinessEndpoint(t *testing.T) {
	for _, status := range []string{"done", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			h := newHarness(t)
			issue := createCanonicalOwnerTestIssue(t, h)
			a := attentionForTest(t, h, protocol.CreateAttentionPayload{Kind: "action", IssueID: issue.IssueID, Title: "Owner reviews result", BlocksClose: true})
			p := protocol.PutPlannerEntryPayload{Entry: protocol.PlannerEntry{Kind: "user_action", IssueID: issue.IssueID, AttentionID: a.AttentionID, Title: a.Title, Responsible: owner.Subject, Status: status, Reason: "Try to finish only the display entry"}}
			op := h.exec(owner, h.env("planner.entries_put", protocol.Target{Type: "planner_entry"}, p, nil), PutPlannerEntry{})
			if op.Status != protocol.OpFailed || op.Error.Code != protocol.CodeInvalidTransition {
				t.Fatal("entry created a second answer state", op)
			}
			p.Entry.Status = "open"
			op = h.exec(owner, h.env("planner.entries_put", protocol.Target{Type: "planner_entry"}, p, nil), PutPlannerEntry{})
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			var entry protocol.PlannerEntry
			if err := json.Unmarshal(op.Result, &entry); err != nil {
				t.Fatal(err)
			}
			op = h.exec(owner, h.env("attentions.confirm", protocol.Target{Type: "attention", ID: a.AttentionID}, protocol.ConfirmAttentionPayload{Summary: "I completed the review"}, rev(a.Revision)), AttentionCommand{Action: "confirm"})
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				page, err := views.NextActions(t.Context(), tx.Q(), "user_action", issue.IssueID)
				if err == nil && (len(page.Items) != 1 || page.Items[0].EntryID != entry.EntryID || page.Items[0].Status != "done") {
					t.Fatal(page)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func withdrawOwnerSource(t *testing.T, h *harness, message string) {
	t.Helper()
	var c protocol.MessageControl
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		c, err = store.MessageControl(t.Context(), tx.Q(), message)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	op := h.exec(owner, h.env("conversation.withdraw_message", protocol.Target{Type: "message", ID: message}, protocol.WithdrawMessagePayload{}, rev(c.Revision)), WithdrawMessage{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}

func TestOwnerActionRelayRejectsWithdrawnMaterial(t *testing.T) {
	for _, planner := range []bool{false, true} {
		t.Run(map[bool]string{false: "attention", true: "next-action"}[planner], func(t *testing.T) {
			h := hostedManagerHarness(t)
			issue := createCanonicalOwnerTestIssue(t, h)
			source := queueMessage(t, h, "I completed this action")
			withdrawOwnerSource(t, h, source)
			_, delivery := h.say("Coordinate the owner's current actions")
			turn := startHostedReview(t, h, "main", delivery)
			if planner {
				p := protocol.PutPlannerEntryPayload{Entry: protocol.PlannerEntry{Kind: "user_action", IssueID: issue.IssueID, Title: "Record owner result", Responsible: owner.Subject, Status: "done", Reason: "Transcribe completion", SourceMessageID: source}}
				requireHostedForbidden(t, h, mainAgent, turn, 1, protocol.Target{Type: "planner_entry"}, nil, p, PutPlannerEntry{}, "")
			} else {
				a := attentionForTest(t, h, protocol.CreateAttentionPayload{Kind: "action", IssueID: issue.IssueID, Title: "Complete owner review"})
				requireHostedForbidden(t, h, mainAgent, turn, 1, protocol.Target{Type: "attention", ID: a.AttentionID}, rev(a.Revision), protocol.ConfirmAttentionPayload{Summary: "Completed", SourceMessageID: source}, AttentionCommand{Action: "confirm"}, "")
			}
		})
	}
}

func TestDeadlineUserAuthorityHasNormalRecoveryPaths(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "planner-change", true: "planner-cancel"}[cancel], func(t *testing.T) {
			h := hostedManagerHarness(t)
			issue := createCanonicalOwnerTestIssue(t, h)
			other := createCanonicalOwnerTestIssue(t, h)
			p := deadlinePayloadForTest(h, issue, 30*time.Hour)
			d := putDeadlineForTest(t, h, "", nil, p)
			source := queueMessage(t, h, "I withdraw the old commitment and choose the revised delivery")
			if op := h.associate(owner, source, other.IssueID); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			_, delivery := h.say("Record my revised plan")
			turn := startHostedReview(t, h, "main", delivery)
			p.Nature = "external"
			p.SourceMessageID = source
			p.ExpectedSourceRevision = rev(1)
			var payload any = p
			if cancel {
				payload = protocol.CancelIssueDeadlinePayload{IssueSourceReference: protocol.IssueSourceReference{SourceMessageID: source, ExpectedSourceRevision: rev(1)}, Reason: "Owner retracts the old commitment"}
			}
			op := hostedEffect(t, h, mainAgent, turn, 1, protocol.Target{Type: "deadline", ID: d.DeadlineID}, rev(d.Revision), payload, IssueDeadlineCommand{Cancel: cancel})
			if op.Status != protocol.OpSucceeded {
				t.Fatal("planner could not use a real cross-Issue owner source", op)
			}
		})
	}
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	d := putDeadlineForTest(t, h, "", nil, deadlinePayloadForTest(h, issue, 30*time.Hour))
	env := h.env("issues.deadlines_cancel", protocol.Target{Type: "deadline", ID: d.DeadlineID}, protocol.CancelIssueDeadlinePayload{}, rev(d.Revision))
	env.Reason = "I cancel my commitment directly"
	if op := h.exec(owner, env, IssueDeadlineCommand{Cancel: true}); op.Status != protocol.OpSucceeded {
		t.Fatal("owner needed a prior proof message", op)
	}
}

func TestOwnerActionRelayAllowsCurrentCrossIssueSource(t *testing.T) {
	for _, planner := range []bool{false, true} {
		t.Run(map[bool]string{false: "attention", true: "next-action"}[planner], func(t *testing.T) {
			h := hostedManagerHarness(t)
			issue := createCanonicalOwnerTestIssue(t, h)
			other := createCanonicalOwnerTestIssue(t, h)
			source := queueMessage(t, h, "I completed the requested action")
			if op := h.associate(owner, source, other.IssueID); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			_, delivery := h.say("Record my actual result")
			turn := startHostedReview(t, h, "main", delivery)
			var op protocol.Operation
			if planner {
				p := protocol.PutPlannerEntryPayload{Entry: protocol.PlannerEntry{Kind: "user_action", IssueID: issue.IssueID, Title: "Record result", Responsible: owner.Subject, Status: "done", Reason: "Owner confirmed", SourceMessageID: source}}
				op = hostedEffect(t, h, mainAgent, turn, 1, protocol.Target{Type: "planner_entry"}, nil, p, PutPlannerEntry{})
			} else {
				a := attentionForTest(t, h, protocol.CreateAttentionPayload{Kind: "action", IssueID: issue.IssueID, Title: "Owner reviews result"})
				op = hostedEffect(t, h, mainAgent, turn, 1, protocol.Target{Type: "attention", ID: a.AttentionID}, rev(a.Revision), protocol.ConfirmAttentionPayload{Summary: "Owner completed it", SourceMessageID: source}, AttentionCommand{Action: "confirm"})
			}
			if op.Status != protocol.OpSucceeded {
				t.Fatal("main incorrectly inherited Issue-local source restriction", op)
			}
		})
	}
}

func TestDeadlineOverdueCancelsOnlyNeverClaimedNear(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "queued", true: "claimed-unknown"}[unknown], func(t *testing.T) {
			h := newHarness(t)
			h.app.followupsEnabled = true
			issue := createCanonicalOwnerTestIssue(t, h)
			d := putDeadlineForTest(t, h, "", nil, deadlinePayloadForTest(h, issue, 30*time.Hour))
			h.clock.Advance(6 * time.Hour)
			h.sweepFollowups()
			before := h.occurrences(d.NearRuleID)[0]
			if unknown {
				delivery, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
				if err != nil || !ok {
					t.Fatal(delivery, ok, err)
				}
				if err := h.app.FinishDelivery(t.Context(), delivery, DeliveryResult{Outcome: Unknown, Reason: "Native acknowledgement is unknown"}); err != nil {
					t.Fatal(err)
				}
				before = h.occurrences(d.NearRuleID)[0]
			}
			h.clock.Advance(25 * time.Hour)
			h.sweepFollowups()
			after := h.occurrences(d.NearRuleID)
			if len(after) != 1 || after[0].OccurrenceID != before.OccurrenceID {
				t.Fatal("original near occurrence was replaced", before, after)
			}
			if unknown {
				if after[0].Status != before.Status || len(h.occurrences(d.DueRuleID)) != 0 {
					t.Fatal("claimed unknown was rewritten or blindly duplicated", before, after)
				}
			} else if after[0].Status != "cancelled" || len(h.occurrences(d.DueRuleID)) != 1 {
				t.Fatal("obsolete unclaimed notice remained live", after, h.occurrences(d.DueRuleID))
			}
		})
	}
}
