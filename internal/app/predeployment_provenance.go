package app

import (
	"context"
	"encoding/json"

	"github.com/GhostFlying/tend/internal/protocol"
	"github.com/GhostFlying/tend/internal/store"
)

func readPredeploymentArtifact(ctx context.Context, q store.Querier, task store.TaskRow, id string) (protocol.ArtifactPayload, *protocol.PredeploymentResult, error) {
	var payload protocol.ArtifactPayload
	if id == "" {
		return payload, nil, nil
	}
	a, err := store.GetArtifact(ctx, q, id)
	if err != nil {
		return payload, nil, err
	}
	if a.TaskID != task.TaskID {
		return payload, nil, protocol.Errorf(protocol.CodeForbidden, "stage evidence belongs to another Task")
	}
	run, err := store.GetRun(ctx, q, a.RunID)
	if err != nil {
		return payload, nil, err
	}
	action, err := store.GetAction(ctx, q, run.ActionID)
	if err != nil {
		return payload, nil, err
	}
	if task.Kind != protocol.TaskKindIssueManager && action.TargetSpecVersion != task.SpecVersion {
		return payload, nil, protocol.Errorf(protocol.CodeStaleRevision, "stage evidence belongs to another specification")
	}
	if err := json.Unmarshal([]byte(a.RefJSON), &payload); err != nil {
		return payload, nil, err
	}
	if payload.Report == nil {
		return payload, nil, nil
	}
	return payload, payload.Report.Predeployment, nil
}

// The immutable stage and its quality agreement belong to the continuing
// delivery, not to the lifecycle's replaceable current-artifact pointer.
func persistentPredeploymentResult(ctx context.Context, q store.Querier, task store.TaskRow) (string, protocol.ArtifactPayload, *protocol.PredeploymentResult, error) {
	artifacts, err := store.ListArtifactsByTask(ctx, q, task.TaskID)
	if err != nil {
		return "", protocol.ArtifactPayload{}, nil, err
	}
	rows, err := q.QueryContext(ctx, `SELECT artifact_id FROM issue_delivery_execution_history WHERE task_id=? AND json_extract(duty_json,'$.status') IN ('complete','report','ended')`, task.TaskID)
	if err != nil {
		return "", protocol.ArtifactPayload{}, nil, err
	}
	boundaries := map[string]bool{}
	for rows.Next() {
		var id *string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return "", protocol.ArtifactPayload{}, nil, err
		}
		if id == nil {
			rows.Close()
			return "", protocol.ArtifactPayload{}, nil, protocol.Errorf(protocol.CodeUnknownOutcome, "the settled delivery's original artifact is unavailable")
		}
		boundaries[*id] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return "", protocol.ArtifactPayload{}, nil, err
	}
	// Creation order is the store's artifact ordering contract. Do not use
	// wall-clock cutoffs: several real transitions can have the same time.
	first := 0
	for n, artifact := range artifacts {
		if boundaries[artifact.ArtifactID] {
			first = n + 1
			delete(boundaries, artifact.ArtifactID)
		}
	}
	if len(boundaries) != 0 {
		return "", protocol.ArtifactPayload{}, nil, protocol.Errorf(protocol.CodeUnknownOutcome, "reconcile the settled delivery's original artifact before using a later stage")
	}
	for n := len(artifacts) - 1; n >= first; n-- {
		id := artifacts[n].ArtifactID
		payload, stage, err := readPredeploymentArtifact(ctx, q, task, id)
		if protocol.CodeOf(err) == protocol.CodeStaleRevision {
			continue
		}
		if err != nil || stage != nil {
			return id, payload, stage, err
		}
	}
	return "", protocol.ArtifactPayload{}, nil, nil
}

// A negative verdict permits correction work, never installation.
func preparationStage(ctx context.Context, q store.Querier, task store.TaskRow) (string, *protocol.PredeploymentResult, bool, error) {
	id, _, stage, err := persistentPredeploymentResult(ctx, q, task)
	if err != nil || stage == nil {
		return id, stage, false, err
	}
	accs, err := store.ListAcceptances(ctx, q, task.TaskID, id)
	if err != nil {
		return id, stage, false, err
	}
	for n := len(accs) - 1; n >= 0; n-- {
		if accs[n].SpecVersion == task.SpecVersion {
			return id, stage, accs[n].Verdict != protocol.VerdictPassed, nil
		}
	}
	return id, stage, false, nil
}

// Installation-report repairs keep the most recent stage for this frozen
// candidate, not whichever artifact happens to be the current task pointer.
// An unaccepted matching stage must not fall back to an older passed one.
func acceptedStageForCandidate(ctx context.Context, q store.Querier, task store.TaskRow, candidate protocol.IssueDeliveryCandidate) (protocol.ArtifactPayload, *protocol.PredeploymentResult, error) {
	id, payload, stage, err := persistentPredeploymentResult(ctx, q, task)
	if err != nil || stage == nil {
		return payload, stage, err
	}
	if stage.DeliveryID != candidate.DeliveryID || stage.HeadSHA != candidate.HeadSHA || stage.BaseSHA != candidate.BaseSHA {
		return payload, stage, protocol.Errorf(protocol.CodeInvalidTransition, "the latest stage must match the frozen delivery candidate")
	}
	_, err = acceptedPredeploymentArtifact(ctx, q, task, id)
	return payload, stage, err
}

func requireInstallationStage(ctx context.Context, q store.Querier, task store.TaskRow, candidate protocol.IssueDeliveryCandidate) error {
	id, _, stage, err := persistentPredeploymentResult(ctx, q, task)
	if err != nil || stage == nil {
		return err
	}
	if stage.DeliveryID != candidate.DeliveryID || stage.HeadSHA != candidate.HeadSHA || stage.BaseSHA != candidate.BaseSHA {
		return protocol.Errorf(protocol.CodeInvalidTransition, "finish the candidate correction and bind its accepted stage before installation")
	}
	_, err = acceptedPredeploymentArtifact(ctx, q, task, id)
	return err
}
