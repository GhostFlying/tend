package app

import (
	"context"
	"slices"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// CanonicalOwnerTaskCreate is internal; no owner HTTP/tool registration is added.
// The owner's explicit frozen task spec authorizes execution through a worker.
type CanonicalOwnerTaskCreate struct{}

func (CanonicalOwnerTaskCreate) Operation() string { return "issues.create_task" }
func (CanonicalOwnerTaskCreate) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}

type canonicalOwnerTaskPayload struct {
	canonicalOwnerIssueSource
	ExpectedAssignmentEpoch int64                   `json:"expected_assignment_epoch"`
	Title                   string                  `json:"title"`
	Spec                    domain.Spec             `json:"spec"`
	Rating                  protocol.Rating         `json:"rating,omitempty"`
	WorkspaceName           string                  `json:"workspace_name,omitempty"`
	StageIDs                []string                `json:"stage_ids,omitempty"`
	Relations               []protocol.TaskRelation `json:"relations,omitempty"`
	UseDefaultWorkspace     bool                    `json:"use_default_workspace,omitempty"`
}
type canonicalTaskCreateContext struct {
	Issue  protocol.Issue
	Source canonicalOwnerIssueSource
	Reason canonicalOwnerTaskPayload
}

func (CanonicalOwnerTaskCreate) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	if x.Env.Target.Type != "issue" || x.Env.ParentRequestID != "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "an independent canonical issue request is required")
	}
	expected, err := requireRevision(x.Env)
	if err != nil {
		return Outcome{}, err
	}
	current, err := store.GetCanonicalIssue(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, errNotFound(err, "canonical issue does not exist")
	}
	if current.Revision != expected {
		return Outcome{}, protocol.Errorf(protocol.CodeStaleRevision, "issue revision changed")
	}
	var input protocol.CreateIssueTaskPayload
	if err := decodePayload(x.Env, &input); err != nil {
		return Outcome{}, err
	}
	p := canonicalOwnerTaskPayload{canonicalOwnerIssueSource: canonicalOwnerIssueSource{SourceMessageID: input.SourceMessageID, ExpectedSourceRevision: input.ExpectedSourceRevision}, ExpectedAssignmentEpoch: input.ExpectedAssignmentEpoch, Title: input.Title, Spec: domainSpecFromOwner(input.Spec), Rating: input.Rating, WorkspaceName: input.WorkspaceName, StageIDs: input.StageIDs, Relations: input.Relations, UseDefaultWorkspace: input.UseDefaultWorkspace}
	if err := domain.CheckIssueEpoch(current.IssueControl, p.ExpectedAssignmentEpoch); err != nil {
		return Outcome{}, err
	}
	if current.State != protocol.IssueActive && current.State != protocol.IssuePaused {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "reopen responsibility before creating a task")
	}
	if current.AssignmentState != "active" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "assignment handoff is unresolved")
	}
	if err := validateCanonicalOwnerGraph(ctx, tx.Q(), current, nil, true); err != nil {
		return Outcome{}, err
	}
	if err := validateCanonicalSourceReference(ctx, tx.Q(), p.canonicalOwnerIssueSource); err != nil {
		return Outcome{}, err
	}
	seen := map[string]bool{}
	for _, id := range p.StageIDs {
		if seen[id] || !slices.ContainsFunc(current.Plan.Stages, func(s protocol.WorkItemStage) bool {
			return s.StageID == id && domain.IssueStageAcceptsWork(current.Plan, id)
		}) {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "stage_ids must name distinct available issue stages")
		}
		seen[id] = true
	}
	spec := p.Spec
	if p.UseDefaultWorkspace {
		if current.Plan.DefaultWorkspaceTaskID == "" {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "issue has no default workspace")
		}
		if err := checkDefaultWorkspaceOwner(ctx, tx.Q(), current.IssueID, current.Plan.DefaultWorkspaceTaskID); err != nil {
			return Outcome{}, err
		}
		spec.Workspace = domain.Workspace{Mode: domain.WorkspaceReuse, SourceTaskID: current.Plan.DefaultWorkspaceTaskID, IncludeExistingChanges: spec.Workspace.IncludeExistingChanges}
	}
	payload := createTaskPayload{Title: p.Title, Spec: spec, Rating: p.Rating, Project: current.Plan.Project, WorkspaceName: p.WorkspaceName, StageIDs: p.StageIDs, Relations: p.Relations}
	return createTask(ctx, x, tx, payload, &canonicalTaskCreateContext{Issue: current, Source: p.canonicalOwnerIssueSource, Reason: p})
}

