package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/GhostFlying/tend/internal/protocol"
)

// QuestionHandoff is an execution boundary, independent of task completion.
type QuestionHandoff struct {
	RunID, DecisionID, ThreadID, SuccessorRunID string
	Stopped                                     bool
}

func GetQuestionHandoff(ctx context.Context, q Querier, run string) (*QuestionHandoff, error) {
	var h QuestionHandoff
	err := q.QueryRowContext(ctx, `SELECT run_id,decision_id,thread_id,stopped,COALESCE(successor_run_id,'') FROM run_question_handoffs WHERE run_id=?`, run).Scan(&h.RunID, &h.DecisionID, &h.ThreadID, &h.Stopped, &h.SuccessorRunID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &h, err
}

func PendingQuestionHandoff(ctx context.Context, q Querier, action string) (*QuestionHandoff, error) {
	var run string
	err := q.QueryRowContext(ctx, `SELECT h.run_id FROM run_question_handoffs h JOIN runs r USING(run_id) WHERE h.continuation_action_id=? AND h.stopped=1 AND h.successor_run_id IS NULL ORDER BY r.created_at DESC,r.rowid DESC LIMIT 1`, action).Scan(&run)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return GetQuestionHandoff(ctx, q, run)
}

// QuestionHandoffAgreementCurrent checks the original action and execution
// contract independently of a later, explicit responsibility adoption.
func QuestionHandoffAgreementCurrent(ctx context.Context, q Querier, run string) (bool, error) {
	var valid bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM run_question_handoffs h JOIN runs r USING(run_id) JOIN plan_actions a ON a.action_id=COALESCE(h.continuation_action_id,r.action_id) JOIN tasks t ON t.task_id=r.task_id WHERE h.run_id=? AND h.successor_run_id IS NULL AND r.desired_state='run' AND (r.observed_state NOT IN ('finished','failed') OR (r.observed_state='finished' AND h.stopped=1)) AND a.status IN ('pending','dispatched') AND a.target_spec_version=t.spec_version AND t.lifecycle NOT IN ('completed','cancelled') AND (a.kind<>'implement' OR a.generation=t.implement_generation) AND (a.kind<>'accept' OR a.target_artifact_id=t.current_artifact_id))`, run).Scan(&valid)
	return valid, err
}

// QuestionHandoffCurrent also checks the effective current execution responsibility.
func QuestionHandoffCurrent(ctx context.Context, q Querier, run string) (bool, error) {
	valid, err := QuestionHandoffAgreementCurrent(ctx, q, run)
	if err != nil || !valid {
		return valid, err
	}
	owned, err := RunIssueResponsibilityCurrent(ctx, q, run)
	if err != nil {
		return false, err
	}
	if !owned {
		owned, err = AdoptedQuestionResponsibilityCurrent(ctx, q, run)
		if err != nil || !owned {
			return false, err
		}
	}
	r, err := GetRun(ctx, q, run)
	if err != nil {
		return false, err
	}
	return TaskOwnerContractCurrent(ctx, q, r.TaskID)
}

// QuestionContinuationForRun carries every answered handoff into a fresh brief.
func QuestionContinuationForRun(ctx context.Context, q Querier, run string) (*protocol.QuestionContinuation, []protocol.WorkerQuestion, error) {
	var parent string
	err := q.QueryRowContext(ctx, `SELECT run_id FROM run_question_handoffs WHERE successor_run_id=?`, run).Scan(&parent)
	if errors.Is(err, sql.ErrNoRows) {
		var paused string
		err = q.QueryRowContext(ctx, `SELECT run_id FROM run_pause_handoffs WHERE successor_run_id=? AND stopped=1`, run).Scan(&paused)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, nil
		}
		if err != nil {
			return nil, nil, err
		}
		_, questions, err := QuestionContinuationForRun(ctx, q, paused)
		return nil, questions, err
	}
	if err != nil {
		return nil, nil, err
	}
	h, err := GetQuestionHandoff(ctx, q, parent)
	if err != nil {
		return nil, nil, err
	}
	continuation := &protocol.QuestionContinuation{RunID: parent, ThreadID: h.ThreadID}
	var questions []protocol.WorkerQuestion
	for parent != "" {
		h, err := GetQuestionHandoff(ctx, q, parent)
		if err != nil {
			return nil, nil, err
		}
		question, err := WorkerQuestion(ctx, q, h.DecisionID)
		if err != nil {
			return nil, nil, err
		}
		questions = append([]protocol.WorkerQuestion{question}, questions...)
		err = q.QueryRowContext(ctx, `SELECT run_id FROM run_question_handoffs WHERE successor_run_id=?`, parent).Scan(&parent)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return nil, nil, err
		}
	}
	return continuation, questions, nil
}
