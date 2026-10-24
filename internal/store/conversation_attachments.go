package store

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
)

// ConversationAttachmentRow is original-file metadata, initially unbound.
type ConversationAttachmentRow struct {
	protocol.ConversationAttachment
	CreatedAt int64
}

// InsertConversationAttachment records a completed file upload.
func (t *Tx) InsertConversationAttachment(ctx context.Context, r ConversationAttachmentRow) error {
	_, err := t.Exec(ctx, `INSERT INTO conversation_attachments(file_id,name,media_type,size,sha256,created_at) VALUES(?,?,?,?,?,?) ON CONFLICT(file_id) DO NOTHING`, r.FileID, r.Name, r.MediaType, r.Size, r.SHA256, r.CreatedAt)
	return err
}

// GetConversationAttachment reads metadata without touching original content.
func GetConversationAttachment(ctx context.Context, q Querier, id string) (r ConversationAttachmentRow, err error) {
	err = q.QueryRowContext(ctx, `SELECT file_id,name,media_type,size,sha256,created_at FROM conversation_attachments WHERE file_id=?`, id).Scan(&r.FileID, &r.Name, &r.MediaType, &r.Size, &r.SHA256, &r.CreatedAt)
	if err != nil {
		return r, notFound(err, "attachment "+id)
	}
	return r, nil
}

// BindConversationAttachment attaches a retained original in message order.
func (t *Tx) BindConversationAttachment(ctx context.Context, message string, position int, id string) error {
	_, err := t.Exec(ctx, `INSERT INTO conversation_message_attachments(message_id,file_id,position) VALUES(?,?,?) ON CONFLICT(message_id,position) DO NOTHING`, message, id, position)
	return err
}

// ConversationAttachments lists only metadata in message attachment order.
func ConversationAttachments(ctx context.Context, q Querier, message string) ([]protocol.ConversationAttachment, error) {
	rows, err := q.QueryContext(ctx, `SELECT a.file_id,a.name,a.media_type,a.size,a.sha256 FROM conversation_attachments a JOIN conversation_message_attachments m ON m.file_id=a.file_id WHERE m.message_id=? ORDER BY m.position`, message)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []protocol.ConversationAttachment
	for rows.Next() {
		var a protocol.ConversationAttachment
		if err := rows.Scan(&a.FileID, &a.Name, &a.MediaType, &a.Size, &a.SHA256); err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	return result, rows.Err()
}

// AttachmentSourceMessage returns the first message that attached the original.
// An upload may be selected explicitly before it is bound to a message.
func AttachmentSourceMessage(ctx context.Context, q Querier, id string) (string, error) {
	var message string
	err := q.QueryRowContext(ctx, `SELECT COALESCE((SELECT a.message_id FROM conversation_message_attachments a JOIN conversation_messages m ON m.message_id=a.message_id WHERE a.file_id=? ORDER BY m.seq LIMIT 1), '')`, id).Scan(&message)
	return message, err
}
