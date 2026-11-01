package store

import (
	"context"
	"encoding/json"

	"github.com/GhostFlying/tend/internal/protocol"
)

func (t *Tx) InsertRunPreparationFailure(ctx context.Context, run string, failure protocol.RunPreparationFailure, now int64) error {
	b, err := json.Marshal(failure)
	if err != nil {
		return err
	}
	_, err = t.Exec(ctx, `INSERT INTO run_preparation_failures(run_id,failure_json,created_at) VALUES(?,?,?)`, run, string(b), now)
	return err
}

func RunPreparationFailure(ctx context.Context, q Querier, run string) (*protocol.RunPreparationFailure, error) {
	var raw string
	if err := q.QueryRowContext(ctx, `SELECT failure_json FROM run_preparation_failures WHERE run_id=?`, run).Scan(&raw); err != nil {
		return nil, notFound(err, "run preparation failure")
	}
	var failure protocol.RunPreparationFailure
	if err := json.Unmarshal([]byte(raw), &failure); err != nil {
		return nil, err
	}
	return &failure, nil
}
