package app

import (
	"context"
	"fmt"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// fenceChangedTaskContext preserves results after an actual responsibility
// change. Ordinary context changes do not fence a result or rewrite its Run.
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
		// The owner's frozen spec authorizes this run. Ordinary source changes
		// neither gate subsequent claims nor invalidate its actual result.
		return domain.Decision{}, false, nil
	}

	return domain.Decision{}, false, nil
}

// recordTaskContextWake sends an evidence-only reconciliation notice to the
// existing coordinator. The task remains paused until an explicit task write.
func recordTaskContextWake(ctx context.Context, x *Exec, tx *store.Tx, e Event, eventID string) error {
	id := x.IDs.New(protocol.PrefixMessage)
	body := fmt.Sprintf("Task %s received a result after its execution responsibility changed or while native reconciliation remained unresolved. The result is retained as historical evidence. Reconcile the actual current responsibility and unresolved operation before deciding whether to continue or revise the Task. Ordinary source updates require no new applicability permission. Do not replay an uncertain write. Source event: %s.", e.ObjectID, eventID)
	if e.Kind == "task.preparation_blocked" {
		body = fmt.Sprintf("Task %s stopped during workspace preparation before its worker started. Read tasks_read runs[].preparation for the exact frozen run/spec, inherited file statuses and missing contract field. Reconcile whether inherited changes belong to the existing authorized goal, then explicitly update this task contract with issues_update_task. Reuse retains files; it does not include them in commit scope. Do not blindly retry the unchanged spec or create another task. Unknown authorship or additional scope requires investigation or clarification. This is a technical blocker for the current Issue owner, not a new human retry decision. Source event: %s.", e.ObjectID, eventID)
	}
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
