package app

import (
	"encoding/json"
	"testing"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// retainedLogicalRepairFixture seeds the exact pending record shape produced by
// the retired metadata repair scheduler. Normal settlement must not create it.
// Retained delivery and admission tests still need to preserve its native scope.
func retainedLogicalRepairFixture(t *testing.T, h *harness, original store.MainTurn) string {
	t.Helper()
	request := h.ids.New(protocol.PrefixRequest)
	if err := h.store.Write(t.Context(), func(tx *store.Tx) error {
		var logical string
		if err := tx.Q().QueryRowContext(t.Context(), `SELECT logical_turn_id FROM logical_turn_executions WHERE request_id=?`, original.RequestID).Scan(&logical); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE logical_turns SET status='recovering',repair_attempts=1,reason='Retained metadata repair queued before upgrade' WHERE logical_turn_id=?`, logical); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO logical_turn_executions(request_id,logical_turn_id,repair) VALUES(?,?,1)`, request, logical); err != nil {
			return err
		}
		conversation, err := store.MessageConversation(t.Context(), tx.Q(), original.MessageID)
		if err != nil {
			return err
		}
		payload, err := json.Marshal(HandleMessage{MessageID: original.MessageID})
		if err != nil {
			return err
		}
		return tx.InsertOutbox(t.Context(), store.OutboxRow{OutboxID: h.ids.New(protocol.PrefixOutbox), RequestID: request, ParentRequestID: original.RequestID, Destination: store.ConversationDestination(conversation), Operation: "conversation.repair_turn", PayloadJSON: string(payload), CreatedAt: h.clock.Now().UnixMilli()})
	}); err != nil {
		t.Fatal(err)
	}
	return request
}
