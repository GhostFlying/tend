package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
)

func IssueProgress(ctx context.Context, q Querier, id string, revision int64) (*protocol.IssueProgress, error) {
	var p protocol.IssueProgress
	var actor string
	err := q.QueryRowContext(ctx, `SELECT summary,next_step,revision,updated_at,updated_by_json FROM issue_progress_revisions WHERE issue_id=? AND revision<=? ORDER BY revision DESC LIMIT 1`, id, revision).Scan(&p.Summary, &p.NextStep, &p.IssueRevision, &p.UpdatedAt, &actor)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: read issue progress: %w", err)
	}
	if err := json.Unmarshal([]byte(actor), &p.UpdatedBy); err != nil {
		return nil, fmt.Errorf("store: decode progress author: %w", err)
	}
	return &p, nil
}

func (t *Tx) InsertIssueProgress(ctx context.Context, id string, p protocol.IssueProgress) error {
	actor, err := json.Marshal(p.UpdatedBy)
	if err != nil {
		return fmt.Errorf("store: encode progress author: %w", err)
	}
	_, err = t.Exec(ctx, `INSERT INTO issue_progress_revisions(issue_id,revision,summary,next_step,updated_at,updated_by_json) VALUES(?,?,?,?,?,?)`, id, p.IssueRevision, p.Summary, p.NextStep, p.UpdatedAt, string(actor))
	if err != nil {
		return fmt.Errorf("store: insert issue progress: %w", err)
	}
	return nil
}
