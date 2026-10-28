package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
)

// GetIssue reads the current aggregate.
func GetIssue(ctx context.Context, q Querier, id string) (protocol.WorkItem, error) {
	var raw string
	if err := q.QueryRowContext(ctx, `SELECT body_json FROM work_items WHERE work_item_id=?`, id).Scan(&raw); err != nil {
		return protocol.WorkItem{}, notFound(err, "work item "+id)
	}
	var w protocol.WorkItem
	err := json.Unmarshal([]byte(raw), &w)
	return w, err
}

// GetIssueRevision reads an immutable historical revision.
func GetIssueRevision(ctx context.Context, q Querier, id string, revision int64) (protocol.WorkItem, error) {
	var raw string
	if err := q.QueryRowContext(ctx, `SELECT body_json FROM work_item_revisions WHERE work_item_id=? AND revision=?`, id, revision).Scan(&raw); err != nil {
		return protocol.WorkItem{}, notFound(err, "work item revision")
	}
	var w protocol.WorkItem
	err := json.Unmarshal([]byte(raw), &w)
	return w, err
}

// ListIssues returns the ledger, optionally filtered by project and status.
func ListIssues(ctx context.Context, q Querier, project, status string) ([]protocol.WorkItem, error) {
	return queryIssues(ctx, q, `SELECT body_json FROM work_items WHERE (?='' OR project=?) AND (?='' OR status=?) ORDER BY updated_at DESC,work_item_id`, project, project, status, status)
}

// ListTaskIssues returns all responsibilities covered by a shared task.
func ListTaskIssues(ctx context.Context, q Querier, taskID string) ([]protocol.WorkItem, error) {
	return queryIssues(ctx, q, `SELECT body_json FROM work_items WHERE work_item_id IN (SELECT work_item_id FROM work_item_task_links WHERE task_id=?) ORDER BY work_item_id`, taskID)
}
func queryIssues(ctx context.Context, q Querier, query string, args ...any) ([]protocol.WorkItem, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list work items: %w", err)
	}
	defer rows.Close()
	out := []protocol.WorkItem{}
	for rows.Next() {
		var raw string
		var w protocol.WorkItem
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &w); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// SaveIssue writes the current plan, immutable revision and stage links in
// the caller's transaction. expected=0 inserts the first revision.
func (t *Tx) SaveIssue(ctx context.Context, w protocol.WorkItem, expected int64, reason any) error {
	if err := RejectCanonicalLegacyIssueWrite(ctx, t.Q(), w.WorkItemID); err != nil {
		return err
	}
	return t.saveIssueProjection(ctx, w, expected, reason)
}

// saveIssueProjection is shared only with the canonical aggregate saver.
func (t *Tx) saveIssueProjection(ctx context.Context, w protocol.WorkItem, expected int64, reason any) error {
	raw, err := json.Marshal(w)
	if err != nil {
		return err
	}
	why, err := json.Marshal(reason)
	if err != nil {
		return err
	}
	if expected == 0 {
		_, err = t.Exec(ctx, `INSERT INTO work_items(work_item_id,project,revision,status,body_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, w.WorkItemID, w.Project, w.Revision, w.Status, string(raw), w.CreatedAt, w.UpdatedAt)
	} else {
		var n int64
		res, e := t.Exec(ctx, `UPDATE work_items SET project=?,revision=?,status=?,body_json=?,updated_at=? WHERE work_item_id=? AND revision=?`, w.Project, w.Revision, w.Status, string(raw), w.UpdatedAt, w.WorkItemID, expected)
		err = e
		if e == nil {
			n, err = res.RowsAffected()
			if err == nil && n != 1 {
				return protocol.Errorf(protocol.CodeStaleRevision, "work item revision changed")
			}
		}
	}
	if err != nil {
		return fmt.Errorf("store: save work item: %w", err)
	}
	if _, err = t.Exec(ctx, `INSERT INTO work_item_revisions(work_item_id,revision,body_json,reason_json,created_at) VALUES(?,?,?,?,?)`, w.WorkItemID, w.Revision, string(raw), string(why), w.UpdatedAt); err != nil {
		return fmt.Errorf("store: record work item revision: %w", err)
	}
	for _, s := range w.Stages {
		for _, id := range s.TaskIDs {
			if _, err = t.Exec(ctx, `INSERT INTO work_item_task_links(work_item_id,stage_id,task_id) VALUES(?,?,?) ON CONFLICT DO NOTHING`, w.WorkItemID, s.StageID, id); err != nil {
				return fmt.Errorf("store: link work item task: %w", err)
			}
		}
	}
	return nil
}
