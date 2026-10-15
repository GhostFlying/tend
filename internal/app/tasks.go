package app

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// claimPlanCommit records that a planning commit of a source message was
// applied. A replay under a new request ID is a duplicate effect.
func claimPlanCommit(ctx context.Context, x *Exec, tx *store.Tx, sourceMessageID string, seq int64) error {
	if sourceMessageID == "" {
		return nil
	}
	err := tx.InsertPlanCommit(ctx, sourceMessageID, seq, x.Env.RequestID, x.Now.UnixMilli())
	if err == nil || !store.IsUniqueViolation(err) {
		return err
	}
	original, getErr := store.GetPlanCommitRequest(ctx, tx.Q(), sourceMessageID, seq)
	if getErr != nil {
		return fmt.Errorf("app: plan commit replay: %w", getErr)
	}
	return duplicateEffect("this planning commit was already applied", map[string]any{
		"source_message_id": sourceMessageID, "commit_seq": seq, "original_request_id": original,
	})
}

// CreateTask handles tasks.create.
type CreateTask struct{}

type createTaskPayload struct {
	Title    string      `json:"title"`
	Spec     domain.Spec `json:"spec"`
	Priority int64       `json:"priority"`
	// SourceMessageID and CommitSeq identify the planning commit that
	// produced this task, when it came from a conversation message.
	SourceMessageID string `json:"source_message_id,omitempty"`
	CommitSeq       int64  `json:"commit_seq,omitempty"`
	// WorkspaceName names the task's workspace directory and branches,
	// such as "fix-windows-build". It defaults to one derived from the
	// task ID.
	WorkspaceName string `json:"workspace_name,omitempty"`
}

// Operation implements Command.
func (CreateTask) Operation() string { return "tasks.create" }

// Authorize implements Command.
func (CreateTask) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, planners...)
}

// Apply implements Command.
func (CreateTask) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p createTaskPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if p.Title == "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "title is required")
	}
	if err := claimPlanCommit(ctx, x, tx, p.SourceMessageID, p.CommitSeq); err != nil {
		return Outcome{}, err
	}
	taskID := x.IDs.New(protocol.PrefixTask)
	ws, err := expandTemplate(ctx, tx.Q(), p.Spec.Workspace)
	if err != nil {
		return Outcome{}, err
	}
	p.Spec.Workspace = ws
	p.Spec = p.Spec.Normalize()
	d, err := domain.NewTask(taskID, p.Spec, p.Priority)
	if err != nil {
		return Outcome{}, err
	}
	if err := checkRepos(ctx, tx.Q(), p.Spec.Workspace.Repos()); err != nil {
		return Outcome{}, err
	}
	name, err := reserveWorkspaceName(ctx, tx.Q(), p.WorkspaceName, taskID)
	if err != nil {
		return Outcome{}, err
	}
	now := x.Now.UnixMilli()
	row := fromDomain(d.Task, store.TaskRow{Title: p.Title, WorkspaceName: name, CreatedAt: now}, now)
	if err := tx.InsertTask(ctx, row); err != nil {
		return Outcome{}, err
	}
	spec, err := json.Marshal(p.Spec)
	if err != nil {
		return Outcome{}, fmt.Errorf("app: encode spec: %w", err)
	}
	if err := tx.InsertSpec(ctx, taskID, d.Task.SpecVersion, string(spec), p.SourceMessageID, now); err != nil {
		return Outcome{}, err
	}
	planRef := ""
	if p.SourceMessageID != "" {
		planRef = fmt.Sprintf("%s#%d", p.SourceMessageID, p.CommitSeq)
	}
	actions, err := insertActions(ctx, x, tx, d.Actions, map[string]any{"on": "task.created"}, planRef)
	if err != nil {
		return Outcome{}, err
	}
	events := []Event{}
	for _, e := range d.Events {
		events = append(events, Event{ObjectType: "task", ObjectID: taskID, ObjectVersion: d.Task.ControlRevision, Kind: e.Kind, Body: e.Body})
	}
	return Outcome{Result: newTaskResult(d.Task, actions), Events: events}, nil
}

// UpdateIntent handles tasks.update_intent: a new spec, a new priority, or
// both, under one expected control revision.
type UpdateIntent struct{}

type updateIntentPayload struct {
	Spec            *domain.Spec `json:"spec,omitempty"`
	Priority        *int64       `json:"priority,omitempty"`
	SourceMessageID string       `json:"source_message_id,omitempty"`
	CommitSeq       int64        `json:"commit_seq,omitempty"`
}

// Operation implements Command.
func (UpdateIntent) Operation() string { return "tasks.update_intent" }

// Authorize implements Command.
func (UpdateIntent) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, planners...)
}

