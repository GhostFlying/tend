package app

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestHostedFencedSendingDeliveryRestoresReadOnlyRuntime(t *testing.T) {
	for _, status := range []string{store.TurnCompleted, store.TurnRejected} {
		t.Run(status, func(t *testing.T) {
			h := hostedManagerHarness(t)
			issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
			conversation := "issue:" + issue.IssueID
			submitted := h.exec(owner, h.env("conversation.submit_message", protocol.Target{Type: "message"}, protocol.SubmitMessagePayload{ConversationID: conversation, Body: "Investigate this scoped material"}, nil), SubmitMessage{})
			if submitted.Status != protocol.OpAccepted {
				t.Fatal(submitted)
			}
			d, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+conversation)
			if err != nil || !ok || d.Operation != OpHandleMessage {
				t.Fatal(d, ok, err)
			}
			var input HandleMessage
			if err = json.Unmarshal(d.Payload, &input); err != nil {
				t.Fatal(err)
			}
			selected, named, err := h.app.NamedHostedSelection(t.Context(), conversation)
			if err != nil || !named {
				t.Fatal(named, err)
			}
			turn := store.MainTurn{RequestID: d.RequestID, MessageID: input.MessageID, ThreadID: "prior-manager-native", Status: store.TurnStarting, Hosted: &selected, Model: selected.Definition.Model, Effort: selected.Definition.Effort}
			if err = h.app.PutMainTurn(t.Context(), turn); err != nil {
				t.Fatal(err)
			}
			if status == store.TurnCompleted {
				turn.Status, turn.TurnID = store.TurnStarted, "prior-native-turn"
				if err = h.app.PutMainTurn(t.Context(), turn); err != nil {
					t.Fatal(err)
				}
			}
			turn.Status = status
			if err = h.app.PutMainTurn(t.Context(), turn); err != nil {
				t.Fatal(err)
			}
			frozen, found, err := h.app.HostedExecution(t.Context(), d.RequestID)
			if err != nil || !found {
				t.Fatal(found, err)
			}
			issue = canonicalTaskIssue(t, h, issue.IssueID)
			fenced := canonicalLifecycleIssue(t, h.exec(owner, h.env("issues.assign", protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.IssueAssignmentPayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Assignee: protocol.IssueAssignee{Kind: protocol.IssueAssigneeOwner, Ref: owner.Subject}, Reason: "Transfer concrete responsibility"}, rev(issue.Revision)), HostedIssueAssignment{}))
			finish := protocol.FinishIssueAssignmentPayload{ExpectedAssignmentEpoch: fenced.AssignmentEpoch, HandoffID: fenced.PendingHandoffID, Reason: "Reconcile the original delivery before finishing"}
			if op := h.exec(owner, h.env("issues.finish_assignment", protocol.Target{Type: "issue", ID: issue.IssueID}, finish, rev(fenced.Revision)), HostedIssueAssignment{Finish: true}); op.Status != protocol.OpFailed {
				t.Fatal("sending did not fence handoff", op)
			}
			h.app = New(h.store, h.ids, h.clock.Now, WithHostedConfiguration(h.app.hostedConfiguration))
			restored, named, err := h.app.HostedRuntimeSelection(t.Context(), conversation)
			expected := frozen.Selection
			expected.ReceiptOnly = true
			if err != nil || !named || !reflect.DeepEqual(restored, expected) {
				t.Fatal("cannot reopen original runtime for reconciliation", restored, named, err)
			}
			if _, _, err = h.app.HostedSelectionForAdmission(t.Context(), d.RequestID, conversation); protocol.CodeOf(err) != protocol.CodeForbidden {
				t.Fatal("fenced pin admitted a new business turn", err)
			}
			// This is the original dispatch receipt, never a replacement request/run.
			outcome := Delivered
			if status == store.TurnRejected {
				outcome = Retry
			}
			if err = h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: outcome}); err != nil {
				t.Fatal(err)
			}
			if op := h.exec(owner, h.env("issues.finish_assignment", protocol.Target{Type: "issue", ID: issue.IssueID}, finish, rev(fenced.Revision)), HostedIssueAssignment{Finish: true}); op.Status != protocol.OpSucceeded {
				t.Fatal("known original receipt did not release handoff", op)
			}
		})
	}
}

