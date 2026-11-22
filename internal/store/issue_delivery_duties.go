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
	d.InstanceID, d.PreviousExecution = body.InstanceID, body.PreviousExecution
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

// DecisionHasDeliveryDuty identifies the original decision governed by a delivery duty.
func DecisionHasDeliveryDuty(ctx context.Context, q Querier, decisionID string) (bool, error) {
	var found bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1
 FROM issue_delivery_duties duty JOIN attentions a ON a.attention_id=json_extract(duty.body_json,'$.attention_id')
 WHERE a.decision_id=?)`, decisionID).Scan(&found)
	return found, err
}

// HasRecordedDeliveryAnswer keeps historical display provenance when the current
// duty moves on to another question. It does not authorize a new answer.
func HasRecordedDeliveryAnswer(ctx context.Context, q Querier, decisionID string) (bool, error) {
	var found bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM issue_delivery_answers WHERE decision_id=?
 )`, decisionID).Scan(&found)
	return found, err
}

// SettledSelfDeliveryExecution identifies the predecessor using the artifact
// named in the settlement, independently of the Task's mutable result pointer.
func SettledSelfDeliveryExecution(ctx context.Context, q Querier, issue string, d protocol.IssueDeliveryDuty) (*protocol.DeliveryExecutionSelector, string, error) {
	if d.Status != "complete" || d.ExecutionTaskID != d.TaskID || d.Candidate == nil {
		return nil, "", nil
	}
	for _, evidence := range d.Evidence {
		var ref protocol.DeliveryExecutionSelector
		var artifact string
		var err error
		switch evidence.Kind {
		case protocol.WorkItemEvidenceResult:
			err = q.QueryRowContext(ctx, `SELECT a.run_id,e.duty_revision,a.artifact_id FROM artifacts a JOIN issue_delivery_executions e USING(task_id)
 WHERE e.issue_id=? AND e.source_task_id=a.task_id AND a.task_id=? AND a.artifact_id=?`,
				issue, d.TaskID, evidence.Reference).Scan(&ref.ExecutionRunID, &ref.DutyRevision, &artifact)
		case protocol.WorkItemEvidenceAcceptance:
			err = q.QueryRowContext(ctx, `SELECT a.run_id,e.duty_revision,a.artifact_id FROM acceptances v JOIN artifacts a USING(artifact_id) JOIN issue_delivery_executions e ON e.task_id=a.task_id
 WHERE e.issue_id=? AND e.source_task_id=a.task_id AND a.task_id=? AND v.acceptance_id=? AND v.verdict='passed'`,
				issue, d.TaskID, evidence.Reference).Scan(&ref.ExecutionRunID, &ref.DutyRevision, &artifact)
		default:
			continue
		}
		if err == sql.ErrNoRows {
			return nil, "", nil
		}
		return &ref, artifact, err
	}
	return nil, "", nil
}
