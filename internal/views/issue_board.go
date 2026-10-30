package views

import (
	"context"
	"slices"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

const issueBoardStatusSQL = `CASE c.state WHEN 'closed' THEN 'done' WHEN 'cancelled' THEN 'cancelled' ELSE COALESCE(NULLIF(c.work_status,''),'todo') END`

func canonicalIssueBoard(ctx context.Context, q store.Querier, f CanonicalIssueFilter) (protocol.IssuePage, error) {
	out := protocol.IssuePage{Items: []protocol.IssueSummary{}, Counts: map[protocol.IssueWorkStatus]int{}, Assignees: []protocol.IssueAssignee{}}
	for _, status := range protocol.IssueWorkStatusValues() {
		out.Counts[status] = 0
	}
	if f.BoardStatus != "" && !slices.Contains(protocol.IssueWorkStatusValues(), f.BoardStatus) {
		return out, protocol.Errorf(protocol.CodeInvalidRequest, "invalid board status")
	}
	if f.AssigneeRef != "" && f.AssigneeKind != protocol.IssueAssigneeOwner && f.AssigneeKind != protocol.IssueAssigneeAgent {
		return out, protocol.Errorf(protocol.CodeInvalidRequest, "assignee kind is required")
	}
	// Board scope always means roots, even when another filter is present.
	base := ` FROM issue_controls c JOIN work_items w ON w.work_item_id=c.issue_id WHERE c.parent_issue_id IS NULL`
	args := []any{}
	if f.ReadRoot != "" {
		base += ` AND c.issue_id=?`
		args = append(args, f.ReadRoot)
	}
	if f.Project != "" {
		base += ` AND w.project=?`
		args = append(args, f.Project)
	}
	if f.State != "" {
		base += ` AND c.state=?`
		args = append(args, f.State)
	}
	if !f.IncludeClosed && f.State == "" {
		base += ` AND c.state NOT IN ('closed','cancelled')`
	}
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT c.assignee_kind,c.assignee_ref`+base+` ORDER BY c.assignee_kind,c.assignee_ref`, args...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var a protocol.IssueAssignee
		if err = rows.Scan(&a.Kind, &a.Ref); err != nil {
			rows.Close()
			return out, err
		}
		out.Assignees = append(out.Assignees, a)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if f.AssigneeRef != "" {
		base += ` AND c.assignee_kind=? AND c.assignee_ref=?`
		args = append(args, f.AssigneeKind, f.AssigneeRef)
	}
	rows, err = q.QueryContext(ctx, `SELECT `+issueBoardStatusSQL+`,COUNT(*)`+base+` GROUP BY `+issueBoardStatusSQL, args...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var status protocol.IssueWorkStatus
		var n int
		if err = rows.Scan(&status, &n); err != nil {
			rows.Close()
			return out, err
		}
		out.Counts[status] = n
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if f.BoardStatus != "" {
		base += ` AND ` + issueBoardStatusSQL + `=?`
		args = append(args, f.BoardStatus)
	}
	base += ` AND c.issue_id>? ORDER BY c.issue_id LIMIT ?`
	args = append(args, f.After, f.Limit+1)
	ids, err := store.FollowupIDs(ctx, q, `SELECT c.issue_id`+base, args...)
	if err != nil {
		return out, err
	}
	if len(ids) > f.Limit {
		out.More = true
		ids = ids[:f.Limit]
		out.Next = ids[len(ids)-1]
	}
	out.FactsSeq, err = store.LatestEventSeq(ctx, q)
	if err != nil {
		return out, err
	}
	if len(ids) == 0 {
		return out, nil
	}
	facts, err := readExecutionFacts(ctx, q)
	if err != nil {
		return out, err
	}
	for _, id := range ids {
		issue, err := store.GetCanonicalIssue(ctx, q, id)
		if err != nil {
			return out, err
		}
		card, err := issueBoardCard(ctx, q, issue, facts)
		if err != nil {
			return out, err
		}
		out.Items = append(out.Items, protocol.IssueSummary{IssueControl: issue.IssueControl, Revision: issue.Revision, Title: issue.Plan.Title, Project: issue.Plan.Project, Board: &card})
	}
	return out, nil
}

func issueBoardCard(ctx context.Context, q store.Querier, issue protocol.Issue, facts executionFacts) (protocol.IssueBoardCard, error) {
	card := protocol.IssueBoardCard{Status: issue.BoardStatus()}
	for _, stage := range issue.Plan.Stages {
		if stage.Status == protocol.WorkItemStageRetired {
			continue
		}
		card.StagesTotal++
		if stage.Status == protocol.WorkItemStageDone {
			card.StagesDone++
		}
		if stage.Status == protocol.WorkItemStageActive && card.StageTitle == "" {
			card.StageTitle = stage.Title
		}
	}
	if card.StageTitle == "" {
		for _, stage := range issue.Plan.Stages {
			if stage.Status == protocol.WorkItemStagePlanned {
				card.StageTitle = stage.Title
				break
			}
		}
	}
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM attentions WHERE issue_id=? AND status='open' AND kind IN ('action','decision')`, issue.IssueID).Scan(&card.AttentionCount); err != nil {
		return card, err
	}
	if issue.Assignee.Kind == protocol.IssueAssigneeAgent {
		if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(due_at),0) FROM issue_status_notifications WHERE issue_id=?`, issue.IssueID).Scan(&card.NotificationDueAt); err != nil {
			return card, err
		}
	}
	// Primary execution ownership is authoritative. Evidence links to another
	// Issue's task are not this card's live execution.
	ids, err := store.FollowupIDs(ctx, q, `SELECT task_id FROM task_work_item_owners WHERE work_item_id=? ORDER BY task_id`, issue.IssueID)
	if err != nil {
		return card, err
	}
	var tasks []store.TaskRow
	for _, id := range ids {
		task, err := store.GetTask(ctx, q, id)
		if err != nil {
			return card, err
		}
		tasks = append(tasks, task)
	}
	summaries, err := facts.summaries(ctx, q, tasks)
	if err != nil {
		return card, err
	}
	for _, task := range summaries {
		countExecution(&card.Execution, task.Execution)
	}
	return card, nil
}
