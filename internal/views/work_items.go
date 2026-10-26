package views

import (
	"context"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// WorkItem reads one dynamic plan and the task facts needed by its card.
func WorkItem(ctx context.Context, q store.Querier, id string) (protocol.WorkItemView, error) {
	w, err := store.GetWorkItem(ctx, q, id)
	if err != nil {
		return protocol.WorkItemView{}, err
	}
	facts, err := readExecutionFacts(ctx, q)
	if err != nil {
		return protocol.WorkItemView{}, err
	}
	return workItemView(ctx, q, w, facts)
}

func workItemView(ctx context.Context, q store.Querier, w protocol.WorkItem, facts executionFacts) (protocol.WorkItemView, error) {
	v := protocol.WorkItemView{WorkItem: w, Column: "unplanned"}
	seen := map[string]bool{}
	var tasks []store.TaskRow
	for _, stage := range w.Stages {
		for _, id := range stage.TaskIDs {
			if seen[id] {
				continue
			}
			seen[id] = true
			t, err := store.GetTask(ctx, q, id)
			if err != nil {
				return v, err
			}
			tasks = append(tasks, t)
		}
	}
	var err error
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
	projectWorkItemExecution(&v)
	v.Observations, err = store.ListDeliveryObservations(ctx, q, w.WorkItemID)
	if err != nil {
		return v, err
	}
	v.Column = WorkItemColumn(v)
	return v, nil
}

// WorkItemColumn uses attention first, retaining concurrent task facts in Tasks.
func WorkItemColumn(v protocol.WorkItemView) string {
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
		if task.Lifecycle.Terminal() {
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

// WorkItems lists work-item concerns independently of terminal task filtering.
func WorkItems(ctx context.Context, q store.Querier, project string, includeClosed bool, column string) (protocol.WorkItemPage, error) {
	items, err := store.ListWorkItems(ctx, q, project, "")
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
	for _, item := range items {
		if !includeClosed && item.Status == "closed" {
			continue
		}
		v, err := workItemView(ctx, q, item, facts)
		if err != nil {
			return page, err
		}
		if column == "" || column == v.Column {
			page.Items = append(page.Items, v)
		}
	}
	return page, nil
}

// projectWorkItemExecution aggregates distinct tasks; parent stages include their
// children, but neither stage status nor completion is changed by this read.
func projectWorkItemExecution(v *protocol.WorkItemView) {
	v.Execution = &protocol.ExecutionCounts{}
	tasks := map[string]*protocol.TaskExecutionView{}
	for _, task := range v.Tasks {
		if _, seen := tasks[task.TaskID]; seen {
			continue
		}
		tasks[task.TaskID] = task.Execution
		countExecution(v.Execution, task.Execution)
	}
	stages := map[string]protocol.WorkItemStage{}
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
