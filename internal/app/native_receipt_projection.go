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
		execution, err := store.GetHostedExecution(ctx, tx.Q(), request)
		actual := execution.ConversationID
		if errors.Is(err, store.ErrNotFound) {
			actual, err = store.MessageConversation(ctx, tx.Q(), rec.MessageID)
		}
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
