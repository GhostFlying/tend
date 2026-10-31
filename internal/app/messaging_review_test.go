package app

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func TestReviewNamedMainCanonicalPublication(t *testing.T) {
	h := hostedManagerHarness(t)
	source, d := h.say("Investigate this goal")
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	if op := h.associate(owner, source, issue.IssueID); op.Error != nil {
		t.Fatal(op.Error)
	}
	turn := startHostedReview(t, h, "main", d)
	p := protocol.CoordinationReply{ReplyTo: source, Body: "The investigation is delegated.", Purpose: "progress", Disposition: "linked", WorkItemIDs: []string{issue.IssueID}, Contexts: []protocol.LogicalContextReview{{WorkItemID: issue.IssueID, Revision: h.agentContext(issue.IssueID).Revision}}, ReviewedKinds: allFactKinds}
	op := hostedEffect(t, h, mainAgent, turn, 1, protocol.Target{Type: "logical_turn", ID: turn.RequestID}, nil, p, CoordinationReply{})
	if op.Error != nil {
		t.Fatalf("named main cannot publish canonical reply: %v", op.Error)
	}
}

func TestReviewMainAttachmentOnlyPublication(t *testing.T) {
	h := newHarness(t)
	source, turn := startReviewTurn(t, h)
	uploaded, err := h.app.UploadConversationAttachment(t.Context(), owner, h.ids.New(protocol.PrefixRequest), "evidence.txt", "text/plain", strings.NewReader("evidence"))
	if err != nil || uploaded.Error != nil {
		t.Fatal(uploaded, err)
	}
	var file protocol.ConversationAttachment
	if err := json.Unmarshal(uploaded.Result, &file); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error { return tx.BindConversationAttachment(t.Context(), source, 0, file.FileID) }); err != nil {
		t.Fatal(err)
	}
	p := protocol.CoordinationReply{ReplyTo: source, Attachments: []string{file.FileID}, Purpose: "final", Disposition: "unassigned", ReviewedKinds: allFactKinds}
	env := h.env("conversation.reply", protocol.Target{Type: "logical_turn", ID: turn.RequestID}, p, nil)
	env.ParentRequestID = turn.RequestID
	op := h.exec(mainAgent, env, CoordinationReply{})
	if op.Error != nil {
		t.Fatalf("attachment-only publication rejected: %v", op.Error)
	}
}

