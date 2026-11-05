package app

import (
	"context"
	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func validateCanonicalTaskDependencies(ctx context.Context, q store.Querier) error {
	return validateTaskWaitDependencies(ctx, q, nil)
}

// All graph-writing routes see canonical, legacy and continuation waits in the
// same transaction, including a legacy plan that has not been saved yet.
func validateTaskWaitDependencies(ctx context.Context, q store.Querier, candidate *protocol.WorkItem) error {
	ids, err := store.FollowupIDs(ctx, q, `SELECT issue_id FROM issue_controls ORDER BY issue_id`)
	if err != nil {
		return err
	}
	issues := make([]protocol.Issue, 0, len(ids))
	primary := map[string][]string{}
	lives := map[string]protocol.TaskLifecycle{}
	canonical := map[string]bool{}
	for _, id := range ids {
		issue, err := store.GetCanonicalIssue(ctx, q, id)
		if err != nil {
			return err
		}
		issues = append(issues, issue)
		canonical[id] = true
		primary[id], err = store.IssueTaskIDs(ctx, q, id)
		if err != nil {
			return err
		}
		for _, task := range primary[id] {
			lives[task] = ""
		}
	}
	plans, err := taskWaitPlans(ctx, q, candidate)
	if err != nil {
		return err
	}
	legacy := make([]protocol.WorkItem, 0, len(plans))
	for _, plan := range plans {
		if canonical[plan.WorkItemID] {
			continue
		}
		legacy = append(legacy, plan)
		for _, stage := range plan.Stages {
			for _, task := range stage.TaskIDs {
				lives[task] = ""
			}
		}
	}
	deps, err := store.TaskWaitDependencies(ctx, q)
	if err != nil {
		return err
	}
	for task, dependencies := range deps {
		lives[task] = ""
		for _, dep := range dependencies {
			lives[dep] = ""
		}
	}
	for id := range lives {
		task, err := store.GetTask(ctx, q, id)
		if err != nil {
			return err
		}
		lives[id] = task.Lifecycle
	}
	return domain.ValidateIssuePrimaryTaskWaits(issues, primary, lives, deps, legacy...)
}
