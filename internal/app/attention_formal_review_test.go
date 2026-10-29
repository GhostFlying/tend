package app

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestP5ReviewRelayRejectsUnconsumedOwnerPayloads(t *testing.T) {
	for _, state := range []string{"queued", "unknown"} {
		for _, action := range []string{"confirm", "respond", "planner", "deadline"} {
			t.Run(state+"/"+action, func(t *testing.T) {
				h := hostedManagerHarness(t)
				issue := createCanonicalOwnerTestIssue(t, h)
				_, delivery := h.say("Review the already admitted owner request")
				turn := startHostedReview(t, h, "main", delivery)
				source := queueMessage(t, h, "A later owner instruction is not admitted yet")
				if state == "unknown" {
					d, ok, err := h.app.ClaimMainSteer(t.Context(), turn.RequestID, turn.ThreadID, turn.TurnID)
					if err != nil || !ok {
						t.Fatal(ok, err)
					}
					record, found, err := h.app.MainSteer(t.Context(), d.RequestID)
					if err != nil || !found {
						t.Fatal(found, err)
					}
					record.Status = "unknown"
					if err := h.app.UpdateMainSteer(t.Context(), record); err != nil {
						t.Fatal(err)
					}
					if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Unknown, Reason: "Receipt not yet known"}); err != nil {
						t.Fatal(err)
					}
				}
				var admitted bool
				if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
					var err error
					admitted, err = store.OwnerInputAdmitted(t.Context(), tx.Q(), source)
					return err
				}); err != nil || admitted {
					t.Fatal("fixture must be unconsumed", admitted, err)
				}
				switch action {
				case "confirm":
					a := attentionForTest(t, h, protocol.CreateAttentionPayload{Kind: "action", IssueID: issue.IssueID, Title: "Review report"})
					requireHostedForbidden(t, h, mainAgent, turn, 1, protocol.Target{Type: "attention", ID: a.AttentionID}, rev(a.Revision), protocol.ConfirmAttentionPayload{Summary: "Owner finished", SourceMessageID: source}, AttentionCommand{Action: "confirm"}, "")
				case "respond":
					a := attentionForTest(t, h, protocol.CreateAttentionPayload{Kind: "decision", IssueID: issue.IssueID, Title: "Which report?", Options: []string{"report", "changes"}})
					requireHostedForbidden(t, h, mainAgent, turn, 1, protocol.Target{Type: "attention", ID: a.AttentionID}, rev(a.Revision), protocol.RespondAttentionPayload{Version: 1, Option: "report", SourceMessageID: source}, AttentionCommand{Action: "respond"}, "")
				case "planner":
					p := protocol.PutPlannerEntryPayload{Entry: protocol.PlannerEntry{Kind: "user_action", IssueID: issue.IssueID, Title: "Owner reviewed", Responsible: owner.Subject, Status: "done", Reason: "Owner confirmation", SourceMessageID: source}}
					requireHostedForbidden(t, h, mainAgent, turn, 1, protocol.Target{Type: "planner_entry"}, nil, p, PutPlannerEntry{}, "")
				case "deadline":
					p := deadlinePayloadForTest(h, issue, 30*time.Hour)
					p.SourceMessageID = source
					p.ExpectedSourceRevision = rev(1)
					requireHostedForbidden(t, h, mainAgent, turn, 1, protocol.Target{Type: "deadline"}, nil, p, IssueDeadlineCommand{}, "")
				}
			})
		}
	}
}

