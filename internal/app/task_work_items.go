package app

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/scheduler"
	"github.com/GhostFlying/tend/internal/store"
)

func freezeWorkItemContext(item protocol.WorkItem) protocol.TaskWorkContext {
	return protocol.TaskWorkContext{WorkItemID: item.WorkItemID, Revision: item.Revision, Intent: item.Intent, Facts: slices.Clone(item.Background)}
}

func applyWorkItemDependencies(ctx context.Context, q store.Querier, candidates []scheduler.Candidate) error {
	for i := range candidates {
		c := &candidates[i]
		items, err := store.ListTaskWorkItems(ctx, q, c.TaskID)
		if err != nil {
			return err
		}
		for _, item := range items {
			if item.Status != "open" {
				c.DependenciesDone = false
				continue
			}
			stages := map[string]protocol.WorkItemStage{}
			for _, stage := range item.Stages {
				stages[stage.StageID] = stage
			}
			for _, stage := range item.Stages {
				if !slices.Contains(stage.TaskIDs, c.TaskID) {
					continue
				}
				if stage.Status == "retired" || stage.Status == "cancelled" {
					c.DependenciesDone = false
				}
				// A child inherits the prerequisites of every containing group.
				for ancestor := stage; ; ancestor = stages[ancestor.ParentID] {
					if ancestor.Status == "retired" || ancestor.Status == "cancelled" {
						c.DependenciesDone = false
					}
					for _, dependency := range ancestor.DependsOn {
						dep := stages[dependency]
						if dep.Status != "done" && !slices.Contains(dep.TaskIDs, c.TaskID) {
							c.DependenciesDone = false
						}
					}
					if ancestor.ParentID == "" {
						break
					}
				}
			}
		}
	}
	return nil
}

// taskWorkItem resolves the explicit concern or creates one for a standalone
// task from an older client. It never infers that a follow-up should be merged
// into another concern or inherits a workspace merely from a relationship.
func taskWorkItem(ctx context.Context, x *Exec, tx *store.Tx, p *createTaskPayload, project string) (protocol.WorkItem, []Event, error) {
	if p.WorkItemID != "" {
		item, err := store.GetWorkItem(ctx, tx.Q(), p.WorkItemID)
		if err != nil {
			return item, nil, errNotFound(err, "work item does not exist")
		}
		if item.Revision != p.WorkItemRevision {
			return item, nil, protocol.Errorf(protocol.CodeStaleRevision, "work item revision changed")
		}
		if item.Status != "open" {
			return item, nil, protocol.Errorf(protocol.CodeInvalidTransition, "resume or reopen the work item before creating work")
		}
		if item.Project != project {
			return item, nil, protocol.Errorf(protocol.CodeInvalidRequest, "task and primary work item must share a project")
		}
		if len(p.StageIDs) == 0 {
			return item, nil, missingWorkItemStageIDs(item.WorkItemID, p.StageIDs)
		}
		if p.UseDefaultWorkspace {
			if item.DefaultWorkspaceTaskID == "" {
				return item, nil, protocol.Errorf(protocol.CodeInvalidRequest, "work item has no default workspace source task")
			}
			p.Spec.Workspace = domain.Workspace{Mode: domain.WorkspaceReuse, SourceTaskID: item.DefaultWorkspaceTaskID, IncludeExistingChanges: p.Spec.Workspace.IncludeExistingChanges}
		}
		p.Spec.SharedContext = []protocol.TaskWorkContext{freezeWorkItemContext(item)}
		return item, nil, nil
	}
	if p.UseDefaultWorkspace || len(p.StageIDs) > 0 || p.WorkItemRevision != 0 {
		return protocol.WorkItem{}, nil, protocol.Errorf(protocol.CodeInvalidRequest, "work_item_id is required")
	}
	// No historical task conversion: this applies only to a new request.
	facts := []protocol.SharedFact{}
	for _, constraint := range p.Spec.Constraints {
		facts = append(facts, protocol.SharedFact{Kind: "user_constraint", Text: constraint, Source: "initial task specification"})
	}
	item := protocol.WorkItem{WorkItemPlan: protocol.WorkItemPlan{Title: p.Title, Project: project, Intent: p.Spec.Goal, Background: facts, Stages: []protocol.WorkItemStage{{StageID: "initial", Title: p.Title, Status: "planned"}}}, WorkItemID: x.IDs.New(protocol.PrefixWorkItem), Revision: 1, Status: "open", CreatedAt: x.Now.UnixMilli(), UpdatedAt: x.Now.UnixMilli()}
	out, err := saveWorkItem(ctx, tx, item, 0, "work_item.created", map[string]string{"source": "standalone task"})
	if err != nil {
		return item, nil, err
	}
	p.WorkItemID, p.WorkItemRevision, p.StageIDs = item.WorkItemID, item.Revision, []string{"initial"}
	p.Spec.SharedContext = []protocol.TaskWorkContext{freezeWorkItemContext(item)}
	return item, out.Events, nil
}

