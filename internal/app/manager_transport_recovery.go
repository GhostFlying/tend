package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func recordManagerTransportStop(ctx context.Context, x *Exec, tx *store.Tx, r store.RunRow, reason string) error {
	if reason != protocol.ManagerStoppedTransportLease || r.Protocol != 6 || x.Principal.Kind != protocol.PrincipalRun ||
		r.DesiredState != protocol.DesiredRun || r.PauseHandoff != nil {
		return nil
	}
	closeID, _, current, err := store.ManagerNativeClose(ctx, tx.Q(), r.RunID)
	if err != nil || !current || closeID != x.Env.RequestID {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO manager_transport_recoveries
		(original_run_id,task_id,stop_request_id,assignment_epoch,issue_revision,task_revision)
		SELECT r.run_id,m.task_id,?,m.assignment_epoch,w.revision,t.control_revision
		FROM runs r JOIN leases l USING(run_id) JOIN issue_manager_tasks m ON m.task_id=r.task_id
		JOIN issue_controls i ON i.issue_id=m.issue_id JOIN work_items w ON w.work_item_id=i.issue_id
		JOIN tasks t ON t.task_id=m.task_id
		WHERE r.run_id=? AND l.status='released' AND m.state='assigned'
		AND i.state='active' AND i.assignment_state='active' AND i.work_status<>'backlog' AND i.assignment_epoch=m.assignment_epoch
		AND NOT EXISTS(SELECT 1 FROM manager_transport_recoveries prior
			WHERE prior.task_id=r.task_id AND prior.successor_generation=r.generation)
		ON CONFLICT(original_run_id) DO NOTHING`, closeID, r.RunID)
	return err
}

// queueManagerTransportRecovery runs after ordinary receipt reconciliation
// and before the generation is dispatched. The wake carries provenance for
// a fresh judgment on the same Task, without resubmitting the original turn.
func queueManagerTransportRecovery(ctx context.Context, x *Exec, tx *store.Tx, m store.IssueManagerTask, i protocol.Issue, task store.TaskRow, generation int64) (bool, error) {
	if i.State != protocol.IssueActive || i.AssignmentState != "active" || i.BoardStatus() == protocol.IssueBacklog || task.Lifecycle.Terminal() {
		return false, nil
	}
	var runID, stopID, executionID, logicalID string
	err := tx.Q().QueryRowContext(ctx, `SELECT r.run_id,c.stop_request_id,h.execution_id,COALESCE(le.logical_turn_id,'')
		FROM manager_transport_recoveries c JOIN runs r ON r.run_id=c.original_run_id
		JOIN leases l USING(run_id)
		JOIN hosted_executions h ON json_extract(h.private_snapshot,'$.runner_run_id')=r.run_id
		JOIN main_turns mt ON mt.request_id=h.execution_id
		LEFT JOIN logical_turn_executions le ON le.request_id=h.execution_id
		LEFT JOIN logical_turns lt ON lt.logical_turn_id=le.logical_turn_id
		LEFT JOIN conversation_message_controls mc ON mc.message_id=mt.message_id
		WHERE c.task_id=? AND c.successor_generation IS NULL AND c.assignment_epoch=?
		AND c.issue_revision=? AND c.task_revision=?
		AND r.generation=? AND r.observed_state='failed' AND r.desired_state='run' AND l.status='released'
		AND r.native_closed_at>0 AND r.native_close_activity=r.native_activity AND r.native_close_id=c.stop_request_id
		AND h.issue_id=? AND h.assignment_epoch=c.assignment_epoch AND h.conversation_id=?
		AND mt.status='interrupted' AND mt.turn_id<>'' AND COALESCE(mc.withdrawn_at,0)=0
		AND (lt.logical_turn_id IS NULL OR (lt.resumed_by IS NULL AND lt.status NOT IN ('stopped','awaiting_clarification','unknown')))
		AND NOT EXISTS(SELECT 1 FROM outbox o WHERE o.request_id=h.execution_id AND o.status IN ('sending','unknown'))
		AND NOT EXISTS(SELECT 1 FROM manager_native_requests n WHERE n.run_id=r.run_id
			AND n.status IN ('pending','started','unknown') AND n.method NOT IN ('models','find','usage'))
		AND NOT EXISTS(SELECT 1 FROM hosted_executions newer JOIN main_turns nt ON nt.request_id=newer.execution_id
			WHERE json_extract(newer.private_snapshot,'$.runner_run_id')=r.run_id AND nt.rowid>mt.rowid)
		ORDER BY mt.rowid DESC LIMIT 1`, task.TaskID, m.AssignmentEpoch, i.Revision, task.ControlRevision, generation-1,
		m.IssueID, m.ConversationID).Scan(&runID, &stopID, &executionID, &logicalID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	const kind = "run.manager_transport_recovery"
	eventID := x.IDs.New(protocol.PrefixEvent)
	body, err := json.Marshal(map[string]any{"original_run_id": runID, "stop_request_id": stopID,
		"execution_id": executionID, "logical_turn_id": logicalID, "successor_generation": generation})
	if err != nil {
		return false, err
	}
	now := x.Now.UnixMilli()
	if _, err := tx.InsertEvent(ctx, store.EventRow{EventID: eventID, Source: "system", ObjectType: "run",
		ObjectID: runID, Kind: kind, BodyJSON: string(body), RequestID: x.Env.RequestID, CreatedAt: now}); err != nil {
		return false, err
	}
	messageID := x.IDs.New(protocol.PrefixMessage)
	message := fmt.Sprintf("Issue manager Run %s stopped after losing transport. Its original turn %s has settled. Read the current Issue, source authorization and original receipts before continuing any remaining work. Do not repeat the original input or tool calls. This is a system wake, with no new owner authorization.", runID, executionID)
	if _, err := tx.InsertMessage(ctx, store.MessageRow{ConversationID: m.ConversationID, MessageID: messageID,
		Author: "system", Body: message, CreatedAt: now}); err != nil {
		return false, err
	}
	wakeRequestID := x.IDs.New(protocol.PrefixRequest)
	if err := queueSingleEventDelivery(ctx, x, tx, wakeRequestID, messageID, eventID, kind, nil); err != nil {
		return false, err
	}
	_, err = tx.Exec(ctx, `UPDATE manager_transport_recoveries SET successor_generation=?,wake_request_id=?,original_execution_id=? WHERE original_run_id=?`, generation, wakeRequestID, executionID, runID)
	return err == nil, err
}
