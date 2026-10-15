package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func toDomain(r store.TaskRow, budget int64) domain.Task {
	return domain.Task{
		ID: r.TaskID, Lifecycle: r.Lifecycle, Control: r.ControlMode, ControlRevision: r.ControlRevision,
		SpecVersion: r.SpecVersion, Priority: r.Priority, Block: r.BlockReason,
		ImplementGeneration: r.ImplementGeneration, CurrentArtifactID: r.CurrentArtifactID, Budget: budget,
		WorkspaceAssigned: r.WorkspaceAssigned,
	}
}

// fromDomain applies the task state to the stored row. Fields the rules do
// not own, such as the title and workspace, are kept from prev.
func fromDomain(t domain.Task, prev store.TaskRow, now int64) store.TaskRow {
	r := prev
	r.TaskID, r.Lifecycle, r.ControlMode, r.ControlRevision = t.ID, t.Lifecycle, t.Control, t.ControlRevision
	r.SpecVersion, r.Priority, r.BlockReason = t.SpecVersion, t.Priority, t.Block
	r.ImplementGeneration, r.CurrentArtifactID, r.UpdatedAt = t.ImplementGeneration, t.CurrentArtifactID, now
	return r
}

// loadTask reads a task and its current spec.
func loadTask(ctx context.Context, q store.Querier, taskID string) (store.TaskRow, domain.Spec, error) {
	row, err := store.GetTask(ctx, q, taskID)
	if err != nil {
		return store.TaskRow{}, domain.Spec{}, errNotFound(err, "task %s does not exist", taskID)
	}
	body, err := store.GetSpec(ctx, q, taskID, row.SpecVersion)
	if err != nil {
		return store.TaskRow{}, domain.Spec{}, err
	}
	var spec domain.Spec
	if err := json.Unmarshal([]byte(body), &spec); err != nil {
		return store.TaskRow{}, domain.Spec{}, fmt.Errorf("app: decode spec of %s: %w", taskID, err)
	}
	return row, spec, nil
}

// actionResult describes a created action in command results.
type actionResult struct {
	ActionID  string              `json:"action_id"`
	Kind      protocol.ActionKind `json:"kind"`
	EffectKey string              `json:"effect_key"`
}

// applyDecision persists a domain decision for a task: the new task state,
// superseded or cancelled actions, derived actions, open decisions and
// events. It returns the created actions.
func applyDecision(ctx context.Context, x *Exec, tx *store.Tx, prev store.TaskRow, d domain.Decision, trigger map[string]any) ([]actionResult, []Event, error) {
	now := x.Now.UnixMilli()
	row := fromDomain(d.Task, prev, now)
	if row != prev {
		if err := tx.UpdateTask(ctx, row); err != nil {
			return nil, nil, err
		}
	}
	if d.SupersedeOpenActions {
		if err := tx.SupersedeActionsBefore(ctx, d.Task.ID, d.Task.SpecVersion, now); err != nil {
			return nil, nil, err
		}
	}
	if d.CancelOpenActions {
		if err := tx.CancelOpenActions(ctx, d.Task.ID, now); err != nil {
			return nil, nil, err
		}
	}
	actions, err := insertActions(ctx, x, tx, d.Actions, trigger, "")
	if err != nil {
		return nil, nil, err
	}
	events := make([]Event, 0, len(d.Events)+len(d.OpenDecisions))
	for _, e := range d.Events {
		events = append(events, Event{ObjectType: "task", ObjectID: d.Task.ID, ObjectVersion: d.Task.ControlRevision, Kind: e.Kind, Body: e.Body})
	}
	for _, q := range d.OpenDecisions {
		options, err := json.Marshal(q.Options)
		if err != nil {
			return nil, nil, fmt.Errorf("app: encode decision options: %w", err)
		}
		id := x.IDs.New(protocol.PrefixDecision)
		if err := tx.InsertDecision(ctx, id, d.Task.ID, q.Question, string(options), now); err != nil {
			return nil, nil, err
		}
		// Opened by a rule, it surfaces like one a planner opens.
		events = append(events, Event{ObjectType: "decision", ObjectID: id, ObjectVersion: 1, Kind: "decision.opened", Body: map[string]any{"task_id": d.Task.ID}})
	}
	return actions, events, nil
}

// insertActions creates derived or requested actions. An effect that already
// has an action is reported as a duplicate effect naming that action.
func insertActions(ctx context.Context, x *Exec, tx *store.Tx, drafts []domain.ActionDraft, trigger map[string]any, planCommitRef string) ([]actionResult, error) {
	triggerJSON, err := json.Marshal(trigger)
	if err != nil {
		return nil, fmt.Errorf("app: encode trigger: %w", err)
	}
	var out []actionResult
	for _, a := range drafts {
		row := store.ActionRow{
			ActionID: x.IDs.New(protocol.PrefixAction), TaskID: a.TaskID, Kind: a.Kind, EffectKey: a.EffectKey(),
			TriggerJSON: string(triggerJSON), TargetSpecVersion: a.SpecVersion, TargetArtifactID: a.ArtifactID,
			AuthorizationJSON: "{}", Generation: a.Generation, Status: protocol.ActionPending,
			PlanCommitRef: planCommitRef, CreatedRequestID: x.Env.RequestID, CreatedAt: x.Now.UnixMilli(),
		}
		if err := tx.InsertAction(ctx, row); err != nil {
			if !store.IsUniqueViolation(err) {
				return nil, err
			}
			existing, getErr := store.GetActionByEffectKey(ctx, tx.Q(), row.EffectKey)
			if getErr != nil {
				return nil, errors.Join(err, getErr)
			}
			return nil, duplicateEffect("this action already exists", map[string]any{
				"action_id": existing.ActionID, "effect_key": row.EffectKey, "created_request_id": existing.CreatedRequestID,
			})
		}
		out = append(out, actionResult{ActionID: row.ActionID, Kind: row.Kind, EffectKey: row.EffectKey})
	}
	return out, nil
}

func decodePayload(env *protocol.Envelope, v any) error {
	if len(env.Payload) == 0 {
		return protocol.Errorf(protocol.CodeInvalidRequest, "payload is required")
	}
	dec := json.NewDecoder(bytesReader(env.Payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return protocol.Errorf(protocol.CodeInvalidRequest, "payload does not match %s", env.Operation)
	}
	return nil
}

func requireRevision(env *protocol.Envelope) (int64, error) {
	if env.ExpectedControlRevision == nil {
		return 0, protocol.Errorf(protocol.CodeInvalidRequest, "expected_control_revision is required")
	}
	return *env.ExpectedControlRevision, nil
}

// taskResult is the common result of task commands.
type taskResult struct {
	TaskID          string                 `json:"task_id"`
	Lifecycle       protocol.TaskLifecycle `json:"lifecycle"`
	ControlMode     protocol.ControlMode   `json:"control_mode"`
	ControlRevision int64                  `json:"control_revision"`
	SpecVersion     int64                  `json:"spec_version"`
	Priority        int64                  `json:"priority"`
	BlockReason     protocol.BlockReason   `json:"block_reason"`
	Actions         []actionResult         `json:"actions,omitempty"`
}

func newTaskResult(t domain.Task, actions []actionResult) taskResult {
	return taskResult{
		TaskID: t.ID, Lifecycle: t.Lifecycle, ControlMode: t.Control, ControlRevision: t.ControlRevision,
		SpecVersion: t.SpecVersion, Priority: t.Priority, BlockReason: t.Block, Actions: actions,
	}
}
