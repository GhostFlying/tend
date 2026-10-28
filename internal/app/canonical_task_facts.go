package app

import (
	"context"
	"strings"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// Existing task/run/lease/resource records are the execution facts. Mere source
// associations are not manager/native ownership, and no new proof ledger is used.
func appendCanonicalTaskFacts(ctx context.Context, q store.Querier, root string, f *domain.IssueLifecycleFacts) error {
	rows, err := q.QueryContext(ctx, `WITH RECURSIVE scope(id) AS (
 SELECT issue_id FROM issue_controls WHERE issue_id=?
 UNION ALL SELECT c.issue_id FROM issue_controls c JOIN scope p ON c.parent_issue_id=p.id)
 SELECT t.task_id,t.lifecycle,t.control_revision,
 EXISTS(SELECT 1 FROM runs r WHERE r.task_id=t.task_id AND r.observed_state IN ('starting','running','waiting','interrupted','unknown')),
 EXISTS(SELECT 1 FROM runs r JOIN leases l ON l.run_id=r.run_id WHERE r.task_id=t.task_id AND l.status<>'released'),
 EXISTS(SELECT 1 FROM workspaces w WHERE w.occupied_by=t.task_id),
 EXISTS(SELECT 1 FROM decisions d WHERE d.task_id=t.task_id AND d.status='open'),
 EXISTS(SELECT 1 FROM run_messages m WHERE m.task_id=t.task_id AND m.status IN ('dispatching','unknown'))
 FROM task_work_item_owners o JOIN scope s ON s.id=o.work_item_id JOIN tasks t ON t.task_id=o.task_id ORDER BY t.task_id`, root)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var life protocol.TaskLifecycle
		var revision int64
		var execution, lease, workspace, decision, unknown bool
		if err := rows.Scan(&id, &life, &revision, &execution, &lease, &workspace, &decision, &unknown); err != nil {
			return err
		}
		blocker := protocol.IssueBlocker{ObjectID: id, Revision: revision}
		if !life.Terminal() {
			blocker.Code = "task_responsibility"
			f.NonTerminalTasks = append(f.NonTerminalTasks, blocker)
		}
		if execution || lease {
			blocker.Code = "task_execution"
			f.Executions = append(f.Executions, blocker)
		}
		if workspace {
			blocker.Code = "task_workspace"
			f.PendingControls = append(f.PendingControls, blocker)
		}
		if decision {
			blocker.Code = "task_decision"
			f.OpenAttention = append(f.OpenAttention, blocker)
		}
		if unknown {
			blocker.Code = "task_message_unknown"
			f.UnknownOperations = append(f.UnknownOperations, blocker)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	reviews, err := store.ListPendingCompletionReviews(ctx, q, root)
	if err != nil {
		return err
	}
	for _, review := range reviews {
		f.OpenAttention = append(f.OpenAttention, protocol.IssueBlocker{Code: "completion_review", ObjectID: review.EventID})
	}
	return nil
}

func cancelCanonicalPrimaryTasks(ctx context.Context, x *Exec, tx *store.Tx, root string) ([]Event, error) {
	ids, err := store.FollowupIDs(ctx, tx.Q(), `WITH RECURSIVE scope(id)AS(SELECT issue_id FROM issue_controls WHERE issue_id=? UNION ALL SELECT c.issue_id FROM issue_controls c JOIN scope p ON c.parent_issue_id=p.id)SELECT o.task_id FROM task_work_item_owners o JOIN scope s ON s.id=o.work_item_id ORDER BY o.task_id`, root)
	if err != nil {
		return nil, err
	}
	var events []Event
	for _, id := range ids {
		row, spec, err := loadTask(ctx, tx.Q(), id)
		if err != nil {
			return nil, err
		}
		if row.Lifecycle.Terminal() {
			continue
		}
		decision, err := domain.Cancel(toDomain(row, spec.Budget()), row.ControlRevision)
		if err != nil {
			return nil, err
		}
		_, next, err := applyDecision(ctx, x, tx, row, decision, nil)
		if err != nil {
			return nil, err
		}
		events = append(events, next...)
	}
	return events, nil
}

func acknowledgeCanonicalCompletions(ctx context.Context, tx *store.Tx, issue string, revision int64, reviews []protocol.CompletionReviewAcknowledgement) error {
	seen := map[string]bool{}
	for _, review := range reviews {
		if seen[review.EventID] || strings.TrimSpace(review.Reason) == "" {
			return protocol.Errorf(protocol.CodeInvalidRequest, "distinct completion events and review reasons are required")
		}
		seen[review.EventID] = true
		if err := tx.AcknowledgeCompletionReview(ctx, issue, review.EventID, revision, review.Reason); err != nil {
			return err
		}
	}
	return nil
}
