package views

import (
	"context"
	"encoding/json"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func ManagerHandoffBlockers(ctx context.Context, q store.Querier, issue string, nextEpoch int64) ([]protocol.IssueBlocker, error) {
	all, err := managerHandoffBlockersBatch(ctx, q, map[string]int64{issue: nextEpoch})
	if err != nil {
		return nil, err
	}
	return append([]protocol.IssueBlocker{}, all[issue]...), nil
}

// The batch preserves each Issue's epoch and each original blocker identity.
// It shares SQL preparation, not a scheduling decision or authorization grant.
func managerHandoffBlockersBatch(ctx context.Context, q store.Querier, epochs map[string]int64) (map[string][]protocol.IssueBlocker, error) {
	out := map[string][]protocol.IssueBlocker{}
	if len(epochs) == 0 {
		return out, nil
	}
	raw, err := json.Marshal(epochs)
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, managerHandoffBatchSQL, string(raw))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var issue string
		var b protocol.IssueBlocker
		var order int
		if err := rows.Scan(&issue, &order, &b.Code, &b.ObjectID); err != nil {
			return nil, err
		}
		out[issue] = append(out[issue], b)
	}
	return out, rows.Err()
}

const managerHandoffBatchSQL = `WITH needed(issue_id,next_epoch) AS (
 SELECT key,CAST(value AS INTEGER) FROM json_each(?)
), prior_logical AS (
 SELECT DISTINCT n.issue_id,original.logical_turn_id FROM needed n
 JOIN hosted_executions h ON h.issue_id=n.issue_id AND h.assignment_epoch<n.next_epoch
 JOIN logical_turn_executions original ON original.request_id=h.execution_id
), deliveries AS (
 SELECT DISTINCT p.issue_id,o.request_id FROM prior_logical p
 JOIN logical_turn_executions e ON e.logical_turn_id=p.logical_turn_id
 JOIN outbox o ON o.request_id=e.request_id
 WHERE o.operation IN ('conversation.repair_turn','conversation.recover_agent','main_thread.handle_message','main_thread.handle_event') AND o.status IN ('sending','unknown')
 UNION SELECT n.issue_id,o.request_id FROM needed n JOIN agent_conversations c ON c.work_item_id=n.issue_id
 JOIN outbox o ON o.destination='background_thread:'||c.conversation_id
 WHERE o.operation IN ('main_thread.handle_message','main_thread.handle_event') AND o.status IN ('sending','unknown')
)
SELECT n.issue_id,0 AS kind,'old_manager_execution',h.execution_id FROM needed n
 JOIN hosted_executions h ON h.issue_id=n.issue_id AND h.assignment_epoch<n.next_epoch
 JOIN main_turns t ON t.request_id=h.execution_id
 WHERE t.status IN ('starting','started','unknown')
 OR EXISTS(SELECT 1 FROM native_turn_receipts_pending p WHERE p.request_id=h.execution_id)
 OR EXISTS(SELECT 1 FROM agent_recoveries r WHERE r.root_request_id=h.execution_id AND r.status IN ('starting','running','unknown'))
UNION ALL SELECT issue_id,1,'old_manager_delivery',request_id FROM deliveries
UNION ALL SELECT n.issue_id,2,'old_manager_run',r.run_id FROM needed n
 JOIN issue_manager_tasks m ON m.issue_id=n.issue_id AND COALESCE(m.assignment_epoch,m.expected_epoch)<n.next_epoch
 JOIN runs r USING(task_id)
 WHERE r.observed_state IN ('starting','running','waiting','interrupted','unknown')
 OR EXISTS(SELECT 1 FROM leases l WHERE l.run_id=r.run_id AND l.status<>'released')
UNION ALL SELECT DISTINCT n.issue_id,3,'old_manager_events',r.run_id FROM needed n
 JOIN issue_manager_tasks m ON m.issue_id=n.issue_id AND COALESCE(m.assignment_epoch,m.expected_epoch)<n.next_epoch
 JOIN runs r USING(task_id) JOIN manager_native_events e USING(run_id) WHERE e.applied=0
ORDER BY 1,2,4`

