package app

import (
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func setReportForTest(t *testing.T, h *harness, source string, linked, members []string) {
	t.Helper()
	a := h.associations(source)
	p := protocol.SetIssueMessageAssociationsPayload{Disposition: "linked", IssueIDs: linked, ContentRevision: a.ContentRevision, CompletionReportIssueIDs: &members}
	if op := h.exec(owner, h.env("conversation.set_issue_associations", protocol.Target{Type: "message", ID: source}, p, rev(a.Revision)), SetIssueMessageAssociations{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}

func closeReportMember(t *testing.T, h *harness, issue protocol.Issue, source string, kind protocol.IssueClosureKind, reason string) {
	t.Helper()
	p := canonicalLifecycleSourceEndpoint(t, h, issue, source)
	p.Closure = canonicalLifecycleClosure(issue, kind).Closure
	p.Closure.Reason = reason
	canonicalLifecycleIssue(t, canonicalLifecycleControl(t, h, issue, "close", p))
}

func TestCombinedIssueReportPublishesOnceFromCurrentClosures(t *testing.T) {
	h := newHarness(t)
	a, b, unrelated := createCanonicalOwnerTestIssue(t, h), createCanonicalOwnerTestIssue(t, h), createCanonicalOwnerTestIssue(t, h)
	source, _ := h.say("Tell me once when both requested changes are done.")
	linked := []string{a.IssueID, b.IssueID, unrelated.IssueID}
	members := []string{b.IssueID, a.IssueID}
	setReportForTest(t, h, source, linked, members)
	count := func() int {
		return h.countWhere(`SELECT COUNT(*) FROM messaging_messages m JOIN messaging_sources s USING(message_id) WHERE s.source_message_id=? AND m.direction='notification'`, source)
	}
	closeReportMember(t, h, a, source, protocol.WorkItemClosureCompleted, "First result landed.")
	if count() != 0 {
		t.Fatal("partial completion was published")
	}
	h.app = New(h.store, h.ids, h.clock.Now, WithFollowups(true))
	closeReportMember(t, h, b, source, protocol.WorkItemClosureCompleted, "Second result was verified.")
	report := h.associations(source).CompletionReport
	if report == nil || report.PublishedMessageID == "" || count() != 1 {
		t.Fatal("missing combined result", report, count())
	}
	msg, err := h.app.MessagingMessage(t.Context(), report.PublishedMessageID)
	if err != nil || !strings.Contains(msg.Body, "First result landed.") || !strings.Contains(msg.Body, "Second result was verified.") || len(msg.IssueIDs) != 2 || msg.ConversationID != "main" {
		t.Fatal(msg, err)
	}
	if n := h.countWhere(`SELECT COUNT(*) FROM messaging_deliveries WHERE message_id=? AND channel_id='web' AND state='accepted'`, msg.MessageID); n != 1 {
		t.Fatal("missing original channel receipt", n)
	}
	setReportForTest(t, h, source, linked, members)
	// Redelivery of the business event after restart consults the same receipt.
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		return completeMessageReports(t.Context(), &Exec{IDs: h.ids, Now: h.clock.Now(), Env: &protocol.Envelope{}}, tx, b.IssueID)
	}); err != nil {
		t.Fatal(err)
	}
	if count() != 1 {
		t.Fatal("report repeated", count())
	}
	p := protocol.MessagingInput{Body: "What changed?", ReplyTo: msg.MessageID}
	op := h.exec(owner, h.env("messaging.reply", protocol.Target{Type: "message", ID: msg.MessageID}, p, nil), MessagingCommand{Action: "reply"})
	if op.Status != protocol.OpAccepted {
		t.Fatal("cannot ask about the aggregate", op)
	}
	reply, err := h.app.MessagingMessage(t.Context(), messageID(t, op))
	if err != nil || reply.ConversationID != "main" || reply.ReplyTo != msg.MessageID {
		t.Fatal(reply, err)
	}
}

func TestCombinedIssueReportChangesAndNoncompletion(t *testing.T) {
	for _, mode := range []string{"replace", "cancel", "declined", "unlink"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t)
			a, b := createCanonicalOwnerTestIssue(t, h), createCanonicalOwnerTestIssue(t, h)
			source, _ := h.say("Summarize the completed Issues together.")
			ids := []string{a.IssueID, b.IssueID}
			setReportForTest(t, h, source, ids, ids)
			closeReportMember(t, h, a, source, protocol.WorkItemClosureCompleted, "Original A")
			switch mode {
			case "replace":
				setReportForTest(t, h, source, ids, []string{a.IssueID})
				if report := h.associations(source).CompletionReport; report == nil || report.PublishedMessageID == "" {
					t.Fatal("replacement did not assess already completed members")
				}
			case "cancel":
				setReportForTest(t, h, source, ids, []string{})
				closeReportMember(t, h, b, source, protocol.WorkItemClosureCompleted, "Original B")
			case "declined":
				closeReportMember(t, h, b, source, protocol.WorkItemClosureDeclined, "Member cannot deliver as promised")
			case "unlink":
				if op := h.associate(owner, source, a.IssueID); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				if op := h.associate(owner, source, ids...); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
				closeReportMember(t, h, b, source, protocol.WorkItemClosureCompleted, "Original B")
			}
			if mode != "replace" && h.countWhere(`SELECT COUNT(*) FROM issue_completion_reports WHERE source_message_id=? AND published_message_id IS NOT NULL`, source) != 0 {
				t.Fatal("stale or incomplete promise published")
			}
		})
	}
}

