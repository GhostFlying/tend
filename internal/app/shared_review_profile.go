package app

import (
	"context"
	"errors"
	"strings"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// An omitted task selector uses the selected Agent's configuration. It does not
// remove another linked task's explicit formal-review requirements. Once a run
// exists, its immutable pin takes precedence over the current Agent inventory.
func validateSharedReviewTaskProfile(ctx context.Context, q store.Querier, requirements *protocol.SharedReviewExecutionRequirements, task store.TaskRow, spec domain.Spec, run string) error {
	if len(requirements.Profiles) == 0 {
		return nil
	}
	if run == "" {
		ids, err := store.FollowupIDs(ctx, q, `SELECT r.run_id FROM runs r JOIN plan_actions a USING(action_id) WHERE r.task_id=? AND r.role='implement' AND a.target_spec_version=? ORDER BY r.created_at DESC,r.rowid DESC LIMIT 1`, task.TaskID, task.SpecVersion)
		if err != nil {
			return err
		}
		if len(ids) > 0 {
			run = ids[0]
		}
	}
	if run != "" {
		pin, err := store.GetRunExecution(ctx, q, run)
		if err != nil {
			return err
		}
		return store.RequireSharedReviewExecutionProfile(requirements, pin.Profile)
	}
	contract, err := store.GetTaskOwnerContract(ctx, q, task.TaskID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if err == nil {
		profile := protocol.ExecutionProfile{Model: contract.Model, Effort: contract.Effort}
		if contract.Execution != nil {
			profile = contract.Execution.Profile
		}
		return store.RequireSharedReviewExecutionProfile(requirements, profile)
	}
	if spec.AgentSelection == nil {
		return protocol.Errorf(protocol.CodeInvalidTransition, "the named reviewer's execution profile is unavailable")
	}
	parts := strings.Split(spec.AgentSelection.Implementation.AgentID, "/")
	if len(parts) != 2 {
		return protocol.Errorf(protocol.CodeInvalidTransition, "the named reviewer's execution profile is unavailable")
	}
	inventory, err := store.GetAgentInventory(ctx, q, parts[0])
	if err != nil {
		return err
	}
	for _, agent := range inventory.Projection.Agents {
		if agent.Identity.LocalID == parts[1] {
			return store.RequireSharedReviewExecutionProfile(requirements, protocol.ExecutionProfile{Model: agent.RequestedModel, Effort: agent.RequestedEffort})
		}
	}
	return protocol.Errorf(protocol.CodeInvalidTransition, "the named reviewer's execution profile is unavailable")
}
