package app

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// prepareOwnerExecution freezes self-execution. Delegating to a different
// identity requires publishing that identity's child Issue first.
func prepareOwnerExecution(ctx context.Context, x *Exec, q store.Querier, issue protocol.Issue, spec *domain.Spec) (*protocol.AgentInventoryDefinition, error) {
	enabled, err := store.IssueUsesOwnerExecution(ctx, q, issue.IssueID)
	if err != nil || !enabled {
		return nil, err
	}
	if issue.Assignee.Kind != protocol.IssueAssigneeAgent || spec.AgentSelection == nil {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "owner execution requires an assigned Agent and explicit implementation/acceptance categories")
	}
	parts := strings.Split(issue.Assignee.Ref, "/")
	if len(parts) != 2 {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "Issue owner identity is invalid")
	}
	inventory, err := store.GetAgentInventory(ctx, q, parts[0])
	if err != nil {
		return nil, errNotFound(err, "owner inventory is unavailable")
	}
	for _, a := range inventory.Projection.Agents {
		if a.Identity.LocalID != parts[1] {
			continue
		}
		implementation := spec.AgentSelection.Implementation
		if !a.Enabled || !slices.Contains(a.Purposes, "worker_run") {
			return nil, protocol.Errorf(protocol.CodeInvalidTransition, "this Agent has no execution adapter; delegate a child Issue to an execution-capable Agent")
		}
		if implementation.AgentID != "" && implementation.AgentID != issue.Assignee.Ref || !slices.Contains(a.Categories, implementation.CategoryID) || implementation.RuntimeID != "" && implementation.RuntimeID != a.Binding.RuntimeID || !implementation.MatchesProfile(protocol.ExecutionProfile{Model: a.RequestedModel, Effort: a.RequestedEffort}) || spec.Requirements.HostID != "" && spec.Requirements.HostID != parts[0] {
			return nil, protocol.Errorf(protocol.CodeForbidden, "another Agent must own a child Issue before executing this work")
		}
		if x.coordinationExecution != nil && (x.coordinationExecution.Selection.Identity != a.Identity || x.coordinationExecution.Selection.Definition.Revision != a.Revision || x.coordinationExecution.Selection.Definition.Model != a.RequestedModel || x.coordinationExecution.Selection.Definition.Effort != a.RequestedEffort) {
			return nil, protocol.Errorf(protocol.CodeStaleRevision, "owner definition changed; reconcile responsibility before scheduling a new execution")
		}
		selection := *spec.AgentSelection
		selection.Implementation.AgentID = issue.Assignee.Ref
		selection.Implementation.RuntimeID = a.Binding.RuntimeID
		selection.Implementation.Model = a.RequestedModel
		selection.Implementation.Effort = a.RequestedEffort
		spec.AgentSelection = &selection
		spec.Requirements.HostID = parts[0]
		return &a, nil
	}
	return nil, protocol.Errorf(protocol.CodeNotFound, "Issue owner is absent from its registered inventory")
}

func validateRetainedReviewer(ctx context.Context, q store.Querier, contract store.TaskOwnerContract, previous, next domain.Spec) error {
	ids, err := store.FollowupIDs(ctx, q, `SELECT run_id FROM run_issue_responsibilities WHERE issue_id=? ORDER BY created_at DESC,rowid DESC LIMIT 1`, contract.ReviewIssueID)
	if err != nil || len(ids) == 0 {
		return err
	}
	pin, err := store.GetRunExecution(ctx, q, ids[0])
	if err != nil {
		return err
	}
	requirement := next.AgentSelection.Acceptance
	ref := pin.Identity.ExecutionEnvironmentID + "/" + pin.Identity.LocalID
	compatible := (requirement.AgentID == "" || requirement.AgentID == ref) && (requirement.RuntimeID == "" || requirement.RuntimeID == pin.Runtime.RuntimeID) && requirement.MatchesProfile(pin.Profile) && (next.AcceptanceExecution == nil || *next.AcceptanceExecution == pin.Profile)
	if compatible && requirement.CategoryID != previous.AgentSelection.Acceptance.CategoryID {
		compatible = false
		inventory, err := store.GetAgentInventory(ctx, q, pin.Identity.ExecutionEnvironmentID)
		if err != nil {
			return err
		}
		for _, a := range inventory.Projection.Agents {
			compatible = compatible || a.Identity == pin.Identity && slices.Contains(a.Categories, requirement.CategoryID)
		}
	}
	if !compatible {
		return protocol.Errorf(protocol.CodeInvalidTransition, "a task refresh cannot replace its review child owner; explicitly reconcile or replace the responsibility contract")
	}
	return nil
}