func missingWorkItemStageIDs(itemID string, values []string) *protocol.Error {
	details, _ := json.Marshal(struct {
		Field      string   `json:"field"`
		Value      []string `json:"value"`
		MinItems   int      `json:"min_items"`
		WorkItemID string   `json:"work_item_id"`
	}{"stage_ids", values, 1, itemID})
	return &protocol.Error{Code: protocol.CodeInvalidRequest,
		Message: fmt.Sprintf("stage_ids must contain at least one existing stage_id from work_item_id %q", itemID), Details: details}
}

func refreshTaskWorkContext(ctx context.Context, q store.Querier, taskID string, spec *domain.Spec, previous []protocol.TaskWorkContext) error {
	items, err := store.ListTaskWorkItems(ctx, q, taskID)
	if err != nil {
		return err
	}
	spec.SharedContext = nil
	for _, item := range items {
		next := freezeWorkItemContext(item)
		for _, old := range previous {
			if old.WorkItemID == next.WorkItemID && old.Intent == next.Intent && slices.Equal(old.Facts, next.Facts) && slices.Equal(old.Constraints, next.Constraints) {
				next = old
				break
			}
		}
		spec.SharedContext = append(spec.SharedContext, next)
	}
	return nil
}

// validateWorkItemTaskDependencies observes the candidate plan and all existing
// shared-task links in the same write transaction. Legacy task dependencies
// participate because the scheduler enforces both kinds of prerequisites.
func validateWorkItemTaskDependencies(ctx context.Context, q store.Querier, candidate protocol.WorkItem) error {
	items, err := store.ListWorkItems(ctx, q, "", "")
	if err != nil {
		return err
	}
	items = slices.DeleteFunc(items, func(item protocol.WorkItem) bool { return item.WorkItemID == candidate.WorkItemID })
	items = append(items, candidate)
	lifecycles := map[string]protocol.TaskLifecycle{}
	for _, item := range items {
		for _, stage := range item.Stages {
			for _, taskID := range stage.TaskIDs {
				lifecycles[taskID] = ""
			}
		}
	}
	taskDependencies := map[string][]string{}
	rows, err := q.QueryContext(ctx, `SELECT task_id,depends_on FROM task_dependencies`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var taskID, dependency string
		if err := rows.Scan(&taskID, &dependency); err != nil {
			_ = rows.Close()
			return err
		}
		taskDependencies[taskID] = append(taskDependencies[taskID], dependency)
		lifecycles[taskID], lifecycles[dependency] = "", ""
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	for taskID := range lifecycles {
		task, err := store.GetTask(ctx, q, taskID)
		if err != nil {
			return err
		}
		lifecycles[taskID] = task.Lifecycle
	}
	return domain.ValidateWorkItemTaskDependencies(items, lifecycles, taskDependencies)
}
