package store

import (
	"context"
	"fmt"
)

// MessageNoticeRow classifies an immutable system message for owner rendering.
type MessageNoticeRow struct {
	MessageID  string
	NoticeType string
	UpgradeID  string
	HostID     string
}

// PutMessageNotice records metadata atomically with the message and its event.
func (t *Tx) PutMessageNotice(ctx context.Context, n MessageNoticeRow) error {
	_, err := t.Exec(ctx, `INSERT INTO message_notices(message_id,notice_type,upgrade_id,host_id) VALUES(?,?,?,?)`, n.MessageID, n.NoticeType, n.UpgradeID, n.HostID)
	if err != nil {
		return fmt.Errorf("store: put message notice: %w", err)
	}
	return nil
}

// GetMessageNotice returns ErrNotFound for legacy unclassified messages.
func GetMessageNotice(ctx context.Context, q Querier, id string) (MessageNoticeRow, error) {
	var n MessageNoticeRow
	err := q.QueryRowContext(ctx, `SELECT message_id,notice_type,upgrade_id,host_id FROM message_notices WHERE message_id=?`, id).Scan(&n.MessageID, &n.NoticeType, &n.UpgradeID, &n.HostID)
	if err != nil {
		return MessageNoticeRow{}, notFound(err, "message notice "+id)
	}
	return n, nil
}
