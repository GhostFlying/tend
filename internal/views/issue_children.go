package views

import (
	"context"
	"database/sql"
	"errors"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func (facts executionFacts) issueCounts(ctx context.Context, q store.Querier, id string) (protocol.ExecutionCounts, error) {
	var counts protocol.ExecutionCounts
	rows, err := q.QueryContext(ctx, `WITH related(task_id) AS (
 SELECT task_id FROM task_work_item_owners WHERE work_item_id=?
 UNION SELECT task_id FROM task_execution_contracts WHERE review_issue_id=?
 UNION SELECT r.task_id FROM run_issue_responsibilities p JOIN runs r USING(run_id) WHERE p.issue_id=?)
 SELECT t.task_id,COALESCE(o.work_item_id,''),COALESCE(c.review_issue_id,'')
 FROM related JOIN tasks t USING(task_id) LEFT JOIN task_work_item_owners o USING(task_id) LEFT JOIN task_execution_contracts c USING(task_id)
 WHERE t.kind<>'issue_manager' ORDER BY t.task_id`, id, id, id)
	if err != nil {
		return counts, err
	}
	type ownership struct{ task, primary, review string }
	var tasks []ownership
	for rows.Next() {
		var item ownership
		if err := rows.Scan(&item.task, &item.primary, &item.review); err != nil {
			_ = rows.Close()
			return counts, err
		}
		tasks = append(tasks, item)
	}
	err = rows.Err()
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return counts, err
	}
	for _, item := range tasks {
		task, err := store.GetTask(ctx, q, item.task)
		if err != nil {
			return counts, err
		}
		execution, err := facts.task(ctx, q, task)
		if err != nil {
			return counts, err
		}
		responsible := item.primary
		// A settled blocked reviewer has no open run, but its awaiting-acceptance
		// phase still belongs to the review responsibility.
		if item.review != "" && (execution.Role == protocol.RoleAccept || execution.Role == "" && task.Lifecycle == protocol.TaskAwaitingAcceptance) {
			responsible = item.review
		}
		if execution.RunID != "" {
			var explicit string
			err := q.QueryRowContext(ctx, `SELECT issue_id FROM run_issue_responsibilities WHERE run_id=?`, execution.RunID).Scan(&explicit)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return counts, err
			}
			if err == nil {
				responsible = explicit
			}
		}
		if responsible == id {
			countExecution(&counts, execution)
		}
	}
	return counts, nil
}

func issueChildren(ctx context.Context, q store.Querier, parent string, facts executionFacts) (*protocol.IssueChildProgress, error) {
	rows, err := q.QueryContext(ctx, `SELECT c.issue_id,COALESCE(json_extract(w.body_json,'$.title'),''),`+issueBoardStatusSQL+` FROM issue_controls c JOIN work_items w ON w.work_item_id=c.issue_id WHERE c.parent_issue_id=? ORDER BY c.issue_id`, parent)
	if err != nil {
		return nil, err
	}
	var items []protocol.IssueChildSummary
	for rows.Next() {
		var item protocol.IssueChildSummary
		if err := rows.Scan(&item.IssueID, &item.Title, &item.Status); err != nil {
			_ = rows.Close()
			return nil, err
		}
		items = append(items, item)
	}
	err = rows.Err()
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
	if err != nil || len(items) == 0 {
		return nil, err
	}
	out := &protocol.IssueChildProgress{Total: len(items)}
	for i := range items {
		item := &items[i]
		item.Execution, err = facts.issueCounts(ctx, q, item.IssueID)
		if err != nil {
			return nil, err
		}
		if item.Status == protocol.IssueDone {
			out.Done++
		}
		if item.Status == protocol.IssueWorkCancelled {
			out.Cancelled++
		}
		if item.Execution.Running+item.Execution.Accepting > 0 {
			out.Running++
		}
	}
	slices.SortStableFunc(items, func(a, b protocol.IssueChildSummary) int { return childActivityRank(a) - childActivityRank(b) })
	out.Items = items[:min(2, len(items))]
	return out, nil
}

func childActivityRank(item protocol.IssueChildSummary) int {
	switch {
	case item.Execution.Unknown+item.Execution.WaitingOwner+item.Execution.WaitingSupervisor > 0:
		return 0
	case item.Execution.Running+item.Execution.Accepting > 0:
		return 1
	case item.Execution.Starting+item.Execution.Recovering+item.Execution.Queued > 0:
		return 2
	case item.Status == protocol.IssueInProgress || item.Status == protocol.IssueInReview:
		return 3
	case item.Status == protocol.IssueDone || item.Status == protocol.IssueWorkCancelled:
		return 5
	default:
		return 4
	}
}
