package app

import (
	"context"
	"encoding/json"
	"fmt"
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
	IndependentAcceptance   *protocol.IndependentAcceptanceRequest `json:"independent_acceptance,omitempty"`
	ExpectedAssignmentEpoch int64                                  `json:"expected_assignment_epoch"`
	Title                   string                                 `json:"title"`
	Spec                    domain.Spec                            `json:"spec"`
	Rating                  protocol.Rating                        `json:"rating,omitempty"`
	WorkspaceName           string                                 `json:"workspace_name,omitempty"`
	StageIDs                []string                               `json:"stage_ids,omitempty"`
	Relations               []protocol.TaskRelation                `json:"relations,omitempty"`
	UseDefaultWorkspace     bool                                   `json:"use_default_workspace,omitempty"`
}
type canonicalTaskCreateContext struct {
	AcceptancePolicy *protocol.IssueAcceptancePolicy
	Issue            protocol.Issue
	Source           canonicalOwnerIssueSource
	Reason           canonicalOwnerTaskPayload
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
	p := canonicalOwnerTaskPayload{canonicalOwnerIssueSource: canonicalOwnerIssueSource{SourceMessageID: input.SourceMessageID, ExpectedSourceRevision: input.ExpectedSourceRevision}, IndependentAcceptance: input.IndependentAcceptance, ExpectedAssignmentEpoch: input.ExpectedAssignmentEpoch, Title: input.Title, Spec: domainSpecFromOwner(input.Spec), Rating: input.Rating, WorkspaceName: input.WorkspaceName, StageIDs: input.StageIDs, Relations: input.Relations, UseDefaultWorkspace: input.UseDefaultWorkspace}
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
		if seen[id] || !slices.ContainsFunc(current.Plan.Stages, func(s protocol.IssueStage) bool {
			return s.StageID == id && domain.IssueStageAcceptsWork(current.Plan, id)
		}) {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "stage_ids must name distinct available issue stages")
		}
		seen[id] = true
	}
	spec := p.Spec
	var deliveryDuty *protocol.IssueDeliveryDuty
	if input.DeliveryAuthorization != nil {
		d, err := deliveryExecutionAuthorized(ctx, tx.Q(), current, *input.DeliveryAuthorization)
		if err != nil {
			return Outcome{}, err
		}
		if d.ExecutionTaskID != "" {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidTransition, "delivery already has a frozen execution; reconcile it rather than starting another")
		}
		if d.Candidate.Deploy || d.Authorization != nil {
			observed := protocol.ObservationScope{Deployment: d.Candidate.Deploy, WorkItemIDs: []string{current.IssueID}}
			if spec.Observation != nil {
				observed = *spec.Observation
				observed.Deployment = observed.Deployment || d.Candidate.Deploy
				observed.WorkItemIDs = slices.Clone(spec.Observation.WorkItemIDs)
				if !slices.Contains(observed.WorkItemIDs, current.IssueID) {
					observed.WorkItemIDs = append(observed.WorkItemIDs, current.IssueID)
				}
			}
			spec.Observation = &observed
		}
		if !spec.RequiresDelivery(domain.DeliveryReport) {
			spec.Deliverables = append(spec.RequiredDeliverables(), domain.DeliveryReport)
		}
		spec.Acceptance = append(slices.Clone(spec.Acceptance), "Independently verify the approved candidate merge and, when deploy=true, its merged-source release provenance, completed controlled upgrade ID, actual server/host installation readbacks and the original Issue behavior. A report or provider merged flag alone cannot establish successful deployment.")
		deliveryDuty = &d
		spec.Constraints = append(slices.Clone(spec.Constraints), deliveryExecutionScope(*d.Candidate, d.Authorization), "Execute only the approved candidate and scope. Recheck provider head/base before the first mutation. An unknown provider result requires reconciliation, never repeat merge/deploy. If reconciliation is present, this task only verifies the referenced existing completed delivery and must never merge, start or retry an installation. Otherwise Tend must execute the approved merge only when provider_state=open, rechecking the frozen head, historical PR base_sha and current target_sha before merging. After that merge, verify the historical PR base_sha is unchanged and the current target is exactly the resulting merge_sha; build that verified merge_sha. For provider_state=merged verify the frozen existing merge_sha is an ancestor of target_sha and never merge again; build the frozen target_sha. The historical PR base_sha is not the deployment source. Target drift beyond these approved transitions or changed lineage requires a new candidate and the applicable Issue policy or owner approval. Only when deploy=true, build that verified merged source, start the existing controlled upgrade through its local CLI with --delivery-task set to this task ID, and verify installed server version/hash, the complete approved host set, and the original Issue behavior. Do not leave merge or deployment to an external operator. A rebase changing the head needs a new candidate and the applicable Issue policy or owner approval. Keep a stable upgrade/request identity across retries; reconcile unknown outcomes through upgrade status and installed receipts before another mutation. Persist provider merge receipt, release provenance and the exact upgrade:<id> reference in required report.evidence; the server binds that started batch before scheduling independent acceptance. If this worker runs on a deployment target, submit that durable started-batch report and release its run instead of waiting while holding its own drain lease; retain responsibility until independent acceptance reads the completed batch, actual server/host installations and original Issue behavior. Do not claim started means deployed.")
	}
	if p.UseDefaultWorkspace {
		if current.Plan.DefaultWorkspaceTaskID == "" {
			return Outcome{}, protocol.Errorf(protocol.CodeInvalidRequest, "issue has no default workspace")
		}
		if err := checkDefaultWorkspaceOwner(ctx, tx.Q(), current.IssueID, current.Plan.DefaultWorkspaceTaskID); err != nil {
			return Outcome{}, err
		}
		spec.Workspace = domain.Workspace{Mode: domain.WorkspaceReuse, SourceTaskID: current.Plan.DefaultWorkspaceTaskID, IncludeExistingChanges: spec.Workspace.IncludeExistingChanges}
	}
	if _, err := prepareOwnerExecution(ctx, x, tx.Q(), current, &spec); err != nil {
		return Outcome{}, err
	}
	payload := createTaskPayload{Title: p.Title, Spec: spec, Rating: p.Rating, Project: current.Plan.Project, WorkspaceName: p.WorkspaceName, StageIDs: p.StageIDs, Relations: p.Relations}
	creation := &canonicalTaskCreateContext{Issue: current, Source: p.canonicalOwnerIssueSource, Reason: p}
	creation.AcceptancePolicy, err = taskAcceptancePolicy(ctx, x, tx.Q(), current, spec, input.IndependentAcceptance)
	if err != nil {
		return Outcome{}, err
	}
	out, err := createTask(ctx, x, tx, payload, creation)
	if err != nil {
		return Outcome{}, err
	}
	if deliveryDuty != nil {
		task := out.Result.(taskResult).TaskID
		raw, err := json.Marshal(deliveryExecutionSnapshot{IssueDeliveryCandidate: *deliveryDuty.Candidate, AssignmentEpoch: current.AssignmentEpoch, Authorization: deliveryDuty.Authorization})
		if err != nil {
			return Outcome{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO issue_delivery_executions(task_id,issue_id,source_task_id,duty_revision,candidate_json) VALUES(?,?,?,?,?)`, task, current.IssueID, deliveryDuty.TaskID, deliveryDuty.Revision, string(raw)); err != nil {
			return Outcome{}, err
		}
		deliveryDuty.ExecutionTaskID, deliveryDuty.Status = task, "executing"
		if err := tx.PutIssueDeliveryDuty(ctx, current.IssueID, *deliveryDuty); err != nil {
			return Outcome{}, err
		}
	}
	return out, nil
}

// freezeCanonicalIssueDefinition projects responsibility semantics into the
// existing worker facts. Issue closure criteria are not task Acceptance.
func freezeCanonicalIssueDefinition(item protocol.WorkItem) protocol.TaskWorkContext {
	frozen := freezeIssueContext(item)
	source := fmt.Sprintf("issue:%s@revision:%d", item.WorkItemID, item.Revision)
	if item.Scope != "" {
		frozen.Facts = append(frozen.Facts, protocol.SharedFact{Kind: protocol.SharedFactUserConstraint, Text: "Issue scope: " + item.Scope, Source: source})
	}
	for _, criterion := range item.CompletionCriteria {
		frozen.Facts = append(frozen.Facts, protocol.SharedFact{Kind: protocol.SharedFactGoal, Text: "Issue responsibility completion criterion (not task acceptance): " + criterion, Source: source})
	}
	return frozen
}

func freezeCanonicalTaskContext(ctx context.Context, q store.Querier, current protocol.Issue) ([]protocol.TaskWorkContext, error) {
	out := []protocol.TaskWorkContext{}
	for {
		item := protocol.WorkItem{WorkItemID: current.IssueID, Revision: current.Revision, IssuePlan: current.Plan}
		frozen := freezeCanonicalIssueDefinition(item)
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

func validateCanonicalBoundPlan(ctx context.Context, x *Exec, q store.Querier, issue string, plan protocol.IssuePlan, previous *protocol.IssuePlan) error {
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
			existed := previous != nil && slices.ContainsFunc(previous.Stages, func(old protocol.IssueStage) bool {
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
	if x.coordinationExecution != nil {
		if err := domain.ValidateIssue(plan, previous); err != nil {
			return err
		}
		projected := protocol.WorkItem{WorkItemID: issue, IssuePlan: plan}
		var prior *protocol.WorkItem
		if previous != nil {
			prior = &protocol.WorkItem{WorkItemID: issue, IssuePlan: *previous}
		}
		for i := range plan.Stages {
			if err := validateHostedIssueEvidence(ctx, x, q, projected, prior, plan.Stages[i].Evidence); err != nil {
				return err
			}
		}
		for i := range plan.FollowOns {
			if err := validateHostedIssueEvidence(ctx, x, q, projected, prior, plan.FollowOns[i].Evidence); err != nil {
				return err
			}
		}
		return nil
	}
	return validateCanonicalOwnerPlan(x, plan, previous)
}

// Native run facts keep their existing role/lease checks. This check applies
// only to planner control/contract commands while issue managers are disabled.
func canonicalTaskPlannerAccess(ctx context.Context, q store.Querier, x *Exec) error {
	if x.Principal.Kind == protocol.PrincipalOwner {
		return nil
	}
	if x.coordinationExecution != nil && x.coordinationExecution.IssueID != "" {
		primary, err := store.GetTaskIssueOwner(ctx, q, x.Env.Target.ID)
		if err != nil {
			return err
		}
		if primary == x.coordinationExecution.IssueID {
			return nil
		}
		return protocol.Errorf(protocol.CodeForbidden, "task is outside the admitted Issue")
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
