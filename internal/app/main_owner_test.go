package app

import (
	"context"
	"encoding/json"
	"github.com/GhostFlying/tend/internal/agentconfig"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
	"testing"
)

func mainOwnerExecutionFixture(t *testing.T, h *harness) (context.Context, string) {
	t.Helper()
	if op := h.exec(owner, h.env("categories.put", protocol.Target{Type: "category", ID: "planner"}, protocol.PutAgentCategoryPayload{Title: "Planner", State: "active"}, rev(0)), PutAgentCategory{}); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	op, prepared := acceptHostedFixture(t, h, hostedConfigurationFixture(), 0)
	if op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	selected, err := prepared.Select("planner", agentconfig.Main)
	if err != nil {
		t.Fatal(err)
	}
	request, message := h.ids.New(protocol.PrefixRequest), h.ids.New(protocol.PrefixMessage)
	err = h.store.Write(t.Context(), func(tx *store.Tx) error {
		if _, err := tx.InsertMessage(t.Context(), store.MessageRow{MessageID: message, Author: "owner", Body: "Manage current work", CreatedAt: h.clock.Now().UnixMilli()}); err != nil {
			return err
		}
		if err := tx.PutMainTurn(t.Context(), store.MainTurn{RequestID: request, MessageID: message, ThreadID: "fixture-main", TurnID: "fixture-turn", Status: store.TurnStarted}, h.clock.Now().UnixMilli()); err != nil {
			return err
		}
		return tx.InsertHostedExecution(t.Context(), store.HostedExecution{ID: request, ConversationID: "main", Selection: selected, CreatedAt: h.clock.Now().UnixMilli()})
	})
	if err != nil {
		t.Fatal(err)
	}
	h.app.hostedConfiguration = prepared
	h.app.mainOwner = owner.Subject
	call := &OwnerExecution{Actor: protocol.Principal{Kind: protocol.PrincipalMainAgent, Subject: "fixture-harness", Thread: "fixture-main"}, Agent: selected.Identity, ExecutionID: request, CallID: h.ids.New(protocol.PrefixRequest), EffectSeq: 1}
	return WithOwnerExecution(t.Context(), call), request
}

func TestMainOwnerDelegatedOwnerCancelSettlesSavedGiveUp(t *testing.T) {
	for _, delegated := range []bool{false, true} {
		t.Run(map[bool]string{false: "owner", true: "delegated"}[delegated], func(t *testing.T) {
			h, as, plan := boundedRunnerPlanFixture(t)
			if _, err := h.app.AuthorizeRunnerBoundedMaintenance(t.Context(), owner, plan); err != nil {
				t.Fatal(err)
			}
			saved := h.runAs(as, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "transport outcome remains unknown"}, Release{})
			if saved.Status != protocol.OpAccepted {
				t.Fatal(saved)
			}
			ctx := t.Context()
			if delegated {
				ctx, _ = mainOwnerExecutionFixture(t, h)
			}
			env := h.env("tasks.cancel", taskTarget(as.TaskID), struct{}{}, rev(h.task(as.TaskID).ControlRevision))
			op, err := h.app.Execute(ctx, owner, env, CancelTask{})
			if err != nil || op.Status != protocol.OpSucceeded {
				t.Fatal(op, err)
			}
			if h.run(as.RunID).ObservedState != protocol.RunFailed || h.lease(as.RunID).Status != store.LeaseReleased {
				t.Fatalf("successful cancel left run=%s lease=%s", h.run(as.RunID).ObservedState, h.lease(as.RunID).Status)
			}
		})
	}
}

