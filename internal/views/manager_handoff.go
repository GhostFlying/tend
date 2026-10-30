package views

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func ManagerHandoffBlockers(ctx context.Context, q store.Querier, issue string, nextEpoch int64) ([]protocol.IssueBlocker, error) {
	ids, err := store.FollowupIDs(ctx, q, `SELECT h.execution_id FROM hosted_executions h JOIN main_turns t ON t.request_id=h.execution_id WHERE h.issue_id=? AND h.assignment_epoch<? AND (t.status IN ('starting','started','unknown') OR EXISTS(SELECT 1 FROM native_turn_receipts_pending p WHERE p.request_id=h.execution_id) OR EXISTS(SELECT 1 FROM agent_recoveries r WHERE r.root_request_id=h.execution_id AND r.status IN ('starting','running','unknown'))) ORDER BY h.execution_id`, issue, nextEpoch)
	if err != nil {
		return nil, err
	}
	blockers := []protocol.IssueBlocker{}
	for _, id := range ids {
		blockers = append(blockers, protocol.IssueBlocker{Code: "old_manager_execution", ObjectID: id})
	}
	deliveries, err := store.FollowupIDs(ctx, q, `SELECT DISTINCT o.request_id FROM outbox o JOIN logical_turn_executions e ON e.request_id=o.request_id JOIN logical_turn_executions original ON original.logical_turn_id=e.logical_turn_id JOIN hosted_executions h ON h.execution_id=original.request_id WHERE h.issue_id=? AND h.assignment_epoch<? AND o.operation IN ('conversation.repair_turn','conversation.recover_agent','main_thread.handle_message','main_thread.handle_event') AND o.status IN ('sending','unknown') UNION SELECT o.request_id FROM agent_conversations c JOIN outbox o ON o.destination='background_thread:'||c.conversation_id WHERE c.work_item_id=? AND o.operation IN ('main_thread.handle_message','main_thread.handle_event') AND o.status IN ('sending','unknown') ORDER BY 1`, issue, nextEpoch, issue)
	if err != nil {
		return nil, err
	}
	for _, id := range deliveries {
		blockers = append(blockers, protocol.IssueBlocker{Code: "old_manager_delivery", ObjectID: id})
	}
	requests, err := store.FollowupIDs(ctx, q, `SELECT DISTINCT e.request_id FROM hosted_coordination_effects e JOIN hosted_executions h ON h.execution_id=e.execution_id WHERE h.issue_id=? AND h.assignment_epoch<? AND (EXISTS(SELECT 1 FROM request_records r WHERE r.request_id=e.request_id AND r.status IN ('accepted','unknown'))) ORDER BY e.request_id`, issue, nextEpoch)
	if err != nil {
		return nil, err
	}
	for _, id := range requests {
		blockers = append(blockers, protocol.IssueBlocker{Code: "old_manager_control", ObjectID: id})
	}
	runs, err := store.FollowupIDs(ctx, q, `SELECT r.run_id FROM runs r JOIN issue_manager_tasks m USING(task_id) WHERE m.issue_id=? AND COALESCE(m.assignment_epoch,m.expected_epoch)<? AND (r.observed_state IN ('starting','running','waiting','interrupted','unknown') OR EXISTS(SELECT 1 FROM leases l WHERE l.run_id=r.run_id AND l.status<>'released')) ORDER BY r.run_id`, issue, nextEpoch)
	if err != nil {
		return nil, err
	}
	for _, id := range runs {
		blockers = append(blockers, protocol.IssueBlocker{Code: "old_manager_run", ObjectID: id})
	}
	backlogs, err := store.FollowupIDs(ctx, q, `SELECT DISTINCT r.run_id FROM manager_native_events e JOIN runs r USING(run_id) JOIN issue_manager_tasks m USING(task_id) WHERE m.issue_id=? AND COALESCE(m.assignment_epoch,m.expected_epoch)<? AND e.applied=0`, issue, nextEpoch)
	if err != nil {
		return nil, err
	}
	for _, id := range backlogs {
		blockers = append(blockers, protocol.IssueBlocker{Code: "old_manager_events", ObjectID: id})
	}
	return blockers, nil
}

// ManagerReceiptPending keeps the original executor available for read-only
// reconciliation after a fence. A successor's pending task never owns its history.
func ManagerReceiptPending(ctx context.Context, q store.Querier, m store.IssueManagerTask) (bool, error) {
	var pending bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hosted_executions e JOIN main_turns t ON t.request_id=e.execution_id WHERE e.issue_id=? AND e.assignment_epoch=? AND (t.status IN ('starting','started','unknown') OR EXISTS(SELECT 1 FROM native_turn_receipts_pending p WHERE p.request_id=t.request_id))) OR EXISTS(SELECT 1 FROM manager_native_events e JOIN runs r USING(run_id) WHERE r.task_id=? AND r.observed_state IN ('failed','finished') AND e.applied=0)`, m.IssueID, m.AssignmentEpoch, m.TaskID).Scan(&pending)
	return pending, err
}
func ManagerReceiptDue(ctx context.Context, q store.Querier, m store.IssueManagerTask, now int64) (bool, error) {
	var due bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hosted_executions e JOIN main_turns t ON t.request_id=e.execution_id WHERE e.issue_id=? AND e.assignment_epoch=? AND ((t.status IN ('starting','started','unknown') AND NOT EXISTS(SELECT 1 FROM native_turn_receipts_pending p WHERE p.request_id=t.request_id)) OR EXISTS(SELECT 1 FROM native_turn_receipts_pending p WHERE p.request_id=t.request_id AND p.next_reconcile_at<=?))) OR EXISTS(SELECT 1 FROM manager_native_events e JOIN runs r USING(run_id) WHERE r.task_id=? AND r.observed_state IN ('failed','finished') AND e.applied=0)`, m.IssueID, m.AssignmentEpoch, now, m.TaskID).Scan(&due)
	return due, err
}