func TestP5ReviewDeadlineUnknownReconcilesWithoutReschedule(t *testing.T) {
	h := newHarness(t)
	h.app.followupsEnabled = true
	h.legacyMainBatches()
	issue := createCanonicalOwnerTestIssue(t, h)
	d := putDeadlineForTest(t, h, "", nil, deadlinePayloadForTest(h, issue, 2*time.Hour))
	delivery, turn := h.startBackground()
	if err := h.app.FinishDelivery(t.Context(), delivery, DeliveryResult{Outcome: Unknown, Reason: "Wait for original native receipt"}); err != nil {
		t.Fatal(err)
	}
	original := h.occurrences(d.NearRuleID)[0]
	h.clock.Advance(3 * time.Hour)
	h.sweepFollowups()
	if len(h.occurrences(d.DueRuleID)) != 0 || h.occurrences(d.NearRuleID)[0].Status != "unknown" {
		t.Fatal("unknown was replayed")
	}
	// A real later native terminal observation settles the original occurrence.
	turn.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	if h.occurrences(d.NearRuleID)[0].Status == "unknown" {
		t.Fatal("receipt fixture did not reconcile")
	}
	h.clock.Advance(time.Minute)
	h.sweepFollowups()
	h.sweepFollowups()
	near, due := h.occurrences(d.NearRuleID), h.occurrences(d.DueRuleID)
	if len(near) != 1 || near[0].OccurrenceID != original.OccurrenceID || len(due) != 1 {
		t.Fatal("reconciled original left due window stranded or duplicated", near, due, h.readFollowup(d.DueRuleID))
	}
}

func TestP5ReviewClaimDropsExpiredUnclaimedNear(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(map[bool]string{false: "only-near", true: "shared-input"}[shared], func(t *testing.T) {
			h := newHarness(t)
			h.app.followupsEnabled = true
			h.legacyMainBatches()
			issue := createCanonicalOwnerTestIssue(t, h)
			d := putDeadlineForTest(t, h, "", nil, deadlinePayloadForTest(h, issue, time.Hour))
			original := h.occurrences(d.NearRuleID)[0]
			var extra string
			if shared {
				if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
					extra = h.ids.New(protocol.PrefixMessage)
					now := h.clock.Now().UnixMilli()
					if _, err := tx.InsertMessage(t.Context(), store.MessageRow{MessageID: extra, Author: "system", Body: "Independent input must remain", CreatedAt: now}); err != nil {
						return err
					}
					event := h.ids.New(protocol.PrefixEvent)
					if _, err := tx.InsertEvent(t.Context(), store.EventRow{EventID: event, Source: "app", ObjectType: "message", ObjectID: extra, Kind: "conversation.message", BodyJSON: `{}`, CreatedAt: now}); err != nil {
						return err
					}
					return appendBackgroundSource(t.Context(), &Exec{Env: &protocol.Envelope{RequestID: h.ids.New(protocol.PrefixRequest)}, Now: h.clock.Now(), IDs: h.ids}, tx, "deadline:"+d.DeadlineID, "", extra, event, "test.notice", json.RawMessage(`{}`))
				}); err != nil {
					t.Fatal(err)
				}
			}
			h.clock.Advance(2 * time.Hour) // Deliberately do not sweep before dispatch claims.
			delivery, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
			if err != nil {
				t.Fatal(err)
			}
			after := h.occurrences(d.NearRuleID)
			if after[0].OccurrenceID != original.OccurrenceID || after[0].Status != "cancelled" {
				t.Fatal("claim froze obsolete near instead of cancelling unclaimed input", after)
			}
			if shared {
				if !ok {
					t.Fatal("other batch input was lost")
				}
				var p HandleMessage
				if err := json.Unmarshal(delivery.Payload, &p); err != nil {
					t.Fatal(err)
				}
				page := h.backgroundPage(p.MessageID, 0)
				found := false
				for _, s := range page.Sources {
					if s.MessageID == extra && s.CancelledAt == 0 {
						found = true
					}
				}
				if !found || page.Batch.Status != "frozen" {
					t.Fatal("remaining input was not claimable", page)
				}
			} else if ok {
				t.Fatal("empty obsolete batch was delivered", delivery)
			}
		})
	}
}

