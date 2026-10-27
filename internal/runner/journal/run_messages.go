package journal

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
)

type MessageDelivery struct {
	MessageID string
	RunID     string
	Claim     protocol.Envelope
	Invoked   bool
	Report    *protocol.Envelope
}

func (j *Journal) MessageDelivery(ctx context.Context, run, id string) (MessageDelivery, error) {
	var d MessageDelivery
	var claim, report string
	err := j.db.QueryRowContext(ctx, `SELECT message_id,run_id,claim_json,invoked,report_json FROM run_message_deliveries WHERE run_id=? AND message_id=?`, run, id).Scan(&d.MessageID, &d.RunID, &claim, &d.Invoked, &report)
	if err != nil {
		return d, err
	}
	if err := json.Unmarshal([]byte(claim), &d.Claim); err != nil {
		return d, err
	}
	if report != "" {
		d.Report = &protocol.Envelope{}
		if err := json.Unmarshal([]byte(report), d.Report); err != nil {
			return d, err
		}
	}
	return d, nil
}

func (j *Journal) SaveMessageIntent(ctx context.Context, d MessageDelivery) error {
	claim, err := json.Marshal(d.Claim)
	if err != nil {
		return err
	}
	_, err = j.db.ExecContext(ctx, `INSERT INTO run_message_deliveries(message_id,run_id,claim_json) VALUES (?,?,?)`, d.MessageID, d.RunID, string(claim))
	return err
}

func (j *Journal) MarkMessageInvoked(ctx context.Context, id string) error {
	res, err := j.db.ExecContext(ctx, `UPDATE run_message_deliveries SET invoked=1 WHERE message_id=? AND invoked=0 AND settled=0`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n != 1 {
		return fmt.Errorf("journal: message invocation already recorded or absent")
	}
	return err
}

// SaveMessageReceipt keeps the first receipt and its request identity even when
// completion recovery races an upload retry. Return that durable envelope.
func (j *Journal) SaveMessageReceipt(ctx context.Context, id string, env protocol.Envelope) (protocol.Envelope, error) {
	body, err := json.Marshal(env)
	if err != nil {
		return protocol.Envelope{}, err
	}
	var saved string
	if err := j.db.QueryRowContext(ctx, `UPDATE run_message_deliveries SET report_json=CASE WHEN report_json='' THEN ? ELSE report_json END WHERE message_id=? RETURNING report_json`, string(body), id).Scan(&saved); err != nil {
		return protocol.Envelope{}, err
	}
	var original protocol.Envelope
	err = json.Unmarshal([]byte(saved), &original)
	return original, err
}

// MessageReceiptRuns includes final and done runs that ordinary hello omits.
// Only durable receipts are uploaded here; no execution or claim is resumed.
func (j *Journal) MessageReceiptRuns(ctx context.Context) ([]protocol.Assignment, error) {
	rows, err := j.db.QueryContext(ctx, `SELECT assignment_json FROM runs WHERE phase IN ('worker_done','submitting','releasing','done') AND EXISTS (SELECT 1 FROM run_message_deliveries d WHERE d.run_id=runs.run_id AND d.settled=0 AND d.report_json<>'') ORDER BY run_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var assignments []protocol.Assignment
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		var as protocol.Assignment
		if err := json.Unmarshal([]byte(body), &as); err != nil {
			return nil, err
		}
		assignments = append(assignments, as)
	}
	return assignments, rows.Err()
}

func (j *Journal) PendingMessageDeliveryCount(ctx context.Context) (int64, error) {
	var count int64
	err := j.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_message_deliveries WHERE settled=0`).Scan(&count)
	return count, err
}

func (j *Journal) SettleMessageDelivery(ctx context.Context, id string) error {
	_, err := j.db.ExecContext(ctx, `UPDATE run_message_deliveries SET settled=1 WHERE message_id=?`, id)
	return err
}

func (j *Journal) PendingMessageDeliveries(ctx context.Context, run string) ([]MessageDelivery, error) {
	rows, err := j.db.QueryContext(ctx, `SELECT message_id,run_id,claim_json,invoked,report_json FROM run_message_deliveries WHERE run_id=? AND settled=0 ORDER BY rowid`, run)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MessageDelivery
	for rows.Next() {
		var d MessageDelivery
		var claim, report string
		if err := rows.Scan(&d.MessageID, &d.RunID, &claim, &d.Invoked, &report); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(claim), &d.Claim); err != nil {
			return nil, err
		}
		if report != "" {
			d.Report = &protocol.Envelope{}
			if err := json.Unmarshal([]byte(report), d.Report); err != nil {
				return nil, err
			}
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
