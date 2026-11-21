package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/GhostFlying/tend/internal/domain"
	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

type independentReviewTarget struct {
	key, head, base string
}

func independentReviewPRTarget(ref protocol.IssueDelivery, head, base string) independentReviewTarget {
	out := independentReviewTarget{key: fmt.Sprintf("delivery|%s|%s|%d", ref.Provider, ref.Repository, ref.Number)}
	if validDeliverySHA(head) && validDeliverySHA(base) {
		out.head, out.base = head, base
	}
	return out
}

func declaredIndependentReviewTarget(ctx context.Context, q store.Querier, i protocol.Issue, spec domain.Spec) (independentReviewTarget, error) {
	if spec.ReviewDeliveryID != "" {
		index := slices.IndexFunc(i.Plan.Deliveries, func(d protocol.IssueDelivery) bool { return d.DeliveryID == spec.ReviewDeliveryID })
		if index < 0 {
			return independentReviewTarget{}, nil
		}
		ref := i.Plan.Deliveries[index]
		fact, err := store.GetDeliveryObservationFact(ctx, q, ref)
		if err != nil {
			return independentReviewTarget{}, err
		}
		if fact.Observation.ErrorKind == "" && !fact.Observation.RefreshPending {
			return independentReviewPRTarget(ref, fact.Observation.HeadSHA, fact.Observation.BaseSHA), nil
		}
		return independentReviewPRTarget(ref, "", ""), nil
	}
	if spec.ReviewArtifactID != "" {
		return independentReviewTarget{key: "artifact|" + i.IssueID + "|" + spec.ReviewArtifactID}, nil
	}
	return independentReviewTarget{}, nil
}

// A failure with a known target stays with its original Task until an actual
// retry outcome exists. A fully observed different candidate has its own review.
func canStartIndependentReviewTarget(ctx context.Context, q store.Querier, target independentReviewTarget, task, approvedRoot string) (bool, error) {
	if target.key == "" {
		return true, nil
	}
	var blocked bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM independent_review_run_targets t JOIN independent_review_failures f USING(run_id)
		JOIN independent_review_failures root ON root.run_id=f.root_run_id
		WHERE t.target_key=? AND f.task_id<>? AND root.run_id<>? AND root.resolution_run_id IS NULL
		 AND (t.head_sha='' OR ?='' OR (t.head_sha=? AND t.base_sha=?)))`,
		target.key, task, approvedRoot, target.head, target.head, target.base).Scan(&blocked)
	return !blocked, err
}

// Capture the provider facts used by scheduling before the first Run exists.
// Pending replacements are left untouched for a genuinely new candidate.
func filterIndependentReviewCandidates(ctx context.Context, q store.Querier, rows []store.CandidateRow) ([]store.CandidateRow, map[string]independentReviewTarget, error) {
	targets := make(map[string]independentReviewTarget)
	eligible := rows[:0]
	for _, row := range rows {
		if !strings.Contains(row.SpecJSON, `"review_delivery_id"`) && !strings.Contains(row.SpecJSON, `"review_artifact_id"`) {
			eligible = append(eligible, row)
			continue
		}
		var spec domain.Spec
		if err := json.Unmarshal([]byte(row.SpecJSON), &spec); err != nil {
			return nil, nil, err
		}
		target, err := independentReviewTargetForTask(ctx, q, row.Action.TaskID, spec)
		if err != nil {
			return nil, nil, err
		}
		excludeTask := row.Action.TaskID
		failure, err := pendingIndependentReviewFailure(ctx, q, row.Action.TaskID, protocol.RunRole(row.Action.Kind), row.Action.TargetArtifactID, 0)
		if err != nil {
			return nil, nil, err
		}
		if failure != nil && failure.specVersion != row.Action.TargetSpecVersion {
			if target.key == "" {
				continue
			}
			// Revalidate a revised Task's candidate at claim. A provider
			// rollback can restore the original failed obligation.
			excludeTask = ""
		}
		approvedRoot, original, err := approvedIndependentReviewRetry(ctx, q, row.Action)
		if err != nil {
			return nil, nil, err
		}
		if approvedRoot != "" && original.key != "" && !matchesIndependentReviewRetryTarget(original, target) {
			continue
		}
		ok, err := canStartIndependentReviewTarget(ctx, q, target, excludeTask, approvedRoot)
		if err != nil {
			return nil, nil, err
		}
		if ok {
			targets[row.Action.ActionID] = target
			eligible = append(eligible, row)
		}
	}
	return eligible, targets, nil
}

func independentReviewTargetForTask(ctx context.Context, q store.Querier, task string, spec domain.Spec) (independentReviewTarget, error) {
	primary, err := store.GetTaskIssueOwner(ctx, q, task)
	if errors.Is(err, store.ErrNotFound) {
		return independentReviewTarget{}, nil
	}
	if err != nil {
		return independentReviewTarget{}, err
	}
	issue, err := store.GetCanonicalIssue(ctx, q, primary)
	if errors.Is(err, store.ErrNotFound) {
		return independentReviewTarget{}, nil
	}
	if err != nil {
		return independentReviewTarget{}, err
	}
	return declaredIndependentReviewTarget(ctx, q, issue, spec)
}

func saveIndependentReviewTarget(ctx context.Context, tx *store.Tx, run string, target independentReviewTarget) error {
	if target.key == "" {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO independent_review_run_targets(run_id,target_key,head_sha,base_sha) VALUES(?,?,?,?)
		ON CONFLICT(run_id) DO NOTHING`, run, target.key, target.head, target.base)
	return err
}

