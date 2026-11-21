package app

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// A coordination release cannot stand in for physical stop, receipt consumption
// or reconciliation of a dispatched native operation.
func stoppedTaskRecoveryReady(ctx context.Context, q store.Querier, task string) error {
	var busy bool
	if err := q.QueryRowContext(ctx, `SELECT
 EXISTS(SELECT 1 FROM runs r LEFT JOIN leases l USING(run_id) WHERE r.task_id=? AND (r.observed_state NOT IN ('finished','failed') OR COALESCE(l.status,'')<>'released')) OR
 EXISTS(SELECT 1 FROM task_workspace_bindings b JOIN workspace_runs wr USING(workspace_id) JOIN runs r USING(run_id) LEFT JOIN leases l USING(run_id)
        WHERE b.task_id=? AND (r.observed_state NOT IN ('finished','failed') OR COALESCE(l.status,'')<>'released')) OR
 EXISTS(SELECT 1 FROM run_external_waits WHERE task_id=? AND status='stopping') OR
 EXISTS(SELECT 1 FROM run_messages WHERE task_id=? AND status IN ('queued','dispatching','unknown')) OR
 EXISTS(SELECT 1 FROM manager_native_events e JOIN runs r USING(run_id) WHERE r.task_id=? AND e.applied=0) OR
 EXISTS(SELECT 1 FROM hosted_executions e JOIN main_turns t ON t.request_id=e.execution_id JOIN runs r ON r.run_id=json_extract(e.private_snapshot,'$.runner_run_id') WHERE r.task_id=? AND (t.status IN ('starting','started','unknown') OR EXISTS(SELECT 1 FROM native_turn_receipts_pending p WHERE p.request_id=e.execution_id)))`, task, task, task, task, task, task).Scan(&busy); err != nil {
		return err
	}
	if busy {
		return protocol.Errorf(protocol.CodeUnknownOutcome, "settle the original execution, physical stop, resource and external operation receipts before coordination recovery")
	}
	rows, err := q.QueryContext(ctx, `SELECT n.method FROM manager_native_requests n JOIN runs r USING(run_id) WHERE r.task_id=? AND (n.status IN ('pending','started','unknown') OR (n.status='completed' AND COALESCE(json_extract(n.result_json,'$.error'),'')<>'' AND COALESCE(json_extract(n.result_json,'$.rejected'),0)=0))`, task)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var method string
		if err := rows.Scan(&method); err != nil {
			return err
		}
		// Use the existing exact observation-method contract. An unknown
		// read remains historical evidence, never a reason to replay input.
		if !managerNativeObservationMethod(method) {
			return protocol.Errorf(protocol.CodeUnknownOutcome, "reconcile the original native operation before coordination recovery")
		}
	}
	return rows.Err()
}

// A logical result can release the lease before its runner finishes writing in
// the reserved workspace. Require an explicit stop from each writer, or the
// workspace proof covering all its runs in the same journal.
func stoppedReviewerTaskRecoveryReady(ctx context.Context, q store.Querier, task string) error {
	if err := stoppedTaskRecoveryReady(ctx, q, task); err != nil {
		return err
	}
	var missing bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM task_workspace_bindings b JOIN workspaces w USING(workspace_id)
 JOIN workspace_runs wr USING(workspace_id) JOIN runs r USING(run_id)
 LEFT JOIN runner_journal_runs j ON j.run_id=r.run_id
 LEFT JOIN workspace_journal_releases p ON p.workspace_id=w.workspace_id AND p.task_id=r.task_id AND p.journal_id=COALESCE(j.journal_id,'')
 LEFT JOIN run_external_waits e ON e.run_id=r.run_id
 LEFT JOIN run_question_handoffs question ON question.run_id=r.run_id
 LEFT JOIN run_pause_handoffs pause ON pause.run_id=r.run_id
 WHERE b.task_id=? AND w.occupied_by=r.task_id
 GROUP BY w.workspace_id,r.task_id,COALESCE(j.journal_id,'')
 HAVING COUNT(*)>COALESCE(MAX(p.run_count),0) AND COUNT(*) FILTER (
 WHERE r.runner_stopped_at=0 AND COALESCE(json_extract(e.body_json,'$.stopped'),0)=0
 AND COALESCE(question.stopped,0)=0 AND COALESCE(pause.stopped,0)=0)>0)`, task).Scan(&missing); err != nil {
		return err
	}
	if missing {
		return protocol.Errorf(protocol.CodeUnknownOutcome, "the reserved reviewer workspace still needs its original runner stop or journal release proof")
	}
	return nil
}
