package views

import (
	"context"
	"encoding/json"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// initialOwnerHandoffBlockers reads each barrier relation once per candidate
// scan. Targets and results belong only to the caller's current transaction.
// The standalone handoff check remains independent of this projection.
func initialOwnerHandoffBlockers(ctx context.Context, q store.Querier, issues []protocol.Issue) (map[string][]protocol.IssueBlocker, error) {
	out := map[string][]protocol.IssueBlocker{}
	if len(issues) == 0 {
		return out, nil
	}
	type target struct {
		IssueID   string `json:"issue_id"`
		NextEpoch int64  `json:"next_epoch"`
	}
	targets := make([]target, 0, len(issues))
	for _, issue := range issues {
		targets = append(targets, target{issue.IssueID, issue.AssignmentEpoch + 1})
	}
	raw, err := json.Marshal(targets)
	if err != nil {
		return nil, err
	}
	const needed = `WITH targets AS (SELECT json_extract(value,'$.issue_id') AS issue_id,CAST(json_extract(value,'$.next_epoch') AS INTEGER) AS next_epoch FROM json_each(?))`
	stages := []struct{ code, query string }{
		{"old_manager_execution", needed + ` SELECT n.issue_id,h.execution_id FROM targets n JOIN hosted_executions h ON h.issue_id=n.issue_id JOIN main_turns t ON t.request_id=h.execution_id WHERE h.assignment_epoch<n.next_epoch AND (t.status IN ('starting','started','unknown') OR EXISTS(SELECT 1 FROM native_turn_receipts_pending p WHERE p.request_id=h.execution_id) OR EXISTS(SELECT 1 FROM agent_recoveries r WHERE r.root_request_id=h.execution_id AND r.status IN ('starting','running','unknown'))) ORDER BY n.issue_id,h.execution_id`},
		{"old_manager_delivery", needed + `, prior_logical AS (SELECT DISTINCT n.issue_id,original.logical_turn_id FROM targets n JOIN hosted_executions h ON h.issue_id=n.issue_id JOIN logical_turn_executions original ON original.request_id=h.execution_id WHERE h.assignment_epoch<n.next_epoch) SELECT DISTINCT p.issue_id,o.request_id FROM prior_logical p JOIN logical_turn_executions e ON e.logical_turn_id=p.logical_turn_id JOIN outbox o ON o.request_id=e.request_id WHERE o.operation IN ('conversation.repair_turn','conversation.recover_agent','main_thread.handle_message','main_thread.handle_event') AND o.status IN ('sending','unknown') UNION SELECT n.issue_id,o.request_id FROM targets n JOIN agent_conversations c ON c.work_item_id=n.issue_id JOIN outbox o ON o.destination='background_thread:'||c.conversation_id WHERE o.operation IN ('main_thread.handle_message','main_thread.handle_event') AND o.status IN ('sending','unknown') ORDER BY 1,2`},
		{"old_manager_run", needed + ` SELECT n.issue_id,r.run_id FROM targets n JOIN issue_manager_tasks m ON m.issue_id=n.issue_id JOIN runs r USING(task_id) WHERE COALESCE(m.assignment_epoch,m.expected_epoch)<n.next_epoch AND (r.observed_state IN ('starting','running','waiting','interrupted','unknown') OR EXISTS(SELECT 1 FROM leases l WHERE l.run_id=r.run_id AND l.status<>'released')) ORDER BY n.issue_id,r.run_id`},
		{"old_manager_events", needed + ` SELECT DISTINCT n.issue_id,r.run_id FROM targets n JOIN issue_manager_tasks m ON m.issue_id=n.issue_id JOIN runs r USING(task_id) JOIN manager_native_events e USING(run_id) WHERE COALESCE(m.assignment_epoch,m.expected_epoch)<n.next_epoch AND e.applied=0 ORDER BY n.issue_id,r.run_id`},
	}
	for _, stage := range stages {
		rows, err := q.QueryContext(ctx, stage.query, string(raw))
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var issue, id string
			if err := rows.Scan(&issue, &id); err != nil {
				rows.Close()
				return nil, err
			}
			out[issue] = append(out[issue], protocol.IssueBlocker{Code: stage.code, ObjectID: id})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