func recordOwnerExecution(ctx context.Context, x *Exec, tx *store.Tx, issue protocol.Issue, task string, agent protocol.AgentInventoryDefinition, spec domain.Spec) ([]Event, error) {
	now := x.Now.UnixMilli()
	review := protocol.Issue{IssueControl: protocol.IssueControl{IssueID: x.IDs.New(protocol.PrefixWorkItem), ParentIssueID: issue.IssueID, State: protocol.IssueActive, WorkStatus: protocol.IssueTodo, Assignee: issue.Assignee, AssignmentEpoch: 1, AssignmentState: "active"}, Revision: 1,
		Plan: protocol.IssuePlan{Title: "Review: " + spec.Goal, Project: issue.Plan.Project, Intent: "Independently evaluate the frozen task agreement and its submitted artifact", Scope: "Acceptance of task " + task, CompletionCriteria: []string{"Record an independent verdict against the exact artifact and task spec"}, Stages: []protocol.IssueStage{}}, DependsOn: []string{}, CreatedAt: now, UpdatedAt: now}
	out, err := saveCanonicalOwnerDefinition(ctx, x, tx, review, 0, canonicalOwnerIssueSource{}, map[string]any{"task_id": task, "role": protocol.RoleAccept}, "issue.created")
	if err != nil {
		return nil, err
	}
	reviewPlan, err := json.Marshal(review.Plan)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO task_owner_contracts(task_id,issue_id,assignment_epoch,agent_ref,agent_revision,runtime_id,runtime_revision,model,effort,review_issue_id,review_plan_json,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, task, issue.IssueID, issue.AssignmentEpoch, issue.Assignee.Ref, agent.Revision, agent.Binding.RuntimeID, agent.Binding.Revision, agent.RequestedModel, agent.RequestedEffort, review.IssueID, string(reviewPlan), now); err != nil {
		return nil, err
	}
	return out.Events, nil
}

func claimOwnerExecution(ctx context.Context, x *Exec, tx *store.Tx, run store.RunRow, pin *protocol.RunExecutionPin) ([]Event, error) {
	contract, err := store.GetTaskOwnerContract(ctx, tx.Q(), run.TaskID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if pin == nil {
		return nil, protocol.Errorf(protocol.CodeInvalidTransition, "owner execution requires a named Agent")
	}
	ref := pin.Identity.ExecutionEnvironmentID + "/" + pin.Identity.LocalID
	issueID := contract.IssueID
	if run.Role == protocol.RoleAccept {
		issueID = contract.ReviewIssueID
	}
	issue, err := store.GetCanonicalIssue(ctx, tx.Q(), issueID)
	if err != nil {
		return nil, err
	}
	if issue.State != protocol.IssueActive || issue.AssignmentState != "active" {
		return nil, protocol.Errorf(protocol.CodeInvalidTransition, "execution responsibility is unavailable")
	}
	var events []Event
	if run.Role == protocol.RoleImplement {
		if ref != contract.AgentRef || issue.Assignee.Ref != ref || issue.AssignmentEpoch != contract.AssignmentEpoch || uint64(contract.AgentRevision) != pin.AgentRevision || contract.RuntimeID != pin.Runtime.RuntimeID || uint64(contract.RuntimeRevision) != pin.Runtime.Revision || contract.Model != pin.Profile.Model || contract.Effort != pin.Profile.Effort {
			return nil, protocol.Errorf(protocol.CodeStaleRevision, "self-execution no longer matches the frozen Issue owner")
		}
	} else {
		var claimed bool
		if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM run_issue_responsibilities WHERE issue_id=?)`, issueID).Scan(&claimed); err != nil {
			return nil, err
		}
		if claimed && issue.Assignee.Ref != ref {
			return nil, protocol.Errorf(protocol.CodeStaleRevision, "review execution must retain its child Issue owner")
		}
		if !claimed {
			if issue.AssignmentEpoch != 1 || issue.Assignee.Ref != contract.AgentRef || issue.Revision != 1 {
				return nil, protocol.Errorf(protocol.CodeStaleRevision, "the pending review responsibility was changed by its owner")
			}
			previous := issue.Revision
			issue.Assignee = protocol.IssueAssignee{Kind: protocol.IssueAssigneeAgent, Ref: ref}
			issue.AssignmentEpoch++
			issue.Revision++
			issue.WorkStatus = protocol.IssueInProgress
			issue.UpdatedAt = x.Now.UnixMilli()
			out, err := saveCanonicalOwnerDefinition(ctx, x, tx, issue, previous, canonicalOwnerIssueSource{}, map[string]any{"task_id": run.TaskID, "run_id": run.RunID}, "issue.execution_claimed")
			if err != nil {
				return nil, err
			}
			events = out.Events
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO run_issue_responsibilities(run_id,issue_id,assignment_epoch,issue_revision,agent_ref,created_at) VALUES(?,?,?,?,?,?)`, run.RunID, issueID, issue.AssignmentEpoch, issue.Revision, ref, x.Now.UnixMilli())
	return events, err
}

