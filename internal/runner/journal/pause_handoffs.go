package journal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/GhostFlying/tend/internal/protocol"
)

// RecordPause keeps the immutable original stop identity before acting on control.
func (j *Journal) RecordPause(ctx context.Context, run string, p *protocol.PauseHandoff) error {
	if p == nil {
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
		if !as.PauseHandoffs || p.RequestID == "" || p.IssueID == "" || p.IssueRevision <= 0 || p.ControlRevision <= 0 {
			return errors.New("journal: unnegotiated or incomplete pause identity")
		}
		encoded, err := json.Marshal(p)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO pause_handoffs(run_id,identity_json) VALUES(?,?) ON CONFLICT(run_id) DO NOTHING`, run, string(encoded)); err != nil {
			return err
		}
		var saved string
		if err := tx.QueryRowContext(ctx, `SELECT identity_json FROM pause_handoffs WHERE run_id=?`, run).Scan(&saved); err != nil {
			return err
		}
		if saved != string(encoded) {
			return errors.New("journal: pause identity changed")
		}
		return nil
	})
}
func (j *Journal) Pause(ctx context.Context, run string) (*protocol.PauseHandoff, error) {
	var raw string
	err := j.db.QueryRowContext(ctx, `SELECT identity_json FROM pause_handoffs WHERE run_id=?`, run).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p protocol.PauseHandoff
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return nil, err
	}
	return &p, nil
}
