package store

import (
	"context"
	"encoding/json"
	"github.com/GhostFlying/tend/internal/protocol"
)

func GetIssueDeadline(ctx context.Context, q Querier, id string) (protocol.IssueDeadline, error) {
	var raw string
	err := q.QueryRowContext(ctx, `SELECT body_json FROM issue_deadlines WHERE deadline_id=?`, id).Scan(&raw)
	if err != nil {
		return protocol.IssueDeadline{}, notFound(err, "deadline")
	}
	var d protocol.IssueDeadline
	err = json.Unmarshal([]byte(raw), &d)
	return d, err
}
func (tx *Tx) SaveIssueDeadline(ctx context.Context, d protocol.IssueDeadline, old int64, request string) error {
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	if old == 0 {
		_, err = tx.Exec(ctx, `INSERT INTO issue_deadlines(deadline_id,issue_id,revision,status,body_json,updated_at) VALUES(?,?,?,?,?,?)`, d.DeadlineID, d.IssueID, d.Revision, d.Status, string(raw), d.UpdatedAt)
	} else {
		r, e := tx.Exec(ctx, `UPDATE issue_deadlines SET revision=?,status=?,body_json=?,updated_at=? WHERE deadline_id=? AND revision=?`, d.Revision, d.Status, string(raw), d.UpdatedAt, d.DeadlineID, old)
		err = e
		if err == nil {
			n, e := r.RowsAffected()
			err = e
			if err == nil && n != 1 {
				return protocol.Errorf(protocol.CodeStaleRevision, "deadline changed")
			}
		}
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO issue_deadline_revisions(deadline_id,revision,body_json,request_id) VALUES(?,?,?,?)`, d.DeadlineID, d.Revision, string(raw), request)
	return err
}
func GetPlannerEntry(ctx context.Context, q Querier, id string) (protocol.PlannerEntry, error) {
	var raw string
	err := q.QueryRowContext(ctx, `SELECT body_json FROM planner_entries WHERE entry_id=?`, id).Scan(&raw)
	if err != nil {
		return protocol.PlannerEntry{}, notFound(err, "planner entry")
	}
	var p protocol.PlannerEntry
	err = json.Unmarshal([]byte(raw), &p)
	return p, err
}
func (tx *Tx) SavePlannerEntry(ctx context.Context, p protocol.PlannerEntry, old int64) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if old == 0 {
		_, err = tx.Exec(ctx, `INSERT INTO planner_entries(entry_id,revision,kind,issue_id,attention_id,rank,body_json) VALUES(?,?,?,NULLIF(?,''),NULLIF(?,''),?,?)`, p.EntryID, p.Revision, p.Kind, p.IssueID, p.AttentionID, p.Rank, string(raw))
		return err
	}
	r, err := tx.Exec(ctx, `UPDATE planner_entries SET revision=?,rank=?,body_json=? WHERE entry_id=? AND revision=?`, p.Revision, p.Rank, string(raw), p.EntryID, old)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err == nil && n != 1 {
		return protocol.Errorf(protocol.CodeStaleRevision, "planner entry changed")
	}
	return err
}
