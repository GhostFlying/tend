package app

import (
	"context"

	"github.com/GhostFlying/tend/internal/store"
)

// A cancelled source retires only certainly-unsent transport work. Sending and
// unknown attempts retain their receipts and are never declared undone.
func cancelPendingCommunicationNotices(ctx context.Context, x *Exec, tx *store.Tx, rule string) error {
	ids, err := store.FollowupIDs(ctx, tx.Q(), `SELECT DISTINCT d.message_id FROM messaging_deliveries d JOIN conversation_bindings b ON b.message_id=d.message_id JOIN outbox o ON o.outbox_id=d.delivery_id WHERE b.object_type='followup' AND b.object_id=? AND o.status='pending'`, rule)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err = tx.Exec(ctx, `UPDATE messaging_deliveries SET state='cancelled',reason='source_superseded',updated_at=? WHERE message_id=? AND delivery_id IN (SELECT outbox_id FROM outbox WHERE status='pending')`, x.Now.UnixMilli(), id); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE outbox SET status='cancelled',updated_at=? WHERE status='pending' AND outbox_id IN (SELECT delivery_id FROM messaging_deliveries WHERE message_id=? AND state='cancelled')`, x.Now.UnixMilli(), id); err != nil {
			return err
		}
		if err = communicationEvent(ctx, x, tx, id, "messaging.delivery"); err != nil {
			return err
		}
	}
	return nil
}
