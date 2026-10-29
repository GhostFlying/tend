package views

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// Issue reads one dynamic plan and the task facts needed by its card.
func Issue(ctx context.Context, q store.Querier, id string) (protocol.WorkItemView, error) {
	w, err := store.GetIssue(ctx, q, id)
	if err != nil {
		return protocol.WorkItemView{}, err
	}
	facts, err := readExecutionFacts(ctx, q)
	if err != nil {
		return protocol.WorkItemView{}, err
	}
	factsSeq, err := store.LatestEventSeq(ctx, q)
	if err != nil {
		return protocol.WorkItemView{}, err
	}
	return issueView(ctx, q, w, facts, factsSeq)
}

func issueView(ctx context.Context, q store.Querier, w protocol.WorkItem, facts executionFacts, factsSeq int64) (protocol.WorkItemView, error) {
	v := protocol.WorkItemView{WorkItem: w, FactsSeq: factsSeq, Column: "unplanned"}
	ids, err := store.IssueTaskIDs(ctx, q, w.WorkItemID)
	if err != nil {
		return v, err
	}
	var tasks []store.TaskRow
	for _, id := range ids {
		task, err := store.GetTask(ctx, q, id)
		if err != nil {
			return v, err
		}
		tasks = append(tasks, task)
	}
	v.Tasks, err = facts.summaries(ctx, q, tasks)
	if err != nil {
		return v, err
	}
	v.PendingReviews, err = store.ListPendingCompletionReviews(ctx, q, w.WorkItemID)
	if err != nil {
		return v, err
	}
	summary, err := store.FollowupOverview(ctx, q, w.WorkItemID)
	if err != nil {
		return v, err
	}
	v.Followups = &summary
	v.Choices, err = store.ListWorkItemChoices(ctx, q, w.WorkItemID, "")
	if err != nil {
		return v, err
	}
	projectIssueExecution(&v)
	v.Observations, err = store.ListDeliveryObservations(ctx, q, w.WorkItemID)
	if err != nil {
		return v, err
	}
	v.Column = IssueColumn(v)
	return v, nil
}

// IssueColumn uses attention first, retaining concurrent task facts in Tasks.
func IssueColumn(v protocol.WorkItemView) string {
	if v.Status == "closed" {
		return "closed"
	}
	if v.Status == "snoozed" {
		return "snoozed"
	}
	for _, choice := range v.Choices {
		if choice.Status == "open" {
			return "attention"
		}
	}
	for _, observation := range v.Observations {
		if observation.ErrorKind != "" {
			return "attention"
		}
	}
	for _, next := range v.FollowOns {
		if next.Kind == "problem" && next.Status == "open" {
			return "attention"
		}
	}
	running, checking := false, false
	for _, task := range v.Tasks {
		settled := task.Execution == nil || task.Execution.State == protocol.ExecutionCompleted ||
			task.Execution.State == protocol.ExecutionCancelled || task.Execution.State == protocol.ExecutionIdle
		if task.Lifecycle.Terminal() && settled {
			continue
		}
		if task.BlockReason != protocol.BlockNone && task.BlockReason != protocol.BlockWaitingResource || task.ControlMode != protocol.ControlManaged {
			return "attention"
		}
		if task.Lifecycle == protocol.TaskAwaitingAcceptance {
			checking = true
		} else {
			running = true
		}
	}
	if running {
		return "active"
	}
	if checking {
		return "checking"
	}
	return "unplanned"
}

// Issues lists work-item concerns independently of terminal task filtering.
func Issues(ctx context.Context, q store.Querier, project string, includeClosed bool, column string) (protocol.WorkItemPage, error) {
	items, err := store.ListIssues(ctx, q, project, "")
	if err != nil {
		return protocol.WorkItemPage{}, err
	}
	page := protocol.WorkItemPage{Items: []protocol.WorkItemView{}}
	if len(items) == 0 {
		return page, nil
	}
	facts, err := readExecutionFacts(ctx, q)
	if err != nil {
		return page, err
	}
	factsSeq, err := store.LatestEventSeq(ctx, q)
	if err != nil {
		return page, err
	}
	for _, item := range items {
		if !includeClosed && item.Status == "closed" {
			continue
		}
		v, err := issueView(ctx, q, item, facts, factsSeq)
		if err != nil {
			return page, err
		}
		if column == "" || column == v.Column {
			page.Items = append(page.Items, v)
		}
	}
	return page, nil
}

// projectIssueExecution aggregates distinct tasks; parent stages include their
// children, but neither stage status nor completion is changed by this read.
func projectIssueExecution(v *protocol.WorkItemView) {
	v.Execution = &protocol.ExecutionCounts{}
	tasks := map[string]*protocol.TaskExecutionView{}
	for _, task := range v.Tasks {
		if _, seen := tasks[task.TaskID]; seen {
			continue
		}
		tasks[task.TaskID] = task.Execution
		countExecution(v.Execution, task.Execution)
	}
	stages := map[string]protocol.IssueStage{}
	for _, stage := range v.Stages {
		stages[stage.StageID] = stage
	}
	for _, stage := range v.Stages {
		counts := protocol.ExecutionCounts{}
		seen := map[string]bool{}
		for _, child := range v.Stages {
			belongs := child.StageID == stage.StageID
			for parent := child.ParentID; parent != "" && !belongs; parent = stages[parent].ParentID {
				belongs = parent == stage.StageID
			}
			if !belongs {
				continue
			}
			for _, id := range child.TaskIDs {
				if !seen[id] {
					seen[id] = true
					if task, ok := tasks[id]; ok {
						countExecution(&counts, task)
					}
				}
			}
		}
		v.StageExecution = append(v.StageExecution, protocol.StageExecutionView{StageID: stage.StageID, Execution: counts})
	}
}
