package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// TelegramBinding is private adapter state; it is never an agent tool argument.
type TelegramBinding struct {
	Owner                           string
	UserID, BotID, ChatID, Revision int64
	Status                          string
}

func (a *App) TelegramBinding(ctx context.Context) (TelegramBinding, error) {
	var b TelegramBinding
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(ctx, `SELECT owner,user_id,bot_id,chat_id,revision,status FROM messaging_channels WHERE channel_id='telegram'`).Scan(&b.Owner, &b.UserID, &b.BotID, &b.ChatID, &b.Revision, &b.Status)
	})
	return b, err
}

// ConfigureTelegram records the verified runtime bot identity, without its token.
func (a *App) ConfigureTelegram(ctx context.Context, bot int64) error {
	if bot <= 0 {
		return protocol.Errorf(protocol.CodeInvalidRequest, "invalid bot identity")
	}
	err := a.store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE messaging_channels SET chat_id=CASE WHEN bot_id=? THEN chat_id ELSE 0 END,status=CASE WHEN user_id=0 THEN 'disabled' WHEN bot_id=? AND chat_id<>0 THEN 'ready' ELSE 'waiting_start' END,revision=revision+CASE WHEN bot_id=? THEN 0 ELSE 1 END,bot_id=?,updated_at=? WHERE channel_id='telegram'`, bot, bot, bot, bot, a.now().UnixMilli())
		return err
	})
	if err == nil {
		a.notifyChange()
	}
	return err
}

type TelegramInbox struct {
	BotID, UpdateID int64
	Payload         json.RawMessage
	RequestID       string
}

func (a *App) TelegramOffset(ctx context.Context, bot int64) (int64, error) {
	var offset int64
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(ctx, `SELECT COALESCE((SELECT next_offset FROM telegram_offsets WHERE bot_id=?),0)`, bot).Scan(&offset)
	})
	return offset, err
}

// PersistTelegramBatch commits every update before the next polling confirmation.
func (a *App) PersistTelegramBatch(ctx context.Context, bot int64, updates []TelegramInbox) error {
	done, err := a.BeginOwnerMutation(ctx)
	if err != nil {
		return err
	}
	defer done()
	return a.store.Write(ctx, func(tx *store.Tx) error {
		var offset int64
		for _, u := range updates {
			if u.UpdateID <= 0 || len(u.Payload) > 1<<20 || !json.Valid(u.Payload) {
				return protocol.Errorf(protocol.CodeInvalidRequest, "invalid Telegram update")
			}
			if _, err := tx.Exec(ctx, `INSERT INTO telegram_inbox(bot_id,update_id,payload,request_id,created_at) VALUES(?,?,?,?,?) ON CONFLICT DO NOTHING`, bot, u.UpdateID, string(u.Payload), a.ids.New(protocol.PrefixRequest), a.now().UnixMilli()); err != nil {
				return err
			}
			offset = max(offset, u.UpdateID+1)
		}
		if offset == 0 {
			return nil
		}
		_, err := tx.Exec(ctx, `INSERT INTO telegram_offsets(bot_id,next_offset) VALUES(?,?) ON CONFLICT(bot_id) DO UPDATE SET next_offset=max(next_offset,excluded.next_offset)`, bot, offset)
		return err
	})
}
func (a *App) PendingTelegram(ctx context.Context, bot int64) (TelegramInbox, bool, error) {
	var u TelegramInbox
	u.BotID = bot
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		var raw string
		err := tx.Q().QueryRowContext(ctx, `SELECT update_id,payload,request_id FROM telegram_inbox WHERE bot_id=? AND state='pending' ORDER BY update_id LIMIT 1`, bot).Scan(&u.UpdateID, &raw, &u.RequestID)
		u.Payload = json.RawMessage(raw)
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return u, false, nil
	}
	return u, err == nil, err
}
func (a *App) SettleTelegramInput(ctx context.Context, u TelegramInbox, reason string) error {
	return a.store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE telegram_inbox SET state='rejected',reason=? WHERE bot_id=? AND update_id=? AND state='pending'`, reason, u.BotID, u.UpdateID)
		return err
	})
}
func (a *App) TelegramFileRequest(ctx context.Context, u TelegramInbox, position int) (string, error) {
	var id string
	err := a.store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO telegram_file_requests(bot_id,update_id,position,request_id) VALUES(?,?,?,?) ON CONFLICT DO NOTHING`, u.BotID, u.UpdateID, position, a.ids.New(protocol.PrefixRequest))
		if err != nil {
			return err
		}
		return tx.Q().QueryRowContext(ctx, `SELECT request_id FROM telegram_file_requests WHERE bot_id=? AND update_id=? AND position=?`, u.BotID, u.UpdateID, position).Scan(&id)
	})
	return id, err
}

// TelegramIncoming is constructed only by the authenticated local adapter.
type TelegramIncoming struct {
	Forwarded                                   bool
	UserID, ChatID, ExternalID, ReplyExternalID int64
	Start, Edited                               bool
	Input                                       protocol.MessagingInput
}
type receiveTelegram struct {
	Inbox   TelegramInbox
	Message TelegramIncoming
}

func (receiveTelegram) Operation() string { return "messaging.telegram_input" }
func (c receiveTelegram) Authorize(ctx context.Context, x *Exec, q store.Querier) error {
	if err := allowKinds(x.Principal, protocol.PrincipalOwner); err != nil {
		return err
	}
	var allowed bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM messaging_channels WHERE channel_id='telegram' AND owner=? AND user_id=? AND bot_id=? AND user_id>0 AND status<>'disabled')`, x.Principal.Subject, c.Message.UserID, c.Inbox.BotID).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return protocol.Errorf(protocol.CodeForbidden, "Telegram sender is not bound")
	}
	return nil
}
func (c receiveTelegram) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	m := c.Message
	if m.Start {
		if _, err := tx.Exec(ctx, `UPDATE messaging_channels SET chat_id=?,status='ready',updated_at=? WHERE channel_id='telegram'`, m.ChatID, x.Now.UnixMilli()); err != nil {
			return Outcome{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE telegram_inbox SET state='processed' WHERE bot_id=? AND update_id=?`, c.Inbox.BotID, c.Inbox.UpdateID); err != nil {
			return Outcome{}, err
		}
		return Outcome{Result: map[string]any{"bound": true}, Events: []Event{{ObjectType: "channel", ObjectID: "telegram", Kind: "messaging.channel_changed"}}}, nil
	}
	var bound bool
	if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM messaging_channels WHERE channel_id='telegram' AND status='ready' AND chat_id=?)`, m.ChatID).Scan(&bound); err != nil {
		return Outcome{}, err
	}
	if !bound {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "start the bound bot first")
	}
	p := m.Input
	if m.ReplyExternalID != 0 {
		err := tx.Q().QueryRowContext(ctx, `SELECT m.message_id FROM (SELECT message_id,external_id,bot_id,chat_id FROM messaging_messages UNION ALL SELECT message_id,external_id,bot_id,chat_id FROM messaging_deliveries WHERE external_id<>0) m JOIN conversation_messages c USING(message_id) WHERE m.bot_id=? AND m.chat_id=? AND m.external_id=? ORDER BY c.seq DESC LIMIT 1`, c.Inbox.BotID, m.ChatID, m.ReplyExternalID).Scan(&p.ReplyTo)
		if err != nil {
			return Outcome{}, errNotFound(err, "quoted message is not known to Tend")
		}
	}
	if m.Edited {
		err := tx.Q().QueryRowContext(ctx, `SELECT message_id FROM messaging_messages WHERE channel_id='telegram' AND direction='input' AND bot_id=? AND chat_id=? AND external_id=? ORDER BY rowid DESC LIMIT 1`, c.Inbox.BotID, m.ChatID, m.ExternalID).Scan(&p.CorrectionOf)
		if err != nil {
			return Outcome{}, errNotFound(err, "edited input is not known to Tend")
		}
	}
	out, err := submitCommunication(ctx, x, tx, p, "telegram", c.Inbox.BotID, m.ChatID, m.ExternalID, m.Forwarded)
	if err != nil {
		return Outcome{}, err
	}
	id := out.Result.(map[string]any)["message_id"].(string)
	_, err = tx.Exec(ctx, `UPDATE telegram_inbox SET state='processed',message_id=? WHERE bot_id=? AND update_id=?`, id, c.Inbox.BotID, c.Inbox.UpdateID)
	return out, err
}
func (a *App) ReceiveTelegram(ctx context.Context, u TelegramInbox, m TelegramIncoming) (protocol.Operation, error) {
	b, err := a.TelegramBinding(ctx)
	if err != nil {
		return protocol.Operation{}, err
	}
	payload, _ := json.Marshal(m)
	env := &protocol.Envelope{RequestID: u.RequestID, Operation: "messaging.telegram_input", Target: protocol.Target{Type: "message"}, Payload: payload}
	return a.Execute(ctx, protocol.Principal{Kind: protocol.PrincipalOwner, Subject: b.Owner, Client: "telegram"}, env, receiveTelegram{Inbox: u, Message: m})
}

