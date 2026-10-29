package store

import (
	"context"
	"encoding/json"

	"github.com/GhostFlying/tend/internal/protocol"
)

func ManualSource(ctx context.Context, q Querier, id string) (protocol.ManualSource, error) {
	var value protocol.ManualSource
	var raw string
	err := q.QueryRowContext(ctx, `SELECT s.metadata_json,m.body,m.conversation_id,m.created_at FROM manual_message_sources s JOIN conversation_messages m USING(message_id) WHERE s.message_id=?`, id).Scan(&raw, &value.Text, &value.ConversationID, &value.CreatedAt)
	if err != nil {
		return value, notFound(err, "manual source")
	}
	text, conversation, at := value.Text, value.ConversationID, value.CreatedAt
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return value, err
	}
	value.MessageID, value.Text, value.ConversationID, value.CreatedAt = id, text, conversation, at
	value.Attachments, err = ConversationAttachments(ctx, q, id)
	return value, err
}
