package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
)

type TaskOwnerContract struct {
	Execution                                                          *protocol.RunExecutionPin
	TaskID, IssueID, AgentRef, RuntimeID, Model, Effort, ReviewIssueID string
	ReviewPlanJSON                                                     string
	AssignmentEpoch, AgentRevision, RuntimeRevision                    int64
	ReviewAdmissionRevision                                            int64
}

func GetTaskOwnerContract(ctx context.Context, q Querier, task string) (TaskOwnerContract, error) {
	var c TaskOwnerContract
	var raw string
	err := q.QueryRowContext(ctx, `SELECT task_id,issue_id,assignment_epoch,agent_ref,agent_revision,runtime_id,runtime_revision,model,effort,review_issue_id,review_plan_json,COALESCE(execution_pin_json,''),COALESCE((SELECT review_admission_revision FROM task_owner_contracts origin WHERE origin.task_id=task_execution_contracts.task_id),1) FROM task_execution_contracts WHERE task_id=?`, task).Scan(&c.TaskID, &c.IssueID, &c.AssignmentEpoch, &c.AgentRef, &c.AgentRevision, &c.RuntimeID, &c.RuntimeRevision, &c.Model, &c.Effort, &c.ReviewIssueID, &c.ReviewPlanJSON, &raw, &c.ReviewAdmissionRevision)
	if err != nil {
		return c, notFound(err, "task owner contract")
	}
	if raw != "" {
		c.Execution = new(protocol.RunExecutionPin)
		if err := json.Unmarshal([]byte(raw), c.Execution); err != nil {
			return c, err
		}
	}
	if c.Execution == nil {
		m, err := CurrentIssueManagerTask(ctx, q, c.IssueID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return c, err
		}
		if err == nil && m.AssignmentEpoch == c.AssignmentEpoch && m.Pin != nil {
			pin, err := WorkerPinForManager(ctx, q, *m.Pin)
			if err != nil && !errors.Is(err, ErrNotFound) {
				return c, err
			}
			if err == nil && pin.Identity.ExecutionEnvironmentID+"/"+pin.Identity.LocalID == c.AgentRef && int64(pin.AgentRevision) == c.AgentRevision && pin.Runtime.RuntimeID == c.RuntimeID && int64(pin.Runtime.Revision) == c.RuntimeRevision && pin.Profile.Model == c.Model && pin.Profile.Effort == c.Effort {
				c.Execution = &pin
			}
		}
	}
	return c, nil
}

func RunIssueResponsibility(ctx context.Context, q Querier, run string) (protocol.ExecutionResponsibility, error) {
	var r protocol.ExecutionResponsibility
	err := q.QueryRowContext(ctx, `SELECT s.issue_id,s.issue_revision,s.assignment_epoch,s.agent_ref,r.task_id,r.run_id,r.role FROM run_issue_responsibilities s JOIN runs r USING(run_id) WHERE r.run_id=?`, run).Scan(&r.IssueID, &r.IssueRevision, &r.AssignmentEpoch, &r.AgentRef, &r.TaskID, &r.RunID, &r.Role)
	if err != nil {
		return r, notFound(err, "run Issue responsibility")
	}
	return r, nil
}

