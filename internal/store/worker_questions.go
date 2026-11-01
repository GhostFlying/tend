package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/GhostFlying/tend/internal/protocol"
)

// WorkerQuestionRow captures immutable question provenance and dedup identity.
type WorkerQuestionRow struct {
	Issues                                                          []protocol.DecisionIssue
	DecisionID, RunID, ActionID, QuestionKey, PayloadHash, Evidence string
	SpecVersion                                                     int64
}

func (t *Tx) InsertWorkerQuestion(ctx context.Context, w WorkerQuestionRow) error {
	raw, err := json.Marshal(w.Issues)
	if err != nil {
		return fmt.Errorf("store: encode question issues: %w", err)
	}
	_, err = t.Exec(ctx, `INSERT INTO worker_questions(decision_id,run_id,action_id,spec_version,question_key,payload_hash,evidence,issues_json) VALUES(?,?,?,?,?,?,?,?)`, w.DecisionID, w.RunID, w.ActionID, w.SpecVersion, w.QuestionKey, w.PayloadHash, w.Evidence, string(raw))
	if err != nil {
		return fmt.Errorf("store: insert worker question: %w", err)
	}
	return nil
}

func GetWorkerQuestionRow(ctx context.Context, q Querier, decisionID string) (WorkerQuestionRow, error) {
	var w WorkerQuestionRow
	var issues string
	err := q.QueryRowContext(ctx, `SELECT decision_id,run_id,action_id,spec_version,question_key,payload_hash,evidence,issues_json FROM worker_questions WHERE decision_id=?`, decisionID).Scan(&w.DecisionID, &w.RunID, &w.ActionID, &w.SpecVersion, &w.QuestionKey, &w.PayloadHash, &w.Evidence, &issues)
	if err != nil {
		return w, notFound(err, "worker question")
	}
	if err := json.Unmarshal([]byte(issues), &w.Issues); err != nil {
		return w, fmt.Errorf("store: decode question issues: %w", err)
	}
	return w, nil
}

func GetWorkerQuestionByKey(ctx context.Context, q Querier, runID, key string) (WorkerQuestionRow, error) {
	var id string
	if err := q.QueryRowContext(ctx, `SELECT decision_id FROM worker_questions WHERE run_id=? AND question_key=?`, runID, key).Scan(&id); err != nil {
		return WorkerQuestionRow{}, notFound(err, "worker question key")
	}
	return GetWorkerQuestionRow(ctx, q, id)
}

func WorkerQuestion(ctx context.Context, q Querier, id string) (protocol.WorkerQuestion, error) {
	w, err := GetWorkerQuestionRow(ctx, q, id)
	if err != nil {
		return protocol.WorkerQuestion{}, err
	}
	d, err := GetDecision(ctx, q, id)
	if err != nil {
		return protocol.WorkerQuestion{}, err
	}
	out := protocol.WorkerQuestion{DecisionID: id, RunID: w.RunID, ActionID: w.ActionID, SpecVersion: w.SpecVersion, QuestionKey: w.QuestionKey, Question: d.Question, Evidence: w.Evidence, Issues: w.Issues, Version: d.Version, Status: d.Status, QuestionMessageID: d.AskedMessageID}
	if d.Worker != nil {
		out.Role = d.Worker.Role
	}
	if err := json.Unmarshal([]byte(d.OptionsJSON), &out.Options); err != nil {
		return out, fmt.Errorf("store: decode worker question options: %w", err)
	}
	if d.AnswerJSON != "" {
		out.Answer = json.RawMessage(d.AnswerJSON)
	}
	return out, nil
}

func ListWorkerQuestions(ctx context.Context, q Querier, runID string) ([]protocol.WorkerQuestion, error) {
	rows, err := q.QueryContext(ctx, `SELECT w.decision_id FROM worker_questions w JOIN decisions d ON d.decision_id=w.decision_id WHERE w.run_id=? ORDER BY d.created_at,d.decision_id`, runID)
	if err != nil {
		return nil, fmt.Errorf("store: list worker questions: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := make([]protocol.WorkerQuestion, 0, len(ids))
	for _, id := range ids {
		w, err := WorkerQuestion(ctx, q, id)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, nil
}

// CancelObsoleteWorkerQuestions preserves answers as history, but retires open
// questions whose run or frozen Issue ownership can no longer continue.
// Pause and human control retain questions under an unchanged responsibility.
func (t *Tx) CancelObsoleteWorkerQuestions(ctx context.Context, now int64) ([]string, error) {
	rows, err := t.Q().QueryContext(ctx, `UPDATE decisions SET status='cancelled',version=version+1,updated_at=? WHERE status='open' AND (decision_id IN (
 SELECT w.decision_id FROM worker_questions w JOIN runs r ON r.run_id=w.run_id JOIN plan_actions a ON a.action_id=w.action_id JOIN tasks t ON t.task_id=r.task_id
 WHERE t.lifecycle IN ('completed','cancelled') OR r.observed_state IN ('finished','failed') OR a.status IN ('cancelled','superseded') OR a.target_spec_version<>t.spec_version
 OR EXISTS(SELECT 1 FROM run_issue_responsibilities owned JOIN issue_controls i USING(issue_id) WHERE owned.run_id=r.run_id AND (owned.assignment_epoch<>i.assignment_epoch OR owned.agent_ref<>i.assignee_ref OR i.assignment_state<>'active' OR i.state NOT IN ('active','paused')))
 ) OR EXISTS(SELECT 1 FROM task_owner_contracts c JOIN issue_controls i ON i.issue_id=c.issue_id WHERE c.task_id=decisions.task_id AND (c.assignment_epoch<>i.assignment_epoch OR c.agent_ref<>i.assignee_ref OR i.assignment_state<>'active'))) RETURNING decision_id`, now)
	if err != nil {
		return nil, fmt.Errorf("store: cancel stale worker questions: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
