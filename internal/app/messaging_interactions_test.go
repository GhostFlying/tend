package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func imQuestion(t *testing.T, h *harness, object string) protocol.MessagingMessage {
	t.Helper()
	page, err := h.app.MessagingMessages(t.Context(), 0, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range page.Items {
		if m.Interaction != nil && m.Interaction.ObjectID == object {
			return m
		}
	}
	t.Fatalf("missing interaction for %s: %+v", object, page)
	return protocol.MessagingMessage{}
}
func bindIMTelegram(t *testing.T, h *harness) {
	t.Helper()
	if err := h.app.ConfigureTelegram(t.Context(), 123); err != nil {
		t.Fatal(err)
	}
	h.exec(owner, h.env("messaging.bind", protocol.Target{Type: "channel", ID: "telegram"}, protocol.BindTelegram{UserID: 456}, rev(2)), MessagingCommand{Action: "bind"})
	if err := h.app.PersistTelegramBatch(t.Context(), 123, []TelegramInbox{{UpdateID: 1, Payload: json.RawMessage(`{"update_id":1}`)}}); err != nil {
		t.Fatal(err)
	}
	u, _, err := h.app.PendingTelegram(t.Context(), 123)
	if err != nil {
		t.Fatal(err)
	}
	op, err := h.app.ReceiveTelegram(t.Context(), u, TelegramIncoming{UserID: 456, ChatID: 456, Start: true})
	if err != nil || op.Error != nil {
		t.Fatal(op, err)
	}
}
func TestIMAnswerWebInvalidatesTelegramWithoutRepeatingEffect(t *testing.T) {
	h := newHarness(t)
	bindIMTelegram(t, h)
	task, d := h.failImplementation()
	m := imQuestion(t, h, d.DecisionID)
	delivery, _, ok, err := h.app.NextDelivery(t.Context(), MessagingDestination)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err = h.app.FinishDelivery(t.Context(), delivery, DeliveryResult{Outcome: Delivered, ExternalMessageID: 99}); err != nil {
		t.Fatal(err)
	}
	env := h.env("messaging.respond", protocol.Target{Type: "interaction", ID: m.Interaction.ID}, protocol.RespondInteraction{Version: m.Interaction.Version, Option: "retry the implementation"}, nil)
	op := h.exec(owner, env, RespondMessagingInteraction{})
	if op.Error != nil {
		t.Fatal(op)
	}
	current := imQuestion(t, h, d.DecisionID).Interaction
	if current.State != "answered" || current.HandledChannel != "web" || current.HandledAt == 0 || current.Effect == nil || !current.Effect.Applied {
		t.Fatalf("%+v", current)
	}
	if h.task(task).ImplementGeneration != 2 {
		t.Fatal("retry was not applied")
	}
	update, _, ok, err := h.app.NextDelivery(t.Context(), MessagingInteractionDestination)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	var p MessagingPart
	_ = json.Unmarshal(update.Payload, &p)
	if p.ReplyExternalID != 99 || p.Kind != "interaction" {
		t.Fatal(p)
	}
	if !strings.Contains(interactionText(*current), "已在 Web 处理") {
		t.Fatal(interactionText(*current))
	}
	// A conflicting later choice gets the accepted answer, not a second effect.
	later := h.exec(owner, h.env("messaging.respond", env.Target, protocol.RespondInteraction{Version: m.Interaction.Version, Option: "cancel the task"}, nil), RespondMessagingInteraction{})
	if later.Error != nil || h.task(task).Lifecycle == protocol.TaskCancelled || h.task(task).ImplementGeneration != 2 {
		t.Fatal(later, h.task(task))
	}
	replay := h.exec(owner, env, RespondMessagingInteraction{})
	if string(replay.Result) != string(op.Result) {
		t.Fatal("request replay changed")
	}
	if _, _, found, err := h.app.NextDelivery(t.Context(), MessagingInteractionDestination); err != nil || found {
		t.Fatal("closed callbacks duplicated the same presentation update", found, err)
	}
}
func TestIMQuotedChoiceAndExtraRequirementsHaveDifferentEffects(t *testing.T) {
	for _, extra := range []bool{false, true} {
		t.Run(map[bool]string{false: "choice", true: "new requirements"}[extra], func(t *testing.T) {
			h := newHarness(t)
			task, d := h.failImplementation()
			m := imQuestion(t, h, d.DecisionID)
			text := "重新实现"
			if extra {
				text = "先补上兼容性检查，再重新实现"
			}
			op := h.exec(owner, h.env("messaging.reply", protocol.Target{Type: "message", ID: m.MessageID}, protocol.MessagingInput{Body: text}, nil), MessagingCommand{Action: "reply"})
			if op.Error != nil {
				t.Fatal(op)
			}
			current := imQuestion(t, h, d.DecisionID).Interaction
			if extra {
				if current.State != "open" || h.task(task).ImplementGeneration != 1 {
					t.Fatal("extra requirements silently executed", current)
				}
				message, err := h.app.MessagingMessage(t.Context(), messageID(t, op))
				if err != nil || message.Body != text {
					t.Fatal(message, err)
				}
			} else if current.State != "answered" || h.task(task).ImplementGeneration != 2 {
				t.Fatal(current, h.task(task))
			}
		})
	}
}
func TestIMTelegramCallbackUpdatesWebAndAuthenticatesQuestion(t *testing.T) {
	h := newHarness(t)
	bindIMTelegram(t, h)
	task, d := h.failImplementation()
	m := imQuestion(t, h, d.DecisionID)
	delivery, _, ok, err := h.app.NextDelivery(t.Context(), MessagingDestination)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err = h.app.FinishDelivery(t.Context(), delivery, DeliveryResult{Outcome: Delivered, ExternalMessageID: 99}); err != nil {
		t.Fatal(err)
	}
	if err = h.app.PersistTelegramBatch(t.Context(), 123, []TelegramInbox{{UpdateID: 2, Payload: json.RawMessage(`{"update_id":2}`)}}); err != nil {
		t.Fatal(err)
	}
	u, _, err := h.app.PendingTelegram(t.Context(), 123)
	if err != nil {
		t.Fatal(err)
	}
	data := "t:" + m.Interaction.ID + ":0"
	bad, err := h.app.RespondTelegramInteraction(t.Context(), u, 456, 456, 100, data)
	if err != nil || bad.Error == nil {
		t.Fatal("foreign message accepted", bad, err)
	}
	// New update identity, since the invalid attempt is a recorded rejection.
	if err = h.app.PersistTelegramBatch(t.Context(), 123, []TelegramInbox{{UpdateID: 3, Payload: json.RawMessage(`{"update_id":3}`)}}); err != nil {
		t.Fatal(err)
	}
	if err = h.app.SettleTelegramInput(t.Context(), u, "test_rejected"); err != nil {
		t.Fatal(err)
	}
	u, _, _ = h.app.PendingTelegram(t.Context(), 123)
	op, err := h.app.RespondTelegramInteraction(t.Context(), u, 456, 456, 99, data)
	if err != nil || op.Error != nil {
		t.Fatal(op, err)
	}
	current := imQuestion(t, h, d.DecisionID).Interaction
	if current.State != "answered" || current.HandledChannel != "telegram" || h.task(task).ImplementGeneration != 2 {
		t.Fatal(current, h.task(task))
	}
	page, err := h.app.MessagingMessages(t.Context(), 0, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range page.Items {
		if v.Direction == "input" && v.Author == "owner" && v.Body == "retry the implementation" && v.ChannelID != "telegram" {
			t.Fatal("callback source was forged as Web", v)
		}
	}
}
func TestIMHumanActionReadIsNotCompletion(t *testing.T) {
	h := newHarness(t)
	a := attentionForTest(t, h, protocol.CreateAttentionPayload{Kind: "action", Title: "检查报告"})
	m := imQuestion(t, h, a.AttentionID)
	h.exec(owner, h.env("attentions.present", protocol.Target{Type: "attention", ID: a.AttentionID}, protocol.PresentAttentionPayload{Read: func() *bool { v := true; return &v }()}, rev(a.Revision)), AttentionCommand{Action: "present"})
	h.exec(owner, h.env("messaging.reply", protocol.Target{Type: "message", ID: m.MessageID}, protocol.MessagingInput{Body: "我读了"}, nil), MessagingCommand{Action: "reply"})
	current := imQuestion(t, h, a.AttentionID).Interaction
	if current.State != "open" {
		t.Fatal("reading resolved or superseded action", current)
	}
	op := h.exec(owner, h.env("messaging.respond", protocol.Target{Type: "interaction", ID: m.Interaction.ID}, protocol.RespondInteraction{Version: m.Interaction.Version, Confirm: true, Text: "已打开并核对报告，内容无误"}, nil), RespondMessagingInteraction{})
	if op.Error != nil || readAttentionForTest(t, h, a.AttentionID).Status != "resolved" {
		t.Fatal(op)
	}
}
func TestIMQuestionRevisionRetainsOriginalAndRejectsStaleAction(t *testing.T) {
	h := newHarness(t)
	a := attentionForTest(t, h, protocol.CreateAttentionPayload{Kind: "decision", Title: "检查什么？", Options: []string{"手机", "桌面"}})
	old := imQuestion(t, h, a.DecisionID)
	op := h.exec(owner, h.env("attentions.update", protocol.Target{Type: "attention", ID: a.AttentionID}, protocol.UpdateAttentionPayload{Title: "选择新的范围", Options: []string{"报告", "截图"}, DecisionVersion: 1}, rev(a.Revision)), AttentionCommand{Action: "update"})
	if op.Error != nil {
		t.Fatal(op)
	}
	before, err := h.app.MessagingMessage(t.Context(), old.MessageID)
	if err != nil || before.Interaction.State != "superseded" || before.Interaction.Question != "检查什么？" {
		t.Fatal(before, err)
	}
	op = h.exec(owner, h.env("messaging.respond", protocol.Target{Type: "interaction", ID: old.Interaction.ID}, protocol.RespondInteraction{Version: old.Interaction.Version, Option: "手机"}, nil), RespondMessagingInteraction{})
	if op.Error != nil {
		t.Fatal(op)
	}
	var d store.DecisionRow
	_ = h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		d, err = store.GetDecision(t.Context(), tx.Q(), a.DecisionID)
		return err
	})
	if d.Status != "open" || d.Question != "选择新的范围" {
		t.Fatal(d)
	}
}