func TestP5ReviewRevisedDeadlineResumesAfterOriginalReceipt(t *testing.T) {
	h := newHarness(t)
	h.app.followupsEnabled = true
	h.legacyMainBatches()
	issue := createCanonicalOwnerTestIssue(t, h)
	d := putDeadlineForTest(t, h, "", nil, deadlinePayloadForTest(h, issue, 2*time.Hour))
	delivery, turn := h.startBackground()
	if err := h.app.FinishDelivery(t.Context(), delivery, DeliveryResult{Outcome: Unknown, Reason: "Original receipt lost"}); err != nil {
		t.Fatal(err)
	}
	original := h.occurrences(d.NearRuleID)[0]
	next := putDeadlineForTest(t, h, d.DeadlineID, rev(d.Revision), deadlinePayloadForTest(h, issue, 26*time.Hour))
	h.clock.Advance(3 * time.Hour)
	h.sweepFollowups()
	if len(h.occurrences(next.NearRuleID)) != 0 || h.occurrences(d.NearRuleID)[0].Status != "unknown" {
		t.Fatal("unknown original was replayed")
	}
	turn.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(time.Minute)
	h.sweepFollowups()
	h.sweepFollowups()
	if old, fresh := h.occurrences(d.NearRuleID), h.occurrences(next.NearRuleID); len(old) != 1 || old[0].OccurrenceID != original.OccurrenceID || len(fresh) != 1 {
		t.Fatal("revised window stranded or original duplicated", old, fresh)
	}
}

func TestP5ReviewRelayUsesFrozenRecoveryOwnerInput(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	source, delivery := h.say("I completed the report review; record that result")
	turn := startHostedReview(t, h, "main", delivery)
	turn.Status = store.TurnFailed
	turn.Failure = &protocol.AgentFailure{Code: "rate_limit_exceeded", HTTPStatus: 429}
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	recovery, found, err := h.app.AgentRecovery(t.Context(), turn.RequestID)
	if err != nil || !found || recovery.Status != "waiting" {
		t.Fatal(recovery, found, err)
	}
	config := hostedConfigurationFixture()
	config.Revision = 2
	config.Definitions.Agents[0].Revision = 2
	config.Definitions.Agents[0].Model = "replacement-model"
	_, prepared := acceptHostedFixture(t, h, config, 1)
	h.app = New(h.store, h.ids, h.clock.Now, WithHostedConfiguration(prepared))
	selected, named, err := h.app.HostedSelectionForAdmission(t.Context(), recovery.ActiveRequestID, "main")
	if err != nil || !named || selected.ConfigurationRevision != 1 {
		t.Fatal(selected, named, err)
	}
	h.clock.Advance(time.Minute)
	if yes, err := h.app.AdmitAgentRecovery(t.Context(), recovery.ActiveRequestID, turn.ThreadID, selected.ConfigurationRevision); err != nil || !yes {
		t.Fatal(yes, err)
	}
	if yes, err := h.app.ConfirmAgentRecoveryStart(t.Context(), recovery.ActiveRequestID, turn.ThreadID, selected.ConfigurationRevision); err != nil || !yes {
		t.Fatal(yes, err)
	}
	continuation := store.MainTurn{RequestID: recovery.ActiveRequestID, MessageID: source, ThreadID: turn.ThreadID, Status: store.TurnStarting, Hosted: &selected, Model: selected.AppliedModel, ModelProvider: selected.AppliedProvider, Effort: selected.AppliedEffort, SettingsRevision: selected.ConfigurationRevision}
	if err := h.app.PutMainTurn(t.Context(), continuation); err != nil {
		t.Fatal(err)
	}
	continuation.Status = store.TurnStarted
	continuation.TurnID = "native-recovery-turn"
	if err := h.app.PutMainTurn(t.Context(), continuation); err != nil {
		t.Fatal(err)
	}
	a := attentionForTest(t, h, protocol.CreateAttentionPayload{Kind: "action", IssueID: issue.IssueID, Title: "Original owner review"})
	if op := hostedEffect(t, h, mainAgent, continuation, 1, protocol.Target{Type: "attention", ID: a.AttentionID}, rev(a.Revision), protocol.ConfirmAttentionPayload{Summary: "Owner reviewed", SourceMessageID: source}, AttentionCommand{Action: "confirm"}); op.Status != protocol.OpSucceeded {
		t.Fatal("frozen recovery lost admitted owner authority", op)
	}
}
