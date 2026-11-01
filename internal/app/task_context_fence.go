package app

import (
	"context"
	"fmt"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// fenceChangedTaskContext preserves an old run's result as evidence while a
// coordinator explicitly revises the agreement. It never rewrites that run.
func fenceChangedTaskContext(ctx context.Context, q store.Querier, rc runContext, kind, evidenceID string) (domain.Decision, bool, error) {
	if rc.task.Lifecycle.Terminal() || rc.action.TargetSpecVersion != rc.task.SpecVersion {
		return domain.Decision{}, false, nil
	}
	if rc.action.Kind == protocol.ActionImplement && rc.run.Generation != rc.task.ImplementGeneration {
		return domain.Decision{}, false, nil
	}
	if rc.action.Kind == protocol.ActionAccept && (rc.task.Lifecycle != protocol.TaskAwaitingAcceptance || rc.action.TargetArtifactID != rc.task.CurrentArtifactID) {
		return domain.Decision{}, false, nil
	}
	scope, err := store.ResolveTaskIssueScope(ctx, q, rc.task.TaskID)
	if err != nil {
		return domain.Decision{}, false, err
	}
	if scope.Canonical {
		current, err := store.TaskOwnerContractCurrent(ctx, q, rc.task.TaskID)
		if err != nil {
			return domain.Decision{}, false, err
		}
		runCurrent, err := store.RunIssueResponsibilityCurrent(ctx, q, rc.run.RunID)
		if err != nil {
			return domain.Decision{}, false, err
		}
		if !current || !runCurrent {
			task := toDomain(rc.task, rc.spec.Budget())
			d := domain.Decision{Task: task}
			if task.Control == protocol.ControlManaged {
				d, err = domain.SetControlMode(task, protocol.ControlPaused, task.ControlRevision)
				if err != nil {
					return d, false, err
				}
			}
			d.Events = append(d.Events, domain.EventDraft{Kind: kind, Body: map[string]any{"stale": true, "owner_changed": true, "run_id": rc.run.RunID, "evidence_id": evidenceID}}, domain.EventDraft{Kind: "task.context_changed", Body: map[string]any{"reason": "Issue owner changed; reconcile the retained result"}})
			return d, true, nil
		}
		// The owner's frozen spec authorizes this run. Source changes gate a
		// subsequent claim, not a result produced under the existing agreement.
		return domain.Decision{}, false, nil
	}
	for _, binding := range rc.spec.SharedContext {
		if binding.ContextDigest == "" {
			continue
		}
		digest, _, err := store.EffectiveContext(ctx, q, binding.WorkItemID)
		if err != nil {
			return domain.Decision{}, false, err
		}
		reviewed, err := store.WorkItemContextReady(ctx, q, binding.WorkItemID)
		if err != nil {
			return domain.Decision{}, false, err
		}
		if digest == binding.ContextDigest && reviewed {
			continue
		}
		task := toDomain(rc.task, rc.spec.Budget())
		d := domain.Decision{Task: task}
		if task.Control == protocol.ControlManaged {
			d, err = domain.SetControlMode(task, protocol.ControlPaused, task.ControlRevision)
			if err != nil {
				return d, false, err
			}
		}
		d.Events = append(d.Events, domain.EventDraft{Kind: kind, Body: map[string]any{"stale": true, "context_changed": true, "run_id": rc.run.RunID, "evidence_id": evidenceID, "spec_version": rc.action.TargetSpecVersion}}, domain.EventDraft{Kind: "task.context_changed", Body: map[string]any{"work_item_id": binding.WorkItemID, "spec_version": rc.action.TargetSpecVersion, "reason": "effective source context changed or required review/native reconciliation is pending; reconcile before advancing the frozen task agreement"}})
		return d, true, nil
	}
	return domain.Decision{}, false, nil
}

// recordTaskContextWake sends an evidence-only reconciliation notice to the
// existing coordinator. The task remains paused until an explicit task write.
func recordTaskContextWake(ctx context.Context, x *Exec, tx *store.Tx, e Event, eventID string) error {
	id := x.IDs.New(protocol.PrefixMessage)
	body := fmt.Sprintf("Task %s received a result whose source context changed or requires native reconciliation. The result is retained as historical evidence; it does not complete the current responsibility. Reconcile current source facts and explicitly revise the task agreement or keep it paused. Do not replay the old run. Source event: %s.", e.ObjectID, eventID)
	seq, err := tx.InsertMessage(ctx, store.MessageRow{MessageID: id, Author: "system", Body: body, CreatedAt: x.Now.UnixMilli()})
	if err != nil {
		return err
	}
	if err := tx.BindMessage(ctx, id, "task", e.ObjectID, "context_change"); err != nil {
		return err
	}
	if err := tx.BindMessage(ctx, id, "event", eventID, "context_change"); err != nil {
		return err
	}
	if _, err := tx.InsertEvent(ctx, store.EventRow{EventID: x.IDs.New(protocol.PrefixEvent), Source: "app", ObjectType: "message", ObjectID: id, ObjectVersion: seq, Kind: "conversation.message", BodyJSON: `{"author":"system"}`, RequestID: x.Env.RequestID, TraceID: x.TraceID, ActorJSON: `{"kind":"system"}`, CreatedAt: x.Now.UnixMilli()}); err != nil {
		return err
	}
	return queueEventTurn(ctx, x, tx, id, eventID, e.Kind, nil)
}
