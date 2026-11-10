package app

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// retainLegacyDeliveryAnswer loads the historical accepted representation with
// its actual owner message and content revision. It does not ask the corrected
// answer entry point to accept an invalid new request.
func retainLegacyDeliveryAnswer(t *testing.T, h *harness, duty protocol.IssueDeliveryDuty, option, text string) store.DecisionRow {
	t.Helper()
	env := h.env("attentions.respond", protocol.Target{Type: "attention", ID: duty.AttentionID}, protocol.RespondAttentionPayload{Version: 1, Option: option, Text: text}, nil)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		a, err := store.GetAttention(t.Context(), tx.Q(), duty.AttentionID)
		if err != nil {
			return err
		}
		d, err := store.GetDecision(t.Context(), tx.Q(), a.DecisionID)
		if err != nil {
			return err
		}
		x := &Exec{Env: env, Principal: owner, IDs: h.ids, Now: h.clock.Now()}
		p := protocol.AnswerDecisionPayload{Version: 1, Option: option, Text: text}
		if _, err := recordDecisionAnswerSource(t.Context(), x, tx, d, &p); err != nil {
			return err
		}
		raw, _ := json.Marshal(decisionAnswer{Option: option, Text: text})
		if err := tx.AnswerDecision(t.Context(), d.DecisionID, 1, string(raw), p.SourceMessageID, x.Now.UnixMilli()); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO issue_delivery_answers(decision_id,message_id,content_revision) SELECT ?,m.message_id,COALESCE(md.content_revision,1) FROM conversation_messages m LEFT JOIN message_dispositions md USING(message_id) WHERE m.message_id=?`, d.DecisionID, p.SourceMessageID); err != nil {
			return err
		}
		a.Status = "resolved"
		return saveAttention(t.Context(), x, tx, &a, a.Revision)
	}); err != nil {
		t.Fatal(err)
	}
	return deliveryDecisionForTest(t, h, duty)
}

func deliveryDecisionForTest(t *testing.T, h *harness, duty protocol.IssueDeliveryDuty) store.DecisionRow {
	t.Helper()
	var d store.DecisionRow
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		a, err := store.GetAttention(t.Context(), tx.Q(), duty.AttentionID)
		if err != nil {
			return err
		}
		d, err = store.GetDecision(t.Context(), tx.Q(), a.DecisionID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDeliveryMixedAnswerRejectsApprovalButRetainsExplicitDeferral(t *testing.T) {
	for _, entry := range []string{"decision", "attention", "from-message"} {
		for _, option := range []string{"approve", "defer", "end_delivery"} {
			t.Run(entry+"/"+option, func(t *testing.T) {
				h, i, task, c := deliveryFixture(t)
				duty := prepareDeliveryForTest(t, h, i.IssueID, task, c)
				d := deliveryDecisionForTest(t, h, duty)
				messages := h.count("conversation_messages")
				var op protocol.Operation
				if entry == "from-message" {
					op = h.exec(mainAgent, h.env("decisions.resolve_from_message", protocol.Target{Type: "decision", ID: d.DecisionID}, protocol.AnswerDecisionPayload{Version: 1, Option: option, Text: "Only after further verification", SourceMessageID: h.ownerEvidence().Reference}, nil), AnswerDecision{FromMessage: true})
					messages++ // The separately submitted real owner source remains.
				} else if entry == "decision" {
					op = h.exec(owner, h.env("decisions.respond", protocol.Target{Type: "decision", ID: d.DecisionID}, protocol.AnswerDecisionPayload{Version: 1, Option: option, Text: "Only after further verification"}, nil), AnswerDecision{})
				} else {
					op = h.exec(owner, h.env("attentions.respond", protocol.Target{Type: "attention", ID: duty.AttentionID}, protocol.RespondAttentionPayload{Version: 1, Option: option, Text: "Only after further verification"}, rev(1)), AttentionCommand{Action: "respond"})
				}
				if option == "defer" {
					if op.Status != protocol.OpSucceeded {
						t.Fatal("explicit deferral with explanation was rejected", op)
					}
					deferred := deliveryDutyForTest(t, h, i.IssueID, task)
					if deferred.Status != "deferred" || deferred.AttentionID != duty.AttentionID || !reflect.DeepEqual(prepareDeliveryForTest(t, h, i.IssueID, task, c), deferred) {
						t.Fatal("deferral was reopened or lost", deferred)
					}
					answer := deliveryDecisionForTest(t, h, duty)
					var p decisionAnswer
					if err := json.Unmarshal([]byte(answer.AnswerJSON), &p); err != nil || p.Option != "defer" || p.Text != "Only after further verification" {
						t.Fatal("deferral explanation changed", answer, err)
					}
					return
				}
				expectWorkCode(t, op, protocol.CodeInvalidRequest)
				if !strings.Contains(op.Error.Message, "reason") {
					t.Fatal("missing explanation placement", op)
				}
				if got := deliveryDecisionForTest(t, h, duty); !reflect.DeepEqual(got, d) || h.count("conversation_messages") != messages || h.count("issue_delivery_answers") != 0 {
					t.Fatal("rejected mixed answer changed original facts", got)
				}
				if got := prepareDeliveryForTest(t, h, i.IssueID, task, c); got.AttentionID != duty.AttentionID {
					t.Fatal("rejection replaced open question", got)
				}
				answerDeliveryForTest(t, h, duty, option, "")
			})
		}
	}
}

func TestDeliveryHistoricalMixedAnswerCanPrepareSameCandidateWithoutRewritingHistory(t *testing.T) {
	h, _, issue, impl, c := liveReviewedDeliveryFixture(t)
	setPRPolicyForTest(t, h, issue.IssueID, "manual")
	duty := prepareLiveDeliveryForTest(t, h, issue.IssueID, impl.TaskID, c)
	old := retainLegacyDeliveryAnswer(t, h, duty, "approve", "Keep the original constraints")
	p := protocol.UpdateIssueDeliveryPayload{Action: "bind", TaskID: impl.TaskID, ExecutionTaskID: impl.TaskID, ExpectedTaskControlRevision: rev(h.task(impl.TaskID).ControlRevision), IssueControlPayload: protocol.IssueControlPayload{Reason: "Bind the original reviewed Task"}}
	assertLiveDeliveryBindCode(t, h, issue.IssueID, p, protocol.CodeForbidden)
	// An already accepted legacy answer retains its idempotent read semantics.
	op := h.exec(owner, h.env("decisions.respond", protocol.Target{Type: "decision", ID: old.DecisionID}, protocol.AnswerDecisionPayload{Version: 1, Option: "approve", Text: "Keep the original constraints"}, nil), AnswerDecision{})
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	before := h.task(impl.TaskID)
	next := prepareLiveDeliveryForTest(t, h, issue.IssueID, impl.TaskID, c)
	if next.AttentionID == duty.AttentionID || next.Revision != duty.Revision+1 || !reflect.DeepEqual(next.Candidate, duty.Candidate) {
		t.Fatal("unusable live answer was reused", next)
	}
	assertLiveDeliveryBindCode(t, h, issue.IssueID, p, protocol.CodeForbidden)
	answerDeliveryForTest(t, h, next, "approve", "")
	if op := updateDeliveryForTest(t, h, issue.IssueID, p); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	if after := h.task(impl.TaskID); after.SpecVersion != before.SpecVersion || after.ControlRevision != before.ControlRevision || after.CurrentArtifactID != "" {
		t.Fatal("recovery changed original Task", after)
	}
	if got := deliveryDecisionForTest(t, h, duty); !reflect.DeepEqual(got, old) {
		t.Fatal("recovery changed original answer", got, old)
	}
}

func assertLiveDeliveryBindCode(t *testing.T, h *harness, issue string, p protocol.UpdateIssueDeliveryPayload, want protocol.Code) {
	t.Helper()
	i := canonicalTaskIssue(t, h, issue)
	p.ExpectedAssignmentEpoch = i.AssignmentEpoch
	op, err := h.app.Execute(t.Context(), owner, h.env("issues.update_delivery", protocol.Target{Type: "issue", ID: issue}, p, rev(i.Revision)), UpdateIssueDelivery{})
	code := protocol.CodeOf(err)
	if code == "" && op.Error != nil {
		code = op.Error.Code
	}
	if code != want {
		t.Fatal("binding result", op, err, want)
	}
}

func TestDeliveryTextOnlyAnswerDoesNotAuthorizeAndCanPrepareExplicitly(t *testing.T) {
	h, _, i, impl, c := liveReviewedDeliveryFixture(t)
	setPRPolicyForTest(t, h, i.IssueID, "manual")
	duty := prepareLiveDeliveryForTest(t, h, i.IssueID, impl.TaskID, c)
	answerDeliveryForTest(t, h, duty, "", "Wait for verification before merging")
	old := deliveryDecisionForTest(t, h, duty)
	p := protocol.UpdateIssueDeliveryPayload{Action: "bind", TaskID: impl.TaskID, ExecutionTaskID: impl.TaskID, ExpectedTaskControlRevision: rev(h.task(impl.TaskID).ControlRevision), IssueControlPayload: protocol.IssueControlPayload{Reason: "Free text cannot approve the original Task"}}
	assertLiveDeliveryBindCode(t, h, i.IssueID, p, protocol.CodeForbidden)
	next := prepareLiveDeliveryForTest(t, h, i.IssueID, impl.TaskID, c)
	if next.AttentionID == duty.AttentionID || !reflect.DeepEqual(deliveryDecisionForTest(t, h, duty), old) {
		t.Fatal("free answer was lost or permanently reused", next)
	}
}

func TestDeliveryValidChoicesKeepSameQuestion(t *testing.T) {
	for _, option := range []string{"approve", "defer", "end_delivery"} {
		t.Run(option, func(t *testing.T) {
			h, i, task, c := deliveryFixture(t)
			duty := prepareDeliveryForTest(t, h, i.IssueID, task, c)
			answerDeliveryForTest(t, h, duty, option, "")
			before := deliveryDutyForTest(t, h, i.IssueID, task)
			next := prepareDeliveryForTest(t, h, i.IssueID, task, c)
			if !reflect.DeepEqual(next, before) || h.count("attentions") != 1 {
				t.Fatal("valid choice duplicated question", before, next)
			}
		})
	}
}

func TestDeliveryManualAnswerRecoveryCannotBecomeAutomaticGrant(t *testing.T) {
	for _, answer := range []string{"text-only", "historical-mixed"} {
		t.Run(answer, func(t *testing.T) {
			h, _, i, impl, c := liveReviewedDeliveryFixture(t)
			setPRPolicyForTest(t, h, i.IssueID, "manual")
			duty := prepareLiveDeliveryForTest(t, h, i.IssueID, impl.TaskID, c)
			setPRPolicyForTest(t, h, i.IssueID, "auto")
			if answer == "text-only" {
				answerDeliveryForTest(t, h, duty, "", "Wait for another verification")
			} else {
				retainLegacyDeliveryAnswer(t, h, duty, "approve", "Wait for another verification")
			}
			old := deliveryDecisionForTest(t, h, duty)
			next := prepareLiveDeliveryForTest(t, h, i.IssueID, impl.TaskID, c)
			if next.Status != "awaiting_owner" || next.AttentionID == "" || next.AttentionID == duty.AttentionID || next.Authorization == nil || next.Authorization.Mode != "manual" {
				t.Fatal("conditional answer became automatic execution authority", next)
			}
			p := protocol.UpdateIssueDeliveryPayload{Action: "bind", TaskID: impl.TaskID, ExecutionTaskID: impl.TaskID, ExpectedTaskControlRevision: rev(h.task(impl.TaskID).ControlRevision), IssueControlPayload: protocol.IssueControlPayload{Reason: "Conditional answer still requires its concrete confirmation"}}
			assertLiveDeliveryBindCode(t, h, i.IssueID, p, protocol.CodeForbidden)
			if !reflect.DeepEqual(deliveryDecisionForTest(t, h, duty), old) {
				t.Fatal("historical answer changed")
			}
			answerDeliveryForTest(t, h, next, "approve", "")
			if op := updateDeliveryForTest(t, h, i.IssueID, p); op.Status != protocol.OpSucceeded {
				t.Fatal(op)
			}
		})
	}
}

func TestDeliveryAnswerRecoveryRechecksLiveSourceControls(t *testing.T) {
	for _, change := range []string{"missing-control", "stale-control", "paused", "cancelled", "paused-current", "cancelled-current"} {
		t.Run(change, func(t *testing.T) {
			h, _, i, impl, c := liveReviewedDeliveryFixture(t)
			setPRPolicyForTest(t, h, i.IssueID, "manual")
			duty := prepareLiveDeliveryForTest(t, h, i.IssueID, impl.TaskID, c)
			answerDeliveryForTest(t, h, duty, "", "Only after further verification")
			control := h.task(impl.TaskID).ControlRevision
			expected := rev(control)
			switch change {
			case "missing-control":
				expected = nil
			case "stale-control":
				expected = rev(control + 1)
			case "paused", "paused-current":
				op := h.exec(owner, h.env("control.set_mode", taskTarget(impl.TaskID), protocol.SetControlModePayload{Mode: protocol.ControlPaused}, rev(control)), SetControlMode{})
				if op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			case "cancelled", "cancelled-current":
				op := h.exec(owner, h.env("tasks.cancel", taskTarget(impl.TaskID), nil, rev(control)), CancelTask{})
				if op.Status != protocol.OpSucceeded {
					t.Fatal(op)
				}
			}
			if strings.HasSuffix(change, "-current") {
				expected = rev(h.task(impl.TaskID).ControlRevision)
			}
			before := deliveryDutyForTest(t, h, i.IssueID, impl.TaskID)
			i = canonicalTaskIssue(t, h, i.IssueID)
			attentionCount := h.count("attentions")
			p := protocol.UpdateIssueDeliveryPayload{Action: "prepare", TaskID: impl.TaskID, Candidate: &c, ExpectedTaskControlRevision: expected, IssueControlPayload: protocol.IssueControlPayload{ExpectedAssignmentEpoch: i.AssignmentEpoch, Reason: "Recovery must retain the actual Task controls"}}
			op, err := h.app.Execute(t.Context(), owner, h.env("issues.update_delivery", protocol.Target{Type: "issue", ID: i.IssueID}, p, rev(i.Revision)), UpdateIssueDelivery{})
			code := protocol.CodeOf(err)
			if op.Error != nil {
				code = op.Error.Code
			}
			if change == "paused-current" {
				if err != nil || op.Status != protocol.OpSucceeded || deliveryDutyForTest(t, h, i.IssueID, impl.TaskID).AttentionID == before.AttentionID {
					t.Fatal("current paused control was unnecessarily rejected", op, err)
				}
				return
			}
			want := protocol.CodeStaleRevision
			if change == "cancelled-current" {
				want = protocol.CodeInvalidTransition
			}
			if code != want {
				t.Fatal("stale live source replaced question", op, err)
			}
			if !reflect.DeepEqual(before, deliveryDutyForTest(t, h, i.IssueID, impl.TaskID)) || canonicalTaskIssue(t, h, i.IssueID).Revision != i.Revision || h.count("attentions") != attentionCount {
				t.Fatal("failed control check mutated confirmation")
			}
		})
	}
}

func TestDeliveryAnswerRecoveryPreservesCompletedHistoricalSource(t *testing.T) {
	h, _, i, task, c, acceptance := sharedReviewFixture(t)
	importSharedReviewForTest(t, h, i, task, acceptance)
	setPRPolicyForTest(t, h, i.IssueID, "manual")
	p := protocol.UpdateIssueDeliveryPayload{Action: "prepare", TaskID: task, Candidate: &c, IssueControlPayload: protocol.IssueControlPayload{Reason: "Recover the accepted historical source without live Task controls"}}
	if op := updateDeliveryForTest(t, h, i.IssueID, p); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	duty := deliveryDutyForTest(t, h, i.IssueID, task)
	if duty.AcceptanceID == "" || h.task(task).Lifecycle != protocol.TaskCompleted {
		t.Fatal("not an accepted historical source", duty)
	}
	answerDeliveryForTest(t, h, duty, "", "Wait until verification is complete")
	old := deliveryDecisionForTest(t, h, duty)
	if op := updateDeliveryForTest(t, h, i.IssueID, p); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	next := deliveryDutyForTest(t, h, i.IssueID, task)
	if next.AttentionID == duty.AttentionID || next.Status != "awaiting_owner" || !reflect.DeepEqual(deliveryDecisionForTest(t, h, duty), old) {
		t.Fatal("historical recovery acquired a live Task precondition", next)
	}
}
