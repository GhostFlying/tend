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
	return workItemView(ctx, q, w)
}

func workItemView(ctx context.Context, q store.Querier, w protocol.WorkItem) (protocol.WorkItemView, error) {
	v := protocol.WorkItemView{WorkItem: w, Column: "unplanned"}
	seen := map[string]bool{}
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
			v.Tasks = append(v.Tasks, Summary(t))
		}
	}
	var err error
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
	for _, item := range items {
		if !includeClosed && item.Status == "closed" {
			continue
		}
		v, err := workItemView(ctx, q, item)
		if err != nil {
			return page, err
		}
		if column == "" || column == v.Column {
			page.Items = append(page.Items, v)
		}
	}
	return page, nil
}
