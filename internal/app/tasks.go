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

// claimPlanCommit records a planning effect separately from owner attribution.
// Hosted native turns keep their initial delivery as the effect identity even
// after steer changes the payload's source. New call IDs cannot repeat a commit.
func claimPlanCommit(ctx context.Context, x *Exec, tx *store.Tx, sourceMessageID string, seq int64) error {
	if sourceMessageID == "" {
		return nil
	}
	if err := attachLegacyReviewSource(ctx, x, tx, sourceMessageID); err != nil {
		return err
	}
	effectSource := sourceMessageID
	if x.planningSource != "" {
		if x.Principal.Kind != protocol.PrincipalMainAgent {
			return protocol.Errorf(protocol.CodeInvalidRequest, "planning source requires a hosted main agent")
		}
		effectSource = x.planningSource
	}
	err := tx.InsertPlanCommit(ctx, effectSource, seq, x.Env.RequestID, x.Now.UnixMilli())
	if err == nil || !store.IsUniqueViolation(err) {
		return err
	}
	original, getErr := store.GetPlanCommitRequest(ctx, tx.Q(), effectSource, seq)
	if getErr != nil {
		return fmt.Errorf("app: plan commit replay: %w", getErr)
	}
	return duplicateEffect("this planning commit was already applied", map[string]any{
		"source_message_id": sourceMessageID, "planning_source_message_id": effectSource, "commit_seq": seq, "original_request_id": original,
	})
}

// CreateTask handles tasks.create.
type CreateTask struct{}

type createTaskPayload struct {
	WorkItemID          string                  `json:"work_item_id,omitempty"`
	WorkItemRevision    int64                   `json:"work_item_revision,omitempty"`
	StageIDs            []string                `json:"stage_ids,omitempty"`
	UseDefaultWorkspace bool                    `json:"use_default_workspace,omitempty"`
	Relations           []protocol.TaskRelation `json:"relations,omitempty"`
	Title               string                  `json:"title"`
	Spec                domain.Spec             `json:"spec"`
	// Rating is empty for an unrated task.
	Rating protocol.Rating `json:"rating,omitempty"`
	// Project defaults to the calling session's project, or else to the
	// only active project.
	Project string `json:"project,omitempty"`
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
	return createTask(ctx, x, tx, p, nil)
}

