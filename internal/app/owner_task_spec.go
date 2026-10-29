package app

import (
	"context"
	"encoding/json"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// domainSpecFromOwner maps the owner-input projection to the existing domain
// agreement. SharedContext is deliberately absent and frozen by the server.
func domainSpecFromOwner(p protocol.OwnerTaskSpec) domain.Spec {
	mounts := make([]domain.Mount, 0, len(p.Workspace.Mounts))
	for _, mount := range p.Workspace.Mounts {
		mounts = append(mounts, domain.Mount{Repo: mount.Repo, Path: mount.Path, Base: domain.Base{Branch: mount.Base.Branch, Commit: mount.Base.Commit}, Profile: mount.Profile})
	}
	inputs := make([]protocol.TaskInput, 0, len(p.Inputs))
	for _, input := range p.Inputs {
		inputs = append(inputs, protocol.TaskInput{FileID: input.FileID})
	}
	return domain.Spec{AgentSelection: p.AgentSelection, AcceptanceExecution: p.AcceptanceExecution, Observation: p.Observation, Goal: p.Goal, Scope: p.Scope, Constraints: p.Constraints, Acceptance: p.Acceptance, Delivery: p.Delivery, Deliverables: p.Deliverables, Inputs: inputs, ImplementationContext: p.ImplementationContext, MaxImplementAttempts: p.MaxImplementAttempts, Workspace: domain.Workspace{Mode: p.Workspace.Mode, SourceTaskID: p.Workspace.SourceTaskID, IncludeExistingChanges: p.Workspace.IncludeExistingChanges, Template: p.Workspace.Template, Mounts: mounts, Profile: p.Workspace.Profile}, Requirements: domain.Requirements{HostID: p.Requirements.HostID, OS: p.Requirements.OS, Labels: p.Requirements.Labels}}
}

// CanonicalOwnerTaskUpdate shares UpdateIntent's spec, action and run rules.
// The owner controls the agreement directly without a main-agent approval turn.
type CanonicalOwnerTaskUpdate struct{}

func (CanonicalOwnerTaskUpdate) Operation() string { return "issues.update_task" }
func (CanonicalOwnerTaskUpdate) Authorize(_ context.Context, x *Exec, _ store.Querier) error {
	return allowKinds(x.Principal, protocol.PrincipalOwner)
}
func (CanonicalOwnerTaskUpdate) Apply(ctx context.Context, x *Exec, tx *store.Tx) (Outcome, error) {
	if x.Env.Target.Type != "task" || x.Env.ParentRequestID != "" {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "an independent owner task request is required")
	}
	var p protocol.UpdateIssueTaskPayload
	if err := decodePayload(x.Env, &p); err != nil {
		return Outcome{}, err
	}
	primary, err := store.GetTaskIssueOwner(ctx, tx.Q(), x.Env.Target.ID)
	if err != nil {
		return Outcome{}, err
	}
	issue, err := store.GetCanonicalIssue(ctx, tx.Q(), primary)
	if err != nil {
		return Outcome{}, errNotFound(err, "task has no canonical primary issue")
	}
	if err := domain.CheckIssueEpoch(issue.IssueControl, p.ExpectedAssignmentEpoch); err != nil {
		return Outcome{}, err
	}
	if issue.State != protocol.IssueActive && issue.State != protocol.IssuePaused {
		return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "reopen the primary issue before revising its task agreement")
	}
	payload := updateIntentPayload{Rating: p.Rating, Relations: p.Relations}
	if p.Spec != nil {
		spec := domainSpecFromOwner(*p.Spec)
		payload.Spec = &spec
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Outcome{}, err
	}
	// Preserve the outer owner request identity and transaction. Only the private
	// payload shape is adapted for the existing command, never a second request.
	adapted := *x
	env := *x.Env
	env.Payload = body
	adapted.Env = &env
	return (UpdateIntent{}).Apply(ctx, &adapted, tx)
}
