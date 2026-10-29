package app

import (
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"testing"
	"time"
)

func submitHostedAdmissionInput(t *testing.T, h *harness, conversation string) {
	t.Helper()
	op := h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{ConversationID: conversation, Body: "Continue with this additional owner goal"}, nil), SubmitMessage{})
	if op.Status != protocol.OpAccepted {
		t.Fatal(op)
	}
}

func TestHostedHandoffFencesFirstClaimBeforeNativeAdmission(t *testing.T) {
	for _, status := range []string{"sending", "unknown", "failed"} {
		t.Run(status, func(t *testing.T) {
			h := hostedManagerHarness(t)
			issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
			conversation := "issue:" + issue.IssueID
			submitHostedAdmissionInput(t, h, conversation)
			d, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+conversation)
			if err != nil || !ok {
				t.Fatal(d, ok, err)
			}
			if _, err = h.app.AgentContext(t.Context(), conversation, d.RequestID); err != nil {
				t.Fatal(err)
			}
			if h.count("hosted_executions") != 0 || h.count("main_turns") != 0 || h.count("logical_turn_executions") != 0 {
				t.Fatal("native execution already admitted")
			}
			if status != "sending" {
				outcome := Unknown
				if status == "failed" {
					outcome = Failed
				}
				if err = h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: outcome}); err != nil {
					t.Fatal(err)
				}
			}
			next := canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.assign", protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.IssueAssignmentPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: owner.Subject}, Reason: "Owner receives remaining responsibility"}, rev(issue.Revision)), HostedIssueAssignment{}))
			finish := protocol.FinishIssueAssignmentPayload{ExpectedAssignmentEpoch: next.AssignmentEpoch, HandoffID: next.PendingHandoffID, Reason: "Finish only after delivery reconciliation"}
			op := h.exec(owner, h.env("issues.finish_assignment", protocol.Target{Type: "issue", ID: issue.IssueID}, finish, rev(next.Revision)), HostedIssueAssignment{Finish: true})
			if status == "failed" {
				if op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			} else {
				expectWorkCode(t, op, protocol.CodeInvalidTransition)
			}
		})
	}
}