func TestGroupedIssueOwnerProgressAndRequestedExceptions(t *testing.T) {
	for _, basis := range []string{"", "requested_update", "commitment_changed", "reply"} {
		t.Run("communication_"+basis, func(t *testing.T) {
			h := hostedManagerHarness(t)
			i := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
			other := createCanonicalOwnerTestIssue(t, h)
			source, _ := h.say("Give one report after both are complete. Also tell me if the deadline slips or the requested review finishes.")
			setReportForTest(t, h, source, []string{i.IssueID, other.IssueID}, []string{i.IssueID, other.IssueID})
			turn, principal := startHostedIssueReview(t, h, i)
			q := protocol.UpdateIssueProgressPayload{ExpectedAssignmentEpoch: i.AssignmentEpoch, Summary: "Review in progress", NextStep: "Wait for CI"}
			if op := hostedEffect(t, h, principal, turn, 1, protocol.Target{Type: "issue", ID: i.IssueID}, rev(i.Revision), q, UpdateIssueProgress{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			before := h.countWhere(`SELECT COUNT(*) FROM messaging_messages WHERE direction<>'input'`)
			target := protocol.Target{Type: "logical_turn", ID: turn.RequestID}
			var op protocol.Operation
			if basis == "reply" {
				// The original human input is a real admitted source on this turn.
				op = hostedEffect(t, h, principal, turn, 2, target, nil, protocol.CoordinationReply{ReplyTo: turn.MessageID, Body: "Review is in progress."}, CoordinationReply{})
			} else {
				p := protocol.MessagingPublication{Body: "Review status", NotificationBasis: basis, Reason: "Report the specific milestone or impact requested by the owner"}
				op = hostedEffect(t, h, principal, turn, 2, target, nil, p, PublishCommunication{Notify: true})
			}
			after := h.countWhere(`SELECT COUNT(*) FROM messaging_messages WHERE direction<>'input'`)
			if basis == "" {
				if op.Error == nil || op.Error.Code != protocol.CodeInvalidTransition || after != before {
					t.Fatal("individual progress leaked", op, after, before)
				}
			} else if op.Status != protocol.OpSucceeded || after != before+1 {
				t.Fatal("requested communication was lost", basis, op, after, before)
			}
		})
	}
}

func TestPRIntegrationPublishesOriginalCardAndObservedResult(t *testing.T) {
	for _, mode := range []string{"auto", "manual"} {
		t.Run(mode, func(t *testing.T) {
			h, _, i, source, c, acceptance := sharedReviewFixture(t)
			outgoing := func() int { return h.countWhere(`SELECT COUNT(*) FROM messaging_messages WHERE direction<>'input'`) }
			initial := outgoing()
			importSharedReviewForTest(t, h, i, source, acceptance)
			i = setPRPolicyForTest(t, h, i.IssueID, mode)
			progress := protocol.UpdateIssueProgressPayload{ExpectedAssignmentEpoch: i.AssignmentEpoch, Summary: "Candidate reviewed; CI passed", NextStep: "Integrate the exact reviewed candidate"}
			if op := h.exec(owner, h.env("issues.update_progress", protocol.Target{Type: "issue", ID: i.IssueID}, progress, rev(i.Revision)), UpdateIssueProgress{}); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if outgoing() != initial {
				t.Fatal("review or CI was published")
			}
			duty := prepareDeliveryForTest(t, h, i.IssueID, source, c)
			want := initial
			if mode == "manual" {
				want++
				decision := deliveryDecisionForTest(t, h, duty)
				card := imQuestion(t, h, decision.DecisionID)
				if outgoing() != want {
					t.Fatal("expected one decision card", outgoing(), want)
				}
				op := h.exec(owner, h.env("messaging.respond", protocol.Target{Type: "interaction", ID: card.Interaction.ID}, protocol.RespondInteraction{Version: card.Interaction.Version, Option: "approve"}, nil), RespondMessagingInteraction{})
				if op.Error != nil {
					t.Fatal(op)
				}
				duty = deliveryDutyForTest(t, h, i.IssueID, source)
			}
			if op := createDeliveryExecutionForTest(t, h, i.IssueID, duty); op.Status != protocol.OpSucceeded {
				t.Fatal("candidate could not execute under the actual grant", op)
			}
			if outgoing() != want {
				t.Fatal("unexpected extra update", outgoing(), want)
			}
			h.app.serverDeliveryObservations = true
			recordSettlementSnapshot(t, h, settlementSnapshot(c))
			var observed store.DeliveryObservationRow
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				var err error
				observed, err = store.GetDeliveryObservation(t.Context(), tx.Q(), i.IssueID, c.DeliveryID)
				return err
			}); err != nil || observed.Observation.State != "merged" || observed.Observation.HeadSHA != c.HeadSHA || !observed.Observation.TargetContainsMerge {
				t.Fatal(observed, err)
			}
			// Stand in for the Agent interpreting the agreed delivery point. The
			// provider observation, formal publication and channel receipts are real
			// app paths. No GitHub merge or model call is made by this fixture.
			_, turn := startReviewTurn(t, h)
			p := protocol.MessagingPublication{Body: "The reviewed PR has merged at " + observed.Observation.MergeSHA, IssueIDs: []string{i.IssueID}}
			env := h.env("conversation.notify", protocol.Target{Type: "logical_turn", ID: turn.RequestID}, p, nil)
			env.ParentRequestID = turn.RequestID
			op := h.exec(mainAgent, env, PublishCommunication{Notify: true})
			if op.Status != protocol.OpSucceeded || outgoing() != want+1 {
				t.Fatal("missing promised result", op, outgoing(), want)
			}
			if n := h.countWhere(`SELECT COUNT(*) FROM messaging_deliveries WHERE message_id=? AND channel_id='web' AND state='accepted'`, messageID(t, op)); n != 1 {
				t.Fatal("missing final channel receipt", n)
			}
		})
	}
}
