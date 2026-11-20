package journal

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
)

// BeginDelegatedRun commits the startup facts and the local phase together.
// A crash before this commit cannot have launched an agent.
func (j *Journal) BeginDelegatedRun(ctx context.Context, run string, reports []protocol.Envelope) error {
	return j.tx(ctx, func(tx *sql.Tx) error {
		for _, env := range reports {
			if _, err := tx.ExecContext(ctx, `INSERT INTO run_start_reports(run_id,operation,request_id,payload_json) VALUES(?,?,?,?)`, run, env.Operation, env.RequestID, string(env.Payload)); err != nil {
				return err
			}
		}
		res, err := tx.ExecContext(ctx, `UPDATE runs SET phase='started',updated_at=? WHERE run_id=? AND phase='claimed'`, j.now().UnixMilli(), run)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			return fmt.Errorf("journal: Run %s is no longer claimed", run)
		}
		return nil
	})
}

func (j *Journal) StartReports(ctx context.Context, run string) ([]protocol.Envelope, error) {
	rows, err := j.db.QueryContext(ctx, `SELECT operation,request_id,payload_json FROM run_start_reports WHERE run_id=? ORDER BY rowid`, run)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var reports []protocol.Envelope
	for rows.Next() {
		var e protocol.Envelope
		var raw string
		if err := rows.Scan(&e.Operation, &e.RequestID, &raw); err != nil {
			return nil, err
		}
		e.Payload = json.RawMessage(raw)
		if raw == "" {
			e.Payload = nil
		}
		e.Target = protocol.Target{Type: "run", ID: run}
		reports = append(reports, e)
	}
	return reports, rows.Err()
}
func (j *Journal) AckStartReport(ctx context.Context, run, request string) error {
	return j.exec(ctx, `DELETE FROM run_start_reports WHERE run_id=? AND request_id=?`, run, request)
}
func (j *Journal) SetReceivedDesired(ctx context.Context, run string, desired protocol.RunDesired) error {
	return j.exec(ctx, `UPDATE runs SET received_desired=? WHERE run_id=? AND phase<>'done' AND received_desired<>'stopped'`, desired, run)
}
func (j *Journal) ReceivedDesired(ctx context.Context, run string) (protocol.RunDesired, error) {
	var desired protocol.RunDesired
	err := j.db.QueryRowContext(ctx, `SELECT received_desired FROM runs WHERE run_id=?`, run).Scan(&desired)
	return desired, err
}

// LastEventSequence returns the highest locally committed fact, including acknowledged events.
func (j *Journal) LastEventSequence(ctx context.Context, run string) (int64, error) {
	var seq int64
	err := j.db.QueryRowContext(ctx, `SELECT MAX(next_seq-1,COALESCE((SELECT next_seq-1 FROM manager_event_sequences m WHERE m.run_id=r.run_id),0)) FROM runs r WHERE run_id=?`, run).Scan(&seq)
	return seq, err
}
