package views

import (
	"context"
	"database/sql"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// issueOwnerExecution reads the current coordination carrier independently of
// the ordinary Task associations and the manager's logical lifecycle.
func (f executionFacts) issueOwnerExecution(ctx context.Context, q store.Querier, issue protocol.Issue) (*protocol.TaskExecutionView, error) {
	if issue.AssignmentState != "active" {
		return nil, nil
	}
	allowContinuation := issue.State != protocol.IssueClosed && issue.State != protocol.IssueCancelled
	if _, queued := f.issues[issue.IssueID]; queued && allowContinuation {
		return &protocol.TaskExecutionView{State: protocol.ExecutionQueued, Role: protocol.RoleIssueManager}, nil
	}
	m, err := store.CurrentIssueManagerTask(ctx, q, issue.IssueID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if m.State == "assigned" {
		if m.AssignmentEpoch != issue.AssignmentEpoch || m.Pin == nil || issue.Assignee.Kind != protocol.IssueAssigneeAgent ||
			m.Pin.Identity.ExecutionEnvironmentID+"/"+m.Pin.Identity.LocalID != issue.Assignee.Ref {
			return nil, nil
		}
		execution, err := ownerCarrierRun(ctx, q, issue, m, allowContinuation)
		if execution != nil || err != nil {
			return execution, err
		}
	}
	if !allowContinuation {
		return nil, nil
	}
	if state, pending := f.ownerActions[m.TaskID]; pending {
		return &protocol.TaskExecutionView{State: state, Role: protocol.RoleIssueManager}, nil
	}
	return nil, nil
}

func ownerCarrierRun(ctx context.Context, q store.Querier, issue protocol.Issue, m store.IssueManagerTask, allowContinuation bool) (*protocol.TaskExecutionView, error) {
	// The carrier pin covers older Runs without their own pin. An explicit Run
	// identity or responsibility must agree whenever the ledger recorded it.
	// An Issue can reach its endpoint before the runner releases its lease.
	var v protocol.TaskExecutionView
	var observed protocol.RunObserved
	var stoppedWait bool
	err := q.QueryRowContext(ctx, `SELECT r.run_id,r.observed_state,r.observed_state='finished'
 FROM runs r
 LEFT JOIN run_agent_executions e USING(run_id)
 LEFT JOIN run_issue_responsibilities d USING(run_id)
 WHERE r.task_id=? AND r.role='issue_manager' AND r.host_id=?
 AND (e.run_id IS NULL OR (json_extract(e.pin_json,'$.identity.execution_environment_id')=? AND json_extract(e.pin_json,'$.identity.local_id')=?))
 AND (d.run_id IS NULL OR (d.issue_id=? AND d.assignment_epoch=? AND d.agent_ref=?))
 AND ((r.observed_state IN ('starting','running','waiting','interrupted','unknown') AND
       (? OR EXISTS(SELECT 1 FROM leases l WHERE l.run_id=r.run_id AND l.status IN ('active','lapsed')))) OR
      (? AND r.observed_state='finished' AND EXISTS(
         SELECT 1 FROM run_external_waits w JOIN leases l USING(run_id)
         WHERE w.run_id=r.run_id AND w.status='waiting' AND w.successor_run_id IS NULL AND l.status='released')))
 ORDER BY (r.observed_state='finished'),r.created_at DESC,r.run_id DESC LIMIT 1`,
		m.TaskID, m.HostID, m.Pin.Identity.ExecutionEnvironmentID, m.Pin.Identity.LocalID,
		issue.IssueID, issue.AssignmentEpoch, issue.Assignee.Ref, allowContinuation, allowContinuation).Scan(&v.RunID, &observed, &stoppedWait)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	v.Role = protocol.RoleIssueManager
	if stoppedWait {
		v.State = protocol.ExecutionWaitingExternal
		return &v, nil
	}
	switch observed {
	case protocol.RunUnknown:
		v.State = protocol.ExecutionUnknown
	case protocol.RunInterrupted:
		v.State = protocol.ExecutionPaused
	case protocol.RunStarting:
		v.State = protocol.ExecutionStarting
	default:
		v.State = protocol.ExecutionRunning
	}
	if allowContinuation && (v.State == protocol.ExecutionStarting || v.State == protocol.ExecutionRunning) {
		v.Recovery, err = issueOwnerRunRecovery(ctx, q, issue, v.RunID)
		if v.Recovery != nil {
			v.State = protocol.ExecutionRecovering
		}
	}
	return &v, err
}

func issueOwnerRunRecovery(ctx context.Context, q store.Querier, issue protocol.Issue, runID string) (*protocol.ExecutionRecovery, error) {
	// Until the scheduled attempt has a native execution, its source still
	// identifies the Run retaining the recovery.
	var recovery protocol.ExecutionRecovery
	err := q.QueryRowContext(ctx, `SELECT r.status,r.attempts
 FROM runner_coordination_executions original
 JOIN agent_recoveries r ON r.root_request_id=original.execution_id
 LEFT JOIN agent_recovery_attempts attempt ON attempt.request_id=r.active_request_id
 LEFT JOIN runner_coordination_executions resumed ON resumed.execution_id=r.active_request_id
 JOIN runner_coordination_executions current
   ON current.execution_id=COALESCE(resumed.execution_id,attempt.source_request_id,original.execution_id)
 WHERE original.issue_id=? AND original.assignment_epoch=? AND original.agent_ref=?
 AND current.issue_id=original.issue_id AND current.assignment_epoch=original.assignment_epoch
 AND current.agent_ref=original.agent_ref AND current.conversation_id=r.conversation_id
 AND json_extract(current.private_snapshot,'$.runner_run_id')=?
 AND r.status IN ('waiting','starting','running','unknown','blocked','exhausted')
 ORDER BY r.updated_at DESC,r.root_request_id DESC LIMIT 1`, issue.IssueID, issue.AssignmentEpoch, issue.Assignee.Ref, runID).Scan(&recovery.Phase, &recovery.Attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &recovery, nil
}
