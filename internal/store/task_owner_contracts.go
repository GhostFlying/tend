package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
)

type TaskOwnerContract struct {
	Execution                                                          *protocol.RunExecutionPin
	TaskID, IssueID, AgentRef, RuntimeID, Model, Effort, ReviewIssueID string
	ReviewPlanJSON                                                     string
	AssignmentEpoch, AgentRevision, RuntimeRevision                    int64
}

func GetTaskOwnerContract(ctx context.Context, q Querier, task string) (TaskOwnerContract, error) {
	var c TaskOwnerContract
	var raw string
	err := q.QueryRowContext(ctx, `SELECT task_id,issue_id,assignment_epoch,agent_ref,agent_revision,runtime_id,runtime_revision,model,effort,review_issue_id,review_plan_json,COALESCE(execution_pin_json,'') FROM task_execution_contracts WHERE task_id=?`, task).Scan(&c.TaskID, &c.IssueID, &c.AssignmentEpoch, &c.AgentRef, &c.AgentRevision, &c.RuntimeID, &c.RuntimeRevision, &c.Model, &c.Effort, &c.ReviewIssueID, &c.ReviewPlanJSON, &raw)
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
	var current bool
	err := q.QueryRowContext(ctx, `SELECT NOT EXISTS(SELECT 1 FROM task_execution_contracts c JOIN issue_controls i ON i.issue_id=c.issue_id WHERE c.task_id=? AND (i.assignment_epoch<>c.assignment_epoch OR i.assignee_ref<>c.agent_ref OR i.assignment_state<>'active'))`, task).Scan(&current)
	return current, err
}

func RunIssueResponsibilityCurrent(ctx context.Context, q Querier, run string) (bool, error) {
	var current bool
	err := q.QueryRowContext(ctx, `SELECT NOT EXISTS(SELECT 1 FROM run_issue_responsibilities r JOIN issue_controls i USING(issue_id) WHERE r.run_id=? AND (i.assignment_epoch<>r.assignment_epoch OR i.assignee_ref<>r.agent_ref OR i.assignment_state<>'active' OR i.state NOT IN ('active','paused')))`, run).Scan(&current)
	return current, err
}
