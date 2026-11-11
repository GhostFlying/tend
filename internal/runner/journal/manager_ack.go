package journal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
)

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

// SettleManagerReceipt commits local retirement before a delivery ACK may reach
// the peer. A crash leaves either the original pending reply or its compact ACK.
func (j *Journal) SettleManagerReceipt(ctx context.Context, run string, env protocol.Envelope) error {
	if env.Operation != protocol.OpManagerNativeReply || env.RequestID == "" {
		return errors.New("journal: manager settlement requires its original reply")
	}
	return j.tx(ctx, func(tx *sql.Tx) error {
		var operation, originalRun, target, payload string
		var revision sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT operation,run_id,target_json,payload_json,expected_control_revision FROM requests WHERE request_id=?`, env.RequestID).Scan(&operation, &originalRun, &target, &payload, &revision); err != nil {
			return err
		}
		var original protocol.Envelope
		if err := json.Unmarshal([]byte(target), &original.Target); err != nil {
			return err
		}
		body, err := j.readRequestBody(ctx, operation, payload)
		if err != nil {
			return err
		}
		original.RequestID, original.Operation, original.Payload = env.RequestID, operation, json.RawMessage(body)
		if revision.Valid {
			original.ExpectedControlRevision = &revision.Int64
		}
		before, err := protocol.PayloadHash(&original)
		if err != nil {
			return err
		}
		after, err := protocol.PayloadHash(&env)
		if err != nil {
			return err
		}
		if originalRun != run || before != after {
			return errors.New("journal: manager reply differs from original pending delivery")
		}
		var reply protocol.ManagerNativeReply
		if err := json.Unmarshal(original.Payload, &reply); err != nil {
			return err
		}
		if err := enqueueManagerReceiptAcknowledgement(ctx, tx, run, reply); err != nil {
			return err
		}
		return j.deleteRequest(ctx, tx, env.RequestID)
	})
}

func enqueueManagerReceiptAcknowledgement(ctx context.Context, tx *sql.Tx, run string, reply protocol.ManagerNativeReply) error {
	if reply.ID == "" {
		return errors.New("journal: manager reply has no command identity")
	}
	if ManagerReadOnly(reply.Method) {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO manager_receipt_ack_ids(run_id,command_id) VALUES(?,?) ON CONFLICT DO NOTHING`, run, reply.ID)
	return err
}
