package store

import (
	"context"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
)

// ConversationImageRow retains bytes separately from conversation text and events.
type ConversationImageRow struct {
	protocol.ConversationImage
	Data []byte
}

// InsertConversationImage adds one attachment in the message transaction.
func (t *Tx) InsertConversationImage(ctx context.Context, messageID string, position int, image ConversationImageRow) error {
	_, err := t.Exec(ctx, `INSERT INTO conversation_images(image_id,message_id,position,name,media_type,data) VALUES(?,?,?,?,?,?)`, image.ImageID, messageID, position, image.Name, image.MediaType, image.Data)
	if err != nil {
		return fmt.Errorf("store: insert conversation image: %w", err)
	}
	return nil
}

// ConversationImages lists metadata in the owner's attachment order.
func ConversationImages(ctx context.Context, q Querier, messageID string) ([]protocol.ConversationImage, error) {
	rows, err := q.QueryContext(ctx, `SELECT image_id,name,media_type,length(data) FROM conversation_images WHERE message_id=? ORDER BY position`, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var images []protocol.ConversationImage
	for rows.Next() {
		var i protocol.ConversationImage
		if err := rows.Scan(&i.ImageID, &i.Name, &i.MediaType, &i.Size); err != nil {
			return nil, err
		}
		images = append(images, i)
	}
	return images, rows.Err()
}

// GetConversationImage reads one retained image, or ErrNotFound.
func GetConversationImage(ctx context.Context, q Querier, id string) (ConversationImageRow, error) {
	var r ConversationImageRow
	err := q.QueryRowContext(ctx, `SELECT image_id,name,media_type,length(data),data FROM conversation_images WHERE image_id=?`, id).Scan(&r.ImageID, &r.Name, &r.MediaType, &r.Size, &r.Data)
	if err != nil {
		return r, notFound(err, "conversation image")
	}
	return r, nil
}
