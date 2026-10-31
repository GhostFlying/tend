package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func messageID(t *testing.T, op protocol.Operation) string {
	t.Helper()
	if op.Error != nil {
		t.Fatal(op.Error)
	}
	var v struct {
		ID string `json:"message_id"`
	}
	if err := json.Unmarshal(op.Result, &v); err != nil || v.ID == "" {
		t.Fatal(string(op.Result), err)
	}
	return v.ID
}
func TestMessagingPublicationAndLegacySeparation(t *testing.T) {
	h := newHarness(t)
	env := h.env("messaging.send", protocol.Target{Type: "message"}, protocol.MessagingInput{Body: "Please investigate"}, nil)
	op := h.exec(owner, env, MessagingCommand{Action: "send"})
	id := messageID(t, op)
	page, err := h.app.MessagingMessages(t.Context(), 0, "", "", "")
	if err != nil || len(page.Items) != 1 || page.Items[0].MessageID != id {
		t.Fatal(page, err)
	}
	replay := h.exec(owner, env, MessagingCommand{Action: "send"})
	if string(replay.Result) != string(op.Result) || h.count("messaging_messages") != 1 {
		t.Fatal("duplicate input")
	}
	// Native narration remains available through legacy history only.
	if err = h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.InsertMessage(t.Context(), store.MessageRow{MessageID: h.ids.New(protocol.PrefixMessage), Author: "main_agent", Body: "Internal narration", CreatedAt: h.clock.Now().UnixMilli()})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	page, err = h.app.MessagingMessages(t.Context(), 0, "", "", "")
	if err != nil || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	pub := h.exec(owner, h.env("messaging.notify", protocol.Target{Type: "message"}, protocol.MessagingPublication{Body: "Result ready", Purpose: "final", Reason: "verified_result"}, nil), MessagingCommand{Action: "notify"})
	result := messageID(t, pub)
	receipts, err := h.app.MessagingReceipts(t.Context(), result)
	if err != nil || len(receipts.Deliveries) != 2 {
		t.Fatal(receipts, err)
	}
	for _, d := range receipts.Deliveries {
		if d.ChannelID == "web" && d.State != "accepted" || d.ChannelID == "telegram" && d.State != "unavailable" {
			t.Fatal(d)
		}
	}
}
func TestMessagingReadRequiresConsumptionAndPublishesBeforeCompletion(t *testing.T) {
	h := newHarness(t)
	source, turn := startReviewTurn(t, h)
	before, err := h.app.MessagingReceipts(t.Context(), source)
	if err != nil || len(before.Reads) != 0 {
		t.Fatal(before, err)
	}
	if err = h.app.RecordMessagingConsumption(t.Context(), turn.RequestID, source, "native_input:input-1"); err != nil {
		t.Fatal(err)
	}
	after, err := h.app.MessagingReceipts(t.Context(), source)
	if err != nil || len(after.Reads) != 1 {
		t.Fatal(after, err)
	}
	if err = h.app.RecordMessagingConsumption(t.Context(), turn.RequestID, source, "native_input:input-1"); err != nil {
		t.Fatal(err)
	}
	if h.count("messaging_reads") != 1 {
		t.Fatal("duplicate read")
	}
	state, err := h.app.LogicalTurn(t.Context(), turn.RequestID)
	if err != nil || state.Status != "processing" {
		t.Fatal(state, err)
	}
	m, err := h.app.MessagingMessage(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	read := h.exec(owner, h.env("messaging.read", protocol.Target{Type: "message", ID: source}, protocol.MessagingReadPayload{ContentRevision: m.ContentRevision + 1}, nil), MessagingCommand{Action: "read"})
	if read.Error == nil || read.Error.Code != protocol.CodeStaleRevision {
		t.Fatal(read)
	}
}
func TestMessagingTelegramDurableIdentityAndQuotedReplies(t *testing.T) {
	h := newHarness(t)
	if err := h.app.ConfigureTelegram(t.Context(), 123); err != nil {
		t.Fatal(err)
	}
	h.exec(owner, h.env("messaging.bind", protocol.Target{Type: "channel", ID: "telegram"}, protocol.BindTelegram{UserID: 456}, rev(2)), MessagingCommand{Action: "bind"})
	makeInput := func(update int64, m TelegramIncoming) string {
		t.Helper()
		raw := json.RawMessage(`{"update_id":` + strings.Repeat(" ", 0) + "1}")
		if err := h.app.PersistTelegramBatch(t.Context(), 123, []TelegramInbox{{UpdateID: update, Payload: raw}}); err != nil {
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
	makeInput(1, TelegramIncoming{UserID: 456, ChatID: 456, Start: true})
	id := makeInput(2, TelegramIncoming{UserID: 456, ChatID: 456, ExternalID: 55, Input: protocol.MessagingInput{Body: "Hello"}})
	if offset, err := h.app.TelegramOffset(t.Context(), 123); err != nil || offset != 3 {
		t.Fatal(offset, err)
	}
	if err := h.app.PersistTelegramBatch(t.Context(), 123, []TelegramInbox{{UpdateID: 2, Payload: json.RawMessage(`{"update_id":2}`)}}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := h.app.PendingTelegram(t.Context(), 123); err != nil || ok {
		t.Fatal("duplicate update returned", ok, err)
	}
	_, turn := startReviewTurn(t, h)
	// This different active turn may not publish a response to unadmitted input.
	env := h.env("conversation.publish", protocol.Target{Type: "logical_turn", ID: turn.RequestID}, protocol.MessagingPublication{ReplyTo: id, Body: "Should not send", Purpose: "final"}, nil)
	env.ParentRequestID = turn.RequestID
	op, err := h.app.Execute(t.Context(), mainAgent, env, PublishCommunication{})
	if err == nil && op.Error == nil {
		t.Fatal("unadmitted reply was published")
	}
}
func TestMessagingUnicodeParts(t *testing.T) {
	parts := splitCommunication(strings.Repeat("🙂", 2100), 4000)
	if len(parts) != 2 || len([]rune(parts[0])) != 2000 || strings.Join(parts, "") != strings.Repeat("🙂", 2100) {
		t.Fatal("invalid UTF-16 splitting")
	}
}

func TestMessagingReadSyncIsIndependentAndContiguous(t *testing.T) {
	h := newHarness(t)
	if err := h.app.ConfigureTelegram(t.Context(), 123); err != nil {
		t.Fatal(err)
	}
	h.exec(owner, h.env("messaging.bind", protocol.Target{Type: "channel", ID: "telegram"}, protocol.BindTelegram{UserID: 456}, rev(2)), MessagingCommand{Action: "bind"})
	next := func(id int64, m TelegramIncoming) string {
		t.Helper()
		raw, _ := json.Marshal(map[string]int64{"update_id": id})
		if err := h.app.PersistTelegramBatch(t.Context(), 123, []TelegramInbox{{UpdateID: id, Payload: raw}}); err != nil {
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
	first := next(2, TelegramIncoming{UserID: 456, ChatID: 456, ExternalID: 10, Input: protocol.MessagingInput{Body: "First"}})
	second := next(3, TelegramIncoming{UserID: 456, ChatID: 456, ExternalID: 11, Input: protocol.MessagingInput{Body: "Second"}})
	if err := h.app.ConfigureMessagingReadCapability(t.Context(), "telegram", "cursor"); err != nil {
		t.Fatal(err)
	}
	read := func(id, execution string) {
		t.Helper()
		err := h.store.Write(t.Context(), func(tx *store.Tx) error {
			x := &Exec{IDs: h.ids, Now: h.clock.Now(), Env: &protocol.Envelope{RequestID: execution}}
			return recordCommunicationRead(t.Context(), x, tx, id, 1, "agent", "agent", "source_read_tool", execution)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	read(second, "later-execution")
	if _, _, ok, err := h.app.NextDelivery(t.Context(), MessagingReadDestination); err != nil || ok {
		t.Fatal("cursor skipped unread input", ok, err)
	}
	read(first, "earlier-execution")
	d, _, ok, err := h.app.NextDelivery(t.Context(), MessagingReadDestination)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err = h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Failed, Reason: "read_sync_failed"}); err != nil {
		t.Fatal(err)
	}
	receipts, err := h.app.MessagingReceipts(t.Context(), first)
	if err != nil || len(receipts.Reads) != 1 {
		t.Fatal(receipts, err)
	}
	if receipts.Deliveries[0].State != "failed" {
		t.Fatal(receipts)
	}
	// The other read-control operation remains independent of ordinary sending.
	if _, _, ok, err = h.app.NextDelivery(t.Context(), MessagingReadDestination); err != nil || !ok {
		t.Fatal(ok, err)
	}
}

func TestMessagingUnknownDeliveryDoesNotReplay(t *testing.T) {
	h := newHarness(t)
	if err := h.app.ConfigureTelegram(t.Context(), 123); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE messaging_channels SET owner=?,user_id=456,chat_id=456,status='ready' WHERE channel_id='telegram'`, owner.Subject)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	op := h.exec(owner, h.env("messaging.notify", protocol.Target{Type: "message"}, protocol.MessagingPublication{Body: "Only once", Purpose: "final", Reason: "verified_result"}, nil), MessagingCommand{Action: "notify"})
	id := messageID(t, op)
	d, _, ok, err := h.app.NextDelivery(t.Context(), MessagingDestination)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err = h.app.FinishDelivery(t.Context(), d, DeliveryResult{Outcome: Unknown, Reason: "send_outcome_unknown"}); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, err = h.app.NextDelivery(t.Context(), MessagingDestination); err != nil || ok {
		t.Fatal("unknown was replayed", ok, err)
	}
	receipts, err := h.app.MessagingReceipts(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(receipts.Deliveries) != 2 || receipts.Deliveries[0].State != "unknown" {
		t.Fatal(receipts)
	}
}

func TestMessagingQuotedMainInputRoutesDirectlyToIssue(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	source, _ := h.say("Original goal in the main conversation")
	if op := h.associate(owner, source, issue.IssueID); op.Error != nil {
		t.Fatal(op)
	}
	before := 0
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT count(*) FROM outbox WHERE destination='main_thread'`).Scan(&before)
	}); err != nil {
		t.Fatal(err)
	}
	op := h.exec(owner, h.env("messaging.reply", protocol.Target{Type: "message", ID: source}, protocol.MessagingInput{Body: "Please continue this issue"}, nil), MessagingCommand{Action: "reply"})
	id := messageID(t, op)
	m, err := h.app.MessagingMessage(t.Context(), id)
	if err != nil || m.ConversationID != "issue:"+issue.IssueID || m.ReplyTo != source {
		t.Fatal(m, err)
	}
	var after int
	if err := h.store.Read(t.Context(), func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(t.Context(), `SELECT count(*) FROM outbox WHERE destination='main_thread'`).Scan(&after)
	}); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatal("main agent was unnecessarily woken")
	}
}

func TestMessagingManagerReplyRequiresExactContext(t *testing.T) {
	h := hostedManagerHarness(t)
	issue := assignHostedManager(t, h, createCanonicalOwnerTestIssue(t, h))
	turn, principal := startHostedIssueReview(t, h, issue)
	p := protocol.CoordinationReply{Body: "A scoped progress update", Purpose: "progress", ReplyTo: turn.MessageID, Disposition: "linked", ReviewedKinds: allFactKinds, WorkItemIDs: []string{issue.IssueID}}
	op := hostedEffect(t, h, principal, turn, 1, protocol.Target{Type: "logical_turn", ID: turn.RequestID}, nil, p, CoordinationReply{})
	if op.Error == nil || op.Error.Code != protocol.CodeInvalidRequest {
		t.Fatal("missing context accepted", op)
	}
	p.Contexts = []protocol.LogicalContextReview{{WorkItemID: issue.IssueID, Revision: h.agentContext(issue.IssueID).Revision + 1}}
	op = hostedEffect(t, h, principal, turn, 1, protocol.Target{Type: "logical_turn", ID: turn.RequestID}, nil, p, CoordinationReply{})
	if op.Error == nil || op.Error.Code != protocol.CodeStaleRevision {
		t.Fatal("stale context accepted", op)
	}
}
