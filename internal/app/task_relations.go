package app

import (
	"context"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// validateRelations resolves references in the write transaction and rejects
// follow-up cycles. General references may connect any projects or lifecycles.
func validateRelations(ctx context.Context, q store.Querier, taskID string, refs []protocol.TaskRelation) ([]protocol.TaskRelation, error) {
	normalized, err := domain.NormalizeTaskRelations(taskID, refs)
	if err != nil {
		return nil, err
	}
	for _, r := range normalized {
		if _, err := store.GetTask(ctx, q, r.TaskID); err != nil {
			return nil, errNotFound(err, "related task %s does not exist", r.TaskID)
		}
		if r.Kind == protocol.RelationFollowUp {
			cycle, err := store.FollowsUp(ctx, q, r.TaskID, taskID)
			if err != nil {
				return nil, err
			}
			if cycle {
				return nil, protocol.Errorf(protocol.CodeInvalidRequest, "follow-up references must not form a cycle")
			}
		}
	}
	return normalized, nil
}

func declaredRelations(ctx context.Context, q store.Querier, taskID string) ([]protocol.TaskRelation, error) {
	rows, err := store.ListTaskRelations(ctx, q, taskID)
	if err != nil {
		return nil, err
	}
	var refs []protocol.TaskRelation
	for _, r := range rows {
		if r.SourceTaskID == taskID {
			refs = append(refs, protocol.TaskRelation{Kind: r.Kind, TaskID: r.TargetTaskID})
		}
	}
	return refs, nil
}

// backlinkEvents notify the tasks whose derived incoming references changed.
// Their control revisions remain unchanged because the source owns the write.
func backlinkEvents(ctx context.Context, q store.Querier, source string, previous, next []protocol.TaskRelation) ([]Event, error) {
	seen := map[string]bool{}
	var out []Event
	for _, refs := range [][]protocol.TaskRelation{previous, next} {
		for _, r := range refs {
			if seen[r.TaskID] {
				continue
			}
			seen[r.TaskID] = true
			task, err := store.GetTask(ctx, q, r.TaskID)
			if err != nil {
				return nil, err
			}
			out = append(out, Event{ObjectType: "task", ObjectID: task.TaskID, ObjectVersion: task.ControlRevision, Kind: "task.relations_backlinks_updated", Body: map[string]any{"source_task_id": source}})
		}
	}
	return out, nil
}
