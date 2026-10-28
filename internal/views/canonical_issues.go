package views

import (
	"context"

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
		return out, err
	}
	out.Issue, err = store.GetCanonicalIssue(ctx, q, id)
	if err != nil {
		return out, err
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
	return out, rows.Err()
}

type CanonicalIssueFilter struct {
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
	query := `SELECT c.issue_id FROM issue_controls c JOIN work_items w ON w.work_item_id=c.issue_id WHERE c.issue_id>?`
	args := []any{f.After}
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