func TestReviewRescheduledReminderCannotStillSend(t *testing.T) {
	h := newHarness(t)
	h.app.followupsEnabled = true
	if err := h.app.ConfigureTelegram(t.Context(), 123); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE messaging_channels SET owner=?,user_id=456,chat_id=456,status='ready' WHERE channel_id='telegram'`, owner.Subject)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	issue := createCanonicalOwnerTestIssue(t, h)
	d := putDeadlineForTest(t, h, "", nil, deadlinePayloadForTest(h, issue, 2*time.Hour))
	old := h.occurrences(d.NearRuleID)[0]
	putDeadlineForTest(t, h, d.DeadlineID, rev(d.Revision), deadlinePayloadForTest(h, issue, 50*time.Hour))
	if h.occurrences(d.NearRuleID)[0].Status != "cancelled" {
		t.Fatal("fixture was not cancelled")
	}
	delivery, _, ok, err := h.app.NextDelivery(t.Context(), MessagingDestination)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		var p MessagingPart
		_ = json.Unmarshal(delivery.Payload, &p)
		if p.MessageID == old.MessageID {
			t.Fatal("cancelled reminder is still queued for Telegram delivery")
		}
	}
}

func TestReviewNativeCompletionIsNotLogicalCompletion(t *testing.T) {
	h := newHarness(t)
	source, turn := startReviewTurn(t, h)
	turn.Status = store.TurnCompleted
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	logical, err := h.app.LogicalTurn(t.Context(), turn.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	message, err := h.app.MessagingMessage(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	if logical.Status != "completed" && message.Processing == "processed" {
		t.Fatalf("native completion reports processed with logical turn still %s", logical.Status)
	}
}

func TestCommunicationReadRejectsUnadmittedOwnerSources(t *testing.T) {
	for _, state := range []string{"queued", "withdrawn"} {
		t.Run(state, func(t *testing.T) {
			h := hostedManagerHarness(t)
			issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
			source := queueMessage(t, h, "Pending owner instruction")
			if op := h.associate(owner, source, issue.IssueID); op.Error != nil {
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
				if op := h.exec(owner, h.env("conversation.withdraw_message", protocol.Target{Type: "message", ID: source}, protocol.WithdrawMessagePayload{}, rev(control.Revision)), WithdrawMessage{}); op.Error != nil {
					t.Fatal(op)
				}
			}
			conversation := "issue:" + issue.IssueID
			d, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+conversation)
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			turn := startHostedReview(t, h, conversation, d)
			if _, err := h.app.ReadCommunicationInput(t.Context(), turn.RequestID, source); protocol.CodeOf(err) != protocol.CodeForbidden {
				t.Fatalf("%s source accepted: %v", state, err)
			}
			r, err := h.app.MessagingReceipts(t.Context(), source)
			if err != nil || len(r.Reads) != 0 {
				t.Fatal(r, err)
			}
		})
	}
}

func TestCommunicationManagerCannotPublishUnrelatedMainAttachment(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	source, d := h.say("Admitted source for managed Issue")
	startHostedReview(t, h, "main", d)
	if op := h.associate(owner, source, issue.IssueID); op.Error != nil {
		t.Fatal(op)
	}
	unrelated := queueMessage(t, h, "Private attachment outside this Issue")
	uploaded, err := h.app.UploadConversationAttachment(t.Context(), owner, h.ids.New(protocol.PrefixRequest), "private.txt", "text/plain", strings.NewReader("not part of Issue"))
	if err != nil || uploaded.Error != nil {
		t.Fatal(uploaded, err)
	}
	var file protocol.ConversationAttachment
	if err := json.Unmarshal(uploaded.Result, &file); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error { return tx.BindConversationAttachment(t.Context(), unrelated, 0, file.FileID) }); err != nil {
		t.Fatal(err)
	}
	conversation := "issue:" + issue.IssueID
	delivery, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+conversation)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	turn := startHostedReview(t, h, conversation, delivery)
	principal := protocol.Principal{Kind: protocol.PrincipalBackgroundAgent, Subject: "manager", Thread: turn.ThreadID, ConversationID: conversation}
	p := protocol.CoordinationReply{Body: "Here is a file", Purpose: "final", ReplyTo: source, Attachments: []string{file.FileID}, Disposition: "linked", ReviewedKinds: allFactKinds, WorkItemIDs: []string{issue.IssueID}, Contexts: []protocol.LogicalContextReview{{WorkItemID: issue.IssueID, Revision: h.agentContext(issue.IssueID).Revision}}}
	op, err := h.app.Execute(t.Context(), principal, h.env("conversation.reply", protocol.Target{Type: "logical_turn", ID: turn.RequestID}, p, nil), HostedCoordinationCommand{Command: CoordinationReply{}, ExecutionID: turn.RequestID, EffectSeq: 1})
	if protocol.CodeOf(err) != protocol.CodeForbidden && (op.Error == nil || op.Error.Code != protocol.CodeForbidden) {
		t.Fatalf("unrelated attachment allowed: %+v", op)
	}
}

func TestCommunicationNativeConsumptionCanPrecedeStartReceipt(t *testing.T) {
	h := newHarness(t)
	id, d := h.say("Consumed while native start is returning")
	turn := store.MainTurn{RequestID: d.RequestID, MessageID: id, ThreadID: mainAgent.Thread, Status: store.TurnStarting}
	if err := h.app.PutMainTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	if err := h.app.RememberMessagingNativeInput(t.Context(), turn.RequestID, id, "exact frozen native input"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.ReadCommunicationInput(t.Context(), turn.RequestID, id); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatalf("starting is not a read: %v", err)
	}
	if err := h.app.RecordMessagingConsumption(t.Context(), turn.RequestID, id, "native_input:actual-item"); err != nil {
		t.Fatal(err)
	}
	receipts, err := h.app.MessagingReceipts(t.Context(), id)
	if err != nil || len(receipts.Reads) != 1 {
		t.Fatal(receipts, err)
	}
}

func TestMessagingConsultationNotifyIssueScope(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	c := createConsultationFixture(t, h)
	source, turn, principal := consultationTurn(t, h, c, "Only explain this topic")
	before := h.agentContext(issue.IssueID).Revision
	p := protocol.MessagingPublication{SourceMessageIDs: []string{source}, IssueIDs: []string{issue.IssueID}, Body: "An unsolicited Issue association", Purpose: "progress", Reason: "consultation explanation"}
	op, rejection := h.app.Execute(t.Context(), principal, h.env("conversation.notify", protocol.Target{Type: "logical_turn", ID: turn.RequestID}, p, nil), HostedCoordinationCommand{Command: PublishCommunication{Notify: true}, ExecutionID: turn.RequestID, EffectSeq: 1})
	if protocol.CodeOf(rejection) != protocol.CodeForbidden && (op.Error == nil || op.Error.Code != protocol.CodeForbidden) {
		id := messageID(t, op)
		m, err := h.app.MessagingMessage(t.Context(), id)
		t.Fatalf("consultation mutated unrelated Issue: issueIDs=%v context revision=%d->%d err=%v", m.IssueIDs, before, h.agentContext(issue.IssueID).Revision, err)
	}
	if h.agentContext(issue.IssueID).Revision != before {
		t.Fatal("rejected consultation changed Issue context")
	}
}
func TestMessagingQuotedEditedInputUsesLatestVersion(t *testing.T) {
	h := newHarness(t)
	if err := h.app.ConfigureTelegram(t.Context(), 123); err != nil {
		t.Fatal(err)
	}
	h.exec(owner, h.env("messaging.bind", protocol.Target{Type: "channel", ID: "telegram"}, protocol.BindTelegram{UserID: 456}, rev(2)), MessagingCommand{Action: "bind"})
	next := func(n int64, m TelegramIncoming) string {
		raw, _ := json.Marshal(map[string]int64{"update_id": n})
		if err := h.app.PersistTelegramBatch(t.Context(), 123, []TelegramInbox{{UpdateID: n, Payload: raw}}); err != nil {
			t.Fatal(err)
		}
		u, ok, err := h.app.PendingTelegram(t.Context(), 123)
		if err != nil || !ok {
			t.Fatal(ok, err)
		}
		op, err := h.app.ReceiveTelegram(t.Context(), u, m)
		if err != nil || op.Error != nil {
			t.Fatal(op, err)
		}
		if m.Start {
			return ""
		}
		return messageID(t, op)
	}
	next(1, TelegramIncoming{UserID: 456, ChatID: 456, Start: true})
	first := next(2, TelegramIncoming{UserID: 456, ChatID: 456, ExternalID: 10, Input: protocol.MessagingInput{Body: "Use 100"}})
	edited := next(3, TelegramIncoming{UserID: 456, ChatID: 456, ExternalID: 10, Edited: true, Input: protocol.MessagingInput{Body: "Use 200"}})
	reply := next(4, TelegramIncoming{UserID: 456, ChatID: 456, ExternalID: 11, ReplyExternalID: 10, Input: protocol.MessagingInput{Body: "Proceed with this value"}})
	m, err := h.app.MessagingMessage(t.Context(), reply)
	if err != nil {
		t.Fatal(err)
	}
	if m.ReplyTo != edited {
		t.Fatalf("quote targets %s (original %s), want corrected input %s", m.ReplyTo, first, edited)
	}
}
func TestMessagingManagerPublishedTrace(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	turn, principal := startHostedIssueReview(t, h, issue)
	p := protocol.CoordinationReply{ReplyTo: turn.MessageID, Body: "A formal answer", Purpose: "final", Disposition: "linked", WorkItemIDs: []string{issue.IssueID}, Contexts: []protocol.LogicalContextReview{{WorkItemID: issue.IssueID, Revision: h.agentContext(issue.IssueID).Revision}}, ReviewedKinds: allFactKinds}
	id := messageID(t, hostedEffect(t, h, principal, turn, 1, protocol.Target{Type: "logical_turn", ID: turn.RequestID}, nil, p, CoordinationReply{}))
	trace, err := h.app.MessagingTrace(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(trace.Turns) == 0 {
		t.Fatalf("no originating turn for publication execution_id=%s (want %s)", trace.Message.ExecutionID, turn.RequestID)
	}
}
func TestMessagingFailedInputProcessing(t *testing.T) {
	h := newHarness(t)
	id, d := h.say("An input rejected before native start")
	if err := h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Failed, Reason: "definite pre-start failure"}); err != nil {
		t.Fatal(err)
	}
	m, err := h.app.MessagingMessage(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if m.Processing != "failed" {
		t.Fatalf("definitely failed input still reports %s", m.Processing)
	}

}

func TestMessagingManagerReplyToMainCreatesNoPendingObligation(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	source, d := h.say("Source in main")
	startHostedReview(t, h, "main", d)
	if op := h.associate(owner, source, issue.IssueID); op.Error != nil {
		t.Fatal(op)
	}
	conversation := "issue:" + issue.IssueID
	delivery, _, ok, err := h.app.NextDelivery(t.Context(), "background_thread:"+conversation)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	turn := startHostedReview(t, h, conversation, delivery)
	principal := protocol.Principal{Kind: protocol.PrincipalBackgroundAgent, Subject: "manager", Thread: turn.ThreadID, ConversationID: conversation}
	review := hostedEffect(t, h, principal, turn, 1, protocol.Target{Type: "issue", ID: issue.IssueID}, nil, hostedReviewPayload(t, h, issue), ReviewHostedIssueContext{})
	if review.Error != nil {
		t.Fatal(review)
	}
	p := protocol.CoordinationReply{Body: "An update", Purpose: "final", ReplyTo: source, Disposition: "linked", ReviewedKinds: allFactKinds, WorkItemIDs: []string{issue.IssueID}, Contexts: []protocol.LogicalContextReview{{WorkItemID: issue.IssueID, Revision: h.agentContext(issue.IssueID).Revision}}}
	op := hostedEffect(t, h, principal, turn, 2, protocol.Target{Type: "logical_turn", ID: turn.RequestID}, nil, p, CoordinationReply{})
	if op.Error != nil {
		t.Fatal(op)
	}
	var pending bool
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		pending, err = hostedIssueContextPending(t.Context(), tx.Q(), issue.IssueID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if pending {
		t.Fatal("formal reply created a pending context obligation after review")
	}
	if op := commitHostedReview(t, h, principal, turn, 3); op.Error != nil {
		t.Fatal("formal publication invalidated the reviewed turn", op)
	}
}
func TestMessagingDeadlineQuoteRoutesToIssue(t *testing.T) {
	h := hostedManagerHarness(t)
	h.app.followupsEnabled = true
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	d := putDeadlineForTest(t, h, "", nil, deadlinePayloadForTest(h, issue, 2*time.Hour))
	old := h.occurrences(d.NearRuleID)[0]
	page, err := h.app.MessagingMessages(t.Context(), 0, "", issue.IssueID, "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range page.Items {
		if item.MessageID == old.MessageID {
			found = true
		}
	}
	if !found {
		t.Fatal("Issue query omitted its deadline notification")
	}
	op := h.exec(owner, h.env("messaging.reply", protocol.Target{Type: "message", ID: old.MessageID}, protocol.MessagingInput{Body: "Please follow up this deadline"}, nil), MessagingCommand{Action: "reply"})
	if op.Error != nil {
		t.Fatal(op)
	}
	m, err := h.app.MessagingMessage(t.Context(), messageID(t, op))
	if err != nil {
		t.Fatal(err)
	}
	if m.ConversationID != "issue:"+issue.IssueID {
		t.Fatalf("routed to %q, wanted issue %q", m.ConversationID, issue.IssueID)
	}
}

func TestMessagingDecisionAttentionIsPublished(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	op := h.exec(owner, h.env("attentions.create", protocol.Target{Type: "attention"}, protocol.CreateAttentionPayload{Kind: "decision", IssueID: issue.IssueID, Title: "Choose the next step", Options: []string{"A", "B"}}, nil), AttentionCommand{Action: "create"})
	if op.Error != nil {
		t.Fatal(op)
	}
	page, err := h.app.MessagingMessages(t.Context(), 0, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) == 0 {
		t.Fatalf("created %d diagnostic conversation messages but no communication for decision Attention", h.count("conversation_messages"))
	}
}

func TestMessagingMainDecisionPublishesWithoutSelfWake(t *testing.T) {
	h := hostedManagerHarness(t)
	_, d := h.say("Clarify my goal")
	turn := startHostedReview(t, h, "main", d)
	count := func() int {
		var n int
		if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
			return tx.Q().QueryRowContext(t.Context(), `SELECT count(*) FROM outbox WHERE destination='main_thread'`).Scan(&n)
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := count()
	op := hostedEffect(t, h, mainAgent, turn, 1, protocol.Target{Type: "attention"}, nil, protocol.CreateAttentionPayload{Kind: "decision", Title: "Choose the next step", Options: []string{"A", "B"}}, AttentionCommand{Action: "create"})
	if op.Error != nil {
		t.Fatal(op)
	}
	page, err := h.app.MessagingMessages(t.Context(), 0, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, message := range page.Items {
		if strings.HasPrefix(message.Reason, "decision_opened:") {
			found = true
		}
	}
	if !found {
		t.Fatal("main's own decision was not published")
	}
	if count() != before {
		t.Fatal("publishing a decision woke main again")
	}
}

func TestMessagingWithdrawnIssueInput(t *testing.T) {
	h := newHarness(t)
	issue := createCanonicalOwnerTestIssue(t, h)
	id := queueMessage(t, h, "withdraw me")
	if op := h.associate(owner, id, issue.IssueID); op.Error != nil {
		t.Fatal(op)
	}
	var c protocol.MessageControl
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var e error
		c, e = store.MessageControl(t.Context(), tx.Q(), id)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	op := h.exec(owner, h.env("conversation.withdraw_message", protocol.Target{Type: "message", ID: id}, protocol.WithdrawMessagePayload{}, rev(c.Revision)), WithdrawMessage{})
	if op.Error != nil {
		t.Fatal(op)
	}
	m, err := h.app.MessagingMessage(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if m.Processing != "interrupted" {
		t.Fatalf("withdrawn input reports %s", m.Processing)
	}
}
func TestMessagingNamedMainNotifyContext(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	manager, principal := startHostedIssueReview(t, h, issue)
	reviewed := hostedEffect(t, h, principal, manager, 1, protocol.Target{Type: "issue", ID: issue.IssueID}, nil, hostedReviewPayload(t, h, issue), ReviewHostedIssueContext{})
	if reviewed.Error != nil {
		t.Fatal(reviewed)
	}
	if op := commitHostedReview(t, h, principal, manager, 2); op.Error != nil {
		t.Fatal("initial commit", op)
	}
	_, d := h.say("Request")
	turn := startHostedReview(t, h, "main", d)
	p := protocol.MessagingPublication{Body: "A user-facing update", Purpose: "progress", Reason: "verified progress", IssueIDs: []string{issue.IssueID}}
	op := hostedEffect(t, h, mainAgent, turn, 1, protocol.Target{Type: "logical_turn", ID: turn.RequestID}, nil, p, PublishCommunication{Notify: true})
	if op.Error != nil {
		t.Fatal(op)
	}
	id := messageID(t, op)
	var pending bool
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		pending, err = hostedIssueContextPending(t.Context(), tx.Q(), issue.IssueID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var obligations int
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT count(*) FROM message_context_obligations WHERE message_id=? AND needs_review=1`, id).Scan(&obligations)
	}); err != nil {
		t.Fatal(err)
	}
	var wakes int
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT count(*) FROM background_sources WHERE message_id=?`, id).Scan(&wakes)
	}); err != nil {
		t.Fatal(err)
	}
	if op := commitHostedReview(t, h, principal, manager, 3); op.Error != nil {
		t.Logf("new manager commit blocked: %v", op.Error)
	}
	if pending {
		t.Fatalf("display-only notification generated pending=%v obligations=%d manager_wakes=%d notification=%s", pending, obligations, wakes, id)
	}
}

func TestMessagingLongWorkerQuestion(t *testing.T) {
	h, as, _ := questionRun(t, protocol.RoleImplement)
	p := scopeQuestion
	p.Evidence = strings.Repeat("e", 65536)
	op := h.runAs(as, protocol.OpRunsAskOwner, p, AskOwner{})
	if op.Status != protocol.OpSucceeded {
		t.Fatalf("valid existing worker question rejected: status=%s error=%+v", op.Status, op.Error)
	}
}
func TestMessagingOwnerPanelAnswerIsCommunication(t *testing.T) {
	h, as, _ := questionRun(t, protocol.RoleImplement)
	q := askQuestion(t, h, as, scopeQuestion)
	if op := answerQuestion(t, h, q, "renderer only", "My answer from owner panel"); op.Status != protocol.OpSucceeded {
		t.Fatal(op)
	}
	var source string
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT answered_message_id FROM decisions WHERE decision_id=?`, q.DecisionID).Scan(&source)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.MessagingMessage(t.Context(), source); err != nil {
		t.Fatalf("owner answer source %s missing from communication: %v", source, err)
	}
}

