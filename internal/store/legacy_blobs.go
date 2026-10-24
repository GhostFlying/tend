package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/GhostFlying/tend/internal/blobstore"
	"github.com/GhostFlying/tend/internal/protocol"
)

// exportLegacyBlobs is restartable independently of schema migration. A row is
// marked external only after Put has synced its complete content and directory.
// No SQL migration discards bytes. Interrupted publications remain reusable.
// This one-time compatibility export loads one historical BLOB at a time; its
// memory use depends on that old file. New uploads use file streaming only.
func (s *Store) exportLegacyBlobs(ctx context.Context) error {
	for _, images := range []bool{true, false} {
		for {
			done, err := s.exportLegacyBlob(ctx, images)
			if err != nil {
				return err
			}
			if done {
				break
			}
		}
	}
	return nil
}

func (s *Store) exportLegacyBlob(ctx context.Context, images bool) (done bool, err error) {
	var id, message, name, media, expectedHash string
	var position int
	var size, createdAt int64
	var object blobstore.Object
	err = s.Read(ctx, func(tx *ReadTx) error {
		query := `SELECT file_id,name,sha256,size FROM deliverable_files WHERE externalized=0 ORDER BY file_id LIMIT 1`
		if images {
			query = `SELECT i.image_id,i.message_id,i.position,i.name,i.media_type,length(i.data),m.created_at FROM conversation_images i JOIN conversation_messages m ON m.message_id=i.message_id WHERE i.sha256='' ORDER BY i.image_id LIMIT 1`
			err := tx.Q().QueryRowContext(ctx, query).Scan(&id, &message, &position, &name, &media, &size, &createdAt)
			if errors.Is(err, sql.ErrNoRows) {
				done = true
				return nil
			}
			if err != nil {
				return err
			}
		} else {
			err := tx.Q().QueryRowContext(ctx, query).Scan(&id, &name, &expectedHash, &size)
			if errors.Is(err, sql.ErrNoRows) {
				done = true
				return nil
			}
			if err != nil {
				return err
			}
		}
		query = `SELECT content FROM deliverable_files WHERE file_id=?`
		if images {
			query = `SELECT data FROM conversation_images WHERE image_id=?`
		}
		var data []byte
		if err := tx.Q().QueryRowContext(ctx, query, id).Scan(&data); err != nil {
			return err
		}
		if int64(len(data)) != size {
			return errors.New("legacy file length differs from metadata")
		}
		var err error
		object, err = s.files.Put(ctx, bytes.NewReader(data))
		if err != nil {
			return err
		}
		if object.Size != size {
			return errors.New("legacy file length differs from metadata")
		}
		if expectedHash != "" && object.SHA256 != expectedHash {
			return errors.New("legacy file SHA256 differs from metadata")
		}
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("store: export legacy file: %w", err)
	}
	if done {
		return true, nil
	}
	err = s.Write(ctx, func(tx *Tx) error {
		if images {
			if _, err := tx.Exec(ctx, `UPDATE conversation_images SET size=?,sha256=?,data=X'' WHERE image_id=? AND sha256=''`, object.Size, object.SHA256, id); err != nil {
				return err
			}
			if err := tx.InsertConversationAttachment(ctx, ConversationAttachmentRow{ConversationAttachment: protocol.ConversationAttachment{FileID: id, Name: name, MediaType: media, Size: object.Size, SHA256: object.SHA256}, CreatedAt: createdAt}); err != nil {
				return err
			}
			return tx.BindConversationAttachment(ctx, message, position, id)
		}
		_, err := tx.Exec(ctx, `UPDATE deliverable_files SET content=X'',externalized=1 WHERE file_id=? AND externalized=0`, id)
		return err
	})
	if err != nil {
		return false, fmt.Errorf("store: finish legacy file export: %w", err)
	}
	return false, nil
}
