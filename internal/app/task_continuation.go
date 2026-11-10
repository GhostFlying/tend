package app

import (
	"context"
	"errors"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// ContinueTask appends another execution for an unchanged completed goal.
type ContinueTask struct{}

func (ContinueTask) Operation() string { return "tasks.continue" }
func (ContinueTask) Authorize(ctx context.Context, x *Exec, q store.Querier) error {
	if err := allowKinds(x.Principal, planners...); err != nil {
		return err
	}
	return canonicalTaskPlannerAccess(ctx, q, x)
}
func (ContinueTask) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	if x.Env.Target.Type != "task" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "continuation requires a Task target")
	}
	var p protocol.ContinueTaskPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	expected, err := requireRevision(x.Env)
	if err != nil {
		return Outcome{}, err
	}
	row, spec, err := loadTask(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, err
	}
	if row.Kind == protocol.TaskKindIssueManager {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "Issue owner execution continues through its existing responsibility")
	}
	primary, err := store.GetTaskIssueOwner(ctx, tx.Q(), row.TaskID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return Outcome{}, err
	}
	scope, err := store.ResolveTaskIssueScope(ctx, tx.Q(), row.TaskID)
	if err != nil {
		return Outcome{}, err
	}
	if primary != "" && scope.Canonical {
		issue, err := store.GetCanonicalIssue(ctx, tx.Q(), primary)
		if err != nil {
			return Outcome{}, err
		}
		if p.ExpectedAssignmentEpoch != 0 {
			if err := domain.CheckIssueEpoch(issue.IssueControl, p.ExpectedAssignmentEpoch); err != nil {
				return Outcome{}, err
			}
		}
		if issue.State != protocol.IssueActive && issue.State != protocol.IssuePaused {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "reopen the Issue before continuing its existing task")
		}
		current, err := store.TaskOwnerContractCurrent(ctx, tx.Q(), row.TaskID)
		if err != nil {
			return Outcome{}, err
		}
		if !current {
			return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "the Task's current Issue responsibility must be retained before continuation")
		}
	}
	var busy bool
	if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs r LEFT JOIN leases l USING(run_id) WHERE r.task_id=? AND (r.observed_state IN ('starting','running','waiting','interrupted','unknown') OR l.status<>'released')) OR EXISTS(SELECT 1 FROM workspaces WHERE occupied_by=?)`, row.TaskID, row.TaskID).Scan(&busy); err != nil {
		return Outcome{}, err
	}
	if busy {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "reconcile the previous run, physical stop and resource receipt before continuing the Task")
	}
	d, err := domain.ContinueTask(toDomain(row, spec.Budget()), expected, p.Reason)
	if err != nil {
		return Outcome{}, err
	}
	if err := archiveTaskDeliveryForContinuation(ctx, x, tx, row); err != nil {
		return Outcome{}, err
	}
	actions, events, err := applyDecision(ctx, x, tx, row, d, map[string]any{"on": "continued", "reason": p.Reason, "previous_artifact_id": row.CurrentArtifactID})
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: newTaskResult(d.Task, actions), Events: events}, nil
}