// createTask shares persistence without expanding the legacy entry's authority.
func createTask(ctx context.Context, x *Exec, tx *store.Tx, p createTaskPayload, canonical *canonicalTaskCreateContext) (Outcome, error) {
	if p.Title == "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "title is required")
	}
	if canonical == nil {
		if err := claimPlanCommit(ctx, x, tx, p.SourceMessageID, p.CommitSeq); err != nil {
			return Outcome{}, err
		}
	}
	taskID := x.IDs.New(protocol.PrefixTask)
	refs, err := validateRelations(ctx, tx.Q(), taskID, p.Relations)
	if err != nil {
		return Outcome{}, err
	}
	project, err := resolveProject(ctx, tx.Q(), p.Project, x.Principal)
	if err != nil {
		return Outcome{}, err
	}
	var item protocol.WorkItem
	var itemEvents []Event
	if canonical == nil {
		item, itemEvents, err = taskIssue(ctx, x, tx, &p, project.Project)
	} else {
		item = protocol.WorkItem{WorkItemID: canonical.Issue.IssueID, Revision: canonical.Issue.Revision, IssuePlan: canonical.Issue.Plan}
		p.Spec.SharedContext, err = freezeCanonicalTaskContext(ctx, tx.Q(), canonical.Issue)
	}
	if err != nil {
		return Outcome{}, err
	}
	if x.Principal.Kind == protocol.PrincipalMainAgent && x.Env.ParentRequestID != "" {
		digest, facts, err := store.EffectiveContext(ctx, tx.Q(), item.WorkItemID)
		if err != nil {
			return Outcome{}, err
		}
		p.Spec.SharedContext[0].ContextDigest = digest
		p.Spec.SharedContext[0].Facts = append(p.Spec.SharedContext[0].Facts, facts...)
	}
	ws, resource, err := prepareWorkspace(ctx, tx.Q(), p.Spec.Workspace, project.Template, taskID, item.WorkItemID)
	if err != nil {
		return Outcome{}, err
	}
	p.Spec.Workspace = ws
	if err := checkRequiredHost(ctx, tx.Q(), p.Spec, resource); err != nil {
		return Outcome{}, err
	}
	p.Spec = p.Spec.Normalize()
	if err := checkObservationScope(ctx, tx.Q(), p.Spec.Observation); err != nil {
		return Outcome{}, err
	}
	if err := freezeTaskInputs(ctx, tx.Q(), &p.Spec, nil); err != nil {
		return Outcome{}, err
	}
	if err := checkWorkspaceProtocol(ctx, tx.Q(), p.Spec); err != nil {
		return Outcome{}, err
	}
	d, err := domain.NewTask(taskID, project.Project, p.Spec, p.Rating)
	if err != nil {
		return Outcome{}, err
	}
	if err := checkRepos(ctx, tx.Q(), p.Spec.Workspace.Repos()); err != nil {
		return Outcome{}, err
	}
	if err := checkSpecMounts(ctx, tx.Q(), p.Spec); err != nil {
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

	if resource != nil {
		if err := tx.BindWorkspace(ctx, taskID, resource.WorkspaceID, domain.WorkspaceReuse, p.Spec.Workspace.SourceTaskID, now); err != nil {
			return Outcome{}, err
		}
	}
	if err := tx.ReplaceTaskRelations(ctx, taskID, refs, now); err != nil {
		return Outcome{}, err
	}
	spec, err := json.Marshal(p.Spec)
	if err != nil {
		return Outcome{}, fmt.Errorf("app: encode spec: %w", err)
	}
	if err := tx.InsertSpec(ctx, taskID, d.Task.SpecVersion, string(spec), p.SourceMessageID, now); err != nil {
		return Outcome{}, err
	}
	if err := tx.InsertTaskIssueOwner(ctx, taskID, item.WorkItemID); err != nil {
		return Outcome{}, err
	}
	var linkedEvents []Event
	if canonical == nil {
		_, linkedEvents, err = BindIssueTask(ctx, x, tx, item.WorkItemID, item.Revision, taskID, p.StageIDs)
	} else {
		linkedEvents, err = bindCanonicalPrimaryTask(ctx, x, tx, *canonical, taskID, p.StageIDs)
	}
	if err != nil {
		return Outcome{}, err
	}
	itemEvents = append(itemEvents, linkedEvents...)
	planRef := ""
	if p.SourceMessageID != "" {
		planRef = fmt.Sprintf("%s#%d", p.SourceMessageID, p.CommitSeq)
	}
	actions, err := insertActions(ctx, x, tx, d.Actions, map[string]any{"on": "task.created"}, planRef)
	if err != nil {
		return Outcome{}, err
	}
	if err := bindSource(ctx, tx, p.SourceMessageID, "task", taskID); err != nil {
		return Outcome{}, err
	}
	events := itemEvents
	for _, e := range d.Events {
		events = append(events, Event{ObjectType: "task", ObjectID: taskID, ObjectVersion: d.Task.ControlRevision, Kind: e.Kind, Body: e.Body})
	}
	if len(refs) > 0 {
		events = append(events, Event{ObjectType: "task", ObjectID: taskID, ObjectVersion: d.Task.ControlRevision, Kind: "task.relations_updated", Body: map[string]any{"relations": refs}})
		backlinks, err := backlinkEvents(ctx, tx.Q(), taskID, nil, refs)
		if err != nil {
			return Outcome{}, err
		}
		events = append(events, backlinks...)
	}
	return Outcome{Result: newTaskResult(d.Task, actions), Events: events}, nil
}

// UpdateIntent handles tasks.update_intent: a new spec, rating or
// project, or several of them, under one expected control revision.
type UpdateIntent struct{}

type updateIntentPayload struct {
	Relations       *[]protocol.TaskRelation `json:"relations,omitempty"`
	Spec            *domain.Spec             `json:"spec,omitempty"`
	Rating          *protocol.Rating         `json:"rating,omitempty"`
	Project         *string                  `json:"project,omitempty"`
	SourceMessageID string                   `json:"source_message_id,omitempty"`
	CommitSeq       int64                    `json:"commit_seq,omitempty"`
}

// Operation implements Command.
func (UpdateIntent) Operation() string { return "tasks.update_intent" }

// Authorize implements Command.
func (UpdateIntent) Authorize(ctx context.Context, x *Exec, q store.Querier) error {
	if err := allowKinds(x.Principal, planners...); err != nil {
		return err
	}
	return canonicalTaskPlannerAccess(ctx, q, x)
}

