package app

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestHostedContextReviewPersistsBoundedSourceBatches(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	turn, p := startHostedIssueReview(t, h, issue)
	// These are reviewable historical materials, not still-queued owner input.
	// Witness their normal native consumption through the existing steer path.
	_, initial := h.say("Receive the bounded batch of source materials")
	main := startHostedReview(t, h, "main", initial)
	var admitted []string
	for i := 0; i < 100; i++ {
		source := queueMessage(t, h, fmt.Sprintf("Additional owner material %d", i))
		delivery, ok, err := h.app.ClaimMainSteer(t.Context(), main.RequestID, main.ThreadID, main.TurnID)
		if err != nil || !ok {
			t.Fatal("fixture steer step", i, delivery, ok, err)
		}
		steer, ok, err := h.app.MainSteer(t.Context(), delivery.RequestID)
		if err != nil || !ok {
			t.Fatal(ok, err)
		}
		steer.InputText = fmt.Sprintf("Exact consumed source %d", i)
		if err := h.app.UpdateMainSteer(t.Context(), steer); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := h.app.ConsumeMainSteer(t.Context(), main.ThreadID, main.TurnID, steer.InputText, fmt.Sprintf("source-item-%d", i)); err != nil || !ok {
			t.Fatal(ok, err)
		}
		if err := h.app.FinishDelivery(t.Context(), delivery, DeliveryResult{Outcome: Delivered}); err != nil {
			t.Fatal(err)
		}
		admitted = append(admitted, source)
	}
	// Association notices are queue barriers, so classify the batch after its
	// exact native consumption receipts, rather than interrupting the batch.
	for _, source := range admitted {
		if op := h.associate(owner, source, issue.IssueID); op.Status != protocol.OpSucceeded {
			t.Fatal(op)
		}
	}
	review := hostedReviewPayload(t, h, issue)
	if len(review.Sources) != 101 {
		t.Fatal(len(review.Sources))
	}
	review.Sources = review.Sources[:100]
	op := hostedEffect(t, h, p, turn, 1, protocol.Target{Type: "issue", ID: issue.IssueID}, nil, review, ReviewHostedIssueContext{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal("bounded review rolled back", op)
	}
	if remaining := hostedReviewPayload(t, h, issue); len(remaining.Sources) != 1 {
		t.Fatal("review did not retain exact remaining share", len(remaining.Sources))
	}
	expectWorkCode(t, commitHostedReview(t, h, p, turn, 2), protocol.CodeStaleRevision)
	if op := hostedEffect(t, h, p, turn, 3, protocol.Target{Type: "issue", ID: issue.IssueID}, nil, hostedReviewPayload(t, h, issue), ReviewHostedIssueContext{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := commitHostedReview(t, h, p, turn, 4); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	finishHostedReview(t, h, turn)
	state, err := h.app.LogicalTurn(t.Context(), turn.RequestID)
	if err != nil || state.Status != "completed" {
		t.Fatal(state, err)
	}
}

func TestHostedBackgroundRepairDefiniteFailureRemainsBounded(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	turn, _ := startHostedIssueReview(t, h, issue)
	finishHostedReview(t, h, turn)
	for attempt := 0; attempt < 5; attempt++ {
		h.clock.Advance(11 * time.Minute)
		d, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:issue:"+issue.IssueID)
		for ok && err == nil && d.Operation != "conversation.repair_turn" {
			if err = h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Delivered}); err != nil {
				t.Fatal(err)
			}
			d, _, ok, err = h.app.NextDelivery(t.Context(), "background_thread:issue:"+issue.IssueID)
		}
		if err != nil || !ok || d.Operation != "conversation.repair_turn" {
			t.Fatal("repair stranded", attempt, d, ok, err)
		}
		selection, named, err := h.app.HostedSelectionForAdmission(t.Context(), d.RequestID, "issue:"+issue.IssueID)
		if err != nil || !named {
			t.Fatal(selection, named, err)
		}
		repair := store.MainTurn{RequestID: d.RequestID, MessageID: turn.MessageID, ThreadID: turn.ThreadID, Status: store.TurnStarting, Hosted: &selection, Model: selection.Definition.Model, Effort: selection.Definition.Effort}
		if err := h.app.PutMainTurn(t.Context(), repair); err != nil {
			t.Fatal(err)
		}
		repair.Status = store.TurnRejected
		if err := h.app.PutMainTurn(t.Context(), repair); err != nil {
			t.Fatal(err)
		}
		if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Failed}); err != nil {
			t.Fatal(err)
		}
	}
	state, err := h.app.LogicalTurn(t.Context(), turn.RequestID)
	if err != nil || state.Status != "needs_review" || state.RepairAttempts != 5 {
		t.Fatal(state, err)
	}
}

