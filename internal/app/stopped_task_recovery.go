package app

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// A coordination release cannot stand in for physical stop, receipt consumption
// or reconciliation of a dispatched native operation.
func stoppedTaskRecoveryReady(ctx context.Context, q store.Querier, task string, retainedWorkspace bool) error {
	var busy bool
	if err := q.QueryRowContext(ctx, `SELECT
 EXISTS(SELECT 1 FROM runs r LEFT JOIN leases l USING(run_id) WHERE r.task_id=? AND (r.observed_state NOT IN ('finished','failed') OR l.status<>'released')) OR
 EXISTS(SELECT 1 FROM workspaces w WHERE occupied_by=? AND (?=0 OR EXISTS(SELECT 1 FROM workspace_runs wr JOIN runs r USING(run_id) WHERE wr.workspace_id=w.workspace_id AND r.observed_state NOT IN ('finished','failed')))) OR
 EXISTS(SELECT 1 FROM run_messages WHERE task_id=? AND status IN ('queued','dispatching','unknown')) OR
 EXISTS(SELECT 1 FROM manager_native_events e JOIN runs r USING(run_id) WHERE r.task_id=? AND e.applied=0) OR
 EXISTS(SELECT 1 FROM hosted_executions e JOIN main_turns t ON t.request_id=e.execution_id JOIN runs r ON r.run_id=json_extract(e.private_snapshot,'$.runner_run_id') WHERE r.task_id=? AND (t.status IN ('starting','started','unknown') OR EXISTS(SELECT 1 FROM native_turn_receipts_pending p WHERE p.request_id=e.execution_id)))`, task, task, retainedWorkspace, task, task, task).Scan(&busy); err != nil {
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
