package journal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/GhostFlying/tend/internal/protocol"
)

// RecordExternalWait persists the server receipt before stopping native work.
func (j *Journal) RecordExternalWait(ctx context.Context, run string, v *protocol.ExternalWait) error {
	if v == nil {
		return nil
	}
	return j.tx(ctx, func(tx *sql.Tx) error {
		var raw string
		if err := tx.QueryRowContext(ctx, `SELECT assignment_json FROM runs WHERE run_id=?`, run).Scan(&raw); err != nil {
			return err
		}
		var as protocol.Assignment
		if err := json.Unmarshal([]byte(raw), &as); err != nil {
			return err
		}
		if !as.ExternalWaits || v.RunID != run || v.TaskID != as.TaskID || v.WaitID == "" {
			return errors.New("journal: unnegotiated external wait identity")
		}
		var prior string
		err := tx.QueryRowContext(ctx, `SELECT identity_json FROM external_waits WHERE run_id=?`, run).Scan(&prior)
		if err == nil {
			var old protocol.ExternalWait
			if err := json.Unmarshal([]byte(prior), &old); err != nil {
				return err
			}
			if old.WaitID != v.WaitID || old.WaitExternalPayload != v.WaitExternalPayload {
				return errors.New("journal: external wait identity changed")
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		encoded, err := json.Marshal(v)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO external_waits(run_id,identity_json) VALUES(?,?)`, run, string(encoded))
		return err
	})
}
func (j *Journal) ExternalWait(ctx context.Context, run string) (*protocol.ExternalWait, error) {
	var raw string
	err := j.db.QueryRowContext(ctx, `SELECT identity_json FROM external_waits WHERE run_id=?`, run).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out protocol.ExternalWait
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (j *Journal) SessionStopReport(ctx context.Context, run string) (*protocol.Envelope, error) {
	var raw string
	err := j.db.QueryRowContext(ctx, `SELECT envelope_json FROM session_stop_reports WHERE run_id=?`, run).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var env protocol.Envelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		return nil, err
	}
	return &env, nil
}
func (j *Journal) SaveSessionStopReport(ctx context.Context, run string, env *protocol.Envelope) error {
	return j.tx(ctx, func(tx *sql.Tx) error {
		var stopped bool
		if err := tx.QueryRowContext(ctx, `SELECT stopped_confirmed FROM runs WHERE run_id=?`, run).Scan(&stopped); err != nil {
			return err
		}
		if !stopped {
			return errors.New("session close report requires physical stop proof")
		}
		raw, err := json.Marshal(env)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO session_stop_reports(run_id,envelope_json) VALUES(?,?) ON CONFLICT(run_id) DO NOTHING`, run, string(raw))
		return err
	})
}