func prepareHostedAdmissionRepair(t *testing.T, h *harness, issue protocol.Issue) store.MainTurn {
	t.Helper()
	turn, _ := startHostedIssueReview(t, h, issue)
	finishHostedReview(t, h, turn)
	h.clock.Advance(11 * time.Minute)
	return turn
}
func reloadHostedAdmissionConfig(t *testing.T, h *harness, disabled bool) {
	t.Helper()
	c := hostedConfigurationFixture()
	c.Revision = 2
	c.Definitions.Agents[0].Revision = 2
	c.Definitions.Agents[0].Model = "new-model"
	if disabled {
		replacement := c.Definitions.Agents[0]
		replacement.LocalID = "replacement-main"
		c.Definitions.Agents = append(c.Definitions.Agents, replacement)
		c.MainAgentID = "replacement-main"
		c.Definitions.Agents[0].Enabled = false
	}
	op, p := acceptHostedFixture(t, h, c, 1)
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	h.app = New(h.store, h.ids, h.clock.Now, WithHostedConfiguration(p))
}
func TestHostedRuntimeSelectionMatchesOwnerPriorityOverPinnedRepair(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	prepareHostedAdmissionRepair(t, h, issue)
	reloadHostedAdmissionConfig(t, h, false)
	conversation := "issue:" + issue.IssueID
	submitHostedAdmissionInput(t, h, conversation)
	selected, named, err := h.app.HostedRuntimeSelection(t.Context(), conversation)
	if err != nil || !named {
		t.Fatal(selected, named, err)
	}
	d, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+conversation)
	if err != nil || !ok || d.Operation != OpHandleMessage {
		t.Fatal(d, ok, err)
	}
	admitted, named, err := h.app.HostedSelectionForAdmission(t.Context(), d.RequestID, conversation)
	if err != nil || !named || selected.ConfigurationRevision != admitted.ConfigurationRevision || selected.ConfigurationRevision != 2 {
		t.Fatal("runtime queue head generation mismatch", selected.ConfigurationRevision, admitted.ConfigurationRevision, err)
	}
}
func TestHostedOrdinaryReceiptBlocksNewBusinessButPreservesDiscovery(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	turn, _ := startHostedIssueReview(t, h, issue)
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
			if err = h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Unknown}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if h.count("agent_recoveries") != 0 {
		t.Fatal("fixture has recovery rather than ordinary native receipt")
	}
	conversation := "issue:" + issue.IssueID
	submitHostedAdmissionInput(t, h, conversation)
	ids, err := h.app.PendingAgentConversations(t.Context())
	if err != nil || !hasConversation(ids, conversation) {
		t.Fatal("receipt discovery lost", ids, err)
	}
	if d, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+conversation); err != nil || ok {
		t.Fatal("uncertain ordinary receipt admitted fresh business", d, ok, err)
	}
	turn.Status = store.TurnInterrupted
	turn.NativeReconciled = true
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	resumed, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+conversation)
	if err != nil || !ok || resumed.Operation != OpHandleMessage {
		t.Fatal("reconciled receipt did not release the original pending input", resumed, ok, err)
	}
	other := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	submitHostedAdmissionInput(t, h, "issue:"+other.IssueID)
	if _, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:issue:"+other.IssueID); err != nil || !ok {
		t.Fatal("unrelated issue blocked", ok, err)
	}
}
func TestHostedFrozenRepairServiceableAfterCurrentDefinitionDisabled(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	prepareHostedAdmissionRepair(t, h, issue)
	conversation := "issue:" + issue.IssueID
	submitHostedAdmissionInput(t, h, conversation)
	reloadHostedAdmissionConfig(t, h, true)
	ids, err := h.app.PendingAgentConversations(t.Context())
	if err != nil || !hasConversation(ids, conversation) {
		t.Fatal("frozen repair excluded by current definition", ids, err)
	}
	d, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+conversation)
	if err != nil || !ok || d.Operation != "conversation.repair_turn" {
		t.Fatal("fresh unavailable work stranded repair", d, ok, err)
	}
	if _, err = h.app.AgentContext(t.Context(), conversation, d.RequestID); err != nil {
		t.Fatal("frozen repair context rejected", err)
	}
	selection, named, err := h.app.HostedSelectionForAdmission(t.Context(), d.RequestID, conversation)
	if err != nil || !named || selection.ConfigurationRevision != 1 {
		t.Fatal(selection, named, err)
	}
}

func TestHostedFrozenRecoveryServiceableAfterCurrentDefinitionRemoved(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	turn, _ := startHostedIssueReview(t, h, issue)
	turn.Status = store.TurnFailed
	turn.Failure = &protocol.AgentFailure{Code: "rate_limit_exceeded", HTTPStatus: 429}
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	finishOriginalHostedDelivery(t, h, turn.RequestID)
	conversation := "issue:" + issue.IssueID
	c := hostedConfigurationFixture()
	c.Revision = 2
	c.MainAgentID = "replacement-main"
	c.Definitions.Agents[0].LocalID = "replacement-main"
	op, p := acceptHostedFixture(t, h, c, 1)
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	h.app = New(h.store, h.ids, h.clock.Now, WithHostedConfiguration(p))
	h.clock.Advance(time.Minute)
	ids, err := h.app.PendingAgentConversations(t.Context())
	if err != nil || !hasConversation(ids, conversation) {
		t.Fatal("frozen recovery discovery lost", ids, err)
	}
	selected, named, err := h.app.HostedRuntimeSelection(t.Context(), conversation)
	if err != nil || !named || selected.ConfigurationRevision != 1 {
		t.Fatal("frozen recovery runtime lost", selected, named, err)
	}
	d, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+conversation)
	if err != nil || !ok || d.Operation != OpRecoverAgent {
		t.Fatal("fresh unavailable work blocked recovery", d, ok, err)
	}
	if _, err = h.app.AgentContext(t.Context(), conversation, d.RequestID); err != nil {
		t.Fatal("frozen recovery context rejected", err)
	}
	if admitted, err := h.app.AdmitAgentRecovery(t.Context(), d.RequestID, turn.ThreadID, selected.ConfigurationRevision); err != nil || !admitted {
		t.Fatal("same-epoch frozen recovery admission failed", admitted, err)
	}
}