func TestMessagingChoicePanelUsesSameCommunicationMessages(t *testing.T) {
	h := newHarness(t)
	w := h.createWork(workPlan())
	choice := h.askChoice(w, h.ownerEvidence().Reference)
	notice, err := h.app.MessagingMessage(t.Context(), choice.MessageID)
	if err != nil || notice.Direction != "notification" {
		t.Fatal(notice, err)
	}
	env := h.env("work_item_choices.respond", protocol.Target{Type: "work_item_choice", ID: choice.ChoiceID}, protocol.UpdateWorkItemChoicePayload{Answer: "Later"}, rev(choice.Version))
	first := h.exec(owner, env, UpdateWorkItemChoice{Action: "respond"})
	second := h.exec(owner, env, UpdateWorkItemChoice{Action: "respond"})
	if first.Error != nil || string(first.Result) != string(second.Result) {
		t.Fatal(first, second)
	}
	var answered protocol.WorkItemChoice
	if err := json.Unmarshal(first.Result, &answered); err != nil {
		t.Fatal(err)
	}
	source, err := h.app.MessagingMessage(t.Context(), answered.AnswerSourceID)
	if err != nil || source.Direction != "input" || source.ChannelID != "web" || source.ReplyTo != choice.MessageID {
		t.Fatal(source, err)
	}
	receipt, err := h.app.MessagingReceipts(t.Context(), source.MessageID)
	if err != nil || len(receipt.Reads) != 0 {
		t.Fatal("business answer is not native consumption", receipt, err)
	}
}

func TestMessagingFormalReplyCannotTargetOwnPublication(t *testing.T) {
	h := newHarness(t)
	source, turn := startReviewTurn(t, h)
	p := protocol.CoordinationReply{ReplyTo: source, Body: "Progress", Purpose: "progress", Disposition: "unassigned", ReviewedKinds: allFactKinds}
	env := h.env("conversation.reply", protocol.Target{Type: "logical_turn", ID: turn.RequestID}, p, nil)
	env.ParentRequestID = turn.RequestID
	published := messageID(t, h.exec(mainAgent, env, CoordinationReply{}))
	count := h.count("messaging_messages")
	p.ReplyTo = published
	retry := h.env("conversation.reply", protocol.Target{Type: "logical_turn", ID: turn.RequestID}, p, nil)
	retry.ParentRequestID = turn.RequestID
	op, err := h.app.Execute(t.Context(), mainAgent, retry, CoordinationReply{})
	if protocol.CodeOf(err) != protocol.CodeInvalidRequest && (op.Error == nil || op.Error.Code != protocol.CodeInvalidRequest) {
		t.Fatal(op, err)
	}
	if h.count("messaging_messages") != count {
		t.Fatal("invalid target created another publication")
	}
}
