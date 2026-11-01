package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
)

func startInteractionBot(t *testing.T, c *Client, a *app.App) {
	t.Helper()
	if err := a.PersistTelegramBatch(t.Context(), 777, []app.TelegramInbox{{UpdateID: 1, Payload: json.RawMessage(`{"update_id":1}`)}}); err != nil {
		t.Fatal(err)
	}
	u, _, _ := a.PendingTelegram(t.Context(), 777)
	op, err := a.ReceiveTelegram(t.Context(), u, app.TelegramIncoming{UserID: 42, ChatID: 42, Start: true})
	if err != nil || op.Error != nil {
		t.Fatal(op, err)
	}
}
func createInteraction(t *testing.T, a *app.App, ids protocol.IDGen) protocol.MessagingMessage {
	t.Helper()
	raw, _ := json.Marshal(protocol.CreateAttentionPayload{Kind: "decision", Title: "请选择检查范围", Options: []string{"手机", "桌面"}})
	op, err := a.Execute(t.Context(), protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "test"}, &protocol.Envelope{RequestID: ids.New(protocol.PrefixRequest), Operation: "attentions.create", Target: protocol.Target{Type: "attention"}, Payload: raw}, app.AttentionCommand{Action: "create"})
	if err != nil || op.Error != nil {
		t.Fatal(op, err)
	}
	page, err := a.MessagingMessages(t.Context(), 0, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range page.Items {
		if m.Interaction != nil {
			return m
		}
	}
	t.Fatal("missing question")
	return protocol.MessagingMessage{}
}
func deliverQuestion(t *testing.T, c *Client, a *app.App) {
	t.Helper()
	d, _, ok, err := a.NextDelivery(t.Context(), app.MessagingDestination)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	result := c.Deliver(t.Context(), d)
	if result.Outcome != app.Delivered {
		t.Fatal(result)
	}
	if err = a.FinishDelivery(t.Context(), d, result); err != nil {
		t.Fatal(err)
	}
}
func TestTelegramInteractionButtonsCallbackAndDurableACK(t *testing.T) {
	var application *app.App
	var data string
	var callbackACK atomic.Int64
	c, a, ids := testTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		if botSetup(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			var p struct {
				Text   string `json:"text"`
				Markup struct {
					Rows [][]struct {
						Data string `json:"callback_data"`
					} `json:"inline_keyboard"`
				} `json:"reply_markup"`
			}
			decodeTelegramParams(r, &p)
			if len(p.Markup.Rows) == 0 || !strings.Contains(p.Text, "请选择检查范围") {
				t.Errorf("missing native interaction: %+v", p)
			} else {
				data = p.Markup.Rows[0][0].Data
			}
			io.WriteString(w, `{"ok":true,"result":{"message_id":50,"date":1,"chat":{"id":42,"type":"private"}}}`)
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			fmt.Fprintf(w, `{"ok":true,"result":[{"update_id":2,"callback_query":{"id":"callback-2","from":{"id":42},"message":{"message_id":50,"date":1,"chat":{"id":42,"type":"private"}},"data":%q}}]}`, data)
		case strings.HasSuffix(r.URL.Path, "/answerCallbackQuery"):
			offset, err := application.TelegramOffset(context.Background(), 777)
			if err != nil || offset != 3 {
				t.Errorf("ACK preceded durable inbox: %d %v", offset, err)
			}
			callbackACK.Add(1)
			io.WriteString(w, `{"ok":true,"result":true}`)
		default:
			t.Errorf("unexpected API %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	application = a
	startInteractionBot(t, c, a)
	m := createInteraction(t, a, ids)
	deliverQuestion(t, c, a)
	if data == "" || len(data) > 64 {
		t.Fatal(data)
	}
	if err := c.poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	if callbackACK.Load() == 0 {
		t.Fatal("callback not acknowledged by polling")
	}
	before, err := a.MessagingInteraction(t.Context(), m.Interaction.ID)
	if err != nil || before.State != "open" {
		t.Fatal("ACK was confused with a business answer", before, err)
	}
	u, ok, err := a.PendingTelegram(t.Context(), 777)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err = c.process(t.Context(), u); err != nil {
		t.Fatal(err)
	}
	current, err := a.MessagingMessage(t.Context(), m.MessageID)
	if err != nil || current.Interaction.State != "answered" || current.Interaction.HandledChannel != "telegram" {
		t.Fatal(current, err)
	}
	if err = c.process(t.Context(), u); err != nil {
		t.Fatal(err)
	}
}
func TestTelegramCrossChannelEditFallbackIsNotRepeated(t *testing.T) {
	var sends, edits atomic.Int64
	c, a, ids := testTelegram(t, func(w http.ResponseWriter, r *http.Request) {
		if botSetup(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			sends.Add(1)
			fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d,"date":1,"chat":{"id":42,"type":"private"}}}`, 50+sends.Load())
			return
		}
		if strings.HasSuffix(r.URL.Path, "/editMessageText") {
			edits.Add(1)
			var p struct {
				Text   string `json:"text"`
				Markup struct {
					Rows []json.RawMessage `json:"inline_keyboard"`
				} `json:"reply_markup"`
			}
			decodeTelegramParams(r, &p)
			if !strings.Contains(p.Text, "已在 Web 处理") || len(p.Markup.Rows) != 0 {
				t.Errorf("stale control not retired: %+v", p)
			}
			w.WriteHeader(400)
			io.WriteString(w, `{"ok":false,"error_code":400,"description":"Bad Request: message to edit not found"}`)
			return
		}
		t.Errorf("unexpected API %s", r.URL.Path)
		w.WriteHeader(404)
	})
	startInteractionBot(t, c, a)
	m := createInteraction(t, a, ids)
	deliverQuestion(t, c, a)
	raw, _ := json.Marshal(protocol.RespondInteraction{Version: m.Interaction.Version, Option: "手机"})
	op, err := a.Execute(t.Context(), protocol.Principal{Kind: protocol.PrincipalOwner, Subject: "owner", Client: "test"}, &protocol.Envelope{RequestID: ids.New(protocol.PrefixRequest), Operation: "messaging.respond", Target: protocol.Target{Type: "interaction", ID: m.Interaction.ID}, Payload: raw}, app.RespondMessagingInteraction{})
	if err != nil || op.Error != nil {
		t.Fatal(op, err)
	}
	d, _, ok, err := a.NextDelivery(t.Context(), app.MessagingInteractionDestination)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	result := c.Deliver(t.Context(), d)
	if result.Reason != "interaction_edit_unavailable" {
		t.Fatal(result)
	}
	if err = a.FinishDelivery(t.Context(), d, result); err != nil {
		t.Fatal(err)
	}
	fallback, _, ok, err := a.NextDelivery(t.Context(), app.MessagingDestination)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if result = c.Deliver(t.Context(), fallback); result.Outcome != app.Delivered {
		t.Fatal(result)
	}
	if err = a.FinishDelivery(t.Context(), fallback, result); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, err = a.NextDelivery(t.Context(), app.MessagingInteractionDestination); err != nil || ok {
		t.Fatal("fallback created an edit loop", ok, err)
	}
	if c.Reconcile(t.Context(), fallback).Outcome != app.Unknown || sends.Load() != 2 || edits.Load() != 1 {
		t.Fatal("fallback send was retried", sends.Load(), edits.Load())
	}
}

func decodeTelegramParams(r *http.Request, value any) {
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		_ = json.NewDecoder(r.Body).Decode(value)
		return
	}
	_ = r.ParseMultipartForm(1 << 20)
	fields := map[string]any{}
	for k, v := range r.Form {
		if len(v) > 0 {
			if k == "reply_markup" {
				fields[k] = json.RawMessage(v[0])
			} else {
				fields[k] = v[0]
			}
		}
	}
	raw, _ := json.Marshal(fields)
	_ = json.Unmarshal(raw, value)
}

func TestTelegramReminderWindowRemainsWithInteractionStatus(t *testing.T) {
	i := &protocol.MessagingInteraction{Question: "核对报告", State: "answered", HandledChannel: "web"}
	for _, tc := range []struct{ window, label string }{{"near", "即将到达约定行动时间"}, {"due", "已到约定行动时间"}} {
		text := boundedInteractionMessage(protocol.MessagingMessage{Interaction: i, Reason: "attention_reminder:attention:" + tc.window})
		if !strings.Contains(text, tc.label) || !strings.Contains(text, "已在 Web 处理") {
			t.Fatal(text)
		}
	}
}