// A review child has a bounded responsibility: judging this exact artifact.
// Its closure never closes the implementation or parent Issue.
func finishOwnerReview(ctx context.Context, x *Exec, tx *store.Tx, run store.RunRow, acceptance string, verdict protocol.Verdict) ([]Event, error) {
	contract, err := store.GetTaskOwnerContract(ctx, tx.Q(), run.TaskID)
	if errors.Is(err, store.ErrNotFound) || verdict != protocol.VerdictPassed {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	issue, err := store.GetCanonicalIssue(ctx, tx.Q(), contract.ReviewIssueID)
	if err != nil {
		return nil, err
	}
	var current bool
	if err = tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM run_issue_responsibilities WHERE run_id=? AND issue_id=? AND assignment_epoch=? AND issue_revision=? AND agent_ref=?)`, run.RunID, issue.IssueID, issue.AssignmentEpoch, issue.Revision, issue.Assignee.Ref).Scan(&current); err != nil {
		return nil, err
	}
	if !current || issue.State != protocol.IssueActive || issue.AssignmentState != "active" {
		return nil, nil
	}
	plan, err := json.Marshal(issue.Plan)
	if err != nil || string(plan) != contract.ReviewPlanJSON {
		// A human may have expanded the responsibility beyond this bounded
		// review. Its original verdict cannot settle the added scope.
		return nil, err
	}
	facts, err := canonicalOwnerEndpointFacts(ctx, tx.Q(), issue)
	if err != nil {
		return nil, err
	}
	if len(domain.IssueEndpointBlockers(facts)) != 0 {
		return nil, nil
	}
	if err := checkCanonicalCompletionPrerequisites(ctx, tx.Q(), issue); err != nil {
		if protocol.CodeOf(err) == protocol.CodeInvalidTransition {
			return nil, nil
		}
		return nil, err
	}
	closure := &protocol.IssueClosure{Kind: protocol.WorkItemClosureCompleted, Reason: "The assigned reviewer submitted the final independent verdict", Evidence: []protocol.IssueEvidence{{Kind: protocol.WorkItemEvidenceAcceptance, Reference: acceptance, Summary: "Independent acceptance passed for the exact artifact and frozen task spec"}}}
	if err := domain.ValidateResponsibleIssueClosure(protocol.WorkItem{IssuePlan: issue.Plan, Closure: closure}, *closure); err != nil {
		if protocol.CodeOf(err) == protocol.CodeInvalidTransition || protocol.CodeOf(err) == protocol.CodeInvalidRequest {
			return nil, nil
		}
		return nil, err
	}
	previous := issue.Revision
	issue.IssueControl, err = domain.CloseIssueControl(issue.IssueControl, facts)
	if err != nil {
		return nil, err
	}
	issue.Revision++
	issue.UpdatedAt = x.Now.UnixMilli()
	issue.Closure = closure
	out, err := saveCanonicalOwnerDefinition(ctx, x, tx, issue, previous, canonicalOwnerIssueSource{}, map[string]any{"acceptance_id": acceptance}, "issue.closed")
	if err == nil {
		err = recordCanonicalEndpointContextReviews(ctx, tx, issue.IssueID)
	}
	return out.Events, err
}

// Cancelling a task also withdraws its generated bounded review, once every
// physical execution is settled. Human changes to that child require review.
func settleCancelledOwnerReview(ctx context.Context, x *Exec, tx *store.Tx, task string) ([]Event, error) {
	contract, err := store.GetTaskOwnerContract(ctx, tx.Q(), task)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	issue, err := store.GetCanonicalIssue(ctx, tx.Q(), contract.ReviewIssueID)
	if err != nil || issue.State == protocol.IssueClosed || issue.State == protocol.IssueCancelled {
		return nil, err
	}
	var open bool
	if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE task_id=? AND observed_state NOT IN ('finished','failed'))`, task).Scan(&open); err != nil || open {
		return nil, err
	}
	var count int
	if err := tx.Q().QueryRowContext(ctx, `SELECT COUNT(*) FROM run_issue_responsibilities WHERE issue_id=?`, issue.IssueID).Scan(&count); err != nil {
		return nil, err
	}
	if issue.AssignmentState != "active" || issue.State != protocol.IssueActive {
		return nil, nil
	}
	plan, err := json.Marshal(issue.Plan)
	if err != nil || string(plan) != contract.ReviewPlanJSON {
		return nil, err
	}
	if count == 0 {
		if issue.Revision != 1 || issue.Assignee.Ref != contract.AgentRef {
			return nil, nil
		}
	} else {
		var current bool
		if err := tx.Q().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM run_issue_responsibilities WHERE issue_id=? AND assignment_epoch=? AND issue_revision=? AND agent_ref=?)`, issue.IssueID, issue.AssignmentEpoch, issue.Revision, issue.Assignee.Ref).Scan(&current); err != nil || !current {
			return nil, err
		}
	}
	previous := issue.Revision
	issue.State = protocol.IssueCancelled
	facts, err := canonicalOwnerEndpointFacts(ctx, tx.Q(), issue)
	if err != nil {
		return nil, err
	}
	if len(domain.IssueEndpointBlockers(facts)) != 0 {
		return nil, nil
	}
	issue.Revision++
	issue.UpdatedAt = x.Now.UnixMilli()
	out, err := saveCanonicalOwnerDefinition(ctx, x, tx, issue, previous, canonicalOwnerIssueSource{}, map[string]any{"task_id": task, "reason": "The task and all of its execution attempts are cancelled and settled"}, "issue.cancelled")
	return out.Events, err
}
