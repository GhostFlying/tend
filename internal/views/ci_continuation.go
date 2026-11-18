package views

import (
	"context"
	"database/sql"
	"errors"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// OwnerCIContinuation reads one original owner's stopped checks wait. Older
// waits without source identity cannot authorize a replacement notification.
func OwnerCIContinuation(ctx context.Context, q store.Querier, taskID, runID string) (protocol.CIContinuation, error) {
	f := protocol.CIContinuation{TaskID: taskID, State: "unverified"}
	if runID == "latest" {
		err := q.QueryRowContext(ctx, `SELECT run_id FROM run_external_waits WHERE task_id=? ORDER BY rowid DESC LIMIT 1`, taskID).Scan(&runID)
		if errors.Is(err, sql.ErrNoRows) {
			return f, nil
		}
		if err != nil {
			return f, err
		}
	}
	w, err := store.GetExternalWait(ctx, q, runID)
	if err != nil || w == nil || w.TaskID != taskID || w.Kind != "checks" || !w.Stopped || w.Status != "continued" || w.WakeReason != "checks_"+w.ObservedState || !slices.Contains([]string{"success", "failure", "cancelled", "skipped"}, w.ObservedState) {
		return f, err
	}
	f.IssueID, f.RunID, f.WaitID, f.DeliveryID = w.IssueID, w.RunID, w.WaitID, w.TargetID
	f.Head, f.Result, f.SourceMessage, f.SourceEvent = w.ObservedHead, w.ObservedState, w.SourceMessage, w.SourceEvent
	f.Base, f.Target, f.NoticeRestored = w.ObservedBase, w.ObservedTarget, w.NoticeRestored
	if w.SourceEvent == "" || w.SourceMessage == "" {
		return f, nil
	}

	m, err := store.GetIssueManagerTask(ctx, q, taskID)
	if errors.Is(err, store.ErrNotFound) {
		return f, nil
	}
	if err != nil || m.IssueID != w.IssueID {
		return f, err
	}
	var batch, turn string
	var carrier bool
	err = q.QueryRowContext(ctx, `SELECT b.status,EXISTS(
        SELECT 1 FROM outbox o WHERE o.destination=? AND o.operation='main_thread.handle_event'
        AND o.status IN ('pending','sending') AND json_extract(o.payload_json,'$.message_id')=b.message_id),COALESCE((
        SELECT t.turn_id FROM main_turns t
        JOIN runner_coordination_executions e ON e.execution_id=t.request_id
        JOIN runs r ON r.run_id=json_extract(e.private_snapshot,'$.runner_run_id')
        JOIN background_reads rd ON rd.batch_id=s.batch_id AND rd.source_seq=s.seq AND rd.thread_id=t.thread_id AND rd.turn_id=t.turn_id
        WHERE t.message_id=b.message_id AND t.status='completed' AND b.status='completed' AND e.issue_id=? AND e.assignment_epoch=? AND r.task_id=?
        AND NOT EXISTS(SELECT 1 FROM native_turn_receipts_pending receipt WHERE receipt.request_id=t.request_id)
        AND (EXISTS(
            SELECT 1 FROM message_work_items scope JOIN messaging_messages publication USING(message_id)
            JOIN messaging_sources source USING(message_id)
            WHERE scope.work_item_id=e.issue_id AND scope.active=1 AND publication.execution_id=t.request_id
              AND publication.direction='notification' AND source.source_message_id=s.message_id)
          OR (json_extract(NULLIF(b.checkin_json,''),'$.turn_id')=t.turn_id AND
              json_extract(NULLIF(b.checkin_json,''),'$.thread_id')=t.thread_id) OR EXISTS (
            SELECT 1 FROM conversation_messages publication JOIN messaging_messages reply USING(message_id)
            WHERE publication.reply_to IN (s.message_id,b.message_id) AND reply.execution_id=t.request_id AND reply.direction='publication'
            AND (EXISTS(SELECT 1 FROM messaging_sources ms WHERE ms.message_id=reply.message_id AND ms.source_message_id=s.message_id) OR (
                NOT EXISTS(SELECT 1 FROM background_sources other WHERE other.batch_id=b.batch_id AND other.seq<>s.seq AND other.cancelled_at=0)))))
        LIMIT 1),'')
        FROM background_sources s JOIN background_batches b USING(batch_id)
        WHERE s.message_id=? AND s.event_id=? AND s.kind='task.external_wait_ready' AND s.cancelled_at=0 AND b.work_item_id=? LIMIT 1`,
		store.ConversationDestination(m.ConversationID), w.IssueID, m.AssignmentEpoch, taskID, w.SourceMessage, w.SourceEvent, w.IssueID).Scan(&batch, &carrier, &turn)
	if err == nil {
		if turn != "" {
			f.State, f.OwnerTurn = "processed", turn
		} else if carrier && (batch == "pending" || batch == "frozen") {
			f.State = "notified"
		}
		return f, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return f, err
	}
	if w.NoticeRestored {
		return f, nil
	}

	// No source is only actionable while this candidate and its original
	// responsibility are current. Active work, other wakes and uncertainties
	// can still be processing the same result.
	issue, err := store.GetCanonicalIssue(ctx, q, w.IssueID)
	if err != nil {
		return f, err
	}
	if m.State != "assigned" || issue.AssignmentState != "active" || issue.AssignmentEpoch != m.AssignmentEpoch || issue.State != protocol.IssueActive || issue.Assignee.Kind != protocol.IssueAssigneeAgent || issue.WorkStatus == protocol.IssueBacklog {
		return f, nil
	}
	task, err := store.GetTask(ctx, q, taskID)
	if err != nil || task.Lifecycle.Terminal() || task.ControlMode != protocol.ControlManaged {
		return f, err
	}
	row, err := store.GetDeliveryObservation(ctx, q, w.IssueID, w.TargetID)
	if errors.Is(err, store.ErrNotFound) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	o := row.Observation
	if o.State != "open" || o.ErrorKind != "" || o.RefreshPending || o.HeadSHA == "" || o.HeadSHA != f.Head || f.Base == "" || f.Target == "" || o.BaseSHA != f.Base || o.TargetSHA != f.Target || o.LineageErrorKind != "" || o.CI == nil || o.CI.Truncated || o.CI.ErrorKind != "" || o.CI.HeadSHA != f.Head || o.CI.State != f.Result {
		return f, nil
	}
	current, err := store.RunIssueResponsibilityCurrent(ctx, q, runID)
	if err != nil || !current {
		return f, err
	}
	valid, err := store.IssueManagerAuthorizationValid(ctx, q, taskID)
	if err != nil || !valid {
		return f, err
	}
	pending, err := ManagerReceiptPending(ctx, q, m)
	if err != nil || pending {
		return f, err
	}
	var held bool
	err = q.QueryRowContext(ctx, `SELECT
        EXISTS(SELECT 1 FROM runs WHERE task_id=? AND observed_state NOT IN ('finished','failed')) OR
        EXISTS(SELECT 1 FROM plan_actions WHERE task_id=? AND status IN ('pending','ready')) OR
        EXISTS(SELECT 1 FROM runs r JOIN manager_native_requests request USING(run_id) WHERE r.task_id=? AND request.status IN ('pending','started','unknown')) OR
        EXISTS(SELECT 1 FROM run_external_waits WHERE task_id=? AND status IN ('waiting','stopping')) OR
        EXISTS(SELECT 1 FROM decisions WHERE task_id=? AND status='open') OR
        EXISTS(SELECT 1 FROM task_work_item_owners o JOIN tasks t USING(task_id) WHERE o.work_item_id=? AND t.task_id<>? AND t.lifecycle IN ('queued','running','waiting','awaiting_acceptance','changes_required')) OR
        EXISTS(SELECT 1 FROM issue_change_notifications WHERE issue_id=? AND due_at>0) OR
        EXISTS(SELECT 1 FROM outbox o WHERE o.destination=? AND o.status IN ('pending','sending','unknown')
          AND (o.status<>'pending' OR NOT `+store.IssueOwnerConsumptionMatchesOutbox+`)) OR
        EXISTS(SELECT 1 FROM background_sources WHERE message_id=? AND event_id=?) OR
        EXISTS(SELECT 1 FROM issue_delivery_duties WHERE issue_id=? AND status IN ('awaiting_owner','authorized','executing','deferred','ended','complete','report')
          AND json_extract(body_json,'$.candidate.delivery_id')=?) OR
        EXISTS(SELECT 1 FROM agent_recoveries r JOIN runner_coordination_executions e ON r.root_request_id=e.execution_id WHERE e.issue_id=? AND e.assignment_epoch=? AND r.status IN ('waiting','starting','running','unknown','blocked','exhausted'))`,
		taskID, taskID, taskID, taskID, taskID, w.IssueID, taskID, w.IssueID, store.ConversationDestination(m.ConversationID), w.SourceMessage, w.SourceEvent, w.IssueID, w.TargetID, w.IssueID, m.AssignmentEpoch).Scan(&held)
	if err != nil || held {
		return f, err
	}
	f.State = "missing"
	return f, nil
}
