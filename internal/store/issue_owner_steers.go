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

// IssueOwnerConsumptionMatchesOutbox matches only the ordinary delivery of
// the witnessed original into the same Issue. The caller aliases outbox as o.
const IssueOwnerConsumptionMatchesOutbox = `(o.operation='main_thread.handle_message' AND EXISTS(
 SELECT 1 FROM issue_owner_consumed_inputs i WHERE i.source_message_id=json_extract(o.payload_json,'$.message_id')
 AND o.destination='background_thread:'||i.conversation_id))`

func IssueOwnerInputConsumedForRequest(ctx context.Context, q Querier, request string) (bool, error) {
	var consumed bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM outbox o WHERE o.request_id=? AND `+IssueOwnerConsumptionMatchesOutbox+`)`, request).Scan(&consumed)
	return consumed, err
}

func ConsumedIssueOwnerInput(ctx context.Context, q Querier, source string) (IssueOwnerSteer, error) {
	var message string
	if err := q.QueryRowContext(ctx, `SELECT receipt_message_id FROM issue_owner_consumed_inputs WHERE source_message_id=?`, source).Scan(&message); err != nil {
		return IssueOwnerSteer{}, notFound(err, "consumed Issue owner input")
	}
	return GetIssueOwnerSteer(ctx, q, message)
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
