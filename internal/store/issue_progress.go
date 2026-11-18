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
	var actor, delivery string
	err := q.QueryRowContext(ctx, `SELECT summary,next_step,revision,updated_at,updated_by_json,delivery_json FROM issue_progress_revisions WHERE issue_id=? AND revision<=? ORDER BY revision DESC LIMIT 1`, id, revision).Scan(&p.Summary, &p.NextStep, &p.IssueRevision, &p.UpdatedAt, &actor, &delivery)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: read issue progress: %w", err)
	}
	if err := json.Unmarshal([]byte(actor), &p.UpdatedBy); err != nil {
		return nil, fmt.Errorf("store: decode progress author: %w", err)
	}
	if err := json.Unmarshal([]byte(delivery), &p.Delivery); err != nil {
		return nil, fmt.Errorf("store: decode progress candidate: %w", err)
	}
	return &p, nil
}

func (t *Tx) InsertIssueProgress(ctx context.Context, id string, p protocol.IssueProgress) error {
	actor, err := json.Marshal(p.UpdatedBy)
	if err != nil {
		return fmt.Errorf("store: encode progress author: %w", err)
	}
	delivery, err := json.Marshal(p.Delivery)
	if err != nil {
		return fmt.Errorf("store: encode progress candidate: %w", err)
	}
	_, err = t.Exec(ctx, `INSERT INTO issue_progress_revisions(issue_id,revision,summary,next_step,updated_at,updated_by_json,delivery_json) VALUES(?,?,?,?,?,?,?)`, id, p.IssueRevision, p.Summary, p.NextStep, p.UpdatedAt, string(actor), string(delivery))
	if err != nil {
		return fmt.Errorf("store: insert issue progress: %w", err)
	}
	return nil
}

// IssueConflictProgress returns the strongest public action by the current
// responsibility for this specific observed conflict.
func IssueConflictProgress(ctx context.Context, q Querier, issue protocol.Issue, d protocol.IssueDelivery, c protocol.DeliveryConflict) (*protocol.IssueProgress, error) {
	var revision int64
	err := q.QueryRowContext(ctx, `SELECT p.revision
 FROM issue_progress_revisions p
 JOIN issue_control_revisions r ON r.issue_id=p.issue_id AND r.revision=p.revision
 WHERE p.issue_id=? AND p.revision<=? AND json_extract(r.snapshot_json,'$.control.assignment_epoch')=?
 AND json_extract(p.delivery_json,'$.delivery_id')=? AND json_extract(p.delivery_json,'$.provider')=?
 AND json_extract(p.delivery_json,'$.repository')=? AND json_extract(p.delivery_json,'$.number')=?
 AND json_extract(p.delivery_json,'$.conflict_revision')=?
 AND json_extract(p.delivery_json,'$.conflict_action') IN ('seen','working')
 ORDER BY json_extract(p.delivery_json,'$.conflict_action')='working' DESC,p.revision DESC LIMIT 1`,
		issue.IssueID, issue.Revision, issue.AssignmentEpoch, d.DeliveryID, d.Provider, d.Repository, d.Number, c.Revision).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: read conflict progress: %w", err)
	}
	return IssueProgress(ctx, q, issue.IssueID, revision)
}