func TestHostedKnownUnsentRetryRestoresFrozenRuntime(t *testing.T) {
	h := hostedManagerHarness(t)
	_, d := h.say("Investigate using the selected hosted definition")
	turn := startHostedReview(t, h, "main", d)
	frozen, _, err := h.app.HostedExecution(t.Context(), turn.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	original := frozen.Selection
	turn.Status = store.TurnRejected
	turn.TurnID = ""
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Retry, RetryAfter: time.Second}); err != nil {
		t.Fatal(err)
	}
	config := hostedConfigurationFixture()
	config.Revision = 2
	config.Definitions.Agents[0].Revision = 2
	config.Definitions.Agents[0].Model = "replacement-model"
	config.Definitions.Runtimes[0].Revision = 2
	config.Definitions.Runtimes[0].ExecutableRef = "replacement-executable"
	op, prepared := acceptHostedFixture(t, h, config, 1)
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	restarted := New(h.store, h.ids, h.clock.Now, WithHostedConfiguration(prepared))
	selected, named, err := restarted.HostedRuntimeSelection(t.Context(), "main")
	if err != nil || !named || !reflect.DeepEqual(selected, original) {
		t.Fatal("known-unsent retry reselected runtime", selected, original, err)
	}
	h.clock.Advance(time.Second)
	retry, _, ok, err := restarted.NextDelivery(t.Context(), MainThreadDestination)
	if err != nil || !ok || retry.RequestID != turn.RequestID {
		t.Fatal(retry, ok, err)
	}
	admitted, named, err := restarted.HostedSelectionForAdmission(t.Context(), retry.RequestID, "main")
	if err != nil || !named || !reflect.DeepEqual(admitted, selected) {
		t.Fatal(admitted, named, err)
	}
	turn.Status = store.TurnStarting
	turn.Hosted = &admitted
	if err := restarted.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal("old named executor cannot retry the known-unsent request", err)
	}
}

