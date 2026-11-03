package store

import (
	"context"
	"fmt"
)

// TaskRuleQuestionOrigin is frozen only by a task rule, never reconstructed for
// a legacy question or accepted from a planner's free-form question payload.
type TaskRuleQuestionOrigin struct {
	Format              int    `json:"format"`
	TaskID              string `json:"task_id"`
	SpecVersion         int64  `json:"spec_version"`
	ActionID            string `json:"action_id,omitempty"`
	SourceActionID      string `json:"source_action_id,omitempty"`
	ImplementGeneration int64  `json:"implement_generation,omitempty"`
	ArtifactID          string `json:"artifact_id,omitempty"`
}

// The opening event is append-only and events_object bounds this lookup to the
// exact decision. An action is usable only when its immutable task/spec binding
// agrees with the origin. Control revisions and pause never establish expiry.
const taskRuleQuestionOriginEvidence = `SELECT 1 FROM events e JOIN tasks t ON t.task_id=decisions.task_id
 LEFT JOIN plan_actions a ON a.action_id=json_extract(e.body_json,'$.task_rule_origin.action_id')
  AND a.task_id=t.task_id AND a.target_spec_version=json_extract(e.body_json,'$.task_rule_origin.spec_version')
 WHERE e.object_type='decision' AND e.object_id=decisions.decision_id AND e.kind='decision.opened'
 AND json_type(e.body_json,'$.task_rule_origin.format')='integer'
 AND json_extract(e.body_json,'$.task_rule_origin.format')=1
 AND json_extract(e.body_json,'$.task_rule_origin.task_id')=t.task_id
 AND json_type(e.body_json,'$.task_rule_origin.spec_version')='integer'
 AND json_extract(e.body_json,'$.task_rule_origin.spec_version')>0`

const taskRuleQuestionObsolete = `EXISTS(` + taskRuleQuestionOriginEvidence + `
 AND (json_extract(e.body_json,'$.task_rule_origin.spec_version')<>t.spec_version
 OR (json_type(e.body_json,'$.task_rule_origin.implement_generation')='integer' AND json_extract(e.body_json,'$.task_rule_origin.implement_generation')>0 AND json_extract(e.body_json,'$.task_rule_origin.implement_generation')<>t.implement_generation)
 OR (json_type(e.body_json,'$.task_rule_origin.artifact_id')='text' AND json_extract(e.body_json,'$.task_rule_origin.artifact_id')<>'' AND json_extract(e.body_json,'$.task_rule_origin.artifact_id') IS NOT t.current_artifact_id)
 OR a.status IN ('cancelled','superseded')
 OR (a.kind='implement' AND a.generation<>t.implement_generation)
 OR (a.kind='accept' AND (a.target_artifact_id IS NOT t.current_artifact_id OR EXISTS(SELECT 1 FROM plan_actions newer WHERE newer.task_id=a.task_id AND newer.kind='accept' AND newer.target_spec_version=a.target_spec_version AND newer.target_artifact_id=a.target_artifact_id AND newer.generation>a.generation)))))`

func TaskRuleQuestionObsolete(ctx context.Context, q Querier, decision string) (bool, error) {
	var obsolete bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM decisions WHERE decision_id=? AND asked_revision IS NOT NULL AND `+taskRuleQuestionObsolete+`)`, decision).Scan(&obsolete)
	if err != nil {
		return false, fmt.Errorf("store: read task question applicability: %w", err)
	}
	return obsolete, nil
}

// TaskRuleQuestionApplicability distinguishes recorded provenance from the
// legacy control-revision hint. Unknown provenance is never backfilled.
func TaskRuleQuestionApplicability(ctx context.Context, q Querier, decision string) (known, obsolete bool, err error) {
	err = q.QueryRowContext(ctx, `SELECT asked_revision IS NOT NULL AND EXISTS(`+taskRuleQuestionOriginEvidence+`), asked_revision IS NOT NULL AND `+taskRuleQuestionObsolete+` FROM decisions WHERE decision_id=?`, decision).Scan(&known, &obsolete)
	if err != nil {
		err = fmt.Errorf("store: read task question origin applicability: %w", err)
	}
	return known, obsolete, err
}

// CancelObsoleteTaskRuleQuestions retires only unanswered, provably superseded
// rule questions. Legacy questions and accepted answers remain authoritative.
func (t *Tx) CancelObsoleteTaskRuleQuestions(ctx context.Context, now int64) ([]string, error) {
	rows, err := t.Q().QueryContext(ctx, `UPDATE decisions SET status='cancelled',version=version+1,updated_at=? WHERE status='open' AND asked_revision IS NOT NULL AND `+taskRuleQuestionObsolete+` RETURNING decision_id`, now)
	if err != nil {
		return nil, fmt.Errorf("store: cancel superseded task rule questions: %w", err)
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