func freezeCanonicalTaskContext(ctx context.Context, q store.Querier, current protocol.Issue) ([]protocol.TaskWorkContext, error) {
	out := []protocol.TaskWorkContext{}
	for {
		item := protocol.WorkItem{WorkItemID: current.IssueID, Revision: current.Revision, WorkItemPlan: current.Plan}
		frozen := freezeIssueContext(item)
		digest, facts, err := store.EffectiveContext(ctx, q, current.IssueID)
		if err != nil {
			return nil, err
		}
		frozen.ContextDigest = digest
		frozen.Facts = append(frozen.Facts, facts...)
		out = append(out, frozen)
		if current.ParentIssueID == "" {
			return out, nil
		}
		current, err = store.GetCanonicalIssue(ctx, q, current.ParentIssueID)
		if err != nil {
			return nil, err
		}
	}
}

func bindCanonicalPrimaryTask(ctx context.Context, x *Exec, tx *store.Tx, creation canonicalTaskCreateContext, task string, stages []string) ([]Event, error) {
	next := creation.Issue
	next.Plan.Stages = slices.Clone(next.Plan.Stages)
	for i := range next.Plan.Stages {
		if slices.Contains(stages, next.Plan.Stages[i].StageID) {
			next.Plan.Stages[i].TaskIDs = append(slices.Clone(next.Plan.Stages[i].TaskIDs), task)
		}
	}
	next.Revision++
	next.UpdatedAt = x.Now.UnixMilli()
	// Membership is recorded without manufacturing a stage or rewriting spec1.
	out, err := saveCanonicalOwnerDefinition(ctx, x, tx, next, creation.Issue.Revision, creation.Source, map[string]any{"task_id": task, "request": creation.Reason}, "issue.task_created")
	return out.Events, err
}

func validateCanonicalBoundPlan(ctx context.Context, x *Exec, q store.Querier, issue string, plan protocol.WorkItemPlan, previous *protocol.WorkItemPlan) error {
	if plan.DefaultWorkspaceTaskID != "" {
		if err := checkDefaultWorkspaceOwner(ctx, q, issue, plan.DefaultWorkspaceTaskID); err != nil {
			return err
		}
	}
	for _, stage := range plan.Stages {
		for _, id := range stage.TaskIDs {
			primary, err := store.GetTaskIssueOwner(ctx, q, id)
			if err != nil {
				return err
			}
			if primary != issue {
				return protocol.Errorf(protocol.CodeInvalidRequest, "stage task must belong to this primary issue")
			}
			existed := previous != nil && slices.ContainsFunc(previous.Stages, func(old protocol.WorkItemStage) bool {
				return old.StageID == stage.StageID && slices.Contains(old.TaskIDs, id)
			})
			if !existed {
				task, err := store.GetTask(ctx, q, id)
				if err != nil {
					return err
				}
				if !task.Lifecycle.Terminal() && !domain.IssueStageAcceptsWork(plan, stage.StageID) {
					return protocol.Errorf(protocol.CodeInvalidTransition, "new work requires an unfinished stage and containing groups")
				}
			}
		}
	}
	return validateCanonicalOwnerPlan(x, plan, previous)
}

// Native run facts keep their existing role/lease checks. This check applies
// only to planner control/contract commands while issue managers are disabled.
func canonicalTaskPlannerAccess(ctx context.Context, q store.Querier, x *Exec) error {
	if x.Principal.Kind == protocol.PrincipalOwner {
		return nil
	}
	scope, err := store.ResolveTaskIssueScope(ctx, q, x.Env.Target.ID)
	if err != nil {
		return err
	}
	if scope.Canonical {
		return protocol.Errorf(protocol.CodeForbidden, "canonical task planning requires the owner until scoped managers are enabled")
	}
	return nil
}

// Legacy planning sources carry main coordination effects. Canonical owner task
// edits are authorized by their own request until the direct-chat source API is
// introduced; they must not attach a canonical responsibility to a main turn.
func claimTaskPlanningSource(ctx context.Context, x *Exec, tx *store.Tx, source string, seq int64) error {
	scope, err := store.ResolveTaskIssueScope(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return err
	}
	if scope.Canonical {
		if source != "" || seq != 0 {
			return protocol.Errorf(protocol.CodeInvalidRequest, "canonical owner task edits do not accept a legacy main planning source")
		}
		return nil
	}
	return claimPlanCommit(ctx, x, tx, source, seq)
}
