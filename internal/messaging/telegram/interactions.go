package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/GhostFlying/tend/internal/app"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func (c *Client) ackCallback(ctx context.Context, id, text string) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	// Receipt acknowledgement never claims business completion.
	_, _ = c.sdk.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: id, Text: text})
}
func (c *Client) processCallback(ctx context.Context, u app.TelegramInbox, q *models.CallbackQuery) error {
	c.ackCallback(ctx, q.ID, "")
	b, err := c.app.TelegramBinding(ctx)
	if err != nil {
		return err
	}
	var chat models.Chat
	var external int
	if q.Message.Message != nil {
		chat = q.Message.Message.Chat
		external = q.Message.Message.ID
	} else if q.Message.InaccessibleMessage != nil {
		chat = q.Message.InaccessibleMessage.Chat
		external = q.Message.InaccessibleMessage.MessageID
	}
	if q.From.ID != b.UserID || chat.Type != "private" || chat.ID != b.ChatID || b.Status != "ready" || b.BotID != c.botID || len(q.Data) > 64 || external == 0 {
		return c.app.SettleTelegramInput(ctx, u, "unbound_callback")
	}
	done, err := c.app.BeginOwnerMutation(ctx)
	if err != nil {
		return err
	}
	defer done()
	op, err := c.app.RespondTelegramInteraction(ctx, u, q.From.ID, chat.ID, int64(external), q.Data)
	if err != nil {
		if protocol.CodeOf(err) == protocol.CodeForbidden || protocol.CodeOf(err) == protocol.CodeInvalidRequest || protocol.CodeOf(err) == protocol.CodeNotFound {
			c.ackCallback(ctx, q.ID, "此操作已失效，请查看最新问题。")
			return c.app.SettleTelegramInput(ctx, u, "invalid_callback")
		}
		return err
	}
	if op.Error != nil {
		c.ackCallback(ctx, q.ID, "问题已变化，请查看最新消息。")
		return c.app.SettleTelegramInput(ctx, u, "interaction_refused")
	}
	var result struct {
		Interaction *protocol.MessagingInteraction `json:"interaction"`
		Already     bool                           `json:"already_handled"`
	}
	if json.Unmarshal(op.Result, &result) == nil && result.Already && result.Interaction != nil {
		channel := result.Interaction.HandledChannel
		if channel == "web" {
			channel = "Web"
		} else if channel == "telegram" {
			channel = "Telegram"
		}
		text := "此请求已处理"
		if channel != "" {
			text = "已在 " + channel + " 处理"
		}
		c.ackCallback(ctx, q.ID, text)
	}
	return nil
}
func interactionKeyboard(i protocol.MessagingInteraction) *models.InlineKeyboardMarkup {
	keyboard := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{}}
	if i.State != "open" {
		return keyboard
	}
	for n, o := range i.Options {
		keyboard.InlineKeyboard = append(keyboard.InlineKeyboard, []models.InlineKeyboardButton{{Text: o.Label, CallbackData: "t:" + i.ID + ":" + strconv.Itoa(n)}})
	}
	keyboard.InlineKeyboard = append(keyboard.InlineKeyboard, []models.InlineKeyboardButton{{Text: "文字回答", CallbackData: "t:" + i.ID + ":text"}})
	return keyboard
}
func boundedInteractionText(i protocol.MessagingInteraction) string {
	text := app.MessagingInteractionText(i)
	var result strings.Builder
	units := 0
	for _, r := range text {
		n := 1
		if r > 0xffff {
			n = 2
		}
		if units+n > 3850 {
			return result.String() + "\n…（请引用消息回答，完整问题可在 Web 查看）"
		}
		result.WriteRune(r)
		units += n
	}
	return result.String()
}
func boundedInteractionMessage(m protocol.MessagingMessage) string {
	text := boundedInteractionText(*m.Interaction)
	if strings.HasPrefix(m.Reason, "attention_reminder:") {
		notice := "已到约定行动时间"
		if strings.HasSuffix(m.Reason, ":near") {
			notice = "即将到达约定行动时间"
		}
		text = notice + " · 阅读或归档不会完成此事项\n\n" + text
	}
	return text
}
func (c *Client) editInteraction(ctx context.Context, p app.MessagingPart) app.DeliveryResult {
	message, err := c.app.MessagingMessage(ctx, p.MessageID)
	i := message.Interaction
	if err != nil || i == nil {
		return app.DeliveryResult{Outcome: app.Retry, Reason: "interaction_projection_unavailable"}
	}
	_, err = c.sdk.EditMessageText(ctx, &bot.EditMessageTextParams{ChatID: p.ChatID, MessageID: int(p.ReplyExternalID), Text: boundedInteractionMessage(message), ReplyMarkup: interactionKeyboard(*i)})
	if err == nil || errors.Is(err, bot.ErrorBadRequest) && strings.Contains(strings.ToLower(err.Error()), "message is not modified") {
		return app.DeliveryResult{Outcome: app.Delivered, ExternalMessageID: p.ReplyExternalID}
	}
	if errors.Is(err, bot.ErrorBadRequest) || errors.Is(err, bot.ErrorNotFound) {
		return app.DeliveryResult{Outcome: app.Failed, Reason: "interaction_edit_unavailable"}
	}
	result := resultError(sanitize(err))
	if result.Outcome == app.Unknown {
		result.Outcome = app.Retry
		result.Reason = "interaction_edit_retry"
	}
	return result
}
