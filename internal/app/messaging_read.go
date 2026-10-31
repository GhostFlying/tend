package app

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// RememberMessagingNativeInput freezes the exact input before its native call.
// Repairs and empty continuations never register a new consumption witness.
func (a *App) RememberMessagingNativeInput(ctx context.Context, execution, id, text string) error {
	return a.store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO messaging_native_inputs(request_id,message_id,content_revision,input_hash) SELECT t.request_id,m.message_id,d.content_revision,? FROM main_turns t JOIN messaging_messages m ON m.message_id=t.message_id JOIN message_dispositions d ON d.message_id=m.message_id WHERE t.request_id=? AND t.message_id=? AND t.status='starting' ON CONFLICT(request_id) DO UPDATE SET content_revision=excluded.content_revision,input_hash=excluded.input_hash`, fmt.Sprintf("%x", sha256.Sum256([]byte(text))), execution, id)
		return err
	})
}

// MessagingNativeInputMatches validates retained native user content, including
// its original version, before reconciliation can record a read.
func (a *App) MessagingNativeInputMatches(ctx context.Context, execution, text string) (bool, error) {
	var matches bool
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		return tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM messaging_native_inputs n JOIN message_dispositions d USING(message_id) WHERE n.request_id=? AND n.input_hash=? AND n.content_revision=d.content_revision)`, execution, fmt.Sprintf("%x", sha256.Sum256([]byte(text)))).Scan(&matches)
	})
	return matches, err
}

// ConfigureMessagingReadCapability is adapter registration, never an agent grant.
func (a *App) ConfigureMessagingReadCapability(ctx context.Context, channel, mode string) error {
	if mode != "" && mode != "message" && mode != "cursor" {
		return protocol.Errorf(protocol.CodeInvalidRequest, "invalid read capability")
	}
	return a.store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE messaging_channels SET mark_read_mode=? WHERE channel_id=?`, mode, channel)
		return err
	})
}
func queueCommunicationReads(ctx context.Context, x *Exec, tx *store.Tx) error {
	rows, err := tx.Q().QueryContext(ctx, `SELECT m.message_id,m.channel_id,m.bot_id,m.chat_id,m.external_id,c.revision,c.mark_read_mode FROM messaging_messages m JOIN conversation_messages original USING(message_id) JOIN messaging_channels c USING(channel_id) WHERE m.direction='input' AND c.mark_read_mode<>'' AND c.status='ready' AND m.bot_id=c.bot_id AND m.chat_id=c.chat_id AND EXISTS(SELECT 1 FROM messaging_reads r WHERE r.message_id=m.message_id AND r.reader LIKE 'agent:%') AND NOT EXISTS(SELECT 1 FROM messaging_deliveries d WHERE d.message_id=m.message_id AND d.kind='read') AND (c.mark_read_mode='message' OR NOT EXISTS(SELECT 1 FROM messaging_messages earlier JOIN conversation_messages e USING(message_id) WHERE earlier.channel_id=m.channel_id AND earlier.bot_id=m.bot_id AND earlier.chat_id=m.chat_id AND earlier.direction='input' AND e.seq<original.seq AND NOT EXISTS(SELECT 1 FROM messaging_reads r WHERE r.message_id=earlier.message_id AND r.reader LIKE 'agent:%'))) ORDER BY original.seq`)
	if err != nil {
		return err
	}
	type pending struct {
		id, channel, mode             string
		bot, chat, external, revision int64
	}
	var all []pending
	for rows.Next() {
		var p pending
		if err = rows.Scan(&p.id, &p.channel, &p.bot, &p.chat, &p.external, &p.revision, &p.mode); err != nil {
			rows.Close()
			return err
		}
		all = append(all, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range all {
		d := MessagingPart{DeliveryID: x.IDs.New(protocol.PrefixOutbox), MessageID: p.id, BotID: p.bot, ChatID: p.chat, ReplyExternalID: p.external, Kind: "read", BindingRevision: p.revision}
		if _, err = tx.Exec(ctx, `INSERT INTO messaging_deliveries(delivery_id,message_id,channel_id,part,kind,bot_id,chat_id,reply_external_id,state,created_at,updated_at,binding_revision) VALUES(?,?,?,-1,'read',?,?,?,'queued',?,?,?)`, d.DeliveryID, p.id, p.channel, p.bot, p.chat, p.external, x.Now.UnixMilli(), x.Now.UnixMilli(), p.revision); err != nil {
			return err
		}
		if err = insertMessagingOutbox(ctx, x, tx, d, MessagingReadDestination); err != nil {
			return err
		}
	}
	return nil
}

// ReadCommunicationInput is invoked by an authenticated agent tool, not by
// queue admission. Returning exact original content is witnessed source reading.
func (a *App) ReadCommunicationInput(ctx context.Context, execution, id string) (protocol.MessagingMessage, error) {
	var result protocol.MessagingMessage
	err := a.store.Write(ctx, func(tx *store.Tx) error {
		turn, err := store.GetMainTurn(ctx, tx.Q(), execution)
		if err != nil {
			return err
		}
		conversation, err := store.MessageConversation(ctx, tx.Q(), turn.MessageID)
		if err != nil {
			return err
		}
		if err = communicationSourceAllowed(ctx, tx.Q(), execution, conversation, id); err != nil {
			return err
		}
		result, err = messagingMessage(ctx, tx.Q(), id)
		if err != nil {
			return err
		}
		x := &Exec{IDs: a.ids, Now: a.now(), Env: &protocol.Envelope{RequestID: execution}}
		return recordCommunicationRead(ctx, x, tx, id, result.ContentRevision, "agent", "agent", "source_read_tool", execution)
	})
	if err == nil {
		a.notifyChange()
		if a.notifier != nil {
			a.notifier.Notify()
		}
	}
	return result, err
}