// Apply implements Command.
func (UpdateIntent) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p updateIntentPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if p.Spec == nil && p.Priority == nil {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "spec or priority is required")
	}
	expected, err := requireRevision(x.Env)
	if err != nil {
		return Outcome{}, err
	}
	if err := claimPlanCommit(ctx, x, tx, p.SourceMessageID, p.CommitSeq); err != nil {
		return Outcome{}, err
	}
	row, current, err := loadTask(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, err
	}
	task := toDomain(row, current.Budget())
	merged := domain.Decision{Task: task}
	if p.Spec != nil {
		*p.Spec = p.Spec.Normalize()
		if err := checkRepos(ctx, tx.Q(), addedRepos(current.Workspace, p.Spec.Workspace)); err != nil {
			return Outcome{}, err
		}
		d, err := domain.UpdateSpec(task, *p.Spec, current, expected)
		if err != nil {
			return Outcome{}, err
		}
		merged = d
		// The priority change below belongs to the same intent, so it is
		// checked against the revision this request started from.
		expected = d.Task.ControlRevision
	}
	if p.Priority != nil {
		d, err := domain.SetPriority(merged.Task, *p.Priority, expected)
		if err != nil {
			return Outcome{}, err
		}
		merged.Task = d.Task
		merged.Events = append(merged.Events, d.Events...)
	}
	if p.Spec != nil && merged.Task.SpecVersion != row.SpecVersion {
		spec, err := json.Marshal(p.Spec)
		if err != nil {
			return Outcome{}, fmt.Errorf("app: encode spec: %w", err)
		}
		if err := tx.InsertSpec(ctx, row.TaskID, merged.Task.SpecVersion, string(spec), p.SourceMessageID, x.Now.UnixMilli()); err != nil {
			return Outcome{}, err
		}
	}
	actions, events, err := applyDecision(ctx, x, tx, row, merged, map[string]any{"on": "task.spec_updated"})
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: newTaskResult(merged.Task, actions), Events: events}, nil
}

// SetControlMode handles control.set_mode.
type SetControlMode struct{}

type setControlModePayload struct {
	Mode protocol.ControlMode `json:"mode"`
}

// Operation implements Command.
func (SetControlMode) Operation() string { return "control.set_mode" }

// Authorize implements Command. Human control is a human decision: only the
// owner (or tend acting for the owner) may take or hand back control.
func (SetControlMode) Authorize(ctx context.Context, x *Exec, q store.Querier) error {
	if err := allowKinds(x.Principal, planners...); err != nil {
		return err
	}
	if x.Principal.Kind == protocol.PrincipalMainAgent {
		var p setControlModePayload
		if err := decodePayload(x.Env, &p); err != nil {
			return err
		}
		if p.Mode == protocol.ControlHumanControl {
			return protocol.Errorf(protocol.CodeForbidden, "only the owner may take human control")
		}
		row, err := store.GetTask(ctx, q, x.Env.Target.ID)
		if err == nil && row.ControlMode == protocol.ControlHumanControl {
			return protocol.Errorf(protocol.CodeForbidden, "only the owner may hand back human control")
		}
	}
	return nil
}

// Apply implements Command.
func (SetControlMode) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p setControlModePayload
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
	d, err := domain.SetControlMode(toDomain(row, spec.Budget()), p.Mode, expected)
	if err != nil {
		return Outcome{}, err
	}
	actions, events, err := applyDecision(ctx, x, tx, row, d, nil)
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: newTaskResult(d.Task, actions), Events: events}, nil
}

// CancelTask handles tasks.cancel.
type CancelTask struct{}

// Operation implements Command.
func (CancelTask) Operation() string { return "tasks.cancel" }

// Authorize implements Command.
func (CancelTask) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, planners...)
}

// Apply implements Command.
func (CancelTask) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	expected, err := requireRevision(x.Env)
	if err != nil {
		return Outcome{}, err
	}
	row, spec, err := loadTask(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, err
	}
	d, err := domain.Cancel(toDomain(row, spec.Budget()), expected)
	if err != nil {
		return Outcome{}, err
	}
	_, events, err := applyDecision(ctx, x, tx, row, d, nil)
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: newTaskResult(d.Task, nil), Events: events}, nil
}

// CreateAction handles actions.create. A planner creates another implement
// generation, or another acceptance of the artifact under acceptance after
// an attempt could not reach a verdict. Every other action is derived by
// the rules.
type CreateAction struct{}

type createActionPayload struct {
	Kind            protocol.ActionKind `json:"kind"`
	Generation      int64               `json:"generation"`
	SourceMessageID string              `json:"source_message_id,omitempty"`
	CommitSeq       int64               `json:"commit_seq,omitempty"`
}

// Operation implements Command.
func (CreateAction) Operation() string { return "actions.create" }

// Authorize implements Command.
func (CreateAction) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, planners...)
}

// Apply implements Command.
func (CreateAction) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p createActionPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if p.Kind != protocol.ActionImplement && p.Kind != protocol.ActionAccept {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "planners may only request implement or accept actions")
	}
	expected, err := requireRevision(x.Env)
	if err != nil {
		return Outcome{}, err
	}
	row, spec, err := loadTask(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, err
	}
	task := toDomain(row, spec.Budget())
	var d domain.Decision
	if p.Kind == protocol.ActionImplement {
		d, err = domain.RequestImplement(task, p.Generation, expected)
	} else {
		var latest int64
		if latest, err = store.LatestAcceptGeneration(ctx, tx.Q(), row.TaskID, row.SpecVersion, row.CurrentArtifactID); err != nil {
			return Outcome{}, err
		}
		d, err = domain.RequestAcceptance(task, latest, p.Generation, expected)
	}
	if err != nil {
		return Outcome{}, err
	}
	if err := claimPlanCommit(ctx, x, tx, p.SourceMessageID, p.CommitSeq); err != nil {
		return Outcome{}, err
	}
	actions, events, err := applyDecision(ctx, x, tx, row, d, map[string]any{"on": "requested"})
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: newTaskResult(d.Task, actions), Events: events}, nil
}
