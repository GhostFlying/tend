package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
)

// DecisionContinuationRow is independent of the immutable answer record.
type DecisionContinuationRow struct {
	DecisionID, State, AnswerRequestID, ResumeRequestID string
	Revision, CreatedAt, UpdatedAt                      int64
	Policy                                              protocol.DecisionContinuationPolicy
}

func GetDecisionContinuation(ctx context.Context, q Querier, id string) (*DecisionContinuationRow, error) {
	var row DecisionContinuationRow
	var raw string
	err := q.QueryRowContext(ctx, `SELECT decision_id,revision,policy_json,state,answer_request_id,resume_request_id,created_at,updated_at FROM decision_continuations WHERE decision_id=?`, id).
		Scan(&row.DecisionID, &row.Revision, &raw, &row.State, &row.AnswerRequestID, &row.ResumeRequestID, &row.CreatedAt, &row.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: read decision continuation: %w", err)
	}
	if err := json.Unmarshal([]byte(raw), &row.Policy); err != nil {
		return nil, fmt.Errorf("store: decode decision continuation: %w", err)
	}
	return &row, nil
}

func (tx *Tx) PutDecisionContinuation(ctx context.Context, row DecisionContinuationRow) error {
	raw, err := json.Marshal(row.Policy)
	if err != nil {
		return fmt.Errorf("store: encode decision continuation: %w", err)
	}
	var task any
	var spec any
	if row.Policy.Dependency != nil {
		task, spec = row.Policy.Dependency.TaskID, row.Policy.Dependency.SpecVersion
	}
	_, err = tx.Exec(ctx, `INSERT INTO decision_continuations(decision_id,revision,mode,policy_json,state,dependency_task_id,dependency_spec_version,answer_request_id,resume_request_id,created_at,updated_at)
 VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(decision_id) DO UPDATE SET revision=excluded.revision,state=excluded.state,resume_request_id=excluded.resume_request_id,updated_at=excluded.updated_at`,
		row.DecisionID, row.Revision, row.Policy.Mode, string(raw), row.State, task, spec, row.AnswerRequestID, row.ResumeRequestID, row.CreatedAt, row.UpdatedAt)
	return err
}

// DecisionDependencyReady reads authoritative completion, never caller text.
func DecisionDependencyReady(ctx context.Context, q Querier, dependency *protocol.DecisionContinuationDependency) (bool, string, error) {
	if dependency == nil {
		return false, "dependency_missing", nil
	}
	task, err := GetTask(ctx, q, dependency.TaskID)
	if errors.Is(err, ErrNotFound) {
		return false, "dependency_missing", nil
	}
	if err != nil {
		return false, "", err
	}
	if task.Kind != protocol.TaskKindExecution {
		return false, "dependency_kind_unsupported", nil
	}
	if task.SpecVersion != dependency.SpecVersion {
		return false, "dependency_spec_changed", nil
	}
	if task.Lifecycle == protocol.TaskCancelled {
		return false, "dependency_cancelled", nil
	}
	if task.Lifecycle != protocol.TaskCompleted {
		return false, "dependency_not_completed", nil
	}
	return true, "", nil
}