func TaskOwnerContractCurrent(ctx context.Context, q Querier, task string) (bool, error) {
	var current, contract bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_execution_contracts WHERE task_id=?), NOT EXISTS(SELECT 1 FROM task_execution_contracts c JOIN issue_controls i ON i.issue_id=c.issue_id WHERE c.task_id=? AND (i.assignment_epoch<>c.assignment_epoch OR i.assignee_ref<>c.agent_ref OR i.assignment_state<>'active'))`, task, task).Scan(&contract, &current)
	if err != nil || contract {
		return current, err
	}
	origin, err := TaskIssueResponsibilityOrigin(ctx, q, task)
	if errors.Is(err, ErrNotFound) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	issue, err := GetCanonicalIssue(ctx, q, origin.IssueID)
	if err != nil {
		return false, err
	}
	return issue.AssignmentEpoch == origin.AssignmentEpoch && issue.Assignee.Ref == origin.AgentRef && issue.AssignmentState == "active", nil
}

// TaskIssueResponsibilityOrigin recovers the Issue owner that published an
// ordinary collaboration Task. It is independent of its selected executor.
// Existing spec1 and Issue snapshots are immutable; later context revisions
// cannot replace this assignment epoch or create a new execution permission.
func TaskIssueResponsibilityOrigin(ctx context.Context, q Querier, task string) (protocol.ExecutionResponsibility, error) {
	var origin protocol.ExecutionResponsibility
	var revision int64
	err := q.QueryRowContext(ctx, `SELECT o.work_item_id,json_extract(c.value,'$.revision') FROM task_work_item_owners o JOIN task_specs s ON s.task_id=o.task_id AND s.spec_version=1 JOIN json_each(s.body_json,'$.shared_context') c WHERE o.task_id=? AND json_extract(c.value,'$.work_item_id')=o.work_item_id LIMIT 1`, task).Scan(&origin.IssueID, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return origin, ErrNotFound
	}
	if err != nil {
		return origin, err
	}
	issue, err := GetCanonicalIssueRevision(ctx, q, origin.IssueID, revision)
	if err != nil {
		return origin, err
	}
	enabled := issue.OwnerSelection != nil && issue.OwnerSelection.OwnershipPolicy == protocol.IssueOwnerPolicy
	if issue.OwnerSelection == nil {
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM issue_ownership_policies WHERE issue_id=?)`, issue.IssueID).Scan(&enabled); err != nil {
			return origin, err
		}
	}
	if !enabled {
		return origin, ErrNotFound
	}

	origin.TaskID, origin.IssueRevision, origin.AssignmentEpoch, origin.AgentRef = task, revision, issue.AssignmentEpoch, issue.Assignee.Ref
	return origin, nil
}

func RunIssueResponsibilityCurrent(ctx context.Context, q Querier, run string) (bool, error) {
	var current bool
	err := q.QueryRowContext(ctx, `SELECT NOT EXISTS(SELECT 1 FROM run_issue_responsibilities r JOIN issue_controls i USING(issue_id) WHERE r.run_id=? AND (i.assignment_epoch<>r.assignment_epoch OR i.assignee_ref<>r.agent_ref OR i.assignment_state<>'active' OR i.state NOT IN ('active','paused')))`, run).Scan(&current)
	return current, err
}

// ReviewAdmissionCurrent permits execution admission changes without rewriting
// the frozen review contract. Owner edits and transfers still require a new
// responsibility; technical initialization is not an owner edit.
func ReviewAdmissionCurrent(ctx context.Context, q Querier, contract TaskOwnerContract, issue protocol.Issue) (bool, error) {
	if issue.AssignmentEpoch != 1 || issue.Assignee.Ref != contract.AgentRef || issue.AssignmentState != "active" || issue.Revision < contract.ReviewAdmissionRevision {
		return false, nil
	}
	var current bool
	err := q.QueryRowContext(ctx, `SELECT NOT EXISTS(SELECT 1 FROM issue_control_revisions v WHERE v.issue_id=? AND v.revision>? AND v.revision<=? AND v.operation NOT IN ('system.initialize_issue_admission','issues.set_status','issues.pause','issues.resume'))`, issue.IssueID, contract.ReviewAdmissionRevision, issue.Revision).Scan(&current)
	if err != nil || !current {
		return current, err
	}
	origin, err := GetCanonicalIssueRevision(ctx, q, issue.IssueID, contract.ReviewAdmissionRevision)
	if err != nil {
		return false, err
	}
	before, err := json.Marshal(origin.OwnerSelection)
	if err != nil {
		return false, err
	}
	after, err := json.Marshal(issue.OwnerSelection)
	return string(before) == string(after), err
}

// IssueHasRetainedExecution derives selection from existing unfinished work.
// It does not create a manager or introduce another execution queue.
func IssueHasRetainedExecution(ctx context.Context, q Querier, issue string) (bool, error) {
	var found bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_execution_contracts c JOIN tasks t USING(task_id) WHERE c.review_issue_id=? AND t.lifecycle NOT IN ('completed','cancelled')) OR EXISTS(SELECT 1 FROM task_work_item_owners o JOIN tasks t USING(task_id) WHERE o.work_item_id=? AND t.lifecycle NOT IN ('completed','cancelled','draft')) OR EXISTS(SELECT 1 FROM issue_manager_tasks m JOIN tasks t USING(task_id) WHERE m.issue_id=? AND m.state IN ('pending','assigned') AND t.lifecycle NOT IN ('completed','cancelled'))`, issue, issue, issue).Scan(&found)
	return found, err
}
