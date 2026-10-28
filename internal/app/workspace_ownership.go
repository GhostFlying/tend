package app

import (
	"context"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// unusedWorkspaceRebind is deliberately narrower than general workspace moves:
// only a queued reuse agreement that never ran can become an independent one.
func unusedWorkspaceRebind(ctx context.Context, q store.Querier, task store.TaskRow, current, next domain.Workspace) (*store.WorkspaceRow, error) {
	if !task.WorkspaceAssigned || current.ModeValue() != domain.WorkspaceReuse || (next.ModeValue() != domain.WorkspaceNew && next.ModeValue() != domain.WorkspaceTemporary) {
		return nil, nil
	}
	var unsafe bool
	// Worker effects always have a run first. A dispatched action without a
	// run is also unresolved and cannot be treated as an unused agreement.
	if err := q.QueryRowContext(ctx, `SELECT
 EXISTS(SELECT 1 FROM runs WHERE task_id=?) OR
 EXISTS(SELECT 1 FROM workspaces WHERE occupied_by=?) OR
 EXISTS(SELECT 1 FROM plan_actions WHERE task_id=? AND status='dispatched')`, task.TaskID, task.TaskID, task.TaskID).Scan(&unsafe); err != nil {
		return nil, err
	}
	if unsafe {
		return nil, protocol.Errorf(protocol.CodeInvalidTransition, "workspace rebinding requires a task with no run history, resource occupancy or dispatched effects")
	}
	if task.Lifecycle != protocol.TaskQueued && task.Lifecycle != protocol.TaskDraft {
		return nil, protocol.Errorf(protocol.CodeInvalidTransition, "only an unexecuted queued or draft reuse task can change its workspace binding")
	}
	resource, err := store.GetWorkspaceByTask(ctx, q, task.TaskID)
	if err != nil {
		return nil, err
	}
	if resource.Mode != domain.WorkspaceReuse {
		return nil, protocol.Errorf(protocol.CodeInvalidTransition, "only an unused reuse binding can become an independent workspace")
	}
	return &resource, nil
}

func checkDefaultWorkspaceOwner(ctx context.Context, q store.Querier, itemID, sourceTaskID string) error {
	owner, err := store.GetTaskIssueOwner(ctx, q, sourceTaskID)
	if err != nil {
		return err
	}
	if owner == "" || owner != itemID {
		return protocol.Errorf(protocol.CodeInvalidRequest, "default workspace source must belong to the same verified primary work item")
	}
	return nil
}