// ValidateMessagingPart fences a frozen destination against revoked bindings.
func (a *App) ValidateMessagingPart(ctx context.Context, p MessagingPart) error {
	b, err := a.TelegramBinding(ctx)
	if err != nil {
		return err
	}
	if b.Status != "ready" || b.BotID != p.BotID || b.ChatID != p.ChatID || b.Revision != p.BindingRevision {
		return protocol.Errorf(protocol.CodeForbidden, "channel binding is no longer active")
	}
	var stale bool
	err = a.store.Read(ctx, func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_bindings b JOIN followup_occurrences f ON f.occurrence_id=b.object_id JOIN followup_rules r ON r.rule_id=f.rule_id WHERE b.message_id=? AND b.object_type='followup_occurrence' AND (f.status='cancelled' OR r.status IN ('paused','cancelled')))`, p.MessageID).Scan(&stale)
	})
	if err != nil {
		return err
	}
	if stale {
		return protocol.Errorf(protocol.CodeInvalidTransition, "notification source is no longer current")
	}

	return nil
}

func (a *App) TelegramUploadedFile(ctx context.Context, request string) (string, bool, error) {
	var id string
	found := false
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		r, err := store.GetRequest(ctx, tx.Q(), "owner", request)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if r.Status != protocol.OpSucceeded {
			return protocol.Errorf(protocol.CodeInvalidTransition, "file upload has a recorded failure")
		}
		var f protocol.ConversationAttachment
		if err = json.Unmarshal([]byte(r.ResultJSON), &f); err != nil {
			return err
		}
		id = f.FileID
		found = true
		return nil
	})
	return id, found, err
}

// RejectTelegramInput retains a definite input failure and queues a factual
// transport response, without admitting partial content to any agent.
func (a *App) RejectTelegramInput(ctx context.Context, u TelegramInbox, reason string) error {
	err := a.store.Write(ctx, func(tx *store.Tx) error {
		var state string
		if err := tx.Q().QueryRowContext(ctx, `SELECT state FROM telegram_inbox WHERE bot_id=? AND update_id=?`, u.BotID, u.UpdateID).Scan(&state); err != nil {
			return err
		}
		if state != "pending" {
			return nil
		}
		var bot, chat int64
		if err := tx.Q().QueryRowContext(ctx, `SELECT bot_id,chat_id FROM messaging_channels WHERE channel_id='telegram' AND status='ready'`).Scan(&bot, &chat); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		x := &Exec{IDs: a.ids, Now: a.now(), Env: &protocol.Envelope{RequestID: u.RequestID}}
		if bot == u.BotID && chat != 0 {
			id := a.ids.New(protocol.PrefixMessage)
			body := telegramRejectionText(reason)
			if _, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: id, Author: "system", Body: body, CreatedAt: x.Now.UnixMilli()}); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO messaging_messages(message_id,direction,channel_id,purpose,reason) VALUES(?,'notification','telegram','final',?)`, id, reason); err != nil {
				return err
			}
			if err := queueCommunication(ctx, x, tx, id, "telegram", bot, chat, 0, protocol.MessagingPublication{Body: body, Purpose: "final", Reason: reason}); err != nil {
				return err
			}
			if err := communicationEvent(ctx, x, tx, id, "messaging.published"); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `UPDATE telegram_inbox SET state='rejected',reason=? WHERE bot_id=? AND update_id=?`, reason, u.BotID, u.UpdateID)
		return err
	})
	if err == nil {
		a.notifyChange()
		if a.notifier != nil {
			a.notifier.Notify()
		}
	}
	return err
}

