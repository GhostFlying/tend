package views

import (
	"context"
	"errors"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// CanonicalIssue presents the canonical aggregate without a legacy facade.
// Historical snapshots deliberately contain no current execution/source facts.
func CanonicalIssue(ctx context.Context, q store.Querier, id string, revision int64) (protocol.IssueView, error) {
	var out protocol.IssueView
	var err error
	if revision > 0 {
		out.Issue, err = store.GetCanonicalIssueRevision(ctx, q, id, revision)
		out.Historical = true
		if err != nil {
			return out, err
		}
		return out, issueDetailMetadata(ctx, q, &out)
	}
	out.Issue, err = store.GetCanonicalIssue(ctx, q, id)
	if err != nil {
		return out, err
	}
	manager, e := store.CurrentIssueManagerTask(ctx, q, id)
	if errors.Is(e, store.ErrNotFound) {
		manager, e = store.LatestIssueManagerTask(ctx, q, id)
	}
	if e == nil {
		task, e := store.GetTask(ctx, q, manager.TaskID)
		if e != nil {
			return out, e
		}
		out.Manager = &protocol.IssueManagerView{ControlMode: task.ControlMode, ConversationID: manager.ConversationID, TaskID: manager.TaskID, CategoryID: manager.CategoryID, State: task.Lifecycle, HostID: manager.HostID, Execution: manager.Pin}
		if err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE task_id=? AND observed_state='unknown') OR EXISTS(SELECT 1 FROM hosted_executions e JOIN main_turns t ON t.request_id=e.execution_id WHERE e.issue_id=? AND e.assignment_epoch=? AND (t.status='unknown' OR EXISTS(SELECT 1 FROM native_turn_receipts_pending p WHERE p.request_id=t.request_id) OR (t.status IN ('starting','started') AND NOT EXISTS(SELECT 1 FROM runs r WHERE r.run_id=json_extract(e.private_snapshot,'$.runner_run_id') AND r.observed_state IN ('starting','running','waiting','interrupted','unknown')))))`, manager.TaskID, id, manager.AssignmentEpoch).Scan(&out.Manager.ReconciliationPending); err != nil {
			return out, err
		}
		recoveries, err := store.FollowupIDs(ctx, q, `SELECT r.root_request_id FROM agent_recoveries r JOIN runner_coordination_executions e ON e.execution_id=r.root_request_id WHERE e.issue_id=? AND e.assignment_epoch=? AND r.status IN ('waiting','starting','running','unknown','blocked','exhausted') ORDER BY r.updated_at DESC LIMIT 1`, id, manager.AssignmentEpoch)
		if err != nil {
			return out, err
		}
		if len(recoveries) > 0 {
			recovery, err := store.RecoveryForRequest(ctx, q, recoveries[0])
			if err != nil {
				return out, err
			}
			out.Manager.Recovery = &recovery.AgentRecoveryView
		}
		if err := q.QueryRowContext(ctx, `SELECT COALESCE((SELECT t.thread_id FROM runner_coordination_executions e JOIN main_turns t ON t.request_id=e.execution_id WHERE e.issue_id=? AND e.assignment_epoch=? ORDER BY e.created_at DESC,e.rowid DESC LIMIT 1),''),COALESCE((SELECT run_id FROM runs WHERE task_id=? AND observed_state IN ('starting','running','waiting','interrupted','unknown') ORDER BY created_at DESC LIMIT 1),'')`, id, manager.AssignmentEpoch, manager.TaskID).Scan(&out.Manager.NativeThreadID, &out.Manager.RunID); err != nil {
			return out, err
		}
		if manager.Pin != nil {
			identity := manager.Pin.Identity
			out.Manager.Agent = &identity
		}
		if task.Lifecycle == protocol.TaskQueued {
			facts, e := readExecutionFacts(ctx, q)
			if e != nil {
				return out, e
			}
			if candidate, ok := facts.candidates[task.TaskID]; ok {
				out.Manager.QueueReason = facts.waitReason(candidate)
			}
		}
	} else if !errors.Is(e, store.ErrNotFound) {
		return out, e
	}
	current, err := Issue(ctx, q, id)
	if err != nil {
		return out, err
	}
	for _, task := range current.Tasks {
		out.Tasks = append(out.Tasks, protocol.IssueTaskSummary{TaskID: task.TaskID, Title: task.Title, Lifecycle: task.Lifecycle, ControlMode: task.ControlMode, ControlRevision: task.ControlRevision, SpecVersion: task.SpecVersion, Rating: task.Rating, BlockReason: task.BlockReason, Execution: task.Execution})
	}
	out.PendingReviews = current.PendingReviews
	if err := q.QueryRowContext(ctx, `SELECT revision FROM work_item_contexts WHERE work_item_id=?`, id).Scan(&out.ContextRevision); err != nil {
		return out, err
	}
	rows, err := q.QueryContext(ctx, `SELECT m.message_id,m.author,m.body,COALESCE(d.content_revision,1),COALESCE(a.revision,0),COALESCE(c.withdrawn_at,0)<>0
 FROM message_context_obligations o JOIN conversation_messages m USING(message_id)
 LEFT JOIN message_dispositions d USING(message_id) LEFT JOIN message_association_revisions a USING(message_id)
 LEFT JOIN conversation_message_controls c USING(message_id)
 WHERE o.work_item_id=? AND o.needs_review=1 ORDER BY m.message_id`, id)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var source protocol.IssueSourceReview
		if err := rows.Scan(&source.MessageID, &source.Author, &source.Body, &source.ContentRevision, &source.AssociationRevision, &source.Withdrawn); err != nil {
			return out, err
		}
		out.SourceReviews = append(out.SourceReviews, source)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	rows.Close()
	page, err := Attentions(ctx, q, AttentionFilter{IssueID: id, IncludeArchived: true})
	if err != nil {
		return out, err
	}
	out.Attentions = &page
	out.Deadlines, err = Deadlines(ctx, q, id)
	if err != nil {
		return out, err
	}
	return out, issueDetailMetadata(ctx, q, &out)
}

type CanonicalIssueFilter struct {
	Board         bool
	BoardStatus   protocol.IssueWorkStatus
	AssigneeKind  protocol.IssueAssigneeKind
	AssigneeRef   string
	ReadRoot      string
	Project       string
	ParentID      *string
	State         protocol.IssueState
	IncludeClosed bool
	After         string
	Limit         int
}

// CanonicalIssues never adopts old plans that lack canonical controls.
func CanonicalIssues(ctx context.Context, q store.Querier, f CanonicalIssueFilter) (protocol.IssuePage, error) {
	out := protocol.IssuePage{Items: []protocol.IssueSummary{}}
	if f.Limit == 0 {
		f.Limit = 50
	}
	if f.Limit < 1 || f.Limit > 100 {
		return out, protocol.Errorf(protocol.CodeInvalidRequest, "limit must be between 1 and 100")
	}
	if f.State != "" && !slices.Contains([]protocol.IssueState{protocol.IssueActive, protocol.IssuePaused, protocol.IssueCancelling, protocol.IssueCancelled, protocol.IssueClosed}, f.State) {
		return out, protocol.Errorf(protocol.CodeInvalidRequest, "invalid Issue state")
	}
	if f.Board {
		return canonicalIssueBoard(ctx, q, f)
	}

	query := `SELECT c.issue_id FROM issue_controls c JOIN work_items w ON w.work_item_id=c.issue_id WHERE c.issue_id>?`
	args := []any{f.After}
	if f.ReadRoot != "" {
		query += ` AND c.issue_id IN (WITH RECURSIVE tree(id) AS (SELECT ? UNION ALL SELECT i.issue_id FROM issue_controls i JOIN tree p ON i.parent_issue_id=p.id) SELECT id FROM tree)`
		args = append(args, f.ReadRoot)
	}

	if f.Project != "" {
		query += ` AND w.project=?`
		args = append(args, f.Project)
	}
	if f.ParentID != nil {
		query += ` AND COALESCE(c.parent_issue_id,'')=?`
		args = append(args, *f.ParentID)
	}
	if f.State != "" {
		query += ` AND c.state=?`
		args = append(args, f.State)
	} else if !f.IncludeClosed {
		query += ` AND c.state NOT IN ('closed','cancelled')`
	}
	query += ` ORDER BY c.issue_id LIMIT ?`
	args = append(args, f.Limit+1)
	ids, err := store.FollowupIDs(ctx, q, query, args...)
	if err != nil {
		return out, err
	}
	if len(ids) > f.Limit {
		out.More = true
		ids = ids[:f.Limit]
	}
	for _, id := range ids {
		issue, err := store.GetCanonicalIssue(ctx, q, id)
		if err != nil {
			return out, err
		}
		out.Items = append(out.Items, protocol.IssueSummary{IssueControl: issue.IssueControl, Revision: issue.Revision, Title: issue.Plan.Title, Project: issue.Plan.Project})
	}
	if out.More && len(ids) > 0 {
		out.Next = ids[len(ids)-1]
	}
	return out, nil
}
