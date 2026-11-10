package app

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/extensions"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

// prepareOwnerExecution freezes self-execution when selected. Ordinary scoped
// collaboration may select another configured executor without a child Issue.
func prepareOwnerExecution(ctx context.Context, x *Exec, q store.Querier, issue protocol.Issue, spec *domain.Spec) (*protocol.AgentInventoryDefinition, error) {
	enabled, err := store.IssueUsesOwnerExecution(ctx, q, issue.IssueID)
	if err != nil || !enabled {
		return nil, err
	}
	if issue.Assignee.Kind != protocol.IssueAssigneeAgent || spec.AgentSelection == nil {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "owner execution requires an assigned Agent and an implementation category")
	}
	parts := strings.Split(issue.Assignee.Ref, "/")
	if len(parts) != 2 {
		return nil, protocol.Errorf(protocol.CodeInvalidRequest, "Issue owner identity is invalid")
	}
	inventory, _, err := store.RetainedOwnerInventory(ctx, q, issue)
	if err != nil {
		return nil, errNotFound(err, "owner inventory is unavailable")
	}
	for _, a := range inventory.Projection.Agents {
		if a.Identity.LocalID != parts[1] {
			continue
		}
		implementation := spec.AgentSelection.Implementation
		if implementation.AgentID != "" && implementation.AgentID != issue.Assignee.Ref || !slices.Contains(a.Categories, implementation.CategoryID) || implementation.RuntimeID != "" && implementation.RuntimeID != a.Binding.RuntimeID || !implementation.MatchesProfile(protocol.ExecutionProfile{Model: a.RequestedModel, Effort: a.RequestedEffort}) || spec.Requirements.HostID != "" && spec.Requirements.HostID != parts[0] {
			return nil, nil
		}
		if !a.Enabled || !slices.Contains(a.Purposes, "worker_run") {
			return nil, protocol.Errorf(protocol.CodeInvalidTransition, "this Agent has no execution adapter; select an execution-capable Agent")
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
	if contract.ReviewIssueID == "" {
		return nil
	}
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
		runtime, err := store.GetHostRuntime(ctx, q, pin.Identity.ExecutionEnvironmentID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		var inventory store.AgentInventoryRow
		if err == nil && runtimeSupportsAgentContinuation(&runtime.Runtime) {
			inventory, err = store.GetAgentInventoryAt(ctx, q, *pin)
		} else {
			inventory, err = store.GetAgentInventory(ctx, q, pin.Identity.ExecutionEnvironmentID)
		}
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

// ownerTaskWorkerPin derives the execution identity used by Task creation and
// explicit stopped continuation. The manager transport and category are not the
// Task's runtime or managed-extension scope.
func ownerTaskWorkerPin(ctx context.Context, q store.Querier, manager protocol.RunExecutionPin, category string) (protocol.RunExecutionPin, error) {
	pin, err := store.WorkerPinForManager(ctx, q, manager)
	if err != nil {
		return protocol.RunExecutionPin{}, protocol.Errorf(protocol.CodeInvalidTransition, "original owner execution snapshot is unavailable")
	}
	runtime, err := store.GetHostRuntime(ctx, q, pin.Identity.ExecutionEnvironmentID)
	if err != nil {
		return protocol.RunExecutionPin{}, err
	}
	pin.Extensions = protocol.ExtensionExecutionPin{}
	if status := runtime.Runtime.Extensions; status != nil && status.Enabled && slices.Contains(status.Runtimes, pin.Runtime.RuntimeID) && slices.Contains(runtime.Runtime.Capabilities, protocol.CapabilityManagedExtensions) {
		state, err := store.GetExtensionState(ctx, q)
		if err != nil {
			return protocol.RunExecutionPin{}, err
		}
		if state.Published == nil {
			return protocol.RunExecutionPin{}, protocol.Errorf(protocol.CodeInvalidTransition, "worker managed extension baseline has not been published")
		}
		release, _, err := store.GetExtensionRelease(ctx, q, state.Published.ID)
		if err != nil {
			return protocol.RunExecutionPin{}, err
		}
		combination, err := extensions.Resolve(release, category, pin.Identity)
		if err != nil {
			return protocol.RunExecutionPin{}, err
		}
		pin.Extensions = combination.Pin
	}
	return pin, nil
}

func recordOwnerExecution(ctx context.Context, x *Exec, tx *store.Tx, issue protocol.Issue, task string, agent protocol.AgentInventoryDefinition, spec domain.Spec) ([]Event, error) {
	inventory, manager, err := store.RetainedOwnerInventory(ctx, tx.Q(), issue)
	if err != nil {
		return nil, err
	}
	var execution *protocol.RunExecutionPin
	if manager != nil {
		pin, err := ownerTaskWorkerPin(ctx, tx.Q(), *manager, spec.AgentSelection.Implementation.CategoryID)
		if err != nil {
			return nil, err
		}
		execution = &pin
	} else {
		runtime, err := store.GetHostRuntime(ctx, tx.Q(), agent.Identity.ExecutionEnvironmentID)
		if err != nil {
			return nil, err
		}
		for _, e := range runtime.Runtime.AgentExecutors {
			if (e.Pin.Extensions == (protocol.ExtensionExecutionPin{}) || e.Pin.Extensions.CategoryID == spec.AgentSelection.Implementation.CategoryID) && e.Pin.Purpose == "" && e.Pin.Identity == agent.Identity && e.Pin.AgentRevision == agent.Revision && e.Pin.Runtime == agent.Binding && e.Pin.SnapshotID == inventory.Receipt.SnapshotID && e.Pin.PublicSHA256 == inventory.Receipt.PublicSHA256 {
				pin := e.Pin
				execution = &pin
				break
			}
		}
	}
	if execution == nil {
		runtime, err := store.GetHostRuntime(ctx, tx.Q(), agent.Identity.ExecutionEnvironmentID)
		if err != nil {
			return nil, err
		}
		if runtimeSupportsAgentContinuation(&runtime.Runtime) {
			return nil, protocol.Errorf(protocol.CodeInvalidTransition, "owner execution has no frozen snapshot")
		}
	}
	var rawPin any
	if execution != nil {
		body, err := json.Marshal(execution)
		if err != nil {
			return nil, err
		}
		rawPin = string(body)
	}
	now := x.Now.UnixMilli()
	if issue.AcceptancePolicy != nil && !issue.AcceptancePolicy.Required {
		_, err := tx.Exec(ctx, `INSERT INTO creator_task_owner_contracts(task_id,issue_id,assignment_epoch,agent_ref,agent_revision,runtime_id,runtime_revision,model,effort,created_at,execution_pin_json) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, task, issue.IssueID, issue.AssignmentEpoch, issue.Assignee.Ref, agent.Revision, agent.Binding.RuntimeID, agent.Binding.Revision, agent.RequestedModel, agent.RequestedEffort, now, rawPin)
		return nil, err
	}
	review := protocol.Issue{IssueControl: protocol.IssueControl{IssueID: x.IDs.New(protocol.PrefixWorkItem), ParentIssueID: issue.IssueID, State: protocol.IssueActive, WorkStatus: protocol.IssueBacklog, Assignee: issue.Assignee, AssignmentEpoch: 1, AssignmentState: "active"}, Revision: 1,
		Plan: protocol.IssuePlan{Title: "Review: " + spec.Goal, Project: issue.Plan.Project, Intent: "Independently evaluate the frozen task agreement and its submitted artifact", Scope: "Acceptance of task " + task, CompletionCriteria: []string{"Record an independent verdict against the exact artifact and task spec"}, Stages: []protocol.IssueStage{}}, DependsOn: []string{}, CreatedAt: now, UpdatedAt: now}
	out, err := saveCanonicalOwnerDefinition(ctx, x, tx, review, 0, canonicalOwnerIssueSource{}, map[string]any{"task_id": task, "role": protocol.RoleAccept}, "issue.created")
	if err != nil {
		return nil, err
	}
	review.WorkStatus = protocol.IssueTodo
	review.TodoAt = now
	review.TodoRevision = 2
	review.Revision = 2
	admitted, err := saveCanonicalOwnerDefinition(ctx, x, tx, review, 1, canonicalOwnerIssueSource{}, map[string]any{"task_id": task, "role": protocol.RoleAccept}, "issue.status_changed")
	if err != nil {
		return nil, err
	}
	out.Events = append(out.Events, admitted.Events...)

	reviewPlan, err := json.Marshal(review.Plan)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO task_owner_contracts(task_id,issue_id,assignment_epoch,agent_ref,agent_revision,runtime_id,runtime_revision,model,effort,review_issue_id,review_plan_json,created_at,execution_pin_json,review_admission_revision) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, task, issue.IssueID, issue.AssignmentEpoch, issue.Assignee.Ref, agent.Revision, agent.Binding.RuntimeID, agent.Binding.Revision, agent.RequestedModel, agent.RequestedEffort, review.IssueID, string(reviewPlan), now, rawPin, review.Revision); err != nil {
		return nil, err
	}
	return out.Events, nil
}

func claimOwnerExecution(ctx context.Context, x *Exec, tx *store.Tx, run store.RunRow, pin *protocol.RunExecutionPin) ([]Event, error) {
	contract, err := store.GetTaskOwnerContract(ctx, tx.Q(), run.TaskID)
	if errors.Is(err, store.ErrNotFound) {
		origin, err := store.TaskIssueResponsibilityOrigin(ctx, tx.Q(), run.TaskID)
		if err == nil {
			current, err := store.TaskOwnerContractCurrent(ctx, tx.Q(), run.TaskID)
			if err != nil {
				return nil, err
			}
			if !current {
				return nil, protocol.Errorf(protocol.CodeStaleRevision, "the collaboration's Issue responsibility was handed over")
			}
			started, err := markIssueWorkStarted(ctx, x, tx, origin.IssueID)
			if err != nil {
				return nil, err
			}
			// The publishing owner is the control fence. The independently
			// selected collaborator remains in run_agent_executions.
			_, err = tx.Exec(ctx, `INSERT INTO run_issue_responsibilities(run_id,issue_id,assignment_epoch,issue_revision,agent_ref,created_at) VALUES(?,?,?,?,?,?)`, run.RunID, origin.IssueID, origin.AssignmentEpoch, origin.IssueRevision, origin.AgentRef, x.Now.UnixMilli())
			return started, err
		}
		if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		ids, err := store.FollowupIDs(ctx, tx.Q(), `SELECT c.issue_id FROM issue_controls c JOIN task_work_item_owners o ON o.work_item_id=c.issue_id WHERE o.task_id=?`, run.TaskID)
		if err != nil || len(ids) == 0 {
			return nil, err
		}
		return markIssueWorkStarted(ctx, x, tx, ids[0])
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
			if issue.AssignmentEpoch != 1 || issue.Assignee.Ref != contract.AgentRef || issue.Revision != contract.ReviewAdmissionRevision {
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
	started, err := markIssueWorkStarted(ctx, x, tx, issueID)
	if err != nil {
		return nil, err
	}
	events = append(events, started...)
	issue, err = store.GetCanonicalIssue(ctx, tx.Q(), issueID)
	if err != nil {
		return nil, err
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
	if contract.ReviewIssueID == "" {
		return nil, nil
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
	if contract.ReviewIssueID == "" {
		return nil, nil
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
		if issue.Revision != contract.ReviewAdmissionRevision || issue.Assignee.Ref != contract.AgentRef {
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
