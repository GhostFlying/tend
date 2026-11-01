package views

import (
	"context"
	"encoding/json"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type AttentionFilter struct {
	IssueID, Kind, Status, After string
	IncludeArchived              bool
	ActionableOnly               bool
	Limit                        int
}

func Attention(ctx context.Context, q store.Querier, id string) (protocol.AttentionView, error) {
	return (&attentionReader{}).read(ctx, q, id)
}

// A page shares scheduling facts instead of rereading the full scheduler inputs
// for every linked task. Standalone reads initialize them only when needed.
type attentionReader struct {
	execution *executionFacts
}

func (reader *attentionReader) read(ctx context.Context, q store.Querier, id string) (protocol.AttentionView, error) {
	a, err := store.GetAttention(ctx, q, id)
	if err != nil {
		return protocol.AttentionView{}, err
	}
	v := protocol.AttentionView{Attention: a}
	var taskRevision int64
	if a.IssueID != "" {
		issue, err := store.GetIssue(ctx, q, a.IssueID)
		if err != nil {
			return v, err
		}
		v.IssueTitle = issue.Title
		v.IssueIntent = issue.Intent
	}
	if a.TaskID != "" {
		task, err := store.GetTask(ctx, q, a.TaskID)
		if err != nil {
			return v, err
		}
		v.TaskTitle = task.Title
		taskRevision = task.ControlRevision
		raw, err := store.GetSpec(ctx, q, task.TaskID, task.SpecVersion)
		if err != nil {
			return v, err
		}
		var spec struct {
			Goal string `json:"goal"`
		}
		if err := json.Unmarshal([]byte(raw), &spec); err != nil {
			return v, err
		}
		taskContext := &protocol.AttentionTaskContext{
			Goal: spec.Goal, Lifecycle: task.Lifecycle, ControlMode: task.ControlMode,
			BlockReason: task.BlockReason,
		}
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artifacts a JOIN runs r USING(run_id) JOIN plan_actions p USING(action_id) WHERE a.task_id=? AND p.target_spec_version=?)`, task.TaskID, task.SpecVersion).Scan(&taskContext.HasCurrentArtifact); err != nil {
			return v, err
		}
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM acceptances WHERE task_id=? AND spec_version=? AND artifact_id=? AND verdict='passed')`, task.TaskID, task.SpecVersion, task.CurrentArtifactID).Scan(&taskContext.CurrentArtifactAccepted); err != nil {
			return v, err
		}
		if reader.execution == nil {
			facts, err := readExecutionFacts(ctx, q)
			if err != nil {
				return v, err
			}
			reader.execution = &facts
		}
		taskContext.Execution, err = reader.execution.task(ctx, q, task)
		if err != nil {
			return v, err
		}
		v.TaskContext = taskContext
	}
	if a.DecisionID != "" {
		d, e := store.GetDecision(ctx, q, a.DecisionID)
		if e != nil {
			return v, e
		}
		value := Decision(d)
		v.Decision = &value
		if v.TaskContext != nil {
			v.TaskContext.QuestionCurrent = d.AskedRevision.Valid && d.AskedRevision.Int64 == taskRevision
		}
	}
	if a.ChoiceID != "" {
		d, e := store.GetWorkItemChoice(ctx, q, a.ChoiceID)
		if e != nil {
			return v, e
		}
		v.Choice = &d
	}
	return v, nil
}
func Attentions(ctx context.Context, q store.Querier, f AttentionFilter) (protocol.AttentionPage, error) {
	if f.Limit < 1 || f.Limit > 100 {
		f.Limit = 50
	}
	rows, err := q.QueryContext(ctx, `SELECT body_json FROM attentions WHERE (?='' OR issue_id=?) AND (?='' OR kind=?) AND (?='' OR status=?) AND attention_id>? AND (? OR COALESCE(json_extract(body_json,'$.archived_at'),0)=0) AND (NOT ? OR kind IN ('decision','action')) ORDER BY attention_id LIMIT ?`, f.IssueID, f.IssueID, f.Kind, f.Kind, f.Status, f.Status, f.After, f.IncludeArchived, f.ActionableOnly, f.Limit+1)
	if err != nil {
		return protocol.AttentionPage{}, err
	}
	var ids []string
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return protocol.AttentionPage{}, err
		}
		var a protocol.Attention
		if err := json.Unmarshal([]byte(raw), &a); err != nil {
			rows.Close()
			return protocol.AttentionPage{}, err
		}
		ids = append(ids, a.AttentionID)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return protocol.AttentionPage{}, err
	}
	page := protocol.AttentionPage{Items: []protocol.AttentionView{}}
	if len(ids) > f.Limit {
		page.More = true
		ids = ids[:f.Limit]
		page.Next = ids[len(ids)-1]
	}
	reader := attentionReader{}
	for _, id := range ids {
		v, e := reader.read(ctx, q, id)
		if e != nil {
			return page, e
		}
		page.Items = append(page.Items, v)
	}
	return page, nil
}