// DisableTelegramRuntime preserves the binding for a later configured restart,
// while new notifications cannot become an implicit historical backlog.
func (a *App) DisableTelegramRuntime(ctx context.Context) error {
	return a.store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE messaging_channels SET status=CASE WHEN user_id>0 THEN 'unavailable' ELSE 'disabled' END,health='disabled' WHERE channel_id='telegram'`)
		return err
	})
}

// ObserveTelegramHealth records only fixed adapter reason codes, never SDK errors.
func (a *App) ObserveTelegramHealth(ctx context.Context, health string) error {
	if health != "ready" && health != "poll_failed" && health != "input_prepare_failed" && health != "disabled" {
		return protocol.Errorf(protocol.CodeInvalidRequest, "unknown Telegram health code")
	}
	err := a.store.Write(ctx, func(tx *store.Tx) error {
		var old string
		if err := tx.Q().QueryRowContext(ctx, `SELECT health FROM messaging_channels WHERE channel_id='telegram'`).Scan(&old); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE messaging_channels SET health=?,last_observed_at=? WHERE channel_id='telegram'`, health, a.now().UnixMilli()); err != nil {
			return err
		}
		if old == health {
			return nil
		}
		_, err := tx.InsertEvent(ctx, store.EventRow{EventID: a.ids.New(protocol.PrefixEvent), Source: "messaging", ObjectType: "channel", ObjectID: "telegram", Kind: "messaging.channel_health", BodyJSON: `{}`, CreatedAt: a.now().UnixMilli()})
		return err
	})
	if err == nil {
		a.notifyChange()
	}
	return err
}

func telegramRejectionText(reason string) string {
	switch reason {
	case "file_exceeds_download_limit":
		return "文件超过 Telegram Bot 的 20 MB 接收上限，未进入处理。请使用 Web 上传原文件。"
	case "unsupported_message_content":
		return "目前支持文本、图片和文件；这条消息未进入处理。请转换格式后重新发送。"
	case "file_unavailable":
		return "未能取得这份文件，尚未交给助理。请重新发送原文件。"
	case "input_rejected_not_found":
		return "找不到被引用的 Tend 消息。请引用已有的 Tend 回复，或直接发送新消息。"
	case "input_rejected_stale_revision":
		return "原问题已经变化。请查看最新问题后再回复。"
	default:
		return "这条消息尚未进入处理。请检查内容和引用，或在 Web 中查看当前事项后重试。"
	}
}
