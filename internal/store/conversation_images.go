package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
)

// ConversationImageRow describes an original stored outside conversation text.
// Data is a compatibility upload input only; reads return SHA256 and Size.
type ConversationImageRow struct {
	protocol.ConversationImage
	Data   []byte
	SHA256 string
}

// InsertConversationImage adds an image and its general attachment reference.
// Non-nil Data is staged and determines SHA256/Size (including an empty slice).
// With nil Data, SHA256/Size must describe an original already staged by the app.
func (t *Tx) InsertConversationImage(ctx context.Context, messageID string, position int, image ConversationImageRow) error {
	if image.Data != nil {
		if t.files == nil {
			return errors.New("store: image file storage unavailable")
		}
		object, err := t.files.Put(ctx, bytes.NewReader(image.Data))
		if err != nil {
			return err
		}
		image.SHA256, image.Size = object.SHA256, object.Size
	}
	if image.SHA256 == "" {
		return errors.New("store: image original is not staged")
	}
	_, err := t.Exec(ctx, `INSERT INTO conversation_images(image_id,message_id,position,name,media_type,data,size,sha256) VALUES(?,?,?,?,?,X'',?,?)`, image.ImageID, messageID, position, image.Name, image.MediaType, image.Size, image.SHA256)
	if err != nil {
		return fmt.Errorf("store: insert conversation image: %w", err)
	}
	if err := t.InsertConversationAttachment(ctx, ConversationAttachmentRow{ConversationAttachment: protocol.ConversationAttachment{FileID: image.ImageID, Name: image.Name, MediaType: image.MediaType, Size: image.Size, SHA256: image.SHA256}}); err != nil {
		return err
	}
	return t.BindConversationAttachment(ctx, messageID, position, image.ImageID)
}

// ConversationImages lists metadata in the owner's attachment order.
func ConversationImages(ctx context.Context, q Querier, messageID string) ([]protocol.ConversationImage, error) {
	rows, err := q.QueryContext(ctx, `SELECT image_id,name,media_type,size FROM conversation_images WHERE message_id=? ORDER BY position`, messageID)
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

// GetConversationImage reads image metadata without loading the original file.
func GetConversationImage(ctx context.Context, q Querier, id string) (ConversationImageRow, error) {
	var r ConversationImageRow
	err := q.QueryRowContext(ctx, `SELECT image_id,name,media_type,size,sha256 FROM conversation_images WHERE image_id=?`, id).Scan(&r.ImageID, &r.Name, &r.MediaType, &r.Size, &r.SHA256)
	if err != nil {
		return r, notFound(err, "conversation image")
	}
	return r, nil
}
