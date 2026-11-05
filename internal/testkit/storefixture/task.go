package storefixture

import (
	"context"
	"fmt"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// TaskSeed captures a real, newly queued task's read/scheduling projection.
// Copies belong to the same issue. This deliberately omits creation requests,
// events and outbox work: creation and issue workflow tests must use the app.
type TaskSeed struct {
	Row     store.TaskRow
	spec    string
	owner   string
	actions []store.ActionRow
}

// ReadTaskSeed reads the template in the caller's transaction.
func ReadTaskSeed(ctx context.Context, q store.Querier, id string) (TaskSeed, error) {
	var seed TaskSeed
	var err error
	seed.Row, err = store.GetTask(ctx, q, id)
	if err != nil {
		return seed, err
	}
	if seed.Row.SpecVersion != 1 || seed.Row.Lifecycle != protocol.TaskQueued || seed.Row.WorkspaceAssigned {
		return seed, fmt.Errorf("task seed requires a newly queued, unassigned task")
	}
	seed.spec, err = store.GetSpec(ctx, q, id, 1)
	if err != nil {
		return seed, err
	}
	seed.owner, err = store.GetTaskIssueOwner(ctx, q, id)
	if err != nil {
		return seed, err
	}
	seed.actions, err = store.ListActions(ctx, q, id)
	return seed, err
}

// Insert writes a distinct task, spec, ownership and action projection. The
// caller supplies its row and ID generator and owns the preparation transaction.
func (seed TaskSeed) Insert(ctx context.Context, tx *store.Tx, ids protocol.IDGen, row store.TaskRow) error {
	if err := tx.InsertTask(ctx, row); err != nil {
		return err
	}
	if err := tx.InsertSpec(ctx, row.TaskID, 1, seed.spec, "", row.CreatedAt); err != nil {
		return err
	}
	if seed.owner != "" {
		if err := tx.InsertTaskIssueOwner(ctx, row.TaskID, seed.owner); err != nil {
			return err
		}
	}
	for _, action := range seed.actions {
		action.TaskID, action.ActionID = row.TaskID, ids.New(protocol.PrefixAction)
		action.EffectKey = (domain.ActionDraft{TaskID: row.TaskID, Kind: action.Kind, SpecVersion: action.TargetSpecVersion, ArtifactID: action.TargetArtifactID, Generation: action.Generation}).EffectKey()
		action.CreatedRequestID, action.PlanCommitRef, action.CreatedAt = "fixture-"+row.TaskID, "", row.CreatedAt
		if err := tx.InsertAction(ctx, action); err != nil {
			return err
		}
	}
	return nil
}
