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
	if issue.AssignmentState != "active" || issue.State == protocol.IssueClosed || issue.State == protocol.IssueCancelled {
		return nil, nil
	}
	if _, queued := f.issues[issue.IssueID]; queued {
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
		execution, err := ownerCarrierRun(ctx, q, issue, m)
		if execution != nil || err != nil {
			return execution, err
		}
	}
	if state, pending := f.ownerActions[m.TaskID]; pending {
		return &protocol.TaskExecutionView{State: state, Role: protocol.RoleIssueManager}, nil
	}
	return nil, nil
}

func ownerCarrierRun(ctx context.Context, q store.Querier, issue protocol.Issue, m store.IssueManagerTask) (*protocol.TaskExecutionView, error) {
	// The carrier pin covers older Runs without their own pin. An explicit Run
	// identity or responsibility must agree whenever the ledger recorded it.
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
 AND (r.observed_state IN ('starting','running','waiting','interrupted','unknown') OR
      (r.observed_state='finished' AND EXISTS(
         SELECT 1 FROM run_external_waits w JOIN leases l USING(run_id)
         WHERE w.run_id=r.run_id AND w.status='waiting' AND w.successor_run_id IS NULL AND l.status='released')))
 ORDER BY (r.observed_state='finished'),r.created_at DESC,r.run_id DESC LIMIT 1`,
		m.TaskID, m.HostID, m.Pin.Identity.ExecutionEnvironmentID, m.Pin.Identity.LocalID, issue.IssueID, issue.AssignmentEpoch, issue.Assignee.Ref).Scan(&v.RunID, &observed, &stoppedWait)
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
	if v.State == protocol.ExecutionStarting || v.State == protocol.ExecutionRunning {
		v.Recovery, err = executionRecovery(ctx, q, v.RunID)
		if v.Recovery != nil {
			v.State = protocol.ExecutionRecovering
		}
	}
	return &v, err
}
