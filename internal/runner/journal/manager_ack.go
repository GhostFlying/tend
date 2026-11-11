package journal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
)

// AcknowledgeManagerReceipt records actual peer acceptance, never native success.
// The compact ID is not a replayable command or a response cache.
func (j *Journal) AcknowledgeManagerReceipt(ctx context.Context, run string, reply protocol.ManagerNativeReply) error {
	if reply.ID == "" || ManagerReadOnly(reply.Method) {
		return nil
	}
	return j.tx(ctx, func(tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE run_id=?)`, run).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return errors.New("journal: manager receipt has no original Run")
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO manager_receipt_ack_ids(run_id,command_id) VALUES(?,?) ON CONFLICT DO NOTHING`, run, reply.ID)
		return err
	})
}
func (j *Journal) ManagerReceiptAcknowledgements(ctx context.Context, run string) ([]string, error) {
	rows, err := j.db.QueryContext(ctx, `SELECT command_id FROM manager_receipt_ack_ids WHERE run_id=? ORDER BY command_id LIMIT 100`, run)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func (j *Journal) ConfirmManagerAcknowledgements(ctx context.Context, run string, ids []string) error {
	return j.tx(ctx, func(tx *sql.Tx) error {
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, `DELETE FROM manager_receipt_ack_ids WHERE run_id=? AND command_id=?`, run, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// ManagerAcknowledgementRuns includes finished Runs: delivery metadata never
// keeps an execution slot occupied and remains retryable after a restart.
func (j *Journal) ManagerAcknowledgementRuns(ctx context.Context) ([]protocol.Assignment, error) {
	rows, err := j.db.QueryContext(ctx, `SELECT assignment_json FROM runs WHERE run_id IN (SELECT run_id FROM manager_receipt_ack_ids) ORDER BY run_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []protocol.Assignment
	for rows.Next() {
		var raw string
		var as protocol.Assignment
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &as); err != nil {
			return nil, err
		}
		out = append(out, as)
	}
	return out, rows.Err()
}

// AcknowledgeResult retains the completed result until the exact original
// final request has succeeded and the actual executor is confirmed stopped.
func (j *Journal) AcknowledgeResult(ctx context.Context, run, request string) error {
	return j.tx(ctx, func(tx *sql.Tx) error {
		var n int
		err := tx.QueryRowContext(ctx, `SELECT count(*) FROM requests q JOIN runs r USING(run_id) WHERE q.run_id=? AND q.request_id=? AND r.stopped_confirmed=1`, run, request).Scan(&n)
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("journal: result acknowledgement does not match stopped Run request")
		}
		_, err = tx.ExecContext(ctx, `UPDATE runs SET result_acknowledged=1 WHERE run_id=?`, run)
		return err
	})
}
