package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

const taskExecutionContextLimit = 10

// workerSpecForRun rebuilds the current execution from durable facts even when
// no native session survives. It does not revise the frozen task agreement.
func workerSpecForRun(ctx context.Context, q store.Querier, run store.RunRow, action store.ActionRow, task store.TaskRow, spec domain.Spec) (json.RawMessage, error) {
	index, err := taskExecutionContext(ctx, q, run, action)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(index)
	if err != nil {
		return nil, err
	}
	spec.Scope += fmt.Sprintf("\n\nCurrent execution context reconstructed from the ledger (Task %s, unchanged agreement version %d):\n%s\nUse read_context kind=overview, tasks, artifacts, acceptances, questions, reviews and delivery_authorization for current requirements, authorization, original results and unresolved facts. Read saved originals by ID; this bounded index is not every report or check. more=true means older facts exist; use exact IDs or bounded artifact/acceptance/question pages instead of loading every report. Previous Run failures or unknown outcomes remain facts to reconcile, never permission to repeat an external mutation. Session reuse is optional: continue from these durable records when starting fresh. A source or candidate change does not by itself revise this Task's goal. For a same-goal recheck, the current bound review target supplies the candidate; historical candidate IDs in earlier reports do not force a new spec or permit broader scope.\n", task.TaskID, action.TargetSpecVersion, raw)
	var wait string
	err = q.QueryRowContext(ctx, `SELECT body_json FROM run_external_waits WHERE successor_run_id=?`, run.RunID).Scan(&wait)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		spec.Scope += "\nThis execution continues the original Task after a physically stopped external wait. Read the original target, observed facts, wake reason and recovery position below. Resumption grants no additional authorization and does not replay an unknown operation. Native session history is optional.\n" + wait + "\n"
	}
	return workerSpec(spec, run.Role)
}

func taskExecutionContext(ctx context.Context, q store.Querier, run store.RunRow, action store.ActionRow) (protocol.TaskExecutionContext, error) {
	out := protocol.TaskExecutionContext{Generation: action.Generation, Runs: []protocol.RunView{}, Artifacts: []protocol.TaskResultReference{}, Reviews: []protocol.TaskReviewTarget{}}
	var trigger struct {
		Reason             string `json:"reason"`
		PreviousArtifactID string `json:"previous_artifact_id"`
	}
	if err := json.Unmarshal([]byte(action.TriggerJSON), &trigger); err != nil {
		return out, fmt.Errorf("decode execution continuation: %w", err)
	}
	out.NextStep, out.PreviousArtifactID = trigger.Reason, trigger.PreviousArtifactID
	rows, err := q.QueryContext(ctx, `SELECT r.run_id,r.role,a.generation,a.target_spec_version,r.host_id,r.observed_state FROM runs r JOIN plan_actions a USING(action_id) WHERE r.task_id=? AND r.run_id<>? ORDER BY r.created_at DESC,r.run_id DESC LIMIT ?`, run.TaskID, run.RunID, taskExecutionContextLimit+1)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var v protocol.RunView
		if err := rows.Scan(&v.RunID, &v.Role, &v.Generation, &v.SpecVersion, &v.Host, &v.Observed); err != nil {
			rows.Close()
			return out, err
		}
		if len(out.Runs) < taskExecutionContextLimit {
			out.Runs = append(out.Runs, v)
		} else {
			out.More = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	rows, err = q.QueryContext(ctx, `SELECT f.artifact_id,f.run_id,a.target_spec_version,a.generation,f.digest FROM artifacts f JOIN runs r USING(run_id) JOIN plan_actions a USING(action_id) WHERE f.task_id=? ORDER BY f.created_at DESC,f.artifact_id DESC LIMIT ?`, run.TaskID, taskExecutionContextLimit+1)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var v protocol.TaskResultReference
		if err := rows.Scan(&v.ArtifactID, &v.RunID, &v.SpecVersion, &v.Generation, &v.SHA256); err != nil {
			rows.Close()
			return out, err
		}
		if len(out.Artifacts) < taskExecutionContextLimit {
			out.Artifacts = append(out.Artifacts, v)
		} else {
			out.More = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	rows, err = q.QueryContext(ctx, `SELECT DISTINCT s.body_json FROM shared_delivery_reviews s JOIN shared_delivery_review_links l USING(review_id) JOIN task_work_item_owners o ON o.work_item_id=l.issue_id WHERE o.task_id=? ORDER BY s.review_id LIMIT ?`, run.TaskID, taskExecutionContextLimit+1)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return out, err
		}
		var r protocol.SharedDeliveryReview
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			rows.Close()
			return out, err
		}
		if len(out.Reviews) < taskExecutionContextLimit {
			out.Reviews = append(out.Reviews, protocol.TaskReviewTarget{ReviewID: r.ReviewID, Provider: r.Provider, Repository: r.Repository, Number: r.Number, Revision: r.Revision, Status: r.Status, HeadSHA: r.HeadSHA, BaseSHA: r.BaseSHA, RequirementsDigest: r.RequirementsDigest, Reviewer: r.Reviewer})
		} else {
			out.More = true
		}
	}
	err = rows.Err()
	rows.Close()
	return out, err
}