func TestHostedFencedOrdinaryReceiptReadSurvivesRestart(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	turn, _ := startHostedIssueReview(t, h, issue)
	next := canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.assign", protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.IssueAssignmentPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: owner.Subject}, Reason: "Owner takes remaining responsibility"}, rev(issue.Revision)), HostedIssueAssignment{}))
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
	for _, d := range inflight {
		if d.RequestID == turn.RequestID {
			if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Unknown}); err != nil {
				t.Fatal(err)
			}
		}
	}
	conversation := "issue:" + issue.IssueID
	restarted := New(h.store, h.ids, h.clock.Now, WithHostedConfiguration(h.app.hostedConfiguration))
	ids, err := restarted.PendingAgentConversations(t.Context())
	if err != nil || !hasConversation(ids, conversation) {
		t.Fatal("ordinary fenced receipt lost", ids, err)
	}
	if pending, err := restarted.DeferAgentReceiptRead(t.Context(), conversation); err != nil || !pending {
		t.Fatal(pending, err)
	}
	restarted = New(h.store, h.ids, h.clock.Now, WithHostedConfiguration(h.app.hostedConfiguration))
	ids, err = restarted.PendingAgentConversations(t.Context())
	if err != nil || hasConversation(ids, conversation) {
		t.Fatal("receipt backoff lost", ids, err)
	}
	h.clock.Advance(31 * time.Second)
	ids, err = restarted.PendingAgentConversations(t.Context())
	if err != nil || !hasConversation(ids, conversation) {
		t.Fatal("receipt not due after restart", ids, err)
	}
	selected, named, err := restarted.HostedRuntimeSelection(t.Context(), conversation)
	if err != nil || !named || selected.ConfigurationHash != turn.Hosted.ConfigurationHash || selected.Runtime.ExecutableRef != turn.Hosted.Runtime.ExecutableRef {
		t.Fatal(selected, named, err)
	}
	finish := protocol.FinishIssueAssignmentPayload{ExpectedAssignmentEpoch: next.AssignmentEpoch, HandoffID: next.PendingHandoffID, Reason: "Original native receipt reconciled"}
	expectWorkCode(t, h.exec(owner, h.env("issues.finish_assignment", protocol.Target{Type: "issue", ID: next.IssueID}, finish, rev(next.Revision)), HostedIssueAssignment{Finish: true}), protocol.CodeInvalidTransition)
	if _, _, ok, err := restarted.NextDelivery(t.Context(), "background_thread:"+conversation); err != nil || ok {
		t.Fatal("receipt-only discovery admitted new business turn", ok, err)
	}
	turn.Status = store.TurnInterrupted
	turn.NativeReconciled = true
	if err := restarted.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	op := h.exec(owner, h.env("issues.finish_assignment", protocol.Target{Type: "issue", ID: next.IssueID}, finish, rev(next.Revision)), HostedIssueAssignment{Finish: true})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
}
func hasConversation(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

func TestHostedManagerSettlementIgnoresLaterSiblingFact(t *testing.T) {
	h := hostedManagerHarness(t)
	source, delivery := h.say("Two independent questions share this owner material")
	first := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	second := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	if op := h.associate(owner, source, first.IssueID, second.IssueID); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	main := startHostedReview(t, h, "main", delivery)
	commitFixtureTurn(t, h, main.RequestID)
	finishHostedReview(t, h, main)
	a, pa := startHostedIssueReview(t, h, first)
	if op := hostedEffect(t, h, pa, a, 1, protocol.Target{Type: "issue", ID: first.IssueID}, nil, hostedReviewPayload(t, h, first), ReviewHostedIssueContext{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	b, pb := startHostedIssueReview(t, h, second)
	fact := protocol.ContextFact{Kind: "goal", Scope: "work_item", Body: "Investigate the second question separately", SourceMessageID: source}
	if op := hostedEffect(t, h, pb, b, 1, protocol.Target{Type: "issue", ID: second.IssueID}, rev(h.agentContext(second.IssueID).Revision), fact, HostedIssueContextFact{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if op := commitHostedReview(t, h, pa, a, 2); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	finishHostedReview(t, h, a)
	state, err := h.app.LogicalTurn(t.Context(), a.RequestID)
	if err != nil || state.Status != "completed" {
		t.Fatal("sibling context stranded scoped manager", state, err)
	}
	if len(hostedReviewPayload(t, h, second).Sources) == 0 {
		t.Fatal("sibling review obligation disappeared")
	}
}

func TestHostedHandoffRetiresRejectedOrdinaryDelivery(t *testing.T) {
	h := hostedManagerHarness(t)
	config := hostedConfigurationFixture()
	config.Revision = 2
	replacement := config.Definitions.Agents[0]
	replacement.LocalID = "replacement"
	config.Definitions.Agents = append(config.Definitions.Agents, replacement)
	op, prepared := acceptHostedFixture(t, h, config, 1)
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	h.app = New(h.store, h.ids, h.clock.Now, WithHostedConfiguration(prepared))
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	turn, _ := startHostedIssueReview(t, h, issue)
	turn.Status, turn.TurnID = store.TurnRejected, ""
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	inflight, err := h.app.InFlightDeliveries(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var original Delivery
	for _, delivery := range inflight {
		if delivery.RequestID == turn.RequestID {
			original = delivery
		}
	}
	if original.RequestID == "" {
		t.Fatal("ordinary delivery not claimed")
	}
	next := canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.assign", protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.IssueAssignmentPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeAgent, Ref: "server-hosted/replacement"}, Reason: "Replace the manager after a definite unsent rejection"}, rev(issue.Revision)), HostedIssueAssignment{}))
	finish := protocol.FinishIssueAssignmentPayload{ExpectedAssignmentEpoch: next.AssignmentEpoch, HandoffID: next.PendingHandoffID, Reason: "The old native start was definitively rejected"}
	// Rejection is known, but until its delivery result is recorded the
	// sending request remains an actual outstanding operation.
	expectWorkCode(t, h.exec(owner, h.env("issues.finish_assignment", protocol.Target{Type: "issue", ID: next.IssueID}, finish, rev(next.Revision)), HostedIssueAssignment{Finish: true}), protocol.CodeInvalidTransition)
	if err := h.app.FinishDelivery(t.Context(), original, DeliveryResult{Outcome: Retry, RetryAfter: time.Second}); err != nil {
		t.Fatal(err)
	}
	next = canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.finish_assignment", protocol.Target{Type: "issue", ID: next.IssueID}, finish, rev(next.Revision)), HostedIssueAssignment{Finish: true}))
	var status string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT status FROM outbox WHERE request_id=?`, turn.RequestID).Scan(&status)
	}); err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Fatalf("definitely unsent old-epoch delivery remains dispatchable: %s", status)
	}
	if len(hostedReviewPayload(t, h, next).Sources) == 0 {
		t.Fatal("handoff discarded original source duties")
	}
	h.clock.Advance(time.Second)
	conversation := "issue:" + issue.IssueID
	selected, named, err := h.app.HostedRuntimeSelection(t.Context(), conversation)
	if err != nil || !named || selected.Identity.LocalID != "replacement" {
		t.Fatal("old rejected executor still selected", selected, named, err)
	}
	delivery, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+conversation)
	if err != nil || !ok || delivery.RequestID == turn.RequestID {
		t.Fatal("replacement did not receive remaining responsibility", delivery, ok, err)
	}
	current := startHostedReview(t, h, conversation, delivery)
	if current.Hosted.Identity.LocalID != "replacement" {
		t.Fatal("replacement turn admitted with old snapshot", current.Hosted)
	}
}