// Apply implements Command.
func (UpdateIntent) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p updateIntentPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	if p.Spec == nil && p.Rating == nil && p.Project == nil && p.Relations == nil {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "spec, rating, project or relations is required")
	}
	expected, err := requireRevision(x.Env)
	if err != nil {
		return Outcome{}, err
	}
	if err := claimTaskPlanningSource(ctx, x, tx, p.SourceMessageID, p.CommitSeq); err != nil {
		return Outcome{}, err
	}
	row, current, err := loadTask(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, err
	}
	if row.Kind == protocol.TaskKindIssueManager {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "manager responsibility changes require an issue manager handoff")
	}
	task := toDomain(row, current.Budget())
	if expected != row.ControlRevision {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "control revision is %d, request expected %d", row.ControlRevision, expected)
	}
	merged := domain.Decision{Task: task}
	var pendingResource *store.WorkspaceRow
	var detachedResource *store.WorkspaceRow
	if p.Spec != nil {
		detachedResource, err = unusedWorkspaceRebind(ctx, tx.Q(), row, current.Workspace, p.Spec.Workspace)
		if err != nil {
			return Outcome{}, err
		}
		if detachedResource != nil {
			// The app proved there is no execution agreement to move. Domain
			// validation now sees the same resource freedom as a new task.
			task.WorkspaceAssigned = false
		}
		if err := refreshTaskWorkContext(ctx, tx.Q(), row.TaskID, p.Spec, current.SharedContext); err != nil {
			return Outcome{}, err
		}
		owner, err := store.GetTaskIssueOwner(ctx, tx.Q(), row.TaskID)
		if err != nil {
			return Outcome{}, err
		}
		ws, resource, err := prepareWorkspace(ctx, tx.Q(), p.Spec.Workspace, "", row.TaskID, owner)
		pendingResource = resource
		if err != nil {
			return Outcome{}, err
		}
		p.Spec.Workspace = ws
		if resource == nil && detachedResource == nil {
			existing, lookupErr := store.GetWorkspaceByTask(ctx, tx.Q(), row.TaskID)
			if lookupErr == nil {
				resource = &existing
			} else if !errors.Is(lookupErr, store.ErrNotFound) {
				return Outcome{}, lookupErr
			}
		}
		if err := checkRequiredHost(ctx, tx.Q(), *p.Spec, resource); err != nil {
			return Outcome{}, err
		}
		*p.Spec = p.Spec.Normalize()
		if err := checkObservationScope(ctx, tx.Q(), p.Spec.Observation); err != nil {
			return Outcome{}, err
		}
		if err := freezeTaskInputs(ctx, tx.Q(), p.Spec, current.Inputs); err != nil {
			return Outcome{}, err
		}
		if err := checkWorkspaceProtocol(ctx, tx.Q(), *p.Spec); err != nil {
			return Outcome{}, err
		}
		if err := checkRepos(ctx, tx.Q(), addedRepos(current.Workspace, p.Spec.Workspace)); err != nil {
			return Outcome{}, err
		}
		// A task that already has several mounts keeps them and waits for
		// a host that supports them, but takes no new ones.
		if addsMounts(current.Workspace, p.Spec.Workspace) {
			if err := checkSpecMounts(ctx, tx.Q(), *p.Spec); err != nil {
				return Outcome{}, err
			}
		}
		d, err := domain.UpdateSpec(task, *p.Spec, current, expected)
		if err != nil {
			return Outcome{}, err
		}
		merged = d
		// The rating change below belongs to the same intent, so it is
		// checked against the revision this request started from.
		expected = d.Task.ControlRevision
	}
	if p.Rating != nil {
		d, err := domain.SetRating(merged.Task, *p.Rating, expected)
		if err != nil {
			return Outcome{}, err
		}
		merged.Task = d.Task
		merged.Events = append(merged.Events, d.Events...)
		expected = d.Task.ControlRevision
	}
	if p.Project != nil {
		// Staying in a project that has since been retired is no move,
		// but the revision is still checked.
		if *p.Project == "" {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "name the project to move the task to")
		}
		if *p.Project != row.Project {
			scope, err := store.ResolveTaskIssueScope(ctx, tx.Q(), row.TaskID)
			if err != nil {
				return Outcome{}, err
			}
			if scope.Canonical {
				return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "canonical task must retain its primary issue project")
			}
			if _, err := resolveProject(ctx, tx.Q(), *p.Project, x.Principal); err != nil {
				return Outcome{}, err
			}
		}
		d, err := domain.SetProject(merged.Task, *p.Project, expected)
		if err != nil {
			return Outcome{}, err
		}
		merged.Task = d.Task
		merged.Events = append(merged.Events, d.Events...)
	}
	var backlinks []Event
	if p.Relations != nil {
		refs, err := validateRelations(ctx, tx.Q(), row.TaskID, *p.Relations)
		if err != nil {
			return Outcome{}, err
		}
		currentRefs, err := declaredRelations(ctx, tx.Q(), row.TaskID)
		if err != nil {
			return Outcome{}, err
		}
		d, err := domain.UpdateRelations(merged.Task, refs, currentRefs, merged.Task.ControlRevision)
		if err != nil {
			return Outcome{}, err
		}
		merged.Task = d.Task
		merged.Events = append(merged.Events, d.Events...)
		if len(d.Events) > 0 {
			backlinks, err = backlinkEvents(ctx, tx.Q(), row.TaskID, currentRefs, refs)
			if err != nil {
				return Outcome{}, err
			}
			if err := tx.ReplaceTaskRelations(ctx, row.TaskID, refs, x.Now.UnixMilli()); err != nil {
				return Outcome{}, err
			}
		}
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
	if detachedResource != nil {
		if err := tx.DetachUnusedWorkspace(ctx, row.TaskID, detachedResource.WorkspaceID); err != nil {
			return Outcome{}, err
		}
	}
	if pendingResource != nil && !row.WorkspaceAssigned {
		if err := tx.BindWorkspace(ctx, row.TaskID, pendingResource.WorkspaceID, domain.WorkspaceReuse, p.Spec.Workspace.SourceTaskID, x.Now.UnixMilli()); err != nil {
			return Outcome{}, err
		}
	}
	actions, events, err := applyDecision(ctx, x, tx, row, merged, map[string]any{"on": "task.spec_updated"})
	if err != nil {
		return Outcome{}, err
	}
	if err := bindSource(ctx, tx, p.SourceMessageID, "task", row.TaskID); err != nil {
		return Outcome{}, err
	}
	if detachedResource != nil {
		events = append(events, Event{ObjectType: "task", ObjectID: row.TaskID, ObjectVersion: merged.Task.ControlRevision, Kind: "task.workspace_detached",
			Body: map[string]any{"workspace_id": detachedResource.WorkspaceID, "source_task_id": detachedResource.SourceTaskID, "previous_spec_version": row.SpecVersion, "spec_version": merged.Task.SpecVersion}})
	}
	events = append(events, backlinks...)
	if err := validateCanonicalTaskDependencies(ctx, tx.Q()); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: newTaskResult(merged.Task, actions), Events: events}, nil
}

