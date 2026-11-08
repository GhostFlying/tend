package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/GhostFlying/tend/internal/protocol"
)

func GetIssueDeliveryDuty(ctx context.Context, q Querier, issue, task string) (protocol.IssueDeliveryDuty, error) {
	var d protocol.IssueDeliveryDuty
	var raw string
	var accepted sql.NullString
	err := q.QueryRowContext(ctx, `SELECT task_id,event_id,acceptance_id,revision,status,body_json FROM issue_delivery_duties WHERE issue_id=? AND task_id=?`, issue, task).Scan(&d.TaskID, &d.EventID, &accepted, &d.Revision, &d.Status, &raw)
	if err != nil {
		return d, notFound(err, "delivery duty")
	}
	d.AcceptanceID = accepted.String
	if !accepted.Valid {
		var artifact sql.NullString
		if err := q.QueryRowContext(ctx, `SELECT artifact_id FROM issue_delivery_duties WHERE issue_id=? AND task_id=?`, issue, task).Scan(&artifact); err != nil {
			return d, err
		}
		d.ArtifactID = artifact.String
	}
	var body protocol.IssueDeliveryDuty
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		return d, err
	}
	d.Authorization = body.Authorization
	d.PolicyAttentionID = body.PolicyAttentionID
	d.Candidate, d.AttentionID, d.ExecutionTaskID, d.Reason, d.Evidence, d.AssignmentEpoch = body.Candidate, body.AttentionID, body.ExecutionTaskID, body.Reason, body.Evidence, body.AssignmentEpoch
	return d, nil
}

func ListIssueDeliveryDuties(ctx context.Context, q Querier, issue string) ([]protocol.IssueDeliveryDuty, error) {
	ids, err := FollowupIDs(ctx, q, `SELECT task_id FROM issue_delivery_duties WHERE issue_id=? ORDER BY task_id`, issue)
	if err != nil {
		return nil, err
	}
	out := []protocol.IssueDeliveryDuty{}
	for _, id := range ids {
		d, err := GetIssueDeliveryDuty(ctx, q, issue, id)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

func (t *Tx) PutIssueDeliveryDuty(ctx context.Context, issue string, d protocol.IssueDeliveryDuty) error {
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	_, err = t.Exec(ctx, `UPDATE issue_delivery_duties SET revision=?,status=?,body_json=? WHERE issue_id=? AND task_id=?`, d.Revision, d.Status, string(raw), issue, d.TaskID)
	return err
}