func boundIndependentReviewTarget(ctx context.Context, q store.Querier, rc runContext) (independentReviewTarget, error) {
	ref, round, err := boundIndependentReviewRound(ctx, q, rc.task.TaskID, rc.action.TargetSpecVersion, rc.run.Role)
	if err != nil || round == nil {
		return independentReviewTarget{}, err
	}
	return independentReviewPRTarget(ref, round.HeadSHA, round.BaseSHA), nil
}

func boundIndependentReviewRound(ctx context.Context, q store.Querier, task string, spec int64, role protocol.RunRole) (protocol.IssueDelivery, *protocol.SharedDeliveryReviewRound, error) {
	var ref protocol.IssueDelivery
	var raw string
	err := q.QueryRowContext(ctx, `SELECT r.provider,r.repository,r.number,part.value
		FROM shared_delivery_reviews r JOIN json_each(r.body_json, '$.rounds') part
		WHERE (json_extract(part.value,'$.task_id')=? AND json_extract(part.value,'$.spec_version')=?
		    AND COALESCE(json_extract(part.value,'$.reviewer_role'),'implement')=? AND json_extract(part.value,'$.result') IS NULL)
		   OR (json_extract(part.value,'$.supplement_task_id')=? AND json_extract(part.value,'$.supplement_spec_version')=?
		    AND COALESCE(json_extract(part.value,'$.supplement_artifact_id'),'')='' AND ?='implement')
		ORDER BY r.revision DESC,r.review_id,part.key DESC LIMIT 1`,
		task, spec, role, task, spec, role).
		Scan(&ref.Provider, &ref.Repository, &ref.Number, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return ref, nil, nil
	}
	if err != nil {
		return ref, nil, err
	}
	var round protocol.SharedDeliveryReviewRound
	if err := json.Unmarshal([]byte(raw), &round); err != nil {
		return ref, nil, err
	}
	return ref, &round, nil
}

// A registered target remains the same obligation when its first provider
// observation arrives after the failed attempt.
func matchesIndependentReviewRetryTarget(original, current independentReviewTarget) bool {
	return original.key == current.key && (original.head == "" || original == current)
}

func approvedIndependentReviewRetry(ctx context.Context, q store.Querier, action store.ActionRow) (string, independentReviewTarget, error) {
	var root string
	var target independentReviewTarget
	err := q.QueryRowContext(ctx, `SELECT f.root_run_id,COALESCE(t.target_key,''),COALESCE(t.head_sha,''),COALESCE(t.base_sha,'')
		FROM independent_review_failures f JOIN plan_actions approved ON approved.action_id=f.retry_action_id
		LEFT JOIN independent_review_run_targets t ON t.run_id=f.run_id
		WHERE approved.task_id=? AND approved.kind=? AND approved.target_spec_version=?
		 AND COALESCE(approved.target_artifact_id,'')=? AND approved.generation=?`,
		action.TaskID, action.Kind, action.TargetSpecVersion, action.TargetArtifactID, action.Generation).
		Scan(&root, &target.key, &target.head, &target.base)
	if errors.Is(err, sql.ErrNoRows) {
		return "", target, nil
	}
	return root, target, err
}
