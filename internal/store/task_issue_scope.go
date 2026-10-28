package store

import (
	"context"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
)

// TaskIssueScope separates immutable primary responsibility from evidence links.
// Legacy shared responsibilities retain their original link-based semantics.
type TaskIssueScope struct {
	PrimaryID        string
	Canonical        bool
	Responsibilities []protocol.WorkItem
	References       []protocol.WorkItem
}

func ResolveTaskIssueScope(ctx context.Context, q Querier, taskID string) (TaskIssueScope, error) {
	var scope TaskIssueScope
	var err error
	scope.PrimaryID, err = GetTaskIssueOwner(ctx, q, taskID)
	if err != nil {
		return scope, err
	}
	if err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM issue_controls WHERE issue_id=?)`, scope.PrimaryID).Scan(&scope.Canonical); err != nil {
		return scope, err
	}
	linked, err := ListTaskIssues(ctx, q, taskID)
	if err != nil {
		return scope, err
	}
	if scope.Canonical {
		primary, err := GetIssue(ctx, q, scope.PrimaryID)
		if err != nil {
			return scope, err
		}
		scope.Responsibilities = []protocol.WorkItem{primary}
	}
	for _, item := range linked {
		if scope.Canonical {
			if item.WorkItemID != scope.PrimaryID {
				scope.References = append(scope.References, item)
			}
			continue
		}
		var canonical bool
		if err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM issue_controls WHERE issue_id=?)`, item.WorkItemID).Scan(&canonical); err != nil {
			return scope, err
		}
		if canonical {
			scope.References = append(scope.References, item)
		} else {
			scope.Responsibilities = append(scope.Responsibilities, item)
		}
	}
	return scope, nil
}

// TaskIssueResponsibilities never promotes result references into control scope.
func TaskIssueResponsibilities(ctx context.Context, q Querier, taskID string) ([]protocol.WorkItem, error) {
	scope, err := ResolveTaskIssueScope(ctx, q, taskID)
	return scope.Responsibilities, err
}

// TaskIssueContextItems adds inherited shared context, not additional task owners.
func TaskIssueContextItems(ctx context.Context, q Querier, taskID string) ([]protocol.WorkItem, bool, error) {
	scope, err := ResolveTaskIssueScope(ctx, q, taskID)
	if err != nil {
		return nil, false, err
	}
	items := slices.Clone(scope.Responsibilities)
	if scope.Canonical {
		for id := scope.PrimaryID; id != ""; {
			issue, err := GetCanonicalIssue(ctx, q, id)
			if err != nil {
				return nil, true, err
			}
			id = issue.ParentIssueID
			if id != "" {
				parent, err := GetIssue(ctx, q, id)
				if err != nil {
					return nil, true, err
				}
				items = append(items, parent)
			}
		}
	}
	return items, scope.Canonical, nil
}

func TaskIssueResponsible(ctx context.Context, q Querier, taskID, issueID string) (bool, error) {
	items, err := TaskIssueResponsibilities(ctx, q, taskID)
	return slices.ContainsFunc(items, func(item protocol.WorkItem) bool { return item.WorkItemID == issueID }), err
}

// TaskIssueEvidenceReadable is bounded to one task's explicitly linked result.
// It does not confer source, control, completion-review or conversation authority.
func TaskIssueEvidenceReadable(ctx context.Context, q Querier, taskID, issueID string) (bool, error) {
	scope, err := ResolveTaskIssueScope(ctx, q, taskID)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(scope.Responsibilities, func(item protocol.WorkItem) bool { return item.WorkItemID == issueID }) || slices.ContainsFunc(scope.References, func(item protocol.WorkItem) bool { return item.WorkItemID == issueID }), nil
}

// IssueTaskIDs lists direct responsibility members, without inventing a stage.
// Legacy views keep their plan-stage ordering and membership.
func IssueTaskIDs(ctx context.Context, q Querier, issueID string) ([]string, error) {
	var canonical bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM issue_controls WHERE issue_id=?)`, issueID).Scan(&canonical); err != nil {
		return nil, err
	}
	if canonical {
		return FollowupIDs(ctx, q, `SELECT task_id FROM task_work_item_owners WHERE work_item_id=? ORDER BY task_id`, issueID)
	}
	item, err := GetIssue(ctx, q, issueID)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for _, stage := range item.Stages {
		for _, id := range stage.TaskIDs {
			responsible, err := TaskIssueResponsible(ctx, q, id, issueID)
			if err != nil {
				return nil, err
			}
			if !responsible {
				continue
			}
			if !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	return ids, nil
}