func TestHostedBackgroundSourcesWithholdUnadmittedAndWithdrawnMaterial(t *testing.T) {
	for _, state := range []string{"queued", "withdrawn", "admitted"} {
		t.Run(state, func(t *testing.T) {
			h := hostedManagerHarness(t)
			issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
			body := "OWNER_MATERIAL_MUST_MATCH_ITS_NATIVE_RECEIPT"
			source := queueMessage(t, h, body)
			if state == "admitted" {
				d, _, ok, err := h.app.NextDelivery(t.Context(), MainThreadDestination)
				if err != nil || !ok {
					t.Fatal(ok, err)
				}
				startHostedReview(t, h, "main", d)
			}
			if op := h.associate(owner, source, issue.IssueID); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			if state == "withdrawn" {
				var control protocol.MessageControl
				if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
					var err error
					control, err = store.MessageControl(t.Context(), tx.Q(), source)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				if op := h.exec(owner, h.env("conversation.withdraw_message", protocol.Target{Type: "message", ID: source}, protocol.WithdrawMessagePayload{}, rev(control.Revision)), WithdrawMessage{}); op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			conversation := "issue:" + issue.IssueID
			d, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+conversation)
			if err != nil || !ok || d.Operation != OpHandleEvent {
				t.Fatal(d, ok, err)
			}
			turn := startHostedReview(t, h, conversation, d)
			principal := protocol.Principal{Kind: protocol.PrincipalBackgroundAgent, Subject: "manager", Thread: turn.ThreadID, ConversationID: conversation}
			cmd := BackgroundCommand{Action: "sources", MessageID: turn.MessageID, TurnID: turn.TurnID, DeliveryID: turn.RequestID}
			op := h.exec(principal, h.env(cmd.Operation(), protocol.Target{Type: "background_batch"}, map[string]int64{"after": 0}, nil), cmd)
			if op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
			var page protocol.BackgroundSourcePage
			if err = json.Unmarshal(op.Result, &page); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, s := range page.Sources {
				if s.MessageID == source {
					found = true
					if state == "admitted" && s.Body != body {
						t.Fatal("confirmed material hidden", s)
					}
					if state != "admitted" && strings.Contains(string(op.Result), body) {
						t.Fatal("unadmitted owner content exposed", state, s)
					}
				}
			}
			if !found {
				t.Fatal("projection changed source identity/pagination", page)
			}
			if err = h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				raw, err := store.BackgroundSources(t.Context(), tx.Q(), turn.MessageID, 0)
				if err != nil {
					return err
				}
				found := false
				for _, s := range raw.Sources {
					if s.MessageID == source {
						found = true
						if s.Body != body {
							t.Fatal("owner history was rewritten")
						}
					}
				}
				if !found {
					t.Fatal("original source missing")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestHostedUnknownOrdinaryStartIsNotSourceConsumption(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	manager, p := startHostedIssueReview(t, h, issue)
	source, d := h.say("OWNER_LOST_START_RESPONSE_MATERIAL")
	selected, _, err := h.app.NamedHostedSelection(t.Context(), "main")
	if err != nil {
		t.Fatal(err)
	}
	native := store.MainTurn{RequestID: d.RequestID, MessageID: source, ThreadID: "uncertain-main-thread", Status: store.TurnStarting, Hosted: &selected, Model: selected.Definition.Model, Effort: selected.Definition.Effort}
	if err = h.app.PutMainTurn(t.Context(), native); err != nil {
		t.Fatal(err)
	}
	if err = h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Unknown, Reason: "Actual turn/start response is absent"}); err != nil {
		t.Fatal(err)
	}
	if err = h.app.AwaitNativeReceipts(t.Context(), native.ThreadID); err != nil {
		t.Fatal(err)
	}
	if op := h.associate(owner, source, issue.IssueID); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	command := HostedCoordinationCommand{Command: CanonicalOwnerIssueCommand{Action: "update"}, ExecutionID: manager.RequestID, EffectSeq: 1}
	issue = canonicalTaskIssue(t, h, issue.IssueID)
	plan := issue.Plan
	plan.Stages = []protocol.IssueStage{{StageID: "result", Title: "Result", Status: protocol.WorkItemStageDone, Evidence: []protocol.IssueEvidence{{Kind: protocol.WorkItemEvidenceOwnerDecision, Reference: source, Summary: "Owner confirmed the result"}}}}
	op, err := h.app.Execute(t.Context(), p, h.env(command.Operation(), protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.UpdateIssuePayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Plan: plan}, rev(issue.Revision)), command)
	if protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatal("unknown start treated as a consumed owner decision", op, err)
	}
	// An actual native consumption receipt, even with tail delivery unknown,
	// makes the same original message usable without a replacement identity.
	native.Status, native.TurnID = store.TurnStarted, "confirmed-native-turn"
	if err = h.app.PutMainTurn(t.Context(), native); err != nil {
		t.Fatal(err)
	}
	op, err = h.app.Execute(t.Context(), p, h.env(command.Operation(), protocol.Target{Type: "issue", ID: issue.IssueID}, protocol.UpdateIssuePayload{ExpectedAssignmentEpoch: issue.AssignmentEpoch, Plan: plan}, rev(issue.Revision)), command)
	if err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal("confirmed consumption remained hidden", op, err)
	}
}
