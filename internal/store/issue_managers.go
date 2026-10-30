package store

import (
	"context"
	"encoding/json"

	"github.com/GhostFlying/tend/internal/protocol"
)

// IssueManagerTask records continuing responsibility independently of a run lease.
type IssueManagerTask struct {
	HandoffID       string                    `json:"handoff_id,omitempty"`
	TaskID          string                    `json:"task_id"`
	IssueID         string                    `json:"issue_id"`
	CategoryID      string                    `json:"category_id"`
	ConversationID  string                    `json:"conversation_id,omitempty"`
	HostID          string                    `json:"host_id,omitempty"`
	SourceMessageID string                    `json:"source_message_id,omitempty"`
	ExpectedEpoch   int64                     `json:"expected_assignment_epoch"`
	AssignmentEpoch int64                     `json:"assignment_epoch,omitempty"`
	State           string                    `json:"state"`
	Pin             *protocol.RunExecutionPin `json:"execution,omitempty"`
}

func GetIssueManagerTask(ctx context.Context, q Querier, task string) (IssueManagerTask, error) {
	var m IssueManagerTask
	var raw string
	err := q.QueryRowContext(ctx, `SELECT task_id,issue_id,category_id,expected_epoch,COALESCE(assignment_epoch,0),COALESCE(conversation_id,''),COALESCE(host_id,''),COALESCE(pin_json,''),state,source_message_id,handoff_id FROM issue_manager_tasks WHERE task_id=?`, task).Scan(&m.TaskID, &m.IssueID, &m.CategoryID, &m.ExpectedEpoch, &m.AssignmentEpoch, &m.ConversationID, &m.HostID, &raw, &m.State, &m.SourceMessageID, &m.HandoffID)
	if err != nil {
		return m, notFound(err, "manager task")
	}
	if raw != "" {
		m.Pin = new(protocol.RunExecutionPin)
		err = json.Unmarshal([]byte(raw), m.Pin)
	}
	return m, err
}

func CurrentIssueManagerTask(ctx context.Context, q Querier, issue string) (IssueManagerTask, error) {
	var task string
	if err := q.QueryRowContext(ctx, `SELECT task_id FROM issue_manager_tasks WHERE issue_id=? AND state IN ('pending','assigned')`, issue).Scan(&task); err != nil {
		return IssueManagerTask{}, notFound(err, "current manager task")
	}
	return GetIssueManagerTask(ctx, q, task)
}

func LatestIssueManagerTask(ctx context.Context, q Querier, issue string) (IssueManagerTask, error) {
	var task string
	if err := q.QueryRowContext(ctx, `SELECT task_id FROM issue_manager_tasks WHERE issue_id=? ORDER BY created_at DESC,rowid DESC LIMIT 1`, issue).Scan(&task); err != nil {
		return IssueManagerTask{}, notFound(err, "latest manager task")
	}
	return GetIssueManagerTask(ctx, q, task)
}

// IssueManagerAuthorizationValid compares the frozen publication source with
// current facts. Recovery receipts remain readable independently of this gate.
func IssueManagerAuthorizationValid(ctx context.Context, q Querier, task string) (bool, error) {
	raw, err := GetSpec(ctx, q, task, 1)
	if err != nil {
		return false, err
	}
	var spec protocol.IssueManagerSpec
	if err = json.Unmarshal([]byte(raw), &spec); err != nil {
		return false, err
	}
	if spec.SourceMessageID == "" {
		return true, nil
	}
	var valid bool
	err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_messages m LEFT JOIN message_dispositions d USING(message_id) LEFT JOIN message_association_revisions a USING(message_id) LEFT JOIN conversation_message_controls c USING(message_id) WHERE m.message_id=? AND m.kind='message' AND COALESCE(d.content_revision,1)=? AND (COALESCE(a.revision,0)=? OR EXISTS(SELECT 1 FROM message_work_items s WHERE s.message_id=m.message_id AND s.work_item_id=? AND s.active=1)) AND COALESCE(c.withdrawn_at,0)=0)`, spec.SourceMessageID, spec.SourceContentRevision, spec.SourceAssociationRevision, spec.IssueID).Scan(&valid)
	return valid, err
}