func TestMainOwnerDelegatedActionResolutionRemainsOwnerEvidence(t *testing.T) {
	for _, delegated := range []bool{false, true} {
		t.Run(map[bool]string{false: "owner", true: "delegated"}[delegated], func(t *testing.T) {
			h := newHarness(t)
			issue := createCanonicalOwnerTestIssue(t, h)
			a := attentionForTest(t, h, protocol.CreateAttentionPayload{IssueID: issue.IssueID, Kind: "action", Title: "Approve the current outcome"})
			ctx := t.Context()
			if delegated {
				ctx, _ = mainOwnerExecutionFixture(t, h)
			}
			env := h.env("attentions.resolve", protocol.Target{Type: "attention", ID: a.AttentionID}, protocol.HandleAttentionPayload{ExpectedAssignmentEpoch: a.Responsibility.AssignmentEpoch, Reason: "Reviewed current facts", Text: "The current outcome is approved"}, rev(a.Responsibility.Revision))
			op, err := h.app.Execute(ctx, owner, env, HandleAttention{})
			if err != nil || op.Status != protocol.OpSucceeded {
				t.Fatal(op, err)
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				valid, err := resolvedIssueAttentionDecision(t.Context(), tx.Q(), issue.IssueID, a.AttentionID)
				if err != nil {
					return err
				}
				if !valid {
					t.Error("resolved owner-authorized action is rejected as owner decision evidence")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMainOwnerDelegatedRunSupplementKeepsAuthenticatedSender(t *testing.T) {
	h, as := messageRun(t)
	ctx, _ := mainOwnerExecutionFixture(t, h)
	env := h.env(protocol.OpRunsSendMessage, runTarget(as.RunID), protocol.SendRunMessagePayload{TaskID: as.TaskID, SpecVersion: as.SpecVersion, Generation: as.Generation, Text: "Inspect this extra evidence"}, rev(h.task(as.TaskID).ControlRevision))
	op, err := h.app.Execute(ctx, owner, env, SendRunMessage{})
	if err != nil {
		t.Fatal(err)
	}
	m := messageResult(t, op)
	if m.Source.Kind != protocol.PrincipalMainAgent {
		t.Fatalf("run supplement authenticated sender=%+v, want actual main Agent", m.Source)
	}
}

func TestMainOwnerDelegatedInputRetainsOwnerControls(t *testing.T) {
	for _, delegated := range []bool{false, true} {
		t.Run(map[bool]string{false: "owner", true: "delegated"}[delegated], func(t *testing.T) {
			h := newHarness(t)
			ctx := t.Context()
			if delegated {
				ctx, _ = mainOwnerExecutionFixture(t, h)
			}
			env := h.env("conversation.submit_message", protocol.Target{Type: "conversation"}, protocol.SubmitMessagePayload{Body: "Coordinate the current work"}, nil)
			op, err := h.app.Execute(ctx, owner, env, SubmitMessage{})
			if err != nil || op.Status != protocol.OpAccepted {
				t.Fatal(op, err)
			}
			var message struct {
				MessageID string `json:"message_id"`
			}
			if err := json.Unmarshal(op.Result, &message); err != nil {
				t.Fatal(err)
			}
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				_, err := store.MessageControl(t.Context(), tx.Q(), message.MessageID)
				return err
			}); err != nil {
				t.Error("queued input has no withdrawal control:", err)
			}
			interrupted, err := h.app.Execute(t.Context(), owner, h.env("conversation.interrupt_reply", protocol.Target{Type: "message"}, protocol.InterruptReplyPayload{MessageID: message.MessageID}, nil), InterruptReply{})
			if err != nil || interrupted.Error != nil {
				t.Fatal("owner cannot interrupt admitted input", interrupted, err)
			}
		})
	}
}

func TestMainOwnerDelegatedInputCanSupplyIssueContextFact(t *testing.T) {
	for _, delegated := range []bool{false, true} {
		t.Run(map[bool]string{false: "owner", true: "delegated"}[delegated], func(t *testing.T) {
			h := newHarness(t)
			mainCtx, _ := mainOwnerExecutionFixture(t, h)
			issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
			ctx := t.Context()
			if delegated {
				ctx = mainCtx
			}
			submitted, err := h.app.Execute(ctx, owner, h.env("conversation.submit_message", protocol.Target{Type: "conversation"}, protocol.SubmitMessagePayload{ConversationID: "issue:" + issue.IssueID, Body: "Do not change the established API"}, nil), SubmitMessage{})
			if err != nil || submitted.Status != protocol.OpAccepted {
				t.Fatal(submitted, err)
			}
			var source struct {
				MessageID string `json:"message_id"`
			}
			if err := json.Unmarshal(submitted.Result, &source); err != nil {
				t.Fatal(err)
			}
			delivery, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:issue:"+issue.IssueID)
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			turn := startHostedReview(t, h, "issue:"+issue.IssueID, delivery)
			principal := protocol.Principal{Kind: protocol.PrincipalBackgroundAgent, Subject: "issue-manager", Thread: turn.ThreadID, ConversationID: "issue:" + issue.IssueID}
			fact := protocol.ContextFact{Scope: "work_item", Kind: "constraint", Body: "Do not change the established API", SourceMessageID: source.MessageID}
			op := hostedEffect(t, h, principal, turn, 1, protocol.Target{Type: "issue", ID: issue.IssueID}, rev(h.agentContext(issue.IssueID).Revision), fact, HostedIssueContextFact{})
			if op.Status != protocol.OpSucceeded {
				t.Fatalf("cannot record exact admitted input as context fact: %+v", op)
			}
		})
	}
}

func TestMainOwnerInputCreatesIssueReviewObligation(t *testing.T) {
	for _, delegated := range []bool{false, true} {
		t.Run(map[bool]string{false: "owner", true: "main"}[delegated], func(t *testing.T) {
			h := newHarness(t)
			mainCtx, _ := mainOwnerExecutionFixture(t, h)
			issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
			before := h.agentContext(issue.IssueID).Revision
			ctx := t.Context()
			if delegated {
				ctx = mainCtx
			}
			op, err := h.app.Execute(ctx, owner, h.env("conversation.submit_message", protocol.Target{Type: "conversation"}, protocol.SubmitMessagePayload{ConversationID: "issue:" + issue.IssueID, Body: "A new binding constraint"}, nil), SubmitMessage{})
			if err != nil || op.Error != nil {
				t.Fatal(op, err)
			}
			var m struct {
				MessageID string `json:"message_id"`
			}
			if err := json.Unmarshal(op.Result, &m); err != nil {
				t.Fatal(err)
			}
			var pending bool
			if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
				return tx.Q().QueryRowContext(t.Context(), `SELECT EXISTS(SELECT 1 FROM message_context_obligations WHERE work_item_id=? AND message_id=? AND needs_review=1)`, issue.IssueID, m.MessageID).Scan(&pending)
			}); err != nil {
				t.Fatal(err)
			}
			if after := h.agentContext(issue.IssueID).Revision; !pending || after <= before {
				t.Fatalf("new input leaves pending=%v context %d -> %d", pending, before, after)
			}
		})
	}
}

func TestMainOwnerCommunicationRejectsUnadmittedInputs(t *testing.T) {
	for _, state := range []string{"queued", "withdrawn"} {
		t.Run(state, func(t *testing.T) {
			h := newHarness(t)
			ctx, _ := mainOwnerExecutionFixture(t, h)
			issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
			op, err := h.app.Execute(ctx, owner, h.env("conversation.submit_message", protocol.Target{Type: "conversation"}, protocol.SubmitMessagePayload{Body: "Pending owner instruction"}, nil), SubmitMessage{})
			if err != nil || op.Error != nil {
				t.Fatal(op, err)
			}
			var m struct {
				MessageID string `json:"message_id"`
			}
			if err := json.Unmarshal(op.Result, &m); err != nil {
				t.Fatal(err)
			}
			if op := h.associate(owner, m.MessageID, issue.IssueID); op.Error != nil {
				t.Fatal(op)
			}
			if state == "withdrawn" {
				var control protocol.MessageControl
				if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
					var err error
					control, err = store.MessageControl(t.Context(), tx.Q(), m.MessageID)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				if op := h.exec(owner, h.env("conversation.withdraw_message", protocol.Target{Type: "message", ID: m.MessageID}, protocol.WithdrawMessagePayload{}, rev(control.Revision)), WithdrawMessage{}); op.Error != nil {
					t.Fatal(op)
				}
			}
			conversation := "issue:" + issue.IssueID
			delivery, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+conversation)
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			turn := startHostedReview(t, h, conversation, delivery)
			if input, err := h.app.ReadCommunicationInput(t.Context(), turn.RequestID, m.MessageID); protocol.CodeOf(err) != protocol.CodeForbidden {
				t.Fatalf("%s source visible before admission: %+v, %v", state, input, err)
			}
		})
	}
}

func TestMainOwnerReuseAcceptsDelegatedSource(t *testing.T) {
	h := newHarness(t)
	ctx, _ := mainOwnerExecutionFixture(t, h)
	issue := createCanonicalOwnerTestIssue(t, h)
	submitted, err := h.app.Execute(ctx, owner, h.env("conversation.submit_message", protocol.Target{Type: "conversation"}, protocol.SubmitMessagePayload{Body: "Continue the same approved work"}, nil), SubmitMessage{})
	if err != nil || submitted.Error != nil {
		t.Fatal(submitted, err)
	}
	var m struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(submitted.Result, &m); err != nil {
		t.Fatal(err)
	}
	op, err := h.app.Execute(ctx, owner, h.env("issues.reuse", protocol.Target{Type: "issue", ID: issue.IssueID}, reusePayload(t, h, issue, m.MessageID), rev(issue.Revision)), ReuseIssue{})
	if err != nil || op.Status != protocol.OpSucceeded {
		t.Fatal(op, err)
	}
}