func TestIMRevisedActionStaysSupersededAfterCurrentActionCompletes(t *testing.T) {
	h := newHarness(t)
	a := attentionForTest(t, h, protocol.CreateAttentionPayload{Kind: "action", Title: "检查原报告"})
	old := imQuestion(t, h, a.AttentionID)
	op := h.exec(owner, h.env("attentions.update", protocol.Target{Type: "attention", ID: a.AttentionID}, protocol.UpdateAttentionPayload{Title: "检查更新后的报告"}, rev(a.Revision)), AttentionCommand{Action: "update"})
	if op.Error != nil {
		t.Fatal(op)
	}
	page, err := h.app.MessagingMessages(t.Context(), 0, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	var current *protocol.MessagingInteraction
	for _, m := range page.Items {
		if m.Interaction != nil && m.Interaction.ObjectID == a.AttentionID && m.Interaction.State == "open" {
			current = m.Interaction
		}
	}
	if current == nil {
		t.Fatal("missing new question")
	}
	op = h.exec(owner, h.env("messaging.respond", protocol.Target{Type: "interaction", ID: current.ID}, protocol.RespondInteraction{Version: current.Version, Confirm: true, Text: "已检查更新后的报告"}, nil), RespondMessagingInteraction{})
	if op.Error != nil {
		t.Fatal(op)
	}
	original, err := h.app.MessagingMessage(t.Context(), old.MessageID)
	if err != nil || original.Interaction.State != "superseded" {
		t.Fatal(original, err)
	}
}

func TestIMNumericLiteralBeforeOptionNumber(t *testing.T) {
	h := newHarness(t)
	a := attentionForTest(t, h, protocol.CreateAttentionPayload{Kind: "decision", Title: "Select a value", Options: []string{"2", "3"}})
	m := imQuestion(t, h, a.DecisionID)
	op := h.exec(owner, h.env("messaging.reply", protocol.Target{Type: "message", ID: m.MessageID}, protocol.MessagingInput{Body: "2"}, nil), MessagingCommand{Action: "reply"})
	if op.Error != nil {
		t.Fatal(op)
	}
	got := imQuestion(t, h, a.DecisionID).Interaction
	if got.AnswerSummary != "2" {
		t.Fatalf("quoted exact option 2 was recorded as %q", got.AnswerSummary)
	}
}
func TestIMAcceptedCompletionProcessing(t *testing.T) {
	h := newHarness(t)
	a := attentionForTest(t, h, protocol.CreateAttentionPayload{Kind: "action", Title: "Check report"})
	m := imQuestion(t, h, a.AttentionID)
	op := h.exec(owner, h.env("messaging.respond", protocol.Target{Type: "interaction", ID: m.Interaction.ID}, protocol.RespondInteraction{Version: m.Interaction.Version, Confirm: true, Text: "Checked the report"}, nil), RespondMessagingInteraction{})
	if op.Error != nil {
		t.Fatal(op)
	}
	answer, err := h.app.MessagingMessage(t.Context(), messageID(t, op))
	if err != nil {
		t.Fatal(err)
	}
	if answer.Processing != "processed" {
		t.Fatalf("accepted completion displays %q", answer.Processing)
	}
}
func TestIMWorkerQuestionRetainsEvidence(t *testing.T) {
	h, as, _ := questionRun(t, protocol.RoleImplement)
	q := askQuestion(t, h, as, scopeQuestion)
	m := imQuestion(t, h, q.DecisionID)
	if !strings.Contains(m.Body, scopeQuestion.Evidence) {
		t.Fatal("fixture missing evidence")
	}
	if !strings.Contains(MessagingInteractionText(*m.Interaction), scopeQuestion.Evidence) {
		t.Fatal("Telegram interaction replaced evidence-bearing notice with bare question")
	}
}
func TestIMLateTelegramTextRetained(t *testing.T) {
	h := newHarness(t)
	bindIMTelegram(t, h)
	_, d := h.failImplementation()
	m := imQuestion(t, h, d.DecisionID)
	delivery, _, ok, err := h.app.NextDelivery(t.Context(), MessagingDestination)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err = h.app.FinishDelivery(t.Context(), delivery, DeliveryResult{Outcome: Delivered, ExternalMessageID: 99}); err != nil {
		t.Fatal(err)
	}
	op := h.exec(owner, h.env("messaging.respond", protocol.Target{Type: "interaction", ID: m.Interaction.ID}, protocol.RespondInteraction{Version: m.Interaction.Version, Option: "retry the implementation"}, nil), RespondMessagingInteraction{})
	if op.Error != nil {
		t.Fatal(op)
	}
	if err = h.app.PersistTelegramBatch(t.Context(), 123, []TelegramInbox{{UpdateID: 2, Payload: json.RawMessage(`{"update_id":2}`)}}); err != nil {
		t.Fatal(err)
	}
	u, _, _ := h.app.PendingTelegram(t.Context(), 123)
	text := "Please wait for my confirmation before continuing"
	op, err = h.app.ReceiveTelegram(t.Context(), u, TelegramIncoming{UserID: 456, ChatID: 456, ExternalID: 100, ReplyExternalID: 99, Input: protocol.MessagingInput{Body: text}})
	if err != nil || op.Error != nil {
		t.Fatal(op, err)
	}
	page, err := h.app.MessagingMessages(t.Context(), 0, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range page.Items {
		if m.Body == text {
			return
		}
	}
	t.Fatalf("successful Telegram input was not admitted: %s", op.Result)
}
func TestReviewStructuredQuestionContext(t *testing.T) {
	h, as, _ := questionRun(t, protocol.RoleImplement)
	p := scopeQuestion
	p.Evidence = ""
	p.Issues = []protocol.DecisionIssue{{IssueID: "authorization", Category: "authorization", Summary: "Permission is required to change the loader", Impact: "This changes the public API", NextAction: "Approve expanding the scope", Actor: "owner", Status: "open", Source: "spec requirement"}}
	q := askQuestion(t, h, as, p)
	m := imQuestion(t, h, q.DecisionID)
	if !strings.Contains(m.Body, p.Issues[0].Impact) {
		t.Fatal("fixture missing context")
	}
	if !strings.Contains(MessagingInteractionText(*m.Interaction), p.Issues[0].Impact) {
		t.Fatal("interaction drops structured impact and next action")
	}
}

func TestIMQuotedAnswerPreservesOwnerText(t *testing.T) {
	h := newHarness(t)
	_, d := h.failImplementation()
	m := imQuestion(t, h, d.DecisionID)
	op := h.exec(owner, h.env("messaging.reply", protocol.Target{Type: "message", ID: m.MessageID}, protocol.MessagingInput{Body: "1"}, nil), MessagingCommand{Action: "reply"})
	if op.Error != nil {
		t.Fatal(op.Error)
	}
	answer, err := h.app.MessagingMessage(t.Context(), messageID(t, op))
	if err != nil {
		t.Fatal(err)
	}
	if answer.Body != "1" {
		t.Fatalf("owner's raw answer was replaced: got %q, want %q", answer.Body, "1")
	}
}

func TestIMStartupDiagnosticRemainsVisible(t *testing.T) {
	h := newHarness(t)
	h.createTask(owner, createTaskPayload{Title: "startup notice", Spec: testSpec})
	as, _, _ := h.claim("linux-1")
	h.runAs(as, protocol.OpRunsReportStarted, nil, ReportRunStarted{})
	_, err := h.app.RecordRunEvents(t.Context(), runPrincipal(as.RunID, as.Role), as.RunID, []protocol.RunEvent{{Seq: 1, Kind: "worker.startup_failed", Body: json.RawMessage(`{"attempts":1,"diagnostic":{"code":"readiness_timeout","phase":"readiness"}}`)}})
	if err != nil {
		t.Fatal(err)
	}
	h.runAs(as, protocol.OpRunsRelease, protocol.ReleasePayload{Reason: "worker_start_failed"}, Release{})
	page, err := h.app.MessagingMessages(t.Context(), 0, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range page.Items {
		if m.Interaction != nil && strings.Contains(m.Body, "等待 CLI 就绪超时") {
			shown := MessagingInteractionText(*m.Interaction)
			if !strings.Contains(shown, "等待 CLI 就绪超时") {
				t.Fatalf("safe diagnosis hidden by interaction: raw=%q; shown=%q", m.Body, shown)
			}
			return
		}
	}
	t.Fatal("no diagnostic question")
}

func TestIMAcceptsBoundedCompletionSummary(t *testing.T) {
	h := newHarness(t)
	a := attentionForTest(t, h, protocol.CreateAttentionPayload{Kind: "action", Title: "Check the report"})
	m := imQuestion(t, h, a.AttentionID)
	op := h.exec(owner, h.env("messaging.respond", protocol.Target{Type: "interaction", ID: m.Interaction.ID}, protocol.RespondInteraction{Version: m.Interaction.Version, Confirm: true, Text: strings.Repeat("a", maxMessageBytes)}, nil), RespondMessagingInteraction{})
	if op.Error != nil {
		t.Fatalf("valid completion within maxMessageBytes was rolled back: %v", op.Error)
	}
}

func TestIMForceReplyAttachmentUsesCurrentResponsibility(t *testing.T) {
	h := hostedManagerHarness(t)
	bindIMTelegram(t, h)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	a := attentionForTest(t, h, protocol.CreateAttentionPayload{Kind: "decision", Title: "Which evidence?", IssueID: issue.IssueID, Options: []string{"A", "B"}})
	m := imQuestion(t, h, a.DecisionID)
	deliver, _, ok, err := h.app.NextDelivery(t.Context(), MessagingDestination)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err = h.app.FinishDelivery(t.Context(), deliver, DeliveryResult{Outcome: Delivered, ExternalMessageID: 99}); err != nil {
		t.Fatal(err)
	}
	if err = h.app.PersistTelegramBatch(t.Context(), 123, []TelegramInbox{{UpdateID: 2, Payload: json.RawMessage(`{"update_id":2}`)}}); err != nil {
		t.Fatal(err)
	}
	u, _, _ := h.app.PendingTelegram(t.Context(), 123)
	op, err := h.app.RespondTelegramInteraction(t.Context(), u, 456, 456, 99, "t:"+m.Interaction.ID+":text")
	if err != nil || op.Error != nil {
		t.Fatal(op, err)
	}
	promptID := messageID(t, op)
	var routed string
	err = h.store.Write(t.Context(), func(tx *store.Tx) error {
		var e error
		routed, e = communicationRoute(t.Context(), &Exec{Now: h.clock.Now()}, tx, promptID)
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	if routed != "issue:"+issue.IssueID {
		t.Errorf("ForceReply source routes to %q, want current manager %q", routed, "issue:"+issue.IssueID)
	}
	// Attachments bypass the interaction text responder and use this route.
	file := uploadChatFile(t, h, "evidence.txt", "text/plain", []byte("new evidence"))
	promptDelivery, _, ok, err := h.app.NextDelivery(t.Context(), MessagingDestination)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err = h.app.FinishDelivery(t.Context(), promptDelivery, DeliveryResult{Outcome: Delivered, ExternalMessageID: 100}); err != nil {
		t.Fatal(err)
	}
	if err = h.app.PersistTelegramBatch(t.Context(), 123, []TelegramInbox{{UpdateID: 3, Payload: json.RawMessage(`{"update_id":3}`)}}); err != nil {
		t.Fatal(err)
	}
	u, _, _ = h.app.PendingTelegram(t.Context(), 123)
	op, err = h.app.ReceiveTelegram(t.Context(), u, TelegramIncoming{UserID: 456, ChatID: 456, ExternalID: 101, ReplyExternalID: 100, Input: protocol.MessagingInput{Body: "Please use this evidence", Attachments: []string{file.FileID}}})
	if err != nil {
		t.Fatal(err)
	}
	if op.Error != nil {
		t.Fatal(op)
	}
	got, err := h.app.MessagingMessage(t.Context(), messageID(t, op))
	if err != nil {
		t.Fatal(err)
	}
	if got.ConversationID != "issue:"+issue.IssueID {
		t.Errorf("attachment reply admitted to %q, want current Issue manager; message=%+v", got.ConversationID, got)
	}
}