// RateTask handles tasks.rate, the owner's way to rate a task without
// planning it (D60). Agents rate through tasks.update_intent.
type RateTask struct{}

// Operation implements Command.
func (RateTask) Operation() string { return "tasks.rate" }

// Authorize implements Command.
func (RateTask) Authorize(ctx context.Context, x *Exec, q store.Querier) error {
	if err := allowKinds(x.Principal, planners...); err != nil {
		return err
	}
	return canonicalTaskPlannerAccess(ctx, q, x)
}

// Apply implements Command.
func (RateTask) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.RateTaskPayload
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
	d, err := domain.SetRating(toDomain(row, spec.Budget()), p.Rating, expected)
	if err != nil {
		return Outcome{}, err
	}
	actions, events, err := applyDecision(ctx, x, tx, row, d, nil)
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: newTaskResult(d.Task, actions), Events: events}, nil
}

// SetControlMode handles control.set_mode.
type SetControlMode struct{}

// Operation implements Command.
func (SetControlMode) Operation() string { return "control.set_mode" }

// Authorize implements Command. Human control is a human decision: only the
// owner (or tend acting for the owner) may take or hand back control.
func (SetControlMode) Authorize(ctx context.Context, x *Exec, q store.Querier) error {
	if err := canonicalTaskPlannerAccess(ctx, q, x); err != nil {
		return err
	}
	if err := allowKinds(x.Principal, planners...); err != nil {
		return err
	}
	if x.Principal.Kind == protocol.PrincipalMainAgent {
		var p protocol.SetControlModePayload
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
	var p protocol.SetControlModePayload
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
func (CancelTask) Authorize(ctx context.Context, x *Exec, q store.Querier) error {
	if err := allowKinds(x.Principal, planners...); err != nil {
		return err
	}
	return canonicalTaskPlannerAccess(ctx, q, x)
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

// Operation implements Command.
func (CreateAction) Operation() string { return "actions.create" }

// Authorize implements Command.
func (CreateAction) Authorize(ctx context.Context, x *Exec, q store.Querier) error {
	if err := allowKinds(x.Principal, planners...); err != nil {
		return err
	}
	return canonicalTaskPlannerAccess(ctx, q, x)
}

// Apply implements Command.
func (CreateAction) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	var p protocol.CreateActionPayload
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
	if row.Kind == protocol.TaskKindIssueManager {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "manager tasks cannot create delivery or acceptance actions")
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
	if err := claimTaskPlanningSource(ctx, x, tx, p.SourceMessageID, p.CommitSeq); err != nil {
		return Outcome{}, err
	}
	actions, events, err := applyDecision(ctx, x, tx, row, d, map[string]any{"on": "requested"})
	if err != nil {
		return Outcome{}, err
	}
	if err := bindSource(ctx, tx, p.SourceMessageID, "task", row.TaskID); err != nil {
		return Outcome{}, err
	}
	return Outcome{Result: newTaskResult(d.Task, actions), Events: events}, nil
}
