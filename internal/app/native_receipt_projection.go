package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// ObserveNativeTurnTerminal records an adapter's observed terminal before its
// messages are projected. Pending projection and outbox receipts remain intact.
func (a *App) ObserveNativeTurnTerminal(ctx context.Context, observed store.MainTurn) error {
	if observed.Status != store.TurnCompleted && observed.Status != store.TurnInterrupted && observed.Status != store.TurnFailed {
		return protocol.Errorf(protocol.CodeInvalidRequest, "native terminal observation is required")
	}
	return a.write(ctx, func(tx *store.Tx) error {
		prior, err := store.GetMainTurn(ctx, tx.Q(), observed.RequestID)
		if err != nil {
			return err
		}
		if prior.ThreadID != observed.ThreadID || prior.TurnID != "" && prior.TurnID != observed.TurnID || observed.TurnID == "" {
			return protocol.Errorf(protocol.CodeConflictPayload, "native terminal belongs to another execution")
		}
		prior.TurnID, prior.Status, prior.Failure = observed.TurnID, observed.Status, observed.Failure
		if _, err := tx.Exec(ctx, `INSERT INTO native_turn_receipts_pending(request_id) VALUES(?) ON CONFLICT DO NOTHING`, prior.RequestID); err != nil {
			return err
		}
		// This is a small observation fact, not a second copy of native items.
		// Full history is retained only once its required source coverage is known.
		conversation, err := nativeReceiptConversation(ctx, tx.Q(), prior)
		if err != nil {
			return err
		}
		terminal, _ := json.Marshal(struct {
			Status string `json:"status"`
		}{observed.Status})
		if _, err := tx.Exec(ctx, `INSERT INTO agent_rollout_events(conversation_id,thread_id,turn_id,item_id,kind,data_json,created_at) SELECT ?,?,?,'','native_terminal',?,? WHERE NOT EXISTS(SELECT 1 FROM agent_rollout_events WHERE conversation_id=? AND thread_id=? AND turn_id=? AND kind='native_terminal')`, conversation, prior.ThreadID, prior.TurnID, string(terminal), a.now().UnixMilli(), conversation, prior.ThreadID, prior.TurnID); err != nil {
			return err
		}

		return tx.PutMainTurn(ctx, prior, a.now().UnixMilli())
	})
}

// RetainedNativeReceipt reads the single original for this exact execution.
// It neither observes a new native outcome nor settles pending consumption.
func (a *App) RetainedNativeReceipt(ctx context.Context, conversation, request string) (json.RawMessage, error) {
	var original json.RawMessage
	err := a.store.Read(ctx, func(tx *store.ReadTx) error {
		rec, err := store.GetMainTurn(ctx, tx.Q(), request)
		if err != nil {
			return err
		}
		actual, err := nativeReceiptConversation(ctx, tx.Q(), rec)
		if err != nil {
			return err
		}

		if store.ConversationID(actual) != store.ConversationID(conversation) {
			return protocol.Errorf(protocol.CodeForbidden, "native receipt is outside the original conversation")
		}
		var raw string
		err = tx.Q().QueryRowContext(ctx, `SELECT data_json FROM agent_rollout_events WHERE conversation_id=? AND thread_id=? AND turn_id=? AND kind='native_receipt' ORDER BY seq LIMIT 1`, store.ConversationID(conversation), rec.ThreadID, rec.TurnID).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		raw, err = store.ResolveJSON(ctx, tx.Q(), raw)
		if err != nil {
			return err
		}
		original = json.RawMessage(raw)
		return nil
	})
	return original, err
}

// Legacy source messages may be shared into a different admitted conversation.
// Match the same precedence used by hostedRuntimeStartupSelection.
func nativeReceiptConversation(ctx context.Context, q store.Querier, rec store.MainTurn) (string, error) {
	execution, err := store.GetHostedExecution(ctx, q, rec.RequestID)
	if err == nil {
		return store.ConversationID(execution.ConversationID), nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	var actual string
	err = q.QueryRowContext(ctx, `SELECT COALESCE(c.conversation_id,m.conversation_id) FROM conversation_messages m LEFT JOIN agent_turn_contexts c ON c.request_id=? WHERE m.message_id=?`, rec.RequestID, rec.MessageID).Scan(&actual)
	return store.ConversationID(actual), err
}
