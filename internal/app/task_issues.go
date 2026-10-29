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
	"github.com/GhostFlying/tend/internal/views"
)

func freezeIssueContext(item protocol.WorkItem) protocol.TaskWorkContext {
	return protocol.TaskWorkContext{WorkItemID: item.WorkItemID, Revision: item.Revision, Intent: item.Intent, Facts: slices.Clone(item.Background)}
}

func applyIssueDependencies(ctx context.Context, q store.Querier, candidates []scheduler.Candidate) error {
	return views.ApplyIssueDependencies(ctx, q, candidates)
}

// taskIssue resolves the explicit concern or creates one for a standalone
// task from an older client. It never infers that a follow-up should be merged
// into another concern or inherits a workspace merely from a relationship.
func taskIssue(ctx context.Context, x *Exec, tx *store.Tx, p *createTaskPayload, project string) (protocol.WorkItem, []Event, error) {
	if p.WorkItemID != "" {
		item, err := store.GetIssue(ctx, tx.Q(), p.WorkItemID)
		if err != nil {
			return item, nil, errNotFound(err, "work item does not exist")
		}
		if err := store.RejectCanonicalLegacyIssueWrite(ctx, tx.Q(), item.WorkItemID); err != nil {
			return item, nil, err
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
			return item, nil, missingIssueStageIDs(item.WorkItemID, p.StageIDs)
		}
		if p.UseDefaultWorkspace {
			if item.DefaultWorkspaceTaskID == "" {
				return item, nil, protocol.Errorf(protocol.CodeInvalidRequest, "work item has no default workspace source task")
			}
			if err := checkDefaultWorkspaceOwner(ctx, tx.Q(), item.WorkItemID, item.DefaultWorkspaceTaskID); err != nil {
				return item, nil, err
			}
			p.Spec.Workspace = domain.Workspace{Mode: domain.WorkspaceReuse, SourceTaskID: item.DefaultWorkspaceTaskID, IncludeExistingChanges: p.Spec.Workspace.IncludeExistingChanges}
		}
		p.Spec.SharedContext = []protocol.TaskWorkContext{freezeIssueContext(item)}
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
	item := protocol.WorkItem{IssuePlan: protocol.IssuePlan{Title: p.Title, Project: project, Intent: p.Spec.Goal, Background: facts, Stages: []protocol.IssueStage{{StageID: "initial", Title: p.Title, Status: "planned"}}}, WorkItemID: x.IDs.New(protocol.PrefixWorkItem), Revision: 1, Status: "open", CreatedAt: x.Now.UnixMilli(), UpdatedAt: x.Now.UnixMilli()}
	out, err := saveIssue(ctx, tx, item, 0, "work_item.created", map[string]string{"source": "standalone task"})
	if err != nil {
		return item, nil, err
	}
	if _, err := ensureAgentConversation(ctx, x, tx, item.WorkItemID); err != nil {
		return item, nil, err
	}
	// Standalone requests have an explicit task specification but no hosted
	// conversational review. Record that bounded program baseline rather than
	// leave their future events permanently inadmissible.
	if _, err := tx.Exec(ctx, `UPDATE work_item_contexts SET baseline_revision=revision+1,revision=revision+1,coverage=?,gaps=?,prepared_by='program' WHERE work_item_id=?`, "Initial standalone task request: goal, constraints, stages and task specification.", "No additional conversation or owner authorization was supplied or inferred.", item.WorkItemID); err != nil {
		return item, nil, err
	}
	p.WorkItemID, p.WorkItemRevision, p.StageIDs = item.WorkItemID, item.Revision, []string{"initial"}
	p.Spec.SharedContext = []protocol.TaskWorkContext{freezeIssueContext(item)}
	return item, out.Events, nil
}

func missingIssueStageIDs(itemID string, values []string) *protocol.Error {
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
	items, canonical, err := store.TaskIssueContextItems(ctx, q, taskID)
	if err != nil {
		return err
	}
	fingerprint := canonical
	for _, old := range previous {
		fingerprint = fingerprint || old.ContextDigest != ""
	}
	spec.SharedContext = nil
	for _, item := range items {
		next := freezeIssueContext(item)
		if canonical {
			next = freezeCanonicalIssueDefinition(item)
		}
		if fingerprint {
			digest, facts, err := store.EffectiveContext(ctx, q, item.WorkItemID)
			if err != nil {
				return err
			}
			next.ContextDigest = digest
			next.Facts = append(next.Facts, facts...)
		}

		for _, old := range previous {
			if old.WorkItemID == next.WorkItemID && old.ContextDigest == next.ContextDigest && old.Intent == next.Intent && slices.Equal(old.Facts, next.Facts) && slices.Equal(old.Constraints, next.Constraints) {
				next = old
				break
			}
		}
		spec.SharedContext = append(spec.SharedContext, next)
	}
	return nil
}

// validateIssueTaskDependencies observes the candidate plan and all existing
// shared-task links in the same write transaction. Legacy task dependencies
// participate because the scheduler enforces both kinds of prerequisites.
func validateIssueTaskDependencies(ctx context.Context, q store.Querier, candidate protocol.WorkItem) error {
	items, err := store.ListIssues(ctx, q, "", "")
	if err != nil {
		return err
	}
	items = slices.DeleteFunc(items, func(item protocol.WorkItem) bool { return item.WorkItemID == candidate.WorkItemID })
	items = append(items, candidate)
	// Project only dispatch responsibility into stage waits. A candidate's new
	// legacy links are not persisted yet, so retain noncanonical shared tasks;
	// only canonical tasks have one immutable primary and evidence-only refs.
	scopes := map[string]store.TaskIssueScope{}
	for i := range items {
		items[i].Stages = slices.Clone(items[i].Stages)
		for j := range items[i].Stages {
			stage := &items[i].Stages[j]
			members := make([]string, 0, len(stage.TaskIDs))
			for _, id := range stage.TaskIDs {
				scope, found := scopes[id]
				if !found {
					scope, err = store.ResolveTaskIssueScope(ctx, q, id)
					if err != nil {
						return err
					}
					scopes[id] = scope
				}
				if !scope.Canonical || scope.PrimaryID == items[i].WorkItemID {
					members = append(members, id)
				}
			}
			stage.TaskIDs = members
		}
	}
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
	return domain.ValidateIssueTaskDependencies(items, lifecycles, taskDependencies)
}