// ReadDecisionContinuation preserves the original legacy contract without inventing a policy;
// it does not materialize a policy or replay an old business request.
func ReadDecisionContinuation(ctx context.Context, q Querier, id string) (protocol.DecisionContinuationView, error) {
	v := protocol.DecisionContinuationView{DecisionID: id, State: "legacy", ResumeCondition: "legacy_answer_and_stopped"}
	var stopped bool
	err := q.QueryRowContext(ctx, `SELECT h.run_id,r.task_id,h.stopped,COALESCE(h.continuation_action_id,''),COALESCE(h.successor_run_id,'') FROM run_question_handoffs h JOIN runs r USING(run_id) WHERE h.decision_id=?`, id).
		Scan(&v.RunID, &v.TaskID, &stopped, &v.ContinuationActionID, &v.SuccessorRunID)
	if err != nil {
		return v, notFound(err, "question continuation")
	}
	d, err := GetDecision(ctx, q, id)
	if err != nil {
		return v, err
	}
	v.DecisionVersion = d.Version
	task, err := GetTask(ctx, q, v.TaskID)
	if err != nil {
		return v, err
	}
	v.TaskControlRevision = task.ControlRevision
	primary, err := GetTaskIssueOwner(ctx, q, v.TaskID)
	if err != nil {
		return v, err
	}
	if primary != "" {
		issue, err := GetCanonicalIssue(ctx, q, primary)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return v, err
		}
		if err == nil {
			v.CurrentAssignmentEpoch = issue.AssignmentEpoch
			current, err := TaskOwnerContractCurrent(ctx, q, v.TaskID)
			if err != nil {
				return v, err
			}
			owned, err := RunIssueResponsibilityCurrent(ctx, q, v.RunID)
			if err != nil {
				return v, err
			}
			adopted, err := AdoptedQuestionResponsibilityCurrent(ctx, q, v.RunID)
			if err != nil {
				return v, err
			}
			v.ResponsibilityAdoptionRequired = !current || (!owned && !adopted)
		}
	}
	row, err := GetDecisionContinuation(ctx, q, id)
	if err != nil {
		return v, err
	}
	if row != nil {
		v.Revision, v.State = row.Revision, row.State
		policy := row.Policy
		v.Policy = &policy
		v.ResumeCondition = "explicit_resume"
		v.AnswerRequestID, v.ResumeRequestID = row.AnswerRequestID, row.ResumeRequestID
	}
	if v.Policy != nil && v.Policy.Mode == protocol.DecisionDependency {
		v.ResumeCondition = "task_completed_at_spec"
	} else if v.Policy != nil && v.Policy.Mode == protocol.DecisionContinueNow {
		v.ResumeCondition = "original_handoff_stopped_and_current"
	}
	if v.ContinuationActionID != "" || v.SuccessorRunID != "" {
		v.State = "continued"
		if v.SuccessorRunID == "" && v.Policy != nil && v.Policy.Mode == protocol.DecisionDependency {
			_, v.BlockedReason, err = DecisionDependencyReady(ctx, q, v.Policy.Dependency)
		}
		return v, err
	}
	if d.Status != DecisionAnswered {
		v.BlockedReason = "answer_pending"
		return v, nil
	}
	if row == nil {
		if !stopped {
			v.BlockedReason = "original_process_not_stopped"
		} else {
			v.BlockedReason = "legacy_contract_without_successor"
		}
		return v, nil
	}
	if v.Policy != nil && v.Policy.Mode == protocol.DecisionDependency {
		ready, reason, err := DecisionDependencyReady(ctx, q, v.Policy.Dependency)
		if err != nil {
			return v, err
		}
		if !ready {
			v.BlockedReason = reason
			return v, nil
		}
	}
	if v.State == "waiting" {
		v.BlockedReason = "explicit_resume_required"
		if v.Policy != nil && v.Policy.Mode == protocol.DecisionDependency {
			v.BlockedReason = "dependency_ready_reconcile_required"
		}
		return v, nil
	}
	if !stopped {
		v.BlockedReason = "original_process_not_stopped"
		return v, nil
	}
	valid, err := QuestionHandoffCurrent(ctx, q, v.RunID)
	if err != nil {
		return v, err
	}
	if !valid {
		v.BlockedReason = "original_contract_changed"
	}
	return v, nil
}

// TaskWaitDependencies includes unresolved typed handoffs in the existing wait
// graph. Pausing or waiting for physical exit does not discard scheduling intent.
// A queued continuation uses its new action; the predecessor action is then done.
func TaskWaitDependencies(ctx context.Context, q Querier) (map[string][]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT task_id,depends_on FROM task_dependencies
 UNION
 SELECT r.task_id,c.dependency_task_id FROM decision_continuations c
 JOIN run_question_handoffs h ON h.decision_id=c.decision_id
 JOIN decisions d ON d.decision_id=c.decision_id
 JOIN runs r ON r.run_id=h.run_id
 JOIN tasks t ON t.task_id=r.task_id
 JOIN tasks dep ON dep.task_id=c.dependency_task_id
 JOIN plan_actions a ON a.action_id=COALESCE(h.continuation_action_id,r.action_id)
 WHERE c.mode='dependency' AND c.state IN ('waiting','ready','continued')
 AND d.status='answered' AND h.successor_run_id IS NULL
 AND t.lifecycle NOT IN ('completed','cancelled') AND a.status IN ('pending','dispatched')
 AND a.target_spec_version=t.spec_version
 AND (a.kind<>'implement' OR a.generation=t.implement_generation)
 AND (a.kind<>'accept' OR a.target_artifact_id=t.current_artifact_id)
 AND NOT COALESCE(dep.lifecycle='completed' AND dep.spec_version=c.dependency_spec_version,0)
 AND NOT EXISTS(SELECT 1 FROM task_execution_contracts ec JOIN issue_controls i ON i.issue_id=ec.issue_id
  WHERE ec.task_id=t.task_id AND (i.assignment_epoch<>ec.assignment_epoch OR i.assignee_ref<>ec.agent_ref OR i.assignment_state<>'active'))
 AND NOT EXISTS(SELECT 1 FROM run_issue_responsibilities rr JOIN issue_controls i ON i.issue_id=rr.issue_id
  WHERE rr.run_id=r.run_id AND (i.assignment_epoch<>rr.assignment_epoch OR i.assignee_ref<>rr.agent_ref OR i.assignment_state<>'active' OR i.state NOT IN ('active','paused')))`)
	if err != nil {
		return nil, fmt.Errorf("store: read task wait dependencies: %w", err)
	}
	defer rows.Close()
	deps := map[string][]string{}
	for rows.Next() {
		var task, dep string
		if err := rows.Scan(&task, &dep); err != nil {
			return nil, fmt.Errorf("store: scan task wait dependency: %w", err)
		}
		deps[task] = append(deps[task], dep)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list task wait dependencies: %w", err)
	}
	return deps, nil
}