// ManagerReceiptPending keeps the original executor available for read-only
// reconciliation after a fence. A successor's pending task never owns its history.
func ManagerReceiptPending(ctx context.Context, q store.Querier, m store.IssueManagerTask) (bool, error) {
	return managerReceiptPending(ctx, q, m, false)
}

// managerReceiptsPending batches the ordinary scheduling receipt predicate.
// Native-only maintenance retains its distinct terminal-presentation boundary.
func managerReceiptsPending(ctx context.Context, q store.Querier, tasks []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(tasks) == 0 {
		return out, nil
	}
	raw, err := json.Marshal(tasks)
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `SELECT m.task_id,
 EXISTS(SELECT 1 FROM hosted_executions e JOIN main_turns t ON t.request_id=e.execution_id WHERE e.issue_id=m.issue_id AND e.assignment_epoch=COALESCE(m.assignment_epoch,0) AND (t.status IN ('starting','started','unknown') OR EXISTS(SELECT 1 FROM native_turn_receipts_pending p WHERE p.request_id=t.request_id)))
 OR EXISTS(SELECT 1 FROM manager_native_events e JOIN runs r USING(run_id) WHERE r.task_id=m.task_id AND r.observed_state IN ('failed','finished') AND e.applied=0)
 FROM issue_manager_tasks m JOIN json_each(?) requested ON requested.value=m.task_id`, string(raw))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var task string
		var pending bool
		if err := rows.Scan(&task, &pending); err != nil {
			return nil, err
		}
		out[task] = pending
	}
	return out, rows.Err()
}

// ManagerNativeReceiptPending excludes an already observed native terminal whose
// presentation alone needs retry. That retry must not block physical maintenance.
func ManagerNativeReceiptPending(ctx context.Context, q store.Querier, m store.IssueManagerTask) (bool, error) {
	return managerReceiptPending(ctx, q, m, true)
}

func managerReceiptPending(ctx context.Context, q store.Querier, m store.IssueManagerTask, nativeOnly bool) (bool, error) {
	var pending bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hosted_executions e JOIN main_turns t ON t.request_id=e.execution_id WHERE e.issue_id=? AND e.assignment_epoch=? AND (t.status IN ('starting','started','unknown') OR (EXISTS(SELECT 1 FROM native_turn_receipts_pending p WHERE p.request_id=t.request_id) AND (?=0 OR t.status NOT IN ('completed','interrupted','failed') OR NOT EXISTS(SELECT 1 FROM agent_rollout_events o WHERE o.conversation_id=e.conversation_id AND o.thread_id=t.thread_id AND o.turn_id=t.turn_id AND o.kind IN ('native_receipt','native_terminal')))))) OR EXISTS(SELECT 1 FROM manager_native_events e JOIN runs r USING(run_id) WHERE r.task_id=? AND r.observed_state IN ('failed','finished') AND e.applied=0)`, m.IssueID, m.AssignmentEpoch, nativeOnly, m.TaskID).Scan(&pending)
	return pending, err
}

func ManagerReceiptDue(ctx context.Context, q store.Querier, m store.IssueManagerTask, now int64) (bool, error) {
	var due bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hosted_executions e JOIN main_turns t ON t.request_id=e.execution_id WHERE e.issue_id=? AND e.assignment_epoch=? AND ((t.status IN ('starting','started','unknown') AND NOT EXISTS(SELECT 1 FROM native_turn_receipts_pending p WHERE p.request_id=t.request_id)) OR EXISTS(SELECT 1 FROM native_turn_receipts_pending p WHERE p.request_id=t.request_id AND p.next_reconcile_at<=?))) OR EXISTS(SELECT 1 FROM manager_native_events e JOIN runs r USING(run_id) WHERE r.task_id=? AND r.observed_state IN ('failed','finished') AND e.applied=0)`, m.IssueID, m.AssignmentEpoch, now, m.TaskID).Scan(&due)
	return due, err
}
