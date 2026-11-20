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
	rows, err := j.db.QueryContext(ctx, startReportQuery+" ORDER BY rowid", run)
	return scanStartReports(run, rows, err)
}

const startReportQuery = `SELECT operation,request_id,payload_json FROM run_start_reports WHERE run_id=?1`

func scanStartReports(run string, rows *sql.Rows, err error) ([]protocol.Envelope, error) {
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

// ArchiveRetiredStartReports prepares facts from a physically retired journal.
// Its successor may transmit observations but cannot replay startup commands.
func (j *Journal) ArchiveRetiredStartReports(ctx context.Context, run string) error {
	return j.tx(ctx, func(tx *sql.Tx) error { return j.archiveClosedStartReports(ctx, tx, run) })
}

// Once a Run settles, its live startup commands can no longer change server
// state. Preserve undelivered envelopes as observations under the original Run.
func (j *Journal) archiveClosedStartReports(ctx context.Context, tx *sql.Tx, run string) error {
	rows, err := tx.QueryContext(ctx, startReportQuery+` AND EXISTS(
 SELECT 1 FROM runs WHERE run_id=?1 AND json_extract(assignment_json,'$.event_sync')=1) ORDER BY rowid`, run)
	reports, err := scanStartReports(run, rows, err)
	if err != nil {
		return err
	}
	for _, env := range reports {
		raw, err := json.Marshal(env)
		if err != nil {
			return err
		}
		if err := j.appendEvent(ctx, tx, run, protocol.RunEvent{NativeID: env.RequestID, Kind: "runner.startup_observation", Body: raw}); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM run_start_reports WHERE run_id=? AND request_id=?`, run, env.RequestID); err != nil {
			return err
		}
	}
	return nil
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
