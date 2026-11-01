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
 SELECT t.task_id,t.kind,t.lifecycle,t.control_revision,
 EXISTS(SELECT 1 FROM runs r WHERE r.task_id=t.task_id AND r.observed_state IN ('starting','running','waiting','interrupted','unknown')),
 EXISTS(SELECT 1 FROM runs r JOIN leases l ON l.run_id=r.run_id WHERE r.task_id=t.task_id AND l.status<>'released'),
 EXISTS(SELECT 1 FROM workspaces w WHERE w.occupied_by=t.task_id),
 EXISTS(SELECT 1 FROM decisions d WHERE d.task_id=t.task_id AND d.status='open'),
	 EXISTS(SELECT 1 FROM run_messages m WHERE m.task_id=t.task_id AND m.status IN ('dispatching','unknown')),
	 EXISTS(SELECT 1 FROM runs r WHERE r.task_id=t.task_id AND r.observed_state='unknown') OR
	 EXISTS(SELECT 1 FROM issue_manager_tasks im JOIN hosted_executions e ON e.issue_id=im.issue_id AND e.assignment_epoch=im.assignment_epoch JOIN main_turns mt ON mt.request_id=e.execution_id WHERE im.task_id=t.task_id AND (mt.status='unknown' OR EXISTS(SELECT 1 FROM native_turn_receipts_pending p WHERE p.request_id=mt.request_id)))
 FROM task_work_item_owners o JOIN scope s ON s.id=o.work_item_id JOIN tasks t ON t.task_id=o.task_id ORDER BY t.task_id`, root)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var kind protocol.TaskKind
		var life protocol.TaskLifecycle
		var revision int64
		var execution, lease, workspace, decision, unknown, managerUnknown bool
		if err := rows.Scan(&id, &kind, &life, &revision, &execution, &lease, &workspace, &decision, &unknown, &managerUnknown); err != nil {
			return err
		}
		blocker := protocol.IssueBlocker{ObjectID: id, Revision: revision}
		if kind != protocol.TaskKindIssueManager && !life.Terminal() {
			blocker.Code = "task_responsibility"
			f.NonTerminalTasks = append(f.NonTerminalTasks, blocker)
		}
		if kind != protocol.TaskKindIssueManager && (execution || lease) {
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
		if kind == protocol.TaskKindIssueManager && managerUnknown {
			blocker.Code = "manager_execution_unknown"
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
	return appendDelegatedRunFacts(ctx, q, root, f)
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
		if row.Lifecycle.Terminal() || row.Kind == protocol.TaskKindIssueManager {
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
	reviewEvents, err := cancelDelegatedReviewRuns(ctx, x, tx, root)
	return append(events, reviewEvents...), err
}

// A review run belongs to its child Issue, while the frozen delivery task and
// workspace still belong to the implementation Issue. Do not cancel another
// Issue's implementation when withdrawing only its delegated review.
func cancelDelegatedReviewRuns(ctx context.Context, x *Exec, tx *store.Tx, root string) ([]Event, error) {
	actions, err := store.FollowupIDs(ctx, tx.Q(), `WITH RECURSIVE scope(id) AS (SELECT issue_id FROM issue_controls WHERE issue_id=? UNION ALL SELECT c.issue_id FROM issue_controls c JOIN scope s ON c.parent_issue_id=s.id)
 SELECT a.action_id FROM task_owner_contracts c JOIN scope s ON s.id=c.review_issue_id JOIN plan_actions a ON a.task_id=c.task_id WHERE a.kind='accept' AND a.status IN ('pending','dispatched') ORDER BY a.action_id`, root)
	if err != nil {
		return nil, err
	}
	for _, id := range actions {
		if err := tx.SetActionStatus(ctx, id, protocol.ActionCancelled, x.Now.UnixMilli()); err != nil {
			return nil, err
		}
	}
	runs, err := store.FollowupIDs(ctx, tx.Q(), `WITH RECURSIVE scope(id) AS (SELECT issue_id FROM issue_controls WHERE issue_id=? UNION ALL SELECT c.issue_id FROM issue_controls c JOIN scope s ON c.parent_issue_id=s.id)
 SELECT r.run_id FROM run_issue_responsibilities d JOIN scope s ON s.id=d.issue_id JOIN runs r USING(run_id) WHERE r.role='accept' AND r.observed_state IN ('starting','running','waiting','interrupted','unknown') ORDER BY r.run_id`, root)
	if err != nil {
		return nil, err
	}
	var events []Event
	for _, run := range runs {
		if _, err := tx.Exec(ctx, `UPDATE runs SET desired_state='stopped',updated_at=? WHERE run_id=?`, x.Now.UnixMilli(), run); err != nil {
			return nil, err
		}
		events = append(events, Event{ObjectType: "run", ObjectID: run, Kind: "run.stop_requested", Body: map[string]any{"issue_id": root, "reason": "review responsibility cancelled"}})
	}
	return events, nil
}

func appendDelegatedRunFacts(ctx context.Context, q store.Querier, root string, f *domain.IssueLifecycleFacts) error {
	rows, err := q.QueryContext(ctx, `WITH RECURSIVE scope(id) AS (SELECT issue_id FROM issue_controls WHERE issue_id=? UNION ALL SELECT c.issue_id FROM issue_controls c JOIN scope s ON c.parent_issue_id=s.id)
 SELECT r.run_id,r.generation,r.observed_state,
 EXISTS(SELECT 1 FROM leases l WHERE l.run_id=r.run_id AND l.status<>'released'),
 EXISTS(SELECT 1 FROM run_messages m WHERE m.run_id=r.run_id AND m.status IN ('dispatching','unknown'))
 FROM run_issue_responsibilities d JOIN scope s ON s.id=d.issue_id JOIN runs r USING(run_id)
 WHERE NOT EXISTS(SELECT 1 FROM task_work_item_owners o JOIN scope owned ON owned.id=o.work_item_id WHERE o.task_id=r.task_id) ORDER BY r.run_id`, root)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var generation int64
		var observed protocol.RunObserved
		var lease, unknown bool
		if err := rows.Scan(&id, &generation, &observed, &lease, &unknown); err != nil {
			return err
		}
		if observed != protocol.RunFinished && observed != protocol.RunFailed || lease {
			f.Executions = append(f.Executions, protocol.IssueBlocker{Code: "delegated_execution", ObjectID: id, Revision: generation})
		}
		if observed == protocol.RunUnknown || unknown {
			f.UnknownOperations = append(f.UnknownOperations, protocol.IssueBlocker{Code: "delegated_execution_unknown", ObjectID: id, Revision: generation})
		}
	}
	return rows.Err()
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
