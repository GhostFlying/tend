package store

import (
	"context"
	"encoding/json"

	"github.com/GhostFlying/tend/internal/protocol"
)

type IssueOwnerSteer struct {
	protocol.IssueOwnerSteerView
	IssueRevision int64
	ExecutionID   string
	InputHeader   string
	InputSHA256   string
}

func GetIssueOwnerSteer(ctx context.Context, q Querier, message string) (IssueOwnerSteer, error) {
	var s IssueOwnerSteer
	var sender string
	err := q.QueryRowContext(ctx, `SELECT message_id,issue_id,issue_revision,assignment_epoch,task_id,run_id,
 execution_id,thread_id,turn_id,source_message_id,source_revision,COALESCE(revokes_source_message_id,''),
 sender_json,input_header,input_sha256,status,reason,accepted_at,COALESCE(native_request_id,''),native_item_id
 FROM issue_owner_steers WHERE message_id=?`, message).Scan(
		&s.MessageID, &s.IssueID, &s.IssueRevision, &s.AssignmentEpoch, &s.TaskID, &s.RunID,
		&s.ExecutionID, &s.ThreadID, &s.TurnID, &s.SourceMessageID, &s.SourceRevision, &s.RevokesSourceMessageID,
		&sender, &s.InputHeader, &s.InputSHA256, &s.Status, &s.Reason, &s.AcceptedAt, &s.NativeRequestID, &s.NativeItemID)
	if err != nil {
		return s, notFound(err, "issue owner steer")
	}
	return s, json.Unmarshal([]byte(sender), &s.Sender)
}

func (t *Tx) InsertIssueOwnerSteer(ctx context.Context, s IssueOwnerSteer) error {
	sender, err := json.Marshal(s.Sender)
	if err != nil {
		return err
	}
	_, err = t.Exec(ctx, `INSERT INTO issue_owner_steers(message_id,issue_id,issue_revision,assignment_epoch,
 task_id,run_id,execution_id,thread_id,turn_id,source_message_id,source_revision,revokes_source_message_id,
 sender_json,input_header,input_sha256,status) VALUES(?,?,?,?,?,?,?,?,?,?,?,NULLIF(?,''),?,?,?,'queued')`,
		s.MessageID, s.IssueID, s.IssueRevision, s.AssignmentEpoch, s.TaskID, s.RunID, s.ExecutionID,
		s.ThreadID, s.TurnID, s.SourceMessageID, s.SourceRevision, s.RevokesSourceMessageID, string(sender), s.InputHeader, s.InputSHA256)
	return err
}
