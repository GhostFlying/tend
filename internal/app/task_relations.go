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
